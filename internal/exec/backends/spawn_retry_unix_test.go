//go:build unix

package backends

import (
	"context"
	"errors"
	"os"
	"syscall"
	"testing"
)

// TestRetrySpawnRetriesETXTBSY is the regression for the cached capability
// probe failure (nightly 2026-11-10, TestSingularityRunContract): the first
// two forks report ETXTBSY because a write-then-rename fixture was still
// open in a forked child; the third succeeds and must be returned.
func TestRetrySpawnRetriesETXTBSY(t *testing.T) {
	t.Parallel()
	busy := &os.PathError{Op: "fork/exec", Path: "fake-cli", Err: syscall.ETXTBSY}
	attempts := 0
	err := retrySpawn(t.Context(), func() error {
		attempts++
		if attempts < 3 {
			return busy
		}
		return nil
	})
	if err != nil {
		t.Fatalf("retrySpawn = %v, want nil", err)
	}
	if attempts != 3 {
		t.Fatalf("attempts = %d, want 3", attempts)
	}
}

// TestRetrySpawnStopsOnOtherError: only ETXTBSY is transient; a real spawn
// failure must reach the caller after one attempt.
func TestRetrySpawnStopsOnOtherError(t *testing.T) {
	t.Parallel()
	attempts := 0
	want := &os.PathError{Op: "fork/exec", Path: "missing", Err: syscall.ENOENT}
	err := retrySpawn(t.Context(), func() error {
		attempts++
		return want
	})
	if !errors.Is(err, syscall.ENOENT) || attempts != 1 {
		t.Fatalf("retrySpawn = %v after %d attempts, want ENOENT after 1", err, attempts)
	}
}

// TestRetrySpawnStopsOnContext: a cancelled probe must not spend the retry
// budget.
func TestRetrySpawnStopsOnContext(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	attempts := 0
	err := retrySpawn(ctx, func() error {
		attempts++
		return &os.PathError{Op: "fork/exec", Path: "fake-cli", Err: syscall.ETXTBSY}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("retrySpawn = %v, want context.Canceled", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (no backoff after a dead context)", attempts)
	}
}
