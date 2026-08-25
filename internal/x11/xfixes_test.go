// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"strings"
	"testing"
)

// cursorReply builds a GetCursorImage reply for a w×h cursor whose pixels
// count up from 1.
func cursorReply(order ByteOrder, x, y int16, w, h, xhot, yhot uint16, serial uint32) []byte {
	fixed := make([]byte, 24)
	order.PutUint16(fixed[0:2], uint16(x))
	order.PutUint16(fixed[2:4], uint16(y))
	order.PutUint16(fixed[4:6], w)
	order.PutUint16(fixed[6:8], h)
	order.PutUint16(fixed[8:10], xhot)
	order.PutUint16(fixed[10:12], yhot)
	order.PutUint32(fixed[12:16], serial)
	pix := make([]byte, int(w)*int(h)*4)
	for i := range pix {
		pix[i] = byte(i + 1)
	}
	return reply(order, 0, fixed, pix)
}

// xfixesServer scripts an XFIXES-capable server.
func xfixesServer(t *testing.T, order ByteOrder, cursor []byte) func(op, data byte, body []byte) []byte {
	t.Helper()
	return func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			name := string(body[4 : 4+int(order.Uint16(body[0:2]))])
			if name == XfixesName {
				return reply(order, 0, []byte{1, 138, 87, 140}, nil)
			}
			return reply(order, 0, []byte{0, 0, 0, 0}, nil)
		}
		if op == 138 && data == xfReqQueryVersion {
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], 5)
			order.PutUint32(fixed[4:8], 0)
			return reply(order, 0, fixed, nil)
		}
		if op == 138 && data == xfReqGetCursorImage {
			if cursor == nil {
				return errorPacket(order, ErrCodeCursor, 0, op, 0)
			}
			return cursor
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	}
}

func TestQueryXfixesAndGetCursorImage(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, xfixesServer(t, order, cursorReply(order, 100, 200, 4, 3, 1, 2, 77)))
	xf, err := c.QueryXfixes()
	if err != nil || xf == nil {
		t.Fatalf("QueryXfixes = %+v, %v", xf, err)
	}
	if xf.VerMajor != 5 || xf.VerMinor != 0 {
		t.Errorf("Xfixes = %+v", xf)
	}
	ci, err := xf.GetCursorImage()
	if err != nil {
		t.Fatal(err)
	}
	if ci.X != 100 || ci.Y != 200 || ci.Width != 4 || ci.Height != 3 ||
		ci.XHot != 1 || ci.YHot != 2 || ci.Serial != 77 {
		t.Errorf("CursorImage = %+v", ci)
	}
	if len(ci.Pix) != 4*3*4 {
		t.Fatalf("cursor pixels = %d bytes, want %d", len(ci.Pix), 4*3*4)
	}
	// The bytes come back exactly as the extension laid them out for this
	// connection's byte order.
	for i := range ci.Pix {
		if ci.Pix[i] != byte(i+1) {
			t.Fatalf("pixel byte %d = %d, want %d", i, ci.Pix[i], i+1)
		}
	}
	if ox, oy := ci.Origin(); ox != 99 || oy != 198 {
		t.Errorf("Origin() = %d, %d; want 99, 198", ox, oy)
	}
}

func TestGetCursorImageBigEndian(t *testing.T) {
	order := binary.BigEndian
	c, _ := dialFakeOrder(t, order, xfixesServer(t, order, cursorReply(order, 0, 0, 2, 2, 0, 0, 1)))
	xf, err := c.QueryXfixes()
	if err != nil {
		t.Fatal(err)
	}
	ci, err := xf.GetCursorImage()
	if err != nil {
		t.Fatal(err)
	}
	// The ARGB CARD32s are re-written in the SAME order they were read, so a
	// big-endian connection round-trips them byte for byte.
	for i := range ci.Pix {
		if ci.Pix[i] != byte(i+1) {
			t.Fatalf("pixel byte %d = %d, want %d", i, ci.Pix[i], i+1)
		}
	}
}

func TestQueryXfixesAbsentAndFailing(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{0, 0, 0, 0}, nil)
	})
	if xf, err := c.QueryXfixes(); err != nil || xf != nil {
		t.Fatalf("QueryXfixes = %+v, %v", xf, err)
	}
	c2, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c2.QueryXfixes(); err == nil {
		t.Error("QueryXfixes accepted a failed QueryExtension")
	}
	c3, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 138, 0, 0}, nil)
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c3.QueryXfixes(); err == nil {
		t.Error("QueryXfixes accepted a failed XFixesQueryVersion")
	}
}

func TestGetCursorImageError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, xfixesServer(t, order, nil))
	xf, err := c.QueryXfixes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := xf.GetCursorImage(); err == nil {
		t.Fatal("GetCursorImage accepted an error reply")
	}
}

func TestDecodeCursorImageTruncated(t *testing.T) {
	order := binary.LittleEndian
	hdr := make([]byte, 32)
	order.PutUint16(hdr[12:14], 8) // width
	order.PutUint16(hdr[14:16], 8) // height
	if _, err := decodeCursorImage(order, hdr, make([]byte, 16)); err == nil ||
		!strings.Contains(err.Error(), "reply carried") {
		t.Fatalf("decodeCursorImage reported %v, want a short-pixel error", err)
	}
	if _, err := decodeCursorImage(order, hdr[:10], nil); err == nil ||
		!strings.Contains(err.Error(), "truncated reply header") {
		t.Fatalf("decodeCursorImage reported %v, want a short-header error", err)
	}
}

func TestDecodeCursorImageEmpty(t *testing.T) {
	order := binary.LittleEndian
	hdr := make([]byte, 32)
	ci, err := decodeCursorImage(order, hdr, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(ci.Pix) != 0 || ci.Width != 0 {
		t.Errorf("empty cursor = %+v", ci)
	}
}
