// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import "testing"

// bgra builds a w×h test image whose pixels encode their own coordinates, so a
// resampler that reads the wrong source pixel is caught by value rather than
// by eye.
func bgra(w, h int) []byte {
	b := make([]byte, w*h*4)
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			o := (y*w + x) * 4
			b[o+0] = byte(x)
			b[o+1] = byte(y)
			b[o+2] = byte(x*7 + y*3)
			b[o+3] = 0xff
		}
	}
	return b
}

func TestNewScalerReportsNoWorkNeeded(t *testing.T) {
	if s := newScaler(100, 100, 100, 100, false); s != nil {
		t.Error("newScaler built a resampler for an identical size")
	}
	for _, wh := range [][4]int{{0, 10, 10, 10}, {10, 0, 10, 10}, {10, 10, 0, 10}, {10, 10, 10, 0},
		{-1, 10, 10, 10}} {
		if s := newScaler(wh[0], wh[1], wh[2], wh[3], false); s != nil {
			t.Errorf("newScaler(%v) built a resampler for an empty size", wh)
		}
	}
}

func TestScalerStretch(t *testing.T) {
	src := bgra(4, 4)
	s := newScaler(4, 4, 8, 2, false)
	if s == nil {
		t.Fatal("newScaler returned nothing")
	}
	if s.dstStride() != 32 || s.bufLen() != 64 {
		t.Fatalf("dstStride=%d bufLen=%d", s.dstStride(), s.bufLen())
	}
	if s.letterbox {
		t.Error("a stretch reported itself letterboxed")
	}
	dst := make([]byte, s.bufLen())
	s.scale(dst, src, 16)
	// Doubling the width maps destination columns 0 and 1 to source column 0,
	// 2 and 3 to source column 1, and so on.
	for x := 0; x < 8; x++ {
		if got := dst[x*4]; got != byte(x/2) {
			t.Errorf("column %d took source column %d, want %d", x, got, x/2)
		}
	}
	// Halving the height CENTRE-samples: two output rows take source rows 1
	// and 3, not 0 and 2, so the picture does not drift half a pixel up.
	if got := dst[1]; got != 1 {
		t.Errorf("destination row 0 took source row %d, want 1", got)
	}
	if got := dst[32+1]; got != 3 {
		t.Errorf("destination row 1 took source row %d, want 3", got)
	}
}

func TestScalerLetterbox(t *testing.T) {
	// A 4:1 source into a square destination must sit in a horizontal band
	// with black above and below.
	src := bgra(8, 2)
	s := newScaler(8, 2, 8, 8, true)
	if s == nil {
		t.Fatal("newScaler returned nothing")
	}
	if !s.letterbox || s.outW != 8 || s.outH != 2 || s.offX != 0 || s.offY != 3 {
		t.Fatalf("letterbox = %+v", s)
	}
	dst := make([]byte, s.bufLen())
	s.scale(dst, src, 32)
	for y := 0; y < 8; y++ {
		o := y*32 + 4*4
		if y >= 3 && y < 5 {
			if dst[o+3] != 0xff || dst[o] != 4 {
				t.Errorf("content row %d = % x", y, dst[o:o+4])
			}
			continue
		}
		if dst[o] != 0 || dst[o+1] != 0 || dst[o+2] != 0 || dst[o+3] != 0xff {
			t.Errorf("margin row %d = % x, want opaque black", y, dst[o:o+4])
		}
	}

	// And the other way round: a tall source gets vertical bars.
	s2 := newScaler(2, 8, 8, 8, true)
	if s2 == nil || !s2.letterbox || s2.outW != 2 || s2.offX != 3 {
		t.Fatalf("tall letterbox = %+v", s2)
	}
}

func TestScalerFitWithMatchingAspect(t *testing.T) {
	// Same aspect ratio: fit is exact, so there are no margins to clear.
	s := newScaler(4, 2, 8, 4, true)
	if s == nil {
		t.Fatal("newScaler returned nothing")
	}
	if s.letterbox || s.outW != 8 || s.outH != 4 || s.offX != 0 || s.offY != 0 {
		t.Fatalf("exact fit = %+v", s)
	}
}

func TestScalerClampsToAtLeastOnePixel(t *testing.T) {
	// An extreme aspect ratio must not compute a zero-pixel content area,
	// which would produce an empty index map and a blank frame.
	s := newScaler(1000, 1, 10, 10, true)
	if s == nil || s.outH < 1 || len(s.ymap) != s.outH {
		t.Fatalf("extreme fit = %+v", s)
	}
	s2 := newScaler(1, 1000, 10, 10, true)
	if s2 == nil || s2.outW < 1 || len(s2.xmap) != s2.outW {
		t.Fatalf("extreme fit = %+v", s2)
	}
	dst := make([]byte, s.bufLen())
	s.scale(dst, bgra(1000, 1), 4000)
}

func TestScalerIndexMapsStayInRange(t *testing.T) {
	// The rounding in the index maps must never point past the last source
	// pixel, which would be an out-of-range read on the hot path.
	for _, wh := range [][4]int{{7, 5, 3, 2}, {3, 2, 7, 5}, {1, 1, 100, 100}, {100, 100, 1, 1}} {
		s := newScaler(wh[0], wh[1], wh[2], wh[3], false)
		if s == nil {
			continue
		}
		for _, v := range s.xmap {
			if int(v) < 0 || int(v) >= wh[0] {
				t.Fatalf("%v: xmap entry %d out of range", wh, v)
			}
		}
		for _, v := range s.ymap {
			if int(v) < 0 || int(v) >= wh[1] {
				t.Fatalf("%v: ymap entry %d out of range", wh, v)
			}
		}
		dst := make([]byte, s.bufLen())
		s.scale(dst, bgra(wh[0], wh[1]), wh[0]*4)
	}
}

func TestScalerDoesNotAllocate(t *testing.T) {
	s := newScaler(640, 480, 320, 240, false)
	src := bgra(640, 480)
	dst := make([]byte, s.bufLen())
	if got := testing.AllocsPerRun(20, func() { s.scale(dst, src, 640*4) }); got != 0 {
		t.Errorf("scale allocated %v times per run", got)
	}
}

func TestClearOpaque(t *testing.T) {
	b := make([]byte, 12)
	for i := range b {
		b[i] = 0x55
	}
	clearOpaque(b)
	for i, v := range b {
		want := byte(0)
		if i%4 == 3 {
			want = 0xff
		}
		if v != want {
			t.Fatalf("byte %d = %#x, want %#x", i, v, want)
		}
	}
}

func TestBlendCursor(t *testing.T) {
	// An opaque 2x2 white cursor lands exactly where it is put.
	dst := make([]byte, 4*4*4)
	cur := []byte{
		255, 255, 255, 255, 255, 255, 255, 255,
		255, 255, 255, 255, 255, 255, 255, 255,
	}
	blendCursor(dst, 4, 4, 16, cur, 2, 2, 8, 1, 1)
	for y := 0; y < 4; y++ {
		for x := 0; x < 4; x++ {
			o := y*16 + x*4
			inside := x >= 1 && x < 3 && y >= 1 && y < 3
			if inside != (dst[o] == 255) {
				t.Fatalf("pixel (%d,%d) = % x, inside=%v", x, y, dst[o:o+4], inside)
			}
		}
	}
}

func TestBlendCursorPremultipliedAlpha(t *testing.T) {
	// A half-transparent white pixel is stored premultiplied (0x80 on every
	// channel) and composites source-over onto an opaque black frame.
	dst := []byte{0, 0, 0, 255}
	cur := []byte{0x80, 0x80, 0x80, 0x80}
	blendCursor(dst, 1, 1, 4, cur, 1, 1, 4, 0, 0)
	// 0x80 + 0 * (255-0x80)/255 = 0x80 on the colour channels; the alpha
	// becomes 0x80 + 255*127/255 = 255.
	if dst[0] != 0x80 || dst[1] != 0x80 || dst[2] != 0x80 || dst[3] != 0xff {
		t.Fatalf("blended = % x, want 80 80 80 ff", dst)
	}
	// A fully transparent cursor pixel changes nothing.
	dst2 := []byte{1, 2, 3, 255}
	blendCursor(dst2, 1, 1, 4, []byte{9, 9, 9, 0}, 1, 1, 4, 0, 0)
	if dst2[0] != 1 || dst2[1] != 2 || dst2[2] != 3 {
		t.Fatalf("a transparent cursor pixel changed the frame: % x", dst2)
	}
}

func TestBlendCursorClipsToTheFrame(t *testing.T) {
	// A cursor half off the top-left and one half off the bottom-right must
	// draw only what is inside, and must never index outside the frame.
	dst := make([]byte, 4*4*4)
	cur := make([]byte, 4*4*4)
	for i := range cur {
		cur[i] = 0xff
	}
	blendCursor(dst, 4, 4, 16, cur, 4, 4, 16, -2, -2)
	if dst[0] != 0xff {
		t.Errorf("the visible part of a clipped cursor was not drawn: % x", dst[0:4])
	}
	if dst[2*16+2*4] != 0 {
		t.Errorf("a clipped cursor drew past its size")
	}
	dst2 := make([]byte, 4*4*4)
	blendCursor(dst2, 4, 4, 16, cur, 4, 4, 16, 3, 3)
	if dst2[3*16+3*4] != 0xff {
		t.Errorf("the bottom-right corner was not drawn")
	}
	// Entirely outside: nothing happens and nothing panics.
	dst3 := make([]byte, 4*4*4)
	blendCursor(dst3, 4, 4, 16, cur, 4, 4, 16, 100, 100)
	blendCursor(dst3, 4, 4, 16, cur, 4, 4, 16, -100, -100)
	for _, v := range dst3 {
		if v != 0 {
			t.Fatal("an off-frame cursor touched the frame")
		}
	}
	// An empty cursor is a no-op rather than a division by zero.
	blendCursor(dst3, 4, 4, 16, nil, 0, 0, 0, 0, 0)
	blendCursor(dst3, 4, 4, 16, nil, 4, 0, 16, 0, 0)
}
