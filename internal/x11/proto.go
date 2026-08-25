// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

// Core request opcodes used by this package (X11 protocol, appendix B). Only
// the read-side requests a capture needs are here; this client never draws.
const (
	opGetWindowAttributes = 3
	opGetGeometry         = 14
	opQueryTree           = 15
	opInternAtom          = 16
	opGetAtomName         = 17
	opGetProperty         = 20
	opTranslateCoordinate = 40
	opQueryPointer        = 38
	opQueryExtension      = 98
	opGetImage            = 73
)

// GetImage image formats. ZPixmap is the packed, one-run-of-bytes layout a
// capture wants; XYPixmap is bitplane-per-plane and is never requested here.
const (
	imageFormatXYPixmap = 1
	imageFormatZPixmap  = 2
)

// AllPlanes is the plane-mask that asks for every bit of every plane.
const AllPlanes = 0xffffffff

// Window map states, as reported by GetWindowAttributes.
const (
	MapStateUnmapped   = 0
	MapStateUnviewable = 1
	MapStateViewable   = 2
)

// Window classes, as reported by GetWindowAttributes.
const (
	WindowClassInputOutput = 1
	WindowClassInputOnly   = 2
)

// Predefined atoms (X11/Xatom.h) this package reads.
const (
	AtomNone     = 0
	AtomAtom     = 4
	AtomCardinal = 6
	AtomWindow   = 33
	AtomString   = 31
	AtomWMName   = 39
	AtomWMClass  = 67
	AtomAnyType  = 0
)

// Reply/error stream discriminators (first byte of a 32-byte packet). Any
// other value is an event, which this client buffers and ignores: it selects
// no event mask, so the only events it can see are unsolicited ones from
// extensions it did not enable.
const (
	pktError = 0
	pktReply = 1
)

// X11 core error codes (X11 protocol, section "Errors"). They are mapped to
// package-level sentinels by [XError.Kind].
const (
	ErrCodeRequest        = 1
	ErrCodeValue          = 2
	ErrCodeWindow         = 3
	ErrCodePixmap         = 4
	ErrCodeAtom           = 5
	ErrCodeCursor         = 6
	ErrCodeFont           = 7
	ErrCodeMatch          = 8
	ErrCodeDrawable       = 9
	ErrCodeAccess         = 10
	ErrCodeAlloc          = 11
	ErrCodeColormap       = 12
	ErrCodeGContext       = 13
	ErrCodeIDChoice       = 14
	ErrCodeName           = 15
	ErrCodeLength         = 16
	ErrCodeImplementation = 17
)

// errorNames maps a core error code to its protocol name, so a failure reads
// as "BadDrawable" rather than as a bare 9.
var errorNames = map[byte]string{
	ErrCodeRequest:        "BadRequest",
	ErrCodeValue:          "BadValue",
	ErrCodeWindow:         "BadWindow",
	ErrCodePixmap:         "BadPixmap",
	ErrCodeAtom:           "BadAtom",
	ErrCodeCursor:         "BadCursor",
	ErrCodeFont:           "BadFont",
	ErrCodeMatch:          "BadMatch",
	ErrCodeDrawable:       "BadDrawable",
	ErrCodeAccess:         "BadAccess",
	ErrCodeAlloc:          "BadAlloc",
	ErrCodeColormap:       "BadColor",
	ErrCodeGContext:       "BadGC",
	ErrCodeIDChoice:       "BadIDChoice",
	ErrCodeName:           "BadName",
	ErrCodeLength:         "BadLength",
	ErrCodeImplementation: "BadImplementation",
}

// ErrorName returns the protocol name of a core error code, or "" for a code
// belonging to an extension.
func ErrorName(code byte) string { return errorNames[code] }
