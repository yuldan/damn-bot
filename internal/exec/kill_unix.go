//go:build unix

package exec

import (
	"errors"
	"fmt"
	"syscall"
)

// killPid sends SIGKILL to one swept process; it may be gone already.
func killPid(pid int) error {
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil && !errors.Is(err, syscall.ESRCH) {
		return fmt.Errorf("exec: kill %d: %w", pid, err)
	}
	return nil
}

// isBrokenPipe: the child stopped reading its stdin.
func isBrokenPipe(err error) bool { return errors.Is(err, syscall.EPIPE) }
