// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package screencast

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/go-freedesktop/screencast/internal/x11"
	"github.com/godbus/dbus/v5"
)

// These are the Linux-only parts that need no display: the error translation,
// the identifier arithmetic, the property decoding and the portal probe. What
// genuinely needs an X server is in live_linux_test.go, behind the integration
// tag.

func TestMapXError(t *testing.T) {
	for _, tc := range []struct {
		code byte
		want error
	}{
		{x11.ErrCodeWindow, ErrNotFound},
		{x11.ErrCodeDrawable, ErrNotFound},
		{x11.ErrCodePixmap, ErrNotFound},
		{x11.ErrCodeAccess, ErrPermissionDenied},
		{x11.ErrCodeAlloc, ErrProtocol},
		{x11.ErrCodeMatch, ErrProtocol},
		{x11.ErrCodeValue, ErrProtocol},
	} {
		in := &x11.XError{Code: tc.code, Name: x11.ErrorName(tc.code), Op: "GetImage"}
		got := mapXError(in)
		if !errors.Is(got, tc.want) {
			t.Errorf("error code %d mapped to %v, want %v", tc.code, got, tc.want)
		}
		if !errors.As(got, new(*x11.XError)) {
			t.Errorf("error code %d lost the underlying X error: %v", tc.code, got)
		}
	}
	// Anything that is not an X protocol error passes through untouched.
	other := errors.New("a socket went away")
	if got := mapXError(other); !errors.Is(got, other) {
		t.Errorf("mapXError rewrote a non-protocol error: %v", got)
	}
	if mapXError(nil) != nil {
		t.Error("mapXError invented an error out of nil")
	}
}

func TestMapDialError(t *testing.T) {
	setup := &x11.SetupError{Reason: "No protocol specified"}
	got := mapDialError(setup)
	if !errors.Is(got, ErrPermissionDenied) {
		t.Errorf("a setup refusal mapped to %v, want ErrPermissionDenied", got)
	}
	if !strings.Contains(got.Error(), "No protocol specified") {
		t.Errorf("the mapped error lost the server's own wording: %v", got)
	}

	// A missing socket on an X11 session is "no backend"; the diagnosis is
	// spliced in when the session explains itself better.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	missing := fmt.Errorf("dialing: %w", os.ErrNotExist)
	if got := mapDialError(missing); !errors.Is(got, ErrNoBackend) {
		t.Errorf("a missing socket mapped to %v, want ErrNoBackend", got)
	}

	// On a portal-only Wayland session the SAME failure is the PipeWire wall.
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	withPortal(t, 5)
	if got := mapDialError(missing); !errors.Is(got, ErrPortalPipeWire) {
		t.Errorf("a missing socket in a Wayland session mapped to %v, want ErrPortalPipeWire", got)
	}

	// Anything else passes through.
	other := errors.New("connection reset by peer")
	if got := mapDialError(other); !errors.Is(got, other) {
		t.Errorf("mapDialError rewrote an unrelated error: %v", got)
	}
}

func TestErrIsNotExist(t *testing.T) {
	if !errIsNotExist(fmt.Errorf("wrapped: %w", os.ErrNotExist)) {
		t.Error("errIsNotExist missed a wrapped ErrNotExist")
	}
	if errIsNotExist(errors.New("something else")) {
		t.Error("errIsNotExist matched an unrelated error")
	}
}

func TestSyntheticDisplayID(t *testing.T) {
	// The high bit is what keeps a synthetic id away from a real RANDR name
	// atom, which the server allocates from the bottom up.
	seen := map[uint32]bool{}
	for screen := 0; screen < 4; screen++ {
		for i := 0; i < 4; i++ {
			id := syntheticDisplayID(screen, i)
			if id&0x80000000 == 0 {
				t.Errorf("syntheticDisplayID(%d, %d) = %#x, which could collide with an atom",
					screen, i, id)
			}
			if seen[id] {
				t.Errorf("syntheticDisplayID(%d, %d) = %#x, already used", screen, i, id)
			}
			seen[id] = true
		}
	}
}

func TestSplitWMClass(t *testing.T) {
	for _, tc := range []struct {
		name            string
		in              []byte
		instance, class string
	}{
		{"both", []byte("xterm\x00XTerm\x00"), "xterm", "XTerm"},
		{"no trailing NUL", []byte("xterm\x00XTerm"), "xterm", "XTerm"},
		{"instance only", []byte("xterm\x00"), "xterm", ""},
		{"no NUL at all", []byte("xterm"), "xterm", ""},
		{"empty", nil, "", ""},
		{"empty instance", []byte("\x00XTerm\x00"), "", "XTerm"},
	} {
		instance, class := splitWMClass(tc.in)
		if instance != tc.instance || class != tc.class {
			t.Errorf("%s: splitWMClass(%q) = %q, %q; want %q, %q",
				tc.name, tc.in, instance, class, tc.instance, tc.class)
		}
	}
}

func TestApplicationsOf(t *testing.T) {
	ws := []Window{
		{ID: 1, PID: 200, AppName: "B", BundleID: "b"},
		{ID: 2, PID: 100, AppName: "", BundleID: ""},
		{ID: 3, PID: 100, AppName: "A", BundleID: "a"},
		{ID: 4, PID: 0, AppName: "no pid"},
	}
	got := applicationsOf(ws)
	if len(got) != 2 {
		t.Fatalf("applicationsOf returned %d applications: %+v", len(got), got)
	}
	// Ascending PID, so the result is stable between calls.
	if got[0].PID != 100 || got[1].PID != 200 {
		t.Errorf("applicationsOf is not sorted by pid: %+v", got)
	}
	// A later window with a name fills in for an earlier nameless one.
	if got[0].Name != "A" || got[0].BundleID != "a" {
		t.Errorf("applicationsOf kept the nameless entry: %+v", got[0])
	}
	if applicationsOf(nil) == nil {
		t.Error("applicationsOf(nil) returned nil rather than an empty slice")
	}
}

// withPortal substitutes a session bus that answers the ScreenCast version
// property, so the portal probe is testable with no bus at all.
func withPortal(t *testing.T, version uint32) {
	t.Helper()
	origBus, origVer := dbusSessionBus, portalVersion
	t.Cleanup(func() { dbusSessionBus, portalVersion = origBus, origVer })
	dbusSessionBus = func() (*dbus.Conn, error) { return nil, nil }
	portalVersion = func(*dbus.Conn) (uint32, error) { return version, nil }
}

// withoutPortal substitutes a session bus that is not there.
func withoutPortal(t *testing.T) {
	t.Helper()
	origBus, origVer := dbusSessionBus, portalVersion
	t.Cleanup(func() { dbusSessionBus, portalVersion = origBus, origVer })
	dbusSessionBus = func() (*dbus.Conn, error) { return nil, errors.New("no session bus") }
	portalVersion = func(*dbus.Conn) (uint32, error) { return 0, errors.New("unreachable") }
}

func TestPortalAvailable(t *testing.T) {
	withPortal(t, 4)
	if present, version := PortalAvailable(); !present || version != 4 {
		t.Errorf("PortalAvailable = %v, %d; want true, 4", present, version)
	}
	withoutPortal(t)
	if present, version := PortalAvailable(); present || version != 0 {
		t.Errorf("PortalAvailable with no bus = %v, %d", present, version)
	}
	// A bus that answers, but with a property the interface does not carry.
	origBus, origVer := dbusSessionBus, portalVersion
	t.Cleanup(func() { dbusSessionBus, portalVersion = origBus, origVer })
	dbusSessionBus = func() (*dbus.Conn, error) { return nil, nil }
	portalVersion = func(*dbus.Conn) (uint32, error) { return 0, errors.New("no such interface") }
	if present, _ := PortalAvailable(); present {
		t.Error("PortalAvailable reported a portal that does not implement ScreenCast")
	}
}

func TestPortalVersionFrom(t *testing.T) {
	if v, err := portalVersionFrom(dbus.MakeVariant(uint32(7))); err != nil || v != 7 {
		t.Errorf("portalVersionFrom(uint32 7) = %d, %v", v, err)
	}
	// A portal that answered with something else is an error, never a panic.
	_, err := portalVersionFrom(dbus.MakeVariant("five"))
	if err == nil || !strings.Contains(err.Error(), "not uint32") {
		t.Errorf("portalVersionFrom(string) = %v, want a type error", err)
	}
}

func TestDiagnose(t *testing.T) {
	// An X11 or Xwayland session names a display, so there is nothing to
	// diagnose; whether the connection then succeeds is Probe's question.
	t.Setenv("DISPLAY", ":0")
	t.Setenv("WAYLAND_DISPLAY", "")
	if err := Diagnose(); err != nil {
		t.Errorf("Diagnose on an X11 session = %v, want nil", err)
	}
	t.Setenv("WAYLAND_DISPLAY", "wayland-0")
	if err := Diagnose(); err != nil {
		t.Errorf("Diagnose on an Xwayland session = %v, want nil", err)
	}

	// A Wayland session with the portal there IS the PipeWire wall, and the
	// message must say so precisely — that is the whole point of the probe.
	t.Setenv("DISPLAY", "")
	withPortal(t, 5)
	err := Diagnose()
	if !errors.Is(err, ErrPortalPipeWire) {
		t.Fatalf("Diagnose on a portal-only Wayland session = %v, want ErrPortalPipeWire", err)
	}
	for _, want := range []string{"PipeWire", "ScreenCast version 5", "wayland-0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the diagnosis does not mention %q: %v", want, err)
		}
	}

	// A Wayland session with no portal either is simply no backend.
	withoutPortal(t)
	if err := Diagnose(); !errors.Is(err, ErrNoBackend) {
		t.Errorf("Diagnose on a Wayland session with no portal = %v, want ErrNoBackend", err)
	}

	// No graphical session at all.
	t.Setenv("WAYLAND_DISPLAY", "")
	err = Diagnose()
	if !errors.Is(err, ErrNoBackend) {
		t.Errorf("Diagnose with no session = %v, want ErrNoBackend", err)
	}
	if !strings.Contains(err.Error(), "neither DISPLAY nor WAYLAND_DISPLAY") {
		t.Errorf("the diagnosis does not say what is missing: %v", err)
	}
}

func TestProbeAndAvailableWithNoSession(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	withoutPortal(t)
	if err := Probe(); !errors.Is(err, ErrNoBackend) {
		t.Errorf("Probe with no session = %v, want ErrNoBackend", err)
	}
	if Available() || Authorized() || RequestAuthorization() {
		t.Error("a process with no display reported that capture is possible")
	}
	// The enumerations refuse for the same reason rather than returning
	// something empty and pretending.
	ctx := t.Context()
	for name, fn := range map[string]func() error{
		"Displays":                func() error { _, err := Displays(ctx); return err },
		"Windows":                 func() error { _, err := Windows(ctx); return err },
		"Shareable":               func() error { _, err := Shareable(ctx); return err },
		"CurrentProcessShareable": func() error { _, err := CurrentProcessShareable(ctx); return err },
		"CaptureDisplay":          func() error { _, err := CaptureDisplay(ctx, Display{}, Options{}); return err },
		"CaptureWindow":           func() error { _, err := CaptureWindow(ctx, Window{}, Options{}); return err },
	} {
		if err := fn(); !errors.Is(err, ErrNoBackend) {
			t.Errorf("%s with no session reported %v, want ErrNoBackend", name, err)
		}
	}
}

func TestSettledDialAnswer(t *testing.T) {
	for _, err := range []error{ErrPermissionDenied, ErrPortalPipeWire, ErrNoBackend,
		fmt.Errorf("wrapped: %w", ErrNoBackend)} {
		if !settledDialAnswer(err) {
			t.Errorf("%v was treated as worth retrying", err)
		}
	}
	for _, err := range []error{errors.New("connection reset by peer"), ErrProtocol} {
		if settledDialAnswer(err) {
			t.Errorf("%v was treated as settled", err)
		}
	}
}

func TestProbeDoesNotRetryASettledAnswer(t *testing.T) {
	// A refusal that will not change must not cost a second round trip: the
	// retry is there for a server resetting a connection under churn, not for
	// an authentication failure or a missing session.
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	withoutPortal(t)
	for i := 0; i < 3; i++ {
		if err := Probe(); !errors.Is(err, ErrNoBackend) {
			t.Fatalf("Probe = %v", err)
		}
	}
}

func TestCaptureValidatesOptionsBeforeTouchingTheDisplay(t *testing.T) {
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "")
	withoutPortal(t)
	ctx := t.Context()
	bad := Options{QueueDepth: 1}
	if _, err := CaptureDisplay(ctx, Display{}, bad); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("CaptureDisplay reported %v, want ErrInvalidOption", err)
	}
	if _, err := CaptureWindow(ctx, Window{}, bad); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("CaptureWindow reported %v, want ErrInvalidOption", err)
	}
}

func TestBackendIsNamed(t *testing.T) {
	if Backend != "X11" {
		t.Errorf("Backend = %q, want \"X11\"", Backend)
	}
}
