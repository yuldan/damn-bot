package journal

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestValidate(t *testing.T) {
	t.Parallel()
	good := &Record{SessionID: "s", Kind: FileWrite, Path: "/a", Before: &Snapshot{}}
	if err := good.Validate(); err != nil {
		t.Fatalf("valid record: %v", err)
	}
	bad := []*Record{
		{SessionID: "s", Kind: "shell", Path: "/a", Before: &Snapshot{}},
		{Kind: FileWrite, Path: "/a", Before: &Snapshot{}},
		{SessionID: "s", Kind: FileWrite, Path: "relative", Before: &Snapshot{}},
		{SessionID: "s", Kind: FileMove, Path: "/a", Before: &Snapshot{}},
		{SessionID: "s", Kind: FileWrite, Path: "/a"},
	}
	for i, r := range bad {
		if err := r.Validate(); !errors.Is(err, ErrIrreversible) {
			t.Errorf("bad[%d] = %v", i, err)
		}
	}
}

func TestNewUndoerRejectsNil(t *testing.T) {
	t.Parallel()
	_, u, _ := workspace(t)
	if _, err := NewUndoer(nil, u.sb); err == nil {
		t.Fatal("nil store accepted")
	}
	if _, err := NewUndoer(newFake(), nil); err == nil {
		t.Fatal("nil sandbox accepted")
	}
}

func TestUndoStoreErrors(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	stop := errors.New("store down")
	p := filepath.Join(root, "x")
	write(t, p, []byte("new"), 0o644)
	f.add(action(1, FileWrite, p, f.put([]byte("old"), 0o644), f.put([]byte("new"), 0o644)))

	f.getErr = stop
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, stop) {
		t.Fatalf("get error = %v", err)
	}
	f.getErr = nil
	f.listErr = stop
	if _, err := u.UndoLast(t.Context(), "s1", 1, false); !errors.Is(err, stop) {
		t.Fatalf("list error = %v", err)
	}
	f.listErr = nil
	f.blobErr = stop
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, stop) {
		t.Fatalf("blob error = %v", err)
	}
	f.blobErr = nil
	f.finishErr = stop
	res, err := u.Undo(t.Context(), "s1", 1, false)
	if !errors.Is(err, stop) || res.Status != Undone || string(read(t, p)) != "old" {
		t.Fatalf("finish error = %+v, %v", res, err)
	}
}

func TestUndoMoveVariants(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		name             string
		src, dst         string
		srcPresent       bool
		dstPresent       bool
		force            bool
		want             Status
		wantErr          error
		wantSrc, wantDst bool
	}{
		{"noop", "body", "", true, false, false, Undone, nil, true, false},
		{"changed", "", "other", false, true, false, Conflict, ErrConflict, false, true},
		{"both", "body", "body", true, true, false, Conflict, ErrConflict, true, true},
		{"force-both", "body", "body", true, true, true, Undone, nil, true, false},
		{"gone", "", "", false, false, false, Failed, ErrState, false, false},
	} {
		t.Run(c.name, func(t *testing.T) {
			root, u, f := workspace(t)
			src, dst := filepath.Join(root, "a"), filepath.Join(root, "b")
			if c.srcPresent {
				write(t, src, []byte(c.src), 0o644)
			}
			if c.dstPresent {
				write(t, dst, []byte(c.dst), 0o644)
			}
			a := action(1, FileMove, src, f.put([]byte("body"), 0o644), f.put([]byte("body"), 0o644))
			a.MoveTo = dst
			f.add(a)
			res, err := u.Undo(t.Context(), "s1", 1, c.force)
			if !errors.Is(err, c.wantErr) || res.Status != c.want {
				t.Fatalf("undo = %+v, %v", res, err)
			}
			if present(src) != c.wantSrc || present(dst) != c.wantDst {
				t.Fatalf("src/dst present = %v/%v", present(src), present(dst))
			}
		})
	}
}

func present(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// TestUndoSymlinkInsideRoot: a final component swapped for a symlink is a
// conflict; the link target is never touched (JN2-1).
func TestUndoSymlinkInsideRoot(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	q := filepath.Join(root, "q")
	write(t, q, []byte("new"), 0o644)
	p := filepath.Join(root, "p")
	if err := os.Symlink(q, p); err != nil {
		t.Skipf("symlink: %v", err)
	}
	f.add(action(1, FileWrite, p, f.put([]byte("old"), 0o644), f.put([]byte("new"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("symlink write: %v", err)
	}
	if string(read(t, q)) != "new" {
		t.Fatal("symlink target changed")
	}
	f.acts = map[int64]Action{}
	f.add(action(2, FileWrite, p, nil, f.put([]byte("new"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 2, false); !errors.Is(err, ErrConflict) {
		t.Fatalf("symlink create: %v", err)
	}
	if string(read(t, q)) != "new" {
		t.Fatal("symlink target removed")
	}
}

// TestUndoCreateHardlinkRefused: removing one link of a hard-linked path
// needs --force (JN2-12).
func TestUndoCreateHardlinkRefused(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("hard link detection is unavailable on Windows")
	}
	root, u, f := workspace(t)
	p, q := filepath.Join(root, "a"), filepath.Join(root, "b")
	write(t, p, []byte("body"), 0o644)
	if err := os.Link(p, q); err != nil {
		t.Skipf("hard link: %v", err)
	}
	f.add(action(1, FileWrite, p, nil, f.put([]byte("body"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); !errors.Is(err, ErrUnsafePath) {
		t.Fatalf("hard link create: %v", err)
	}
	if !present(p) || !present(q) {
		t.Fatal("hard link removed without force")
	}
	if _, err := u.Undo(t.Context(), "s1", 1, true); err != nil {
		t.Fatal(err)
	}
	if present(p) || !present(q) {
		t.Fatal("force did not remove only the action path")
	}
}

func TestUndoModeZero(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode 0000 is not meaningful on Windows")
	}
	root, u, f := workspace(t)
	p := filepath.Join(root, "a")
	write(t, p, []byte("new"), 0o644)
	before := f.put([]byte("old"), 0o644)
	before.Mode, before.ModeSet = 0, true
	f.add(action(1, FileWrite, p, before, f.put([]byte("new"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode().Perm() != 0 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestUndoSetuidPreserved(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("setuid is not meaningful on Windows")
	}
	root, u, f := workspace(t)
	p := filepath.Join(root, "a")
	write(t, p, []byte("new"), 0o644)
	mode := fs.ModeSetuid | 0o755
	before := f.put([]byte("old"), 0o644)
	before.Mode, before.ModeSet = mode, true
	f.add(action(1, FileWrite, p, before, f.put([]byte("new"), 0o644)))
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(p)
	if err != nil || fi.Mode()&fs.ModeSetuid == 0 || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestUndoMoveRestoresMode(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits differ on Windows")
	}
	root, u, f := workspace(t)
	src, dst := filepath.Join(root, "a"), filepath.Join(root, "b")
	write(t, dst, []byte("body"), 0o644)
	a := action(1, FileMove, src, &Image{BlobSHA: hash([]byte("body")), Size: 4, Mode: 0o755, ModeSet: true}, f.put([]byte("body"), 0o644))
	a.MoveTo = dst
	f.add(a)
	if _, err := u.Undo(t.Context(), "s1", 1, false); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(src)
	if err != nil || fi.Mode().Perm() != 0o755 {
		t.Fatalf("mode = %v, %v", fi.Mode(), err)
	}
}

func TestUndoWriteMissingParent(t *testing.T) {
	t.Parallel()
	root, u, f := workspace(t)
	p := filepath.Join(root, "missing", "f")
	f.add(action(1, FileDelete, p, f.put([]byte("old"), 0o644), nil))
	res, err := u.Undo(t.Context(), "s1", 1, false)
	if err == nil || res.Status != Failed {
		t.Fatalf("missing parent = %+v, %v", res, err)
	}
}

func TestUndoHelperErrors(t *testing.T) {
	t.Parallel()
	root, u, _ := workspace(t)
	dir := filepath.Join(root, "dir")
	if err := os.MkdirAll(filepath.Join(dir, "child"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := u.remove(dir); err == nil {
		t.Fatal("remove non-empty dir succeeded")
	}
	if err := u.chmod(filepath.Join(root, "missing"), 0o644); err == nil {
		t.Fatal("chmod missing succeeded")
	}
	if runtime.GOOS != "windows" {
		if err := syncDir(filepath.Join(root, "missing")); err == nil {
			t.Fatal("syncDir missing succeeded")
		}
	}
}
