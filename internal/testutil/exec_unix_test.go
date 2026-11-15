//go:build unix

package testutil

// The retry path of ExecRetry is identical on every unix, but the kernel
// evidence differs: Linux returns ETXTBSY while a fixture writer is still open,
// whereas macOS (like the BSDs) commonly allows execve and starts the program
// anyway. The real-kernel proofs therefore live in exec_linux_test.go
// (//go:build linux); here the errno is injected exactly as os/exec wraps it,
// so darwin/BSD CI still covers the attempt limit, backoff, error
// pass-through, and cancellation every test caller depends on. ExecRetry is
// not platform-gated, so neither is this contract.

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

// injectedETXTBSY is the error os/exec returns for a busy executable: the errno
// wrapped in *fs.PathError. Injecting it keeps the retry contract testable on
// kernels whose execve does not enforce the Linux ETXTBSY window.
func injectedETXTBSY() error {
	return &fs.PathError{Op: "fork/exec", Path: "fixture", Err: syscall.ETXTBSY}
}

// execBackoffTotal is the sum of the waits before the final attempt:
// execBackoff<<0 through execBackoff<<(execRetries-1).
func execBackoffTotal() time.Duration {
	var total time.Duration
	for attempt := range execRetries {
		total += execBackoff << attempt
	}
	return total
}

// TestExecRetryWrappedETXTBSY: os/exec wraps the errno in *fs.PathError; the
// helper must see through it and retry exactly once.
func TestExecRetryWrappedETXTBSY(t *testing.T) {
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		if attempts == 1 {
			return injectedETXTBSY()
		}
		return nil
	})
	if err != nil || attempts != 2 {
		t.Fatalf("err = %v, attempts = %d; want nil and 2", err, attempts)
	}
}

// TestExecRetryHonoursAttemptLimit always fails with ETXTBSY: one call plus
// exactly execRetries retries, and the last error is returned verbatim.
func TestExecRetryHonoursAttemptLimit(t *testing.T) {
	last := injectedETXTBSY()
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		return last
	})
	if !errors.Is(err, syscall.ETXTBSY) || !errors.Is(err, last) {
		t.Fatalf("err = %v, want the final ETXTBSY preserved", err)
	}
	if attempts != 1+execRetries {
		t.Fatalf("attempts = %d, want %d", attempts, 1+execRetries)
	}
}

// TestExecRetryBacksOff: retry n waits execBackoff<<n before calling fn again,
// so a busy executable is not hammered.
func TestExecRetryBacksOff(t *testing.T) {
	var stamps []time.Time
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		stamps = append(stamps, time.Now())
		return injectedETXTBSY()
	})
	if !errors.Is(err, syscall.ETXTBSY) {
		t.Fatalf("err = %v, want ETXTBSY", err)
	}
	if attempts != 1+execRetries || len(stamps) != attempts {
		t.Fatalf("attempts = %d (stamps %d), want %d", attempts, len(stamps), 1+execRetries)
	}
	for i := 1; i < len(stamps); i++ {
		want := execBackoff << (i - 1)
		if gap := stamps[i].Sub(stamps[i-1]); gap < want {
			t.Fatalf("gap before attempt %d = %s, want >= %s", i+1, gap, want)
		}
	}
}

// TestExecRetryNeverRetriesOtherErrors is the negative control: only ETXTBSY is
// transient, so any other failure is returned unchanged on the first attempt.
func TestExecRetryNeverRetriesOtherErrors(t *testing.T) {
	cases := map[string]error{
		"permission denied": &fs.PathError{Op: "fork/exec", Path: "fixture", Err: syscall.EACCES},
		"missing fixture":   &fs.PathError{Op: "fork/exec", Path: "fixture", Err: fs.ErrNotExist},
		"bad executable":    &fs.PathError{Op: "fork/exec", Path: "fixture", Err: syscall.ENOEXEC},
		"bare EAGAIN":       syscall.EAGAIN,
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			attempts := 0
			err := ExecRetry(t.Context(), func() error {
				attempts++
				return want
			})
			if !errors.Is(err, want) {
				t.Fatalf("err = %v, want the injected error unchanged", err)
			}
			if attempts != 1 {
				t.Fatalf("attempts = %d, want 1 (non-ETXTBSY errors are final)", attempts)
			}
		})
	}
}

// TestExecRetryPassesRealMissingExecutable: os/exec's own ENOENT wrapping is
// not ETXTBSY on any unix and must fail on the first attempt.
func TestExecRetryPassesRealMissingExecutable(t *testing.T) {
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
		return injectedETXTBSY()
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1", attempts)
	}
}

// TestExecRetryStopsDuringBackoff: cancellation interrupts the backoff wait
// instead of sleeping through it.
func TestExecRetryStopsDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	attempts := 0
	err := ExecRetry(ctx, func() error {
		attempts++
		if attempts == 1 {
			time.AfterFunc(time.Millisecond, cancel)
		}
		return injectedETXTBSY()
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
