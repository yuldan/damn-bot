package archtest

import (
	"bytes"
	"fmt"
	"io/fs"
	"strings"
	"testing"
)

// TestConflictMarkers (gate CI): penanda konflik Git yang belum diselesaikan tidak boleh ada di
// pohon repo. Pemindaian memakai filesystem, bukan `git grep`, karena meta-test menjalankan gate
// pada salinan `git archive` (tanpa `.git`) dan pelanggaran sintetisnya adalah file sementara.
func TestConflictMarkers(t *testing.T) {
	t.Parallel()
	fsys := openRepo(t).FS()
	scanned := 0
	err := fs.WalkDir(fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if skipConflictDir(p, d.Name()) {
				return fs.SkipDir
			}
			return nil
		}
		if skipConflictFile(p, d) {
			return nil
		}
		scanned++
		return scanConflictMarkers(t, fsys, p)
	})
	if err != nil {
		t.Fatalf("walk repo: %v", err)
	}
	if scanned == 0 {
		t.Fatal("tidak ada file yang dipindai")
	}
}

// skipConflictDir mengecualikan `.git`, fixture `testdata/`, dan dokumen review `docs/review*`.
func skipConflictDir(p, name string) bool {
	return p != "." && (name == ".git" || name == "testdata" || strings.HasPrefix(p, "docs/review"))
}

// skipConflictFile mengecualikan file gate ini (memuat pola penanda sebagai data) dan entri yang
// bukan file reguler.
func skipConflictFile(p string, d fs.DirEntry) bool {
	const self = "internal/archtest/conflict_marker_test.go"
	return d.Name() == ".git" || p == self || !d.Type().IsRegular()
}

// scanConflictMarkers gagal bila file p memuat salah satu penanda konflik Git pada awal baris.
func scanConflictMarkers(t *testing.T, fsys fs.FS, p string) error {
	t.Helper()
	src, err := fs.ReadFile(fsys, p)
	if err != nil {
		return fmt.Errorf("read %s: %w", p, err)
	}
	ours, sep, theirs := []byte("<<<<<<< "), []byte("======="), []byte(">>>>>>> ")
	for i, line := range bytes.Split(src, []byte("\n")) {
		if bytes.HasPrefix(line, ours) || bytes.Equal(line, sep) || bytes.HasPrefix(line, theirs) {
			t.Errorf("CONFLICT MARKER: %s:%d: %s", p, i+1, line)
		}
	}
	return nil
}
