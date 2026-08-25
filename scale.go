// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

// X11 has no server-side scaler: GetImage hands back the pixels that are
// there, at the size they are. When a consumer asks for a frame size other
// than the source's, the resampling happens here, on the CPU.
//
// The resampler is nearest-neighbour, and it precomputes its source-index maps
// ONCE, at stream creation, so a frame costs one table lookup and one 4-byte
// copy per output pixel and NO arithmetic per pixel and NO allocation. A
// smarter filter would look better and cost several times as much; a consumer
// that wants one should ask for the native size and resample on the GPU it is
// already compositing with.

// scaler resamples BGRA frames from one fixed size to another.
type scaler struct {
	srcW, srcH int
	dstW, dstH int
	// outW, outH is the size the content actually occupies in the
	// destination, and offX, offY where it sits. They differ from dstW, dstH
	// only when letterboxing.
	outW, outH int
	offX, offY int
	letterbox  bool

	xmap []int32 // outW entries: destination column to source column
	ymap []int32 // outH entries: destination row to source row
}

// newScaler builds the resampler from srcW×srcH to dstW×dstH. When fit is
// true the source keeps its aspect ratio and is centred in the destination,
// with the margins left opaque black; otherwise it is stretched to fill.
//
// It returns nil when no resampling is needed, which is the caller's signal to
// hand the captured buffer straight through.
func newScaler(srcW, srcH, dstW, dstH int, fit bool) *scaler {
	if srcW <= 0 || srcH <= 0 || dstW <= 0 || dstH <= 0 {
		return nil
	}
	if srcW == dstW && srcH == dstH {
		return nil
	}
	s := &scaler{srcW: srcW, srcH: srcH, dstW: dstW, dstH: dstH, outW: dstW, outH: dstH}
	if fit {
		// Whichever axis runs out first sets the scale.
		if srcW*dstH > dstW*srcH {
			s.outW, s.outH = dstW, srcH*dstW/srcW
		} else {
			s.outW, s.outH = srcW*dstH/srcH, dstH
		}
		if s.outW < 1 {
			s.outW = 1
		}
		if s.outH < 1 {
			s.outH = 1
		}
		s.offX = (dstW - s.outW) / 2
		s.offY = (dstH - s.outH) / 2
		s.letterbox = s.outW != dstW || s.outH != dstH
	}
	// Centre sampling: output pixel i takes the source pixel under the middle
	// of its footprint, so a downscale does not drift half a pixel towards
	// the origin. The result is always in [0, src): with i < out, the largest
	// value is ((out-1)*src + src/2)/out, which is strictly below src.
	s.xmap = make([]int32, s.outW)
	for i := range s.xmap {
		s.xmap[i] = int32((i*srcW + srcW/2) / s.outW)
	}
	s.ymap = make([]int32, s.outH)
	for i := range s.ymap {
		s.ymap[i] = int32((i*srcH + srcH/2) / s.outH)
	}
	return s
}

// dstStride is the number of bytes per row of the resampler's output.
func (s *scaler) dstStride() int { return s.dstW * 4 }

// bufLen is the number of bytes an output frame occupies.
func (s *scaler) bufLen() int { return s.dstStride() * s.dstH }

// scale resamples one frame. src is srcStride bytes per row; dst is
// s.dstStride() bytes per row and at least s.bufLen() long. It allocates
// nothing.
func (s *scaler) scale(dst, src []byte, srcStride int) {
	stride := s.dstStride()
	if s.letterbox {
		// Only the margins need clearing, and only to opaque black; the
		// content area is overwritten wholesale below.
		clearOpaque(dst[:stride*s.dstH])
	}
	for y := 0; y < s.outH; y++ {
		srow := src[int(s.ymap[y])*srcStride:]
		drow := dst[(y+s.offY)*stride+s.offX*4:]
		for x := 0; x < s.outW; x++ {
			sx := int(s.xmap[x]) * 4
			copy(drow[x*4:x*4+4], srow[sx:sx+4])
		}
	}
}

// clearOpaque writes opaque black over a BGRA buffer.
func clearOpaque(b []byte) {
	for i := range b {
		if i%4 == 3 {
			b[i] = 0xff
		} else {
			b[i] = 0
		}
	}
}

// blendCursor composites a premultiplied-alpha BGRA cursor bitmap onto a BGRA
// frame at (ox, oy), clipping to the frame. It is source-over:
// dst = src + dst*(1-alpha). It allocates nothing.
//
// The X server never draws the pointer into the framebuffer — it is a sprite
// composited at scan-out — so a capture that wants the cursor has to put it
// there itself.
func blendCursor(dst []byte, dstW, dstH, dstStride int,
	cur []byte, curW, curH, curStride, ox, oy int) {
	if curW <= 0 || curH <= 0 {
		return
	}
	for y := 0; y < curH; y++ {
		dy := oy + y
		if dy < 0 || dy >= dstH {
			continue
		}
		srow := cur[y*curStride:]
		drow := dst[dy*dstStride:]
		for x := 0; x < curW; x++ {
			dx := ox + x
			if dx < 0 || dx >= dstW {
				continue
			}
			a := uint32(srow[x*4+3])
			if a == 0 {
				continue
			}
			d := drow[dx*4 : dx*4+4]
			s := srow[x*4 : x*4+4]
			if a == 0xff {
				copy(d, s)
				continue
			}
			inv := 255 - a
			d[0] = byte(uint32(s[0]) + uint32(d[0])*inv/255)
			d[1] = byte(uint32(s[1]) + uint32(d[1])*inv/255)
			d[2] = byte(uint32(s[2]) + uint32(d[2])*inv/255)
			d[3] = byte(uint32(s[3]) + uint32(d[3])*inv/255)
		}
	}
}
