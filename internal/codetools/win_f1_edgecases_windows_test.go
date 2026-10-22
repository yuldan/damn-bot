//go:build windows

package codetools

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestWindowsF1EdgeCases covers the EC-WIN entries the roadmap places at F1
// that need a native Windows mechanism; EC-WIN-014 is the sharing-violation
// retry of the atomic replace.
func TestWindowsF1EdgeCases(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if g.Item(t, "EC-WIN-014", "F1", "covered") {
		t.Run("EC-WIN-014", ecWin014)
	}
}

// lockFile opens path without sharing, the way an antivirus scanner or a
// process still loading the file holds it.
func lockFile(t *testing.T, path string) windows.Handle {
	t.Helper()
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ, 0, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatalf("lock %s: %v", path, err)
	}
	return h
}

// EC-WIN-014: a rename and a delete that hit a transient sharing violation
// are retried and succeed once the holder releases the file; a holder that
// never releases still fails after the bounded retry instead of hanging.
func ecWin014(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	read := func(name string) string {
		t.Helper()
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		return string(b)
	}

	// Rename: the destination is locked, then released during the retry.
	write("new.txt", "new")
	write("old.txt", "old")
	h := lockFile(t, filepath.Join(dir, "old.txt"))
	done := make(chan error, 1)
	go func() { done <- renameReplace(filepath.Join(dir, "new.txt"), filepath.Join(dir, "old.txt")) }()
	time.Sleep(2 * shareRetryBackoff)
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("rename after the holder released: %v", err)
	}
	if got := read("old.txt"); got != "new" {
		t.Fatalf("replaced content = %q", got)
	}

	// Delete: removeAt has the same retry.
	write("locked.txt", "x")
	d, err := os.OpenFile(dir, os.O_RDONLY, 0) //nolint:gosec // G304: test dir
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.Close() }()
	h = lockFile(t, filepath.Join(dir, "locked.txt"))
	done = make(chan error, 1)
	go func() { done <- removeAt(d, "locked.txt") }()
	time.Sleep(2 * shareRetryBackoff)
	if err := windows.CloseHandle(h); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("remove after the holder released: %v", err)
	}

	// Negative control: an unreleased holder fails after the bounded retry.
	write("blocked.txt", "blocked")
	write("src.txt", "src")
	h = lockFile(t, filepath.Join(dir, "blocked.txt"))
	defer func() { _ = windows.CloseHandle(h) }()
	start := time.Now()
	err = renameReplace(filepath.Join(dir, "src.txt"), filepath.Join(dir, "blocked.txt"))
	if err == nil {
		t.Fatal("rename over a held destination succeeded")
	}
	if !errors.Is(err, windows.ERROR_SHARING_VIOLATION) && !errors.Is(err, windows.ERROR_ACCESS_DENIED) {
		t.Fatalf("rename error = %v, want a sharing violation", err)
	}
	if waited := time.Since(start); waited < shareRetryBackoff*(shareRetryAttempts-1) {
		t.Fatalf("retry gave up after %v", waited)
	}
	if got := read("blocked.txt"); got != "blocked" {
		t.Fatalf("destination changed under the holder: %q", got)
	}
}
