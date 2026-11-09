//go:build !minimal

package main

// evalSurface registers the F8 eval command in the full build.
func evalSurface() surfaceEntry {
	return surfaceEntry{Name: "eval", Summary: "Batch runs, evals, trajectory, scorecard", Phase: phaseF8, Run: runEval, Usage: evalUsage}
}

func addEvalCompletion(m map[string][]string) {
	m["eval"] = []string{"batch", "scorecard", "recall"}
	m["eval batch"] = []string{"run", "compress"}
	m["eval scorecard"] = []string{"run", "last"}
}
