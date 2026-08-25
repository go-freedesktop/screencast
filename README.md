# go-freedesktop/screencast

[![ci](https://github.com/go-freedesktop/screencast/actions/workflows/ci.yml/badge.svg)](https://github.com/go-freedesktop/screencast/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/go-freedesktop/screencast.svg)](https://pkg.go.dev/github.com/go-freedesktop/screencast)
[![Go Report Card](https://goreportcard.com/badge/github.com/go-freedesktop/screencast)](https://goreportcard.com/report/github.com/go-freedesktop/screencast)
[![License](https://img.shields.io/badge/license-BSD--3--Clause-blue)](LICENSE)
[![Go](https://img.shields.io/badge/go-1.26.4%2B-00ADD8)](https://go.dev/dl/)
[![Coverage](https://img.shields.io/badge/coverage-100%25%20portable%20layer-1a7f37)](#testing)

Capture the pixels of displays and windows **on Linux**, in pure Go, with
`CGO_ENABLED=0`, without shelling out to anything.

No Xlib. No XCB. No cgo. No `exec.Command` to `xwd`, `import`, `grim` or
`ffmpeg`. This package speaks the X11 wire protocol itself, over the display's
unix socket, and lets the X server write captured frames straight into shared
memory it handed over with `SCM_RIGHTS`.

It is the Linux sibling of [`go-macos/screencapture`](https://github.com/go-macos/screencapture)
and presents deliberately the same shape, so one consumer can drive both
platforms through near-identical adapters.

```go
ctx := context.Background()

content, err := screencast.Shareable(ctx)
d, err := content.MainDisplay()

s, err := screencast.CaptureDisplay(ctx, d, screencast.Options{FPS: 60})
defer s.Close()

for {
    f, fresh := s.Frame()      // borrowed BGRA bytes; no allocation
    if fresh {
        upload(f.Pix, f.Width, f.Height, f.Stride)   // index with Stride
    }
}
```

## What it does

| | |
|---|---|
| Enumerate displays | one per RANDR 1.5 monitor, per XINERAMA screen, or per X screen |
| Enumerate windows | `_NET_CLIENT_LIST`, or a walk of the window tree with `WM_STATE` resolution |
| Capture a display | MIT-SHM `ShmGetImage`, falling back to core `GetImage` |
| Capture a window | same, on the window drawable |
| Pixel format | **BGRA**, always, converted from whatever visual the server has |
| Cursor | composited from XFIXES `GetCursorImage`, on request |
| Resampling | nearest-neighbour on the CPU, optional letterboxing |
| Other platforms | compile and report `ErrUnsupported` |

## Two contract details

**Stride is carried, never assumed.** `Frame.Stride` is the number of BYTES per
row and is not necessarily `Width*4`: X11 pads every scanline to the pixmap
format's boundary. Index with `Stride`, or use `Frame.Row(y)`. A consumer that
assumes `Width*4` shears the picture progressively down the screen.

**`Frame()` does not allocate.** It hands back a borrowed view of the capture
buffer — for MIT-SHM, the shared segment the X server itself wrote into.
Measured at **35 ns/op, 0 allocs/op**. The consumer's whole frame budget is
16.6 ms.

## Measured

On a Debian 13 cloud VM (32 vCPU, no GPU), against `Xvfb`, `CGO_ENABLED=0`:

| capture | transport | ms/frame | fps |
|---|---|---|---|
| 1280×800, depth 24 | MIT-SHM | 2.50 | 400 |
| 1280×800, depth 24 | GetImage | 3.76 | 266 |
| 800×600, depth 16 (converted) | MIT-SHM | 4.61 | 217 |
| 800×600, depth 30 (converted) | MIT-SHM | 5.30 | 189 |
| 3840×2160, depth 24 | MIT-SHM | 24.3 | 41 |
| 3840×2160, depth 24, `RawAlpha` | MIT-SHM | **5.81** | **172** |

`Stream.Frame()`: 35 ns/op, **0 allocs/op**, at every size.

That last pair is worth reading twice. On the common path the captured bytes
are ALREADY BGRA, so the only per-frame work the capture does is forcing the
fourth byte opaque — and at 4K that one pass costs 18 of the 24 milliseconds.
A consumer that ignores alpha, or sets it itself on the GPU, sets
`Options.RawAlpha` and gets it back.

## Proof, not assertion

"The frame is not all zeroes" is a weak claim: a capture that read the wrong
drawable, swapped red and blue, or sheared every row would pass it. A static
grey buffer is the classic silent failure of a screen capture.

So the integration suite and `sccheck -selftest` **paint**: they cover the
display with an override-redirect window, fill it with a colour built from the
visual's OWN channel masks, and then require every captured pixel to be exactly
that colour, in that byte order, at the declared stride — and then require the
next colour to be seen.

```
$ DISPLAY=:99 sccheck -selftest
self-test: painting root 0x21f, visual 0x21 masks r=0xff0000 g=0xff00 b=0xff
self-test red   (pixel 0x00ff0000): all 1024000 pixels are 00 00 ff ff, stride 5120, seq 9
self-test green (pixel 0x0000ff00): all 1024000 pixels are 00 ff 00 ff, stride 5120, seq 12
self-test blue  (pixel 0x000000ff): all 1024000 pixels are ff 00 00 ff, stride 5120, seq 15
self-test white (pixel 0x00ffffff): all 1024000 pixels are ff ff ff ff, stride 5120, seq 18
self-test black (pixel 0x00000000): all 1024000 pixels are 00 00 00 ff, stride 5120, seq 21
self-test: PASS
```

That passes identically on a 5-6-5 visual, an 8-8-8 one and a 10-10-10 one,
because a full-scale channel widens to 255 whatever its bit width.

A frame captured this way is committed at
[`testdata/xvfb-1280x800-capture.png`](testdata/xvfb-1280x800-capture.png).

## Backends

**X11 — works.** The whole of it: MIT-SHM 1.2 with `AttachFd` descriptor
passing, core `GetImage` fallback, MIT-MAGIC-COOKIE-1 authorization, RANDR 1.5
and XINERAMA monitor enumeration, XFIXES cursor, every TrueColor and
DirectColor visual at 16, 24 and 32 bits per pixel.

The wire codec, the `.Xauthority` parser, the setup exchange, the shared-memory
segment and the monitor enumeration are not owned here: they are
[`go-freedesktop/x11`](https://github.com/go-freedesktop/x11), shared with
[`go-widgets/window`](https://github.com/go-widgets/window). Which display is
where is not a capture question — a toolkit putting a window full-screen on a
chosen panel needs the identical answer — and two copies of a protocol parser
drift silently until something fails on one back-end only.

**Wayland — not implemented.** A wlroots compositor exposing
`wlr-screencopy-unstable-v1` could be driven the same way. It is not done here.
Under Wayland, capture works only through Xwayland.

**xdg-desktop-portal — deliberately not started.** On GNOME and KDE the
sanctioned route is `org.freedesktop.portal.ScreenCast`, which negotiates over
D-Bus and then hands back a **PipeWire** stream. A pure-Go PipeWire client is a
large piece of work. Rather than half-build it, this package DETECTS that
situation and says so:

```go
if err := screencast.Diagnose(); errors.Is(err, screencast.ErrPortalPipeWire) {
    // "this session can only be captured through xdg-desktop-portal's
    //  org.freedesktop.portal.ScreenCast, which delivers frames over PipeWire;
    //  this package implements no PipeWire client. ..."
}
```

`Diagnose` consults the session bus to say whether the portal is actually
there — it only LOOKS, and never opens a session, prompts anyone or starts a
stream.

## Differences from the macOS sibling

The API is the same shape. Where Linux genuinely differs:

- **FPS is a polling RATE, not a ceiling.** X11 `GetImage` is pull-based: the
  loop asks for a frame every tick and gets one whether or not anything
  changed. ScreenCaptureKit is change-driven and delivers nothing while the
  screen is still. A consumer written against the macOS `fresh` flag still
  works; it just sees `fresh` on every tick.
- **`Options.Width`/`Height` resample on the CPU.** X11 has no server-side
  scaler, so any size other than native costs a pass over the pixels.
- **Rect coordinates are in PIXELS.** X11 has no points.
  `Display.PixelWidth == Display.Width` for the same reason; both pairs are
  kept so a consumer's field access is identical on both platforms.
- **`Authorized`/`RequestAuthorization` are not a permission dance.** X11 has
  no per-application capture grant: if the connection authenticates, capture is
  allowed. They report whether a usable connection can be opened. `Probe`
  reports WHY when the answer is no.
- **`Options.ExcludeWindows` is rejected**, not ignored: the X server cannot
  capture a screen minus a window.
- **New:** `Options.RawAlpha`, `Options.ForceGetImage`, `Stream.Transport()`,
  `Stream.Converts()`, `Probe()`, `Diagnose()`, `Session()`,
  `PortalAvailable()`.

## Two X11 facts a window capture cannot hide

- A window that is not VIEWABLE has no pixels. X11 keeps no offscreen copy of a
  minimised window unless a compositing manager has redirected it. Capturing
  one reports `ErrNotFound` rather than a stale or black frame.
- Without a compositing manager, reading a window's pixels reads the
  FRAMEBUFFER where that window is, so anything overlapping it is in the
  capture. Under a compositing manager — which is every modern desktop — each
  window is redirected to its own offscreen pixmap and the capture is clean.

## The probe

```
go run ./cmd/sccheck                 # enumerate, capture, measure, report
go run ./cmd/sccheck -list           # enumerate only
go run ./cmd/sccheck -selftest       # paint known colours and verify them
go run ./cmd/sccheck -png frame.png  # save a captured frame
go run ./cmd/sccheck -slow           # force the core GetImage path
go run ./cmd/sccheck -raw-alpha      # skip the opaque-alpha pass
```

## Tested against real hardware

Or rather: mostly not against hardware, and that is worth saying outright. A
capture path that has only ever run against a software framebuffer can be wrong
in ways nothing here would report.

### What was actually run

| Where | What was done |
|---|---|
| Debian 13 cloud VM, 32 vCPU, **no GPU**, X server = `Xvfb` | every figure in **Measured**, and every self-test transcript in **Proof, not assertion** |
| Depths 16, 24 and 30; TrueColor and DirectColor visuals | the painted self-test passes identically on all of them, because a full-scale channel widens to 255 whatever its bit width |
| MIT-SHM 1.2 with `AttachFd`, and the core `GetImage` fallback | both exercised, and compared against each other |
| Every other platform | the whole wire codec runs in-process over a `net.Pipe` against a scripted server, so a protocol bug fails the macOS and Windows lanes too |

### Not proven

- **No physical GPU, and no physical X server.** `Xvfb` renders into ordinary
  memory. A real driver's read-back cost is the one number here that a software
  framebuffer cannot stand in for, and the 4K figures should be read as the
  cost of this package's own work, not of the whole path on a real display.
- **No physical monitor, so no real RANDR 1.5 layout.** Monitor enumeration is
  exercised against a scripted server and against `Xvfb`'s single screen; a
  multi-head desktop, a rotated panel or a mixed-DPI arrangement has not been
  seen.
- **Wayland is not implemented at all**, and is stated as such above rather
  than left to be discovered. Under Wayland this captures only through
  Xwayland.
- **xdg-desktop-portal / PipeWire is detected, never driven.** `Diagnose` only
  looks; it never opens a session.

### Send us hardware

A machine with a real GPU, a multi-head RANDR 1.5 desktop, or a rotated panel
would each turn one of the lines above into a measurement. If you want one
settled, **send us the hardware** and what it shows will be listed here. Until
then, an unverified line says so.

## Testing

```
go test ./...                        # everything portable, on any platform
go test -race ./...

Xvfb :99 -screen 0 1280x800x24 -ac &
DISPLAY=:99 SCREENCAST_LIVE=1 go test -tags integration -run Live -v ./...
```

The X11 wire codec is transport-agnostic: `internal/x11.Handshake` wraps any
`io.ReadWriteCloser`, so the whole request/reply/error machine runs in-process
over a `net.Pipe` against a scripted fake server. That is why a protocol bug is
caught on macOS and on Windows as well as on Linux, and why `internal/x11`
reaches **100% coverage on every platform**.

CI gates 100% coverage on the portable layer — the public types and their
arithmetic, the stream machinery, the resampler, the cursor blend, the stubs
and the entire wire codec. It does not gate the code that drives a real X
server; that runs against a real `Xvfb` in the Linux lane, and the lane prints
what it reached.

## Licence

BSD-3-Clause.
