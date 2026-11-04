//go:build unix

package testutil

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// busyExecutable writes a script and keeps it open for writing, which is the
// exact state that makes execve fail with ETXTBSY.
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

// TestExecRetryHonoursAttemptLimit always fails with ETXTBSY: one call plus
// exactly execRetries retries, and the last error is returned verbatim.
func TestExecRetryHonoursAttemptLimit(t *testing.T) {
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
	if elapsed := time.Since(start); elapsed < 25*time.Millisecond {
		t.Fatalf("retries did not back off: %s", elapsed)
	}
}

// TestExecRetryPassesOtherErrors: a missing executable is not ETXTBSY and must
// fail on the first attempt.
func TestExecRetryPassesOtherErrors(t *testing.T) {
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		missing := filepath.Join(t.TempDir(), "missing")
		if rerr := exec.CommandContext(t.Context(), missing).Run(); rerr != nil { // #nosec G204 -- deliberate miss
			return fmt.Errorf("exec %s: %w", missing, rerr)
		}
		return nil
	})
	if !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("err = %v, want ErrNotExist", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// TestExecRetryStopsOnCancel: retries stop as soon as the context is done, and
// the cancellation is reported.
func TestExecRetryStopsOnCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	err := ExecRetry(ctx, func() error {
		attempts++
		cancel()
		return &fs.PathError{Op: "fork/exec", Path: "fixture", Err: syscall.ETXTBSY}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// TestExecRetryCancelledBeforeStart: a dead context runs nothing.
func TestExecRetryCancelledBeforeStart(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	attempts := 0
	err := ExecRetry(ctx, func() error {
		attempts++
		return nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 0 {
		t.Fatalf("err = %v, attempts = %d; want context.Canceled and 0", err, attempts)
	}
}

// TestExecRetryWrappedETXTBSY: os/exec wraps the errno in *fs.PathError; the
// helper must see through it and retry exactly once.
func TestExecRetryWrappedETXTBSY(t *testing.T) {
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		if attempts == 1 {
			return &fs.PathError{Op: "fork/exec", Path: "fixture", Err: syscall.ETXTBSY}
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err = %v, attempts = %d; want nil and 2", err, attempts)
	}
}
