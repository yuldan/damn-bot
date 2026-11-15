//go:build minimal

package main

import "io"

// runPromptSize keeps `prompt-size` documented in the minimal build; the run
// wiring it measures is excluded from the size-gated binary (P13).
func runPromptSize(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "prompt-size: not compiled in the minimal build\n")
	return 2
}

func promptSizeUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot prompt-size (not compiled in the minimal build)\n")
}
