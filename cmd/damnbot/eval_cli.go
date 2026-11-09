//go:build !minimal

package main

import (
	"io"
)

// runEval implements SPEC §10 `damnbot eval`: batch runs, the deterministic
// evals (F8), trajectory compression, and the scorecard job.
func runEval(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		evalUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "-h", "--help", "help":
		evalUsage(stdout)
		return 0
	case "recall":
		return runEvalRecall(rest, stdout, stderr)
	case "batch":
		return runEvalBatch(rest, stdout, stderr)
	case "scorecard":
		return runEvalScorecard(rest, stdout, stderr)
	default:
		sessionsPrintf(stderr, "eval: unknown subcommand %q\n", cleanArg(sub))
		evalUsage(stderr)
		return 2
	}
}

func evalUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot eval <batch|scorecard|recall> ...\n")
	sessionsPrintf(w, "  batch run <prompts.jsonl> [--parallel N] [--out DIR] [--model REF] [--resume]\n")
	sessionsPrintf(w, "  batch compress <out-dir> [--max-chars N]\n")
	sessionsPrintf(w, "  scorecard run|last [--json|--csv]\n")
	sessionsPrintf(w, "  recall [E4 memory runner flags]\n")
}

// runEvalScorecard maps SPEC `scorecard last` onto the stored-run reader and
// reuses the F4 job surface unchanged.
func runEvalScorecard(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		evalUsage(stderr)
		return 2
	}
	sub := args[0]
	if sub == "last" {
		sub = "status"
	}
	return runScorecard(append([]string{sub}, args[1:]...), stdout, stderr)
}
