//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
)

// readSecretNoEcho disables terminal echo around read when stdout is a TTY.
func readSecretNoEcho(stdout io.Writer, read func() (string, error)) (string, error) {
	if !configTerminal(stdout) {
		return read()
	}
	if err := stty("-echo"); err != nil {
		return "", err
	}
	defer func() {
		if err := stty("echo"); err != nil {
			return
		}
	}()
	return read()
}

func stty(arg string) error {
	cmd := exec.CommandContext(context.Background(), "stty", arg) // #nosec G204 -- fixed stty utility
	cmd.Stdin = os.Stdin
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("stty %s: %w", arg, err)
	}
	return nil
}
