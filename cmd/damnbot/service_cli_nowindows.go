//go:build !windows

package main

import "io"

// runWindowsService exists only so runService compiles on every platform; it
// is unreachable there (runService checks the GOOS first). The Windows
// implementation and its schtasks effects live in service_cli_windows.go.
func runWindowsService(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "service: Windows Task Scheduler backend is only built for Windows\n")
	return 1
}
