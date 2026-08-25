// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	xproto "github.com/go-freedesktop/x11"
)

// The RANDR 1.5 and XINERAMA monitor enumerations used to live here, in full.
// They now live in github.com/go-freedesktop/x11, which github.com/go-widgets/
// window shares: an X screen is one coordinate space with the physical
// displays laid out inside it, and which rectangle is where does not depend on
// whether the client is capturing them or putting a window on one. A copy in
// each drifts silently until something fails on one back-end only.
//
// What stays here is the adapter: [Conn.Request], which is the whole of what
// the shared enumeration needs of a connection, plus the screen-index lookup
// this package's callers already speak. The capture loop's own request/reply
// machine — reused buffers, no allocation per frame — is untouched.

// Monitor is one physical output's rectangle inside an X screen. It is a name
// for xproto.Monitor, not a copy.
type Monitor = xproto.Monitor

// Request sends one request and returns its reply — the 32-byte fixed part
// followed by its additional data, as one slice — which is what
// [xproto.Requester] asks of a connection.
//
// It allocates, unlike [Conn.roundTrip], because it has to join the two halves
// the capture path deliberately keeps apart. That is the right trade here: an
// enumeration happens once when a caller picks a display, not once per frame.
func (c *Conn) Request(op string, opcode, data byte, body []byte) ([]byte, error) {
	hdr, extra, err := c.roundTrip(op, opcode, data, body)
	if err != nil {
		return nil, err
	}
	reply := make([]byte, len(hdr)+len(extra))
	copy(reply, hdr[:])
	copy(reply[len(hdr):], extra)
	return reply, nil
}

// Monitors lists the monitors of the given screen, trying RANDR 1.5 first,
// then XINERAMA, and falling back to the whole screen as a single nameless
// monitor. It never returns an empty list without an error: a screen always
// has at least itself.
func (c *Conn) Monitors(screen int) ([]Monitor, error) {
	return xproto.Monitors(c, c.setup.ScreenOf(screen))
}

// Conn must satisfy the interface the shared enumeration asks through, and
// asserting it here means a signature change upstream fails the build rather
// than the enumeration.
var _ xproto.Requester = (*Conn)(nil)
