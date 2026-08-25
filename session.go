// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package screencast

import "os"

// SessionKind is what kind of display session the process is sitting in, as
// the environment describes it.
type SessionKind int

// The recognised session kinds.
const (
	// SessionNone means neither DISPLAY nor WAYLAND_DISPLAY is set: there is
	// no graphical session here at all.
	SessionNone SessionKind = iota
	// SessionX11 means DISPLAY is set. This package can capture it.
	SessionX11
	// SessionWayland means WAYLAND_DISPLAY is set and DISPLAY is not, so
	// there is not even an Xwayland to fall back on.
	SessionWayland
	// SessionXwayland means both are set: a Wayland compositor running an
	// Xwayland server. Whether an X11 capture sees the whole desktop or only
	// the X11 clients depends on the compositor, and is stated in the doc of
	// [Diagnose].
	SessionXwayland
)

// String names the session kind.
func (k SessionKind) String() string {
	switch k {
	case SessionX11:
		return "X11"
	case SessionWayland:
		return "Wayland"
	case SessionXwayland:
		return "Xwayland"
	default:
		return "none"
	}
}

// Session reports what kind of display session the environment describes.
func Session() SessionKind { return sessionFrom(os.Getenv("DISPLAY"), os.Getenv("WAYLAND_DISPLAY")) }

// sessionFrom classifies a (DISPLAY, WAYLAND_DISPLAY) pair. It is split out
// from [Session] so the classification is testable without touching the
// process environment.
func sessionFrom(display, wayland string) SessionKind {
	switch {
	case display != "" && wayland != "":
		return SessionXwayland
	case display != "":
		return SessionX11
	case wayland != "":
		return SessionWayland
	default:
		return SessionNone
	}
}

// PortalService, PortalPath and PortalIface name the xdg-desktop-portal
// ScreenCast interface [PortalAvailable] looks for. They are exported on every
// platform so a consumer that reports them compiles everywhere, even where
// there is no session bus to look on.
const (
	PortalService = "org.freedesktop.portal.Desktop"
	PortalPath    = "/org/freedesktop/portal/desktop"
	PortalIface   = "org.freedesktop.portal.ScreenCast"
)
