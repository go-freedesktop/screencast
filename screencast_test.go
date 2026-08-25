// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import (
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestPixelFormatString(t *testing.T) {
	if got := FormatBGRA.String(); got != "BGRA" {
		t.Errorf("FormatBGRA.String() = %q", got)
	}
	if got := PixelFormat(0x00010203).String(); got != "PixelFormat(0x00010203)" {
		t.Errorf("unprintable format = %q", got)
	}
	if got := FormatBGRA.BytesPerPixel(); got != 4 {
		t.Errorf("BytesPerPixel = %d", got)
	}
	if got := PixelFormat(0).BytesPerPixel(); got != 0 {
		t.Errorf("unknown format BytesPerPixel = %d", got)
	}
}

func TestRect(t *testing.T) {
	r := Rect{X: 1, Y: 2, W: 30, H: 40}
	if got := r.String(); got != "(1,2)+(30×40)" {
		t.Errorf("Rect.String() = %q", got)
	}
	if r.Empty() {
		t.Error("a 30x40 rectangle reported itself empty")
	}
	for _, e := range []Rect{{}, {W: 10}, {H: 10}, {W: -1, H: 5}, {W: 5, H: -1}} {
		if !e.Empty() {
			t.Errorf("%v reported itself non-empty", e)
		}
	}
}

func TestDisplayAndWindowStrings(t *testing.T) {
	d := Display{ID: 42, Name: "HDMI-1", Width: 1920, Height: 1080,
		Frame: Rect{W: 1920, H: 1080}}
	if got := d.String(); !strings.HasPrefix(got, "HDMI-1 42 1920x1080 px") {
		t.Errorf("Display.String() = %q", got)
	}
	if got := d.Scale(); got != 1 {
		t.Errorf("Scale() = %v, want 1 on X11", got)
	}
	unnamed := Display{ID: 7, Width: 800, Height: 600}
	if got := unnamed.String(); !strings.HasPrefix(got, "display 7 ") {
		t.Errorf("unnamed Display.String() = %q", got)
	}
	w := Window{ID: 0x1400007, Title: "Terminal", AppName: "XTerm",
		Frame: Rect{X: 10, Y: 20, W: 640, H: 480}}
	if got := w.String(); !strings.Contains(got, `"Terminal"`) || !strings.Contains(got, "[XTerm]") {
		t.Errorf("Window.String() = %q", got)
	}
}

// sampleContent is a small but realistic snapshot to test the lookups against.
func sampleContent() *Content {
	return &Content{
		Displays: []Display{
			{ID: 1, Name: "eDP-1", Width: 1920, Height: 1080},
			{ID: 2, Name: "HDMI-1", Width: 2560, Height: 1440, Main: true},
		},
		Windows: []Window{
			{ID: 10, Title: "one", PID: 100, AppName: "A"},
			{ID: 11, Title: "two", PID: 100, AppName: "A"},
			{ID: 12, Title: "one", PID: 200, AppName: "B"},
		},
	}
}

func TestContentLookups(t *testing.T) {
	c := sampleContent()
	if d, err := c.Display(2); err != nil || d.Name != "HDMI-1" {
		t.Errorf("Display(2) = %+v, %v", d, err)
	}
	if _, err := c.Display(99); !errors.Is(err, ErrNotFound) {
		t.Errorf("Display(99) reported %v, want ErrNotFound", err)
	}
	if d, err := c.MainDisplay(); err != nil || d.ID != 2 {
		t.Errorf("MainDisplay = %+v, %v", d, err)
	}
	if w, err := c.Window(11); err != nil || w.Title != "two" {
		t.Errorf("Window(11) = %+v, %v", w, err)
	}
	if _, err := c.Window(99); !errors.Is(err, ErrNotFound) {
		t.Errorf("Window(99) reported %v, want ErrNotFound", err)
	}
	if got := c.WindowsByTitle("one"); len(got) != 2 {
		t.Errorf("WindowsByTitle(\"one\") returned %d windows", len(got))
	}
	if got := c.WindowsByTitle("nope"); got != nil {
		t.Errorf("WindowsByTitle of a title nobody has returned %v", got)
	}
	if got := c.WindowsOfPID(100); len(got) != 2 {
		t.Errorf("WindowsOfPID(100) returned %d windows", len(got))
	}
	if got := c.WindowsOfPID(999); got != nil {
		t.Errorf("WindowsOfPID of an absent pid returned %v", got)
	}
}

func TestMainDisplayFallbacks(t *testing.T) {
	// No display at all.
	if _, err := (&Content{}).MainDisplay(); !errors.Is(err, ErrNoDisplay) {
		t.Errorf("MainDisplay on an empty content reported %v, want ErrNoDisplay", err)
	}
	// None flagged primary: the first one wins rather than nothing.
	c := &Content{Displays: []Display{{ID: 5}, {ID: 6}}}
	if d, err := c.MainDisplay(); err != nil || d.ID != 5 {
		t.Errorf("MainDisplay with no primary = %+v, %v", d, err)
	}
}

func TestOptionsValidate(t *testing.T) {
	if err := (Options{}).Validate(); err != nil {
		t.Errorf("the zero Options is invalid: %v", err)
	}
	if err := (Options{Width: 640, Height: 480, FPS: 30, QueueDepth: 3,
		ShowsCursor: true, ScalesToFit: true, ForceGetImage: true}).Validate(); err != nil {
		t.Errorf("a fully-populated Options is invalid: %v", err)
	}
	for _, tc := range []struct {
		name string
		opt  Options
		want string
	}{
		{"negative width", Options{Width: -1, Height: 1}, "negative size"},
		{"negative height", Options{Width: 1, Height: -1}, "negative size"},
		{"width without height", Options{Width: 640}, "both be set"},
		{"height without width", Options{Height: 480}, "both be set"},
		{"width too large", Options{Width: MaxDimension + 1, Height: 1}, "exceeds"},
		{"height too large", Options{Width: 1, Height: MaxDimension + 1}, "exceeds"},
		{"negative fps", Options{FPS: -1}, "negative FPS"},
		{"fps too small", Options{FPS: 0.001}, "below the 0.01 minimum"},
		{"negative queue depth", Options{QueueDepth: -1}, "negative QueueDepth"},
		{"queue depth too small", Options{QueueDepth: 2}, "below the minimum"},
		{"exclude windows", Options{ExcludeWindows: []uint32{1}}, "ExcludeWindows is not supported"},
	} {
		err := tc.opt.Validate()
		if !errors.Is(err, ErrInvalidOption) {
			t.Errorf("%s: Validate reported %v, want ErrInvalidOption", tc.name, err)
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Validate reported %v, want an error mentioning %q", tc.name, err, tc.want)
		}
	}
}

func TestOptionsResolve(t *testing.T) {
	r, err := Options{}.resolve(1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if r.Width != 1920 || r.Height != 1080 || r.FPS != DefaultFPS || r.QueueDepth != DefaultQueueDepth {
		t.Errorf("resolved zero Options = %+v", r)
	}
	// An explicit size, rate and depth survive untouched.
	r2, err := Options{Width: 640, Height: 480, FPS: 24, QueueDepth: 4}.resolve(1920, 1080)
	if err != nil {
		t.Fatal(err)
	}
	if r2.Width != 640 || r2.Height != 480 || r2.FPS != 24 || r2.QueueDepth != 4 {
		t.Errorf("resolved explicit Options = %+v", r2)
	}
	// An invalid Options never resolves.
	if _, err := (Options{FPS: -1}).resolve(100, 100); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("resolve accepted invalid options: %v", err)
	}
	// A source with no size and no requested size cannot resolve.
	for _, wh := range [][2]int{{0, 0}, {0, 10}, {10, 0}, {-1, -1}} {
		if _, err := (Options{}).resolve(wh[0], wh[1]); !errors.Is(err, ErrInvalidOption) {
			t.Errorf("resolve against a %dx%d source reported %v", wh[0], wh[1], err)
		}
	}
}

func TestTickInterval(t *testing.T) {
	// The interval is the rounded reciprocal, so it may sit one nanosecond
	// off the exact ratio; anything more would drift.
	if got := tickInterval(60); got < time.Second/60 || got > time.Second/60+1 {
		t.Errorf("tickInterval(60) = %v, want %v", got, time.Second/60)
	}
	if got := tickInterval(0); got != tickInterval(DefaultFPS) {
		t.Errorf("tickInterval(0) = %v, want the default rate's interval", got)
	}
	if got := tickInterval(-5); got != tickInterval(DefaultFPS) {
		t.Errorf("tickInterval(-5) = %v", got)
	}
	// A rate faster than a microsecond clamps rather than becoming zero,
	// which would spin a ticker at zero interval and panic.
	if got := tickInterval(10_000_000); got != time.Microsecond {
		t.Errorf("tickInterval(1e7) = %v, want 1µs", got)
	}
}

// testFrame builds a w×h BGRA frame at the given stride with a recognisable
// pattern, so a shear or an off-by-one row shows up as wrong bytes.
func testFrame(w, h, stride int) Frame {
	pix := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := y*stride + x*4
			pix[o+0] = byte(x)     // blue
			pix[o+1] = byte(y)     // green
			pix[o+2] = byte(x + y) // red
			pix[o+3] = byte(0x80)  // alpha
		}
		// Poison the padding, so anything that reads it is caught.
		for i := y*stride + w*4; i < (y+1)*stride; i++ {
			pix[i] = 0xee
		}
	}
	return Frame{Pix: pix, Width: w, Height: h, Stride: stride, Seq: 7,
		At: time.Unix(1700000000, 0)}
}

func TestFrameValidity(t *testing.T) {
	f := testFrame(4, 3, 32)
	if !f.Valid() {
		t.Fatal("a well-formed frame reported itself invalid")
	}
	if f.Format() != FormatBGRA {
		t.Errorf("Format() = %v", f.Format())
	}
	if f.TightLen() != 4*4*3 {
		t.Errorf("TightLen = %d", f.TightLen())
	}
	for _, bad := range []Frame{
		{},
		{Width: 0, Height: 3, Stride: 32, Pix: make([]byte, 96)},
		{Width: 4, Height: 0, Stride: 32, Pix: make([]byte, 96)},
		{Width: 4, Height: 3, Stride: 8, Pix: make([]byte, 96)}, // stride below width*4
		{Width: 4, Height: 3, Stride: 32, Pix: make([]byte, 8)}, // too few bytes
	} {
		if bad.Valid() {
			t.Errorf("%+v reported itself valid", bad)
		}
	}
}

func TestFrameRowIgnoresPadding(t *testing.T) {
	// The contract that matters: Stride is carried, and Row hands back
	// exactly Width*4 bytes with the padding trimmed off. A consumer that
	// assumed Width*4 was the stride would shear the picture.
	f := testFrame(4, 3, 32)
	for y := 0; y < 3; y++ {
		row := f.Row(y)
		if len(row) != 16 {
			t.Fatalf("row %d is %d bytes, want 16", y, len(row))
		}
		for x := 0; x < 4; x++ {
			if row[x*4] != byte(x) || row[x*4+1] != byte(y) {
				t.Fatalf("row %d pixel %d = % x", y, x, row[x*4:x*4+4])
			}
		}
		// Row must not expose the padding, even by capacity: appending to it
		// must not scribble on the next row.
		if cap(row) != 16 {
			t.Errorf("row %d has capacity %d; the padding is reachable", y, cap(row))
		}
	}
	if f.Row(-1) != nil || f.Row(3) != nil {
		t.Error("Row accepted an out-of-range y")
	}
	if (Frame{}).Row(0) != nil {
		t.Error("Row on an invalid frame returned data")
	}
}

func TestFrameCopyTight(t *testing.T) {
	// The padded case: CopyTight must skip the padding row by row.
	f := testFrame(4, 3, 32)
	dst := make([]byte, f.TightLen())
	n, err := f.CopyTight(dst)
	if err != nil || n != f.TightLen() {
		t.Fatalf("CopyTight = %d, %v", n, err)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			o := (y*4 + x) * 4
			if dst[o] != byte(x) || dst[o+1] != byte(y) || dst[o+2] != byte(x+y) {
				t.Fatalf("tight pixel (%d,%d) = % x", x, y, dst[o:o+4])
			}
		}
	}
	// The unpadded case takes the single-copy fast path and must agree.
	g := testFrame(4, 3, 16)
	dst2 := make([]byte, g.TightLen())
	if _, err := g.CopyTight(dst2); err != nil {
		t.Fatal(err)
	}
	for i := range dst2 {
		if dst2[i] != dst[i] {
			t.Fatalf("the padded and unpadded copies differ at byte %d", i)
		}
	}

	if _, err := f.CopyTight(make([]byte, 4)); !errors.Is(err, ErrShortBuffer) {
		t.Errorf("CopyTight into a short buffer reported %v, want ErrShortBuffer", err)
	}
	if _, err := (Frame{}).CopyTight(dst); !errors.Is(err, ErrNoFrame) {
		t.Errorf("CopyTight on an invalid frame reported %v, want ErrNoFrame", err)
	}
}

func TestFrameCopyTightDoesNotAllocate(t *testing.T) {
	f := testFrame(64, 64, 64*4+16)
	dst := make([]byte, f.TightLen())
	if got := testing.AllocsPerRun(50, func() { _, _ = f.CopyTight(dst) }); got != 0 {
		t.Errorf("CopyTight allocated %v times per run", got)
	}
}

func TestFrameNRGBA(t *testing.T) {
	f := testFrame(4, 3, 32)
	img, err := f.NRGBA()
	if err != nil {
		t.Fatal(err)
	}
	if img.Rect.Dx() != 4 || img.Rect.Dy() != 3 {
		t.Fatalf("image is %v", img.Rect)
	}
	for y := 0; y < 3; y++ {
		for x := 0; x < 4; x++ {
			o := y*img.Stride + x*4
			// BGRA in, RGBA out, alpha forced opaque because an X11 screen
			// capture carries no meaningful alpha.
			if img.Pix[o+0] != byte(x+y) || img.Pix[o+1] != byte(y) ||
				img.Pix[o+2] != byte(x) || img.Pix[o+3] != 0xff {
				t.Fatalf("pixel (%d,%d) = % x", x, y, img.Pix[o:o+4])
			}
		}
	}
	if _, err := (Frame{}).NRGBA(); !errors.Is(err, ErrNoFrame) {
		t.Errorf("NRGBA on an invalid frame reported %v, want ErrNoFrame", err)
	}
}

func TestStatsFPS(t *testing.T) {
	if got := (Stats{Interval: time.Second / 60}).FPS(); got < 59.9 || got > 60.1 {
		t.Errorf("FPS() = %v, want ~60", got)
	}
	if got := (Stats{}).FPS(); got != 0 {
		t.Errorf("FPS() with no interval = %v", got)
	}
	if got := (Stats{Interval: -time.Second}).FPS(); got != 0 {
		t.Errorf("FPS() with a negative interval = %v", got)
	}
}

func TestSentinelsAreDistinctAndDescriptive(t *testing.T) {
	for _, err := range []error{
		ErrUnsupported, ErrNoBackend, ErrPortalPipeWire, ErrPermissionDenied,
		ErrNoDisplay, ErrNotFound, ErrClosed, ErrNoFrame, ErrInvalidOption,
		ErrShortBuffer, ErrProtocol,
	} {
		if !strings.HasPrefix(err.Error(), "screencast: ") {
			t.Errorf("%v does not name the package", err)
		}
	}
	// The PipeWire wall must SAY it is PipeWire, so a user reading the error
	// knows exactly what is missing rather than guessing.
	if !strings.Contains(ErrPortalPipeWire.Error(), "PipeWire") ||
		!strings.Contains(ErrPortalPipeWire.Error(), "portal") {
		t.Errorf("ErrPortalPipeWire = %q", ErrPortalPipeWire)
	}
	if errors.Is(ErrNoBackend, ErrPortalPipeWire) {
		t.Error("two distinct sentinels compare equal")
	}
	// A wrapped sentinel still matches, which is the whole contract.
	wrapped := fmt.Errorf("%w: extra", ErrNotFound)
	if !errors.Is(wrapped, ErrNotFound) {
		t.Error("a wrapped ErrNotFound stopped matching")
	}
}
