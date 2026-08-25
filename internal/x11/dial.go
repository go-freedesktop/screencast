// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"fmt"
)

// dialSocket is the transport seam. It is a package variable so the dial
// sequence above it — parse, refuse a remote host, load the cookie, hand
// shake — is testable on every platform, including the ones with no X server
// and no unix transport at all.
var dialSocket = dialUnix

// Dial opens a connection to the X server named by a DISPLAY value, loading
// the MIT-MAGIC-COOKIE-1 from the Xauthority file and running the setup
// exchange. An empty display uses $DISPLAY.
//
// It dials a unix-domain socket, and only a unix-domain socket: a TCP X server
// cannot be handed a shared-memory descriptor, so the fast path would be
// unavailable and the capture would ship every pixel over the network anyway.
func Dial(display string) (*Conn, Address, error) {
	if display == "" {
		display = DisplayEnv()
	}
	addr, err := ParseDisplay(display)
	if err != nil {
		return nil, Address{}, err
	}
	if !addr.Local() {
		return nil, addr, fmt.Errorf(
			"x11: DISPLAY %q names the remote host %q; this package dials only local "+
				"unix-domain servers, because a remote server cannot be handed a "+
				"shared-memory descriptor", display, addr.Host)
	}
	rw, err := dialSocket(addr.SocketPath())
	if err != nil {
		return nil, addr, err
	}
	name, data, err := LoadAuthCookie(AuthFilePath(), "", addr.DisplayNumber())
	if err != nil {
		_ = rw.Close()
		return nil, addr, err
	}
	// The wire order is the client's choice; little-endian matches every
	// machine this fleet builds for and keeps the common path a plain memory
	// read.
	c, err := Handshake(rw, binary.LittleEndian, name, data)
	if err != nil {
		_ = rw.Close()
		return nil, addr, err
	}
	return c, addr, nil
}
