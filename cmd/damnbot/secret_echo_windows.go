//go:build windows

package main

import (
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/windows"
)

// readSecretNoEcho clears ENABLE_ECHO_INPUT around read when stdout is a TTY.
func readSecretNoEcho(stdout io.Writer, read func() (string, error)) (string, error) {
	if !configTerminal(stdout) {
		return read()
	}
	fd := windows.Handle(os.Stdin.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(fd, &mode); err != nil {
		return "", fmt.Errorf("console mode: %w", err)
	}
	if err := windows.SetConsoleMode(fd, mode&^windows.ENABLE_ECHO_INPUT); err != nil {
		return "", fmt.Errorf("console echo off: %w", err)
	}
	defer func() {
		if err := windows.SetConsoleMode(fd, mode); err != nil {
			return
		}
	}()
	return read()
}
