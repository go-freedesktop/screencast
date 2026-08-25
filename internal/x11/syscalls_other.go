// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package x11

import "errors"

// ErrNoSharedMemory is reported off Linux, where this package never dials an X
// server and so never needs a shared segment. The wire codec above it is fully
// portable and fully tested here; only the shared memory and the socket are
// not.
var ErrNoSharedMemory = errors.New("x11: shared memory segments are only implemented on Linux")

// The shared-memory primitives are package variables off Linux too, so a test
// can substitute a plain heap-backed "segment" and exercise the MIT-SHM
// request encoding and the segment lifecycle on any platform.
var (
	createAnonFile = func(size int) (int, error) { return -1, ErrNoSharedMemory }
	mmapRegion     = func(fd, size int) ([]byte, error) { return nil, ErrNoSharedMemory }
	munmapRegion   = func(b []byte) error { return nil }
	closeFD        = func(fd int) error { return nil }
)
