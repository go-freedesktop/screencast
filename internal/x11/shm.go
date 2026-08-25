// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import "fmt"

// This file implements the client side of the MIT-SHM extension, version 1.2,
// in the direction a capture needs: ShmGetImage.
//
// Core GetImage ships every pixel of a frame back through the X socket — for a
// 3840×2160 display at 60 Hz that is two gigabytes a second of socket traffic
// and one memcpy per frame in the server, the kernel and the client. MIT-SHM
// instead has both peers map the same memory: the client allocates an
// anonymous shared segment, passes its descriptor to the server over
// SCM_RIGHTS with AttachFd (the 1.2 request, which needs no SysV shmget and no
// shared key namespace), and each frame is then a fixed 32-byte ShmGetImage
// request after which the pixels are simply THERE.
//
// Everything degrades cleanly: when the server lacks the extension, lacks
// version 1.2, or the transport cannot pass descriptors, the caller keeps
// using core GetImage.

// MIT-SHM minor opcodes (MIT-SHM protocol specification).
const (
	shmReqQueryVersion = 0
	shmReqAttach       = 1
	shmReqDetach       = 2
	shmReqPutImage     = 3
	shmReqGetImage     = 4
	shmReqAttachFd     = 6
)

// ShmName is the extension name passed to QueryExtension.
const ShmName = "MIT-SHM"

// Shm is a queried, ready-to-use MIT-SHM handle: the negotiated major opcode
// and version, and whether AttachFd is usable on this connection.
type Shm struct {
	c         *Conn
	major     byte
	VerMajor  uint16
	VerMinor  uint16
	SharedPix bool  // server supports shared pixmaps
	PixmapFmt uint8 // pixmap format for shared pixmaps
	FDCapable bool  // AttachFd usable: version >= 1.2 AND the transport passes fds
}

// QueryShm queries MIT-SHM and its version. It returns (nil, nil) — no error —
// when the server does not implement the extension, so the caller simply falls
// back to GetImage. FDCapable additionally requires the transport to support
// descriptor passing.
func (c *Conn) QueryShm() (*Shm, error) {
	present, major, _, _, err := c.QueryExtension(ShmName)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	hdr, _, err := c.roundTrip("ShmQueryVersion", major, shmReqQueryVersion, nil)
	if err != nil {
		return nil, err
	}
	s := &Shm{
		c:         c,
		major:     major,
		SharedPix: hdr[1] != 0,
		VerMajor:  c.order.Uint16(hdr[8:10]),
		VerMinor:  c.order.Uint16(hdr[10:12]),
		PixmapFmt: hdr[16],
	}
	s.FDCapable = c.SupportsFDPassing() && shmAtLeast(s.VerMajor, s.VerMinor, 1, 2)
	return s, nil
}

// Major returns the extension's negotiated major opcode.
func (s *Shm) Major() byte { return s.major }

// shmAtLeast reports whether version maj.min is at least wantMaj.wantMin.
func shmAtLeast(maj, min, wantMaj, wantMin uint16) bool {
	return maj > wantMaj || (maj == wantMaj && min >= wantMin)
}

// AttachFd registers the shared-memory segment named by seg, backed by fd,
// with the server. The descriptor is passed over SCM_RIGHTS; readOnly declares
// whether the server may only read the segment — a capture needs the server to
// WRITE into it, so a capture passes false. The server takes ownership of the
// passed descriptor and closes its copy on Detach.
func (s *Shm) AttachFd(seg uint32, fd int, readOnly bool) error {
	e := newEncoder(s.c.order)
	e.put32(seg)
	ro := byte(0)
	if readOnly {
		ro = 1
	}
	e.put8(ro)
	e.skip(3) // unused
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	return s.c.writeRequestFD(s.major, shmReqAttachFd, e.buf, fd)
}

// Detach releases a previously attached segment.
func (s *Shm) Detach(seg uint32) error {
	e := newEncoder(s.c.order)
	e.put32(seg)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	return s.c.writeRequest(s.major, shmReqDetach, e.buf)
}

// encodeGetImage builds the ShmGetImage request body. It is split out so the
// wire layout is testable without a server: sixteen well-defined bytes plus
// the segment and the offset.
func encodeGetImage(order ByteOrder, drawable uint32, x, y int16, w, h uint16,
	seg, offset uint32) []byte {
	e := newEncoder(order)
	e.put32(drawable)
	e.put16(uint16(x))
	e.put16(uint16(y))
	e.put16(w)
	e.put16(h)
	e.put32(AllPlanes)
	e.put8(imageFormatZPixmap)
	e.skip(3) // unused
	e.put32(seg)
	e.put32(offset)
	return e.buf
}

// GetImage asks the server to write the w×h rectangle of drawable at (x, y)
// into the attached segment seg, at byte offset. No pixel travels through the
// socket: the request is 32 bytes and the reply is 32 bytes, and when the reply
// arrives the pixels are already in the mapped segment.
//
// The returned ImageReply's Bytes is the size the server reports it wrote.
func (s *Shm) GetImage(drawable uint32, x, y int16, w, h uint16, seg, offset uint32) (ImageReply, error) {
	body := encodeGetImage(s.c.order, drawable, x, y, w, h, seg, offset)
	s.c.mu.Lock()
	defer s.c.mu.Unlock()
	if err := s.c.writeRequest(s.major, shmReqGetImage, body); err != nil {
		return ImageReply{}, err
	}
	if _, err := s.c.readReply("ShmGetImage", nil); err != nil {
		return ImageReply{}, err
	}
	return ImageReply{
		Depth:  s.c.hdr[1],
		Visual: s.c.order.Uint32(s.c.hdr[8:12]),
		Bytes:  int(s.c.order.Uint32(s.c.hdr[12:16])),
	}, nil
}

// Segment is a mapped anonymous shared-memory region backing a MIT-SHM
// attachment: Data is the pixel store both peers see, FD is the descriptor
// handed to the X server by [Shm.AttachFd], and Seg is the resource id the
// server knows it by.
//
// The lifecycle is transport-agnostic; the shared-memory syscalls themselves
// live behind createAnonFile/mmapRegion/munmapRegion/closeFD, which are
// provided per platform. Off Linux there is no X server to attach to, so
// createAnonFile reports an error and no segment is ever created.
type Segment struct {
	Seg  uint32
	FD   int
	Data []byte
	size int
}

// NewSegment allocates and maps a shared-memory segment of size bytes and
// assigns it the resource id seg. The caller registers it with the server via
// [Shm.AttachFd] and frees it with [Segment.Close].
func NewSegment(seg uint32, size int) (*Segment, error) {
	fd, err := createAnonFile(size)
	if err != nil {
		return nil, err
	}
	data, err := mmapRegion(fd, size)
	if err != nil {
		_ = closeFD(fd)
		return nil, fmt.Errorf("x11: shm mmap: %w", err)
	}
	return &Segment{Seg: seg, FD: fd, Data: data, size: size}, nil
}

// Size returns the segment's byte length.
func (s *Segment) Size() int { return s.size }

// Close unmaps the region and closes its descriptor, returning the first
// error; both steps are attempted regardless. It is idempotent.
func (s *Segment) Close() error {
	var first error
	if s.Data != nil {
		if err := munmapRegion(s.Data); err != nil && first == nil {
			first = err
		}
		s.Data = nil
	}
	if s.FD >= 0 {
		if err := closeFD(s.FD); err != nil && first == nil {
			first = err
		}
		s.FD = -1
	}
	return first
}
