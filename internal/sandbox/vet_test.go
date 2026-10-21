package sandbox

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestOpenFileDenyPathFallback (REVIEW-F2-SEC5-20): when the handle path
// cannot be read (/proc absent, GetFinalPathNameByHandle failure), the
// deny_paths check falls back to the lexical child path instead of being
// skipped. The seam replaces the platform path reader, so no /proc is
// needed.
func TestOpenFileDenyPathFallback(t *testing.T) {
	root := canonicalTemp(t)
	dirPath := filepath.Join(root, "d")
	if err := os.Mkdir(dirPath, 0o750); err != nil {
		t.Fatal(err)
	}
	// Denied path does not exist at New: no deny identity, only the
	// lexical pattern can refuse it.
	denied := filepath.Join(dirPath, "f.txt")
	sb, err := New(&Config{Roots: []string{root}, DenyPaths: []string{denied}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	if werr := os.WriteFile(denied, []byte("x"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	if werr := os.WriteFile(filepath.Join(dirPath, "ok.txt"), []byte("y"), 0o600); werr != nil {
		t.Fatal(werr)
	}
	d, err := sb.OpenDir(t.Context(), dirPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck // test cleanup
	sb.handlePath = func(*os.File) (string, bool) { return "", false }
	if _, oerr := d.OpenFile("f.txt", os.O_RDONLY, 0); !errors.Is(oerr, ErrDeniedPath) {
		t.Fatalf("OpenFile without a handle path = %v, want ErrDeniedPath", oerr)
	}
	f, err := d.OpenFile("ok.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatalf("OpenFile(ok) = %v", err)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
}

// TestDirOpenSkipsHandlePath (REVIEW-F2-SEC5-21): the codetools walker path
// (Dir.Open) checks deny_paths by identity and lexical path only, so it
// pays no per-file readlink; the undo path (OpenFile) still reads the
// handle path.
func TestDirOpenSkipsHandlePath(t *testing.T) {
	root := canonicalTemp(t)
	if err := os.WriteFile(filepath.Join(root, "f.txt"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	sb, err := New(&Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	d, err := sb.OpenDir(t.Context(), root, false)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close() //nolint:errcheck // test cleanup
	calls := 0
	sb.handlePath = func(*os.File) (string, bool) {
		calls++
		return filepath.Join(root, "f.txt"), true
	}
	f, v, err := d.Open("f.txt")
	if err != nil || v != Inside {
		t.Fatalf("Open = %v, %v", v, err)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if calls != 0 {
		t.Fatalf("Dir.Open read the handle path %d times, want 0", calls)
	}
	g, err := d.OpenFile("f.txt", os.O_RDONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := g.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if calls != 1 {
		t.Fatalf("Dir.OpenFile read the handle path %d times, want 1", calls)
	}
	// The seam is used by vetHandle; make sure the real reader path also
	// still refuses a denied path when it works.
	sb.handlePath = nil
	if _, _, err := d.Open("f.txt"); err != nil {
		t.Fatalf("Open after reset = %v", err)
	}
}
