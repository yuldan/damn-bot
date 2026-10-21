package main

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/sandbox"
)

// TestUndoDryRunAncestorSwapNeverReadsOutside (REVIEW-F2-SEC5-3): swapping
// the action's parent directory for a symlink to an outside directory
// between the sandbox check and the open must never let --dry-run read
// outside the journaled root. Bounded by a deadline.
func TestUndoDryRunAncestorSwapNeverReadsOutside(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	sb, err := sandbox.New(&sandbox.Config{Roots: []string{root}, Home: root})
	if err != nil {
		t.Fatal(err)
	}
	inside := filepath.Join(root, "real")
	dir := filepath.Join(root, "sub")
	if err := os.Mkdir(inside, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(dir, 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("insider"), 0o600); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "f.txt"), []byte("TOPSECRET"), 0o600); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "f.txt")
	if data, refusal := readInSandbox(t.Context(), sb, path); refusal != "" || string(data) != "insider" {
		t.Fatalf("pre-race read = %q, %q; want insider", data, refusal)
	}

	done := make(chan struct{})
	errc := make(chan error, 1)
	var wg sync.WaitGroup
	wg.Go(func() { swapDir(dir, dir+".parked", outside, done, errc) })

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); {
		select {
		case err := <-errc:
			close(done)
			wg.Wait()
			t.Fatal(err)
		default:
		}
		if data, _ := readInSandbox(t.Context(), sb, path); bytes.Contains(data, []byte("TOPSECRET")) {
			close(done)
			wg.Wait()
			t.Fatalf("dry-run read outside the sandbox: %q", data)
		}
	}
	close(done)
	wg.Wait()
}

// swapDir alternates dir between a real directory and a symlink to target,
// moving the directory to parked each time; it runs until done.
func swapDir(dir, parked, target string, done <-chan struct{}, errc chan<- error) {
	symlinked := false
	for {
		select {
		case <-done:
			return
		default:
		}
		var err error
		if symlinked {
			err = unsymlinkDir(dir, parked)
		} else {
			err = symlinkDir(dir, parked, target)
		}
		if err != nil {
			select {
			case errc <- err:
			default:
			}
			return
		}
		symlinked = !symlinked
		runtime.Gosched()
	}
}

// symlinkDir moves dir to parked and leaves a symlink to target in its place.
//
//nolint:wrapcheck // the test surfaces the OS cause to the swapper.
func symlinkDir(dir, parked, target string) error {
	if err := os.Rename(dir, parked); err != nil {
		return err
	}
	if err := os.Symlink(target, dir); err != nil {
		return errors.Join(err, os.Rename(parked, dir))
	}
	return nil
}

// unsymlinkDir removes the symlink at dir and moves the parked directory back.
//
//nolint:wrapcheck // the test surfaces the OS cause to the swapper.
func unsymlinkDir(dir, parked string) error {
	if err := os.Remove(dir); err != nil {
		return err
	}
	return os.Rename(parked, dir)
}
