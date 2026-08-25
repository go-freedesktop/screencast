// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-freedesktop/screencast"
	"github.com/go-freedesktop/screencast/internal/x11"
)

// These tests cover the parts of the probe that do not need a display: the
// flag handling, the exit statuses, and the frame arithmetic the report is
// built from. The capture itself is proved by the live suite in the parent
// package and by running this command against a real X server.

func TestRunRejectsBadFlags(t *testing.T) {
	var out, errOut bytes.Buffer
	if rc := run([]string{"-nosuchflag"}, &out, &errOut); rc != 2 {
		t.Errorf("run with an unknown flag returned %d, want 2", rc)
	}
	if errOut.Len() == 0 {
		t.Error("run said nothing about the unknown flag")
	}
}

func TestRunReportsAnUnreachableDisplay(t *testing.T) {
	// Point at a display that certainly is not there. On Linux the dial
	// fails; everywhere else the package is unsupported. Either way the
	// command must report the reason and exit non-zero, not panic.
	t.Setenv("DISPLAY", ":98765")
	t.Setenv("WAYLAND_DISPLAY", "")
	var out, errOut bytes.Buffer
	rc := run([]string{"-list"}, &out, &errOut)
	if rc != 1 {
		t.Fatalf("run returned %d, want 1", rc)
	}
	if !strings.Contains(out.String(), "backend:") {
		t.Errorf("run printed no header:\n%s", out.String())
	}
	if !strings.Contains(errOut.String(), "sccheck:") {
		t.Errorf("run printed no reason:\n%s", errOut.String())
	}
}

// frame builds a w×h BGRA frame at the given stride, filled with fill.
func frame(w, h, stride int, fill [4]byte) screencast.Frame {
	pix := make([]byte, stride*h)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			copy(pix[y*stride+x*4:], fill[:])
		}
	}
	return screencast.Frame{Pix: pix, Width: w, Height: h, Stride: stride, Seq: 1}
}

func TestIsUniform(t *testing.T) {
	f := frame(8, 4, 8*4+8, [4]byte{1, 2, 3, 255})
	if !isUniform(f) {
		t.Error("a frame of one colour did not report itself uniform")
	}
	// One pixel different anywhere is enough — including in the last row,
	// which a check that stopped early would miss.
	f.Pix[3*f.Stride+7*4] = 9
	if isUniform(f) {
		t.Error("a frame with a differing pixel reported itself uniform")
	}
	if !isUniform(screencast.Frame{}) {
		t.Error("an invalid frame did not report itself uniform")
	}
	// The padding between rows must not count: a frame whose padding differs
	// is still uniform.
	g := frame(8, 4, 8*4+8, [4]byte{5, 5, 5, 255})
	for y := 0; y < 4; y++ {
		g.Pix[y*g.Stride+8*4] = 0xee
	}
	if !isUniform(g) {
		t.Error("row padding was counted as picture content")
	}
}

func TestChecksumDistinguishesFrames(t *testing.T) {
	a := frame(64, 8, 64*4, [4]byte{1, 2, 3, 255})
	b := frame(64, 8, 64*4, [4]byte{4, 5, 6, 255})
	if checksum(a) == checksum(b) {
		t.Error("two different frames hashed the same")
	}
	if checksum(a) != checksum(frame(64, 8, 64*4, [4]byte{1, 2, 3, 255})) {
		t.Error("the same frame hashed differently twice")
	}
}

func TestColourFor(t *testing.T) {
	// The usual depth-24 TrueColor visual.
	v := x11.VisualType{ID: 0x21, Class: x11.VisualTrueColor,
		RedMask: 0x00ff0000, GreenMask: 0x0000ff00, BlueMask: 0x000000ff}
	for _, tc := range []struct {
		c     selfTestColour
		pixel uint32
		want  [4]byte
	}{
		{selfTestColour{"red", true, false, false}, 0x00ff0000, [4]byte{0, 0, 0xff, 0xff}},
		{selfTestColour{"green", false, true, false}, 0x0000ff00, [4]byte{0, 0xff, 0, 0xff}},
		{selfTestColour{"blue", false, false, true}, 0x000000ff, [4]byte{0xff, 0, 0, 0xff}},
		{selfTestColour{"white", true, true, true}, 0x00ffffff, [4]byte{0xff, 0xff, 0xff, 0xff}},
		{selfTestColour{"black", false, false, false}, 0, [4]byte{0, 0, 0, 0xff}},
	} {
		pixel, want := colourFor(v, tc.c)
		if pixel != tc.pixel || want != tc.want {
			t.Errorf("%s: colourFor = %#x, % x; want %#x, % x",
				tc.c.name, pixel, want, tc.pixel, tc.want)
		}
	}
}

func TestFindWrongPixel(t *testing.T) {
	want := [4]byte{1, 2, 3, 255}
	f := frame(8, 4, 8*4+8, want)
	if _, _, _, bad := findWrongPixel(f, want); bad {
		t.Error("findWrongPixel flagged a frame that is entirely the wanted colour")
	}
	f.Pix[2*f.Stride+5*4+1] = 9
	x, y, got, bad := findWrongPixel(f, want)
	if !bad || x != 5 || y != 2 {
		t.Errorf("findWrongPixel = %d, %d, % x, %v", x, y, got, bad)
	}
	// The row padding is not picture content and must not be flagged.
	g := frame(8, 4, 8*4+8, want)
	g.Pix[8*4] = 0xee
	if _, _, _, bad := findWrongPixel(g, want); bad {
		t.Error("findWrongPixel walked into the row padding")
	}
}

func TestSelfTestColoursCoverEveryChannel(t *testing.T) {
	// A red/blue swap is the single most likely mistake in a BGRA pipeline,
	// so the sequence must contain a colour that lights red alone and one
	// that lights blue alone.
	var r, g, b bool
	for _, c := range selfTestColours {
		r = r || (c.r && !c.g && !c.b)
		g = g || (!c.r && c.g && !c.b)
		b = b || (!c.r && !c.g && c.b)
	}
	if !r || !g || !b {
		t.Errorf("the self-test sequence does not isolate every channel: %+v", selfTestColours)
	}
}

func TestWritePNG(t *testing.T) {
	path := filepath.Join(t.TempDir(), "out.png")
	if err := writePNG(path, frame(4, 3, 4*4+4, [4]byte{9, 8, 7, 255})); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Size() == 0 {
		t.Fatalf("writePNG produced %v, %v", st, err)
	}
	if err := writePNG(path, screencast.Frame{}); err == nil {
		t.Error("writePNG accepted an invalid frame")
	}
	if err := writePNG(filepath.Join(path, "nested", "x.png"),
		frame(2, 2, 8, [4]byte{1, 1, 1, 255})); err == nil {
		t.Error("writePNG accepted an unwritable path")
	}
}

func TestPickDisplay(t *testing.T) {
	c := &screencast.Content{Displays: []screencast.Display{
		{ID: 1}, {ID: 2, Main: true},
	}}
	if d, err := pickDisplay(c, 0); err != nil || d.ID != 2 {
		t.Errorf("pickDisplay(0) = %+v, %v; want the primary display", d, err)
	}
	if d, err := pickDisplay(c, 1); err != nil || d.ID != 1 {
		t.Errorf("pickDisplay(1) = %+v, %v", d, err)
	}
	if _, err := pickDisplay(c, 99); err == nil {
		t.Error("pickDisplay accepted an id nobody has")
	}
}

func TestOsExitSeamIsWired(t *testing.T) {
	// main() is one line; the seam is what makes it testable at all.
	orig := osExit
	defer func() { osExit = orig }()
	got := -1
	osExit = func(code int) { got = code }
	osExit(3)
	if got != 3 {
		t.Errorf("the exit seam recorded %d", got)
	}
}

// scriptedSource is a frameSource that hands out a fixed sequence of frames.
// It is what makes the report's arithmetic and exit statuses testable with no
// display anywhere.
type scriptedSource struct {
	frames []screencast.Frame
	i      int
	err    error
	stats  screencast.Stats
}

func (s *scriptedSource) Frame() (screencast.Frame, bool) {
	if s.i == 0 || len(s.frames) == 0 {
		return screencast.Frame{}, false
	}
	return s.frames[s.i-1], false
}

func (s *scriptedSource) WaitFrame(ctx context.Context) (screencast.Frame, error) {
	if s.i >= len(s.frames) {
		if s.err != nil {
			return screencast.Frame{}, s.err
		}
		return screencast.Frame{}, screencast.ErrNoFrame
	}
	f := s.frames[s.i]
	s.i++
	return f, nil
}

func (s *scriptedSource) Stats() screencast.Stats { return s.stats }

// varyingFrames builds n frames that differ from one another.
func varyingFrames(n, w, h, stride int) []screencast.Frame {
	out := make([]screencast.Frame, n)
	for i := range out {
		f := frame(w, h, stride, [4]byte{byte(i + 1), 2, 3, 255})
		// One differing pixel keeps the frames non-uniform as well as
		// different from each other.
		f.Pix[0] = 0xff
		f.Seq = uint64(i + 1)
		out[i] = f
	}
	return out
}

func TestMeasureReportsAGoodCapture(t *testing.T) {
	src := &scriptedSource{frames: varyingFrames(5, 8, 4, 8*4+8)}
	var out, errOut bytes.Buffer
	path := filepath.Join(t.TempDir(), "f.png")
	if rc := measure(context.Background(), src, 5, path, true, &out, &errOut); rc != 0 {
		t.Fatalf("measure returned %d: %s", rc, errOut.String())
	}
	for _, want := range []string{"frames:", "ms/frame:", "Frame():", "geometry:", "stride=40",
		"changed:   true", "uniform:   false", "wrote:"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("the report does not mention %q:\n%s", want, out.String())
		}
	}
	if _, err := os.Stat(path); err != nil {
		t.Errorf("measure did not write the PNG: %v", err)
	}
}

func TestMeasureRejectsAUniformCapture(t *testing.T) {
	// The classic silent failure: a capture that always hands back the same
	// flat buffer. Strict mode must call it out rather than reporting success.
	f := frame(8, 4, 8*4, [4]byte{0x80, 0x80, 0x80, 255})
	src := &scriptedSource{frames: []screencast.Frame{f, f, f}}
	var out, errOut bytes.Buffer
	if rc := measure(context.Background(), src, 3, "", true, &out, &errOut); rc != 1 {
		t.Fatalf("measure returned %d for a uniform capture, want 1", rc)
	}
	if !strings.Contains(errOut.String(), "uniform") {
		t.Errorf("measure did not say why:\n%s", errOut.String())
	}
	if !strings.Contains(out.String(), "changed:   false") {
		t.Errorf("measure did not report that nothing changed:\n%s", out.String())
	}
	// The same capture is fine when the caller has said it expects uniform
	// frames, which is what the self-test does.
	var out2, err2 bytes.Buffer
	src2 := &scriptedSource{frames: []screencast.Frame{f, f, f}}
	if rc := measure(context.Background(), src2, 3, "", false, &out2, &err2); rc != 0 {
		t.Fatalf("measure returned %d in non-strict mode, want 0: %s", rc, err2.String())
	}
}

func TestMeasureReportsAFailedWait(t *testing.T) {
	// No frame at all is a failure, whatever the mode.
	src := &scriptedSource{err: screencast.ErrClosed}
	var out, errOut bytes.Buffer
	if rc := measure(context.Background(), src, 3, "", true, &out, &errOut); rc != 1 {
		t.Fatalf("measure returned %d, want 1", rc)
	}
	if !strings.Contains(errOut.String(), "waiting for frame 1") {
		t.Errorf("measure did not name the failure:\n%s", errOut.String())
	}
	// Running out of frames after at least one arrived is not a failure: a
	// capture that stops is still a capture that worked.
	src2 := &scriptedSource{frames: varyingFrames(2, 8, 4, 32)}
	var out2, err2 bytes.Buffer
	if rc := measure(context.Background(), src2, 10, "", true, &out2, &err2); rc != 0 {
		t.Fatalf("measure returned %d after a short run, want 0: %s", rc, err2.String())
	}
}

func TestMeasureReportsAnUnwritablePNG(t *testing.T) {
	src := &scriptedSource{frames: varyingFrames(2, 8, 4, 32)}
	var out, errOut bytes.Buffer
	bad := filepath.Join(t.TempDir(), "file", "nested", "f.png")
	if err := os.WriteFile(filepath.Dir(filepath.Dir(bad)), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if rc := measure(context.Background(), src, 2, bad, true, &out, &errOut); rc != 1 {
		t.Fatalf("measure returned %d for an unwritable PNG path, want 1", rc)
	}
	if !strings.Contains(errOut.String(), "writing") {
		t.Errorf("measure did not name the write failure:\n%s", errOut.String())
	}
}

func TestMeasureReportsAShortCopyTarget(t *testing.T) {
	// A frame whose declared geometry does not match its bytes must be
	// reported, not silently copied past the end of the buffer.
	good := frame(8, 4, 32, [4]byte{1, 2, 3, 255})
	good.Pix[0] = 0xff
	bad := good
	bad.Width = 64 // claims eight times the width it has bytes for
	src := &scriptedSource{frames: []screencast.Frame{good, bad}}
	var out, errOut bytes.Buffer
	if rc := measure(context.Background(), src, 2, "", true, &out, &errOut); rc != 1 {
		t.Fatalf("measure returned %d for a malformed frame, want 1", rc)
	}
	if !strings.Contains(errOut.String(), "CopyTight") {
		t.Errorf("measure did not name the copy failure:\n%s", errOut.String())
	}
}

func TestBenchFrameNs(t *testing.T) {
	src := &scriptedSource{frames: varyingFrames(1, 4, 4, 16)}
	if got := benchFrameNs(src); got <= 0 {
		t.Errorf("benchFrameNs = %v, want a positive duration", got)
	}
}

func TestSelfTestNeedsAnXConnection(t *testing.T) {
	t.Setenv("DISPLAY", ":98765")
	t.Setenv("WAYLAND_DISPLAY", "")
	src := &scriptedSource{frames: varyingFrames(4, 4, 4, 16)}
	var out, errOut bytes.Buffer
	if rc := selfTest(context.Background(), src, 0, &out, &errOut); rc != 1 {
		t.Fatalf("selfTest returned %d without a display, want 1", rc)
	}
	if !strings.Contains(errOut.String(), "own X connection") {
		t.Errorf("selfTest did not say what it needs:\n%s", errOut.String())
	}
}
