// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package screencast

import (
	"errors"
	"fmt"
	"os"

	"github.com/godbus/dbus/v5"
)

// This file does not capture anything. It answers the question a failed
// capture raises: WHY is there nothing to capture, and is that a bug or a
// wall?
//
// On GNOME and on KDE the answer is the same and it is a wall. Those
// compositors expose no screen-copy protocol of their own; the sanctioned
// route is xdg-desktop-portal's org.freedesktop.portal.ScreenCast, which
// negotiates over D-Bus and then hands back a PIPEWIRE stream. A pure-Go
// PipeWire client is a large piece of work — a native protocol over its own
// socket, SPA pods, buffer negotiation, dmabuf — and this package deliberately
// does not start it.
//
// So instead of failing with "no display", a Wayland session gets
// [ErrPortalPipeWire], which names the wall precisely. Knowing where the wall
// is, is a result.

// PortalAvailable reports whether xdg-desktop-portal's ScreenCast interface is
// on the session bus, and the interface version it advertises.
//
// It only LOOKS. It never opens a session, never prompts the user and never
// starts a PipeWire stream, because this package cannot consume one.
func PortalAvailable() (present bool, version uint32) {
	conn, err := dbusSessionBus()
	if err != nil {
		return false, 0
	}
	if conn != nil {
		defer func() { _ = conn.Close() }()
	}
	v, err := portalVersion(conn)
	if err != nil {
		return false, 0
	}
	return true, v
}

// dbusSessionBus and portalVersion sit behind package variables so the probe's
// failure paths are testable without a session bus.
var (
	dbusSessionBus = func() (*dbus.Conn, error) { return dbus.SessionBus() }

	portalVersion = func(conn *dbus.Conn) (uint32, error) {
		obj := conn.Object(PortalService, dbus.ObjectPath(PortalPath))
		v, err := obj.GetProperty(PortalIface + ".version")
		if err != nil {
			return 0, err
		}
		return portalVersionFrom(v)
	}
)

// portalVersionFrom reads the version out of the property variant. It is split
// out from the D-Bus call so the type check — a portal that answered with
// something other than a uint32 must be an error, not a panic — is testable
// without a session bus.
func portalVersionFrom(v dbus.Variant) (uint32, error) {
	n, ok := v.Value().(uint32)
	if !ok {
		return 0, fmt.Errorf("screencast: the portal's %s.version property is %T, not uint32",
			PortalIface, v.Value())
	}
	return n, nil
}

// Diagnose explains why no X11 capture is possible, given the session the
// process is in. It is what [CaptureDisplay] reports when the connection
// cannot be made, and it is worth calling directly to print a useful message.
//
// It returns nil when an X11 display is at least NAMED — whether the
// connection then succeeds is a separate question, answered by [Authorized].
//
// On a Wayland session it consults the portal. If the portal is there, the
// answer is [ErrPortalPipeWire] with the portal's version spliced in: the
// route exists, it just ends in PipeWire. If it is not, the answer is
// [ErrNoBackend].
func Diagnose() error {
	switch Session() {
	case SessionX11, SessionXwayland:
		return nil
	case SessionWayland:
		if present, version := PortalAvailable(); present {
			return fmt.Errorf("%w (xdg-desktop-portal ScreenCast version %d is on the "+
				"session bus; WAYLAND_DISPLAY=%s and DISPLAY is unset, so there is no "+
				"Xwayland to capture instead)",
				ErrPortalPipeWire, version, os.Getenv("WAYLAND_DISPLAY"))
		}
		return fmt.Errorf("%w: WAYLAND_DISPLAY=%s names a Wayland compositor, DISPLAY is "+
			"unset so there is no Xwayland, and xdg-desktop-portal is not on the session "+
			"bus either", ErrNoBackend, os.Getenv("WAYLAND_DISPLAY"))
	default:
		return fmt.Errorf("%w: neither DISPLAY nor WAYLAND_DISPLAY is set", ErrNoBackend)
	}
}

// errIsNotExist reports whether err bottoms out in a missing file, which is
// what dialing an X socket that is not there looks like.
func errIsNotExist(err error) bool { return errors.Is(err, os.ErrNotExist) }
