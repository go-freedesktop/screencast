// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build linux

package x11

import (
	"fmt"
	"io"
	"net"
	"syscall"
)

// unixRW adapts a *net.UnixConn to the Conn transport, adding descriptor
// passing over SCM_RIGHTS. It is what lets the connection hand the X server a
// shared-memory descriptor with MIT-SHM AttachFd while every other request
// travels as an ordinary socket write.
type unixRW struct {
	c *net.UnixConn
}

// WrapUnix wraps a dialed *net.UnixConn as an fd-passing transport. A
// connection built over it reports SupportsFDPassing() == true.
func WrapUnix(c *net.UnixConn) io.ReadWriteCloser { return &unixRW{c: c} }

func (u *unixRW) Read(b []byte) (int, error)  { return u.c.Read(b) }
func (u *unixRW) Write(b []byte) (int, error) { return u.c.Write(b) }
func (u *unixRW) Close() error                { return u.c.Close() }

// SendFD writes msg with fd attached as a single SCM_RIGHTS control message.
func (u *unixRW) SendFD(msg []byte, fd int) error {
	oob := syscall.UnixRights(fd)
	_, _, err := u.c.WriteMsgUnix(msg, oob, nil)
	return err
}

// dialUnix connects to the X server's unix-domain socket.
func dialUnix(path string) (io.ReadWriteCloser, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("x11: dialing %s: %w", path, err)
	}
	return WrapUnix(c), nil
}
