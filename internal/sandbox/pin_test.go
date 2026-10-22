package sandbox

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// canonicalTemp returns a symlink-free temporary directory.
func canonicalTemp(t *testing.T) string {
	t.Helper()
	p, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestRootOf: RootOf maps a path to its containing canonical root and the
// relative name, and reports outside paths as not ok.
func TestRootOf(t *testing.T) {
	t.Parallel()
	root, other := canonicalTemp(t), canonicalTemp(t)
	sb, err := New(&Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "a", "b")
	if err := os.MkdirAll(inner, 0o750); err != nil {
		t.Fatal(err)
	}
	got, rel, ok := sb.RootOf(filepath.Join(inner, "f.txt"))
	if !ok || got != root || rel != filepath.Join("a", "b", "f.txt") {
		t.Fatalf("RootOf = %q, %q, %v", got, rel, ok)
	}
	if _, _, ok := sb.RootOf(filepath.Join(other, "f.txt")); ok {
		t.Fatal("RootOf accepted a path outside every root")
	}
}

// TestPinRoot (REVIEW-F2-SEC5-16): PinRoot opens the root recorded at New
// and refuses a different directory at the same path or a symlink there.
func TestPinRoot(t *testing.T) {
	t.Parallel()
	base := canonicalTemp(t)
	root := filepath.Join(base, "ws")
	other := filepath.Join(base, "other")
	for _, d := range []string{root, other} {
		if err := os.Mkdir(d, 0o750); err != nil {
			t.Fatal(err)
		}
	}
	sb, err := New(&Config{Roots: []string{root}, Home: base})
	if err != nil {
		t.Fatal(err)
	}
	d, err := sb.PinRoot(t.Context(), root)
	if err != nil {
		t.Fatalf("PinRoot(recorded root) = %v", err)
	}
	if err := d.Close(); err != nil {
		t.Fatal(err)
	}
	// A different directory at the recorded path is refused.
	old := filepath.Join(base, "ws.old")
	if err := os.Rename(root, old); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.PinRoot(t.Context(), root); !errors.Is(err, ErrOutsideWorkspace) {
		t.Fatalf("PinRoot(replaced root) = %v, want ErrOutsideWorkspace", err)
	}
	// A symlink at the recorded path is refused too.
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(other, root); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if _, err := sb.PinRoot(t.Context(), root); err == nil {
		t.Fatal("PinRoot(symlinked root) succeeded")
	}
}

// TestPinRootMissing: a deleted root is refused with a not-exist error.
func TestPinRootMissing(t *testing.T) {
	t.Parallel()
	root := canonicalTemp(t)
	sb, err := New(&Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(root); err != nil {
		t.Fatal(err)
	}
	if _, err := sb.PinRoot(t.Context(), root); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("PinRoot(missing) = %v, want not-exist", err)
	}
}
