//go:build unix && !minimal

package exec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// BackgroundSupported is true where startBackground can own the process tree.
const BackgroundSupported = true

// startBackground starts argv in its own process group with a pipe stdin
// and ring-buffered combined output.
func startBackground(ctx context.Context, sp spawnSpec) (*cmdHandle, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("exec: background start: %w", err)
	}
	//nolint:gosec // argv comes from the approved shell call, not interpolation
	cmd := exec.CommandContext(context.WithoutCancel(ctx), sp.argv[0], sp.argv[1:]...)
	cmd.Dir = sp.dir
	cmd.Env = sp.env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("exec: background stdin: %w", err)
	}
	cmd.Stdout, cmd.Stderr = sp.out, sp.out
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("exec: background start: %w", err)
	}
	return &cmdHandle{
		pid: cmd.Process.Pid, pgid: cmd.Process.Pid, stdin: stdin,
		wait: func() (int, string, error) {
			err := cmd.Wait()
			if exitErr, ok := errors.AsType[*exec.ExitError](err); ok {
				code := exitErr.ExitCode()
				if ws, ok := exitErr.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
					return -1, ws.Signal().String(), nil
				}
				return code, "", nil
			}
			return 0, "", fmt.Errorf("exec: background wait: %w", err)
		},
		kill: func(signal string) error {
			sig := syscall.SIGTERM
			switch signal {
			case "INT":
				sig = syscall.SIGINT
			case "KILL":
				sig = syscall.SIGKILL
			}
			err := syscall.Kill(-cmd.Process.Pid, sig)
			return groupKillError(cmd.Process.Pid, err, errors.Is(err, syscall.EPERM) && groupLive(cmd.Process.Pid))
		},
	}, nil
}

// groupKillError classifies a failed group signal. A group that is already
// gone is not a failure: Linux reports ESRCH for an empty group, macOS
// reports EPERM while the leader is an unreaped zombie (killpg1 skips SZOMB
// and POSIX maps "nothing found" to EPERM, ADR-105 butir 18). EPERM for a
// group that still has a live member stays an error.
func groupKillError(pgid int, err error, alive bool) error {
	if err == nil || errors.Is(err, syscall.ESRCH) || (errors.Is(err, syscall.EPERM) && !alive) {
		return nil
	}
	return fmt.Errorf("exec: kill -%d: %w", pgid, err)
}
