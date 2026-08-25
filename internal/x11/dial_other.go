// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package x11

import (
	"errors"
	"io"
)

// ErrNoTransport is reported off Linux. The whole wire codec above this line
// is portable and is fully exercised on every platform against an in-process
// scripted server; what is missing here is only the socket and the shared
// memory, which is what makes this package Linux-only in practice.
var ErrNoTransport = errors.New("x11: dialing an X server is only implemented on Linux")

// dialUnix reports ErrNoTransport.
func dialUnix(path string) (io.ReadWriteCloser, error) { return nil, ErrNoTransport }
