//go:build unix

package testutil

import (
	"io"
	"os"
	"path/filepath"
	"testing"
)

// TestWriteExecutableReplacesAtomically: Unix rename swaps the name, so an
// already-open handle keeps the replaced inode and the executable mode holds.
func TestWriteExecutableReplacesAtomically(t *testing.T) {
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
	old, oerr := root.Open("tool")
	if oerr != nil {
		t.Fatal(oerr)
	}
	defer func() {
		if cerr := old.Close(); cerr != nil {
			t.Errorf("close old: %v", cerr)
		}
	}()
	if werr := WriteExecutable(filepath.Join(dir, "tool"), []byte("new"), 0o700); werr != nil {
		t.Fatalf("WriteExecutable: %v", werr)
	}
	got, rerr := root.ReadFile("tool")
	if rerr != nil || string(got) != "new" {
		t.Fatalf("content = %q, %v", got, rerr)
	}
	info, serr := root.Stat("tool")
	if serr != nil {
		t.Fatal(serr)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Fatalf("perm = %o, want 700", perm)
	}
	kept, rerr := io.ReadAll(old)
	if rerr != nil || string(kept) != "old" {
		t.Fatalf("open handle saw %q, %v; want the replaced inode", kept, rerr)
	}
}
