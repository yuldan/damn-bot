//go:build minimal

package main

import "io"

// runPeer is the minimal-build answer: the peer transport is not compiled in,
// so the subcommand reports module_unavailable instead of pretending.
func runPeer(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "peer: module_unavailable: not available in the minimal build\n")
	return 3
}
