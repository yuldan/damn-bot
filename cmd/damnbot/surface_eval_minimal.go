//go:build minimal

package main

import "io"

// evalSurface keeps `eval` documented and refuses the batch/evals wiring that
// the minimal build excludes (P13 envelope); eval recall is also excluded so
// the F2 memory harness does not inflate the size-gated binary.
func evalSurface() surfaceEntry {
	return surfaceEntry{Name: "eval", Summary: "Batch runs, evals, trajectory, scorecard", Phase: phaseF8, Run: runEval, Usage: evalUsage}
}

func runEval(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "eval: not compiled in the minimal build\n")
	return 2
}

func evalUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot eval (not compiled in the minimal build)\n")
}

func addEvalCompletion(map[string][]string) {}
