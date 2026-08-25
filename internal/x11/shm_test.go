// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"errors"
	"testing"
	"time"
)

// shmReplies scripts a server that offers MIT-SHM at the given version.
func shmReplies(t *testing.T, order ByteOrder, major uint16, minor uint16,
	extra func(op, data byte, body []byte) []byte) func(op, data byte, body []byte) []byte {
	t.Helper()
	return func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 130, 65, 128}, nil)
		}
		if op == 130 && data == shmReqQueryVersion {
			fixed := make([]byte, 24)
			order.PutUint16(fixed[0:2], major)
			order.PutUint16(fixed[2:4], minor)
			fixed[8] = 2 // pixmap format
			return reply(order, 1, fixed, nil)
		}
		if extra != nil {
			return extra(op, data, body)
		}
		return nil
	}
}

func TestQueryShm(t *testing.T) {
	order := binary.LittleEndian
	c, _, _ := dialFakeFD(t, shmReplies(t, order, 1, 2, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if s == nil {
		t.Fatal("QueryShm reported no extension")
	}
	if s.VerMajor != 1 || s.VerMinor != 2 || !s.SharedPix || s.PixmapFmt != 2 {
		t.Errorf("Shm = %+v", s)
	}
	if !s.FDCapable {
		t.Error("MIT-SHM 1.2 over an fd-passing transport reported FDCapable false")
	}
	if s.Major() != 130 {
		t.Errorf("Major() = %d", s.Major())
	}
}

func TestQueryShmAbsent(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{0, 0, 0, 0}, nil)
	})
	s, err := c.QueryShm()
	if err != nil || s != nil {
		t.Fatalf("QueryShm = %+v, %v; want nil, nil for a server without the extension", s, err)
	}
}

func TestQueryShmVersionTooOld(t *testing.T) {
	order := binary.LittleEndian
	c, _, _ := dialFakeFD(t, shmReplies(t, order, 1, 1, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if s.FDCapable {
		t.Error("MIT-SHM 1.1 reported FDCapable true; AttachFd arrived in 1.2")
	}
}

func TestQueryShmWithoutFDPassing(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, shmReplies(t, order, 1, 2, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if s.FDCapable {
		t.Error("MIT-SHM 1.2 over a transport that cannot pass descriptors reported FDCapable true")
	}
}

func TestShmAtLeast(t *testing.T) {
	for _, tc := range []struct {
		maj, min, wMaj, wMin uint16
		want                 bool
	}{
		{1, 2, 1, 2, true}, {1, 3, 1, 2, true}, {2, 0, 1, 2, true},
		{1, 1, 1, 2, false}, {0, 9, 1, 0, false},
	} {
		if got := shmAtLeast(tc.maj, tc.min, tc.wMaj, tc.wMin); got != tc.want {
			t.Errorf("shmAtLeast(%d.%d, %d.%d) = %v", tc.maj, tc.min, tc.wMaj, tc.wMin, got)
		}
	}
}

func TestQueryShmErrors(t *testing.T) {
	order := binary.LittleEndian
	// QueryExtension itself fails.
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c.QueryShm(); err == nil {
		t.Error("QueryShm accepted a failed QueryExtension")
	}
	// The extension is there but ShmQueryVersion fails.
	c2, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 130, 0, 0}, nil)
		}
		return errorPacket(order, ErrCodeAccess, 0, op, 0)
	})
	if _, err := c2.QueryShm(); err == nil {
		t.Error("QueryShm accepted a failed ShmQueryVersion")
	}
}

func TestEncodeGetImage(t *testing.T) {
	order := binary.LittleEndian
	body := encodeGetImage(order, 0x100, -5, 7, 640, 480, 0x200001, 0)
	if len(body) != 28 {
		t.Fatalf("ShmGetImage body is %d bytes, want 28", len(body))
	}
	if order.Uint32(body[0:4]) != 0x100 {
		t.Errorf("drawable = %#x", order.Uint32(body[0:4]))
	}
	if int16(order.Uint16(body[4:6])) != -5 || int16(order.Uint16(body[6:8])) != 7 {
		t.Errorf("x, y = %d, %d", int16(order.Uint16(body[4:6])), int16(order.Uint16(body[6:8])))
	}
	if order.Uint16(body[8:10]) != 640 || order.Uint16(body[10:12]) != 480 {
		t.Errorf("w, h = %d, %d", order.Uint16(body[8:10]), order.Uint16(body[10:12]))
	}
	if order.Uint32(body[12:16]) != AllPlanes {
		t.Errorf("plane mask = %#x", order.Uint32(body[12:16]))
	}
	if body[16] != imageFormatZPixmap {
		t.Errorf("format = %d, want ZPixmap", body[16])
	}
	if order.Uint32(body[20:24]) != 0x200001 || order.Uint32(body[24:28]) != 0 {
		t.Errorf("seg, offset = %#x, %d", order.Uint32(body[20:24]), order.Uint32(body[24:28]))
	}
}

func TestShmGetImage(t *testing.T) {
	order := binary.LittleEndian
	c, f, _ := dialFakeFD(t, shmReplies(t, order, 1, 2, func(op, data byte, body []byte) []byte {
		if op == 130 && data == shmReqGetImage {
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], 0x21)
			order.PutUint32(fixed[4:8], 1234)
			return reply(order, 24, fixed, nil)
		}
		return nil
	}))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	r, err := s.GetImage(0x100, 0, 0, 640, 480, 0x200001, 0)
	if err != nil {
		t.Fatal(err)
	}
	if r.Depth != 24 || r.Visual != 0x21 || r.Bytes != 1234 {
		t.Errorf("ImageReply = %+v", r)
	}
	// The whole point of MIT-SHM: the frame's pixels never touch the socket,
	// so the request is a fixed 32 bytes no matter how big the image is.
	if got := len(f.lastRequest().Body) + 4; got != 32 {
		t.Errorf("ShmGetImage request is %d bytes, want 32", got)
	}
}

func TestShmGetImageError(t *testing.T) {
	order := binary.LittleEndian
	c, _, _ := dialFakeFD(t, shmReplies(t, order, 1, 2, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeAccess, 0, op, 0)
	}))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetImage(1, 0, 0, 2, 2, 3, 0); err == nil {
		t.Fatal("ShmGetImage accepted an error reply")
	}
}

func TestShmAttachFdAndDetach(t *testing.T) {
	order := binary.LittleEndian
	c, f, p := dialFakeFD(t, shmReplies(t, order, 1, 2, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if err := s.AttachFd(0x200001, 42, false); err != nil {
		t.Fatal(err)
	}
	if fds := p.fds(); len(fds) != 1 || fds[0] != 42 {
		t.Errorf("SendFD got %v, want [42]", fds)
	}
	eventually(t, time.Second, func() bool { return f.lastRequest().Data == shmReqAttachFd })
	r := f.lastRequest()
	if r.Op != 130 || r.Data != shmReqAttachFd || len(r.Body) != 8 {
		t.Fatalf("AttachFd request = %+v", r)
	}
	if order.Uint32(r.Body[0:4]) != 0x200001 || r.Body[4] != 0 {
		t.Errorf("AttachFd body = % x", r.Body)
	}
	if err := s.AttachFd(0x200002, 43, true); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool {
		r := f.lastRequest()
		return r.Data == shmReqAttachFd && len(r.Body) == 8 && r.Body[4] == 1
	})
	if err := s.Detach(0x200001); err != nil {
		t.Fatal(err)
	}
	eventually(t, time.Second, func() bool { return f.lastRequest().Data == shmReqDetach })
}

func TestShmAttachFdSendFailure(t *testing.T) {
	order := binary.LittleEndian
	c, _, p := dialFakeFD(t, shmReplies(t, order, 1, 2, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	p.mu.Lock()
	p.err = errors.New("sendmsg refused")
	p.mu.Unlock()
	if err := s.AttachFd(1, 2, false); err == nil {
		t.Fatal("AttachFd reported success despite a sendmsg failure")
	}
}

// The Segment lifecycle -- allocate, map, close twice, and each failure of
// those -- moved to github.com/go-freedesktop/x11 with the segment itself, and
// is proved there on every platform. What stays here is MIT-SHM: the requests
// that hand a segment to the server and ask it to fill one.

func TestShmRequestsOverAClosedConnection(t *testing.T) {
	order := binary.LittleEndian
	c, _, _ := dialFakeFD(t, shmReplies(t, order, 1, 2, nil))
	s, err := c.QueryShm()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetImage(1, 0, 0, 2, 2, 3, 0); err == nil {
		t.Error("ShmGetImage over a closed connection succeeded")
	}
	if err := s.Detach(3); err == nil {
		t.Error("ShmDetach over a closed connection succeeded")
	}
}
