// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package screencast

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sort"
	"time"

	"github.com/go-freedesktop/screencast/internal/x11"
)

// Backend names the capture route in use.
const Backend = "X11"

// Available reports whether this process can reach a capturable display
// server. It opens a connection and closes it again, so it answers about the
// server that is actually there rather than about an environment variable.
//
// Use [Probe] instead when you want to know WHY the answer is no.
func Available() bool { return Probe() == nil }

// Probe reports nil when a capture backend is reachable, and the reason
// otherwise — [ErrPortalPipeWire] for a portal-only Wayland session,
// [ErrNoBackend] when there is no display server at all,
// [ErrPermissionDenied] when the server refused the cookie.
func Probe() error {
	c, _, err := dial()
	if err != nil {
		return err
	}
	return c.Close()
}

// Authorized reports whether capture is allowed WITHOUT asking anyone.
//
// X11 has no per-application capture grant: a client that completes the setup
// handshake may read any drawable on the server. So this reports whether the
// handshake succeeds, which is the same question in X11 terms — and unlike
// macOS, the answer is not something a user can change in a settings pane. It
// is identical to [Available] by construction; both are kept so a consumer's
// startup check reads the same on every platform.
func Authorized() bool { return Available() }

// RequestAuthorization reports [Authorized]. There is nothing to request: X11
// prompts for nothing and grants nothing per-application. It never blocks and
// never shows a dialog.
func RequestAuthorization() bool { return Authorized() }

// dial opens a connection to the X server, translating a failure into this
// package's sentinels — including [ErrPortalPipeWire] when the session turns
// out to be a portal-only Wayland one.
//
// It retries ONCE when the failure is a transport one. A display server under
// a burst of connect-and-disconnect churn resets a connection now and then —
// Xvfb was measured resetting 6 of 200 back-to-back connections, mid-setup —
// and an enumeration or a capture that failed because of that would be
// reporting the wrong thing entirely. A settled answer, an authentication
// refusal or a session with no backend at all, is never retried: it will not
// change.
func dial() (*x11.Conn, x11.Address, error) {
	c, addr, err := dialOnce()
	if err == nil || settledDialAnswer(err) {
		return c, addr, err
	}
	time.Sleep(dialRetryDelay)
	return dialOnce()
}

// dialRetryDelay is how long dial waits before its single retry: long enough
// for a server's accept queue to drain, short enough to stay unnoticed.
const dialRetryDelay = 20 * time.Millisecond

// settledDialAnswer reports whether a dial failure is one that a retry cannot
// improve.
func settledDialAnswer(err error) bool {
	return errors.Is(err, ErrPermissionDenied) ||
		errors.Is(err, ErrPortalPipeWire) ||
		errors.Is(err, ErrNoBackend)
}

// dialOnce makes one attempt.
func dialOnce() (*x11.Conn, x11.Address, error) {
	if err := Diagnose(); err != nil {
		return nil, x11.Address{}, err
	}
	c, addr, err := x11.Dial("")
	if err != nil {
		return nil, addr, mapDialError(err)
	}
	return c, addr, nil
}

// mapDialError translates an x11 dial or handshake failure into a package
// sentinel, keeping the server's own wording — and the original error — behind
// it. Both %w verbs matter: errors.Is finds the sentinel, and errors.As still
// finds the [x11.SetupError] underneath for a caller that wants the detail.
func mapDialError(err error) error {
	var se *x11.SetupError
	if errors.As(err, &se) {
		return fmt.Errorf("%w: the server said %q: %w", ErrPermissionDenied, se.Reason, err)
	}
	if errIsNotExist(err) {
		if d := Diagnose(); d != nil {
			return d
		}
		return fmt.Errorf("%w: %w", ErrNoBackend, err)
	}
	return err
}

// mapXError translates an X protocol error into a package sentinel, WITHOUT
// discarding it: the result matches the sentinel with errors.Is and still
// yields the [x11.XError] — with the server's error code, opcode and bad
// value — through errors.As.
func mapXError(err error) error {
	var xe *x11.XError
	if errors.As(err, &xe) {
		switch xe.Code {
		case x11.ErrCodeWindow, x11.ErrCodeDrawable, x11.ErrCodePixmap:
			return fmt.Errorf("%w: %w", ErrNotFound, err)
		case x11.ErrCodeAccess:
			return fmt.Errorf("%w: %w", ErrPermissionDenied, err)
		case x11.ErrCodeAlloc:
			return fmt.Errorf("%w: the server could not allocate the image: %w", ErrProtocol, err)
		}
		return fmt.Errorf("%w: %w", ErrProtocol, err)
	}
	return err
}

// Displays lists the capturable displays: one per RANDR monitor of every X
// screen the server has, or one per X screen when the server has no RANDR and
// no XINERAMA.
//
// The ID is the monitor's RANDR name atom when it has one, which is stable for
// the lifetime of the server, and a synthetic screen/index composite
// otherwise. Compare displays by ID, not by position in the slice.
func Displays(ctx context.Context) ([]Display, error) {
	c, _, err := dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return displaysOn(ctx, c)
}

// syntheticDisplayID builds the ID of a monitor that carries no RANDR name
// atom. The high bit is set so it can never collide with a real atom, which
// the server allocates from the bottom up.
func syntheticDisplayID(screen, index int) uint32 {
	return 0x80000000 | uint32(screen&0x7fff)<<16 | uint32(index&0xffff)
}

// displaysOn enumerates the displays of an open connection.
func displaysOn(ctx context.Context, c *x11.Conn) ([]Display, error) {
	setup := c.Setup()
	var out []Display
	for i := range setup.Screens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		sc := &setup.Screens[i]
		mons, err := c.Monitors(i)
		if err != nil {
			return nil, mapXError(err)
		}
		for j, m := range mons {
			id := m.NameAtom
			if id == x11.AtomNone {
				id = syntheticDisplayID(i, j)
			}
			w, h := int(m.Width), int(m.Height)
			out = append(out, Display{
				ID:          id,
				Name:        m.Name,
				Width:       w,
				Height:      h,
				PixelWidth:  w,
				PixelHeight: h,
				Frame:       Rect{X: float64(m.X), Y: float64(m.Y), W: float64(w), H: float64(h)},
				Main:        m.Primary,
				Screen:      i,
				Root:        sc.Root,
			})
		}
	}
	if len(out) == 0 {
		return nil, ErrNoDisplay
	}
	return out, nil
}

// Windows lists the capturable top-level windows, newest-mapped last, as the
// window manager's _NET_CLIENT_LIST states them. A server with no
// EWMH-compliant window manager falls back to walking the window tree.
func Windows(ctx context.Context) ([]Window, error) {
	c, _, err := dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	return windowsOn(ctx, c)
}

// atoms holds the interned atoms a window enumeration needs. They are interned
// once per connection, not once per window.
type atoms struct {
	clientList  uint32
	activeWin   uint32
	netWMName   uint32
	netWMPID    uint32
	utf8String  uint32
	wmState     uint32
	netWMWinTyp uint32
	typeNormal  uint32
	typeDock    uint32
	typeDesktop uint32
}

// internAtoms interns every atom the enumeration uses. onlyIfExists is false
// for the ones a client may legitimately be the first to name.
func internAtoms(c *x11.Conn) (atoms, error) {
	var a atoms
	names := []struct {
		dst  *uint32
		name string
	}{
		{&a.clientList, "_NET_CLIENT_LIST"},
		{&a.activeWin, "_NET_ACTIVE_WINDOW"},
		{&a.netWMName, "_NET_WM_NAME"},
		{&a.netWMPID, "_NET_WM_PID"},
		{&a.utf8String, "UTF8_STRING"},
		{&a.wmState, "WM_STATE"},
		{&a.netWMWinTyp, "_NET_WM_WINDOW_TYPE"},
		{&a.typeNormal, "_NET_WM_WINDOW_TYPE_NORMAL"},
		{&a.typeDock, "_NET_WM_WINDOW_TYPE_DOCK"},
		{&a.typeDesktop, "_NET_WM_WINDOW_TYPE_DESKTOP"},
	}
	for _, n := range names {
		v, err := c.InternAtom(n.name, false)
		if err != nil {
			return atoms{}, mapXError(err)
		}
		*n.dst = v
	}
	return a, nil
}

// windowsOn enumerates the windows of an open connection.
func windowsOn(ctx context.Context, c *x11.Conn) ([]Window, error) {
	a, err := internAtoms(c)
	if err != nil {
		return nil, err
	}
	setup := c.Setup()
	var out []Window
	for i := range setup.Screens {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root := setup.Screens[i].Root
		active := uint32(0)
		if p, err := c.GetProperty(root, a.activeWin, x11.AtomWindow, 1); err == nil {
			if v := p.Uint32s(c.Order()); len(v) > 0 {
				active = v[0]
			}
		}
		ids, err := topLevels(c, root, a)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			w, ok := describeWindow(c, id, root, i, a)
			if !ok {
				continue
			}
			w.Active = id == active
			out = append(out, w)
		}
	}
	return out, nil
}

// topLevels returns the client windows of a screen: the window manager's
// _NET_CLIENT_LIST when there is one, and otherwise the root's direct children
// resolved to their client windows.
func topLevels(c *x11.Conn, root uint32, a atoms) ([]uint32, error) {
	if p, err := c.GetProperty(root, a.clientList, x11.AtomWindow, 4096); err == nil {
		if ids := p.Uint32s(c.Order()); len(ids) > 0 {
			return ids, nil
		}
	}
	_, _, children, err := c.QueryTree(root)
	if err != nil {
		return nil, mapXError(err)
	}
	out := make([]uint32, 0, len(children))
	for _, ch := range children {
		out = append(out, clientWindow(c, ch, a))
	}
	return out, nil
}

// maxClientWindowDepth bounds the descent looking for a reparenting window
// manager's client window. Every window manager in use puts the client at most
// two frames down; the bound is what keeps a malicious or broken tree from
// turning an enumeration into a walk of the whole server.
const maxClientWindowDepth = 4

// clientWindow resolves a root child to the window the application actually
// owns. A reparenting window manager wraps each client in one or more frames;
// the client is the first window at or below w carrying WM_STATE. A subtree
// with no such window is its own answer.
func clientWindow(c *x11.Conn, w uint32, a atoms) uint32 {
	if got, ok := findClientWindow(c, w, a, 0); ok {
		return got
	}
	return w
}

// findClientWindow returns the first window at or below w that carries
// WM_STATE, and whether one was found.
func findClientWindow(c *x11.Conn, w uint32, a atoms, depth int) (uint32, bool) {
	if hasWMState(c, w, a) {
		return w, true
	}
	if depth >= maxClientWindowDepth {
		return 0, false
	}
	_, _, children, err := c.QueryTree(w)
	if err != nil {
		return 0, false
	}
	for _, ch := range children {
		if got, ok := findClientWindow(c, ch, a, depth+1); ok {
			return got, true
		}
	}
	return 0, false
}

// hasWMState reports whether a window carries WM_STATE, which is how a window
// manager marks the window it manages.
func hasWMState(c *x11.Conn, w uint32, a atoms) bool {
	p, err := c.GetProperty(w, a.wmState, x11.AtomAnyType, 1)
	return err == nil && p.Format != 0
}

// describeWindow reads everything the [Window] struct carries. It reports
// false for a window that is not capturable at all — an InputOnly window has
// no pixels, and a window that has gone away between the enumeration and the
// read is simply skipped.
func describeWindow(c *x11.Conn, id, root uint32, screen int, a atoms) (Window, bool) {
	attr, err := c.GetWindowAttributes(id)
	if err != nil || attr.Class == x11.WindowClassInputOnly {
		return Window{}, false
	}
	g, err := c.GetGeometry(id)
	if err != nil {
		return Window{}, false
	}
	x, y := g.X, g.Y
	if dx, dy, _, err := c.TranslateCoordinates(id, root, 0, 0); err == nil {
		x, y = dx, dy
	}
	w := Window{
		ID:       id,
		Frame:    Rect{X: float64(x), Y: float64(y), W: float64(g.Width), H: float64(g.Height)},
		OnScreen: attr.Viewable(),
		Screen:   screen,
		Root:     root,
	}
	if p, err := c.GetProperty(id, a.netWMName, a.utf8String, 1024); err == nil && p.Format == 8 {
		w.Title = p.Text()
	}
	if w.Title == "" {
		if p, err := c.GetProperty(id, x11.AtomWMName, x11.AtomString, 1024); err == nil {
			w.Title = p.Text()
		}
	}
	if p, err := c.GetProperty(id, x11.AtomWMClass, x11.AtomString, 256); err == nil && p.Format == 8 {
		w.BundleID, w.AppName = splitWMClass(p.Value)
	}
	if p, err := c.GetProperty(id, a.netWMPID, x11.AtomCardinal, 1); err == nil {
		if v := p.Uint32s(c.Order()); len(v) > 0 {
			w.PID = int32(v[0])
		}
	}
	w.Layer = windowLayer(c, id, a, attr)
	return w, true
}

// splitWMClass splits the two NUL-separated strings of a WM_CLASS property
// into its instance and class halves. A malformed value yields what is there
// rather than an error: WM_CLASS is advisory.
func splitWMClass(v []byte) (instance, class string) {
	i := 0
	for i < len(v) && v[i] != 0 {
		i++
	}
	instance = string(v[:i])
	if i >= len(v) {
		return instance, ""
	}
	rest := v[i+1:]
	j := 0
	for j < len(rest) && rest[j] != 0 {
		j++
	}
	return instance, string(rest[:j])
}

// Window layers. X11 has no layer number; these are derived from
// _NET_WM_WINDOW_TYPE and the override-redirect flag so a consumer can tell an
// application window from the panel and the desktop, which is what the macOS
// sibling's Layer field is for.
const (
	// LayerNormal is an ordinary application window.
	LayerNormal = 0
	// LayerDesktop is the desktop background window.
	LayerDesktop = -1
	// LayerDock is a panel, taskbar or dock.
	LayerDock = 1
	// LayerOverride is an override-redirect window: a menu, a tooltip, a
	// drag icon. The window manager never sees it.
	LayerOverride = 2
)

// windowLayer derives a layer number from _NET_WM_WINDOW_TYPE.
func windowLayer(c *x11.Conn, id uint32, a atoms, attr x11.WindowAttributes) int {
	if p, err := c.GetProperty(id, a.netWMWinTyp, x11.AtomAtom, 8); err == nil {
		for _, t := range p.Uint32s(c.Order()) {
			switch t {
			case a.typeDesktop:
				return LayerDesktop
			case a.typeDock:
				return LayerDock
			case a.typeNormal:
				return LayerNormal
			}
		}
	}
	if attr.OverrideRedirect {
		return LayerOverride
	}
	return LayerNormal
}

// Shareable returns everything this process may capture.
func Shareable(ctx context.Context) (*Content, error) {
	c, _, err := dial()
	if err != nil {
		return nil, err
	}
	defer func() { _ = c.Close() }()
	d, err := displaysOn(ctx, c)
	if err != nil {
		return nil, err
	}
	w, err := windowsOn(ctx, c)
	if err != nil {
		return nil, err
	}
	return &Content{Displays: d, Windows: w, Applications: applicationsOf(w)}, nil
}

// CurrentProcessShareable returns the content owned by the CALLING process:
// every display (X11 restricts nobody from reading the root window) and only
// this process's own windows.
//
// It exists because the macOS sibling has it, where it is the permission-free
// path that makes the package testable without a TCC grant. Here it is simply
// a filter — but it is the same filter, so a test written once runs on both.
func CurrentProcessShareable(ctx context.Context) (*Content, error) {
	all, err := Shareable(ctx)
	if err != nil {
		return nil, err
	}
	pid := int32(os.Getpid())
	all.Windows = all.WindowsOfPID(pid)
	all.Applications = applicationsOf(all.Windows)
	return all, nil
}

// applicationsOf collapses a window list into the processes owning them, in
// ascending PID order so the result is stable between calls.
func applicationsOf(ws []Window) []Application {
	seen := map[int32]Application{}
	for _, w := range ws {
		if w.PID == 0 {
			continue
		}
		a, ok := seen[w.PID]
		if !ok || (a.Name == "" && w.AppName != "") {
			seen[w.PID] = Application{PID: w.PID, Name: w.AppName, BundleID: w.BundleID}
		}
	}
	out := make([]Application, 0, len(seen))
	for _, a := range seen {
		out = append(out, a)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].PID < out[j].PID })
	return out
}
