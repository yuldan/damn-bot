//go:build minimal

package main

import (
	"io"
)

// apiServerUsage documents the command in the minimal build, where the HTTP
// surface is not linked in (SIZE P13).
func apiServerUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot api-server (not compiled in the minimal build)\n")
}

// runAPIServer refuses in the minimal build: internal/gwapi and the full turn
// wiring are excluded to keep the binary within the P13 envelope.
func runAPIServer(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "api-server: not compiled in the minimal build\n")
	return 2
}
