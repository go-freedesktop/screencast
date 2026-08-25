// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

// Command sccheck probes what github.com/go-freedesktop/screencast can capture
// on this machine, and proves it by capturing.
//
// It prints the session kind, whether a backend is reachable, the displays and
// windows it can see, and then — unless -list is given — runs a real capture
// and reports what it measured: frames per second, milliseconds per frame,
// allocations per Frame call, and whether the pixels actually CHANGED between
// frames. A static grey buffer is the classic silent failure of a screen
// capture, so "the content changed" is checked rather than assumed.
//
// With -png it writes one captured frame out, which is the artefact a human
// can look at.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"image/png"
	"io"
	"os"
	"testing"
	"time"

	"github.com/go-freedesktop/screencast"
)

// osExit is the process-exit seam, so run can be tested without taking the
// test binary down with it.
var osExit = os.Exit

func main() { osExit(run(os.Args[1:], os.Stdout, os.Stderr)) }

// run is the whole program. It reports the process exit status.
func run(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("sccheck", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var (
		list   = fs.Bool("list", false, "only list displays and windows, capture nothing")
		frames = fs.Int("frames", 60, "how many frames to capture")
		fps    = fs.Float64("fps", 60, "capture rate")
		width  = fs.Int("width", 0, "resample frames to this width (0 = native)")
		height = fs.Int("height", 0, "resample frames to this height (0 = native)")
		cursor = fs.Bool("cursor", false, "composite the mouse pointer into the frames")
		slow   = fs.Bool("slow", false, "force the core GetImage path instead of MIT-SHM")
		rawA   = fs.Bool("raw-alpha", false,
			"leave the fourth byte as the server wrote it instead of forcing it opaque")
		pngPath = fs.String("png", "", "write one captured frame to this file")
		window  = fs.Uint("window", 0, "capture this window id instead of a display")
		self    = fs.Bool("selftest", false,
			"paint the root window a known colour and check the capture reproduces it exactly")
		display = fs.Uint("display", 0, "capture this display id (default: the primary one)")
	)
	if err := fs.Parse(args); err != nil {
		return 2
	}
	ctx := context.Background()

	fmt.Fprintf(stdout, "backend:   %s\n", screencast.Backend)
	fmt.Fprintf(stdout, "session:   %s\n", screencast.Session())
	fmt.Fprintf(stdout, "available: %v\n", screencast.Available())
	fmt.Fprintf(stdout, "authorized:%v\n", screencast.Authorized())
	if err := screencast.Probe(); err != nil {
		fmt.Fprintf(stdout, "probe:     %v\n", err)
	}
	if err := screencast.Diagnose(); err != nil {
		fmt.Fprintf(stdout, "diagnosis: %v\n", err)
	}

	content, err := screencast.Shareable(ctx)
	if err != nil {
		fmt.Fprintf(stderr, "sccheck: %v\n", err)
		return 1
	}
	fmt.Fprintf(stdout, "\ndisplays (%d):\n", len(content.Displays))
	for _, d := range content.Displays {
		star := " "
		if d.Main {
			star = "*"
		}
		fmt.Fprintf(stdout, " %s %s (id %d, screen %d, root %#x)\n", star, d, d.ID, d.Screen, d.Root)
	}
	fmt.Fprintf(stdout, "windows (%d):\n", len(content.Windows))
	for _, w := range content.Windows {
		fmt.Fprintf(stdout, "   %s pid=%d layer=%d onscreen=%v\n", w, w.PID, w.Layer, w.OnScreen)
	}
	if *list {
		return 0
	}

	opt := screencast.Options{
		Width: *width, Height: *height, FPS: *fps,
		ShowsCursor: *cursor, ForceGetImage: *slow, RawAlpha: *rawA,
	}
	var stream *screencast.Stream
	if *window != 0 {
		w, err := content.Window(uint32(*window))
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: %v\n", err)
			return 1
		}
		stream, err = screencast.CaptureWindow(ctx, w, opt)
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: %v\n", err)
			return 1
		}
	} else {
		d, err := pickDisplay(content, uint32(*display))
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: %v\n", err)
			return 1
		}
		stream, err = screencast.CaptureDisplay(ctx, d, opt)
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: %v\n", err)
			return 1
		}
	}
	defer func() { _ = stream.Close() }()

	fmt.Fprintf(stdout, "\ncapturing %s at %v fps, %d frames\n",
		stream.Source(), stream.Options().FPS, *frames)
	fmt.Fprintf(stdout, "transport: %s (converts=%v)\n", stream.Transport(), stream.Converts())
	// The self-test paints SOLID colours, so a uniform frame is exactly what
	// it expects; the "did anything actually appear on screen" gate belongs
	// to the plain measurement run.
	if rc := measure(ctx, stream, *frames, *pngPath, !*self, stdout, stderr); rc != 0 {
		return rc
	}
	if *self {
		if *window != 0 {
			fmt.Fprintln(stderr, "sccheck: -selftest paints the root window, so it needs a display capture")
			return 2
		}
		d, err := pickDisplay(content, uint32(*display))
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: %v\n", err)
			return 1
		}
		return selfTest(ctx, stream, d.Root, stdout, stderr)
	}
	return 0
}

// pickDisplay resolves the -display flag, defaulting to the primary display.
func pickDisplay(c *screencast.Content, id uint32) (screencast.Display, error) {
	if id != 0 {
		return c.Display(id)
	}
	return c.MainDisplay()
}

// frameSource is the part of [screencast.Stream] the measurement uses. It is
// an interface so the reporting — the arithmetic, the thresholds, the exit
// statuses — is testable without a display; *screencast.Stream satisfies it.
type frameSource interface {
	Frame() (screencast.Frame, bool)
	WaitFrame(ctx context.Context) (screencast.Frame, error)
	Stats() screencast.Stats
}

// measure captures n frames and reports what it saw. It is the part that
// refuses to say "it works" without evidence: it checks that the frame is not
// uniformly one value, and that the pixels change between frames.
func measure(ctx context.Context, s frameSource, n int, pngPath string,
	strict bool, stdout, stderr io.Writer) int {
	deadline, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()

	var (
		got      int
		firstSum uint64
		changed  bool
		prev     []byte
		last     screencast.Frame
	)
	start := time.Now()
	for got < n {
		f, err := s.WaitFrame(deadline)
		if err != nil {
			if errors.Is(err, screencast.ErrNoFrame) && got > 0 {
				break
			}
			fmt.Fprintf(stderr, "sccheck: waiting for frame %d: %v\n", got+1, err)
			return 1
		}
		got++
		last = f
		sum := checksum(f)
		if got == 1 {
			firstSum = sum
			prev = make([]byte, f.TightLen())
		} else if sum != firstSum {
			changed = true
		}
		if _, err := f.CopyTight(prev); err != nil {
			fmt.Fprintf(stderr, "sccheck: CopyTight: %v\n", err)
			return 1
		}
	}
	elapsed := time.Since(start)

	allocs := testing.AllocsPerRun(200, func() { s.Frame() })
	frameNs := benchFrameNs(s)

	fmt.Fprintf(stdout, "frames:    %d in %v  (%.1f fps measured)\n",
		got, elapsed.Round(time.Millisecond), float64(got)/elapsed.Seconds())
	fmt.Fprintf(stdout, "ms/frame:  %.3f\n", float64(elapsed.Microseconds())/float64(got)/1000)
	fmt.Fprintf(stdout, "Frame():   %.1f ns/op, %.0f allocs/op\n", frameNs, allocs)
	fmt.Fprintf(stdout, "geometry:  %dx%d stride=%d (tight would be %d) format=%s\n",
		last.Width, last.Height, last.Stride, last.Width*4, last.Format())
	fmt.Fprintf(stdout, "stats:     %+v\n", s.Stats())
	fmt.Fprintf(stdout, "uniform:   %v\n", isUniform(last))
	fmt.Fprintf(stdout, "changed:   %v\n", changed)

	if pngPath != "" {
		if err := writePNG(pngPath, last); err != nil {
			fmt.Fprintf(stderr, "sccheck: writing %s: %v\n", pngPath, err)
			return 1
		}
		fmt.Fprintf(stdout, "wrote:     %s\n", pngPath)
	}
	if strict && (got == 0 || isUniform(last)) {
		fmt.Fprintln(stderr, "sccheck: the capture produced no frame, or a uniform one — "+
			"either nothing is on this screen, or the capture is silently failing")
		return 1
	}
	return 0
}

// benchFrameNs times Stream.Frame, which is the call a compositor makes every
// frame and which must not allocate.
//
// It keeps doubling the batch until the elapsed time is well past the clock's
// own resolution. A fixed iteration count is not enough: Frame is tens of
// nanoseconds, and on a platform whose clock ticks every 15 ms a batch that
// finishes inside one tick measures as exactly zero.
func benchFrameNs(s frameSource) float64 {
	const (
		minElapsed = 20 * time.Millisecond
		maxIters   = 1 << 24
	)
	for iters := 1 << 12; ; iters *= 2 {
		start := time.Now()
		for i := 0; i < iters; i++ {
			s.Frame()
		}
		elapsed := time.Since(start)
		if elapsed >= minElapsed || iters >= maxIters {
			if elapsed <= 0 {
				return 0
			}
			return float64(elapsed.Nanoseconds()) / float64(iters)
		}
	}
}

// checksum is a cheap content hash of a frame, used only to tell one frame
// from another.
func checksum(f screencast.Frame) uint64 {
	var h uint64 = 1469598103934665603
	for y := 0; y < f.Height; y++ {
		row := f.Row(y)
		for i := 0; i < len(row); i += 64 {
			h ^= uint64(row[i])
			h *= 1099511628211
		}
	}
	return h
}

// isUniform reports whether every pixel of the frame is the same colour, which
// is what a capture that silently failed looks like.
func isUniform(f screencast.Frame) bool {
	if !f.Valid() {
		return true
	}
	first := f.Row(0)[:4]
	for y := 0; y < f.Height; y++ {
		row := f.Row(y)
		for x := 0; x < len(row); x += 4 {
			if row[x] != first[0] || row[x+1] != first[1] || row[x+2] != first[2] {
				return false
			}
		}
	}
	return true
}

// writePNG saves a frame as a PNG.
func writePNG(path string, f screencast.Frame) error {
	img, err := f.NRGBA()
	if err != nil {
		return err
	}
	out, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() { _ = out.Close() }()
	return png.Encode(out, img)
}
