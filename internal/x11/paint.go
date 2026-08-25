// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

// This file holds the only two WRITE requests in the package.
//
// A capture library has no business drawing, and nothing on the capture path
// calls either of these. They exist so a caller can PROVE a capture is real:
// paint the root window a known colour, capture it, and check that the bytes
// that come back are that colour, in that channel order, at that stride. That
// turns "the frame is not all zeroes" — which a broken capture can pass by
// accident — into "the frame is exactly the colour I just put on the screen",
// which it cannot.
//
// See the self-test in cmd/sccheck and the integration suite.

// The core opcodes used here. They are the only ones in the package that
// change anything on the server.
const (
	opCreateWindow           = 1
	opChangeWindowAttributes = 2
	opDestroyWindow          = 4
	opMapWindow              = 8
	opClearArea              = 61
)

// Window-attribute value-mask bits. The value list that follows a mask is
// ordered by ASCENDING bit, which is why back-pixel is written before
// override-redirect.
const (
	cwBackPixel        = 0x00000002
	cwOverrideRedirect = 0x00000200
)

// classInputOutput is the CreateWindow class of a window that has pixels.
const classInputOutput = 1

// CopyFromParent tells CreateWindow to inherit the parent's depth and visual,
// which sidesteps every BadMatch a mismatched visual could raise.
const CopyFromParent = 0

// SetWindowBackground sets a window's background to a solid pixel value. The
// pixel is in the window's visual's own encoding — for the usual depth-24
// TrueColor visual that is 0x00RRGGBB.
//
// Nothing changes on screen until the window is repainted; see [Conn.ClearArea].
func (c *Conn) SetWindowBackground(window, pixel uint32) error {
	e := newEncoder(c.order)
	e.put32(window)
	e.put32(cwBackPixel)
	e.put32(pixel)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeRequest(opChangeWindowAttributes, 0, e.buf)
}

// ClearArea repaints a rectangle of a window with its background. A zero width
// or height means "to the far edge", so ClearArea(w, 0, 0, 0, 0) repaints the
// whole window.
func (c *Conn) ClearArea(window uint32, x, y int16, w, h uint16) error {
	e := newEncoder(c.order)
	e.put32(window)
	e.put16(uint16(x))
	e.put16(uint16(y))
	e.put16(w)
	e.put16(h)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeRequest(opClearArea, 0, e.buf)
}

// Sync waits until the server has processed every request sent so far. It
// works by making a round trip — any request with a reply will do — so when it
// returns, everything before it has been executed.
//
// A paint followed by a capture needs it: both are requests on the same
// connection, so they are ordered, but a capture on a DIFFERENT connection has
// no such guarantee.
func (c *Conn) Sync() error {
	_, err := c.InternAtom("_GO_FREEDESKTOP_SCREENCAST_SYNC", false)
	return err
}

// CreateSolidWindow creates a mapped, override-redirect child of parent, of
// the given geometry, filled with a solid pixel value in the parent's own
// visual. The caller frees it with [Conn.DestroyWindow].
//
// Override-redirect keeps the window manager out of it: the window appears
// exactly where and at exactly the size it was asked for, with no frame and no
// reparenting, which is what a test needs to make an assertion about pixels.
//
// Like the rest of this file it exists only so a caller can prove a capture is
// real. Nothing on the capture path creates a window.
func (c *Conn) CreateSolidWindow(parent uint32, x, y int16, w, h uint16, pixel uint32) (uint32, error) {
	wid := c.NewID()
	e := newEncoder(c.order)
	e.put32(wid)
	e.put32(parent)
	e.put16(uint16(x))
	e.put16(uint16(y))
	e.put16(w)
	e.put16(h)
	e.put16(0) // border-width
	e.put16(classInputOutput)
	e.put32(CopyFromParent) // visual
	e.put32(cwBackPixel | cwOverrideRedirect)
	e.put32(pixel)
	e.put32(1) // override-redirect
	c.mu.Lock()
	err := c.writeRequest(opCreateWindow, CopyFromParent, e.buf)
	c.mu.Unlock()
	if err != nil {
		return 0, err
	}
	if err := c.MapWindow(wid); err != nil {
		return 0, err
	}
	return wid, c.Sync()
}

// MapWindow makes a window visible.
func (c *Conn) MapWindow(window uint32) error {
	e := newEncoder(c.order)
	e.put32(window)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeRequest(opMapWindow, 0, e.buf)
}

// DestroyWindow removes a window and everything below it.
func (c *Conn) DestroyWindow(window uint32) error {
	e := newEncoder(c.order)
	e.put32(window)
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writeRequest(opDestroyWindow, 0, e.buf)
}
