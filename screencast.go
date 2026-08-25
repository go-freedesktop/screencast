// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

// Package screencast captures the pixels of displays and windows on Linux, in
// pure Go, with CGO_ENABLED=0 and without shelling out to anything.
//
// It is the Linux sibling of github.com/go-macos/screencapture and presents
// deliberately the same shape, so one consumer can drive both platforms
// through near-identical adapters. Where Linux genuinely differs, the
// difference is named in the doc comment of the affected symbol and collected
// under "Deviations from the macOS sibling" below.
//
// # Backends
//
// X11 is the backend that works. It speaks the X11 wire protocol directly —
// no Xlib, no XCB, no cgo — over the display's unix socket, authenticating
// with the MIT-MAGIC-COOKIE-1 from the Xauthority file. Frames are fetched
// with MIT-SHM ShmGetImage into a shared memory segment the X server writes
// straight into (the descriptor is handed over with SCM_RIGHTS), so a frame
// costs one small request and no pixel ever travels through the socket. When
// the server does not offer MIT-SHM, or the transport cannot pass a
// descriptor, it falls back to core GetImage, which is correct but ships every
// pixel over the socket.
//
// Wayland is NOT implemented. A compositor exposing wlr-screencopy could be
// driven the same way; it is not done here. See [ErrNoBackend].
//
// xdg-desktop-portal ScreenCast (the correct route on GNOME and KDE) hands
// back a PipeWire stream, and a pure-Go PipeWire client is a large piece of
// work that this package deliberately does not start. When the portal is the
// only route left, capture fails with [ErrPortalPipeWire], which says exactly
// that. Knowing precisely where the wall is, is the useful result.
//
// # The hot path
//
// The package is written for a compositor that redraws every frame and cannot
// afford a copy or an allocation per frame. [Stream.Frame] hands back a
// BORROWED view of the most recent captured frame — for the MIT-SHM backend
// those are the bytes the X server itself wrote into the shared segment, not a
// copy — together with a boolean saying whether it is newer than the one the
// previous call returned. In steady state a Frame call performs no allocation
// at all.
//
// The borrow is valid until the QueueDepth-th subsequent frame overwrites the
// buffer; with the default depth that is comfortably longer than one
// consumer's turn. Copy out of it (see [Frame.CopyTight] or [Frame.NRGBA]) if
// you need it to outlive the borrow.
//
// # Stride
//
// A captured frame's rows may be PADDED. Stride is the number of bytes per row
// and it is NOT necessarily Width*4: X11 pads every scanline to the pixmap
// format's scanline-pad boundary, which for the usual 32-bits-per-pixel depth
// 24 visual lands on Width*4, but for a 16-bit or a 30-bit visual does not.
// Always index with Stride, or use [Frame.Row]. This is the single most common
// way to get a sheared image.
//
// # Pixel format
//
// Frames are handed back as [FormatBGRA]: four bytes per pixel, blue first,
// then green, red, alpha. That is the memory layout a little-endian X server
// with a depth-24 TrueColor visual already produces, so on the overwhelmingly
// common case there is no conversion at all. On a server whose visual masks or
// image byte order say otherwise, the capture converts each frame in place and
// [Stream.Converts] reports true.
//
// # Deviations from the macOS sibling
//
//   - [Authorized] and [RequestAuthorization] are not a permission dance. X11
//     has no per-application capture grant: if the connection authenticates,
//     capture is allowed. They report whether a usable display connection can
//     be opened.
//   - FPS is a real POLLING RATE, not a ceiling. X11 GetImage is pull-based:
//     the capture loop asks for a frame every tick and gets one whether or not
//     anything changed. ScreenCaptureKit is change-driven and delivers nothing
//     while the screen is still. A consumer written against the macOS
//     "fresh" flag still works; it just sees fresh on every tick.
//   - [Options.Width] and [Options.Height] resample on the CPU (nearest
//     neighbour). X11 has no server-side scaler, so asking for a size other
//     than the source's native one costs a pass over the pixels.
//   - Rect coordinates are in PIXELS, not points. X11 has no points.
//   - Display.PixelWidth equals Display.Width for the same reason; both pairs
//     are kept so a consumer's field access is identical on both platforms.
//   - [Options.ExcludeWindows] is not supported and is rejected: the X server
//     has no notion of capturing the screen minus a window.
package screencast

import (
	"errors"
	"fmt"
	"image"
	"time"
)

// Sentinel errors. All are stable and may be matched with errors.Is.
var (
	// ErrUnsupported is reported on every non-Linux platform.
	ErrUnsupported = errors.New("screencast: unsupported on this platform (Linux only)")

	// ErrNoBackend is reported when no capture backend can serve this
	// session: there is no X11 display to connect to and the Wayland route
	// this package implements is not available.
	ErrNoBackend = errors.New("screencast: no usable capture backend " +
		"(no X11 display reachable; set DISPLAY, or run under Xwayland)")

	// ErrPortalPipeWire is reported when the only remaining capture route is
	// xdg-desktop-portal's org.freedesktop.portal.ScreenCast, which delivers
	// frames over PipeWire. This package speaks no PipeWire and deliberately
	// does not try: the message names the wall rather than pretending.
	ErrPortalPipeWire = errors.New("screencast: this session can only be captured through " +
		"xdg-desktop-portal's org.freedesktop.portal.ScreenCast, which delivers frames over " +
		"PipeWire; this package implements no PipeWire client. Run the session under X11 or " +
		"Xwayland, or use a wlroots compositor, to capture with this package")

	// ErrPermissionDenied is reported when the X server refuses the
	// connection because no usable authorization cookie was found.
	ErrPermissionDenied = errors.New("screencast: the display server refused the connection — " +
		"no matching MIT-MAGIC-COOKIE-1 in the Xauthority file; set XAUTHORITY to the file " +
		"holding a cookie for this display, or run as the user who owns the session")

	// ErrNoDisplay is reported when a capture was asked for and the server
	// listed no display at all.
	ErrNoDisplay = errors.New("screencast: no capturable display")

	// ErrNotFound is reported when a display or window ID does not name
	// anything currently capturable.
	ErrNotFound = errors.New("screencast: no such display or window")

	// ErrClosed is reported by every [Stream] method after [Stream.Close].
	ErrClosed = errors.New("screencast: stream is closed")

	// ErrNoFrame is reported by [Stream.WaitFrame] when no frame arrived
	// before its context expired, and by frame helpers called on a frame that
	// holds no pixels.
	ErrNoFrame = errors.New("screencast: no frame available")

	// ErrInvalidOption is reported by [Options.Validate] and wraps a
	// description of the offending field.
	ErrInvalidOption = errors.New("screencast: invalid option")

	// ErrShortBuffer is reported by [Frame.CopyTight] when the destination is
	// too small to hold the frame.
	ErrShortBuffer = errors.New("screencast: destination buffer too short")

	// ErrProtocol is reported when the display server answered something this
	// package cannot make sense of. It always wraps a description.
	ErrProtocol = errors.New("screencast: display server protocol error")
)

// PixelFormat names the layout of a captured frame. It is spelled as a
// four-character code so it matches the macOS sibling's OSType field for
// field, which lets one consumer print or switch on it identically.
type PixelFormat uint32

// FormatBGRA is 32-bit BGRA: blue, green, red, alpha, in that byte order. It
// is the only format this package hands back. It is what a compositor wants,
// it is what a little-endian X server with a depth-24 TrueColor visual already
// produces, and it is packed rather than planar so a frame is one contiguous
// run of bytes.
const FormatBGRA PixelFormat = 0x42475241 // 'BGRA'

// String renders the code as its four characters, e.g. "BGRA".
func (f PixelFormat) String() string {
	b := [4]byte{byte(f >> 24), byte(f >> 16), byte(f >> 8), byte(f)}
	for _, c := range b {
		if c < 0x20 || c > 0x7e {
			return fmt.Sprintf("PixelFormat(%#08x)", uint32(f))
		}
	}
	return string(b[:])
}

// BytesPerPixel is the size of one pixel in this format.
func (f PixelFormat) BytesPerPixel() int {
	if f == FormatBGRA {
		return 4
	}
	return 0
}

// Rect is a rectangle in the global desktop coordinate space, in PIXELS.
//
// The macOS sibling states these in points; X11 has no points, so the numbers
// here are pixels. The field names and types are identical so a consumer's
// arithmetic is unchanged.
type Rect struct {
	X, Y, W, H float64
}

// String renders the rectangle as "(x,y)+(w×h)".
func (r Rect) String() string { return fmt.Sprintf("(%g,%g)+(%g×%g)", r.X, r.Y, r.W, r.H) }

// Empty reports whether the rectangle encloses no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Display is a capturable display: one monitor of one X screen, as RandR
// reports it, or the whole X screen when the server has no RandR.
//
// Width/Height and PixelWidth/PixelHeight are equal on X11 — there is no
// backing-scale factor to separate them — and both pairs exist so the struct
// is field-for-field the macOS sibling's.
type Display struct {
	ID          uint32 // RandR monitor/CRTC id, or the root window for a whole screen
	Name        string // RandR output name, e.g. "HDMI-1"; "" when unnamed
	Width       int    // pixels
	Height      int    // pixels
	PixelWidth  int    // pixels; equal to Width on X11
	PixelHeight int    // pixels; equal to Height on X11
	Frame       Rect   // position within the X screen, pixels
	Main        bool   // this is the RandR primary monitor
	Screen      int    // X screen index this display belongs to
	Root        uint32 // root window of that screen; the drawable capture reads
}

// Scale is the display's backing scale factor. It is always 1 on X11, and
// exists so a consumer can call it on either platform.
func (d Display) Scale() float64 { return 1 }

// String renders the display for logs.
func (d Display) String() string {
	name := d.Name
	if name == "" {
		name = "display"
	}
	return fmt.Sprintf("%s %d %dx%d px at %s", name, d.ID, d.Width, d.Height, d.Frame)
}

// Window is a capturable top-level window.
type Window struct {
	ID       uint32 // X window id
	Title    string // _NET_WM_NAME, or WM_NAME
	AppName  string // WM_CLASS class part, e.g. "Firefox"
	BundleID string // WM_CLASS instance part; the closest X11 has to a bundle id
	PID      int32  // _NET_WM_PID, 0 when the window does not advertise one
	Frame    Rect   // position within the X screen, pixels
	Layer    int    // 0 for a normal application window; see [Window.Layer]
	OnScreen bool   // mapped and viewable
	Active   bool   // named by _NET_ACTIVE_WINDOW
	Screen   int    // X screen index
	Root     uint32 // root window of that screen
}

// String renders the window for logs.
func (w Window) String() string {
	return fmt.Sprintf("window %d %q [%s] at %s", w.ID, w.Title, w.AppName, w.Frame)
}

// Application is a process owning capturable windows.
type Application struct {
	PID      int32
	Name     string
	BundleID string
}

// Content is a snapshot of what the calling process may capture. It is a
// snapshot: windows open and close, so re-read it rather than caching it.
type Content struct {
	Displays     []Display
	Windows      []Window
	Applications []Application
}

// Display returns the display with the given id.
func (c *Content) Display(id uint32) (Display, error) {
	for _, d := range c.Displays {
		if d.ID == id {
			return d, nil
		}
	}
	return Display{}, fmt.Errorf("%w: display %d", ErrNotFound, id)
}

// MainDisplay returns the primary display, or the first one if none is
// flagged primary.
func (c *Content) MainDisplay() (Display, error) {
	if len(c.Displays) == 0 {
		return Display{}, ErrNoDisplay
	}
	for _, d := range c.Displays {
		if d.Main {
			return d, nil
		}
	}
	return c.Displays[0], nil
}

// Window returns the window with the given id.
func (c *Content) Window(id uint32) (Window, error) {
	for _, w := range c.Windows {
		if w.ID == id {
			return w, nil
		}
	}
	return Window{}, fmt.Errorf("%w: window %d", ErrNotFound, id)
}

// WindowsByTitle returns every window whose title is exactly title.
func (c *Content) WindowsByTitle(title string) []Window {
	var out []Window
	for _, w := range c.Windows {
		if w.Title == title {
			out = append(out, w)
		}
	}
	return out
}

// WindowsOfPID returns every window owned by the given process.
func (c *Content) WindowsOfPID(pid int32) []Window {
	var out []Window
	for _, w := range c.Windows {
		if w.PID == pid {
			out = append(out, w)
		}
	}
	return out
}

// Options configures a capture stream.
//
// The zero Options is usable: it captures the source at its native pixel size,
// at [DefaultFPS] frames per second, without the cursor.
type Options struct {
	// Width and Height are the requested frame size in PIXELS. Zero means
	// "the source's native pixel size", which is what you want: any other
	// value costs a CPU resample of every frame, because X11 has no
	// server-side scaler.
	Width, Height int

	// FPS is the POLLING RATE of the capture loop. Unlike the macOS sibling,
	// where it is a ceiling on a change-driven stream, here it is the rate at
	// which frames are actually fetched. Zero means [DefaultFPS].
	FPS float64

	// ShowsCursor composites the mouse pointer into the captured frames,
	// using the XFIXES extension's GetCursorImage. It is rejected at capture
	// time when the server has no XFIXES rather than silently producing
	// cursorless frames.
	ShowsCursor bool

	// QueueDepth is how many capture buffers the stream cycles through. Zero
	// means [DefaultQueueDepth]. It must leave room for the two frames the
	// package holds on the consumer's behalf (the one lent out and the one
	// being filled), so values below [MinQueueDepth] are rejected.
	QueueDepth int

	// ExcludeWindows exists only so the struct matches the macOS sibling. The
	// X server cannot capture a screen minus a window, so a non-empty value
	// is rejected by [Options.Validate] rather than silently ignored.
	ExcludeWindows []uint32

	// ScalesToFit letterboxes the source into Width×Height, preserving its
	// aspect ratio, instead of stretching it.
	ScalesToFit bool

	// RawAlpha hands back the fourth byte of each pixel exactly as the
	// display server wrote it, instead of forcing it opaque.
	//
	// On the common path — a little-endian server with a depth-24 TrueColor
	// visual, where the captured bytes are already BGRA — forcing alpha
	// opaque is the ONLY per-frame work the capture does, and it is a whole
	// pass over the frame. It is not free: a 3840x2160 capture on a Debian 13
	// cloud VM measured 24.3 ms a frame with the fill and 5.8 ms without it —
	// 41 fps against 172. A consumer that ignores alpha, or that sets it
	// itself on the GPU, gets that back by setting this.
	//
	// The bytes are then UNDEFINED — in practice zero, which reads as fully
	// transparent. Do not set it unless you know what you do with the fourth
	// byte. It has no effect on a visual that needs a conversion pass anyway,
	// nor on a depth-32 visual, where the alpha is real and always carried.
	RawAlpha bool

	// ForceGetImage disables the MIT-SHM fast path and fetches every frame
	// with core GetImage. It exists to make the slow path testable and to
	// give a way out if a server's MIT-SHM is broken.
	ForceGetImage bool
}

// Defaults applied to the zero value of the corresponding [Options] field.
const (
	// DefaultFPS is the polling rate used when Options.FPS is zero.
	DefaultFPS = 60.0
	// DefaultQueueDepth is the buffer count used when Options.QueueDepth is
	// zero.
	DefaultQueueDepth = 6
	// MinQueueDepth is the smallest queue depth this package accepts: one
	// buffer lent to the consumer, one holding the published frame, one being
	// filled.
	MinQueueDepth = 3
	// MaxDimension is the largest frame edge accepted, a sanity bound well
	// above any real display; it exists so a mistaken value fails loudly
	// instead of asking the X server for a terabyte.
	MaxDimension = 32768
	// MinFPS is the slowest polling rate accepted.
	MinFPS = 0.01
)

// Validate reports whether the options are self-consistent, wrapping
// [ErrInvalidOption]. It does not consult the system, and it behaves
// identically on every platform so a consumer's option bug surfaces the same
// everywhere.
func (o Options) Validate() error {
	if o.Width < 0 || o.Height < 0 {
		return fmt.Errorf("%w: negative size %dx%d", ErrInvalidOption, o.Width, o.Height)
	}
	if (o.Width == 0) != (o.Height == 0) {
		return fmt.Errorf("%w: Width and Height must both be set or both be zero, got %dx%d",
			ErrInvalidOption, o.Width, o.Height)
	}
	if o.Width > MaxDimension || o.Height > MaxDimension {
		return fmt.Errorf("%w: size %dx%d exceeds the %d-pixel limit",
			ErrInvalidOption, o.Width, o.Height, MaxDimension)
	}
	if o.FPS < 0 {
		return fmt.Errorf("%w: negative FPS %g", ErrInvalidOption, o.FPS)
	}
	if o.FPS > 0 && o.FPS < MinFPS {
		return fmt.Errorf("%w: FPS %g is below the %g minimum", ErrInvalidOption, o.FPS, MinFPS)
	}
	if o.QueueDepth < 0 {
		return fmt.Errorf("%w: negative QueueDepth %d", ErrInvalidOption, o.QueueDepth)
	}
	if o.QueueDepth > 0 && o.QueueDepth < MinQueueDepth {
		return fmt.Errorf("%w: QueueDepth %d is below the minimum of %d",
			ErrInvalidOption, o.QueueDepth, MinQueueDepth)
	}
	if len(o.ExcludeWindows) > 0 {
		return fmt.Errorf("%w: ExcludeWindows is not supported on X11: the server cannot "+
			"capture a screen minus a window", ErrInvalidOption)
	}
	return nil
}

// resolve fills the zero fields from the defaults and from the source's native
// pixel size, and returns the options actually used. It validates first, so a
// resolved Options is always usable.
func (o Options) resolve(nativeW, nativeH int) (Options, error) {
	if err := o.Validate(); err != nil {
		return Options{}, err
	}
	r := o
	if r.Width == 0 {
		if nativeW <= 0 || nativeH <= 0 {
			return Options{}, fmt.Errorf("%w: no size given and the source reports %dx%d",
				ErrInvalidOption, nativeW, nativeH)
		}
		r.Width, r.Height = nativeW, nativeH
	}
	if r.FPS == 0 {
		r.FPS = DefaultFPS
	}
	if r.QueueDepth == 0 {
		r.QueueDepth = DefaultQueueDepth
	}
	return r, nil
}

// tickInterval is the capture loop's period for a polling rate of fps. A
// non-positive rate yields the interval for [DefaultFPS] rather than a
// division by zero, and the result is never below a microsecond.
func tickInterval(fps float64) time.Duration {
	if fps <= 0 {
		fps = DefaultFPS
	}
	d := time.Duration(float64(time.Second)/fps + 0.5)
	if d < time.Microsecond {
		d = time.Microsecond
	}
	return d
}

// Frame is a BORROWED view of one captured frame.
//
// Pix aliases a capture buffer owned by the stream — for the MIT-SHM backend,
// the shared segment the X server wrote into. It stays valid until the stream
// cycles back round to that buffer, which with the default [Options.QueueDepth]
// is several frames away. Do not retain it; copy with [Frame.CopyTight] or
// [Frame.NRGBA] if you need it to outlive the borrow.
type Frame struct {
	// Pix is the frame's bytes in [FormatBGRA], Stride bytes per row, Height
	// rows. len(Pix) >= Stride*Height.
	Pix []byte
	// Width and Height are the frame's size in pixels.
	Width, Height int
	// Stride is the number of BYTES per row. It is NOT necessarily Width*4:
	// the X server pads each scanline to the pixmap format's boundary.
	Stride int
	// Seq counts frames since the stream started; it is 0 before the first
	// frame and strictly increases afterwards.
	Seq uint64
	// At is when the capture loop received the frame.
	At time.Time
}

// Format reports the pixel format of the frame, always [FormatBGRA].
func (f Frame) Format() PixelFormat { return FormatBGRA }

// Valid reports whether the frame holds pixels.
func (f Frame) Valid() bool {
	return f.Width > 0 && f.Height > 0 && f.Stride >= f.Width*4 && len(f.Pix) >= f.Stride*f.Height
}

// TightLen is the number of bytes the frame occupies with no row padding,
// Width*4*Height.
func (f Frame) TightLen() int { return f.Width * 4 * f.Height }

// Row returns row y of the frame, Width*4 bytes with the padding trimmed off.
// It does not allocate. It returns nil for an out-of-range y or an invalid
// frame.
func (f Frame) Row(y int) []byte {
	if !f.Valid() || y < 0 || y >= f.Height {
		return nil
	}
	off := y * f.Stride
	return f.Pix[off : off+f.Width*4 : off+f.Width*4]
}

// CopyTight copies the frame into dst with the row padding removed, so dst
// holds Width*4*Height bytes of contiguous BGRA. It reports how many bytes it
// wrote, or [ErrShortBuffer] if dst is too small. It allocates nothing.
func (f Frame) CopyTight(dst []byte) (int, error) {
	if !f.Valid() {
		return 0, ErrNoFrame
	}
	n := f.TightLen()
	if len(dst) < n {
		return 0, fmt.Errorf("%w: need %d bytes, got %d", ErrShortBuffer, n, len(dst))
	}
	rowLen := f.Width * 4
	// The fast path: an unpadded frame is one contiguous run. On the usual
	// depth-24 32-bits-per-pixel visual every frame takes it.
	if f.Stride == rowLen {
		return copy(dst, f.Pix[:n]), nil
	}
	for y := 0; y < f.Height; y++ {
		src := y * f.Stride
		copy(dst[y*rowLen:(y+1)*rowLen], f.Pix[src:src+rowLen])
	}
	return n, nil
}

// NRGBA copies the frame into a freshly allocated image.NRGBA, swapping BGRA
// to RGBA as it goes and forcing alpha opaque, because an X11 screen capture
// carries no meaningful alpha. It is the convenience path for saving a frame
// to disk; it allocates, so it does not belong in a per-frame loop.
func (f Frame) NRGBA() (*image.NRGBA, error) {
	if !f.Valid() {
		return nil, ErrNoFrame
	}
	img := image.NewNRGBA(image.Rect(0, 0, f.Width, f.Height))
	for y := 0; y < f.Height; y++ {
		src := f.Pix[y*f.Stride:]
		dst := img.Pix[y*img.Stride:]
		for x := 0; x < f.Width; x++ {
			dst[x*4+0] = src[x*4+2]
			dst[x*4+1] = src[x*4+1]
			dst[x*4+2] = src[x*4+0]
			dst[x*4+3] = 0xff
		}
	}
	return img, nil
}

// Stats reports what a stream has seen since it started.
type Stats struct {
	// Frames is the number of frames actually captured.
	Frames uint64
	// Idle is the number of capture ticks that produced no new frame. On the
	// X11 backend it counts ticks skipped because the previous capture was
	// still in flight; the pull-based backend has no "nothing changed" reply.
	Idle uint64
	// Superseded is the number of captured frames that were replaced by a
	// newer one before the consumer ever asked for them. A large value next
	// to Frames means the consumer is slower than the capture.
	Superseded uint64
	// Last is when the most recent frame arrived.
	Last time.Time
	// Interval is the gap between the two most recent frames.
	Interval time.Duration
}

// FPS is the instantaneous rate implied by [Stats.Interval], 0 when fewer than
// two frames have arrived.
func (s Stats) FPS() float64 {
	if s.Interval <= 0 {
		return 0
	}
	return float64(time.Second) / float64(s.Interval)
}
