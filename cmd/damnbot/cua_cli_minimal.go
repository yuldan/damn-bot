//go:build minimal

package main

import "io"

// runCua is the minimal-build answer: the module surface is not compiled in,
// so every subcommand reports module_unavailable rather than pretending.
func runCua(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "cua: module_unavailable: not available in the minimal build\n")
	return 3
}
