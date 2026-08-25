// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package screencast

import "context"

// On every platform that is not Linux there is no X server to talk to, so the
// entry points below report [ErrUnsupported] rather than failing to build. A
// consumer cross-compiles this package without having to think about it, and
// gets one clear error at run time instead of a link failure at build time.
//
// Everything portable — the option validation, the stride arithmetic, the
// BGRA-to-RGBA conversion, the resampler, the cursor blend, the whole stream
// machinery and the entire X11 wire codec in internal/x11 — is exercised HERE
// as well as on Linux, which is what lets the darwin and windows lanes in CI
// cover it.
//
// The sibling for macOS is github.com/go-macos/screencapture, which presents
// deliberately the same shape over ScreenCaptureKit.

// Backend names the capture route in use; there is none here.
const Backend = "none"

// Available reports false: this package captures through X11, and there is no
// X11 here.
func Available() bool { return false }

// Probe reports [ErrUnsupported].
func Probe() error { return ErrUnsupported }

// Authorized reports false.
func Authorized() bool { return false }

// RequestAuthorization reports false and prompts nothing.
func RequestAuthorization() bool { return false }

// Displays reports [ErrUnsupported].
func Displays(ctx context.Context) ([]Display, error) { return nil, ErrUnsupported }

// Windows reports [ErrUnsupported].
func Windows(ctx context.Context) ([]Window, error) { return nil, ErrUnsupported }

// Shareable reports [ErrUnsupported].
func Shareable(ctx context.Context) (*Content, error) { return nil, ErrUnsupported }

// CurrentProcessShareable reports [ErrUnsupported].
func CurrentProcessShareable(ctx context.Context) (*Content, error) { return nil, ErrUnsupported }

// Diagnose reports [ErrUnsupported]: there is no session to diagnose.
func Diagnose() error { return ErrUnsupported }

// CaptureDisplay reports [ErrUnsupported]. It still validates the options
// first, so a consumer's option bug surfaces identically on every platform.
func CaptureDisplay(ctx context.Context, d Display, opt Options) (*Stream, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}

// CaptureWindow reports [ErrUnsupported], after the same option validation as
// [CaptureDisplay].
func CaptureWindow(ctx context.Context, w Window, opt Options) (*Stream, error) {
	if err := opt.Validate(); err != nil {
		return nil, err
	}
	return nil, ErrUnsupported
}
