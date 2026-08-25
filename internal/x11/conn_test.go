// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strings"
	"testing"
)

func TestHandshakeSuccess(t *testing.T) {
	for _, order := range []ByteOrder{binary.LittleEndian, binary.BigEndian} {
		c, f := dialFakeOrder(t, order, nil)
		if c.Order() != order {
			t.Errorf("Order() = %v, want %v", c.Order(), order)
		}
		s := c.Setup()
		if s.ProtoMajor != 11 || s.ProtoMinor != 0 {
			t.Errorf("protocol = %d.%d", s.ProtoMajor, s.ProtoMinor)
		}
		if len(s.Screens) != 1 || s.Screens[0].Root != 0x100 {
			t.Errorf("screens = %+v", s.Screens)
		}
		// The setup request the server saw must carry the auth blob.
		setup := f.requests()[0]
		if !strings.Contains(string(setup.Body), AuthMITCookie) {
			t.Errorf("setup request did not carry the auth name: % x", setup.Body)
		}
	}
}

func TestHandshakeRefused(t *testing.T) {
	f, cli := newFakeX(t, binary.LittleEndian)
	f.setupStatus = 0
	f.setupReason = "No protocol specified"
	f.serve()
	_, err := Handshake(cli, binary.LittleEndian, "", nil)
	var se *SetupError
	if !errors.As(err, &se) {
		t.Fatalf("Handshake reported %v (%T), want *SetupError", err, err)
	}
	if se.Reason != "No protocol specified" || se.Authenticate {
		t.Errorf("SetupError = %+v", se)
	}
	if !strings.Contains(se.Error(), "refused") {
		t.Errorf("SetupError.Error() = %q", se.Error())
	}
}

func TestHandshakeAuthenticate(t *testing.T) {
	f, cli := newFakeX(t, binary.LittleEndian)
	f.setupStatus = 2
	f.setupReason = "Authorization required"
	f.serve()
	_, err := Handshake(cli, binary.LittleEndian, "", nil)
	var se *SetupError
	if !errors.As(err, &se) || !se.Authenticate {
		t.Fatalf("Handshake reported %v, want an Authenticate SetupError", err)
	}
	if !strings.Contains(se.Error(), "further authentication") {
		t.Errorf("SetupError.Error() = %q", se.Error())
	}
}

func TestHandshakeUnknownStatus(t *testing.T) {
	f, cli := newFakeX(t, binary.LittleEndian)
	f.setupStatus = 9
	f.serve()
	if _, err := Handshake(cli, binary.LittleEndian, "", nil); err == nil ||
		!strings.Contains(err.Error(), "unknown setup status") {
		t.Fatalf("Handshake reported %v, want an unknown-status error", err)
	}
}

func TestHandshakeTransportFailures(t *testing.T) {
	// A transport that refuses the write.
	if _, err := Handshake(deadPipe(t, 0), binary.LittleEndian, "", nil); err == nil {
		t.Error("Handshake succeeded over a closed transport")
	}
	// A transport that accepts the write and then hangs up before the header.
	if _, err := Handshake(shortPipe(t, nil), binary.LittleEndian, "", nil); err == nil {
		t.Error("Handshake succeeded with no reply at all")
	}
	// A header announcing more body than arrives.
	hdr := []byte{1, 0, 11, 0, 0, 0, 10, 0}
	if _, err := Handshake(shortPipe(t, hdr), binary.LittleEndian, "", nil); err == nil {
		t.Error("Handshake succeeded with a truncated body")
	}
	// A well-formed header and body that is not a parseable setup reply.
	body := append(hdr, make([]byte, 40)...)
	if _, err := Handshake(shortPipe(t, body), binary.LittleEndian, "", nil); err == nil {
		t.Error("Handshake accepted an unparseable setup reply")
	}
}

func TestHandshakeRefusedTruncatedReason(t *testing.T) {
	// status 0 with a reason length longer than the body: the reason is
	// simply empty rather than a panic.
	pkt := []byte{0, 200, 11, 0, 0, 0, 1, 0}
	pkt = append(pkt, make([]byte, 4)...)
	_, err := Handshake(shortPipe(t, pkt), binary.LittleEndian, "", nil)
	var se *SetupError
	if !errors.As(err, &se) || se.Reason != "" {
		t.Fatalf("Handshake reported %v, want a SetupError with an empty reason", err)
	}
}

// deadPipe returns a transport that is already closed, so the first write
// fails.
func deadPipe(t *testing.T, _ int) io.ReadWriteCloser {
	t.Helper()
	cli, srv := net.Pipe()
	_ = srv.Close()
	_ = cli.Close()
	return cli
}

// shortPipe returns a transport that swallows the setup request and then
// replies with exactly the given bytes before hanging up.
func shortPipe(t *testing.T, reply []byte) io.ReadWriteCloser {
	t.Helper()
	cli, srv := net.Pipe()
	go func() {
		defer func() { _ = srv.Close() }()
		buf := make([]byte, 4096)
		if _, err := srv.Read(buf); err != nil {
			return
		}
		if len(reply) > 0 {
			_, _ = srv.Write(reply)
		}
	}()
	t.Cleanup(func() { _ = cli.Close() })
	return cli
}

func TestTrimNul(t *testing.T) {
	if got := trimNul([]byte("abc\x00def")); got != "abc" {
		t.Errorf("trimNul = %q", got)
	}
	if got := trimNul([]byte("abc")); got != "abc" {
		t.Errorf("trimNul with no NUL = %q", got)
	}
	if got := trimNul(nil); got != "" {
		t.Errorf("trimNul(nil) = %q", got)
	}
}

func TestNewIDAndSeq(t *testing.T) {
	f, cli := newFakeX(t, binary.LittleEndian)
	f.setupBody = buildSetupBody(binary.LittleEndian, setupSpec{
		Vendor: "x", ResourceIDBase: 0x200000, ResourceIDMask: 0x1fffff,
		Formats: []Format{{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}},
		Screens: []screenSpec{{Root: 1, Width: 8, Height: 8, RootVisual: 2, RootDepth: 24}},
	})
	f.setHandler(func(op, data byte, body []byte) []byte {
		return reply(binary.LittleEndian, 0, nil, nil)
	})
	f.serve()
	c, err := Handshake(cli, binary.LittleEndian, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.NewID(); got != 0x200000 {
		t.Errorf("first NewID = %#x, want 0x200000", got)
	}
	if got := c.NewID(); got != 0x200001 {
		t.Errorf("second NewID = %#x", got)
	}
	if c.Seq() != 0 {
		t.Errorf("Seq before any request = %d", c.Seq())
	}
	if _, err := c.InternAtom("A", false); err != nil {
		t.Fatal(err)
	}
	if c.Seq() != 1 {
		t.Errorf("Seq after one request = %d", c.Seq())
	}
}

func TestQueryExtension(t *testing.T) {
	order := binary.LittleEndian
	c, f := dialFake(t, func(op, data byte, body []byte) []byte {
		if op != opQueryExtension {
			t.Errorf("op = %d, want %d", op, opQueryExtension)
		}
		if n := int(order.Uint16(body[0:2])); n != len("MIT-SHM") {
			t.Errorf("name length = %d", n)
		}
		if got := string(body[4 : 4+len("MIT-SHM")]); got != "MIT-SHM" {
			t.Errorf("name = %q", got)
		}
		return reply(order, 0, []byte{1, 130, 65, 128}, nil)
	})
	present, major, firstEvent, firstError, err := c.QueryExtension("MIT-SHM")
	if err != nil || !present || major != 130 || firstEvent != 65 || firstError != 128 {
		t.Fatalf("QueryExtension = %v, %d, %d, %d, %v", present, major, firstEvent, firstError, err)
	}
	// The request body must be padded to a 4-byte boundary: 2+2 header plus
	// "MIT-SHM" (7) padded to 8.
	if got := len(f.lastRequest().Body); got != 12 {
		t.Errorf("request body is %d bytes, want 12", got)
	}
}

func TestQueryExtensionAbsent(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{0, 0, 0, 0}, nil)
	})
	present, _, _, _, err := c.QueryExtension("NOPE")
	if err != nil || present {
		t.Fatalf("QueryExtension = %v, %v", present, err)
	}
}

func TestXErrorDecoding(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeDrawable, 0xdead, 73, 0)
	})
	_, err := c.GetGeometry(0xdead)
	var xe *XError
	if !errors.As(err, &xe) {
		t.Fatalf("GetGeometry reported %v (%T), want *XError", err, err)
	}
	if xe.Code != ErrCodeDrawable || xe.BadValue != 0xdead || xe.Major != 73 {
		t.Errorf("XError = %+v", xe)
	}
	if xe.Name != "BadDrawable" || !strings.Contains(xe.Error(), "BadDrawable") {
		t.Errorf("XError.Error() = %q", xe.Error())
	}
	if xe.Op != "GetGeometry" || !strings.Contains(xe.Error(), "GetGeometry") {
		t.Errorf("XError does not name the operation: %q", xe.Error())
	}
}

func TestXErrorUnknownCodeAndOp(t *testing.T) {
	e := &XError{Code: 200}
	if !strings.Contains(e.Error(), "error 200") || !strings.Contains(e.Error(), "request") {
		t.Errorf("XError.Error() = %q", e.Error())
	}
	if ErrorName(ErrCodeAlloc) != "BadAlloc" || ErrorName(200) != "" {
		t.Errorf("ErrorName is wrong for %d/%d", ErrCodeAlloc, 200)
	}
}

func TestEventsAreDrainedWhileWaitingForAReply(t *testing.T) {
	order := binary.LittleEndian
	sent := 0
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		sent++
		// Two ordinary events and one GenericEvent with a payload arrive
		// before the reply. All three must be swallowed whole, or the reply
		// parse would read event bytes as geometry.
		var out []byte
		out = append(out, eventPacket(12)...)
		out = append(out, genericEventPacket(order, 3)...)
		out = append(out, eventPacket(22)...)
		fixed := make([]byte, 24)
		order.PutUint32(fixed[0:4], 0x100)
		order.PutUint16(fixed[4:6], 0xfffb) // x = -5
		order.PutUint16(fixed[6:8], 0)
		order.PutUint16(fixed[8:10], 640)
		order.PutUint16(fixed[10:12], 480)
		order.PutUint16(fixed[12:14], 2)
		out = append(out, reply(order, 24, fixed, nil)...)
		return out
	})
	g, err := c.GetGeometry(0x100)
	if err != nil {
		t.Fatalf("GetGeometry: %v", err)
	}
	if g.Depth != 24 || g.Root != 0x100 || g.X != -5 || g.Width != 640 || g.Height != 480 || g.BorderWidth != 2 {
		t.Errorf("Geometry = %+v", g)
	}
	if sent != 1 {
		t.Errorf("handler ran %d times", sent)
	}
}

func TestGetWindowAttributes(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 36)
		order.PutUint32(fixed[0:4], 0x21) // visual
		order.PutUint16(fixed[4:6], WindowClassInputOutput)
		fixed[18] = MapStateViewable // 8 + 18 = 26
		fixed[19] = 1                // override-redirect
		return reply(order, 0, fixed, nil)
	})
	a, err := c.GetWindowAttributes(0x400)
	if err != nil {
		t.Fatal(err)
	}
	if a.Visual != 0x21 || a.Class != WindowClassInputOutput || !a.Viewable() || !a.OverrideRedirect {
		t.Errorf("WindowAttributes = %+v", a)
	}
	if (WindowAttributes{MapState: MapStateUnmapped}).Viewable() {
		t.Error("an unmapped window reported itself viewable")
	}
}

func TestGetWindowAttributesError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeWindow, 1, opGetWindowAttributes, 0)
	})
	if _, err := c.GetWindowAttributes(1); err == nil {
		t.Fatal("GetWindowAttributes accepted an error reply")
	}
}

func TestQueryTree(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint32(fixed[0:4], 0x100)
		order.PutUint32(fixed[4:8], 0x200)
		order.PutUint16(fixed[8:10], 2)
		extra := make([]byte, 8)
		order.PutUint32(extra[0:4], 0x300)
		order.PutUint32(extra[4:8], 0x400)
		return reply(order, 0, fixed, extra)
	})
	root, parent, children, err := c.QueryTree(0x200)
	if err != nil {
		t.Fatal(err)
	}
	if root != 0x100 || parent != 0x200 || len(children) != 2 ||
		children[0] != 0x300 || children[1] != 0x400 {
		t.Errorf("QueryTree = %#x, %#x, %#x", root, parent, children)
	}
}

func TestQueryTreeMismatchedCount(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint16(fixed[8:10], 9) // claims nine children
		return reply(order, 0, fixed, make([]byte, 8))
	})
	if _, _, _, err := c.QueryTree(1); err == nil ||
		!strings.Contains(err.Error(), "children announced") {
		t.Fatalf("QueryTree reported %v, want a count mismatch", err)
	}
}

func TestQueryTreeError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeWindow, 1, opQueryTree, 0)
	})
	if _, _, _, err := c.QueryTree(1); err == nil {
		t.Fatal("QueryTree accepted an error reply")
	}
}

func TestInternAtomAndGetAtomName(t *testing.T) {
	order := binary.LittleEndian
	c, f := dialFake(t, func(op, data byte, body []byte) []byte {
		switch op {
		case opInternAtom:
			fixed := make([]byte, 4)
			order.PutUint32(fixed, 0x1234)
			return reply(order, 0, fixed, nil)
		case opGetAtomName:
			fixed := make([]byte, 24)
			order.PutUint16(fixed[0:2], 6)
			return reply(order, 0, fixed, []byte("HDMI-1\x00\x00"))
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	a, err := c.InternAtom("_NET_WM_NAME", true)
	if err != nil || a != 0x1234 {
		t.Fatalf("InternAtom = %#x, %v", a, err)
	}
	if got := f.lastRequest().Data; got != 1 {
		t.Errorf("onlyIfExists byte = %d, want 1", got)
	}
	if _, err := c.InternAtom("X", false); err != nil {
		t.Fatal(err)
	}
	if got := f.lastRequest().Data; got != 0 {
		t.Errorf("onlyIfExists byte = %d, want 0", got)
	}
	name, err := c.GetAtomName(0x1234)
	if err != nil || name != "HDMI-1" {
		t.Fatalf("GetAtomName = %q, %v", name, err)
	}
}

func TestInternAtomError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeAtom, 0, op, 0)
	})
	if _, err := c.InternAtom("X", true); err == nil {
		t.Fatal("InternAtom accepted an error reply")
	}
	if _, err := c.GetAtomName(1); err == nil {
		t.Fatal("GetAtomName accepted an error reply")
	}
}

func TestGetAtomNameTruncated(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint16(fixed[0:2], 40) // claims a 40-byte name
		return reply(order, 0, fixed, []byte("short   "))
	})
	if _, err := c.GetAtomName(1); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("GetAtomName reported %v, want a truncation error", err)
	}
}

func TestGetProperty(t *testing.T) {
	order := binary.LittleEndian
	for _, tc := range []struct {
		name    string
		format  byte
		count   uint32
		extra   []byte
		wantLen int
	}{
		{"format 8", 8, 5, []byte("hello\x00\x00\x00"), 5},
		{"format 16", 16, 2, []byte{1, 0, 2, 0}, 4},
		{"format 32", 32, 2, []byte{1, 0, 0, 0, 2, 0, 0, 0}, 8},
		{"absent", 0, 0, nil, 0},
	} {
		c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], AtomString)
			order.PutUint32(fixed[4:8], 0)
			order.PutUint32(fixed[8:12], tc.count)
			return reply(order, tc.format, fixed, tc.extra)
		})
		p, err := c.GetProperty(1, 2, 3, 100)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if p.Format != tc.format || len(p.Value) != tc.wantLen {
			t.Errorf("%s: Property = %+v", tc.name, p)
		}
	}
}

func TestPropertyDecoders(t *testing.T) {
	order := binary.LittleEndian
	p := Property{Format: 32, Value: []byte{1, 0, 0, 0, 2, 0, 0, 0}}
	if v := p.Uint32s(order); len(v) != 2 || v[0] != 1 || v[1] != 2 {
		t.Errorf("Uint32s = %v", v)
	}
	if (Property{Format: 8}).Uint32s(order) != nil {
		t.Error("Uint32s decoded a format-8 property")
	}
	if got := (Property{Format: 8, Value: []byte("hi\x00")}).Text(); got != "hi" {
		t.Errorf("Text = %q", got)
	}
	if got := (Property{Format: 32}).Text(); got != "" {
		t.Errorf("Text on a format-32 property = %q", got)
	}
}

func TestGetPropertyTruncated(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint32(fixed[8:12], 99) // claims 99 bytes
		return reply(order, 8, fixed, []byte("short   "))
	})
	if _, err := c.GetProperty(1, 2, 3, 100); err == nil ||
		!strings.Contains(err.Error(), "announced") {
		t.Fatalf("GetProperty reported %v, want a truncation error", err)
	}
}

func TestGetPropertyError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeAtom, 0, op, 0)
	})
	if _, err := c.GetProperty(1, 2, 3, 4); err == nil {
		t.Fatal("GetProperty accepted an error reply")
	}
}

func TestTranslateCoordinates(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint32(fixed[0:4], 0x500)
		order.PutUint16(fixed[4:6], 0xfffd /* -3 */)
		order.PutUint16(fixed[6:8], 42)
		return reply(order, 1, fixed, nil)
	})
	dx, dy, child, err := c.TranslateCoordinates(1, 2, 10, 20)
	if err != nil || dx != -3 || dy != 42 || child != 0x500 {
		t.Fatalf("TranslateCoordinates = %d, %d, %#x, %v", dx, dy, child, err)
	}
}

func TestTranslateCoordinatesError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeWindow, 0, op, 0)
	})
	if _, _, _, err := c.TranslateCoordinates(1, 2, 0, 0); err == nil {
		t.Fatal("TranslateCoordinates accepted an error reply")
	}
}

func TestQueryPointer(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		fixed := make([]byte, 24)
		order.PutUint32(fixed[0:4], 0x100)
		order.PutUint32(fixed[4:8], 0x200)
		order.PutUint16(fixed[8:10], 300)
		order.PutUint16(fixed[10:12], 400)
		order.PutUint16(fixed[12:14], 0xfff9 /* -7 */)
		order.PutUint16(fixed[14:16], 8)
		order.PutUint16(fixed[16:18], 0x100)
		return reply(order, 1, fixed, nil)
	})
	p, err := c.QueryPointer(1)
	if err != nil {
		t.Fatal(err)
	}
	if !p.SameScreen || p.Root != 0x100 || p.Child != 0x200 || p.RootX != 300 ||
		p.RootY != 400 || p.WinX != -7 || p.WinY != 8 || p.Mask != 0x100 {
		t.Errorf("Pointer = %+v", p)
	}
}

func TestQueryPointerError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeWindow, 0, op, 0)
	})
	if _, err := c.QueryPointer(1); err == nil {
		t.Fatal("QueryPointer accepted an error reply")
	}
}

func TestGetImageReadsStraightIntoTheDestination(t *testing.T) {
	order := binary.LittleEndian
	const w, h = 4, 2
	pixels := make([]byte, w*h*4)
	for i := range pixels {
		pixels[i] = byte(i + 1)
	}
	c, f := dialFake(t, func(op, data byte, body []byte) []byte {
		if op != opGetImage || data != imageFormatZPixmap {
			t.Errorf("op = %d, format = %d", op, data)
		}
		if order.Uint32(body[0:4]) != 0x100 {
			t.Errorf("drawable = %#x", order.Uint32(body[0:4]))
		}
		if int16(order.Uint16(body[4:6])) != -2 || int16(order.Uint16(body[6:8])) != 3 {
			t.Errorf("x, y = %d, %d", int16(order.Uint16(body[4:6])), int16(order.Uint16(body[6:8])))
		}
		if order.Uint16(body[8:10]) != w || order.Uint16(body[10:12]) != h {
			t.Errorf("w, h = %d, %d", order.Uint16(body[8:10]), order.Uint16(body[10:12]))
		}
		if order.Uint32(body[12:16]) != AllPlanes {
			t.Errorf("plane mask = %#x", order.Uint32(body[12:16]))
		}
		fixed := make([]byte, 24)
		order.PutUint32(fixed[0:4], 0x21)
		return reply(order, 24, fixed, pixels)
	})
	dst := make([]byte, len(pixels))
	r, err := c.GetImage(0x100, -2, 3, w, h, dst)
	if err != nil {
		t.Fatal(err)
	}
	if r.Depth != 24 || r.Visual != 0x21 || r.Bytes != len(pixels) {
		t.Errorf("ImageReply = %+v", r)
	}
	for i := range pixels {
		if dst[i] != pixels[i] {
			t.Fatalf("byte %d = %d, want %d", i, dst[i], pixels[i])
		}
	}
	// The request is a fixed 20 bytes: 4 of header plus 16 of body.
	if got := len(f.lastRequest().Body); got != 16 {
		t.Errorf("GetImage body is %d bytes, want 16", got)
	}
}

func TestGetImageDestinationTooSmall(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 24, make([]byte, 24), make([]byte, 64))
	})
	if _, err := c.GetImage(1, 0, 0, 4, 4, make([]byte, 8)); err == nil ||
		!strings.Contains(err.Error(), "destination buffer") {
		t.Fatalf("GetImage reported %v, want a short-destination error", err)
	}
}

func TestGetImageEmptyReply(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 24, make([]byte, 24), nil)
	})
	r, err := c.GetImage(1, 0, 0, 0, 0, make([]byte, 4))
	if err != nil || r.Bytes != 0 {
		t.Fatalf("GetImage on an empty rectangle = %+v, %v", r, err)
	}
}

func TestGetImageError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeDrawable, 1, opGetImage, 0)
	})
	if _, err := c.GetImage(1, 0, 0, 2, 2, make([]byte, 64)); err == nil {
		t.Fatal("GetImage accepted an error reply")
	}
}

func TestRequestOverAClosedConnection(t *testing.T) {
	c, _ := dialFake(t, nil)
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := c.InternAtom("X", false); err == nil {
		t.Error("a request over a closed connection succeeded")
	}
	if _, err := c.GetImage(1, 0, 0, 1, 1, make([]byte, 4)); err == nil {
		t.Error("GetImage over a closed connection succeeded")
	}
}

func TestReadReplyTransportFailure(t *testing.T) {
	// A server that takes the request and hangs up without replying.
	cli, srv := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		_, _ = srv.Read(buf) // setup
		_, _ = srv.Write(setupReplyBytes(binary.LittleEndian))
		_, _ = srv.Read(buf) // the request
		_ = srv.Close()
	}()
	c, err := Handshake(cli, binary.LittleEndian, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.InternAtom("X", false); err == nil {
		t.Fatal("a request answered by a hangup succeeded")
	}
}

// setupReplyBytes builds a complete Success setup reply packet.
func setupReplyBytes(order ByteOrder) []byte {
	body := defaultSetupBody(order)
	out := []byte{1, 0}
	var b [2]byte
	order.PutUint16(b[:], 11)
	out = append(out, b[0], b[1])
	order.PutUint16(b[:], 0)
	out = append(out, b[0], b[1])
	order.PutUint16(b[:], uint16(len(body)/4))
	out = append(out, b[0], b[1])
	return append(out, body...)
}

func TestReadReplyTruncatedExtra(t *testing.T) {
	// A reply header announcing more additional data than the server sends.
	cli, srv := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		_, _ = srv.Read(buf)
		_, _ = srv.Write(setupReplyBytes(binary.LittleEndian))
		_, _ = srv.Read(buf)
		pkt := make([]byte, 32)
		pkt[0] = pktReply
		binary.LittleEndian.PutUint32(pkt[4:8], 4) // 16 bytes of extra
		_, _ = srv.Write(pkt)
		_, _ = srv.Write(make([]byte, 4)) // only four arrive
		_ = srv.Close()
	}()
	c, err := Handshake(cli, binary.LittleEndian, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.GetAtomName(1); err == nil {
		t.Fatal("a truncated reply body was accepted")
	}
}

func TestGenericEventTruncated(t *testing.T) {
	cli, srv := net.Pipe()
	go func() {
		buf := make([]byte, 4096)
		_, _ = srv.Read(buf)
		_, _ = srv.Write(setupReplyBytes(binary.LittleEndian))
		_, _ = srv.Read(buf)
		pkt := make([]byte, 32)
		pkt[0] = 35
		binary.LittleEndian.PutUint32(pkt[4:8], 4)
		_, _ = srv.Write(pkt)
		_ = srv.Close()
	}()
	c, err := Handshake(cli, binary.LittleEndian, "", nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	if _, err := c.GetAtomName(1); err == nil {
		t.Fatal("a truncated GenericEvent payload was accepted")
	}
}

func TestSupportsFDPassing(t *testing.T) {
	c, _ := dialFake(t, nil)
	if c.SupportsFDPassing() {
		t.Error("a net.Pipe reported that it can pass descriptors")
	}
	cfd, _, _ := dialFakeFD(t, nil)
	if !cfd.SupportsFDPassing() {
		t.Error("an FDSender transport reported that it cannot pass descriptors")
	}
}

func TestWriteRequestFDWithoutAnFDSender(t *testing.T) {
	c, _ := dialFake(t, nil)
	c.mu.Lock()
	err := c.writeRequestFD(1, 2, nil, 3)
	c.mu.Unlock()
	if err == nil || !strings.Contains(err.Error(), "fd passing") {
		t.Fatalf("writeRequestFD reported %v, want an fd-passing error", err)
	}
}
