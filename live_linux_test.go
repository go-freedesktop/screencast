// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux && integration

package screencast

import (
	"context"
	"errors"
	"fmt"
	"image/png"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-freedesktop/screencast/internal/x11"
)

// This is the live suite. It needs a real X server and does not run by
// default: build it with -tags integration AND set SCREENCAST_LIVE=1.
//
//	Xvfb :99 -screen 0 1280x800x24 -ac &
//	DISPLAY=:99 SCREENCAST_LIVE=1 go test -tags integration -run Live -v ./...
//
// What it proves, and why each step is there:
//
//   - The capture reads the RIGHT drawable, in the RIGHT channel order, at the
//     stride it declares. It does that by PAINTING the root window a colour
//     built from the visual's own channel masks and then requiring every
//     captured pixel to equal it. "The frame is not all zeroes" is a weak
//     assertion — a capture that swapped red and blue would pass it — and a
//     static grey buffer is the classic silent failure of a screen capture.
//   - The capture SEES CHANGES: painting the next colour changes the frames.
//   - Stream.Frame does not allocate, because the consumer's whole budget is
//     16.6 ms a frame.
//   - The resampler, the window capture and the GetImage fallback all work
//     against a real server, not only against the scripted one.

// liveEnv skips unless the suite has been asked for and a display is there.
func liveEnv(t *testing.T) {
	t.Helper()
	if os.Getenv("SCREENCAST_LIVE") != "1" {
		t.Skip("set SCREENCAST_LIVE=1 to run the live suite")
	}
	if !Available() {
		t.Fatalf("SCREENCAST_LIVE is set but no display is reachable: %v", Diagnose())
	}
}

// painter is a second X connection that owns one override-redirect window
// covering the display under test, and repaints it on demand.
//
// It paints its OWN window rather than the root because the root is only
// visible where nothing covers it: on a desktop with a terminal open, an
// assertion about "every pixel of the screen" would be an assertion about the
// terminal too. A fullscreen override-redirect window is above everything, has
// no window-manager frame, and is exactly the size it asked for — so every
// pixel of the display genuinely is the colour that was painted.
type painter struct {
	c      *x11.Conn
	root   uint32
	window uint32
	visual x11.VisualType
}

// newPainter opens the paint connection and covers the display with a window.
func newPainter(t *testing.T, d Display) *painter {
	t.Helper()
	c, _, err := x11.Dial("")
	if err != nil {
		t.Fatalf("opening the paint connection: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	setup := c.Setup()
	i, ok := setup.ScreenOfRoot(d.Root)
	if !ok {
		t.Fatalf("display %d names root %#x, which is not a screen of this server", d.ID, d.Root)
	}
	sc := setup.ScreenOf(i)
	p := &painter{c: c, root: sc.Root, visual: sc.RootVisualType()}
	win, err := c.CreateSolidWindow(sc.Root, int16(d.Frame.X), int16(d.Frame.Y),
		uint16(d.Width), uint16(d.Height), 0)
	if err != nil {
		t.Fatalf("covering the display with a window: %v", err)
	}
	p.window = win
	t.Cleanup(func() { _ = c.DestroyWindow(win) })
	return p
}

// fill paints the root window with the given channels lit, and returns the
// BGRA bytes a correct capture must hand back.
//
// The colour is built from the VISUAL'S OWN MASKS with each channel either
// all-ones or all-zeroes, which is what makes the assertion exact on a 5-6-5
// visual as well as on an 8-8-8 or a 10-10-10 one: a full-scale channel widens
// to 255 whatever its bit width.
func (p *painter) fill(t *testing.T, r, g, b bool) [4]byte {
	t.Helper()
	var pixel uint32
	var want [4]byte
	if r {
		pixel |= p.visual.RedMask
		want[2] = 0xff
	}
	if g {
		pixel |= p.visual.GreenMask
		want[1] = 0xff
	}
	if b {
		pixel |= p.visual.BlueMask
		want[0] = 0xff
	}
	want[3] = 0xff
	if err := p.c.SetWindowBackground(p.window, pixel); err != nil {
		t.Fatalf("SetWindowBackground: %v", err)
	}
	if err := p.c.ClearArea(p.window, 0, 0, 0, 0); err != nil {
		t.Fatalf("ClearArea: %v", err)
	}
	if err := p.c.Sync(); err != nil {
		t.Fatalf("Sync: %v", err)
	}
	return want
}

// settledFrame waits for a frame captured after the most recent paint. The two
// connections are not synchronised, so one frame may have been in flight while
// the paint happened; three fresh frames is comfortably past it.
func settledFrame(t *testing.T, s *Stream) Frame {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var f Frame
	for i := 0; i < 3; i++ {
		var err error
		f, err = s.WaitFrame(ctx)
		if err != nil {
			t.Fatalf("WaitFrame: %v", err)
		}
	}
	return f
}

// assertUniform requires every pixel of the frame to be want, walking with the
// frame's stride.
func assertUniform(t *testing.T, f Frame, want [4]byte, what string) {
	t.Helper()
	if !f.Valid() {
		t.Fatalf("%s: the frame is not valid: %dx%d stride %d, %d bytes",
			what, f.Width, f.Height, f.Stride, len(f.Pix))
	}
	for y := 0; y < f.Height; y++ {
		row := f.Row(y)
		for x := 0; x < f.Width; x++ {
			p := row[x*4 : x*4+4]
			if p[0] != want[0] || p[1] != want[1] || p[2] != want[2] || p[3] != want[3] {
				t.Fatalf("%s: pixel (%d,%d) is % x, want % x", what, x, y, p, want)
			}
		}
	}
}

func TestLiveEnumeration(t *testing.T) {
	liveEnv(t)
	ctx := context.Background()
	// X11 has no per-application grant, so a server we can reach is a server
	// we may capture. Repeat the check: a flaky answer here would mean the
	// connection itself is unreliable, which a consumer's startup probe must
	// not be.
	for i := 0; i < 10; i++ {
		if !Authorized() || !RequestAuthorization() {
			t.Fatalf("attempt %d: a reachable X server reported that capture is not "+
				"authorized; Probe says: %v", i, Probe())
		}
	}
	ds, err := Displays(ctx)
	if err != nil {
		t.Fatalf("Displays: %v", err)
	}
	if len(ds) == 0 {
		t.Fatal("Displays returned nothing on a server that is there")
	}
	for _, d := range ds {
		t.Logf("display %v (id %d, screen %d, root %#x, main %v)", d, d.ID, d.Screen, d.Root, d.Main)
		if d.Width <= 0 || d.Height <= 0 {
			t.Errorf("display %d has size %dx%d", d.ID, d.Width, d.Height)
		}
		if d.PixelWidth != d.Width || d.PixelHeight != d.Height {
			t.Errorf("display %d: the point and pixel sizes differ, which cannot happen on X11: %+v", d.ID, d)
		}
		if d.Root == 0 {
			t.Errorf("display %d carries no root window", d.ID)
		}
		if d.Scale() != 1 {
			t.Errorf("display %d reports scale %v", d.ID, d.Scale())
		}
	}
	if _, err := Windows(ctx); err != nil {
		t.Fatalf("Windows: %v", err)
	}
	c, err := Shareable(ctx)
	if err != nil {
		t.Fatalf("Shareable: %v", err)
	}
	if len(c.Displays) != len(ds) {
		t.Errorf("Shareable listed %d displays, Displays listed %d", len(c.Displays), len(ds))
	}
	if _, err := CurrentProcessShareable(ctx); err != nil {
		t.Fatalf("CurrentProcessShareable: %v", err)
	}
	if _, err := c.MainDisplay(); err != nil {
		t.Fatalf("MainDisplay: %v", err)
	}
}

// TestLiveEnumerationWalksTheTree covers the enumeration path a server with no
// EWMH window manager takes: there is no _NET_CLIENT_LIST to read, so the
// root's children are walked and each is resolved to its client window.
//
// It creates a window of its own first, so the walk has something to walk.
func TestLiveEnumerationWalksTheTree(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	p := newPainter(t, d)

	ws, err := Windows(context.Background())
	if err != nil {
		t.Fatalf("Windows: %v", err)
	}
	var mine *Window
	for i := range ws {
		if ws[i].ID == p.window {
			mine = &ws[i]
		}
	}
	if mine == nil {
		// An EWMH window manager publishes _NET_CLIENT_LIST, and an
		// override-redirect window is never in it — the window manager does
		// not manage it. That is correct, not a failure.
		t.Logf("our override-redirect window is not listed; this display has a window "+
			"manager publishing _NET_CLIENT_LIST (%d windows listed)", len(ws))
		return
	}
	if !mine.OnScreen {
		t.Errorf("our mapped window is reported off screen: %+v", mine)
	}
	if int(mine.Frame.W) != d.Width || int(mine.Frame.H) != d.Height {
		t.Errorf("our window is %vx%v, it was created %dx%d",
			mine.Frame.W, mine.Frame.H, d.Width, d.Height)
	}
	if mine.Layer != LayerOverride {
		t.Errorf("our override-redirect window has layer %d, want %d", mine.Layer, LayerOverride)
	}
	if mine.Root != d.Root {
		t.Errorf("our window reports root %#x, the display says %#x", mine.Root, d.Root)
	}
}

// mainDisplay is the display the capture tests run against.
func mainDisplay(t *testing.T) Display {
	t.Helper()
	c, err := Shareable(context.Background())
	if err != nil {
		t.Fatalf("Shareable: %v", err)
	}
	d, err := c.MainDisplay()
	if err != nil {
		t.Fatalf("MainDisplay: %v", err)
	}
	return d
}

// TestLiveCaptureReproducesWhatWasPainted is the central proof.
func TestLiveCaptureReproducesWhatWasPainted(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	p := newPainter(t, d)

	for _, forceSlow := range []bool{false, true} {
		name := "MIT-SHM"
		if forceSlow {
			name = "GetImage"
		}
		t.Run(name, func(t *testing.T) {
			s, err := CaptureDisplay(context.Background(), d, Options{FPS: 120, ForceGetImage: forceSlow})
			if err != nil {
				t.Fatalf("CaptureDisplay: %v", err)
			}
			defer func() { _ = s.Close() }()
			t.Logf("transport %s, converts=%v", s.Transport(), s.Converts())
			if forceSlow && s.Transport() != "X11/GetImage" {
				t.Errorf("ForceGetImage still used %s", s.Transport())
			}

			// Every channel on its own catches a red/blue swap; white and
			// black catch a buffer that never changes.
			for _, c := range []struct {
				name    string
				r, g, b bool
			}{
				{"red", true, false, false},
				{"green", false, true, false},
				{"blue", false, false, true},
				{"white", true, true, true},
				{"black", false, false, false},
			} {
				want := p.fill(t, c.r, c.g, c.b)
				f := settledFrame(t, s)
				if f.Width != d.Width || f.Height != d.Height {
					t.Fatalf("%s: frame is %dx%d, asked for %dx%d",
						c.name, f.Width, f.Height, d.Width, d.Height)
				}
				if f.Stride < f.Width*4 {
					t.Fatalf("%s: stride %d is below width*4 = %d", c.name, f.Stride, f.Width*4)
				}
				assertUniform(t, f, want, c.name)
			}
		})
	}
}

// TestLiveCaptureSeesChanges proves the frames are not a single snapshot
// repeated: the content differs before and after a paint, and the freshness
// flag says so.
func TestLiveCaptureSeesChanges(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	p := newPainter(t, d)
	s, err := CaptureDisplay(context.Background(), d, Options{FPS: 120})
	if err != nil {
		t.Fatalf("CaptureDisplay: %v", err)
	}
	defer func() { _ = s.Close() }()

	p.fill(t, true, false, false)
	first := settledFrame(t, s)
	firstSeq := first.Seq
	firstPixel := [4]byte{first.Row(0)[0], first.Row(0)[1], first.Row(0)[2], first.Row(0)[3]}

	p.fill(t, false, false, true)
	second := settledFrame(t, s)
	secondPixel := [4]byte{second.Row(0)[0], second.Row(0)[1], second.Row(0)[2], second.Row(0)[3]}

	if firstPixel == secondPixel {
		t.Fatalf("the frame did not change when the screen did: still % x", firstPixel)
	}
	if second.Seq <= firstSeq {
		t.Fatalf("the sequence number did not advance: %d then %d", firstSeq, second.Seq)
	}
	if _, fresh := s.Frame(); fresh {
		t.Error("Frame reported an already-collected frame as fresh")
	}
}

func TestLiveFrameDoesNotAllocate(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	s, err := CaptureDisplay(context.Background(), d, Options{FPS: 60})
	if err != nil {
		t.Fatalf("CaptureDisplay: %v", err)
	}
	defer func() { _ = s.Close() }()
	if _, err := s.WaitFrame(context.Background()); err != nil {
		t.Fatalf("WaitFrame: %v", err)
	}
	if got := testing.AllocsPerRun(1000, func() { s.Frame() }); got != 0 {
		t.Fatalf("Frame allocated %v times per run; the consumer's budget is 16.6 ms a frame", got)
	}
}

func TestLiveThroughput(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	for _, slow := range []bool{false, true} {
		name := "MIT-SHM"
		if slow {
			name = "GetImage"
		}
		t.Run(name, func(t *testing.T) {
			// A very high requested rate makes the capture, not the ticker,
			// the limit, so the measured rate is the real ceiling.
			s, err := CaptureDisplay(context.Background(), d, Options{FPS: 5000, ForceGetImage: slow})
			if err != nil {
				t.Fatalf("CaptureDisplay: %v", err)
			}
			defer func() { _ = s.Close() }()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			const n = 200
			start := time.Now()
			for i := 0; i < n; i++ {
				if _, err := s.WaitFrame(ctx); err != nil {
					t.Fatalf("frame %d: %v", i, err)
				}
			}
			elapsed := time.Since(start)
			t.Logf("%s: %d frames of %dx%d in %v — %.1f fps, %.3f ms/frame",
				s.Transport(), n, d.Width, d.Height, elapsed.Round(time.Millisecond),
				float64(n)/elapsed.Seconds(), float64(elapsed.Microseconds())/n/1000)
		})
	}
}

func TestLiveResampling(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	p := newPainter(t, d)
	want := p.fill(t, false, true, false)

	for _, tc := range []struct {
		name        string
		w, h        int
		fit         bool
		wantUniform bool
	}{
		{"half", d.Width / 2, d.Height / 2, false, true},
		{"double", d.Width * 2, d.Height * 2, false, true},
		{"square stretch", 256, 256, false, true},
		// Letterboxing puts black margins around a uniform source, so the
		// frame is deliberately NOT uniform.
		{"square fit", 256, 256, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, err := CaptureDisplay(context.Background(), d,
				Options{Width: tc.w, Height: tc.h, FPS: 120, ScalesToFit: tc.fit})
			if err != nil {
				t.Fatalf("CaptureDisplay: %v", err)
			}
			defer func() { _ = s.Close() }()
			f := settledFrame(t, s)
			if f.Width != tc.w || f.Height != tc.h {
				t.Fatalf("frame is %dx%d, asked for %dx%d", f.Width, f.Height, tc.w, tc.h)
			}
			if f.Stride != tc.w*4 {
				t.Errorf("resampled stride is %d, want %d", f.Stride, tc.w*4)
			}
			if tc.wantUniform {
				assertUniform(t, f, want, tc.name)
				return
			}
			// The centre must be the painted colour and a corner must be the
			// letterbox black, which is what proves the fit actually fitted.
			mid := f.Row(f.Height / 2)[(f.Width/2)*4 : (f.Width/2)*4+4]
			if mid[0] != want[0] || mid[1] != want[1] || mid[2] != want[2] {
				t.Errorf("the centre of the letterboxed frame is % x, want % x", mid, want)
			}
			corner := f.Row(0)[:4]
			if corner[0] != 0 || corner[1] != 0 || corner[2] != 0 || corner[3] != 0xff {
				t.Errorf("the letterbox margin is % x, want opaque black", corner)
			}
		})
	}
}

func TestLiveCursor(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	s, err := CaptureDisplay(context.Background(), d, Options{FPS: 60, ShowsCursor: true})
	if err != nil {
		if errors.Is(err, ErrInvalidOption) {
			t.Skipf("this server has no XFIXES: %v", err)
		}
		t.Fatalf("CaptureDisplay with the cursor: %v", err)
	}
	defer func() { _ = s.Close() }()
	f, err := s.WaitFrame(context.Background())
	if err != nil {
		t.Fatalf("WaitFrame: %v", err)
	}
	if !f.Valid() {
		t.Fatal("a cursor capture produced an invalid frame")
	}
}

// TestLiveWindowCaptureReproducesWhatWasPainted captures the painter's own
// window, which is a window whose exact contents this test controls.
func TestLiveWindowCaptureReproducesWhatWasPainted(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	p := newPainter(t, d)
	want := p.fill(t, true, false, true)

	s, err := CaptureWindow(context.Background(),
		Window{ID: p.window, Root: p.root, Frame: Rect{W: float64(d.Width), H: float64(d.Height)}},
		Options{FPS: 120})
	if err != nil {
		t.Fatalf("CaptureWindow: %v", err)
	}
	defer func() { _ = s.Close() }()
	f := settledFrame(t, s)
	if f.Width != d.Width || f.Height != d.Height {
		t.Fatalf("window frame is %dx%d, the window is %dx%d", f.Width, f.Height, d.Width, d.Height)
	}
	assertUniform(t, f, want, "window capture")

	// And it sees a change, like a display capture does.
	want2 := p.fill(t, false, true, false)
	assertUniform(t, settledFrame(t, s), want2, "window capture after a repaint")
}

func TestLiveWindowCaptureOfSomeoneElsesWindow(t *testing.T) {
	liveEnv(t)
	ctx := context.Background()
	c, err := Shareable(ctx)
	if err != nil {
		t.Fatalf("Shareable: %v", err)
	}
	var target *Window
	for i := range c.Windows {
		if c.Windows[i].OnScreen && c.Windows[i].Frame.W >= 16 && c.Windows[i].Frame.H >= 16 {
			target = &c.Windows[i]
			break
		}
	}
	if target == nil {
		t.Skip("no viewable window on this display to capture")
	}
	s, err := CaptureWindow(ctx, *target, Options{FPS: 60})
	if err != nil {
		t.Fatalf("CaptureWindow(%v): %v", target, err)
	}
	defer func() { _ = s.Close() }()
	f, err := s.WaitFrame(ctx)
	if err != nil {
		t.Fatalf("WaitFrame: %v", err)
	}
	if f.Width != int(target.Frame.W) || f.Height != int(target.Frame.H) {
		t.Errorf("window frame is %dx%d, the window is %vx%v",
			f.Width, f.Height, target.Frame.W, target.Frame.H)
	}
}

func TestLiveCaptureRejectsAWindowThatIsNotThere(t *testing.T) {
	liveEnv(t)
	_, err := CaptureWindow(context.Background(), Window{ID: 0x7fffffff}, Options{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CaptureWindow on a window that does not exist reported %v, want ErrNotFound", err)
	}
}

func TestLiveCaptureRejectsADisplayThatIsNotThere(t *testing.T) {
	liveEnv(t)
	_, err := CaptureDisplay(context.Background(), Display{ID: 0x7ffffffe}, Options{})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("CaptureDisplay on a display that does not exist reported %v, want ErrNotFound", err)
	}
}

func TestLiveCaptureRejectsBadOptions(t *testing.T) {
	liveEnv(t)
	d := mainDisplay(t)
	for _, o := range []Options{{FPS: -1}, {Width: 10}, {QueueDepth: 1}, {ExcludeWindows: []uint32{1}}} {
		if _, err := CaptureDisplay(context.Background(), d, o); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("CaptureDisplay(%+v) reported %v, want ErrInvalidOption", o, err)
		}
	}
}

// TestLiveWriteArtefact saves one captured frame as a PNG, which is the
// artefact a human can look at. It paints a recognisable pattern first so the
// image says something.
//
// The path is not the caller's to choose freely: it comes from captureDir,
// which puts it somewhere durable OUTSIDE every git work tree and refuses
// anything else. See capturedir_test.go for why, and for the test that the
// refusal really refuses.
func TestLiveWriteArtefact(t *testing.T) {
	liveEnv(t)
	path := filepath.Join(captureDir(t), "display-capture.png")
	d := mainDisplay(t)
	s, err := CaptureDisplay(context.Background(), d, Options{FPS: 60})
	if err != nil {
		t.Fatalf("CaptureDisplay: %v", err)
	}
	defer func() { _ = s.Close() }()
	f := settledFrame(t, s)
	img, err := f.NRGBA()
	if err != nil {
		t.Fatalf("NRGBA: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = out.Close() }()
	if err := png.Encode(out, img); err != nil {
		t.Fatal(err)
	}
	st, _ := out.Stat()
	t.Log(fmt.Sprintf("wrote %s: %dx%d, %d bytes", path, f.Width, f.Height, st.Size()))
}
