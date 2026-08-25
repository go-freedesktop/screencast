// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"fmt"
	"io"
	"sync"
)

// Conn is a connection to an X11 server speaking the core protocol over an
// arbitrary byte stream. It is transport-agnostic: [Handshake] wraps any
// io.ReadWriteCloser — a dialed unix socket in production, one half of a
// net.Pipe in tests — and runs the setup exchange over it.
//
// A Conn is safe for one capture loop at a time: every request/reply pair is
// serialised on a single mutex, so concurrent callers queue rather than
// interleave two half-written requests on the socket.
type Conn struct {
	rw    io.ReadWriteCloser
	order ByteOrder
	setup *Setup

	mu  sync.Mutex // serialises whole request/reply exchanges
	seq uint16     // last sequence number sent (server counts from 1)

	xidBase uint32
	xidMask uint32
	xidNext uint32

	// hdr and req are scratch buffers reused by every exchange so the hot
	// path — one GetImage or ShmGetImage per frame — allocates nothing.
	hdr [32]byte
	req [64]byte
}

// XError is a decoded X11 error reply.
type XError struct {
	Code     byte
	Name     string
	Seq      uint16
	BadValue uint32
	Major    byte
	Minor    uint16
	Op       string
}

// Error renders the protocol name of the code alongside the raw numbers.
func (e *XError) Error() string {
	name := e.Name
	if name == "" {
		name = fmt.Sprintf("error %d", e.Code)
	}
	op := e.Op
	if op == "" {
		op = "request"
	}
	return fmt.Sprintf("x11: %s: %s (major=%d minor=%d bad-value=%#x seq=%d)",
		op, name, e.Major, e.Minor, e.BadValue, e.Seq)
}

// Handshake runs the client connection setup over rw: it sends the byte-order
// sentinel, protocol 11.0 and the authorization name and data, then parses the
// reply. order selects the wire byte order; both are valid and the server
// adopts the client's choice.
func Handshake(rw io.ReadWriteCloser, order ByteOrder, authName string, authData []byte) (*Conn, error) {
	sentinel := byte(orderMSB)
	if order == binary.LittleEndian {
		sentinel = orderLSB
	}
	req := buildSetupRequest(order, sentinel, authName, authData)
	if _, err := rw.Write(req); err != nil {
		return nil, err
	}

	var hdr [8]byte
	if err := readFull(rw, hdr[:]); err != nil {
		return nil, err
	}
	status := hdr[0]
	// The additional-data length, in 4-byte units, sits at bytes 6..7 in the
	// client's chosen order.
	addLen := int(order.Uint16(hdr[6:8])) * 4
	body := make([]byte, addLen)
	if err := readFull(rw, body); err != nil {
		return nil, err
	}

	switch status {
	case 0: // Failed
		reasonLen := int(hdr[1])
		reason := ""
		if reasonLen <= len(body) {
			reason = string(body[:reasonLen])
		}
		return nil, &SetupError{Reason: reason}
	case 2: // Authenticate
		return nil, &SetupError{Reason: trimNul(body), Authenticate: true}
	case 1: // Success
	default:
		return nil, fmt.Errorf("x11: unknown setup status %d", status)
	}

	s, err := parseSetupReply(order, body)
	if err != nil {
		return nil, err
	}
	s.ProtoMajor = order.Uint16(hdr[2:4])
	s.ProtoMinor = order.Uint16(hdr[4:6])
	return &Conn{
		rw:      rw,
		order:   order,
		setup:   s,
		xidBase: s.ResourceIDBase,
		xidMask: s.ResourceIDMask,
	}, nil
}

// SetupError is the connection-setup refusal: the server would not talk to us
// at all. Reason is the server's own wording, which for a missing or wrong
// cookie is "No protocol specified" or "Authorization required".
type SetupError struct {
	Reason       string
	Authenticate bool // the server asked for further authentication
}

// Error renders the refusal.
func (e *SetupError) Error() string {
	if e.Authenticate {
		return fmt.Sprintf("x11: server requires further authentication: %s", e.Reason)
	}
	return fmt.Sprintf("x11: server refused the connection: %s", e.Reason)
}

// trimNul returns b up to its first NUL, as a string.
func trimNul(b []byte) string {
	for i, c := range b {
		if c == 0 {
			return string(b[:i])
		}
	}
	return string(b)
}

// Setup returns the parsed server setup.
func (c *Conn) Setup() *Setup { return c.setup }

// Order returns the negotiated wire byte order.
func (c *Conn) Order() ByteOrder { return c.order }

// Close closes the underlying transport.
func (c *Conn) Close() error { return c.rw.Close() }

// NewID allocates a fresh resource identifier from the server-granted range.
func (c *Conn) NewID() uint32 {
	c.mu.Lock()
	defer c.mu.Unlock()
	id := c.xidBase | (c.xidNext & c.xidMask)
	c.xidNext++
	return id
}

// Seq returns the sequence number of the most recently sent request.
func (c *Conn) Seq() uint16 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

// writeRequest frames body — which must already be 4-byte padded — with the
// four-byte request header (opcode, the per-request data byte, and the total
// length in 4-byte units), writes it, and advances the sequence counter. The
// caller holds c.mu.
func (c *Conn) writeRequest(opcode, data byte, body []byte) error {
	total := 4 + len(body)
	hdr := c.req[:0]
	hdr = append(hdr, opcode, data)
	var l [2]byte
	c.order.PutUint16(l[:], uint16(total/4))
	hdr = append(hdr, l[0], l[1])
	hdr = append(hdr, body...)
	if _, err := c.rw.Write(hdr); err != nil {
		return err
	}
	c.seq++
	return nil
}

// FDSender is implemented by a transport that can pass a file descriptor
// alongside a request over the same socket, via SCM_RIGHTS. The production
// unix transport implements it; the in-process net.Pipe transport used by most
// tests does not, so the MIT-SHM AttachFd path degrades to plain GetImage when
// it is absent.
type FDSender interface {
	// SendFD writes one already-framed request with fd attached as a single
	// SCM_RIGHTS control message.
	SendFD(msg []byte, fd int) error
}

// SupportsFDPassing reports whether the transport can pass a descriptor to the
// server, which MIT-SHM 1.2 AttachFd requires.
func (c *Conn) SupportsFDPassing() bool {
	_, ok := c.rw.(FDSender)
	return ok
}

// writeRequestFD frames body and writes it in a single sendmsg carrying fd as
// SCM_RIGHTS ancillary data. The caller holds c.mu.
func (c *Conn) writeRequestFD(opcode, data byte, body []byte, fd int) error {
	fw, ok := c.rw.(FDSender)
	if !ok {
		return fmt.Errorf("x11: transport does not support fd passing")
	}
	total := 4 + len(body)
	e := newEncoder(c.order)
	e.put8(opcode)
	e.put8(data)
	e.put16(uint16(total / 4))
	e.putBytes(body)
	if err := fw.SendFD(e.buf, fd); err != nil {
		return err
	}
	c.seq++
	return nil
}

// readReply reads packets until it finds this request's reply or error,
// discarding any event that arrives in between. The 32-byte fixed part lands
// in c.hdr; the additional data, if any, is read into extra when it fits and
// into a fresh slice otherwise. It returns the additional data actually read.
// The caller holds c.mu.
func (c *Conn) readReply(op string, extra []byte) ([]byte, error) {
	for {
		if err := readFull(c.rw, c.hdr[:]); err != nil {
			return nil, err
		}
		switch c.hdr[0] {
		case pktError:
			return nil, c.decodeError(op)
		case pktReply:
			n := int(c.order.Uint32(c.hdr[4:8])) * 4
			if n == 0 {
				return nil, nil
			}
			buf := extra
			if len(buf) < n {
				buf = make([]byte, n)
			}
			buf = buf[:n]
			if err := readFull(c.rw, buf); err != nil {
				return nil, err
			}
			return buf, nil
		default:
			// An event. This client selects no event mask and enables no
			// extension events, so anything arriving here is unsolicited;
			// drain it and keep waiting for the reply. A GenericEvent (35)
			// carries additional 4-byte units past the fixed 32.
			if c.hdr[0]&0x7f == 35 {
				n := int(c.order.Uint32(c.hdr[4:8])) * 4
				if n > 0 {
					if _, err := io.CopyN(io.Discard, c.rw, int64(n)); err != nil {
						return nil, err
					}
				}
			}
		}
	}
}

// decodeError parses the 32-byte error packet sitting in c.hdr.
func (c *Conn) decodeError(op string) *XError {
	d := newDecoder(c.order, c.hdr[:])
	d.skip(1) // 0
	code := d.get8()
	seq := d.get16()
	bad := d.get32()
	minor := d.get16()
	major := d.get8()
	return &XError{Code: code, Name: ErrorName(code), Seq: seq, BadValue: bad,
		Minor: minor, Major: major, Op: op}
}

// roundTrip writes a request and waits for its reply. It returns the 32-byte
// fixed part (a copy, so the caller may hold it) and the additional data.
func (c *Conn) roundTrip(op string, opcode, data byte, body []byte) (hdr [32]byte, extra []byte, err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.writeRequest(opcode, data, body); err != nil {
		return hdr, nil, err
	}
	extra, err = c.readReply(op, nil)
	if err != nil {
		return hdr, nil, err
	}
	return c.hdr, extra, nil
}

// QueryExtension resolves an extension by name, returning whether the server
// implements it and, if so, its major opcode plus its first event and error
// codes. It is the standard gate before using any extension's requests.
func (c *Conn) QueryExtension(name string) (present bool, major, firstEvent, firstError byte, err error) {
	e := newEncoder(c.order)
	e.put16(uint16(len(name)))
	e.skip(2) // unused
	e.putString(name)
	hdr, _, err := c.roundTrip("QueryExtension", opQueryExtension, 0, e.buf)
	if err != nil {
		return false, 0, 0, 0, err
	}
	return hdr[8] != 0, hdr[9], hdr[10], hdr[11], nil
}

// InternAtom resolves — or, when onlyIfExists is false, creates — an atom by
// name.
func (c *Conn) InternAtom(name string, onlyIfExists bool) (uint32, error) {
	e := newEncoder(c.order)
	e.put16(uint16(len(name)))
	e.skip(2) // unused
	e.putString(name)
	data := byte(0)
	if onlyIfExists {
		data = 1
	}
	hdr, _, err := c.roundTrip("InternAtom", opInternAtom, data, e.buf)
	if err != nil {
		return 0, err
	}
	return c.order.Uint32(hdr[8:12]), nil
}

// GetAtomName resolves an atom back to its name.
func (c *Conn) GetAtomName(atom uint32) (string, error) {
	e := newEncoder(c.order)
	e.put32(atom)
	hdr, extra, err := c.roundTrip("GetAtomName", opGetAtomName, 0, e.buf)
	if err != nil {
		return "", err
	}
	n := int(c.order.Uint16(hdr[8:10]))
	if n > len(extra) {
		return "", fmt.Errorf("x11: GetAtomName: truncated name (%d of %d bytes)", len(extra), n)
	}
	return string(extra[:n]), nil
}

// Geometry is a drawable's size and position, as GetGeometry reports it.
type Geometry struct {
	Root        uint32
	Depth       uint8
	X, Y        int16
	Width       uint16
	Height      uint16
	BorderWidth uint16
}

// GetGeometry reads a drawable's geometry.
func (c *Conn) GetGeometry(drawable uint32) (Geometry, error) {
	e := newEncoder(c.order)
	e.put32(drawable)
	hdr, _, err := c.roundTrip("GetGeometry", opGetGeometry, 0, e.buf)
	if err != nil {
		return Geometry{}, err
	}
	d := newDecoder(c.order, hdr[:])
	d.skip(1)
	g := Geometry{Depth: d.get8()}
	d.skip(6) // sequence + reply length
	g.Root = d.get32()
	g.X = d.get16s()
	g.Y = d.get16s()
	g.Width = d.get16()
	g.Height = d.get16()
	g.BorderWidth = d.get16()
	return g, nil
}

// WindowAttributes is the subset of GetWindowAttributes a capture cares about.
type WindowAttributes struct {
	Visual           uint32
	Class            uint16
	MapState         uint8
	OverrideRedirect bool
}

// Viewable reports whether the window is mapped and its ancestors are too,
// which is the condition for its pixels to actually exist on screen.
func (a WindowAttributes) Viewable() bool { return a.MapState == MapStateViewable }

// GetWindowAttributes reads a window's visual, class and map state.
func (c *Conn) GetWindowAttributes(window uint32) (WindowAttributes, error) {
	e := newEncoder(c.order)
	e.put32(window)
	hdr, _, err := c.roundTrip("GetWindowAttributes", opGetWindowAttributes, 0, e.buf)
	if err != nil {
		return WindowAttributes{}, err
	}
	d := newDecoder(c.order, hdr[:])
	d.skip(8) // response type, backing-store, sequence, reply length
	a := WindowAttributes{Visual: d.get32(), Class: d.get16()}
	d.skip(2 + 4 + 4) // bit/win gravity, backing-planes, backing-pixel
	d.skip(2)         // save-under, map-is-installed
	a.MapState = d.get8()
	a.OverrideRedirect = d.get8() != 0
	return a, nil
}

// QueryTree returns a window's root, parent and children, bottom-most first
// as the server states them.
func (c *Conn) QueryTree(window uint32) (root, parent uint32, children []uint32, err error) {
	e := newEncoder(c.order)
	e.put32(window)
	hdr, extra, err := c.roundTrip("QueryTree", opQueryTree, 0, e.buf)
	if err != nil {
		return 0, 0, nil, err
	}
	root = c.order.Uint32(hdr[8:12])
	parent = c.order.Uint32(hdr[12:16])
	n := int(c.order.Uint16(hdr[16:18]))
	if n*4 > len(extra) {
		return 0, 0, nil, fmt.Errorf("x11: QueryTree: %d children announced, %d bytes of list",
			n, len(extra))
	}
	children = make([]uint32, n)
	for i := range children {
		children[i] = c.order.Uint32(extra[i*4:])
	}
	return root, parent, children, nil
}

// Property is the value of one window property.
type Property struct {
	Type   uint32
	Format uint8 // 0, 8, 16 or 32 bits per element
	Value  []byte
	After  uint32 // bytes left unread after Value
}

// Uint32s decodes a format-32 property as its list of 32-bit values. It
// returns nil for any other format.
func (p Property) Uint32s(order ByteOrder) []uint32 {
	if p.Format != 32 {
		return nil
	}
	out := make([]uint32, len(p.Value)/4)
	for i := range out {
		out[i] = order.Uint32(p.Value[i*4:])
	}
	return out
}

// Text decodes a format-8 property as a string with any trailing NUL removed.
// It returns "" for any other format.
func (p Property) Text() string {
	if p.Format != 8 {
		return ""
	}
	return trimNul(p.Value)
}

// GetProperty reads up to maxWords 32-bit words of a window property. A
// property that does not exist comes back with Format 0 and no value, which is
// not an error.
func (c *Conn) GetProperty(window, property, typ uint32, maxWords uint32) (Property, error) {
	e := newEncoder(c.order)
	e.put32(window)
	e.put32(property)
	e.put32(typ)
	e.put32(0) // long-offset
	e.put32(maxWords)
	hdr, extra, err := c.roundTrip("GetProperty", opGetProperty, 0, e.buf)
	if err != nil {
		return Property{}, err
	}
	p := Property{
		Format: hdr[1],
		Type:   c.order.Uint32(hdr[8:12]),
		After:  c.order.Uint32(hdr[12:16]),
	}
	n := int(c.order.Uint32(hdr[16:20]))
	switch p.Format {
	case 8:
		// n is already in bytes.
	case 16:
		n *= 2
	case 32:
		n *= 4
	default:
		return p, nil // format 0: the property does not exist
	}
	if n > len(extra) {
		return Property{}, fmt.Errorf("x11: GetProperty: %d bytes announced, %d received",
			n, len(extra))
	}
	p.Value = extra[:n]
	return p, nil
}

// TranslateCoordinates maps (x, y) in src's coordinate space into dst's.
func (c *Conn) TranslateCoordinates(src, dst uint32, x, y int16) (dx, dy int16, child uint32, err error) {
	e := newEncoder(c.order)
	e.put32(src)
	e.put32(dst)
	e.put16(uint16(x))
	e.put16(uint16(y))
	hdr, _, err := c.roundTrip("TranslateCoordinates", opTranslateCoordinate, 0, e.buf)
	if err != nil {
		return 0, 0, 0, err
	}
	child = c.order.Uint32(hdr[8:12])
	return int16(c.order.Uint16(hdr[12:14])), int16(c.order.Uint16(hdr[14:16])), child, nil
}

// Pointer is where the pointer is, as QueryPointer reports it.
type Pointer struct {
	Root       uint32
	Child      uint32
	RootX      int16
	RootY      int16
	WinX       int16
	WinY       int16
	Mask       uint16
	SameScreen bool
}

// QueryPointer reads the pointer position relative to the given window's root.
func (c *Conn) QueryPointer(window uint32) (Pointer, error) {
	e := newEncoder(c.order)
	e.put32(window)
	hdr, _, err := c.roundTrip("QueryPointer", opQueryPointer, 0, e.buf)
	if err != nil {
		return Pointer{}, err
	}
	p := Pointer{SameScreen: hdr[1] != 0}
	p.Root = c.order.Uint32(hdr[8:12])
	p.Child = c.order.Uint32(hdr[12:16])
	p.RootX = int16(c.order.Uint16(hdr[16:18]))
	p.RootY = int16(c.order.Uint16(hdr[18:20]))
	p.WinX = int16(c.order.Uint16(hdr[20:22]))
	p.WinY = int16(c.order.Uint16(hdr[22:24]))
	p.Mask = c.order.Uint16(hdr[24:26])
	return p, nil
}

// ImageReply is what GetImage answered: the depth and visual of the pixels,
// and how many bytes of them landed in the destination buffer.
type ImageReply struct {
	Depth  uint8
	Visual uint32
	Bytes  int
}

// GetImage fetches a w×h rectangle of drawable at (x, y) as a ZPixmap and
// reads it straight into dst, which must be large enough for the image the
// server will send (Stride(w)*h for the drawable's format). It allocates
// nothing when dst is big enough, which is what keeps the non-MIT-SHM fallback
// out of the garbage collector's way.
//
// It is the slow path: every pixel travels through the socket. Prefer
// [Shm.GetImage].
func (c *Conn) GetImage(drawable uint32, x, y int16, w, h uint16, dst []byte) (ImageReply, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := newEncoder(c.order)
	e.put32(drawable)
	e.put16(uint16(x))
	e.put16(uint16(y))
	e.put16(w)
	e.put16(h)
	e.put32(AllPlanes)
	if err := c.writeRequest(opGetImage, imageFormatZPixmap, e.buf); err != nil {
		return ImageReply{}, err
	}
	extra, err := c.readReply("GetImage", dst)
	if err != nil {
		return ImageReply{}, err
	}
	r := ImageReply{Depth: c.hdr[1], Visual: c.order.Uint32(c.hdr[8:12]), Bytes: len(extra)}
	if len(extra) > 0 && &extra[0] != &dst[0] {
		return r, fmt.Errorf("x11: GetImage: destination buffer holds %d bytes, image needs %d",
			len(dst), len(extra))
	}
	return r, nil
}
