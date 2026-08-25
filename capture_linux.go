// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package screencast

import (
	"context"
	"fmt"

	"github.com/go-freedesktop/screencast/internal/x11"
)

// CaptureDisplay starts a stream on a display.
//
// The stream owns its own connection to the X server, so several captures run
// independently and closing one does not disturb another.
//
// Frames come from MIT-SHM ShmGetImage when the server offers the extension
// and the socket can pass a descriptor, which is the case for every local
// Xorg, Xvfb and Xephyr of the last decade; otherwise from core GetImage,
// which is correct but pushes every pixel through the socket. Set
// [Options.ForceGetImage] to take the slow path deliberately.
func CaptureDisplay(ctx context.Context, d Display, opt Options) (*Stream, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}
	c, _, err := dial()
	if err != nil {
		return nil, err
	}
	src, err := newDisplaySource(ctx, c, d, opt)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return newStream(src.g, src.opt), nil
}

// CaptureWindow starts a stream on a single window.
//
// Two X11 facts a consumer must know, because no amount of API can hide them:
//
//   - A window that is not VIEWABLE has no pixels. X11 keeps no offscreen copy
//     of a minimised or fully-obscured window unless a compositing manager has
//     redirected it. Capturing one reports [ErrNotFound] rather than handing
//     back a stale or black frame.
//   - Without a compositing manager, reading a window's pixels reads the
//     FRAMEBUFFER where that window is. Anything overlapping it — another
//     window, a menu — is in the capture, because on a non-composited X server
//     that is genuinely what is on those pixels. Under a compositing manager
//     (which is every modern desktop) each window is redirected to its own
//     offscreen pixmap and the capture is clean.
func CaptureWindow(ctx context.Context, w Window, opt Options) (*Stream, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}
	c, _, err := dial()
	if err != nil {
		return nil, err
	}
	src, err := newWindowSource(ctx, c, w, opt)
	if err != nil {
		_ = c.Close()
		return nil, err
	}
	return newStream(src.g, src.opt), nil
}

// built is a constructed grabber and the options it was resolved with.
type built struct {
	g   *x11Grabber
	opt Options
}

// newDisplaySource builds the grabber for a display capture. The display's
// rectangle is read back from the server when the caller handed in a struct
// that does not carry one, so a Display value from an older enumeration — or
// one built by hand from an ID — still works.
func newDisplaySource(ctx context.Context, c *x11.Conn, d Display, opt Options) (*built, error) {
	if d.Root == 0 || d.Width <= 0 || d.Height <= 0 {
		all, err := displaysOn(ctx, c)
		if err != nil {
			return nil, err
		}
		found, err := (&Content{Displays: all}).Display(d.ID)
		if err != nil {
			return nil, err
		}
		d = found
	}
	setup := c.Setup()
	sc := setup.ScreenOf(d.Screen)
	if sc == nil || sc.Root != d.Root {
		i, ok := setup.ScreenOfRoot(d.Root)
		if !ok {
			return nil, fmt.Errorf("%w: display %d names root window %#x, which is not a "+
				"screen of this server", ErrNotFound, d.ID, d.Root)
		}
		sc = setup.ScreenOf(i)
	}
	visual := sc.RootVisualType()
	name := d.Name
	if name == "" {
		name = fmt.Sprintf("%d", d.ID)
	}
	return newSource(c, sourceSpec{
		drawable: d.Root,
		x:        int16(d.Frame.X),
		y:        int16(d.Frame.Y),
		w:        d.Width,
		h:        d.Height,
		rootX:    int(d.Frame.X),
		rootY:    int(d.Frame.Y),
		depth:    sc.RootDepth,
		visual:   visual,
		source:   "display " + name,
	}, opt)
}

// newWindowSource builds the grabber for a window capture.
func newWindowSource(ctx context.Context, c *x11.Conn, w Window, opt Options) (*built, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	attr, err := c.GetWindowAttributes(w.ID)
	if err != nil {
		return nil, mapXError(err)
	}
	if attr.Class == x11.WindowClassInputOnly {
		return nil, fmt.Errorf("%w: window %#x is InputOnly and has no pixels at all",
			ErrNotFound, w.ID)
	}
	if !attr.Viewable() {
		return nil, fmt.Errorf("%w: window %#x is not viewable (map state %d); X11 keeps no "+
			"offscreen copy of an unmapped window unless a compositing manager redirected it",
			ErrNotFound, w.ID, attr.MapState)
	}
	g, err := c.GetGeometry(w.ID)
	if err != nil {
		return nil, mapXError(err)
	}
	root := g.Root
	setup := c.Setup()
	si, ok := setup.ScreenOfRoot(root)
	if !ok {
		return nil, fmt.Errorf("%w: window %#x reports root %#x, which is not a screen of "+
			"this server", ErrNotFound, w.ID, root)
	}
	sc := setup.ScreenOf(si)
	visual, ok := sc.FindVisual(attr.Visual)
	if !ok {
		visual = sc.RootVisualType()
	}
	rootX, rootY := 0, 0
	if dx, dy, _, err := c.TranslateCoordinates(w.ID, root, 0, 0); err == nil {
		rootX, rootY = int(dx), int(dy)
	}
	return newSource(c, sourceSpec{
		drawable: w.ID,
		window:   w.ID,
		isWindow: true,
		w:        int(g.Width),
		h:        int(g.Height),
		rootX:    rootX,
		rootY:    rootY,
		depth:    g.Depth,
		visual:   visual,
		source:   fmt.Sprintf("window %#x", w.ID),
	}, opt)
}

// sourceSpec is everything newSource needs about what is being captured.
type sourceSpec struct {
	drawable uint32
	window   uint32
	isWindow bool
	x, y     int16 // top-left of the capture rect within the drawable
	w, h     int   // capture rect size, in source pixels
	rootX    int   // where the capture rect sits in root coordinates,
	rootY    int   // which is what places the cursor
	depth    uint8
	visual   x11.VisualType
	source   string
}

// newSource resolves the options against the source's native size, works out
// the pixel conversion, allocates the capture buffers, and attaches the
// MIT-SHM segments when the fast path is available.
func newSource(c *x11.Conn, spec sourceSpec, opt Options) (*built, error) {
	resolved, err := opt.resolve(spec.w, spec.h)
	if err != nil {
		return nil, err
	}
	setup := c.Setup()
	format, ok := setup.FormatFor(spec.depth)
	if !ok {
		return nil, fmt.Errorf("%w: the server lists no pixmap format for depth %d",
			ErrProtocol, spec.depth)
	}
	conv, err := x11.NewConverter(format, spec.visual, setup.ImageByteOrder)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if resolved.RawAlpha {
		conv = conv.WithRawAlpha()
	}

	g := &x11Grabber{
		conn:     c,
		drawable: spec.drawable,
		srcX:     spec.x,
		srcY:     spec.y,
		srcW:     spec.w,
		srcH:     spec.h,
		rootX:    spec.rootX,
		rootY:    spec.rootY,
		isWindow: spec.isWindow,
		window:   spec.window,
		conv:     conv,
		source:   spec.source,
		n:        resolved.QueueDepth,
	}
	g.srcStride = conv.SrcStride(spec.w)
	g.bgraStride = conv.DstStride(spec.w)
	g.sc = newScaler(spec.w, spec.h, resolved.Width, resolved.Height, resolved.ScalesToFit)
	g.outW, g.outH, g.outStride = spec.w, spec.h, g.bgraStride
	if g.sc != nil {
		g.outW, g.outH, g.outStride = resolved.Width, resolved.Height, g.sc.dstStride()
	}

	if resolved.ShowsCursor {
		xf, err := c.QueryXfixes()
		if err != nil {
			return nil, mapXError(err)
		}
		if xf == nil {
			return nil, fmt.Errorf("%w: ShowsCursor needs the XFIXES extension and this "+
				"server has none, so the cursor cannot be captured; ask for ShowsCursor: "+
				"false to capture without it", ErrInvalidOption)
		}
		g.xfixes = xf
	}

	if err := g.allocate(resolved.ForceGetImage); err != nil {
		g.releaseBuffers()
		return nil, err
	}
	return &built{g: g, opt: resolved}, nil
}

// x11Grabber is the X11 half of a capture: the drawable, the buffers, and the
// one request per frame that fills them.
type x11Grabber struct {
	conn *x11.Conn
	shm  *x11.Shm
	segs []*x11.Segment

	// raw is where the server writes: the mapped shared segment on the fast
	// path, a heap buffer on the slow one. bgra is where the BGRA pixels end
	// up — the same slice as raw when the conversion is in place. out is the
	// buffer the consumer is handed — the same slice as bgra unless the frame
	// is being resampled.
	raw  [][]byte
	bgra [][]byte
	out  [][]byte
	n    int

	drawable   uint32
	window     uint32
	isWindow   bool
	srcX, srcY int16
	srcW, srcH int
	rootX      int
	rootY      int

	conv       *x11.Converter
	srcStride  int
	bgraStride int
	outW, outH int
	outStride  int
	sc         *scaler
	xfixes     *x11.Xfixes

	source string
}

// allocate sizes and attaches every buffer. It tries MIT-SHM first unless
// forceGetImage says otherwise, and falls back to heap buffers on any failure
// along the way — a server without the extension, a transport that cannot pass
// a descriptor, or a segment the server refuses.
func (g *x11Grabber) allocate(forceGetImage bool) error {
	rawLen := g.srcStride * g.srcH
	if rawLen <= 0 {
		return fmt.Errorf("%w: a %dx%d capture at %d bytes per row has no pixels",
			ErrInvalidOption, g.srcW, g.srcH, g.srcStride)
	}
	if !forceGetImage {
		if err := g.attachShm(rawLen); err != nil {
			g.releaseShm()
		}
	}
	g.raw = make([][]byte, g.n)
	g.bgra = make([][]byte, g.n)
	g.out = make([][]byte, g.n)
	for i := 0; i < g.n; i++ {
		if g.shm != nil {
			g.raw[i] = g.segs[i].Data[:rawLen]
		} else {
			g.raw[i] = make([]byte, rawLen)
		}
		if g.conv.InPlace {
			g.bgra[i] = g.raw[i]
		} else {
			g.bgra[i] = make([]byte, g.bgraStride*g.srcH)
		}
		if g.sc != nil {
			g.out[i] = make([]byte, g.sc.bufLen())
		} else {
			g.out[i] = g.bgra[i]
		}
	}
	return nil
}

// attachShm allocates and registers one shared segment per buffer. Any failure
// leaves the caller to release what was made and fall back.
func (g *x11Grabber) attachShm(size int) error {
	shm, err := g.conn.QueryShm()
	if err != nil {
		return err
	}
	if shm == nil {
		return fmt.Errorf("screencast: the server has no MIT-SHM extension")
	}
	if !shm.FDCapable {
		return fmt.Errorf("screencast: MIT-SHM %d.%d without descriptor passing",
			shm.VerMajor, shm.VerMinor)
	}
	g.shm = shm
	g.segs = make([]*x11.Segment, 0, g.n)
	for i := 0; i < g.n; i++ {
		seg, err := x11.NewSegment(g.conn.NewID(), size)
		if err != nil {
			return err
		}
		g.segs = append(g.segs, seg)
		// The server must WRITE the captured pixels into the segment, so it
		// is attached read-write. AttachFd hands over our descriptor; the
		// server closes its copy when the segment is detached.
		if err := shm.AttachFd(seg.Seg, seg.FD, false); err != nil {
			return err
		}
		seg.FD = -1 // the server owns it now
	}
	// A single round trip proves the whole arrangement works before the
	// stream starts handing frames out: a server that refuses the segment
	// says so here rather than at the first tick.
	if _, err := shm.GetImage(g.drawable, g.srcX, g.srcY,
		uint16(g.srcW), uint16(g.srcH), g.segs[0].Seg, 0); err != nil {
		return err
	}
	return nil
}

// releaseShm detaches and unmaps every segment.
func (g *x11Grabber) releaseShm() {
	for _, seg := range g.segs {
		if g.shm != nil {
			// The server frees every segment when the connection goes away,
			// so a Detach that fails because the connection is already shut
			// is not a leak and not an error.
			_ = g.shm.Detach(seg.Seg)
		}
		_ = seg.Close()
	}
	g.segs = nil
	g.shm = nil
}

// releaseBuffers drops every buffer, shared or not.
func (g *x11Grabber) releaseBuffers() {
	g.releaseShm()
	g.raw, g.bgra, g.out = nil, nil, nil
}

// Buffers implements grabber.
func (g *x11Grabber) Buffers() int { return g.n }

// Source implements grabber.
func (g *x11Grabber) Source() string { return g.source }

// Converts implements grabber: it reports whether each frame needs more than
// the alpha fill.
func (g *x11Grabber) Converts() bool { return !g.conv.Identity }

// UsesSharedMemory reports whether frames come through MIT-SHM.
func (g *x11Grabber) UsesSharedMemory() bool { return g.shm != nil }

// Transport implements grabber: it names the route the pixels take.
func (g *x11Grabber) Transport() string {
	if g.shm != nil {
		return "X11/MIT-SHM"
	}
	return "X11/GetImage"
}

// Grab implements grabber: one request, one conversion, optionally a cursor
// and a resample, and the frame is ready. It allocates nothing.
func (g *x11Grabber) Grab(i int) (Frame, error) {
	var err error
	if g.shm != nil {
		_, err = g.shm.GetImage(g.drawable, g.srcX, g.srcY,
			uint16(g.srcW), uint16(g.srcH), g.segs[i].Seg, 0)
	} else {
		_, err = g.conn.GetImage(g.drawable, g.srcX, g.srcY,
			uint16(g.srcW), uint16(g.srcH), g.raw[i])
	}
	if err != nil {
		return Frame{}, mapXError(err)
	}
	if err := g.conv.Convert(g.bgra[i], g.raw[i], g.srcW, g.srcH,
		g.bgraStride, g.srcStride); err != nil {
		return Frame{}, fmt.Errorf("%w: %v", ErrProtocol, err)
	}
	if g.xfixes != nil {
		g.drawCursor(g.bgra[i])
	}
	pix := g.bgra[i]
	if g.sc != nil {
		g.sc.scale(g.out[i], g.bgra[i], g.bgraStride)
		pix = g.out[i]
	}
	return Frame{
		Pix:    pix,
		Width:  g.outW,
		Height: g.outH,
		Stride: g.outStride,
	}, nil
}

// drawCursor composites the pointer into a captured frame. A failure is not
// fatal: the frame is still a good frame, it just has no pointer on it, and a
// stream should not die because the cursor changed shape at the wrong moment.
func (g *x11Grabber) drawCursor(dst []byte) {
	ci, err := g.xfixes.GetCursorImage()
	if err != nil || ci.Width == 0 || ci.Height == 0 {
		return
	}
	ox, oy := ci.Origin()
	if g.isWindow {
		// The cursor's position is stated in root coordinates; for a window
		// capture the window's own origin is what matters, and it moves, so
		// it is asked for per frame rather than cached.
		p, err := g.conn.QueryPointer(g.window)
		if err != nil || !p.SameScreen {
			return
		}
		ox = int(p.WinX) - int(ci.XHot)
		oy = int(p.WinY) - int(ci.YHot)
	} else {
		ox -= g.rootX
		oy -= g.rootY
	}
	blendCursor(dst, g.srcW, g.srcH, g.bgraStride,
		ci.Pix, int(ci.Width), int(ci.Height), int(ci.Width)*4, ox, oy)
}

// Interrupt implements grabber: it closes the X connection, which aborts a
// GetImage or ShmGetImage that is waiting for a reply. It frees no buffer, so
// a grab still running cannot touch unmapped memory.
func (g *x11Grabber) Interrupt() { _ = g.conn.Close() }

// Close implements grabber: it releases the segments and the connection. It
// runs only after the capture loop has stopped, so unmapping the shared
// segments cannot race the X server writing into them.
func (g *x11Grabber) Close() error {
	g.releaseBuffers()
	return g.conn.Close()
}
