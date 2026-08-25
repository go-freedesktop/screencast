// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"encoding/binary"
	"io"
	"os"
	"strings"
	"testing"
)

func TestDialRejectsAnUnsetDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	if _, _, err := Dial(""); err == nil || !strings.Contains(err.Error(), "DISPLAY is not set") {
		t.Fatalf("Dial reported %v, want a DISPLAY-is-unset error", err)
	}
}

func TestDialRejectsAMalformedDisplay(t *testing.T) {
	if _, _, err := Dial("nonsense"); err == nil || !strings.Contains(err.Error(), "malformed") {
		t.Fatalf("Dial reported %v, want a malformed-DISPLAY error", err)
	}
}

func TestDialRejectsARemoteServer(t *testing.T) {
	_, addr, err := Dial("far.example:0")
	if err == nil || !strings.Contains(err.Error(), "unix-domain") {
		t.Fatalf("Dial reported %v, want a remote-server refusal", err)
	}
	if addr.Host != "far.example" {
		t.Errorf("the refusal did not carry the parsed address: %+v", addr)
	}
}

func TestDialReportsATransportFailure(t *testing.T) {
	// Display 9998 is past anything a session manager hands out — and off
	// Linux there is no transport at all — so the dial must fail rather than
	// hang. (Do not use a low number here: a machine running the test may
	// well have a real server on :0, and on :99 under Xvfb.)
	if c, _, err := Dial(":9998"); err == nil {
		_ = c.Close()
		t.Fatal("Dial succeeded against a display that is not there")
	}
}

func TestDialUsesTheEnvironment(t *testing.T) {
	t.Setenv("DISPLAY", "far.example:3")
	_, addr, err := Dial("")
	if err == nil {
		t.Fatal("Dial succeeded against a remote display")
	}
	if addr.Display != 3 || addr.Host != "far.example" {
		t.Errorf("Dial did not read DISPLAY from the environment: %+v", addr)
	}
}

// fakeTransport lets the dial sequence be driven end to end on any platform.
func TestDialFullSequence(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XAUTHORITY", dir+"/Xauthority")

	orig := dialSocket
	t.Cleanup(func() { dialSocket = orig })

	// A server that completes the handshake.
	f, cli := newFakeX(t, binary.LittleEndian)
	f.serve()
	dialSocket = func(path string) (io.ReadWriteCloser, error) {
		if path != "/tmp/.X11-unix/X5" {
			t.Errorf("dialed %q, want the socket for display 5", path)
		}
		return cli, nil
	}
	c, addr, err := Dial(":5")
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	if addr.Display != 5 || c.Setup() == nil {
		t.Errorf("Dial returned %+v", addr)
	}
	_ = c.Close()

	// A server that refuses the handshake: the transport must be closed
	// rather than leaked.
	f2, cli2 := newFakeX(t, binary.LittleEndian)
	f2.setupStatus = 0
	f2.setupReason = "No protocol specified"
	f2.serve()
	dialSocket = func(string) (io.ReadWriteCloser, error) { return cli2, nil }
	if _, _, err := Dial(":5"); err == nil {
		t.Error("Dial succeeded against a server that refused the setup")
	}

	// A corrupt Xauthority stops the dial before the handshake.
	if err := os.WriteFile(dir+"/Xauthority", []byte{0, 1, 0, 9, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	f3, cli3 := newFakeX(t, binary.LittleEndian)
	f3.serve()
	dialSocket = func(string) (io.ReadWriteCloser, error) { return cli3, nil }
	if _, _, err := Dial(":5"); err == nil {
		t.Error("Dial succeeded with a corrupt authority file")
	}
}
