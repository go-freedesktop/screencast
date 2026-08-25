// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import "fmt"

// This file enumerates the MONITORS of an X screen.
//
// An X screen is one coordinate space; the physical monitors are laid out
// inside it. A capture that wants "the left-hand display" needs the rectangle,
// and there are two ways to ask:
//
//   - RANDR 1.5 RRGetMonitors, which is the modern answer and the only one
//     that carries a NAME ("HDMI-1") and a primary flag.
//   - XINERAMA QueryScreens, which is older, nameless, and still what a few
//     servers (and Xvfb with +xinerama) answer.
//
// A server offering neither has exactly one monitor: the screen itself.

// Extension names.
const (
	RandrName    = "RANDR"
	XineramaName = "XINERAMA"
)

// RANDR minor opcodes.
const (
	rrReqQueryVersion = 0
	rrReqGetMonitors  = 42
)

// XINERAMA minor opcodes.
const (
	xinReqQueryVersion = 0
	xinReqQueryScreens = 5
)

// Monitor is one physical output's rectangle inside an X screen.
type Monitor struct {
	Name          string // RANDR output name; "" from XINERAMA, which carries none
	NameAtom      uint32
	Primary       bool
	Automatic     bool
	X, Y          int16
	Width, Height uint16
	WidthMM       uint32
	HeightMM      uint32
	Outputs       []uint32
}

// String renders the monitor for logs.
func (m Monitor) String() string {
	name := m.Name
	if name == "" {
		name = "monitor"
	}
	return fmt.Sprintf("%s %dx%d+%d+%d", name, m.Width, m.Height, m.X, m.Y)
}

// Randr is a queried RANDR handle.
type Randr struct {
	c        *Conn
	major    byte
	VerMajor uint32
	VerMinor uint32
}

// QueryRandr queries RANDR and negotiates version 1.5, which is the one that
// carries RRGetMonitors. It returns (nil, nil) when the server has no RANDR.
func (c *Conn) QueryRandr() (*Randr, error) {
	present, major, _, _, err := c.QueryExtension(RandrName)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	e := newEncoder(c.order)
	e.put32(1)
	e.put32(5)
	hdr, _, err := c.roundTrip("RRQueryVersion", major, rrReqQueryVersion, e.buf)
	if err != nil {
		return nil, err
	}
	return &Randr{
		c:        c,
		major:    major,
		VerMajor: c.order.Uint32(hdr[8:12]),
		VerMinor: c.order.Uint32(hdr[12:16]),
	}, nil
}

// HasMonitors reports whether the negotiated version carries RRGetMonitors,
// which arrived in RANDR 1.5.
func (r *Randr) HasMonitors() bool {
	return r.VerMajor > 1 || (r.VerMajor == 1 && r.VerMinor >= 5)
}

// decodeMonitors parses the MONITORINFO list of an RRGetMonitors reply. Each
// entry is a fixed 24-byte head followed by ncrtcs 4-byte output ids.
func decodeMonitors(order ByteOrder, count int, body []byte) ([]Monitor, error) {
	d := newDecoder(order, body)
	out := make([]Monitor, 0, count)
	for i := 0; i < count; i++ {
		var m Monitor
		m.NameAtom = d.get32()
		m.Primary = d.get8() != 0
		m.Automatic = d.get8() != 0
		n := int(d.get16())
		m.X = d.get16s()
		m.Y = d.get16s()
		m.Width = d.get16()
		m.Height = d.get16()
		m.WidthMM = d.get32()
		m.HeightMM = d.get32()
		if n > 0 {
			m.Outputs = make([]uint32, 0, n)
			for j := 0; j < n; j++ {
				m.Outputs = append(m.Outputs, d.get32())
			}
		}
		if !d.ok {
			return nil, fmt.Errorf("x11: RRGetMonitors: truncated monitor %d of %d", i, count)
		}
		out = append(out, m)
	}
	return out, nil
}

// GetMonitors lists the monitors of the screen rooted at root. Names are
// resolved from their atoms, so a monitor comes back as "HDMI-1" rather than
// as a number.
func (r *Randr) GetMonitors(root uint32) ([]Monitor, error) {
	e := newEncoder(r.c.order)
	e.put32(root)
	e.put8(1) // get-active: only monitors currently driving pixels
	e.skip(3)
	hdr, extra, err := r.c.roundTrip("RRGetMonitors", r.major, rrReqGetMonitors, e.buf)
	if err != nil {
		return nil, err
	}
	n := int(r.c.order.Uint32(hdr[12:16]))
	mons, err := decodeMonitors(r.c.order, n, extra)
	if err != nil {
		return nil, err
	}
	for i := range mons {
		if mons[i].NameAtom == AtomNone {
			continue
		}
		name, err := r.c.GetAtomName(mons[i].NameAtom)
		if err != nil {
			// A monitor whose name atom vanished between the two requests is
			// still a perfectly capturable rectangle; it just stays nameless.
			continue
		}
		mons[i].Name = name
	}
	return mons, nil
}

// Xinerama is a queried XINERAMA handle.
type Xinerama struct {
	c        *Conn
	major    byte
	VerMajor uint16
	VerMinor uint16
}

// QueryXinerama queries XINERAMA. It returns (nil, nil) when the server has
// none.
func (c *Conn) QueryXinerama() (*Xinerama, error) {
	present, major, _, _, err := c.QueryExtension(XineramaName)
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	e := newEncoder(c.order)
	e.put8(1)
	e.put8(1)
	e.skip(2)
	hdr, _, err := c.roundTrip("XineramaQueryVersion", major, xinReqQueryVersion, e.buf)
	if err != nil {
		return nil, err
	}
	return &Xinerama{
		c:        c,
		major:    major,
		VerMajor: c.order.Uint16(hdr[8:10]),
		VerMinor: c.order.Uint16(hdr[10:12]),
	}, nil
}

// decodeXineramaScreens parses the ScreenInfo list of a QueryScreens reply:
// count entries of four INT16/CARD16 fields each.
func decodeXineramaScreens(order ByteOrder, count int, body []byte) ([]Monitor, error) {
	d := newDecoder(order, body)
	out := make([]Monitor, 0, count)
	for i := 0; i < count; i++ {
		m := Monitor{
			X:      d.get16s(),
			Y:      d.get16s(),
			Width:  d.get16(),
			Height: d.get16(),
		}
		if !d.ok {
			return nil, fmt.Errorf("x11: XineramaQueryScreens: truncated screen %d of %d", i, count)
		}
		out = append(out, m)
	}
	// XINERAMA carries no primary flag; the first screen is the one the
	// window manager treats as primary in practice.
	if len(out) > 0 {
		out[0].Primary = true
	}
	return out, nil
}

// QueryScreens lists the Xinerama screens, which are the same rectangles
// RANDR would call monitors, without names.
func (x *Xinerama) QueryScreens() ([]Monitor, error) {
	hdr, extra, err := x.c.roundTrip("XineramaQueryScreens", x.major, xinReqQueryScreens, nil)
	if err != nil {
		return nil, err
	}
	n := int(x.c.order.Uint32(hdr[8:12]))
	return decodeXineramaScreens(x.c.order, n, extra)
}

// Monitors lists the monitors of the screen rooted at root, trying RANDR 1.5
// first, then XINERAMA, and falling back to the whole screen as a single
// nameless monitor. It never returns an empty list without an error: a screen
// always has at least itself.
func (c *Conn) Monitors(screen int) ([]Monitor, error) {
	sc := c.setup.ScreenOf(screen)
	if sc == nil {
		return nil, fmt.Errorf("x11: no screen %d (server has %d)", screen, len(c.setup.Screens))
	}
	whole := []Monitor{{
		Primary: true,
		Width:   sc.Width,
		Height:  sc.Height,
		WidthMM: uint32(sc.WidthMM), HeightMM: uint32(sc.HeightMM),
	}}
	if rr, err := c.QueryRandr(); err == nil && rr != nil && rr.HasMonitors() {
		if mons, err := rr.GetMonitors(sc.Root); err == nil && len(mons) > 0 {
			return mons, nil
		}
	}
	if xin, err := c.QueryXinerama(); err == nil && xin != nil {
		if mons, err := xin.QueryScreens(); err == nil && len(mons) > 0 {
			return mons, nil
		}
	}
	return whole, nil
}
