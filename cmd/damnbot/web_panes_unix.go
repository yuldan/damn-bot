//go:build !minimal && !windows

package main

import (
	"fmt"
	"os/exec"
	"syscall"
)

// setPaneProcessGroup puts the shell in its own process group so Close reaches
// every child it spawned.
func setPaneProcessGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killPaneProcessGroup terminates the whole pane process group.
func killPaneProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL); err != nil {
		if kerr := cmd.Process.Kill(); kerr != nil {
			return fmt.Errorf("kill pane process group: %w", kerr)
		}
	}
	return nil
}
