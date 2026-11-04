//go:build windows

package testutil

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

// TestWriteExecutableOpenDestinationFailsClosed is the negative control for
// the Windows remove-then-rename path: os.Open shares read/write but not
// delete, so the held destination is denied. Windows reports
// ERROR_SHARING_VIOLATION for that handle (fail-closed immediately: replaceFile
// retries only ERROR_ACCESS_DENIED), while ERROR_ACCESS_DENIED is the
// equivalent denial from another holder. Either way the old content survives,
// the temp file is removed, and closing the handle is the positive control
// that the same replacement then succeeds.
//
// That remove-then-rename window (the destination briefly has no name) is
// acceptable only for this test fixture helper. Production code must not
// import internal/testutil (archtest testOnlyPackages rejects it) or copy the
// pattern: it needs a platform-atomic replace.
func TestWriteExecutableOpenDestinationFailsClosed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tool")
	root, oerr := os.OpenRoot(dir)
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	}()
	if werr := root.WriteFile("tool", []byte("old"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	locked, lerr := os.Open(path) // #nosec G304 -- fixture created in t.TempDir
	if lerr != nil {
		t.Fatal(lerr)
	}
	defer func() {
		if cerr := locked.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			t.Errorf("close destination: %v", cerr)
		}
	}()

	werr := WriteExecutable(path, []byte("new"), 0o700)
	if denied := errors.Is(werr, windows.ERROR_SHARING_VIOLATION) || errors.Is(werr, windows.ERROR_ACCESS_DENIED); !denied {
		t.Fatalf("WriteExecutable with open destination = %v, want ERROR_SHARING_VIOLATION or ERROR_ACCESS_DENIED", werr)
	}
	got, rerr := root.ReadFile("tool")
	if rerr != nil || string(got) != "old" {
		t.Fatalf("destination changed while open: %q, %v", got, rerr)
	}
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		t.Fatal(derr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tool.tmp-") {
			t.Fatalf("temp file left after denied replace: %s", e.Name())
		}
	}

	if cerr := locked.Close(); cerr != nil {
		t.Fatalf("close destination: %v", cerr)
	}
	if werr := WriteExecutable(path, []byte("new"), 0o700); werr != nil {
		t.Fatalf("WriteExecutable after close: %v", werr)
	}
	got, rerr = root.ReadFile("tool")
	if rerr != nil || string(got) != "new" {
		t.Fatalf("content after close = %q, %v", got, rerr)
	}
}
