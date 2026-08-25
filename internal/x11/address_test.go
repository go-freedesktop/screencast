// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import "testing"

func TestParseDisplay(t *testing.T) {
	for _, tc := range []struct {
		in                    string
		host                  string
		display, screen       int
		local                 bool
		wantErr               bool
		socket, number, print string
	}{
		{in: ":0", local: true, socket: "/tmp/.X11-unix/X0", number: "0", print: ":0.0"},
		{in: ":0.1", screen: 1, local: true, socket: "/tmp/.X11-unix/X0", number: "0", print: ":0.1"},
		{in: ":12.3", display: 12, screen: 3, local: true, socket: "/tmp/.X11-unix/X12", number: "12", print: ":12.3"},
		{in: "unix:0", local: true, socket: "/tmp/.X11-unix/X0", number: "0", print: ":0.0"},
		{in: "unix/:1", display: 1, local: true, socket: "/tmp/.X11-unix/X1", number: "1", print: ":1.0"},
		{in: "myhost/unix:2", display: 2, local: true, socket: "/tmp/.X11-unix/X2", number: "2", print: ":2.0"},
		{in: "localhost:0", host: "localhost", local: true, socket: "/tmp/.X11-unix/X0", number: "0", print: "localhost:0.0"},
		{in: "far.example:0", host: "far.example", socket: "/tmp/.X11-unix/X0", number: "0", print: "far.example:0.0"},
		{in: "somewhere/eth0:0", host: "eth0", socket: "/tmp/.X11-unix/X0", number: "0", print: "eth0:0.0"},
		{in: "", wantErr: true},
		{in: "nocolon", wantErr: true},
		{in: ":", wantErr: true},
		{in: ":abc", wantErr: true},
		{in: ":-1", wantErr: true},
		{in: ":0.x", wantErr: true},
		{in: ":0.-2", wantErr: true},
	} {
		a, err := ParseDisplay(tc.in)
		if tc.wantErr {
			if err == nil {
				t.Errorf("ParseDisplay(%q) unexpectedly succeeded as %+v", tc.in, a)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParseDisplay(%q): %v", tc.in, err)
			continue
		}
		if a.Host != tc.host || a.Display != tc.display || a.Screen != tc.screen {
			t.Errorf("ParseDisplay(%q) = %+v, want host=%q display=%d screen=%d",
				tc.in, a, tc.host, tc.display, tc.screen)
		}
		if a.Local() != tc.local {
			t.Errorf("ParseDisplay(%q).Local() = %v, want %v", tc.in, a.Local(), tc.local)
		}
		if a.SocketPath() != tc.socket {
			t.Errorf("ParseDisplay(%q).SocketPath() = %q, want %q", tc.in, a.SocketPath(), tc.socket)
		}
		if a.DisplayNumber() != tc.number {
			t.Errorf("ParseDisplay(%q).DisplayNumber() = %q, want %q", tc.in, a.DisplayNumber(), tc.number)
		}
		if a.String() != tc.print {
			t.Errorf("ParseDisplay(%q).String() = %q, want %q", tc.in, a.String(), tc.print)
		}
	}
}

func TestDisplayEnv(t *testing.T) {
	t.Setenv("DISPLAY", ":7")
	if got := DisplayEnv(); got != ":7" {
		t.Errorf("DisplayEnv() = %q, want \":7\"", got)
	}
}
