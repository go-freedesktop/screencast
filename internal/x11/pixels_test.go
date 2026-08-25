// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"strings"
	"testing"
)

func TestMaskShift(t *testing.T) {
	for _, tc := range []struct {
		mask        uint32
		shift, bits uint
	}{
		{0x00ff0000, 16, 8}, {0x0000ff00, 8, 8}, {0x000000ff, 0, 8},
		{0xf800, 11, 5}, {0x07e0, 5, 6}, {0x001f, 0, 5},
		{0x3ff00000, 20, 10}, {0, 0, 0},
	} {
		s, b := maskShift(tc.mask)
		if s != tc.shift || b != tc.bits {
			t.Errorf("maskShift(%#x) = %d, %d; want %d, %d", tc.mask, s, b, tc.shift, tc.bits)
		}
	}
}

func TestScaleTable(t *testing.T) {
	if got := scaleTable(0); len(got) != 1 || got[0] != 0 {
		t.Errorf("scaleTable(0) = %v", got)
	}
	t8 := scaleTable(8)
	if len(t8) != 256 || t8[0] != 0 || t8[255] != 255 || t8[128] != 128 {
		t.Errorf("scaleTable(8) is wrong: len=%d [0]=%d [128]=%d [255]=%d",
			len(t8), t8[0], t8[128], t8[255])
	}
	// A 5-bit channel's maximum must widen to 255, not to 248: that is the
	// difference between white and off-white on a 16-bit visual.
	t5 := scaleTable(5)
	if len(t5) != 32 || t5[31] != 255 || t5[0] != 0 {
		t.Errorf("scaleTable(5) is wrong: len=%d [0]=%d [31]=%d", len(t5), t5[0], t5[31])
	}
	t6 := scaleTable(6)
	if len(t6) != 64 || t6[63] != 255 {
		t.Errorf("scaleTable(6) is wrong: len=%d [63]=%d", len(t6), t6[63])
	}
	// A channel wider than 16 bits does not exist; the table is clamped
	// rather than allowed to allocate a gigabyte.
	if got := scaleTable(24); len(got) != 1<<16 {
		t.Errorf("scaleTable(24) allocated %d entries", len(got))
	}
}

func TestByteOfShift(t *testing.T) {
	for _, tc := range []struct {
		shift uint
		order uint8
		want  int
	}{
		{0, ImageOrderLSB, 0}, {8, ImageOrderLSB, 1}, {16, ImageOrderLSB, 2}, {24, ImageOrderLSB, 3},
		{0, ImageOrderMSB, 3}, {8, ImageOrderMSB, 2}, {16, ImageOrderMSB, 1}, {24, ImageOrderMSB, 0},
	} {
		if got := byteOfShift(tc.shift, tc.order); got != tc.want {
			t.Errorf("byteOfShift(%d, %d) = %d, want %d", tc.shift, tc.order, got, tc.want)
		}
	}
}

func TestNewConverterRejectsWhatItCannotDecompose(t *testing.T) {
	good := VisualType{ID: 1, Class: VisualTrueColor,
		RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff}
	for _, tc := range []struct {
		name string
		f    Format
		v    VisualType
		want string
	}{
		{"palette visual", fmt32, VisualType{ID: 2, Class: VisualPseudoColor,
			RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff}, "colormap"},
		{"8 bits per pixel", Format{Depth: 8, BitsPerPix: 8, ScanlinePad: 32}, good, "bits per pixel"},
		{"zero scanline pad", Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 0}, good, "scanline pad"},
		{"no red mask", fmt32, VisualType{ID: 3, Class: VisualTrueColor,
			GreenMask: 0xff00, BlueMask: 0xff}, "empty channel mask"},
	} {
		_, err := NewConverter(tc.f, tc.v, ImageOrderLSB)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: NewConverter reported %v, want an error mentioning %q",
				tc.name, err, tc.want)
		}
	}
}

func TestConverterIdentity(t *testing.T) {
	c, err := NewConverter(fmt32, bgrx24, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Identity || !c.InPlace || c.HasAlpha {
		t.Fatalf("Converter = %+v", c)
	}
	if !strings.Contains(c.String(), "already BGRA") {
		t.Errorf("String() = %q", c.String())
	}
	if c.SrcStride(4) != 16 || c.DstStride(4) != 16 {
		t.Errorf("strides = %d, %d", c.SrcStride(4), c.DstStride(4))
	}

	// In place: only the alpha byte changes.
	src := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	if err := c.Convert(src, src, 2, 1, 8, 8); err != nil {
		t.Fatal(err)
	}
	want := []byte{1, 2, 3, 0xff, 4, 5, 6, 0xff}
	for i := range want {
		if src[i] != want[i] {
			t.Fatalf("in-place identity = % x, want % x", src, want)
		}
	}
	// Into a separate destination.
	src2 := []byte{1, 2, 3, 0, 4, 5, 6, 0}
	dst := make([]byte, 8)
	if err := c.Convert(dst, src2, 2, 1, 8, 8); err != nil {
		t.Fatal(err)
	}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("copied identity = % x, want % x", dst, want)
		}
	}
}

func TestConverterIdentityWithAlphaChannel(t *testing.T) {
	// A depth-32 visual's fourth byte is a real alpha channel, so it is
	// carried through rather than forced opaque.
	f := Format{Depth: 32, BitsPerPix: 32, ScanlinePad: 32}
	c, err := NewConverter(f, VisualType{ID: 1, Class: VisualTrueColor,
		RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff}, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if !c.HasAlpha {
		t.Fatal("a depth-32 visual reported HasAlpha false")
	}
	src := []byte{1, 2, 3, 0x40}
	if err := c.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if src[3] != 0x40 {
		t.Errorf("alpha = %#x, want 0x40 carried through", src[3])
	}
}

func TestConverterSwapsRedAndBlue(t *testing.T) {
	c, err := NewConverter(fmt32, rgbx24, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.Identity {
		t.Fatal("an RGBX visual reported itself already BGRA")
	}
	if !c.shuffle {
		t.Fatal("a byte-aligned 8-bit visual did not take the shuffle path")
	}
	// Memory holds R,G,B,X; the result must hold B,G,R,255.
	src := []byte{0x10, 0x20, 0x30, 0}
	if err := c.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x30, 0x20, 0x10, 0xff}
	for i := range want {
		if src[i] != want[i] {
			t.Fatalf("swap = % x, want % x", src, want)
		}
	}
}

func TestConverterBigEndianImageOrder(t *testing.T) {
	c, err := NewConverter(fmt32, bgrx24, ImageOrderMSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.Identity {
		t.Fatal("an MSBFirst server reported itself already BGRA")
	}
	if !strings.Contains(c.String(), "MSBFirst") {
		t.Errorf("String() = %q", c.String())
	}
	// MSBFirst: memory holds X,R,G,B for masks 0xff0000/0xff00/0xff.
	src := []byte{0, 0x10, 0x20, 0x30}
	if err := c.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x30, 0x20, 0x10, 0xff}
	for i := range want {
		if src[i] != want[i] {
			t.Fatalf("MSBFirst convert = % x, want % x", src, want)
		}
	}
}

func TestConverterUnalignedMasks(t *testing.T) {
	// A 30-bit-per-pixel visual: ten bits per channel, so no byte alignment
	// and no shuffle. This is the general mask-driven path.
	v := VisualType{ID: 1, Class: VisualTrueColor,
		RedMask: 0x3ff00000, GreenMask: 0x000ffc00, BlueMask: 0x000003ff}
	f := Format{Depth: 30, BitsPerPix: 32, ScanlinePad: 32}
	c, err := NewConverter(f, v, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.shuffle || c.Identity {
		t.Fatal("a 10-bit-per-channel visual took a byte-wise fast path")
	}
	// Full white in ten-bit channels must come out as 255, 255, 255.
	src := []byte{0xff, 0xff, 0xff, 0x3f}
	if err := c.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if src[i] != 0xff {
			t.Fatalf("10-bit white = % x, want ff ff ff ff", src)
		}
	}
}

func TestConverter565(t *testing.T) {
	c, err := NewConverter(fmt16, rgb565, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.InPlace {
		t.Fatal("a 16-bit source reported that it can convert in place")
	}
	if c.SrcStride(3) != 8 || c.DstStride(3) != 12 {
		t.Errorf("strides = %d, %d; want 8 and 12", c.SrcStride(3), c.DstStride(3))
	}
	// 0xF800 is pure red, 0x07E0 pure green, 0x001F pure blue.
	src := []byte{0x00, 0xf8, 0xe0, 0x07, 0x1f, 0x00, 0, 0} // 6 bytes of pixels, padded to 8
	dst := make([]byte, 12)
	if err := c.Convert(dst, src, 3, 1, 12, 8); err != nil {
		t.Fatal(err)
	}
	want := []byte{0, 0, 255, 255, 0, 255, 0, 255, 255, 0, 0, 255}
	for i := range want {
		if dst[i] != want[i] {
			t.Fatalf("565 convert = % x, want % x", dst, want)
		}
	}
}

func TestConverter565BigEndian(t *testing.T) {
	c, err := NewConverter(fmt16, rgb565, ImageOrderMSB)
	if err != nil {
		t.Fatal(err)
	}
	src := []byte{0xf8, 0x00, 0, 0} // one pixel, scanline padded to four bytes
	dst := make([]byte, 4)
	if err := c.Convert(dst, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if dst[2] != 255 || dst[0] != 0 || dst[1] != 0 || dst[3] != 255 {
		t.Fatalf("MSBFirst 565 red = % x", dst)
	}
}

func TestConverter24bpp(t *testing.T) {
	for _, order := range []uint8{ImageOrderLSB, ImageOrderMSB} {
		c, err := NewConverter(fmt24, bgrx24, order)
		if err != nil {
			t.Fatal(err)
		}
		var src []byte
		if order == ImageOrderLSB {
			src = []byte{0x30, 0x20, 0x10} // B, G, R little-endian
		} else {
			src = []byte{0x10, 0x20, 0x30} // R, G, B big-endian
		}
		src = append(src, 0) // the scanline pad
		dst := make([]byte, 4)
		if err := c.Convert(dst, src, 1, 1, 4, 4); err != nil {
			t.Fatal(err)
		}
		want := []byte{0x30, 0x20, 0x10, 0xff}
		for i := range want {
			if dst[i] != want[i] {
				t.Fatalf("order %d: 24bpp convert = % x, want % x", order, dst, want)
			}
		}
	}
}

func TestConvertRejectsNarrowInPlace(t *testing.T) {
	c, err := NewConverter(fmt16, rgb565, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 64)
	if err := c.Convert(buf, buf, 2, 2, 8, 4); err == nil ||
		!strings.Contains(err.Error(), "in place") {
		t.Fatalf("Convert reported %v, want an in-place refusal", err)
	}
}

func TestConvertValidatesItsArguments(t *testing.T) {
	c, err := NewConverter(fmt32, bgrx24, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	big := make([]byte, 4096)
	for _, tc := range []struct {
		name                    string
		dst, src                []byte
		w, h, dstStride, srcStr int
		want                    string
	}{
		{"empty width", big, big, 0, 4, 16, 16, "empty"},
		{"empty height", big, big, 4, 0, 16, 16, "empty"},
		{"short source stride", big, big, 4, 4, 16, 8, "source stride"},
		{"short destination stride", big, big, 4, 4, 8, 16, "destination stride"},
		{"short source", big, make([]byte, 8), 4, 4, 16, 16, "source holds"},
		{"short destination", make([]byte, 8), big, 4, 4, 16, 16, "destination holds"},
	} {
		err := c.Convert(tc.dst, tc.src, tc.w, tc.h, tc.dstStride, tc.srcStr)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: Convert reported %v, want an error mentioning %q", tc.name, err, tc.want)
		}
	}
}

func TestSameSlice(t *testing.T) {
	a := make([]byte, 4)
	if !sameSlice(a, a) || !sameSlice(a, a[:2]) {
		t.Error("sameSlice missed two views of the same array")
	}
	if sameSlice(a, make([]byte, 4)) || sameSlice(nil, a) || sameSlice(a, nil) {
		t.Error("sameSlice matched distinct arrays")
	}
}

func TestFillAlphaHandlesOddWidths(t *testing.T) {
	// The 8-byte fast loop leaves a tail for an odd pixel count; the tail
	// must still be filled.
	for _, w := range []int{1, 2, 3, 5, 7} {
		buf := make([]byte, w*4)
		fillAlpha(buf, w, 1, w*4)
		for x := 0; x < w; x++ {
			if buf[x*4+3] != 0xff {
				t.Fatalf("width %d: pixel %d alpha = %d", w, x, buf[x*4+3])
			}
		}
	}
}

func TestConvertRespectsPaddedStrides(t *testing.T) {
	// The stride-is-not-width*4 case, made explicit: a 3-pixel row in a
	// format whose scanline pad is 64 bits occupies 16 bytes, not 12.
	f := Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 64}
	c, err := NewConverter(f, bgrx24, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.SrcStride(3); got != 16 {
		t.Fatalf("SrcStride(3) = %d, want 16", got)
	}
	src := make([]byte, 16*2)
	for i := range src {
		src[i] = 0x11
	}
	// Poison the padding so a converter that walked it would be caught.
	src[12], src[13], src[14], src[15] = 0xde, 0xad, 0xbe, 0xef
	if err := c.Convert(src, src, 3, 2, 16, 16); err != nil {
		t.Fatal(err)
	}
	if src[12] != 0xde || src[15] != 0xef {
		t.Errorf("the converter walked into the scanline padding: % x", src[12:16])
	}
	for x := 0; x < 3; x++ {
		if src[x*4+3] != 0xff {
			t.Errorf("pixel %d alpha = %d", x, src[x*4+3])
		}
	}
}

func TestConvertCarriesAlphaOnTheGeneralPaths(t *testing.T) {
	// A depth-32 visual whose channels are NOT byte-aligned: the general
	// mask-driven 32-bit path, with a real alpha channel to carry.
	deep := VisualType{ID: 1, Class: VisualTrueColor,
		RedMask: 0x3ff00000, GreenMask: 0x000ffc00, BlueMask: 0x000003ff}
	c, err := NewConverter(Format{Depth: 32, BitsPerPix: 32, ScanlinePad: 32}, deep, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.shuffle || !c.HasAlpha {
		t.Fatalf("Converter = %+v", c)
	}
	src := []byte{0x00, 0x00, 0x00, 0x80} // alpha 0x80 in the top byte
	if err := c.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if src[3] != 0x80 {
		t.Errorf("general path alpha = %#x, want 0x80", src[3])
	}

	// And the byte-aligned shuffle path with a real alpha channel: an RGBA
	// depth-32 visual, which is what a compositing X server's ARGB visual is.
	rgba := VisualType{ID: 2, Class: VisualTrueColor,
		RedMask: 0x000000ff, GreenMask: 0x0000ff00, BlueMask: 0x00ff0000}
	c2, err := NewConverter(Format{Depth: 32, BitsPerPix: 32, ScanlinePad: 32}, rgba, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	if !c2.shuffle || !c2.HasAlpha {
		t.Fatalf("Converter = %+v", c2)
	}
	src2 := []byte{0x10, 0x20, 0x30, 0x40}
	if err := c2.Convert(src2, src2, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	want := []byte{0x30, 0x20, 0x10, 0x40}
	for i := range want {
		if src2[i] != want[i] {
			t.Fatalf("shuffle with alpha = % x, want % x", src2, want)
		}
	}
}

func TestConvertUnalignedMasksBigEndian(t *testing.T) {
	// The general 32-bit path with an MSBFirst server: the pixel integer is
	// read big-endian before the masks are applied.
	deep := VisualType{ID: 1, Class: VisualTrueColor,
		RedMask: 0x3ff00000, GreenMask: 0x000ffc00, BlueMask: 0x000003ff}
	c, err := NewConverter(Format{Depth: 30, BitsPerPix: 32, ScanlinePad: 32}, deep, ImageOrderMSB)
	if err != nil {
		t.Fatal(err)
	}
	if c.shuffle {
		t.Fatal("a 10-bit visual took the shuffle path")
	}
	// Big-endian 0x3ff00000 is pure red at full intensity.
	src := []byte{0x3f, 0xf0, 0x00, 0x00}
	dst := make([]byte, 4)
	if err := c.Convert(dst, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if dst[2] != 0xff || dst[1] != 0 || dst[0] != 0 || dst[3] != 0xff {
		t.Fatalf("MSBFirst 10-bit red = % x, want 00 00 ff ff", dst)
	}
}

func TestWithRawAlpha(t *testing.T) {
	c, err := NewConverter(fmt32, bgrx24, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	raw := c.WithRawAlpha()
	if !raw.Identity || !raw.rawAlpha {
		t.Fatalf("WithRawAlpha = %+v", raw)
	}
	if c.rawAlpha {
		t.Fatal("WithRawAlpha mutated the converter it was called on")
	}
	src := []byte{1, 2, 3, 0x00}
	if err := raw.Convert(src, src, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if src[3] != 0x00 {
		t.Errorf("alpha = %#x, want the server's own byte", src[3])
	}
	// Every other path writes the fourth byte anyway, so the flag changes
	// nothing there: a 16-bit source still comes back opaque.
	c16, err := NewConverter(fmt16, rgb565, ImageOrderLSB)
	if err != nil {
		t.Fatal(err)
	}
	dst := make([]byte, 4)
	if err := c16.WithRawAlpha().Convert(dst, []byte{0xf8, 0x00, 0, 0}, 1, 1, 4, 4); err != nil {
		t.Fatal(err)
	}
	if dst[3] != 0xff {
		t.Errorf("a converted 16-bit pixel came back with alpha %#x", dst[3])
	}
}
