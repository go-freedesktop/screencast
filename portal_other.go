// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package screencast

// PortalAvailable reports false: xdg-desktop-portal is a freedesktop.org
// thing, and there is none here. The constants naming it are still exported so
// a consumer's code compiles unchanged on every platform.
func PortalAvailable() (present bool, version uint32) { return false, 0 }
