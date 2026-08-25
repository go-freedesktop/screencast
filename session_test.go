// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import "testing"

func TestSessionFrom(t *testing.T) {
	for _, tc := range []struct {
		display, wayland string
		want             SessionKind
		name             string
	}{
		{":0", "", SessionX11, "X11"},
		{"", "wayland-0", SessionWayland, "Wayland"},
		{":0", "wayland-0", SessionXwayland, "Xwayland"},
		{"", "", SessionNone, "none"},
	} {
		got := sessionFrom(tc.display, tc.wayland)
		if got != tc.want {
			t.Errorf("sessionFrom(%q, %q) = %v, want %v", tc.display, tc.wayland, got, tc.want)
		}
		if got.String() != tc.name {
			t.Errorf("%v.String() = %q, want %q", got, got.String(), tc.name)
		}
	}
	if got := SessionKind(99).String(); got != "none" {
		t.Errorf("an unknown SessionKind renders as %q", got)
	}
}

func TestSessionReadsTheEnvironment(t *testing.T) {
	t.Setenv("DISPLAY", ":3")
	t.Setenv("WAYLAND_DISPLAY", "")
	if got := Session(); got != SessionX11 {
		t.Errorf("Session() = %v, want X11", got)
	}
	t.Setenv("DISPLAY", "")
	t.Setenv("WAYLAND_DISPLAY", "wayland-1")
	if got := Session(); got != SessionWayland {
		t.Errorf("Session() = %v, want Wayland", got)
	}
}

func TestPortalConstantsAreTheRealNames(t *testing.T) {
	// A consumer that reads these to build its own D-Bus call must get the
	// names xdg-desktop-portal actually publishes.
	if PortalService != "org.freedesktop.portal.Desktop" ||
		PortalPath != "/org/freedesktop/portal/desktop" ||
		PortalIface != "org.freedesktop.portal.ScreenCast" {
		t.Errorf("portal names = %q, %q, %q", PortalService, PortalPath, PortalIface)
	}
}
