package codetools

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// linkedEnv is an env whose f.txt (content) has a second name outside the
// root, so edits need approval and run in place (S16-7, CT27-8).
func linkedEnv(t *testing.T, content string) (v *env, p, other string) {
	t.Helper()
	v = newEnv(t)
	p = v.put(t, "f.txt", content)
	other = filepath.Join(v.out, "other.txt")
	if err := os.Link(p, other); err != nil {
		t.Skipf("Link: %v", err)
	}
	return v, p, other
}

// approvedTag reads rel with approval and returns its tag.
func approvedTag(t *testing.T, v *env, rel string) string {
	t.Helper()
	r, err := v.e.Read(context.Background(), ReadArgs{Path: rel, Limit: 1}, Opts{Approved: true})
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	return metaStr(&r, "tag")
}

// noTemps fails when a commit left a temp or backup next to dir's files.
func noTemps(t *testing.T, dir string) {
	t.Helper()
	ents, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range ents {
		if strings.Contains(e.Name(), tempMarker) {
			t.Errorf("leftover %s", e.Name())
		}
	}
}

// TestInPlaceFailuresKeepFile: every failure of an in-place rewrite of a
// hard-linked file (disk full or I/O error mid-copy, while writing the
// temp, a failed snapshot, a no-op edit) leaves both names byte-identical
// and no temp or backup behind (CT27-2, CT27-8).
func TestInPlaceFailuresKeepFile(t *testing.T) {
	t.Parallel()
	const orig = "alpha\nbeta\ngamma\n"
	cases := []struct {
		name string
		hook string
		herr error
		snap error
		edit EditItem
		want Code
	}{
		{"enospc mid-copy", "mid-copy", syscall.ENOSPC, nil, strEdit("beta", strings.Repeat("B", 1<<17)), CodeDiskFull},
		{"eio mid-copy", "mid-copy", syscall.EIO, nil, strEdit("beta", strings.Repeat("B", 1<<17)), CodeInternal},
		{"enospc mid-temp", "mid-temp", syscall.ENOSPC, nil, strEdit("beta", "BETA"), CodeDiskFull},
		{"snapshot fails", "", nil, errors.New("snapshot store down"), strEdit("beta", "BETA"), CodeInternal},
		{"no-op", "", nil, nil, strEdit("beta", "beta"), CodeInvalidArgs},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			v, p, other := linkedEnv(t, orig)
			v.e.snapshot = func(context.Context, string, *os.File) error { return c.snap }
			v.e.hook = func(point string) error {
				if point == c.hook {
					return c.herr
				}
				return nil
			}
			tag := approvedTag(t, v, "f.txt")
			_, err := v.e.Edit(context.Background(), EditArgs{Path: "f.txt", Tag: tag, Edits: []EditItem{c.edit}}, Opts{Approved: true})
			wantCode(t, err, c.want)
			if got := v.get(t, "f.txt"); got != orig {
				t.Errorf("file changed: %q", got)
			}
			if !sameFile(t, p, other) {
				t.Error("hard link broken")
			}
			noTemps(t, v.root)
		})
	}
}

// TestInPlaceEdit: an approved edit of a hard-linked file rewrites the
// shared inode (both names see it), hands the old bytes to Snapshot before
// the first byte changes, and leaves no temp or backup.
func TestInPlaceEdit(t *testing.T) {
	t.Parallel()
	v, p, other := linkedEnv(t, "alpha\nbeta\n")
	var snap []byte
	v.e.snapshot = func(_ context.Context, resolved string, old *os.File) error {
		var err error
		if snap, err = io.ReadAll(io.NewSectionReader(old, 0, 1<<20)); err != nil {
			return err
		}
		if cur := readFixture(t, resolved); !bytes.Equal(cur, snap) {
			t.Errorf("target already changed at snapshot: %q", cur)
		}
		return nil
	}
	ctx := context.Background()
	_, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: approvedTag(t, v, "f.txt"), Edits: []EditItem{strEdit("beta", "BETA\ngamma")}}, Opts{})
	wantCode(t, err, CodeOutsideWorkspace) // hardlinked_file needs approval
	res, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: approvedTag(t, v, "f.txt"), Edits: []EditItem{strEdit("beta", "BETA\ngamma")}}, Opts{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	if res.Meta["inplace"] != "hardlink" || string(snap) != "alpha\nbeta\n" {
		t.Errorf("meta %v, snapshot %q", res.Meta, snap)
	}
	if got := string(readFixture(t, other)); got != "alpha\nBETA\ngamma\n" || !sameFile(t, p, other) {
		t.Errorf("other name: %q", got)
	}
	noTemps(t, v.root)
}

// TestInPlaceLargeFile: hard-linked files above inMemoryMax take the
// streaming line-range path; a real edit is applied to both names, and a
// no-op edit fails with the file untouched (CT27-2 repro: 65 MiB).
func TestInPlaceLargeFile(t *testing.T) {
	if testing.Short() {
		t.Skip("writes 65 MiB files")
	}
	t.Parallel()
	line := strings.Repeat("x", 63) + "\n"
	body := strings.Repeat(line, (inMemoryMax+1<<20)/len(line))
	v, p, other := linkedEnv(t, "first\n"+body+"last\n")
	ctx := context.Background()
	old := time.Now().Add(-time.Hour) // a racy stat tag (§5.5) would need a full hash
	if err := os.Chtimes(p, old, old); err != nil {
		t.Fatal(err)
	}
	one, last := 1, len(body)/len(line)+2
	noop := "last"
	_, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: approvedTag(t, v, "f.txt"), Edits: []EditItem{{StartLine: &last, EndLine: &last, NewText: &noop}}}, Opts{Approved: true})
	wantCode(t, err, CodeInvalidArgs)
	if fi := mustStat(t, p); fi.Size() != int64(len(body)+11) {
		t.Fatalf("no-op edit changed the size to %d", fi.Size())
	}
	first := "FIRST"
	res, err := v.e.Edit(ctx, EditArgs{Path: "f.txt", Tag: approvedTag(t, v, "f.txt"), Edits: []EditItem{{StartLine: &one, EndLine: &one, NewText: &first}}}, Opts{Approved: true})
	if err != nil {
		t.Fatal(err)
	}
	got := readFixture(t, other)
	if !strings.HasPrefix(string(got), "FIRST\n"+line) || len(got) != len(body)+11 || !sameFile(t, p, other) {
		t.Errorf("in-place large edit: %d bytes, meta %v", len(got), res.Meta)
	}
	noTemps(t, v.root)
}

// TestEditLockAlias is CT27-5: edits through two names of one file (a
// symlink, and two hard links) are serialized: with one tag, exactly one
// succeeds and the other is stale_read; no update is lost.
func TestEditLockAlias(t *testing.T) {
	t.Parallel()
	for _, kind := range []string{"symlink", "hardlink"} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()
			v := newEnv(t)
			p := v.put(t, "f.txt", "a\nb\n")
			var err error
			if kind == "symlink" {
				err = os.Symlink("f.txt", filepath.Join(v.root, "link.txt"))
			} else {
				err = os.Link(p, filepath.Join(v.root, "link.txt"))
			}
			if err != nil {
				t.Skipf("%s: %v", kind, err)
			}
			ctx, o := context.Background(), Opts{Approved: true}
			for i := range 150 {
				writeFile(t, p, "a\nb\n")
				tag := approvedTag(t, v, "f.txt")
				var wg sync.WaitGroup
				errs := make([]error, 2)
				for j, a := range []EditArgs{
					{Path: "f.txt", Tag: tag, Edits: []EditItem{strEdit("a", "A")}},
					{Path: "link.txt", Tag: tag, Edits: []EditItem{strEdit("b", "B")}},
				} {
					wg.Go(func() { _, errs[j] = v.e.Edit(ctx, a, o) })
				}
				wg.Wait()
				got := v.get(t, "f.txt")
				ok := (errs[0] == nil) != (errs[1] == nil)
				if !ok || (got != "A\nb\n" && got != "a\nB\n") {
					t.Fatalf("iteration %d: errs %v, file %q", i, errs, got)
				}
			}
		})
	}
}

// TestPathLockAliases is CT27-5 without timing luck: while one name of a
// file holds its edit lock, locking any other name of it (symlink, hard
// link) blocks; an unrelated file does not.
func TestPathLockAliases(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "f.txt", "a\n")
	h := v.put(t, "h.txt", "a\n")
	if err := os.Symlink("f.txt", filepath.Join(v.root, "sym.txt")); err != nil {
		t.Skipf("Symlink: %v", err)
	}
	if err := os.Link(h, filepath.Join(v.root, "hard.txt")); err != nil {
		t.Skipf("Link: %v", err)
	}
	ctx := context.Background()
	for _, c := range []struct {
		held, other string
		block       bool
	}{{"f.txt", "sym.txt", true}, {"h.txt", "hard.txt", true}, {"f.txt", "h.txt", false}} {
		unlock := v.e.pathLock(ctx, c.held)
		got := make(chan struct{})
		go func() {
			v.e.pathLock(ctx, c.other)()
			close(got)
		}()
		select {
		case <-got:
			if c.block {
				t.Errorf("%s: not serialized with %s", c.other, c.held)
			}
		case <-time.After(100 * time.Millisecond):
			if !c.block {
				t.Errorf("%s: blocked by %s", c.other, c.held)
			}
		}
		unlock()
		<-got
	}
}

// TestSetgidPreserved is CT27-9: an edit keeps setgid (and reports
// mode_preserved), which chown and a plain permission copy would drop.
func TestSetgidPreserved(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no setgid on Windows")
	}
	t.Parallel()
	v := newEnv(t)
	p := v.put(t, "f.txt", "a\n")
	if err := os.Chmod(p, 0o640|os.ModeSetgid); err != nil || mustStat(t, p).Mode()&os.ModeSetgid == 0 {
		t.Skipf("setgid not settable here: %v", err)
	}
	res, err := v.e.Edit(context.Background(), EditArgs{Path: "f.txt", Tag: v.tag(t, "f.txt"), Edits: []EditItem{strEdit("a", "b")}}, Opts{})
	if err != nil {
		t.Fatal(err)
	}
	if m := mustStat(t, p).Mode(); m&os.ModeSetgid == 0 || m.Perm() != 0o640 || res.Meta["mode_preserved"] != true {
		t.Errorf("mode %v, meta %v", m, res.Meta)
	}
}

// TestCleanOrphanTempsPattern is CT27-10: only ".<name>.damnbot-<12
// base32>" crash temps are removed; look-alikes and backups stay.
func TestCleanOrphanTempsPattern(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	gone := []string{".f.txt.damnbot-abcdefgh2345", "sub/.g.go.damnbot-zzzzzzzzzzzz"}
	kept := []string{".notes.damnbot-readme", ".f.txt.damnbot-bak-abcdefgh2345", "f.damnbot-abcdefgh2345", ".f.txt.damnbot-ABCDEFGH2345", ".f.txt.damnbot-abcdefgh23456"}
	for _, n := range append(gone, kept...) {
		v.put(t, n, "x")
	}
	n, err := v.e.CleanOrphanTemps(context.Background())
	if err != nil || n != len(gone) {
		t.Fatalf("removed %d, %v", n, err)
	}
	for _, k := range kept {
		if _, err := os.Lstat(filepath.Join(v.root, k)); err != nil {
			t.Errorf("%s removed", k)
		}
	}
}

func strEdit(old, nw string) EditItem { return EditItem{OldString: &old, NewString: &nw} }

func sameFile(t *testing.T, a, b string) bool {
	t.Helper()
	return os.SameFile(mustStat(t, a), mustStat(t, b))
}
