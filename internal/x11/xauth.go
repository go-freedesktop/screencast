// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"os"
)

// AuthMITCookie is the MIT-MAGIC-COOKIE-1 authorization protocol name sent in
// the setup request when a matching cookie is found in the authority file.
const AuthMITCookie = "MIT-MAGIC-COOKIE-1"

// Xauthority address families (X11/Xauth.h). Lengths in the file are always
// big-endian, independent of the machine.
const (
	FamilyInternet  = 0
	FamilyLocal     = 256
	FamilyWild      = 65535
	FamilyInternet6 = 6
)

// AuthEntry is one record parsed from an Xauthority file.
type AuthEntry struct {
	Family  uint16
	Address []byte
	Number  string // display number as ASCII; "" is a wildcard
	Name    string // authorization protocol name
	Data    []byte // the cookie
}

// parseXauthority reads every entry from an Xauthority stream. The file is a
// flat sequence of records, each a big-endian-length-prefixed
// family/address/number/name/data quintuple.
func parseXauthority(r io.Reader) ([]AuthEntry, error) {
	var out []AuthEntry
	for {
		var fam uint16
		err := binary.Read(r, binary.BigEndian, &fam)
		if err == io.EOF {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("x11: xauth: reading family: %w", err)
		}
		addr, err := readAuthField(r)
		if err != nil {
			return nil, err
		}
		num, err := readAuthField(r)
		if err != nil {
			return nil, err
		}
		name, err := readAuthField(r)
		if err != nil {
			return nil, err
		}
		data, err := readAuthField(r)
		if err != nil {
			return nil, err
		}
		out = append(out, AuthEntry{
			Family:  fam,
			Address: addr,
			Number:  string(num),
			Name:    string(name),
			Data:    data,
		})
	}
}

// readAuthField reads one big-endian uint16 length then that many bytes.
func readAuthField(r io.Reader) ([]byte, error) {
	var n uint16
	if err := binary.Read(r, binary.BigEndian, &n); err != nil {
		return nil, fmt.Errorf("x11: xauth: reading field length: %w", err)
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, fmt.Errorf("x11: xauth: reading field body: %w", err)
	}
	return b, nil
}

// matchCookie selects the MIT-MAGIC-COOKIE-1 entry that fits (host, display).
// Preference, most specific first: a Local entry whose address equals host and
// whose number matches; then a Wild-family entry with a matching number; then
// any name-matching entry with a matching number. A blank Number in the file
// is a wildcard that matches any display.
func matchCookie(entries []AuthEntry, host, display string) (AuthEntry, bool) {
	numOK := func(e AuthEntry) bool { return e.Number == "" || e.Number == display }
	var wild, anyNum *AuthEntry
	for i := range entries {
		e := entries[i]
		if e.Name != AuthMITCookie || !numOK(e) {
			continue
		}
		switch {
		case e.Family == FamilyLocal && string(e.Address) == host:
			return e, true
		case e.Family == FamilyWild && wild == nil:
			wild = &entries[i]
		case anyNum == nil:
			anyNum = &entries[i]
		}
	}
	if wild != nil {
		return *wild, true
	}
	if anyNum != nil {
		return *anyNum, true
	}
	return AuthEntry{}, false
}

// LoadAuthCookie resolves the MIT-MAGIC-COOKIE-1 for (host, display) from the
// given authority file. A missing file, or no match, is not an error: it
// returns an empty name and data so the caller falls back to an
// unauthenticated setup, exactly as Xlib does. host defaults to the machine
// hostname when empty.
func LoadAuthCookie(authFile, host, display string) (name string, data []byte, err error) {
	if authFile == "" {
		return "", nil, nil
	}
	f, err := openFile(authFile)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil, nil
		}
		return "", nil, err
	}
	defer func() { _ = f.Close() }()
	entries, err := parseXauthority(f)
	if err != nil {
		return "", nil, err
	}
	if host == "" {
		host = hostname()
	}
	e, ok := matchCookie(entries, host, display)
	if !ok {
		return "", nil, nil
	}
	return e.Name, e.Data, nil
}

// openFile and hostname sit behind package variables so the tests can drive
// the (rare) failure paths without needing a real unreadable file.
var (
	openFile = func(name string) (io.ReadCloser, error) { return os.Open(name) }
	hostname = func() string {
		h, _ := os.Hostname()
		return h
	}
)

// AuthFilePath returns the authority file to consult: $XAUTHORITY if set,
// otherwise $HOME/.Xauthority, otherwise "".
func AuthFilePath() string {
	if p := os.Getenv("XAUTHORITY"); p != "" {
		return p
	}
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		return home + "/.Xauthority"
	}
	return ""
}

// encodeXauthEntry serializes an AuthEntry back to the file format. It is the
// inverse of the parser and exists so tests can build fixtures and round-trip
// them.
func encodeXauthEntry(e AuthEntry) []byte {
	var b bytes.Buffer
	put := func(v uint16) { _ = binary.Write(&b, binary.BigEndian, v) }
	field := func(p []byte) {
		put(uint16(len(p)))
		b.Write(p)
	}
	put(e.Family)
	field(e.Address)
	field([]byte(e.Number))
	field([]byte(e.Name))
	field(e.Data)
	return b.Bytes()
}
