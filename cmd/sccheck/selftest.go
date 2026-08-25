// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/go-freedesktop/screencast"
	"github.com/go-freedesktop/screencast/internal/x11"
)

// The self-test turns "the capture produced something" into "the capture
// produced exactly what I put on the screen".
//
// "The frame is not all zeroes" is a weak assertion: a capture that read the
// wrong drawable, that swapped red and blue, or that sheared every row because
// it assumed the stride, would pass it. So the self-test PAINTS: it sets the
// root window's background to a known colour over its own X connection,
// repaints it, and then checks that the frame the capture hands back is that
// colour, in that channel order, at the stride it declared — and that painting
// the next colour is SEEN.
//
// The colours are built from the ROOT VISUAL'S OWN MASKS, with each channel
// either all-ones or all-zeroes. That is what makes the check exact on every
// visual: a full-scale channel widens to 255 and an empty one to 0 whatever
// the channel's bit width, so the same assertion holds on a 5-6-5 visual, an
// 8-8-8 one and a 10-10-10 one, and any confusion between red and blue shows
// up immediately.

// selfTestColour is one painted step: a name, and which channels are lit.
type selfTestColour struct {
	name    string
	r, g, b bool
}

// selfTestColours are the steps, in order. Pure red, green and blue catch a
// channel swap; white and black catch a stuck buffer.
var selfTestColours = []selfTestColour{
	{"red", true, false, false},
	{"green", false, true, false},
	{"blue", false, false, true},
	{"white", true, true, true},
	{"black", false, false, false},
}

// selfTest paints and verifies. It reports the process exit status.
func selfTest(ctx context.Context, s frameSource, root uint32, stdout, stderr io.Writer) int {
	paint, _, err := x11.Dial("")
	if err != nil {
		fmt.Fprintf(stderr, "sccheck: the self-test needs its own X connection: %v\n", err)
		return 1
	}
	defer func() { _ = paint.Close() }()

	setup := paint.Setup()
	screen := setup.ScreenOf(0)
	for i := range setup.Screens {
		if setup.Screens[i].Root == root {
			screen = setup.ScreenOf(i)
		}
	}
	if screen == nil {
		fmt.Fprintln(stderr, "sccheck: the server lists no screen to paint on")
		return 1
	}
	if root == 0 {
		root = screen.Root
	}
	visual := screen.RootVisualType()
	fmt.Fprintf(stdout, "self-test: painting root %#x, visual %#x masks r=%#x g=%#x b=%#x\n",
		root, visual.ID, visual.RedMask, visual.GreenMask, visual.BlueMask)

	ok := true
	for _, c := range selfTestColours {
		pixel, want := colourFor(visual, c)
		if err := paintRoot(paint, root, pixel); err != nil {
			fmt.Fprintf(stderr, "sccheck: painting %s: %v\n", c.name, err)
			return 1
		}
		f, err := freshFrameAfterPaint(ctx, s)
		if err != nil {
			fmt.Fprintf(stderr, "sccheck: waiting for the repaint in %s: %v\n", c.name, err)
			return 1
		}
		if x, y, got, bad := findWrongPixel(f, want); bad {
			fmt.Fprintf(stdout, "self-test %-5s (pixel %#08x): FAIL at (%d,%d): got % x, want % x\n",
				c.name, pixel, x, y, got, want)
			ok = false
			continue
		}
		fmt.Fprintf(stdout, "self-test %-5s (pixel %#08x): all %d pixels are % x, stride %d, seq %d\n",
			c.name, pixel, f.Width*f.Height, want, f.Stride, f.Seq)
	}
	if !ok {
		fmt.Fprintln(stderr, "sccheck: the capture did not reproduce what was painted")
		return 1
	}
	fmt.Fprintln(stdout, "self-test: PASS — the capture reproduces painted colours exactly, "+
		"in BGRA order, at the declared stride, and sees every change")
	return 0
}

// colourFor builds the pixel value to paint in the visual's own encoding, and
// the BGRA bytes the capture must hand back for it.
func colourFor(v x11.VisualType, c selfTestColour) (pixel uint32, want [4]byte) {
	if c.r {
		pixel |= v.RedMask
		want[2] = 0xff
	}
	if c.g {
		pixel |= v.GreenMask
		want[1] = 0xff
	}
	if c.b {
		pixel |= v.BlueMask
		want[0] = 0xff
	}
	want[3] = 0xff // an X11 screen capture is opaque
	return pixel, want
}

// paintRoot fills the root window with a solid pixel and waits for the server
// to have done it.
func paintRoot(c *x11.Conn, root, pixel uint32) error {
	if err := c.SetWindowBackground(root, pixel); err != nil {
		return err
	}
	if err := c.ClearArea(root, 0, 0, 0, 0); err != nil {
		return err
	}
	return c.Sync()
}

// freshFrameAfterPaint waits for a frame captured strictly after the paint. It
// drops the first two frames it is offered: one may have been in flight while
// the paint was happening, since the capture loop and the paint connection are
// not synchronised with each other.
func freshFrameAfterPaint(ctx context.Context, s frameSource) (screencast.Frame, error) {
	deadline, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	var f screencast.Frame
	for i := 0; i < 3; i++ {
		var err error
		f, err = s.WaitFrame(deadline)
		if err != nil {
			return screencast.Frame{}, err
		}
	}
	return f, nil
}

// findWrongPixel returns the first pixel that is not want, and whether one was
// found. It walks with the frame's STRIDE, which is the whole point: a
// consumer that used Width*4 instead would read into the row padding and
// disagree with itself further down the screen.
func findWrongPixel(f screencast.Frame, want [4]byte) (x, y int, got []byte, bad bool) {
	for y := 0; y < f.Height; y++ {
		row := f.Row(y)
		for x := 0; x < f.Width; x++ {
			p := row[x*4 : x*4+4]
			if p[0] != want[0] || p[1] != want[1] || p[2] != want[2] || p[3] != want[3] {
				return x, y, p, true
			}
		}
	}
	return 0, 0, nil, false
}
