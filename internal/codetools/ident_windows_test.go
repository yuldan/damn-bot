//go:build windows

package codetools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestPathLockDistinctFilesSameContentWindows is the regression test for
// the Windows identity bug: f.txt and h.txt are two different files with
// the same bytes, size and (possibly) creation tick; they must not share
// an edit lock.
func TestPathLockDistinctFilesSameContentWindows(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "f.txt", "a\n")
	v.put(t, "h.txt", "a\n")
	ctx := context.Background()
	unlock, err := v.e.pathLock(ctx, "f.txt")
	if err != nil {
		t.Fatalf("pathLock(f.txt): %v", err)
	}
	done := make(chan error, 1)
	go func() {
		u, lerr := v.e.pathLock(ctx, "h.txt")
		if lerr == nil {
			u()
		}
		done <- lerr
	}()
	select {
	case lerr := <-done:
		if lerr != nil {
			t.Fatalf("pathLock(h.txt): %v", lerr)
		}
	case <-time.After(5 * time.Second):
		unlock()
		<-done
		t.Fatal("h.txt: blocked by f.txt despite distinct identities")
	}
	unlock()
}

// TestPathLockSameFileTwoPathsWindows: a hard link to the same file must
// share the edit lock through volume serial + file index.
func TestPathLockSameFileTwoPathsWindows(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	p := v.put(t, "f.txt", "a\n")
	if err := os.Link(p, filepath.Join(v.root, "hard.txt")); err != nil {
		t.Fatalf("Link: %v", err)
	}
	ctx := context.Background()
	unlock, err := v.e.pathLock(ctx, "f.txt")
	if err != nil {
		t.Fatalf("pathLock(f.txt): %v", err)
	}
	done := make(chan error, 1)
	go func() {
		u, lerr := v.e.pathLock(ctx, "hard.txt")
		if lerr == nil {
			u()
		}
		done <- lerr
	}()
	select {
	case lerr := <-done:
		unlock()
		t.Fatalf("hard.txt: not serialized with f.txt (err %v)", lerr)
	case <-time.After(250 * time.Millisecond):
	}
	unlock()
	if lerr := <-done; lerr != nil {
		t.Fatalf("pathLock(hard.txt): %v", lerr)
	}
}

// TestIdentifyHandleFailureWindows is the fail-closed control: when the
// OS refuses the handle, identify returns a typed error and no fake
// identity. A NUL in the name makes CreateFile fail with EINVAL (never a
// missing-file error), so no share-mode or filesystem behaviour is
// assumed. The same branch serves a real sharing violation or ACL denial.
func TestIdentifyHandleFailureWindows(t *testing.T) {
	t.Parallel()
	const p = "bad\x00name.txt"
	id, err := identify(p)
	if _, ok := errors.AsType[*IdentityError](err); !ok {
		t.Fatalf("identify(%q) = %+v, %v; want *IdentityError", p, id, err)
	}
	if id != (fileID{}) {
		t.Fatalf("identify(%q) returned a fake identity %+v", p, id)
	}
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("identify(%q): %v must not look like a missing file", p, err)
	}
}
