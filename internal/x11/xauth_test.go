// Copyright (c) the go-freedesktop/screencast authors.
// SPDX-License-Identifier: BSD-3-Clause

package x11

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

// cookie is a plausible 16-byte MIT-MAGIC-COOKIE-1.
var cookie = []byte("0123456789abcdef")

func TestXauthRoundTrip(t *testing.T) {
	want := []AuthEntry{
		{Family: FamilyLocal, Address: []byte("thehost"), Number: "0", Name: AuthMITCookie, Data: cookie},
		{Family: FamilyWild, Address: nil, Number: "", Name: AuthMITCookie, Data: cookie},
		{Family: FamilyInternet, Address: []byte{192, 168, 1, 1}, Number: "1", Name: "XDM-AUTHORIZATION-1", Data: []byte("x")},
	}
	var buf bytes.Buffer
	for _, e := range want {
		buf.Write(encodeXauthEntry(e))
	}
	got, err := parseXauthority(&buf)
	if err != nil {
		t.Fatalf("parseXauthority: %v", err)
	}
	if len(got) != len(want) {
		t.Fatalf("parsed %d entries, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i].Family != want[i].Family || got[i].Number != want[i].Number ||
			got[i].Name != want[i].Name || !bytes.Equal(got[i].Data, want[i].Data) {
			t.Errorf("entry %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

func TestParseXauthorityTruncated(t *testing.T) {
	full := encodeXauthEntry(AuthEntry{Family: FamilyLocal, Address: []byte("h"),
		Number: "0", Name: AuthMITCookie, Data: cookie})
	// Every truncation past the first field must be an error, and never a
	// panic: an Xauthority file half-written by a crashing session manager is
	// a real thing.
	for n := 1; n < len(full); n++ {
		if _, err := parseXauthority(bytes.NewReader(full[:n])); err == nil {
			t.Errorf("parseXauthority accepted a %d-byte prefix of a %d-byte record", n, len(full))
		}
	}
	if got, err := parseXauthority(bytes.NewReader(nil)); err != nil || got != nil {
		t.Errorf("empty file gave %v, %v; want no entries and no error", got, err)
	}
}

func TestMatchCookiePrefersMostSpecific(t *testing.T) {
	local := AuthEntry{Family: FamilyLocal, Address: []byte("thehost"), Number: "0", Name: AuthMITCookie, Data: []byte("local")}
	wild := AuthEntry{Family: FamilyWild, Number: "0", Name: AuthMITCookie, Data: []byte("wild")}
	inet := AuthEntry{Family: FamilyInternet, Address: []byte{1, 2, 3, 4}, Number: "0", Name: AuthMITCookie, Data: []byte("inet")}
	other := AuthEntry{Family: FamilyLocal, Address: []byte("elsewhere"), Number: "0", Name: "XDM-AUTHORIZATION-1", Data: []byte("no")}

	for _, tc := range []struct {
		name    string
		entries []AuthEntry
		want    string
		ok      bool
	}{
		{"local wins", []AuthEntry{inet, wild, local}, "local", true},
		{"wild beats any", []AuthEntry{inet, wild}, "wild", true},
		{"any is the last resort", []AuthEntry{inet}, "inet", true},
		{"wrong protocol never matches", []AuthEntry{other}, "", false},
		{"nothing", nil, "", false},
	} {
		got, ok := matchCookie(tc.entries, "thehost", "0")
		if ok != tc.ok || string(got.Data) != tc.want {
			t.Errorf("%s: matchCookie = %q, %v; want %q, %v", tc.name, got.Data, ok, tc.want, tc.ok)
		}
	}

	// A blank display number in the file is a wildcard; a different one is not.
	blank := AuthEntry{Family: FamilyWild, Number: "", Name: AuthMITCookie, Data: []byte("blank")}
	if got, ok := matchCookie([]AuthEntry{blank}, "thehost", "7"); !ok || string(got.Data) != "blank" {
		t.Errorf("blank display number did not act as a wildcard: %q, %v", got.Data, ok)
	}
	mismatch := AuthEntry{Family: FamilyWild, Number: "3", Name: AuthMITCookie, Data: []byte("three")}
	if _, ok := matchCookie([]AuthEntry{mismatch}, "thehost", "7"); ok {
		t.Error("a record for display 3 matched display 7")
	}
}

func TestLoadAuthCookie(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "Xauthority")
	body := append(encodeXauthEntry(AuthEntry{Family: FamilyLocal, Address: []byte("thehost"),
		Number: "0", Name: AuthMITCookie, Data: cookie}),
		encodeXauthEntry(AuthEntry{Family: FamilyWild, Number: "1", Name: AuthMITCookie,
			Data: []byte("wildcookie")})...)
	if err := os.WriteFile(path, body, 0o600); err != nil {
		t.Fatal(err)
	}

	name, data, err := LoadAuthCookie(path, "thehost", "0")
	if err != nil || name != AuthMITCookie || !bytes.Equal(data, cookie) {
		t.Errorf("LoadAuthCookie = %q, %q, %v", name, data, err)
	}
	// Same file, a display the local record does not cover: the wildcard wins.
	name, data, err = LoadAuthCookie(path, "thehost", "1")
	if err != nil || name != AuthMITCookie || string(data) != "wildcookie" {
		t.Errorf("LoadAuthCookie for display 1 = %q, %q, %v", name, data, err)
	}
	// A host with no record at all still gets the wildcard.
	if _, data, err := LoadAuthCookie(path, "somewhere-else", "1"); err != nil || string(data) != "wildcookie" {
		t.Errorf("LoadAuthCookie for another host = %q, %v", data, err)
	}
	// An empty host asks the machine for its name; whatever that is, the call
	// must not fail.
	if _, _, err := LoadAuthCookie(path, "", "0"); err != nil {
		t.Errorf("LoadAuthCookie with an empty host: %v", err)
	}
	// No path, and a path that does not exist, are both "no cookie", not an
	// error: Xlib falls back to an unauthenticated setup and so does this.
	for _, p := range []string{"", filepath.Join(dir, "nope")} {
		if n, d, err := LoadAuthCookie(p, "h", "0"); err != nil || n != "" || d != nil {
			t.Errorf("LoadAuthCookie(%q) = %q, %q, %v; want empty and no error", p, n, d, err)
		}
	}
	// A file with no matching record is also "no cookie".
	empty := filepath.Join(dir, "empty")
	if err := os.WriteFile(empty, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if n, _, err := LoadAuthCookie(empty, "h", "0"); err != nil || n != "" {
		t.Errorf("LoadAuthCookie on an empty file = %q, %v", n, err)
	}
	// A corrupt file IS an error: it means something is wrong, not that there
	// is no cookie.
	bad := filepath.Join(dir, "bad")
	if err := os.WriteFile(bad, []byte{0, 1, 0, 9, 'x'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LoadAuthCookie(bad, "h", "0"); err == nil {
		t.Error("LoadAuthCookie accepted a truncated file")
	}
}

// failingOpen substitutes an openFile that fails with something other than
// "does not exist", which must propagate rather than degrade to "no cookie".
func TestLoadAuthCookieOpenError(t *testing.T) {
	orig := openFile
	t.Cleanup(func() { openFile = orig })
	want := errors.New("permission denied by the test")
	openFile = func(string) (io.ReadCloser, error) { return nil, want }
	if _, _, err := LoadAuthCookie("/anything", "h", "0"); !errors.Is(err, want) {
		t.Fatalf("LoadAuthCookie reported %v, want %v", err, want)
	}
}

func TestLoadAuthCookieHostnameFallback(t *testing.T) {
	orig := hostname
	t.Cleanup(func() { hostname = orig })
	hostname = func() string { return "thehost" }
	dir := t.TempDir()
	path := filepath.Join(dir, "Xauthority")
	if err := os.WriteFile(path, encodeXauthEntry(AuthEntry{Family: FamilyLocal,
		Address: []byte("thehost"), Number: "0", Name: AuthMITCookie, Data: cookie}), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, data, err := LoadAuthCookie(path, "", "0"); err != nil || !bytes.Equal(data, cookie) {
		t.Fatalf("LoadAuthCookie = %q, %v", data, err)
	}
}

func TestAuthFilePath(t *testing.T) {
	t.Setenv("XAUTHORITY", "/somewhere/xauth")
	if got := AuthFilePath(); got != "/somewhere/xauth" {
		t.Errorf("AuthFilePath with XAUTHORITY set = %q", got)
	}
	t.Setenv("XAUTHORITY", "")
	home, err := os.UserHomeDir()
	if err == nil && home != "" {
		if got := AuthFilePath(); got != home+"/.Xauthority" {
			t.Errorf("AuthFilePath = %q, want %q", got, home+"/.Xauthority")
		}
	}
	// With no home either, there is simply no authority file.
	t.Setenv("HOME", "")
	if home, err := os.UserHomeDir(); err != nil || home == "" {
		if got := AuthFilePath(); got != "" {
			t.Errorf("AuthFilePath with no HOME = %q, want empty", got)
		}
	}
}
