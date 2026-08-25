// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package x11

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

// TestDialUnixOverARealSocket exercises the production transport — a real
// unix-domain socket with real SCM_RIGHTS descriptor passing — against a
// listener in this process. It is the one part of the package that cannot be
// covered on any other platform.
func TestDialUnixOverARealSocket(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()

	accepted := make(chan net.Conn, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			close(accepted)
			return
		}
		accepted <- c
	}()

	rw, err := dialUnix(path)
	if err != nil {
		t.Fatalf("dialUnix: %v", err)
	}
	defer func() { _ = rw.Close() }()

	srv := <-accepted
	if srv == nil {
		t.Fatal("the listener never accepted")
	}
	defer func() { _ = srv.Close() }()

	if _, err := rw.Write([]byte("hello")); err != nil {
		t.Fatalf("Write: %v", err)
	}
	buf := make([]byte, 5)
	if _, err := srv.Read(buf); err != nil {
		t.Fatalf("server Read: %v", err)
	}
	if string(buf) != "hello" {
		t.Errorf("server read %q", buf)
	}
	if _, err := srv.Write([]byte("world")); err != nil {
		t.Fatal(err)
	}
	if _, err := rw.Read(buf); err != nil {
		t.Fatalf("Read: %v", err)
	}
	if string(buf) != "world" {
		t.Errorf("client read %q", buf)
	}

	// The whole point of this transport: a descriptor travels with a request.
	fs, ok := rw.(FDSender)
	if !ok {
		t.Fatal("dialUnix returned a transport that cannot pass descriptors")
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = r.Close(); _ = w.Close() }()
	if err := fs.SendFD([]byte("fd!"), int(r.Fd())); err != nil {
		t.Fatalf("SendFD: %v", err)
	}
	// Read it back on the server side and prove a real descriptor arrived.
	usrv, ok := srv.(*net.UnixConn)
	if !ok {
		t.Fatal("the accepted connection is not a *net.UnixConn")
	}
	msg := make([]byte, 8)
	oob := make([]byte, syscall.CmsgSpace(4))
	n, oobn, _, _, err := usrv.ReadMsgUnix(msg, oob)
	if err != nil {
		t.Fatalf("ReadMsgUnix: %v", err)
	}
	if string(msg[:n]) != "fd!" {
		t.Errorf("message = %q", msg[:n])
	}
	scms, err := syscall.ParseSocketControlMessage(oob[:oobn])
	if err != nil || len(scms) != 1 {
		t.Fatalf("ParseSocketControlMessage = %v, %v", scms, err)
	}
	fds, err := syscall.ParseUnixRights(&scms[0])
	if err != nil || len(fds) != 1 {
		t.Fatalf("ParseUnixRights = %v, %v", fds, err)
	}
	_ = syscall.Close(fds[0])
}

func TestDialUnixToNowhere(t *testing.T) {
	_, err := dialUnix(filepath.Join(t.TempDir(), "not-there"))
	if err == nil || !strings.Contains(err.Error(), "dialing") {
		t.Fatalf("dialUnix reported %v, want a dial failure", err)
	}
	if !os.IsNotExist(errUnwrapAll(err)) {
		t.Errorf("the dial failure does not bottom out in a missing file: %v", err)
	}
}

// errUnwrapAll walks an error chain to its root, which is what the caller's
// "is this just a missing socket?" test does.
func errUnwrapAll(err error) error {
	for {
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return err
		}
		next := u.Unwrap()
		if next == nil {
			return err
		}
		err = next
	}
}
