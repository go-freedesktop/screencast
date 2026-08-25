// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"testing"
	"time"
)

func TestSetWindowBackground(t *testing.T) {
	order := binary.LittleEndian
	c, f := dialFake(t, nil)
	if err := c.SetWindowBackground(0x100, 0x3366cc); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return len(f.requests()) > 1 })
	r := f.lastRequest()
	if r.Op != opChangeWindowAttributes || len(r.Body) != 12 {
		t.Fatalf("request = %+v", r)
	}
	if order.Uint32(r.Body[0:4]) != 0x100 || order.Uint32(r.Body[4:8]) != cwBackPixel ||
		order.Uint32(r.Body[8:12]) != 0x3366cc {
		t.Errorf("body = % x", r.Body)
	}
}

func TestClearArea(t *testing.T) {
	order := binary.LittleEndian
	c, f := dialFake(t, nil)
	if err := c.ClearArea(0x100, -2, 3, 640, 480); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return len(f.requests()) > 1 })
	r := f.lastRequest()
	if r.Op != opClearArea || len(r.Body) != 12 {
		t.Fatalf("request = %+v", r)
	}
	if order.Uint32(r.Body[0:4]) != 0x100 ||
		int16(order.Uint16(r.Body[4:6])) != -2 || int16(order.Uint16(r.Body[6:8])) != 3 ||
		order.Uint16(r.Body[8:10]) != 640 || order.Uint16(r.Body[10:12]) != 480 {
		t.Errorf("body = % x", r.Body)
	}
}

func TestSync(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, make([]byte, 4), nil)
	})
	if err := c.Sync(); err != nil {
		t.Fatal(err)
	}
}

func TestPaintOverAClosedConnection(t *testing.T) {
	c, _ := dialFake(t, nil)
	_ = c.Close()
	if err := c.SetWindowBackground(1, 0); err == nil {
		t.Error("SetWindowBackground over a closed connection succeeded")
	}
	if err := c.ClearArea(1, 0, 0, 0, 0); err == nil {
		t.Error("ClearArea over a closed connection succeeded")
	}
	if err := c.Sync(); err == nil {
		t.Error("Sync over a closed connection succeeded")
	}
}

func TestCreateSolidWindow(t *testing.T) {
	order := binary.LittleEndian
	// Only InternAtom has a reply. Answering a request that has none would
	// deadlock the pipe: the server would block writing while the client
	// blocks writing the next request.
	c, f := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opInternAtom {
			return reply(order, 0, make([]byte, 4), nil)
		}
		return nil
	})
	wid, err := c.CreateSolidWindow(0x100, 10, 20, 640, 480, 0x3366cc)
	if err != nil {
		t.Fatal(err)
	}
	if wid == 0 {
		t.Fatal("CreateSolidWindow returned resource id 0")
	}
	reqs := f.requests()
	var create, mapped bool
	for _, r := range reqs {
		switch r.Op {
		case opCreateWindow:
			create = true
			if len(r.Body) != 36 {
				t.Fatalf("CreateWindow body is %d bytes, want 36", len(r.Body))
			}
			if order.Uint32(r.Body[0:4]) != wid || order.Uint32(r.Body[4:8]) != 0x100 {
				t.Errorf("wid/parent = %#x/%#x", order.Uint32(r.Body[0:4]), order.Uint32(r.Body[4:8]))
			}
			if int16(order.Uint16(r.Body[8:10])) != 10 || int16(order.Uint16(r.Body[10:12])) != 20 {
				t.Errorf("x, y = %d, %d", int16(order.Uint16(r.Body[8:10])), int16(order.Uint16(r.Body[10:12])))
			}
			if order.Uint16(r.Body[12:14]) != 640 || order.Uint16(r.Body[14:16]) != 480 {
				t.Errorf("w, h = %d, %d", order.Uint16(r.Body[12:14]), order.Uint16(r.Body[14:16]))
			}
			if order.Uint16(r.Body[18:20]) != classInputOutput {
				t.Errorf("class = %d", order.Uint16(r.Body[18:20]))
			}
			if order.Uint32(r.Body[24:28]) != cwBackPixel|cwOverrideRedirect {
				t.Errorf("value mask = %#x", order.Uint32(r.Body[24:28]))
			}
			// The value list is in ascending bit order: back-pixel, then
			// override-redirect. Getting that order wrong is a BadValue.
			if order.Uint32(r.Body[28:32]) != 0x3366cc {
				t.Errorf("background pixel = %#x", order.Uint32(r.Body[28:32]))
			}
			if order.Uint32(r.Body[32:36]) != 1 {
				t.Errorf("override-redirect = %d; the value list is out of bit order",
					order.Uint32(r.Body[32:36]))
			}
		case opMapWindow:
			mapped = true
			if order.Uint32(r.Body[0:4]) != wid {
				t.Errorf("MapWindow named %#x, not the new window", order.Uint32(r.Body[0:4]))
			}
		}
	}
	if !create || !mapped {
		t.Errorf("CreateSolidWindow sent create=%v map=%v", create, mapped)
	}
}

func TestCreateSolidWindowOverAClosedConnection(t *testing.T) {
	c, _ := dialFake(t, nil)
	_ = c.Close()
	if _, err := c.CreateSolidWindow(1, 0, 0, 2, 2, 0); err == nil {
		t.Error("CreateSolidWindow over a closed connection succeeded")
	}
	if err := c.MapWindow(1); err == nil {
		t.Error("MapWindow over a closed connection succeeded")
	}
	if err := c.DestroyWindow(1); err == nil {
		t.Error("DestroyWindow over a closed connection succeeded")
	}
}

func TestCreateSolidWindowSurfacesAServerRefusal(t *testing.T) {
	// CreateWindow and MapWindow have no replies, so a bad resource id or a
	// BadMatch would go unnoticed — which is exactly why CreateSolidWindow
	// ends with a Sync. The refusal must come back from there rather than
	// leaving the caller with a window id nothing will accept.
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opInternAtom {
			return errorPacket(order, ErrCodeIDChoice, 0, opCreateWindow, 0)
		}
		return nil
	})
	if _, err := c.CreateSolidWindow(0x100, 0, 0, 8, 8, 0); err == nil {
		t.Error("CreateSolidWindow succeeded although the server refused")
	}
}

func TestDestroyWindow(t *testing.T) {
	order := binary.LittleEndian
	c, f := dialFake(t, nil)
	if err := c.DestroyWindow(0x1234); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool {
		rs := f.requests()
		return len(rs) > 1 && rs[len(rs)-1].Op == opDestroyWindow
	})
	if order.Uint32(f.lastRequest().Body[0:4]) != 0x1234 {
		t.Errorf("DestroyWindow named %#x", order.Uint32(f.lastRequest().Body[0:4]))
	}
}

func TestCreateSolidWindowSurfacesAMapFailure(t *testing.T) {
	// The CreateWindow write lands and the MapWindow write does not. The
	// caller must hear about it rather than getting a window id back.
	c := connOverFailingWriter(t, 1)
	if _, err := c.CreateSolidWindow(0x100, 0, 0, 8, 8, 0); err == nil {
		t.Error("CreateSolidWindow succeeded although MapWindow could not be sent")
	}
}
