// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"testing"
)

func TestFormatStride(t *testing.T) {
	for _, tc := range []struct {
		name string
		f    Format
		w    int
		want int
		note string
	}{
		{name: "24bpp32pad", f: Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}, w: 1920, want: 7680},
		{name: "odd width 32bpp", f: Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}, w: 3, want: 12},
		// The case the doc warns about: a 24-bits-per-pixel format pads a
		// 3-pixel row of 9 bytes up to 12.
		{name: "24bpp packed", f: Format{Depth: 24, BitsPerPix: 24, ScanlinePad: 32}, w: 3, want: 12},
		{name: "24bpp packed wide", f: Format{Depth: 24, BitsPerPix: 24, ScanlinePad: 32}, w: 1920, want: 5760},
		{name: "16bpp odd", f: Format{Depth: 16, BitsPerPix: 16, ScanlinePad: 32}, w: 3, want: 8},
		{name: "1bpp", f: Format{Depth: 1, BitsPerPix: 1, ScanlinePad: 32}, w: 33, want: 8},
		{name: "zero width", f: Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}, w: 0, want: 0},
		{name: "negative width", f: Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}, w: -4, want: 0},
		{name: "zero bpp", f: Format{Depth: 24, BitsPerPix: 0, ScanlinePad: 32}, w: 8, want: 0},
		{name: "zero pad", f: Format{Depth: 24, BitsPerPix: 32, ScanlinePad: 0}, w: 8, want: 0},
	} {
		if got := tc.f.Stride(tc.w); got != tc.want {
			t.Errorf("%s: Stride(%d) = %d, want %d", tc.name, tc.w, got, tc.want)
		}
	}
}

func TestVisualDirect(t *testing.T) {
	for class, want := range map[uint8]bool{
		VisualStaticGray: false, VisualGrayScale: false, VisualStaticColor: false,
		VisualPseudoColor: false, VisualTrueColor: true, VisualDirectColor: true,
	} {
		if got := (VisualType{Class: class}).Direct(); got != want {
			t.Errorf("class %d Direct() = %v, want %v", class, got, want)
		}
	}
}

func TestSetupRoundTrip(t *testing.T) {
	for _, order := range []ByteOrder{binary.LittleEndian, binary.BigEndian} {
		spec := setupSpec{
			Release: 12101016, ResourceIDBase: 0x200000, ResourceIDMask: 0x1fffff,
			Vendor: "The X.Org Foundation", MaxRequestLen: 65535, ImageByteOrder: ImageOrderLSB,
			Formats: []Format{
				{Depth: 1, BitsPerPix: 1, ScanlinePad: 32},
				{Depth: 24, BitsPerPix: 32, ScanlinePad: 32},
				{Depth: 32, BitsPerPix: 32, ScanlinePad: 32},
			},
			Screens: []screenSpec{
				{Root: 0x21f, Colormap: 0x20, White: 0xffffff, Black: 0, Width: 1280, Height: 800,
					WidthMM: 338, HeightMM: 211, RootVisual: 0x21, RootDepth: 24,
					Depths: []depthSpec{
						{Depth: 24, Visuals: []VisualType{
							{ID: 0x21, Class: VisualTrueColor, BitsPerRGB: 8, ColormapEnt: 256,
								RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff},
							{ID: 0x22, Class: VisualDirectColor, BitsPerRGB: 8, ColormapEnt: 256,
								RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff},
						}},
						{Depth: 32, Visuals: []VisualType{
							{ID: 0x31, Class: VisualTrueColor, BitsPerRGB: 8, ColormapEnt: 256,
								RedMask: 0xff0000, GreenMask: 0xff00, BlueMask: 0xff},
						}},
					}},
				{Root: 0x400, Width: 640, Height: 480, RootVisual: 0x41, RootDepth: 24},
			},
		}
		body := buildSetupBody(order, spec)
		s, err := parseSetupReply(order, body)
		if err != nil {
			t.Fatalf("%v: parseSetupReply: %v", order, err)
		}
		if s.Release != spec.Release || s.ResourceIDBase != spec.ResourceIDBase ||
			s.ResourceIDMask != spec.ResourceIDMask || s.Vendor != spec.Vendor {
			t.Errorf("%v: header round-trip mismatch: %+v", order, s)
		}
		if len(s.Formats) != 3 || len(s.Screens) != 2 {
			t.Fatalf("%v: got %d formats and %d screens", order, len(s.Formats), len(s.Screens))
		}
		if s.Screens[0].Width != 1280 || s.Screens[0].WidthMM != 338 {
			t.Errorf("%v: screen 0 = %+v", order, s.Screens[0])
		}
		if len(s.Screens[0].Depths) != 2 || len(s.Screens[0].Depths[0].Visuals) != 2 {
			t.Errorf("%v: depth list round-trip lost entries: %+v", order, s.Screens[0].Depths)
		}
		if s.MinKeycode != 8 || s.MaxKeycode != 255 || s.BitmapUnit != 32 || s.BitmapPad != 32 {
			t.Errorf("%v: keyboard/bitmap fields wrong: %+v", order, s)
		}

		if f, ok := s.FormatFor(24); !ok || f.BitsPerPix != 32 {
			t.Errorf("%v: FormatFor(24) = %+v, %v", order, f, ok)
		}
		if _, ok := s.FormatFor(15); ok {
			t.Errorf("%v: FormatFor(15) found a format that is not listed", order)
		}
		if v, ok := s.Screens[0].FindVisual(0x22); !ok || v.Class != VisualDirectColor {
			t.Errorf("%v: FindVisual(0x22) = %+v, %v", order, v, ok)
		}
		if _, ok := s.Screens[0].FindVisual(0xdead); ok {
			t.Errorf("%v: FindVisual found a visual that is not there", order)
		}
		if d, ok := s.Screens[0].DepthOfVisual(0x31); !ok || d != 32 {
			t.Errorf("%v: DepthOfVisual(0x31) = %d, %v", order, d, ok)
		}
		if _, ok := s.Screens[0].DepthOfVisual(0xdead); ok {
			t.Errorf("%v: DepthOfVisual found a visual that is not there", order)
		}
		if v := s.Screens[0].RootVisualType(); v.ID != 0x21 {
			t.Errorf("%v: RootVisualType = %+v", order, v)
		}
		// A screen whose root visual is not in the depth list falls back to a
		// synthesized BGRX TrueColor visual rather than to nothing.
		if v := s.Screens[1].RootVisualType(); v.ID != 0x41 || v.RedMask != 0x00ff0000 {
			t.Errorf("%v: fallback RootVisualType = %+v", order, v)
		}
		if sc := s.ScreenOf(1); sc == nil || sc.Root != 0x400 {
			t.Errorf("%v: ScreenOf(1) = %+v", order, sc)
		}
		if s.ScreenOf(-1) != nil || s.ScreenOf(2) != nil {
			t.Errorf("%v: ScreenOf accepted an out-of-range index", order)
		}
		if i, ok := s.ScreenOfRoot(0x400); !ok || i != 1 {
			t.Errorf("%v: ScreenOfRoot(0x400) = %d, %v", order, i, ok)
		}
		if _, ok := s.ScreenOfRoot(0xdead); ok {
			t.Errorf("%v: ScreenOfRoot found a root that is not there", order)
		}
	}
}

func TestParseSetupReplyRejectsTruncated(t *testing.T) {
	order := binary.LittleEndian
	body := defaultSetupBody(order)
	for _, n := range []int{0, 8, 20, len(body) - 4} {
		if _, err := parseSetupReply(order, body[:n]); err == nil {
			t.Errorf("parseSetupReply accepted a %d-byte body", n)
		}
	}
}

func TestParseSetupReplyRejectsNoScreens(t *testing.T) {
	order := binary.LittleEndian
	body := buildSetupBody(order, setupSpec{Vendor: "x", Formats: []Format{{Depth: 24, BitsPerPix: 32, ScanlinePad: 32}}})
	if _, err := parseSetupReply(order, body); err == nil {
		t.Fatal("parseSetupReply accepted a reply with no screens")
	}
}

func TestBuildSetupRequest(t *testing.T) {
	order := binary.LittleEndian
	req := buildSetupRequest(order, orderLSB, AuthMITCookie, []byte("0123456789abcdef"))
	if req[0] != orderLSB {
		t.Errorf("sentinel = %q", req[0])
	}
	if order.Uint16(req[2:4]) != 11 || order.Uint16(req[4:6]) != 0 {
		t.Errorf("protocol version = %d.%d", order.Uint16(req[2:4]), order.Uint16(req[4:6]))
	}
	if int(order.Uint16(req[6:8])) != len(AuthMITCookie) {
		t.Errorf("auth name length = %d", order.Uint16(req[6:8]))
	}
	if int(order.Uint16(req[8:10])) != 16 {
		t.Errorf("auth data length = %d", order.Uint16(req[8:10]))
	}
	// 12 header + 18 name padded to 20 + 16 data.
	if len(req) != 12+20+16 {
		t.Errorf("request is %d bytes, want %d", len(req), 12+20+16)
	}
	if string(req[12:12+len(AuthMITCookie)]) != AuthMITCookie {
		t.Errorf("auth name = %q", req[12:12+len(AuthMITCookie)])
	}
}
