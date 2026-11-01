//go:build minimal

package main

import "io"

// runMCPImpl is the minimal-build answer: MCP is a lazy module and the minimal
// binary compiles no module surface, so every subcommand is unavailable rather
// than a stub that pretends to work.
func runMCPImpl(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "mcp: not available in the minimal build\n")
	return 1
}
