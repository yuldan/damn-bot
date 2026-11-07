//go:build minimal

package main

import "io"

func runKanban(_ []string, _ io.Writer, stderr io.Writer) int {
	sessionsPrintf(stderr, "kanban: not available in a minimal build\n")
	return 2
}

func kanbanUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot kanban <subcommand> (full build only)\n")
}
