package skills

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"slices"
	"testing"
	"testing/fstest"
	"time"
)

// syncStore is a Store plus exact-name lookup for the bundled-sync tests.
type syncStore struct {
	rows    []Skill
	indexes int
}

func (s *syncStore) Index(_ context.Context, sk *Skill) (int64, error) {
	s.indexes++
	for i := range s.rows {
		if s.rows[i].Name == sk.Name {
			s.rows[i] = *sk
			return sk.ID, nil
		}
	}
	s.rows = append(s.rows, *sk)
	return int64(len(s.rows)), nil
}

func (s *syncStore) Quarantine(context.Context, *Skill, string, bool) (int64, int64, error) {
	return 0, 0, errors.New("unexpected quarantine")
}

func (s *syncStore) Approve(context.Context, int64) error { return errors.New("unexpected approve") }

func (s *syncStore) Search(_ context.Context, _ string, k int) ([]Skill, error) {
	if k > len(s.rows) {
		k = len(s.rows)
	}
	return s.rows[:k], nil
}

func (s *syncStore) Status(context.Context, int64) (Status, error) { return StatusApproved, nil }

func (s *syncStore) ByName(_ context.Context, name string) (Skill, bool, error) {
	for i := range s.rows {
		if s.rows[i].Name == name {
			return s.rows[i], true, nil
		}
	}
	return Skill{}, false, nil
}

func corpus(body string) fstest.MapFS {
	return fstest.MapFS{
		"bundled/code-review/SKILL.md": &fstest.MapFile{Data: []byte(
			"---\nname: code-review\ndescription: Review a change for correctness and tests.\n---\n" + body)},
	}
}

func openRoot(t *testing.T, dir string) *os.Root {
	t.Helper()
	root, err := os.OpenRoot(dir)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := root.Close(); cerr != nil {
			t.Errorf("close root: %v", cerr)
		}
	})
	return root
}

func TestSyncBundledIdempotent(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &syncStore{}
	svc := newService(t, st, nil)
	fsys := corpus("Read the diff before commenting.\n")

	first, err := svc.SyncBundled(t.Context(), fsys, dir)
	if err != nil {
		t.Fatalf("first sync: %v", err)
	}
	if !slices.Equal(first.Installed, []string{"code-review"}) || len(first.Conflicts) != 0 {
		t.Fatalf("first result = %+v", first)
	}
	if st.indexes != 1 || len(st.rows) != 1 {
		t.Fatalf("indexes = %d rows = %d, want 1/1", st.indexes, len(st.rows))
	}
	root := openRoot(t, dir)
	info, err := root.Lstat("code-review/SKILL.md")
	if err != nil {
		t.Fatalf("synced file: %v", err)
	}
	before := info.ModTime()
	want, err := root.ReadFile("code-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}

	assertIdempotentSecondRun(t, svc, fsys, dir, st, root, before, want)
}

func assertIdempotentSecondRun(t *testing.T, svc *Service, fsys fs.FS, dir string, st *syncStore, root *os.Root, before time.Time, want []byte) {
	t.Helper()
	second, err := svc.SyncBundled(t.Context(), fsys, dir)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !slices.Equal(second.Unchanged, []string{"code-review"}) || len(second.Installed) != 0 {
		t.Fatalf("second result = %+v", second)
	}
	if st.indexes != 1 {
		t.Fatalf("second sync indexed %d times, want 1 total", st.indexes)
	}
	after, err := root.Lstat("code-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before) {
		t.Fatalf("mtime changed: %s -> %s", before, after.ModTime())
	}
	got, err := root.ReadFile("code-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("content changed on the idempotent run")
	}
}

func TestSyncBundledDoesNotOverwriteUserEdit(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &syncStore{}
	svc := newService(t, st, nil)
	fsys := corpus("Read the diff before commenting.\n")
	if _, err := svc.SyncBundled(t.Context(), fsys, dir); err != nil {
		t.Fatalf("first sync: %v", err)
	}
	root := openRoot(t, dir)
	edited := []byte("---\nname: code-review\ndescription: user edited\n---\nmy local copy\n")
	f, err := root.Create("code-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, werr := f.Write(edited); werr != nil {
		t.Fatal(werr)
	}
	if cerr := f.Close(); cerr != nil {
		t.Fatal(cerr)
	}

	res, err := svc.SyncBundled(t.Context(), fsys, dir)
	if err != nil {
		t.Fatalf("second sync: %v", err)
	}
	if !slices.Equal(res.Conflicts, []string{"code-review"}) {
		t.Fatalf("conflicts = %v, want [code-review]", res.Conflicts)
	}
	if st.indexes != 1 {
		t.Fatalf("indexed %d times, want 1 (no overwrite install)", st.indexes)
	}
	got, err := root.ReadFile("code-review/SKILL.md")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, edited) {
		t.Fatal("sync overwrote the user-edited file")
	}
}

func TestSyncBundledSkipsStoredConflict(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &syncStore{rows: []Skill{{Name: "code-review", SHA256: "a-different-hash"}}}
	svc := newService(t, st, nil)
	res, err := svc.SyncBundled(t.Context(), corpus("Read the diff before commenting.\n"), dir)
	if err != nil {
		t.Fatalf("sync: %v", err)
	}
	if !slices.Equal(res.Conflicts, []string{"code-review"}) {
		t.Fatalf("conflicts = %v", res.Conflicts)
	}
	if st.indexes != 0 {
		t.Fatalf("stored conflict indexed %d times, want 0", st.indexes)
	}
	root := openRoot(t, dir)
	if _, err := root.Lstat("code-review/SKILL.md"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("stored conflict wrote a file: %v", err)
	}
}

func TestSyncBundledInvalidCorpusFailsClosed(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &syncStore{}
	svc := newService(t, st, nil)
	bad := fstest.MapFS{"bundled/bad/SKILL.md": &fstest.MapFile{Data: []byte("not a skill")}}
	res, err := svc.SyncBundled(t.Context(), bad, dir)
	if err == nil {
		t.Fatal("invalid corpus did not fail")
	}
	if !slices.Equal(res.Failed, []string{"bad"}) {
		t.Fatalf("failed = %v, want [bad]", res.Failed)
	}
	if st.indexes != 0 || len(st.rows) != 0 {
		t.Fatalf("invalid skill reached the catalog: indexes=%d rows=%d", st.indexes, len(st.rows))
	}
	root := openRoot(t, dir)
	if _, err := root.Lstat("bad"); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("invalid skill left a directory: %v", err)
	}
}

func TestSyncBundledEmptyCorpusFails(t *testing.T) {
	t.Parallel()
	svc := newService(t, &syncStore{}, nil)
	if _, err := svc.SyncBundled(t.Context(), fstest.MapFS{}, t.TempDir()); err == nil {
		t.Fatal("empty corpus did not fail")
	}
}

func TestSyncBundledNameMismatchFails(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	st := &syncStore{}
	svc := newService(t, st, nil)
	mismatch := fstest.MapFS{
		"bundled/dir-name/SKILL.md": &fstest.MapFile{Data: []byte(
			"---\nname: other-name\ndescription: mismatch\n---\nbody\n")},
	}
	res, err := svc.SyncBundled(t.Context(), mismatch, dir)
	if err == nil {
		t.Fatal("name mismatch did not fail")
	}
	if !slices.Equal(res.Failed, []string{"dir-name"}) || st.indexes != 0 {
		t.Fatalf("mismatch result = %+v indexes=%d", res, st.indexes)
	}
}
