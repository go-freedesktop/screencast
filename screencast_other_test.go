// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

//go:build !linux

package screencast

import (
	"context"
	"errors"
	"testing"
)

// Off Linux there is no X server, so every entry point reports ErrUnsupported
// rather than failing to build. That is the whole contract of the stub, and it
// is what lets a consumer cross-compile without thinking about it.

func TestStubsReportUnsupported(t *testing.T) {
	ctx := context.Background()
	if Available() || Authorized() || RequestAuthorization() {
		t.Error("a stub reported that capture is possible")
	}
	if Backend != "none" {
		t.Errorf("Backend = %q, want \"none\"", Backend)
	}
	if !errors.Is(Diagnose(), ErrUnsupported) {
		t.Errorf("Diagnose() = %v", Diagnose())
	}
	if !errors.Is(Probe(), ErrUnsupported) {
		t.Errorf("Probe() = %v", Probe())
	}
	if present, version := PortalAvailable(); present || version != 0 {
		t.Errorf("PortalAvailable() = %v, %d", present, version)
	}
	for name, fn := range map[string]func() error{
		"Displays":                func() error { _, err := Displays(ctx); return err },
		"Windows":                 func() error { _, err := Windows(ctx); return err },
		"Shareable":               func() error { _, err := Shareable(ctx); return err },
		"CurrentProcessShareable": func() error { _, err := CurrentProcessShareable(ctx); return err },
		"CaptureDisplay":          func() error { _, err := CaptureDisplay(ctx, Display{}, Options{}); return err },
		"CaptureWindow":           func() error { _, err := CaptureWindow(ctx, Window{}, Options{}); return err },
	} {
		if err := fn(); !errors.Is(err, ErrUnsupported) {
			t.Errorf("%s reported %v, want ErrUnsupported", name, err)
		}
	}
}

func TestStubsValidateOptionsFirst(t *testing.T) {
	// A consumer's option bug must surface identically on every platform, so
	// the stubs validate before they refuse.
	ctx := context.Background()
	bad := Options{FPS: -1}
	if _, err := CaptureDisplay(ctx, Display{}, bad); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("CaptureDisplay reported %v, want ErrInvalidOption", err)
	}
	if _, err := CaptureWindow(ctx, Window{}, bad); !errors.Is(err, ErrInvalidOption) {
		t.Errorf("CaptureWindow reported %v, want ErrInvalidOption", err)
	}
}
