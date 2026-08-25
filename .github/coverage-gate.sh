#!/usr/bin/env bash
# Copyright (c) the go-freedesktop/screencast authors.
# SPDX-License-Identifier: BSD-3-Clause
#
# The coverage gate.
#
# It is on the PORTABLE files, not on the total. The portable set is everything
# that does not need a display server: the public types and their arithmetic,
# the stream machinery, the resampler and the cursor blend, the session
# classification, the non-Linux stubs, and the ENTIRE X11 wire codec in
# internal/x11 — which is portable precisely so that a protocol bug is caught
# on macOS and on Windows as well as on Linux.
#
# What is deliberately NOT gated: the code that drives a real X server
# (screencast_linux.go, capture_linux.go, portal_linux.go, cmd/sccheck's
# capture loop). Its error paths need a server that misbehaves on demand, and a
# gate there would either be a lie or force the tests to fake the very thing
# they exist to prove. The Linux lane runs it against a real Xvfb and prints
# what it reached.
set -euo pipefail

profile="${1:?usage: coverage-gate.sh <coverage profile>}"

portable=(
  '/screencast.go:'
  '/stream.go:'
  '/scale.go:'
  '/session.go:'
  '/screencast_other.go:'
  '/portal_other.go:'
  '/internal/x11/'
)

status=0
for file in "${portable[@]}"; do
  lines=$(go tool cover -func="$profile" | grep -F "$file" || true)
  if [ -z "$lines" ]; then
    # A file that does not build on this platform contributes nothing, which
    # is expected for the stubs on Linux and for the Linux files elsewhere.
    echo "skip  $file (not built on this platform)"
    continue
  fi
  below=$(echo "$lines" | grep -v '100.0%' || true)
  if [ -n "$below" ]; then
    echo "::error::$file is below 100%:"
    echo "$below"
    status=1
  else
    echo "ok    $file 100%"
  fi
done

echo
echo "whole-package coverage:"
go tool cover -func="$profile" | tail -1
exit "$status"
