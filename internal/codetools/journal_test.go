package codetools

import (
	"context"
	"errors"
	"io"
	"io/fs"
	"os"
	"runtime"
	"testing"
)

// sideBytes reads an open commit side through ReadAt, as the journal does.
func sideBytes(t *testing.T, f *os.File) []byte {
	t.Helper()
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	b := make([]byte, fi.Size())
	if _, err := f.ReadAt(b, 0); err != nil && !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	return b
}

// TestJournalHookSeesBothImages: the hook runs once per committing write
// with the current bytes and the durable new bytes, before the target is
// replaced (SPEC §4.3 langkah 8); creation has no before side.
func TestJournalHookSeesBothImages(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	p := v.put(t, "a.txt", "one\r\ntwo\r\n")
	if err := os.Chmod(p, 0o400); err != nil {
		t.Fatal(err)
	}
	var calls int
	var before, after string
	var mode fs.FileMode
	hook := func(_ context.Context, c *Commit) error {
		calls++
		if c.Path != p {
			t.Errorf("path = %q, want %q", c.Path, p)
		}
		if cur := v.get(t, "a.txt"); cur != "one\r\ntwo\r\n" {
			t.Errorf("target replaced before the hook: %q", cur)
		}
		before, after, mode = string(sideBytes(t, c.Before)), string(sideBytes(t, c.After)), c.Mode
		return nil
	}
	old, repl := "two", "three"
	_, err := v.e.Edit(context.Background(), EditArgs{Path: "a.txt", Tag: v.tag(t, "a.txt"), Edits: []EditItem{{OldString: &old, NewString: &repl}}}, Opts{Journal: hook})
	if err != nil {
		t.Fatal(err)
	}
	// Windows models only the read-only bit, so a 0o400 file reads back as
	// 0o444 there while POSIX keeps the exact mode; assert the mode the file
	// system can actually hold for a read-only file.
	want := fs.FileMode(0o400)
	if runtime.GOOS == "windows" {
		want = 0o444
	}
	if calls != 1 || before != "one\r\ntwo\r\n" || after != "one\r\nthree\r\n" || mode.Perm() != want {
		t.Fatalf("calls=%d before=%q after=%q mode=%v", calls, before, after, mode)
	}
	var created bool
	_, err = v.e.Write(context.Background(), WriteArgs{Path: "new.txt", Content: "fresh\n"}, Opts{Journal: func(_ context.Context, c *Commit) error {
		created = c.Before == nil && c.Mode == 0 && string(sideBytes(t, c.After)) == "fresh\n"
		return nil
	}})
	if err != nil || !created {
		t.Fatalf("create hook: created=%v err=%v", created, err)
	}
}

// TestJournalHookErrorAborts: a failed journal record leaves the target
// byte-identical and no temp behind.
func TestJournalHookErrorAborts(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "a.txt", "keep\n")
	fail := func(context.Context, *Commit) error { return errors.New("journal down") }
	_, err := v.e.Write(context.Background(), WriteArgs{Path: "a.txt", Content: "lost\n", Tag: new(v.tag(t, "a.txt"))}, Opts{Journal: fail})
	wantCode(t, err, CodeInternal)
	if got := v.get(t, "a.txt"); got != "keep\n" {
		t.Fatalf("target = %q after a failed journal", got)
	}
	if _, err = v.e.Write(context.Background(), WriteArgs{Path: "b.txt", Content: "x"}, Opts{Journal: fail}); err == nil {
		t.Fatal("create succeeded without a journal record")
	}
	if _, serr := os.Stat(v.root + "/b.txt"); !errors.Is(serr, fs.ErrNotExist) {
		t.Fatalf("created file exists after a failed journal: %v", serr)
	}
	entries, err := os.ReadDir(v.root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("leftover temps: %v %v", entries, err)
	}
}
