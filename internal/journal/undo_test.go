package journal

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/sandbox"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// fakeStore is the in-memory journal.Store used by the executor tests; the
// real ctxdb/journalstore is exercised by TestUndoSuite.
type fakeStore struct {
	acts  map[int64]Action
	blobs map[string][]byte
	undos []Undo
}

func newFake() *fakeStore {
	return &fakeStore{acts: map[int64]Action{}, blobs: map[string][]byte{}}
}

func (f *fakeStore) put(data []byte, mode fs.FileMode) *Image {
	h := hash(data)
	f.blobs[h] = data
	return &Image{BlobSHA: h, Size: int64(len(data)), Mode: mode}
}

func (f *fakeStore) add(a Action) {
	f.acts[a.ID] = a
}

func (f *fakeStore) Record(context.Context, Record) (int64, error) { return 0, nil }

func (f *fakeStore) Get(_ context.Context, id int64) (Action, error) {
	a, ok := f.acts[id]
	if !ok {
		return Action{}, ErrState
	}
	return a, nil
}

func (f *fakeStore) List(_ context.Context, _ string, limit int) ([]Action, error) {
	var out []Action
	for id := int64(len(f.acts)); id >= 1 && len(out) < limit; id-- {
		if a, ok := f.acts[id]; ok {
			out = append(out, a)
		}
	}
	return out, nil
}

func (f *fakeStore) Blob(_ context.Context, sha string) ([]byte, error) {
	b, ok := f.blobs[sha]
	if !ok {
		return nil, ErrBlob
	}
	return b, nil
}

func (f *fakeStore) Finish(_ context.Context, u Undo) error {
	f.undos = append(f.undos, u)
	return nil
}

func workspace(t *testing.T) (string, *Undoer, *fakeStore) {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	f := newFake()
	u, err := NewUndoer(f, sb)
	if err != nil {
		t.Fatal(err)
	}
	return root, u, f
}

func write(t *testing.T, path string, data []byte, mode fs.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func action(id int64, kind Kind, path string, before, after *Image) Action {
	return Action{ID: id, SessionID: "s1", Kind: kind, Reversibility: Reversible, Path: path, Before: before, After: after}
}

func TestUndoWriteRestoresBytesAndMode(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "a.txt")
	write(t, p, []byte("new\n"), 0o644)
	a := action(1, FileWrite, p, f.put([]byte("old\n"), 0o750), f.put([]byte("new\n"), 0o644))
	f.add(a)

	res, err := u.Undo(t.Context(), "s1", 1, false)
	if err != nil || res.Status != Undone {
		t.Fatalf("undo = %+v, %v", res, err)
	}
	if got := read(t, p); string(got) != "old\n" {
		t.Fatalf("content = %q", got)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o750 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
	if len(f.undos) != 1 || f.undos[0].Status != Undone {
		t.Fatalf("recorded = %+v", f.undos)
	}
}

func TestUndoCreateRemovesFile(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "new.txt")
	write(t, p, []byte("body"), 0o644)
	f.add(action(1, FileWrite, p, nil, f.put([]byte("body"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(p); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("file still exists: %v", err)
	}
}

func TestUndoDeleteRecreates(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "gone.txt")
	f.add(action(1, FileDelete, p, f.put([]byte("old"), 0o640), nil))
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, p); string(got) != "old" {
		t.Fatalf("content = %q", got)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0o640 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestUndoMove(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	src, dst := filepath.Join(root, "a"), filepath.Join(root, "b")
	write(t, dst, []byte("body"), 0o600)
	a := action(1, FileMove, src, f.put([]byte("body"), 0o600), f.put([]byte("body"), 0o600))
	a.MoveTo = dst
	f.add(a)
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	if got := read(t, src); string(got) != "body" {
		t.Fatalf("source = %q", got)
	}
	if _, err := os.Stat(dst); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("destination still exists: %v", err)
	}
}

func TestUndoConflictAndForce(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "a")
	write(t, p, []byte("other"), 0o644)
	f.add(action(1, FileWrite, p, f.put([]byte("old"), 0o644), f.put([]byte("new"), 0o644)))

	res, err := u.Undo(t.Context(), "s1", 1, false)
	if !errors.Is(err, ErrConflict) || res.Status != Conflict {
		t.Fatalf("conflict = %+v, %v", res, err)
	}
	if got := read(t, p); string(got) != "other" {
		t.Fatalf("conflict changed content: %q", got)
	}
	if f.undos[0].Status != Conflict {
		t.Fatalf("recorded = %+v", f.undos)
	}
	res, err = u.Undo(t.Context(), "s1", 1, true)
	if err != nil || res.Status != Undone {
		t.Fatalf("force = %+v, %v", res, err)
	}
	if got := read(t, p); string(got) != "old" {
		t.Fatalf("forced content = %q", got)
	}
}

func TestUndoIdempotent(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "a")
	write(t, p, []byte("new"), 0o644)
	f.add(action(1, FileWrite, p, f.put([]byte("old"), 0o644), f.put([]byte("new"), 0o644)))
	for i := range 2 {
		res, err := u.Undo(t.Context(), "s1", 1, false)
		if err != nil || res.Status != Undone || readBytes(t, p) != "old" {
			t.Fatalf("undo %d = %+v, %v, %q", i, res, err, read(t, p))
		}
	}
	if len(f.undos) != 2 || f.undos[1].Detail != "already at the before image" {
		t.Fatalf("undos = %+v", f.undos)
	}
}

func readBytes(t *testing.T, p string) string { return string(read(t, p)) }

func TestUndoRefusesOutsideAndSymlink(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	outside := t.TempDir()
	target := filepath.Join(outside, "secret")
	write(t, target, []byte("new"), 0o600)

	f.add(action(1, FileWrite, target, f.put([]byte("old"), 0o600), f.put([]byte("new"), 0o600)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("outside: %v", err)
	}
	if got := read(t, target); string(got) != "new" {
		t.Fatalf("outside file changed: %q", got)
	}

	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f.acts = map[int64]Action{}
	f.add(action(2, FileWrite, link, f.put([]byte("old"), 0o600), f.put([]byte("new"), 0o600)))
	if _, err := u.Undo(t.Context(), "s1", 2, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("symlink: %v", err)
	}
	if got := read(t, target); string(got) != "new" {
		t.Fatalf("symlink target changed: %q", got)
	}
}

func TestUndoRefusals(t *testing.T) {
	t.Parallel()
	_, u, f := workspace(t)
	cases := []struct {
		name string
		a    Action
		want error
		stat Status
	}{
		{"expired", Action{ID: 1, Kind: FileWrite, Path: "/x", Expired: true, Reversibility: Reversible}, ErrExpired, Expired},
		{"open", Action{ID: 2, Kind: FileWrite, Path: "/x", Open: true, Reversibility: Reversible}, ErrPending, Failed},
		{"irreversible", Action{ID: 3, Kind: FileWrite, Path: "/x", Reversibility: "partial"}, ErrIrreversible, Failed},
	}
	for _, c := range cases {
		f.add(c.a)
		res, err := u.Undo(t.Context(), "s1", c.a.ID, false)
		if !errors.Is(err, c.want) || res.Status != c.stat {
			t.Errorf("%s = %+v, %v", c.name, res, err)
		}
	}
}

func TestUndoMissingBlob(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "a")
	write(t, p, []byte("new"), 0o644)
	f.add(action(1, FileWrite, p, &Image{BlobSHA: hash([]byte("lost")), Size: 4, Mode: 0o644}, f.put([]byte("new"), 0o644)))
	res, err := u.Undo(t.Context(), "s1", 1, false)
	if !errors.Is(err, ErrBlob) || res.Status != Failed {
		t.Fatalf("missing blob = %+v, %v", res, err)
	}
	if got := read(t, p); string(got) != "new" {
		t.Fatalf("file changed: %q", got)
	}
}

func TestUndoHardlink(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p, q := filepath.Join(root, "a"), filepath.Join(root, "b")
	write(t, p, []byte("new"), 0o644)
	if err := os.Link(p, q); err != nil {
		t.Skipf("hard link: %v", err)
	}
	f.add(action(1, FileWrite, p, f.put([]byte("old"), 0o644), f.put([]byte("new"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("hard link without force: %v", err)
	}
	if _, err := u.Undo(t.Context(), "s1", 1, true); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{p, q} {
		if got := read(t, name); string(got) != "old" {
			t.Fatalf("%s = %q", name, got)
		}
	}
	if fi, err := os.Stat(p); err != nil || fi.Mode().Perm() != 0o644 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestUndoLastReverseOrder(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p1, p2 := filepath.Join(root, "1"), filepath.Join(root, "2")
	write(t, p1, []byte("new1"), 0o644)
	write(t, p2, []byte("new2"), 0o644)
	f.add(action(1, FileWrite, p1, f.put([]byte("old1"), 0o644), f.put([]byte("new1"), 0o644)))
	f.add(action(2, FileWrite, p2, f.put([]byte("old2"), 0o644), f.put([]byte("new2"), 0o644)))
	res, err := u.UndoLast(t.Context(), "s1", 2, false)
	if err != nil || len(res) != 2 || res[0].ActionID != 2 || res[1].ActionID != 1 {
		t.Fatalf("undo last = %+v, %v", res, err)
	}
	if string(read(t, p1)) != "old1" || string(read(t, p2)) != "old2" {
		t.Fatal("files not restored")
	}
}
