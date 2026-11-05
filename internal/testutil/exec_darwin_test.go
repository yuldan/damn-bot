//go:build darwin

package testutil

import (
	"errors"
	"fmt"
	"os/exec"
	"syscall"
	"testing"
)

// TestExecRetryLeavesDarwinExecAlone is the explicit platform control for
// macOS: unlike Linux, Darwin does not deny execve for a file this process
// holds open for writing (full-tier main 994db9fc: the Linux fixture ran on the
// first attempt, no ETXTBSY). ExecRetry must therefore pass that success
// through exactly once instead of fabricating a retry, while the injected
// *fs.PathError tests and TestExecRetryPassesOtherErrors keep the retry trigger
// (ETXTBSY only) and pass-through covered on this platform too.
func TestExecRetryLeavesDarwinExecAlone(t *testing.T) {
	_, path := busyExecutable(t)
	attempts := 0
	err := ExecRetry(t.Context(), func() error {
		attempts++
		rerr := exec.CommandContext(t.Context(), path).Run() // #nosec G204 -- fixture written by this test
		if errors.Is(rerr, syscall.ETXTBSY) {
			t.Fatalf("darwin exec of an open writer returned ETXTBSY; the fixture assumption changed")
		}
		if rerr != nil {
			return fmt.Errorf("exec %s: %w", path, rerr)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ExecRetry: %v", err)
	}
	if attempts != 1 {
		t.Fatalf("attempts = %d, want 1 (darwin does not raise ETXTBSY here)", attempts)
	}
}
