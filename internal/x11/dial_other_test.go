// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package x11

import (
	"errors"
	"testing"
)

func TestDialUnixIsLinuxOnly(t *testing.T) {
	if _, err := dialUnix("/tmp/.X11-unix/X0"); !errors.Is(err, ErrNoTransport) {
		t.Fatalf("dialUnix reported %v, want ErrNoTransport", err)
	}
}

func TestSharedMemoryIsLinuxOnly(t *testing.T) {
	if _, err := createAnonFile(4096); !errors.Is(err, ErrNoSharedMemory) {
		t.Fatalf("createAnonFile reported %v, want ErrNoSharedMemory", err)
	}
	if _, err := mmapRegion(3, 4096); !errors.Is(err, ErrNoSharedMemory) {
		t.Fatalf("mmapRegion reported %v, want ErrNoSharedMemory", err)
	}
	if err := munmapRegion(nil); err != nil {
		t.Fatalf("munmapRegion reported %v", err)
	}
	if err := closeFD(3); err != nil {
		t.Fatalf("closeFD reported %v", err)
	}
}
