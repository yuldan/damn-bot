//go:build !minimal

package main

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

// TestDoctorToolsFetchCLI pins the CLI refusal path: fetch displays the
// license but never downloads (or writes under the data dir) without --yes,
// and an unknown name is rejected before any I/O.
func TestDoctorToolsFetchCLI(t *testing.T) {
	home := t.TempDir()
	for _, k := range []string{"HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "APPDATA"} {
		t.Setenv(k, home)
	}
	var out, errOut bytes.Buffer
	if code := runDoctor([]string{"tools", "fetch", "gh"}, &out, &errOut); code != 1 {
		t.Fatalf("fetch without --yes = %d, want 1", code)
	}
	if !bytes.Contains(out.Bytes(), []byte("MIT")) || !bytes.Contains(errOut.Bytes(), []byte("--yes")) {
		t.Fatalf("license/confirmation missing: out=%q err=%q", out.String(), errOut.String())
	}
	var installed []string
	err := filepath.WalkDir(home, func(path string, d os.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "gh" {
			installed = append(installed, path)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(installed) != 0 {
		t.Fatalf("refused fetch wrote %v", installed)
	}

	out.Reset()
	errOut.Reset()
	if code := runDoctor([]string{"tools", "fetch", "no-such-tool", "--yes"}, &out, &errOut); code != 1 {
		t.Fatalf("unknown fetch = %d, want 1", code)
	}
	if !bytes.Contains(errOut.Bytes(), []byte("not in the lock")) {
		t.Fatalf("unknown refusal = %q", errOut.String())
	}
}
