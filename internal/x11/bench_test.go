// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import "testing"

// benchConvert measures one full-frame conversion, which is the only per-frame
// CPU cost this package can incur on a server whose pixels are not already
// BGRA. The consumer's whole frame budget is 16.6 ms, so this number matters.
func benchConvert(b *testing.B, f Format, v VisualType, order uint8, w, h int) {
	b.Helper()
	c, err := NewConverter(f, v, order)
	if err != nil {
		b.Fatal(err)
	}
	srcStride := c.SrcStride(w)
	dstStride := c.DstStride(w)
	src := make([]byte, srcStride*h)
	for i := range src {
		src[i] = byte(i)
	}
	dst := src
	if !c.InPlace {
		dst = make([]byte, dstStride*h)
	}
	b.SetBytes(int64(w * h * 4))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Convert(dst, src, w, h, dstStride, srcStride); err != nil {
			b.Fatal(err)
		}
	}
}

var (
	bgrx24 = VisualType{ID: 1, Class: VisualTrueColor, RedMask: 0x00ff0000, GreenMask: 0x0000ff00, BlueMask: 0x000000ff}
	rgbx24 = VisualType{ID: 2, Class: VisualTrueColor, RedMask: 0x000000ff, GreenMask: 0x0000ff00, BlueMask: 0x00ff0000}
	rgb565 = VisualType{ID: 3, Class: VisualTrueColor, RedMask: 0xf800, GreenMask: 0x07e0, BlueMask: 0x001f}
	fmt32  = Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}
	fmt16  = Format{Depth: 16, BitsPerPix: 16, ScanlinePad: 32}
	fmt24  = Format{Depth: 24, BitsPerPix: 24, ScanlinePad: 32}
)

func BenchmarkConvertIdentity1080p(b *testing.B) {
	benchConvert(b, fmt32, bgrx24, ImageOrderLSB, 1920, 1080)
}
func BenchmarkConvertIdentity4K(b *testing.B) {
	benchConvert(b, fmt32, bgrx24, ImageOrderLSB, 3840, 2160)
}
func BenchmarkConvertSwap1080p(b *testing.B) {
	benchConvert(b, fmt32, rgbx24, ImageOrderLSB, 1920, 1080)
}
func BenchmarkConvert565At1280x800(b *testing.B) {
	benchConvert(b, fmt16, rgb565, ImageOrderLSB, 1280, 800)
}
func BenchmarkConvert24bpp1080p(b *testing.B) {
	benchConvert(b, fmt24, bgrx24, ImageOrderLSB, 1920, 1080)
}

// BenchmarkConvertIdentityRawAlpha4K is the identity path with the opaque
// alpha fill turned off: it is what a consumer that ignores alpha pays, which
// is nothing.
func BenchmarkConvertIdentityRawAlpha4K(b *testing.B) {
	c, err := NewConverter(fmt32, bgrx24, ImageOrderLSB)
	if err != nil {
		b.Fatal(err)
	}
	c = c.WithRawAlpha()
	const w, h = 3840, 2160
	stride := c.SrcStride(w)
	buf := make([]byte, stride*h)
	b.SetBytes(int64(w * h * 4))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.Convert(buf, buf, w, h, stride, stride); err != nil {
			b.Fatal(err)
		}
	}
}
