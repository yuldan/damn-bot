//go:build linux

package testutil

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// busyExecutable writes a script and keeps it open for writing. Linux execve
// refuses this state with ETXTBSY, which is the kernel behaviour ExecRetry
// absorbs; macOS/BSD execve usually starts the program anyway, so these
// kernel-level proofs are Linux-only. ExecRetry itself is not platform-gated:
// exec_unix_test.go exercises the same retry logic on every unix with the
// errno injected.
func busyExecutable(t *testing.T) (*os.File, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "busy")
	if err := WriteExecutable(path, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	fd, err := os.OpenFile(path, os.O_WRONLY, 0) // #nosec G304 -- fixture path from t.TempDir
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := fd.Close(); cerr != nil && !errors.Is(cerr, os.ErrClosed) {
			t.Errorf("close busy executable: %v", cerr)
		}
	})
	return fd, path
}

// TestExecRetryReleasesRealETXTBSY proves the helper against the kernel: the
// first execve runs while the writer is open and the retry runs after it closes.
func TestExecRetryReleasesRealETXTBSY(t *testing.T) {
	fd, path := busyExecutable(t)
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		rerr := exec.CommandContext(t.Context(), path).Run() // #nosec G204 -- fixture written by this test
		if errors.Is(rerr, syscall.ETXTBSY) && attempts == 1 {
			if cerr := fd.Close(); cerr != nil {
				t.Fatalf("close busy executable: %v", cerr)
			}
		}
		if rerr != nil {
			return fmt.Errorf("exec %s: %w", path, rerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecRetry: %v", err)
	}
	if attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (ETXTBSY then success)", attempts)
	}
}

// TestExecRetryKernelAttemptLimit keeps the writer open, so the real kernel
// returns ETXTBSY for every attempt: one call plus exactly execRetries retries,
// each after its backoff, and the last error is returned verbatim.
func TestExecRetryKernelAttemptLimit(t *testing.T) {
	_, path := busyExecutable(t)
	attempts := 0
	start := time.Now()
	err := ExecRetry(t.Context(), func() error {
		attempts++
		if rerr := exec.CommandContext(t.Context(), path).Run(); rerr != nil { // #nosec G204 -- fixture written by this test
			return fmt.Errorf("exec %s: %w", path, rerr)
		}
		return nil
	})
	if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("err = %v, want ETXTBSY", err)
	}
	if attempts != 1+execRetries {
		t.Fatalf("attempts = %d, want %d", attempts, 1+execRetries)
	}
	if elapsed, want := time.Since(start), execBackoffTotal(); elapsed < want {
		t.Fatalf("retries did not back off: %s, want >= %s", elapsed, want)
	}
}
