package codetools

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"
)

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

// TestCleanOrphanTempsPattern is CT27-10: only ".<name>.damnbot-<12
// base32>" crash temps are removed; look-alikes and backups stay.
func TestCleanOrphanTempsPattern(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	gone := []string{".f.txt.damnbot-abcdefgh2345", "sub/.g.go.damnbot-zzzzzzzzzzzz"}
	kept := []string{".notes.damnbot-readme", ".f.txt.damnbot-bak-abcdefgh2345", "f.damnbot-abcdefgh2345", ".f.txt.damnbot-ZYXWVUTSRQPO", ".f.txt.damnbot-abcdefgh23456"}
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
