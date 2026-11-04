package testutil

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestWriteExecutableReplacesExisting: an existing destination (the Windows
// MoveFileEx case) is replaced, including a second replacement, and no temp
// file is left behind.
func TestWriteExecutableReplacesExisting(t *testing.T) {
	dir := t.TempDir()
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
	for _, body := range []string{"new", "newest"} {
		if werr := WriteExecutable(filepath.Join(dir, "tool"), []byte(body), 0o700); werr != nil {
			t.Fatalf("WriteExecutable(%s): %v", body, werr)
		}
		got, rerr := root.ReadFile("tool")
		if rerr != nil || string(got) != body {
			t.Fatalf("content = %q, %v; want %q", got, rerr, body)
		}
	}
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatal(rerr)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".tool.tmp-") {
			t.Fatalf("temp file left behind: %s", e.Name())
		}
	}
}

// TestWriteExecutableReportsMissingDir: failures surface instead of being
// swallowed.
func TestWriteExecutableReportsMissingDir(t *testing.T) {
	if err := WriteExecutable(filepath.Join(t.TempDir(), "missing", "tool"), []byte("x"), 0o700); err == nil {
		t.Fatal("WriteExecutable into a missing directory succeeded")
	}
}
