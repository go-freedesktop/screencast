// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"fmt"
	"os"
	"strconv"
	"strings"
)

// Address is a parsed DISPLAY string: which host, which display number, which
// screen of that display. It is what turns ":0.1" into a socket path and a
// screen index.
type Address struct {
	// Host is the machine the server runs on. Empty (or "unix") means the
	// local machine over a unix-domain socket, which is the only transport
	// this package dials: a TCP X server cannot pass a shared-memory
	// descriptor, so the fast path would be unavailable anyway.
	Host string
	// Display is the display number, the "0" of ":0.1".
	Display int
	// Screen is the screen index, the "1" of ":0.1"; 0 when unstated.
	Screen int
}

// String renders the address back as a DISPLAY value.
func (a Address) String() string { return fmt.Sprintf("%s:%d.%d", a.Host, a.Display, a.Screen) }

// DisplayNumber renders the display number as the ASCII string an Xauthority
// record states it in.
func (a Address) DisplayNumber() string { return strconv.Itoa(a.Display) }

// Local reports whether the address names a unix-domain server on this
// machine.
func (a Address) Local() bool {
	return a.Host == "" || a.Host == "unix" || a.Host == "localhost"
}

// SocketPath is the unix-domain socket a local server listens on.
func (a Address) SocketPath() string { return fmt.Sprintf("/tmp/.X11-unix/X%d", a.Display) }

// ParseDisplay parses a DISPLAY value of the form
// [host]:display[.screen], as well as the "unix/:0" and "host/unix:0" spellings
// some session managers emit. An empty value is an error: there is no default
// display.
func ParseDisplay(s string) (Address, error) {
	if s == "" {
		return Address{}, fmt.Errorf("x11: DISPLAY is not set")
	}
	colon := strings.LastIndex(s, ":")
	if colon < 0 {
		return Address{}, fmt.Errorf("x11: malformed DISPLAY %q: no colon", s)
	}
	host := s[:colon]
	rest := s[colon+1:]
	// "unix/:0" and "hostname/unix:0" both mean the local unix socket.
	if i := strings.LastIndex(host, "/"); i >= 0 {
		if host[i+1:] == "unix" || host[:i] == "unix" {
			host = ""
		} else {
			host = host[i+1:]
		}
	}
	if host == "unix" {
		host = "" // "unix:0" is the local socket spelled out
	}
	if rest == "" {
		return Address{}, fmt.Errorf("x11: malformed DISPLAY %q: no display number", s)
	}
	num, screen := rest, "0"
	if dot := strings.Index(rest, "."); dot >= 0 {
		num, screen = rest[:dot], rest[dot+1:]
	}
	d, err := strconv.Atoi(num)
	if err != nil || d < 0 {
		return Address{}, fmt.Errorf("x11: malformed DISPLAY %q: bad display number %q", s, num)
	}
	sc, err := strconv.Atoi(screen)
	if err != nil || sc < 0 {
		return Address{}, fmt.Errorf("x11: malformed DISPLAY %q: bad screen number %q", s, screen)
	}
	return Address{Host: host, Display: d, Screen: sc}, nil
}

// DisplayEnv returns the DISPLAY environment value.
func DisplayEnv() string { return os.Getenv("DISPLAY") }
