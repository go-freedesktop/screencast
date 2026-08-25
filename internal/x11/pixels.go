// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"fmt"
)

// This file turns whatever the X server sent into BGRA.
//
// A ZPixmap image is a run of pixels, each BitsPerPix wide, laid out in the
// server's IMAGE byte order (which is not necessarily the connection's byte
// order), with each scanline padded to the format's ScanlinePad. What a pixel
// MEANS comes from the visual's three channel masks. On the overwhelmingly
// common case — a little-endian server, a depth-24 TrueColor visual with
// 32 bits per pixel and masks 0xff0000/0xff00/0xff — the bytes are ALREADY
// B,G,R,pad, and the only thing left to do is force the pad byte opaque.
// Everything else goes through a general mask-driven path.

// Converter turns a captured ZPixmap image into BGRA. Build one with
// [NewConverter]; it is immutable and safe to use from several goroutines.
type Converter struct {
	// Format and Visual are what the converter was built for.
	Format Format
	Visual VisualType
	// ImageOrder is the server's image byte order, [ImageOrderLSB] or
	// [ImageOrderMSB].
	ImageOrder uint8

	// Identity is true when the captured bytes are already BGRA and the only
	// work left is the alpha fill.
	Identity bool
	// InPlace is true when a frame can be converted inside the capture buffer
	// itself, with no second buffer: it holds exactly when the source is 32
	// bits per pixel, so source and destination pixels are the same size.
	InPlace bool
	// HasAlpha is true when the visual's depth accounts for all 32 bits, so
	// the fourth byte is a real alpha channel rather than padding. On a
	// depth-24 visual it is false and the converter writes 0xff.
	HasAlpha bool

	bpp    int
	rShift uint
	gShift uint
	bShift uint
	rMask  uint32 // the channel value mask, precomputed from the table length
	gMask  uint32
	bMask  uint32
	rTab   []byte
	gTab   []byte
	bTab   []byte

	// shuffle is the fast path for a 32-bit visual whose three channels are
	// each a whole, byte-aligned byte — which every TrueColor visual on every
	// desktop is. The conversion is then a pure byte permutation with no
	// shifts and no table lookups: rByte, gByte and bByte are the memory
	// offsets within the pixel that the red, green and blue bytes sit at.
	shuffle             bool
	rByte, gByte, bByte int
	aByte               int

	// rawAlpha suppresses the opaque-alpha fill on the identity path. See
	// [Converter.WithRawAlpha].
	rawAlpha bool
}

// WithRawAlpha returns a copy of the converter that leaves the fourth byte of
// each pixel exactly as the server wrote it, instead of forcing it opaque.
//
// It only changes the IDENTITY path — the one where the captured bytes are
// already BGRA and the alpha fill is the only work left. On that path the fill
// is a whole pass over the frame — 2.8 ms for 3840x2160 on an Apple M4 Max,
// and 18 ms of a 24 ms frame on a Debian cloud VM with slower memory. On every
// other path the fourth byte has to be written anyway, so there is nothing to
// save and the flag has no effect.
//
// The bytes it then hands back are UNDEFINED on a depth-24 visual — in
// practice zero, which reads as fully transparent. Use it only when the
// consumer ignores alpha or forces it itself.
func (c *Converter) WithRawAlpha() *Converter {
	d := *c
	d.rawAlpha = true
	return &d
}

// NewConverter builds the converter for a given pixmap format, visual and
// server image byte order. It reports an error for a layout it cannot
// decompose — a palette visual, or a bits-per-pixel it does not implement —
// rather than silently producing a wrong picture.
func NewConverter(f Format, v VisualType, imageOrder uint8) (*Converter, error) {
	if !v.Direct() {
		return nil, fmt.Errorf("x11: visual %#x is class %d, not TrueColor or DirectColor: "+
			"its pixels are colormap indices and this package does not read colormaps",
			v.ID, v.Class)
	}
	switch f.BitsPerPix {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("x11: %d bits per pixel is not supported (16, 24 and 32 are)",
			f.BitsPerPix)
	}
	if f.ScanlinePad == 0 {
		return nil, fmt.Errorf("x11: pixmap format for depth %d states a zero scanline pad", f.Depth)
	}
	if v.RedMask == 0 || v.GreenMask == 0 || v.BlueMask == 0 {
		return nil, fmt.Errorf("x11: visual %#x has an empty channel mask "+
			"(r=%#x g=%#x b=%#x)", v.ID, v.RedMask, v.GreenMask, v.BlueMask)
	}
	c := &Converter{
		Format:     f,
		Visual:     v,
		ImageOrder: imageOrder,
		bpp:        int(f.BitsPerPix),
		HasAlpha:   f.Depth == 32,
	}
	var rBits, gBits, bBits uint
	c.rShift, rBits = maskShift(v.RedMask)
	c.gShift, gBits = maskShift(v.GreenMask)
	c.bShift, bBits = maskShift(v.BlueMask)
	c.rTab = scaleTable(rBits)
	c.gTab = scaleTable(gBits)
	c.bTab = scaleTable(bBits)
	c.rMask = uint32(len(c.rTab) - 1)
	c.gMask = uint32(len(c.gTab) - 1)
	c.bMask = uint32(len(c.bTab) - 1)

	if c.bpp == 32 && rBits == 8 && gBits == 8 && bBits == 8 &&
		c.rShift%8 == 0 && c.gShift%8 == 0 && c.bShift%8 == 0 {
		c.shuffle = true
		c.rByte = byteOfShift(c.rShift, imageOrder)
		c.gByte = byteOfShift(c.gShift, imageOrder)
		c.bByte = byteOfShift(c.bShift, imageOrder)
		c.aByte = 6 - c.rByte - c.gByte - c.bByte // the one index left over
	}

	c.InPlace = c.bpp == 32
	c.Identity = c.bpp == 32 && imageOrder == ImageOrderLSB &&
		v.RedMask == 0x00ff0000 && v.GreenMask == 0x0000ff00 && v.BlueMask == 0x000000ff
	return c, nil
}

// String describes the conversion for logs and for the CLI probe.
func (c *Converter) String() string {
	kind := "mask-driven"
	if c.Identity {
		kind = "identity (already BGRA)"
	}
	order := "LSBFirst"
	if c.ImageOrder == ImageOrderMSB {
		order = "MSBFirst"
	}
	return fmt.Sprintf("depth %d, %d bpp, pad %d, %s, masks r=%#x g=%#x b=%#x: %s",
		c.Format.Depth, c.bpp, c.Format.ScanlinePad, order,
		c.Visual.RedMask, c.Visual.GreenMask, c.Visual.BlueMask, kind)
}

// SrcStride is the number of bytes one captured scanline of w pixels occupies.
func (c *Converter) SrcStride(w int) int { return c.Format.Stride(w) }

// DstStride is the number of bytes one BGRA scanline of w pixels occupies. For
// a 32-bit source it is the source stride, so an in-place conversion keeps the
// server's own padding; otherwise it is the tight w*4.
func (c *Converter) DstStride(w int) int {
	if c.InPlace {
		return c.SrcStride(w)
	}
	return w * 4
}

// byteOfShift maps a byte-aligned bit shift within a 32-bit pixel to the
// MEMORY offset of that byte, which depends on the server's image byte order.
func byteOfShift(shift uint, imageOrder uint8) int {
	i := int(shift / 8)
	if imageOrder == ImageOrderMSB {
		return 3 - i
	}
	return i
}

// maskShift returns how far right a channel mask must be shifted to reach bit
// zero, and how many bits wide it is. A zero mask yields (0, 0).
func maskShift(mask uint32) (shift, bits uint) {
	if mask == 0 {
		return 0, 0
	}
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	for mask&1 == 1 {
		mask >>= 1
		bits++
	}
	return shift, bits
}

// scaleTable builds the lookup that widens an n-bit channel value to eight
// bits, so a 5-bit 31 becomes 255 rather than 248. A zero-width channel yields
// a one-entry table of zero.
func scaleTable(bits uint) []byte {
	if bits == 0 {
		return []byte{0}
	}
	if bits > 16 {
		bits = 16 // a channel wider than 16 bits does not exist on any visual
	}
	n := 1 << bits
	max := n - 1
	tab := make([]byte, n)
	for i := range tab {
		tab[i] = byte((i*255 + max/2) / max)
	}
	return tab
}

// Convert writes the w×h image in src — srcStride bytes per row, this
// converter's layout — into dst as BGRA at dstStride bytes per row.
//
// dst and src may be the SAME slice when [Converter.InPlace] is true; that is
// the point of the in-place flag, and it is what lets a 32-bit capture convert
// the shared segment the X server just wrote into without a second buffer.
//
// It allocates nothing.
func (c *Converter) Convert(dst, src []byte, w, h, dstStride, srcStride int) error {
	if w <= 0 || h <= 0 {
		return fmt.Errorf("x11: convert: empty %dx%d image", w, h)
	}
	if srcStride < c.SrcStride(w) {
		return fmt.Errorf("x11: convert: source stride %d is below the %d bytes a %d-pixel "+
			"row of this format needs", srcStride, c.SrcStride(w), w)
	}
	if dstStride < w*4 {
		return fmt.Errorf("x11: convert: destination stride %d is below the %d bytes a "+
			"%d-pixel BGRA row needs", dstStride, w*4, w)
	}
	if len(src) < srcStride*(h-1)+c.SrcStride(w) {
		return fmt.Errorf("x11: convert: source holds %d bytes, a %dx%d image needs %d",
			len(src), w, h, srcStride*(h-1)+c.SrcStride(w))
	}
	if len(dst) < dstStride*(h-1)+w*4 {
		return fmt.Errorf("x11: convert: destination holds %d bytes, a %dx%d BGRA image needs %d",
			len(dst), w, h, dstStride*(h-1)+w*4)
	}
	if c.Identity {
		if !sameSlice(dst, src) {
			for y := 0; y < h; y++ {
				copy(dst[y*dstStride:y*dstStride+w*4], src[y*srcStride:])
			}
		}
		if !c.HasAlpha && !c.rawAlpha {
			fillAlpha(dst, w, h, dstStride)
		}
		return nil
	}
	if c.bpp == 32 {
		c.convert32(dst, src, w, h, dstStride, srcStride)
		return nil
	}
	if sameSlice(dst, src) {
		return fmt.Errorf("x11: convert: a %d-bit source cannot be converted in place; "+
			"give Convert a separate destination", c.bpp)
	}
	c.convertNarrow(dst, src, w, h, dstStride, srcStride)
	return nil
}

// sameSlice reports whether two slices start at the same address, which is how
// an in-place conversion is recognised.
func sameSlice(a, b []byte) bool {
	return len(a) > 0 && len(b) > 0 && &a[0] == &b[0]
}

// fillAlpha forces every pixel's fourth byte opaque. A depth-24 visual leaves
// that byte undefined — in practice zero — and a consumer that treats the
// frame as BGRA would render a fully transparent desktop.
func fillAlpha(dst []byte, w, h, stride int) {
	// Eight bytes at a time: one read, one OR, one write per two pixels beats
	// one store per pixel by enough to matter at 4K, where this is the only
	// work the identity path does at all.
	const twoAlphas = uint64(0xff000000ff000000)
	for y := 0; y < h; y++ {
		row := dst[y*stride : y*stride+w*4]
		i := 0
		for ; i+8 <= len(row); i += 8 {
			v := binary.LittleEndian.Uint64(row[i:])
			binary.LittleEndian.PutUint64(row[i:], v|twoAlphas)
		}
		for i += 3; i < len(row); i += 4 {
			row[i] = 0xff
		}
	}
}

// convert32 rewrites a 32-bit-per-pixel image through the channel masks. It
// tolerates dst == src.
func (c *Converter) convert32(dst, src []byte, w, h, dstStride, srcStride int) {
	if c.shuffle {
		c.shuffle32(dst, src, w, h, dstStride, srcStride)
		return
	}
	get := binary.LittleEndian.Uint32
	if c.ImageOrder == ImageOrderMSB {
		get = binary.BigEndian.Uint32
	}
	rTab, gTab, bTab := c.rTab, c.gTab, c.bTab
	rSh, gSh, bSh := c.rShift, c.gShift, c.bShift
	rM, gM, bM := c.rMask, c.gMask, c.bMask
	hasAlpha := c.HasAlpha
	for y := 0; y < h; y++ {
		s := src[y*srcStride : y*srcStride+w*4]
		d := dst[y*dstStride : y*dstStride+w*4]
		for x := 0; x+4 <= len(s); x += 4 {
			p := get(s[x : x+4])
			a := byte(0xff)
			if hasAlpha {
				a = byte(p >> 24)
			}
			q := d[x : x+4 : x+4]
			q[0] = bTab[(p>>bSh)&bM]
			q[1] = gTab[(p>>gSh)&gM]
			q[2] = rTab[(p>>rSh)&rM]
			q[3] = a
		}
	}
}

// shuffle32 is convert32's fast path: three byte-aligned 8-bit channels, so
// the conversion is a permutation of the four bytes of each pixel and nothing
// else. It tolerates dst == src because it reads all four bytes of a pixel
// before writing any of them.
func (c *Converter) shuffle32(dst, src []byte, w, h, dstStride, srcStride int) {
	rb, gb, bb, ab := c.rByte, c.gByte, c.bByte, c.aByte
	hasAlpha := c.HasAlpha
	for y := 0; y < h; y++ {
		s := src[y*srcStride : y*srcStride+w*4]
		d := dst[y*dstStride : y*dstStride+w*4]
		for x := 0; x+4 <= len(s); x += 4 {
			p := s[x : x+4 : x+4]
			b0, b1, b2, b3 := p[0], p[1], p[2], p[3]
			all := [4]byte{b0, b1, b2, b3}
			a := byte(0xff)
			if hasAlpha {
				a = all[ab]
			}
			q := d[x : x+4 : x+4]
			q[0] = all[bb]
			q[1] = all[gb]
			q[2] = all[rb]
			q[3] = a
		}
	}
}

// convertNarrow rewrites a 16- or 24-bit-per-pixel image into BGRA. It needs a
// destination distinct from the source, because a BGRA pixel is wider than the
// source pixel it comes from.
func (c *Converter) convertNarrow(dst, src []byte, w, h, dstStride, srcStride int) {
	bpx := c.bpp / 8
	msb := c.ImageOrder == ImageOrderMSB
	rTab, gTab, bTab := c.rTab, c.gTab, c.bTab
	rSh, gSh, bSh := c.rShift, c.gShift, c.bShift
	rM, gM, bM := c.rMask, c.gMask, c.bMask
	for y := 0; y < h; y++ {
		s := src[y*srcStride : y*srcStride+w*bpx]
		d := dst[y*dstStride : y*dstStride+w*4]
		di := 0
		for si := 0; si+bpx <= len(s); si += bpx {
			var p uint32
			if bpx == 2 {
				if msb {
					p = uint32(s[si])<<8 | uint32(s[si+1])
				} else {
					p = uint32(s[si+1])<<8 | uint32(s[si])
				}
			} else {
				if msb {
					p = uint32(s[si])<<16 | uint32(s[si+1])<<8 | uint32(s[si+2])
				} else {
					p = uint32(s[si+2])<<16 | uint32(s[si+1])<<8 | uint32(s[si])
				}
			}
			q := d[di : di+4 : di+4]
			q[0] = bTab[(p>>bSh)&bM]
			q[1] = gTab[(p>>gSh)&gM]
			q[2] = rTab[(p>>rSh)&rM]
			q[3] = 0xff
			di += 4
		}
	}
}
