package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMonetaryKeyMissingTail: a non-existent tail is appended to the longest
// existing parent instead of falling back to the raw spelling (SEC10-18).
func TestMonetaryKeyMissingTail(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	got := monetaryKey(filepath.Join(dir, "no", "such", "file.sh"))
	if !strings.HasPrefix(got, monetaryKey(dir)) || !strings.HasSuffix(got, "/no/such/file.sh") {
		t.Fatalf("missing tail key = %q", got)
	}
	if link, err := filepath.EvalSymlinks(dir); err == nil && link != dir {
		if !strings.HasPrefix(got, monetaryKey(link)) {
			t.Fatalf("tail key %q does not use the resolved parent %q", got, link)
		}
	}
}

// TestCleanDevicePrefix: Win32 device prefixes are stripped before
// canonicalization so they do not bypass the key (SEC10-15).
func TestCleanDevicePrefix(t *testing.T) {
	t.Parallel()
	for in, want := range map[string]string{
		`\\?\C:\Pay\scripts\x.sh`:       `C:\Pay\scripts\x.sh`,
		`\\.\C:\Pay\scripts\x.sh`:       `C:\Pay\scripts\x.sh`,
		`\\?\UNC\server\share\Pay\x.sh`: `\\server\share\Pay\x.sh`,
		`/tmp/pay/scripts/x.sh`:         `/tmp/pay/scripts/x.sh`,
	} {
		if got := cleanDevicePrefix(in); got != want {
			t.Errorf("cleanDevicePrefix(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestMonetaryKeyFileLink: an existing file symlink and its target share a key.
func TestMonetaryKeyFileLink(t *testing.T) {
	t.Parallel()
	target := filepath.Join(t.TempDir(), "target.sh")
	if err := os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link.sh")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	if monetaryKey(link) != monetaryKey(target) {
		t.Fatalf("link %q != target %q", monetaryKey(link), monetaryKey(target))
	}
}
