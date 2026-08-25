// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package x11

import (
	"errors"
	"strings"
	"syscall"
	"testing"
)

func TestCreateAnonFileMakesRealSharedMemory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	fd, err := createAnonFile(8192)
	if err != nil {
		t.Fatalf("createAnonFile: %v", err)
	}
	defer func() { _ = syscall.Close(fd) }()

	var st syscall.Stat_t
	if err := syscall.Fstat(fd, &st); err != nil {
		t.Fatalf("Fstat: %v", err)
	}
	if st.Size != 8192 {
		t.Errorf("the anonymous file is %d bytes, want 8192", st.Size)
	}
	if st.Nlink != 0 {
		t.Errorf("the backing file has %d links; it should have been unlinked immediately", st.Nlink)
	}

	// It must actually be mappable and shared: a write through the mapping is
	// visible through a second mapping of the same descriptor, which is the
	// property the X server relies on.
	a, err := mmapRegion(fd, 8192)
	if err != nil {
		t.Fatalf("mmapRegion: %v", err)
	}
	b, err := mmapRegion(fd, 8192)
	if err != nil {
		t.Fatalf("second mmapRegion: %v", err)
	}
	a[0], a[8191] = 0xab, 0xcd
	if b[0] != 0xab || b[8191] != 0xcd {
		t.Errorf("the mapping is not shared: b[0]=%#x b[8191]=%#x", b[0], b[8191])
	}
	if err := munmapRegion(a); err != nil {
		t.Errorf("munmapRegion: %v", err)
	}
	if err := munmapRegion(b); err != nil {
		t.Errorf("munmapRegion: %v", err)
	}
}

func TestCreateAnonFileFallsBackToTempDir(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", "")
	fd, err := createAnonFile(4096)
	if err != nil {
		t.Fatalf("createAnonFile with no XDG_RUNTIME_DIR: %v", err)
	}
	_ = closeFD(fd)
}

func TestCreateAnonFileRejectsAnEmptySize(t *testing.T) {
	for _, n := range []int{0, -1} {
		if _, err := createAnonFile(n); err == nil || !strings.Contains(err.Error(), "must be positive") {
			t.Errorf("createAnonFile(%d) reported %v", n, err)
		}
	}
}

func TestCreateAnonFileSyscallFailures(t *testing.T) {
	origOpen, origTrunc := shmOpen, ftruncateFD
	t.Cleanup(func() { shmOpen, ftruncateFD = origOpen, origTrunc })

	shmOpen = func(string, int, uint32) (int, error) { return -1, errors.New("no space") }
	if _, err := createAnonFile(4096); err == nil || !strings.Contains(err.Error(), "shm open") {
		t.Errorf("createAnonFile reported %v, want an open failure", err)
	}

	shmOpen = origOpen
	ftruncateFD = func(int, int64) error { return errors.New("cannot grow") }
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	if _, err := createAnonFile(4096); err == nil || !strings.Contains(err.Error(), "ftruncate") {
		t.Errorf("createAnonFile reported %v, want an ftruncate failure", err)
	}
}

func TestSegmentOverRealSharedMemory(t *testing.T) {
	t.Setenv("XDG_RUNTIME_DIR", t.TempDir())
	seg, err := NewSegment(0x200001, 4096)
	if err != nil {
		t.Fatalf("NewSegment: %v", err)
	}
	if len(seg.Data) != 4096 || seg.FD < 0 {
		t.Fatalf("Segment = %+v", seg)
	}
	seg.Data[42] = 7
	if err := seg.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := seg.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}
