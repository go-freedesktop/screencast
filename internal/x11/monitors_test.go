// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"errors"
	"strings"
	"testing"

	xproto "github.com/go-freedesktop/x11"
)

// The RANDR and XINERAMA wire formats are tested where they now live, in
// github.com/go-freedesktop/x11, against a scripted server of their own. What
// is left to prove here is the ADAPTER: that this package's request/reply
// machine satisfies what the shared enumeration asks of it, over a real
// socket, with the whole sequence of requests actually crossing the pipe.

// randrHandler scripts a server with RANDR 1.5 and one 1920x1080 monitor
// named HDMI-1 whose output publishes an EDID.
func randrHandler(order binary.ByteOrder) func(op, data byte, body []byte) []byte {
	const randrMajor = 140
	mon := xproto.NewEncoder(order)
	mon.Put32(0x40) // name atom
	mon.Put8(1)     // primary
	mon.Put8(1)     // automatic
	mon.Put16(1)    // one output
	mon.Put16(0)    // x
	mon.Put16(0)    // y
	mon.Put16(1920)
	mon.Put16(1080)
	mon.Put32(509) // width mm
	mon.Put32(286) // height mm
	mon.Put32(0x42)

	edid := make([]byte, 128)
	copy(edid, []byte{0x00, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00})
	d := edid[54:72]
	d[3] = 0xfc
	copy(d[5:], "DELL U2720Q\n ")

	return func(op, data byte, body []byte) []byte {
		switch op {
		case opQueryExtension:
			// The name follows the 4-byte length header.
			if strings.HasPrefix(string(body[4:]), "RANDR") {
				return reply(order, 0, []byte{1, randrMajor, 64, 128}, nil)
			}
			return reply(order, 0, []byte{0, 0, 0, 0}, nil)
		case opGetAtomName:
			name := xproto.NewEncoder(order)
			name.PutString("HDMI-1")
			fixed := make([]byte, 2)
			order.PutUint16(fixed, 6)
			return reply(order, 0, fixed, name.Bytes())
		case opInternAtom:
			fixed := make([]byte, 4)
			order.PutUint32(fixed, 0x51)
			return reply(order, 0, fixed, nil)
		case randrMajor:
			switch data {
			case 0: // RRQueryVersion
				fixed := make([]byte, 8)
				order.PutUint32(fixed[0:4], 1)
				order.PutUint32(fixed[4:8], 5)
				return reply(order, 0, fixed, nil)
			case 42: // RRGetMonitors
				fixed := make([]byte, 12)
				order.PutUint32(fixed[0:4], 12345) // timestamp
				order.PutUint32(fixed[4:8], 1)     // one monitor
				order.PutUint32(fixed[8:12], 1)    // one output
				return reply(order, 0, fixed, mon.Bytes())
			case 15: // RRGetOutputProperty
				fixed := make([]byte, 12)
				order.PutUint32(fixed[0:4], 19) // INTEGER
				order.PutUint32(fixed[4:8], 0)  // bytes-after
				order.PutUint32(fixed[8:12], uint32(len(edid)))
				return reply(order, 8, fixed, edid)
			}
		}
		return errorPacket(order, ErrCodeRequest, 0, op, uint16(data))
	}
}

func TestMonitorsGoesOverTheWire(t *testing.T) {
	for _, order := range []binary.ByteOrder{binary.LittleEndian, binary.BigEndian} {
		c, f := dialFakeOrder(t, order, randrHandler(order))
		mons, err := c.Monitors(0)
		if err != nil {
			t.Fatalf("Monitors: %v", err)
		}
		if len(mons) != 1 {
			t.Fatalf("got %d monitors, want 1", len(mons))
		}
		m := mons[0]
		if m.Name != "HDMI-1" || m.Model != "DELL U2720Q" || !m.Primary ||
			m.Width != 1920 || m.Height != 1080 || m.WidthMM != 509 {
			t.Fatalf("got %+v, want HDMI-1 / DELL U2720Q / primary / 1920x1080", m)
		}
		// The whole exchange really happened: QueryExtension, RRQueryVersion,
		// RRGetMonitors, GetAtomName, InternAtom, RRGetOutputProperty.
		if n := len(f.requests()); n != 7 { // 1 setup + 6
			t.Errorf("%d packets crossed the pipe, want 7 (setup plus six requests)", n)
		}
	}
}

func TestMonitorsRefusesAScreenThatDoesNotExist(t *testing.T) {
	c, _ := dialFake(t, randrHandler(binary.LittleEndian))
	if _, err := c.Monitors(9); err == nil {
		t.Fatal("Monitors accepted screen 9 of a one-screen server")
	}
}

func TestRequestReportsAServerError(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return errorPacket(order, ErrCodeRequest, 0, op, uint16(data))
	})
	// An error reply must come back as an error, not as a packet: the shared
	// enumeration reads fields off whatever it is handed.
	reply, err := c.Request("Anything", opQueryExtension, 0, make([]byte, 8))
	if err == nil {
		t.Fatalf("Request returned %v and no error for an error reply", reply)
	}
	var xerr *XError
	if !errors.As(err, &xerr) || xerr.Op != "Anything" {
		t.Errorf("error %v does not name the request that failed", err)
	}
}

func TestRequestJoinsTheFixedPartAndTheData(t *testing.T) {
	order := binary.LittleEndian
	c, _ := dialFake(t, func(op, data byte, body []byte) []byte {
		return reply(order, 0, []byte{1, 2, 3, 4}, []byte("abcdefgh"))
	})
	pkt, err := c.Request("Anything", opQueryExtension, 0, make([]byte, 8))
	if err != nil {
		t.Fatalf("Request: %v", err)
	}
	if len(pkt) != 40 {
		t.Fatalf("reply is %d bytes, want 32 fixed plus 8 of data", len(pkt))
	}
	if pkt[8] != 1 || string(pkt[32:]) != "abcdefgh" {
		t.Errorf("reply %v: the two halves are not in order", pkt)
	}
}
