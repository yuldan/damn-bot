//go:build unix

package exec

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"syscall"
)

// startBackground starts argv in its own process group with a pipe stdin
// and ring-buffered combined output.
func startBackground(sp spawnSpec) (*cmdHandle, error) {
	//nolint:gosec // argv comes from the approved shell call, not interpolation
	cmd := exec.CommandContext(context.Background(), sp.argv[0], sp.argv[1:]...)
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
			return syscall.Kill(-cmd.Process.Pid, sig)
		},
	}, nil
}
