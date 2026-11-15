//go:build minimal

package main

import "io"

// runStatus keeps `status` documented in the minimal build; the component
// aggregator and its store readers are excluded from the size-gated binary
// (P13).
func runStatus(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "status: not compiled in the minimal build\n")
	return 2
}

func statusUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot status (not compiled in the minimal build)\n")
}
