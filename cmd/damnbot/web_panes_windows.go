//go:build !minimal && windows

package main

import (
	"fmt"
	"os/exec"
)

// setPaneProcessGroup keeps the default creation flags on Windows.
func setPaneProcessGroup(*exec.Cmd) {}

// killPaneProcessGroup terminates the pane shell.
func killPaneProcessGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	if err := cmd.Process.Kill(); err != nil {
		return fmt.Errorf("kill pane process: %w", err)
	}
	return nil
}
