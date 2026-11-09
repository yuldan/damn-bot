//go:build minimal

package main

import "io"

// runServeSub refuses the web-control subcommands in the minimal build, where
// the web UI (and its control endpoint) is not linked (SIZE P13).
func runServeSub(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 || (args[0] != "open" && args[0] != "sessions") {
		return 0, false
	}
	sessionsPrintf(stderr, "serve %s: not compiled in the minimal build\n", args[0])
	return 2, true
}
