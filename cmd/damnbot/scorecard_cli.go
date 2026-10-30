package main

import (
	"context"
	"errors"
	"flag"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/scorecard"
)

type scorecardOptions struct {
	configPath, dataDir string
	json, csv           bool
}

// runScorecard implements the small manual surface for the weekly job. SPEC
// §10 folds scorecard into F8 `eval scorecard`; F4 exposes the same run/status
// operations under `damnbot scorecard` until that surface lands.
func runScorecard(args []string, stdout, stderr io.Writer) int {
	var o scorecardOptions
	fs := flag.NewFlagSet("scorecard", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.configPath, "config", "", "config.toml path")
	fs.StringVar(&o.dataDir, "data-dir", "", "data directory")
	fs.BoolVar(&o.json, "json", false, "print the result as JSON")
	fs.BoolVar(&o.csv, "csv", false, "print the result as CSV")
	pos, err := parseInterleaved(fs, args)
	if err != nil || len(pos) == 0 {
		scorecardUsage(stderr)
		return 2
	}
	sub, rest := pos[0], pos[1:]
	if sub == "-h" || sub == "--help" {
		scorecardUsage(stdout)
		return 0
	}
	if sub != "run" && sub != "status" {
		sessionsPrintf(stderr, "scorecard: unknown subcommand %q\n", cleanArg(sub))
		scorecardUsage(stderr)
		return 2
	}
	if len(rest) != 0 {
		sessionsPrintf(stderr, "scorecard %s: unexpected argument %q\n", sub, cleanArg(rest[0]))
		return 2
	}
	if o.json && o.csv {
		sessionsPrintf(stderr, "scorecard: --json and --csv are mutually exclusive\n")
		return 2
	}
	env, err := openCLI(context.Background(), o.configPath, o.dataDir)
	if err != nil {
		sessionsPrintf(stderr, "scorecard: %s\n", cleanLine(err.Error()))
		return 1
	}
	defer func() {
		if cerr := env.db.CloseContext(context.Background()); cerr != nil {
			sessionsPrintf(stderr, "scorecard: close: %s\n", cleanLine(cerr.Error()))
		}
	}()
	if sub == "run" {
		return scorecardRun(env, &o, stdout, stderr)
	}
	return scorecardStatus(env, &o, stdout, stderr)
}

func scorecardUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot scorecard <run|status> [--json|--csv] [--config FILE] [--data-dir DIR]\n")
	sessionsPrintf(w, "  run     run the weekly job now (once per ISO week)\n")
	sessionsPrintf(w, "  status  print the last stored run\n")
}

func scorecardRun(env *cliEnv, o *scorecardOptions, stdout, stderr io.Writer) int {
	store := &scorecardStore{db: env.db}
	job := newScorecardJob(env.db, true, nil)
	res, ran, err := job.Tick(context.Background())
	if err != nil {
		sessionsPrintf(stderr, "scorecard run: %s\n", cleanLine(err.Error()))
		return 1
	}
	if !ran {
		last, lerr := store.Last(context.Background())
		if lerr != nil {
			sessionsPrintf(stderr, "scorecard run: %s already claimed and no result stored\n", scorecard.WeekKey(time.Now().UTC()))
			return 1
		}
		res = last
		sessionsPrintf(stderr, "scorecard: %s already ran this week\n", res.Week)
	}
	if code := scorecardPrint(stdout, &res, o); code != 0 {
		return code
	}
	if res.Status == scorecard.StatusFailed {
		return 1
	}
	return 0
}

func scorecardStatus(env *cliEnv, o *scorecardOptions, stdout, stderr io.Writer) int {
	res, err := (&scorecardStore{db: env.db}).Last(context.Background())
	if errors.Is(err, scorecard.ErrNoRun) {
		sessionsPrintf(stderr, "scorecard status: no run yet\n")
		return 1
	}
	if err != nil {
		sessionsPrintf(stderr, "scorecard status: %s\n", cleanLine(err.Error()))
		return 1
	}
	return scorecardPrint(stdout, &res, o)
}

func scorecardPrint(w io.Writer, res *scorecard.Result, o *scorecardOptions) int {
	switch {
	case o.json:
		if err := res.WriteJSON(w); err != nil {
			return 1
		}
		return 0
	case o.csv:
		if err := res.WriteCSV(w); err != nil {
			return 1
		}
		return 0
	default:
		return say(w, "%s\n", scorecard.Summary(res))
	}
}
