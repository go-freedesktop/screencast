// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"
)

// This file builds a SCRIPTED X SERVER in process.
//
// The point is that the wire format is pure logic: a request is a run of bytes
// with a defined layout, and a reply is another. Neither needs an X server to
// be right or wrong. A net.Pipe with a goroutine on the far side that parses
// request headers and answers from a script therefore exercises the ENTIRE
// codec — setup, sequence numbers, error decoding, every request builder,
// every reply parser — on macOS and on Windows as well as on Linux, and lets a
// test assert on the exact bytes that went out.

// fakeX is the scripted server. It reads the setup request, answers it, then
// answers each subsequent request from handle.
type fakeX struct {
	t     *testing.T
	order binary.ByteOrder
	srv   net.Conn

	mu   sync.Mutex
	reqs []request

	// handle answers one request. Returning nil sends nothing, which is what
	// a request with no reply (Detach) does.
	handle func(op, data byte, body []byte) []byte

	// setupStatus and setupBody script the connection setup: status 1 is
	// Success, 0 Failed, 2 Authenticate.
	setupStatus byte
	setupBody   []byte
	setupReason string

	done chan struct{}
}

// request is one request the server saw, split into its header and body.
type request struct {
	Op   byte
	Data byte
	Body []byte
}

// newFakeX starts a scripted server and returns the client half of the pipe.
// The caller runs Handshake over it.
func newFakeX(t *testing.T, order binary.ByteOrder) (*fakeX, net.Conn) {
	t.Helper()
	cli, srv := net.Pipe()
	f := &fakeX{
		t:           t,
		order:       order,
		srv:         srv,
		setupStatus: 1,
		setupBody:   defaultSetupBody(order),
		done:        make(chan struct{}),
	}
	t.Cleanup(func() {
		_ = srv.Close()
		_ = cli.Close()
	})
	return f, cli
}

// serve runs the server loop. It is started after the reply script is in
// place.
func (f *fakeX) serve() {
	go func() {
		defer close(f.done)
		defer func() { _ = f.srv.Close() }()
		if !f.serveSetup() {
			return
		}
		for {
			var hdr [4]byte
			if _, err := io.ReadFull(f.srv, hdr[:]); err != nil {
				return
			}
			n := int(f.order.Uint16(hdr[2:4]))*4 - 4
			body := make([]byte, 0)
			if n > 0 {
				body = make([]byte, n)
				if _, err := io.ReadFull(f.srv, body); err != nil {
					return
				}
			}
			f.mu.Lock()
			f.reqs = append(f.reqs, request{Op: hdr[0], Data: hdr[1], Body: body})
			h := f.handle
			f.mu.Unlock()
			if h == nil {
				continue
			}
			if reply := h(hdr[0], hdr[1], body); reply != nil {
				if _, err := f.srv.Write(reply); err != nil {
					return
				}
			}
		}
	}()
}

// serveSetup reads the client's setup request and answers it. It reports
// whether the session continues.
func (f *fakeX) serveSetup() bool {
	var head [12]byte
	if _, err := io.ReadFull(f.srv, head[:]); err != nil {
		return false
	}
	order, ok := orderFor(head[0])
	if !ok {
		return false
	}
	nameLen := int(order.Uint16(head[6:8]))
	dataLen := int(order.Uint16(head[8:10]))
	rest := make([]byte, pad4(nameLen)+pad4(dataLen))
	if len(rest) > 0 {
		if _, err := io.ReadFull(f.srv, rest); err != nil {
			return false
		}
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, request{Op: 0xff, Body: append(head[:], rest...)})
	f.mu.Unlock()

	body := f.setupBody
	if f.setupStatus != 1 {
		body = append([]byte(f.setupReason), make([]byte, padding(len(f.setupReason)))...)
	}
	var reply []byte
	reply = append(reply, f.setupStatus, byte(len(f.setupReason)))
	var b [2]byte
	order.PutUint16(b[:], 11)
	reply = append(reply, b[0], b[1])
	order.PutUint16(b[:], 0)
	reply = append(reply, b[0], b[1])
	order.PutUint16(b[:], uint16(len(body)/4))
	reply = append(reply, b[0], b[1])
	reply = append(reply, body...)
	if _, err := f.srv.Write(reply); err != nil {
		return false
	}
	return f.setupStatus == 1
}

// requests returns everything the server saw, setup included at index 0.
func (f *fakeX) requests() []request {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]request, len(f.reqs))
	copy(out, f.reqs)
	return out
}

// lastRequest returns the most recent non-setup request.
func (f *fakeX) lastRequest() request {
	f.t.Helper()
	rs := f.requests()
	for i := len(rs) - 1; i >= 0; i-- {
		if rs[i].Op != 0xff {
			return rs[i]
		}
	}
	f.t.Fatal("no request recorded")
	return request{}
}

// setHandler installs the reply script.
func (f *fakeX) setHandler(h func(op, data byte, body []byte) []byte) {
	f.mu.Lock()
	f.handle = h
	f.mu.Unlock()
}

// --- reply builders -------------------------------------------------------

// reply builds a 32-byte reply packet whose fixed part carries the given
// bytes from offset 8 on, plus an optional additional-data block.
func reply(order binary.ByteOrder, detail byte, fixed []byte, extra []byte) []byte {
	pkt := make([]byte, 32)
	pkt[0] = pktReply
	pkt[1] = detail
	order.PutUint16(pkt[2:4], 1) // sequence
	order.PutUint32(pkt[4:8], uint32(len(extra)/4))
	copy(pkt[8:], fixed)
	return append(pkt, extra...)
}

// errorPacket builds a 32-byte error packet.
func errorPacket(order binary.ByteOrder, code byte, bad uint32, major byte, minor uint16) []byte {
	pkt := make([]byte, 32)
	pkt[0] = pktError
	pkt[1] = code
	order.PutUint16(pkt[2:4], 7)
	order.PutUint32(pkt[4:8], bad)
	order.PutUint16(pkt[8:10], minor)
	pkt[10] = major
	return pkt
}

// eventPacket builds a 32-byte event packet with the given code.
func eventPacket(code byte) []byte {
	pkt := make([]byte, 32)
	pkt[0] = code
	return pkt
}

// genericEventPacket builds a GenericEvent (code 35) carrying extra 4-byte
// units past the fixed 32, which the reader has to drain.
func genericEventPacket(order binary.ByteOrder, extraUnits int) []byte {
	pkt := make([]byte, 32+extraUnits*4)
	pkt[0] = 35
	order.PutUint32(pkt[4:8], uint32(extraUnits))
	return pkt
}

// defaultSetupBody builds the additional-data block of a Success setup reply
// describing a plausible modern server: one screen, 1920x1080, depth 24
// TrueColor with 32 bits per pixel and 32-bit scanline padding.
func defaultSetupBody(order binary.ByteOrder) []byte {
	return buildSetupBody(order, setupSpec{
		Vendor:         "fake",
		ResourceIDBase: 0x200000,
		ResourceIDMask: 0x1fffff,
		Formats:        []Format{{Depth: 1, BitsPerPix: 1, ScanlinePad: 32}, {Depth: 24, BitsPerPix: 32, ScanlinePad: 32}},
		Screens: []screenSpec{{
			Root: 0x100, Width: 1920, Height: 1080, WidthMM: 508, HeightMM: 285,
			RootVisual: 0x21, RootDepth: 24,
			Depths: []depthSpec{{Depth: 24, Visuals: []VisualType{{
				ID: 0x21, Class: VisualTrueColor, BitsPerRGB: 8, ColormapEnt: 256,
				RedMask: 0x00ff0000, GreenMask: 0x0000ff00, BlueMask: 0x000000ff,
			}}}},
		}},
	})
}

// setupSpec, screenSpec and depthSpec describe a setup reply to build.
type setupSpec struct {
	Release        uint32
	ResourceIDBase uint32
	ResourceIDMask uint32
	Vendor         string
	MaxRequestLen  uint16
	ImageByteOrder uint8
	Formats        []Format
	Screens        []screenSpec
}

type screenSpec struct {
	Root       uint32
	Colormap   uint32
	White      uint32
	Black      uint32
	Width      uint16
	Height     uint16
	WidthMM    uint16
	HeightMM   uint16
	RootVisual uint32
	RootDepth  uint8
	Depths     []depthSpec
}

type depthSpec struct {
	Depth   uint8
	Visuals []VisualType
}

// buildSetupBody encodes a setup reply body, which is the exact inverse of
// parseSetupReply and lets a test round-trip any server description.
func buildSetupBody(order binary.ByteOrder, s setupSpec) []byte {
	e := newEncoder(order)
	e.put32(s.Release)
	e.put32(s.ResourceIDBase)
	e.put32(s.ResourceIDMask)
	e.put32(256) // motion-buffer-size
	e.put16(uint16(len(s.Vendor)))
	max := s.MaxRequestLen
	if max == 0 {
		max = 65535
	}
	e.put16(max)
	e.put8(uint8(len(s.Screens)))
	e.put8(uint8(len(s.Formats)))
	e.put8(s.ImageByteOrder)
	e.put8(0) // bitmap-bit-order
	e.put8(32)
	e.put8(32)
	e.put8(8)   // min-keycode
	e.put8(255) // max-keycode
	e.skip(4)
	e.putString(s.Vendor)
	for _, f := range s.Formats {
		e.put8(f.Depth)
		e.put8(f.BitsPerPix)
		e.put8(f.ScanlinePad)
		e.skip(5)
	}
	for _, sc := range s.Screens {
		e.put32(sc.Root)
		e.put32(sc.Colormap)
		e.put32(sc.White)
		e.put32(sc.Black)
		e.put32(0) // current-input-masks
		e.put16(sc.Width)
		e.put16(sc.Height)
		e.put16(sc.WidthMM)
		e.put16(sc.HeightMM)
		e.put16(1)
		e.put16(1)
		e.put32(sc.RootVisual)
		e.put8(0)
		e.put8(0)
		e.put8(sc.RootDepth)
		e.put8(uint8(len(sc.Depths)))
		for _, dp := range sc.Depths {
			e.put8(dp.Depth)
			e.skip(1)
			e.put16(uint16(len(dp.Visuals)))
			e.skip(4)
			for _, v := range dp.Visuals {
				e.put32(v.ID)
				e.put8(v.Class)
				e.put8(v.BitsPerRGB)
				e.put16(v.ColormapEnt)
				e.put32(v.RedMask)
				e.put32(v.GreenMask)
				e.put32(v.BlueMask)
				e.skip(4)
			}
		}
	}
	return e.buf
}

// dialFake starts a scripted server with the given handler and returns a
// connected Conn.
func dialFake(t *testing.T, h func(op, data byte, body []byte) []byte) (*Conn, *fakeX) {
	t.Helper()
	return dialFakeOrder(t, binary.LittleEndian, h)
}

// dialFakeOrder is dialFake with an explicit wire byte order.
func dialFakeOrder(t *testing.T, order binary.ByteOrder,
	h func(op, data byte, body []byte) []byte) (*Conn, *fakeX) {
	t.Helper()
	f, cli := newFakeX(t, order)
	f.setHandler(h)
	f.serve()
	c, err := Handshake(cli, order, AuthMITCookie, []byte("0123456789abcdef"))
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, f
}

// fdPipe wraps a net.Pipe end with a no-op SendFD so the MIT-SHM fd path can
// be exercised without a real socket. It records what it was asked to send.
type fdPipe struct {
	net.Conn
	mu   sync.Mutex
	sent []int
	err  error
}

// SendFD implements FDSender.
func (p *fdPipe) SendFD(msg []byte, fd int) error {
	p.mu.Lock()
	p.sent = append(p.sent, fd)
	err := p.err
	p.mu.Unlock()
	if err != nil {
		return err
	}
	_, werr := p.Conn.Write(msg)
	return werr
}

// fds returns the descriptors SendFD was handed.
func (p *fdPipe) fds() []int {
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make([]int, len(p.sent))
	copy(out, p.sent)
	return out
}

// dialFakeFD is dialFake over a transport that supports descriptor passing.
func dialFakeFD(t *testing.T, h func(op, data byte, body []byte) []byte) (*Conn, *fakeX, *fdPipe) {
	t.Helper()
	order := binary.LittleEndian
	f, cli := newFakeX(t, order)
	f.setHandler(h)
	f.serve()
	p := &fdPipe{Conn: cli}
	c, err := Handshake(p, order, "", nil)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c, f, p
}

// eventually retries fn until it reports true or the deadline passes. It keeps
// the pipe-driven tests free of arbitrary sleeps.
func eventually(t *testing.T, d time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition never became true")
}

// failAfterWriter accepts n writes and then refuses every one. It is how the
// tests reach the error paths of a multi-request sequence, where the first
// request lands and a later one does not.
type failAfterWriter struct {
	io.ReadWriteCloser
	mu   sync.Mutex
	left int
}

func (w *failAfterWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	ok := w.left > 0
	if ok {
		w.left--
	}
	w.mu.Unlock()
	if !ok {
		return 0, errors.New("the transport refused the write")
	}
	return w.ReadWriteCloser.Write(b)
}

// connOverFailingWriter returns a handshaken Conn whose transport accepts n
// further writes and then fails.
func connOverFailingWriter(t *testing.T, n int) *Conn {
	t.Helper()
	f, cli := newFakeX(t, binary.LittleEndian)
	f.setHandler(nil)
	f.serve()
	w := &failAfterWriter{ReadWriteCloser: cli, left: 1 + n} // one for the setup
	c, err := Handshake(w, binary.LittleEndian, "", nil)
	if err != nil {
		t.Fatalf("Handshake: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
