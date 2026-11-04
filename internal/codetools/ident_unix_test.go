//go:build unix

package codetools

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"
)

// TestIdentifyDistinctFilesSameContentUnix is the identity contract on
// Unix: dev+inode separates two files even when size and mtime agree, and
// every name of one object shares it.
func TestIdentifyDistinctFilesSameContentUnix(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	a := v.put(t, "a.txt", "a\n")
	b := v.put(t, "b.txt", "a\n")
	ida, err := identify(a)
	if err != nil {
		t.Fatalf("identify(%s): %v", a, err)
	}
	idb, err := identify(b)
	if err != nil {
		t.Fatalf("identify(%s): %v", b, err)
	}
	if ida == idb {
		t.Fatalf("distinct files share identity %+v", ida)
	}
	if ida == (fileID{}) {
		t.Fatalf("identify(%s) returned the zero identity", a)
	}
	link := filepath.Join(v.root, "a-link.txt")
	if lerr := os.Link(a, link); lerr != nil {
		t.Fatalf("Link: %v", lerr)
	}
	idl, err := identify(link)
	if err != nil {
		t.Fatalf("identify(%s): %v", link, err)
	}
	if idl != ida {
		t.Fatalf("hard link identity %+v != %+v", idl, ida)
	}
}

// TestIdentifyMissingFileUnix is the fail-closed contract: an identity
// that cannot be read is a typed error, never a size+mtime tuple.
func TestIdentifyMissingFileUnix(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	missing := filepath.Join(v.root, "missing.txt")
	_, err := identify(missing)
	if _, ok := errors.AsType[*IdentityError](err); !ok {
		t.Fatalf("identify(%s) error = %v, want *IdentityError", missing, err)
	}
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("identify(%s) error = %v, want fs.ErrNotExist in chain", missing, err)
	}
}

// TestPathLockDistinctFilesSameContentUnix keeps the old Unix behavior:
// the lock keys of byte-identical unrelated files are disjoint.
func TestPathLockDistinctFilesSameContentUnix(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	v.put(t, "f.txt", "a\n")
	v.put(t, "h.txt", "a\n")
	ctx := context.Background()
	ka, err := v.e.lockKeys(ctx, "f.txt")
	if err != nil {
		t.Fatalf("lockKeys(f.txt): %v", err)
	}
	kh, err := v.e.lockKeys(ctx, "h.txt")
	if err != nil {
		t.Fatalf("lockKeys(h.txt): %v", err)
	}
	if slices.ContainsFunc(ka, func(k string) bool { return slices.Contains(kh, k) }) {
		t.Fatalf("lock keys overlap: %q vs %q", ka, kh)
	}
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
		if lerr := <-done; lerr != nil {
			t.Fatalf("pathLock(h.txt): %v", lerr)
		}
		t.Fatal("h.txt: blocked by f.txt despite distinct identities")
	}
	unlock()
}

// TestPathLockSameFileTwoPathsUnix keeps the alias contract: two names of
// one inode serialize.
func TestPathLockSameFileTwoPathsUnix(t *testing.T) {
	t.Parallel()
	v := newEnv(t)
	p := v.put(t, "f.txt", "a\n")
	link := filepath.Join(v.root, "hard.txt")
	if err := os.Link(p, link); err != nil {
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
