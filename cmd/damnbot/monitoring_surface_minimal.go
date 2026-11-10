//go:build minimal

package main

import "io"

func monitoringSurface() surfaceEntry {
	return surfaceEntry{Name: "monitoring", Summary: "Gateway health and redacted diagnostics", Phase: phaseF8, Run: runMonitoring, Usage: monitoringUsage}
}

func runMonitoring(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "monitoring: not compiled in the minimal build\n")
	return 2
}

func monitoringUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot monitoring (not compiled in the minimal build)\n")
}
