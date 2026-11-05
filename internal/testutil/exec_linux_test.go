//go:build linux

package testutil

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

// TestExecRetryReleasesRealETXTBSY proves the helper against the Linux kernel:
// the first execve runs while the writer is open and the retry runs after it
// closes.
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

// TestExecRetryHonoursAttemptLimit always fails with a real ETXTBSY: one call
// plus exactly execRetries retries, and the last error is returned.
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
