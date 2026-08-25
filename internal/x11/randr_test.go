// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"strings"
	"testing"

	xproto "github.com/go-freedesktop/x11"
)

// monitorInfo encodes one MONITORINFO, which is what RRGetMonitors returns a
// list of.
func monitorInfo(order ByteOrder, nameAtom uint32, primary bool, x, y int16,
	w, h uint16, outputs ...uint32) []byte {
	e := xproto.NewEncoder(order)
	e.Put32(nameAtom)
	if primary {
		e.Put8(1)
	} else {
		e.Put8(0)
	}
	e.Put8(1) // automatic
	e.Put16(uint16(len(outputs)))
	e.Put16(uint16(x))
	e.Put16(uint16(y))
	e.Put16(w)
	e.Put16(h)
	e.Put32(510)
	e.Put32(290)
	for _, o := range outputs {
		e.Put32(o)
	}
	return e.Bytes()
}

func TestDecodeMonitors(t *testing.T) {
	order := binary.LittleEndian
	body := append(monitorInfo(order, 100, true, 0, 0, 1920, 1080, 66),
		monitorInfo(order, 101, false, 1920, 0, 2560, 1440, 67, 68)...)
	mons, err := decodeMonitors(order, 2, body)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 2 {
		t.Fatalf("decoded %d monitors", len(mons))
	}
	if !mons[0].Primary || mons[0].Width != 1920 || mons[0].Height != 1080 ||
		len(mons[0].Outputs) != 1 || mons[0].Outputs[0] != 66 {
		t.Errorf("monitor 0 = %+v", mons[0])
	}
	if mons[1].X != 1920 || mons[1].Primary || len(mons[1].Outputs) != 2 ||
		mons[1].WidthMM != 510 {
		t.Errorf("monitor 1 = %+v", mons[1])
	}
	if got := mons[1].String(); got != "monitor 2560x1440+1920+0" {
		t.Errorf("String() = %q", got)
	}
	mons[1].Name = "HDMI-1"
	if got := mons[1].String(); !strings.HasPrefix(got, "HDMI-1 ") {
		t.Errorf("named String() = %q", got)
	}
}

func TestDecodeMonitorsTruncated(t *testing.T) {
	order := binary.LittleEndian
	body := monitorInfo(order, 100, true, 0, 0, 1920, 1080, 66)
	for _, n := range []int{0, 8, len(body) - 4} {
		if _, err := decodeMonitors(order, 1, body[:n]); err == nil {
			t.Errorf("decodeMonitors accepted a %d-byte body", n)
		}
	}
	// A count that overruns the body is also a truncation.
	if _, err := decodeMonitors(order, 3, body); err == nil {
		t.Error("decodeMonitors accepted a count larger than the body")
	}
}

// randrServer scripts a RANDR-capable server.
func randrServer(t *testing.T, order ByteOrder, verMajor, verMinor uint32,
	monitors []byte, count int, names map[uint32]string) func(op, data byte, body []byte) []byte {
	t.Helper()
	return func(op, data byte, body []byte) []byte {
		switch {
		case op == opQueryExtension:
			name := string(body[4 : 4+int(order.Uint16(body[0:2]))])
			if name == RandrName {
				return reply(order, 0, []byte{1, 140, 89, 147}, nil)
			}
			return reply(order, 0, []byte{0, 0, 0, 0}, nil)
		case op == 140 && data == rrReqQueryVersion:
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], verMajor)
			order.PutUint32(fixed[4:8], verMinor)
			return reply(order, 0, fixed, nil)
		case op == 140 && data == rrReqGetMonitors:
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], 12345) // timestamp
			order.PutUint32(fixed[4:8], uint32(count))
			order.PutUint32(fixed[8:12], 1)
			return reply(order, 0, fixed, monitors)
		case op == opGetAtomName:
			atom := order.Uint32(body[0:4])
			n, ok := names[atom]
			if !ok {
				return errorPacket(order, ErrCodeAtom, atom, opGetAtomName, 0)
			}
			fixed := make([]byte, 24)
			order.PutUint16(fixed[0:2], uint16(len(n)))
			return reply(order, 0, fixed, append([]byte(n), make([]byte, xproto.Padding(len(n)))...))
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	}
}

func TestQueryRandrAndGetMonitors(t *testing.T) {
	order := binary.LittleEndian
	body := append(monitorInfo(order, 100, true, 0, 0, 1920, 1080),
		monitorInfo(order, 101, false, 1920, 0, 1280, 1024)...)
	c, _ := dialFake(t, randrServer(t, order, 1, 5, body, 2,
		map[uint32]string{100: "eDP-1", 101: "HDMI-1"}))
	rr, err := c.QueryRandr()
	if err != nil || rr == nil {
		t.Fatalf("QueryRandr = %+v, %v", rr, err)
	}
	if rr.VerMajor != 1 || rr.VerMinor != 5 || !rr.HasMonitors() {
		t.Fatalf("Randr = %+v", rr)
	}
	mons, err := rr.GetMonitors(0x100)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 2 || mons[0].Name != "eDP-1" || mons[1].Name != "HDMI-1" {
		t.Fatalf("GetMonitors = %+v", mons)
	}
}

func TestRandrHasMonitors(t *testing.T) {
	for _, tc := range []struct {
		maj, min uint32
		want     bool
	}{{1, 5, true}, {1, 6, true}, {2, 0, true}, {1, 4, false}, {0, 9, false}} {
		r := &Randr{VerMajor: tc.maj, VerMinor: tc.min}
		if got := r.HasMonitors(); got != tc.want {
			t.Errorf("RANDR %d.%d HasMonitors = %v", tc.maj, tc.min, got)
		}
	}
}

func TestGetMonitorsUnnamedAndUnresolvable(t *testing.T) {
	order := binary.LittleEndian
	// One monitor with no name atom at all, one whose atom the server refuses
	// to resolve. Neither is an error: a nameless rectangle is still
	// capturable.
	body := append(monitorInfo(order, AtomNone, true, 0, 0, 800, 600),
		monitorInfo(order, 999, false, 800, 0, 800, 600)...)
	c, _ := dialFake(t, randrServer(t, order, 1, 5, body, 2, nil))
	rr, err := c.QueryRandr()
	if err != nil {
		t.Fatal(err)
	}
	mons, err := rr.GetMonitors(0x100)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 2 || mons[0].Name != "" || mons[1].Name != "" {
		t.Fatalf("GetMonitors = %+v", mons)
	}
}

func TestQueryRandrAbsentAndFailing(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{0, 0, 0, 0}, nil)
	})
	rr, err := c.QueryRandr()
	if err != nil || rr != nil {
		t.Fatalf("QueryRandr on a server without RANDR = %+v, %v", rr, err)
	}
	c2, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c2.QueryRandr(); err == nil {
		t.Error("QueryRandr accepted a failed QueryExtension")
	}
	c3, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 140, 0, 0}, nil)
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c3.QueryRandr(); err == nil {
		t.Error("QueryRandr accepted a failed RRQueryVersion")
	}
}

func TestGetMonitorsErrors(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 140, 0, 0}, nil)
		}
		if op == 140 && data == rrReqQueryVersion {
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], 1)
			order.PutUint32(fixed[4:8], 5)
			return reply(order, 0, fixed, nil)
		}
		return errorPacket(order, ErrCodeValue, 0, op, 0)
	})
	rr, err := c.QueryRandr()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rr.GetMonitors(1); err == nil {
		t.Error("GetMonitors accepted an error reply")
	}

	// A count that does not match the body is a decode failure.
	c2, _ := dialFake(t, randrServer(t, order, 1, 5, nil, 4, nil))
	rr2, err := c2.QueryRandr()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := rr2.GetMonitors(1); err == nil {
		t.Error("GetMonitors accepted a monitor count with no bodies behind it")
	}
}

func TestDecodeXineramaScreens(t *testing.T) {
	order := binary.LittleEndian
	e := xproto.NewEncoder(order)
	e.Put16(0)
	e.Put16(0)
	e.Put16(1024)
	e.Put16(768)
	e.Put16(1024)
	e.Put16(0)
	e.Put16(1280)
	e.Put16(1024)
	mons, err := decodeXineramaScreens(order, 2, e.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 2 || !mons[0].Primary || mons[0].Width != 1024 ||
		mons[1].X != 1024 || mons[1].Height != 1024 || mons[1].Primary {
		t.Fatalf("decodeXineramaScreens = %+v", mons)
	}
	if _, err := decodeXineramaScreens(order, 3, e.Bytes()); err == nil {
		t.Error("decodeXineramaScreens accepted a count larger than the body")
	}
	if got, err := decodeXineramaScreens(order, 0, nil); err != nil || len(got) != 0 {
		t.Errorf("empty screen list = %+v, %v", got, err)
	}
}

// xineramaServer scripts a XINERAMA-only server.
func xineramaServer(t *testing.T, order ByteOrder, screens []byte, count int,
	fail bool) func(op, data byte, body []byte) []byte {
	t.Helper()
	return func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			name := string(body[4 : 4+int(order.Uint16(body[0:2]))])
			if name == XineramaName {
				return reply(order, 0, []byte{1, 150, 0, 0}, nil)
			}
			return reply(order, 0, []byte{0, 0, 0, 0}, nil)
		}
		if op == 150 && data == xinReqQueryVersion {
			fixed := make([]byte, 24)
			order.PutUint16(fixed[0:2], 1)
			order.PutUint16(fixed[2:4], 1)
			return reply(order, 0, fixed, nil)
		}
		if op == 150 && data == xinReqQueryScreens {
			if fail {
				return errorPacket(order, ErrCodeValue, 0, op, 0)
			}
			fixed := make([]byte, 24)
			order.PutUint32(fixed[0:4], uint32(count))
			return reply(order, 0, fixed, screens)
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	}
}

func TestQueryXinerama(t *testing.T) {
	order := binary.LittleEndian
	e := xproto.NewEncoder(order)
	e.Put16(0)
	e.Put16(0)
	e.Put16(1024)
	e.Put16(768)
	c, _ := dialFake(t, xineramaServer(t, order, e.Bytes(), 1, false))
	x, err := c.QueryXinerama()
	if err != nil || x == nil {
		t.Fatalf("QueryXinerama = %+v, %v", x, err)
	}
	if x.VerMajor != 1 || x.VerMinor != 1 {
		t.Errorf("Xinerama = %+v", x)
	}
	mons, err := x.QueryScreens()
	if err != nil || len(mons) != 1 || mons[0].Width != 1024 {
		t.Fatalf("QueryScreens = %+v, %v", mons, err)
	}
}

func TestQueryXineramaAbsentAndFailing(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{0, 0, 0, 0}, nil)
	})
	if x, err := c.QueryXinerama(); err != nil || x != nil {
		t.Fatalf("QueryXinerama = %+v, %v", x, err)
	}
	c2, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c2.QueryXinerama(); err == nil {
		t.Error("QueryXinerama accepted a failed QueryExtension")
	}
	c3, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{1, 150, 0, 0}, nil)
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	if _, err := c3.QueryXinerama(); err == nil {
		t.Error("QueryXinerama accepted a failed XineramaQueryVersion")
	}
	c4, _ := dialFake(t, xineramaServer(t, order, nil, 0, true))
	x4, err := c4.QueryXinerama()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x4.QueryScreens(); err == nil {
		t.Error("QueryScreens accepted an error reply")
	}
}

func TestMonitorsPrefersRandr(t *testing.T) {
	order := binary.LittleEndian
	body := monitorInfo(order, 100, true, 0, 0, 1920, 1080)
	c, _ := dialFake(t, randrServer(t, order, 1, 5, body, 1, map[uint32]string{100: "eDP-1"}))
	mons, err := c.Monitors(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].Name != "eDP-1" || mons[0].Width != 1920 {
		t.Fatalf("Monitors = %+v", mons)
	}
}

func TestMonitorsFallsBackToXinerama(t *testing.T) {
	order := binary.LittleEndian
	e := xproto.NewEncoder(order)
	e.Put16(0)
	e.Put16(0)
	e.Put16(1024)
	e.Put16(768)
	c, _ := dialFake(t, xineramaServer(t, order, e.Bytes(), 1, false))
	mons, err := c.Monitors(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].Width != 1024 || mons[0].Name != "" {
		t.Fatalf("Monitors = %+v", mons)
	}
}

func TestMonitorsFallsBackToTheWholeScreen(t *testing.T) {
	order := binary.LittleEndian
	// A server with neither extension: the screen itself is the one monitor.
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		if op == opQueryExtension {
			return reply(order, 0, []byte{0, 0, 0, 0}, nil)
		}
		return errorPacket(order, ErrCodeRequest, 0, op, 0)
	})
	mons, err := c.Monitors(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].Width != 1920 || mons[0].Height != 1080 || !mons[0].Primary {
		t.Fatalf("Monitors = %+v", mons)
	}
}

func TestMonitorsRandr14FallsThrough(t *testing.T) {
	order := binary.LittleEndian
	// RANDR 1.4 has no RRGetMonitors, so the whole screen is the answer.
	c, _ := dialFake(t, randrServer(t, order, 1, 4, nil, 0, nil))
	mons, err := c.Monitors(0)
	if err != nil {
		t.Fatal(err)
	}
	if len(mons) != 1 || mons[0].Width != 1920 {
		t.Fatalf("Monitors = %+v", mons)
	}
}

func TestMonitorsUnknownScreen(t *testing.T) {
	c, _ := dialFake(t, nil)
	if _, err := c.Monitors(3); err == nil || !strings.Contains(err.Error(), "no screen 3") {
		t.Fatalf("Monitors(3) reported %v, want a no-such-screen error", err)
	}
}
