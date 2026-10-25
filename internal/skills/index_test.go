package skills

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestIndexDiff: build/diff reports added, changed, removed and no-op.
func TestIndexDiff(t *testing.T) {
	t.Parallel()
	idx := BuildIndex([]Skill{
		{Name: "a", Description: "A", Path: "/a", SHA256: "1111", Source: SourceUser},
		{Name: "b", Description: "B", Path: "/b", SHA256: "2222", Source: SourceBuiltin},
	})
	if len(idx) != 2 || idx["a"].SHA256 != "1111" {
		t.Fatalf("index = %+v", idx)
	}
	next := SkillIndex{"a": {Name: "a", SHA256: "9999"}, "c": {Name: "c", SHA256: "3333"}}
	d := Diff(idx, next)
	if len(d.Added) != 1 || d.Added[0] != "c" || len(d.Changed) != 1 || d.Changed[0] != "a" || len(d.Removed) != 1 || d.Removed[0] != "b" {
		t.Fatalf("delta = %+v", d)
	}
	if !Diff(idx, idx).Empty() {
		t.Error("identical indexes reported a delta")
	}
}

// TestIndexSaveLoad: atomic 0600 file; missing file is empty.
func TestIndexSaveLoad(t *testing.T) {
	t.Parallel()
	idx := SkillIndex{"b": {Name: "b", Description: "B", SHA256: "2222"}}
	dir := t.TempDir()
	if err := SaveIndex(dir, idx); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(filepath.Join(dir, IndexFile))
	if err != nil {
		t.Fatal(err)
	}
	// Windows cannot express 0600: a writable file reports 0666 there, so the
	// POSIX mode assertion only makes sense off Windows (repo pattern).
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600 {
		t.Fatalf("index mode = %v, want 0600", fi.Mode().Perm())
	}
	got, err := LoadIndex(dir)
	if err != nil || got["b"].Description != "B" {
		t.Fatalf("loaded = %+v, %v", got, err)
	}
	if empty, err := LoadIndex(t.TempDir()); err != nil || len(empty) != 0 {
		t.Fatalf("missing index = %v/%v", empty, err)
	}
}

// TestIndexMalformed: junk or unknown fields fail closed.
func TestIndexMalformed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, IndexFile), []byte(`{"a":{"name":"a","unknown":1}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadIndex(dir); err == nil {
		t.Fatal("unknown index field accepted")
	}
}
