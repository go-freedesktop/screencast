// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"fmt"

	xproto "github.com/go-freedesktop/x11"
)

// XFIXES is how a capture gets the CURSOR. The X server does not draw the
// pointer into the framebuffer — it is a hardware overlay or a software sprite
// composited at scan-out — so GetImage never sees it. XFixesGetCursorImage
// hands back the current cursor's ARGB bitmap, its hotspot and where it is,
// and the capture composites it in itself.

// XfixesName is the extension name passed to QueryExtension.
const XfixesName = "XFIXES"

// XFIXES minor opcodes.
const (
	xfReqQueryVersion   = 0
	xfReqGetCursorImage = 25
)

// Xfixes is a queried XFIXES handle.
type Xfixes struct {
	c        *Conn
	major    byte
	VerMajor uint32
	VerMinor uint32
}

// QueryXfixes queries XFIXES and negotiates version 4, the first that carries
// GetCursorImage's full reply. It returns (nil, nil) when the server has no
// XFIXES.
func (c *Conn) QueryXfixes() (*Xfixes, error) {
	present, major, _, _, err := c.QueryExtension(XfixesName)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	e := xproto.NewEncoder(c.order)
	e.Put32(4)
	e.Put32(0)
	hdr, _, err := c.roundTrip("XFixesQueryVersion", major, xfReqQueryVersion, e.Bytes())
	if err != nil {
		return nil, err
	}
	return &Xfixes{
		c:        c,
		major:    major,
		VerMajor: c.order.Uint32(hdr[8:12]),
		VerMinor: c.order.Uint32(hdr[12:16]),
	}, nil
}

// CursorImage is the pointer's current appearance and position.
//
// Pix is the cursor bitmap in BGRA byte order with PREMULTIPLIED alpha, which
// is what the extension delivers (it states one CARD32 of ARGB per pixel; read
// in the connection's byte order those bytes land as B,G,R,A on a
// little-endian client). Width*4 bytes per row, no padding.
type CursorImage struct {
	X, Y       int16  // where the cursor's hotspot is, in root coordinates
	Width      uint16 // bitmap size
	Height     uint16
	XHot, YHot uint16 // hotspot within the bitmap
	Serial     uint32 // changes whenever the cursor image changes
	Pix        []byte // Width*Height*4 bytes, BGRA premultiplied
}

// Origin is the top-left corner of the cursor bitmap in root coordinates:
// the hotspot position minus the hotspot offset.
func (ci CursorImage) Origin() (int, int) {
	return int(ci.X) - int(ci.XHot), int(ci.Y) - int(ci.YHot)
}

// decodeCursorImage parses a GetCursorImage reply. The cursor pixels are a
// LISTofCARD32 of ARGB; each is written back into the destination in the
// connection's byte order, which on a little-endian connection puts them down
// as B,G,R,A — the layout this package hands to consumers.
func decodeCursorImage(order ByteOrder, hdr []byte, body []byte) (CursorImage, error) {
	d := xproto.NewDecoder(order, hdr)
	d.Skip(8) // response type, unused, sequence, reply length
	ci := CursorImage{
		X:      d.Get16s(),
		Y:      d.Get16s(),
		Width:  d.Get16(),
		Height: d.Get16(),
		XHot:   d.Get16(),
		YHot:   d.Get16(),
		Serial: d.Get32(),
	}
	if !d.OK() {
		return CursorImage{}, fmt.Errorf("x11: XFixesGetCursorImage: truncated reply header")
	}
	n := int(ci.Width) * int(ci.Height)
	if n*4 > len(body) {
		return CursorImage{}, fmt.Errorf(
			"x11: XFixesGetCursorImage: %dx%d cursor needs %d bytes, reply carried %d",
			ci.Width, ci.Height, n*4, len(body))
	}
	ci.Pix = make([]byte, n*4)
	for i := 0; i < n; i++ {
		v := order.Uint32(body[i*4:])
		order.PutUint32(ci.Pix[i*4:], v)
	}
	return ci, nil
}

// GetCursorImage reads the pointer's current bitmap and position.
func (x *Xfixes) GetCursorImage() (CursorImage, error) {
	hdr, extra, err := x.c.roundTrip("XFixesGetCursorImage", x.major, xfReqGetCursorImage, nil)
	if err != nil {
		return CursorImage{}, err
	}
	return decodeCursorImage(x.c.order, hdr[:], extra)
}
