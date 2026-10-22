package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb/journalstore"
)

// runCheckpoints implements the journal-checkpoint commands (CLI-039): the
// action journal is Damn-Bot's checkpoint store, so status/list/prune/clear
// operate on it without touching the transcript.
func runCheckpoints(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		checkpointUsage(stderr)
		return 2
	}
	switch args[0] {
	case "status":
		return checkpointsStatus(args[1:], stdout, stderr)
	case "list":
		return checkpointsList(args[1:], stdout, stderr)
	case "prune":
		return checkpointsPrune(args[1:], stdout, stderr)
	case "clear":
		return checkpointsClear(args[1:], stdout, stderr)
	case "clear-legacy":
		return checkpointsClearLegacy(args[1:], stdout, stderr)
	default:
		sessionsPrintf(stderr, "checkpoints: unknown subcommand %q\n", cleanArg(args[0]))
		checkpointUsage(stderr)
		return 2
	}
}

func checkpointUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot checkpoints status|list [--limit N] [--session ID]\n")
	sessionsPrintf(w, "       damnbot checkpoints prune [--older-than 30d] [--dry-run] [--yes]\n")
	sessionsPrintf(w, "       damnbot checkpoints clear|clear-legacy --yes\n")
}

// checkpointArgs parses flags for the checkpoint subcommands.
type checkpointArgs struct {
	dataDir, session, olderThan string
	limit                       int
	yes, dryRun                 bool
	pos                         []string
}

func parseCheckpointArgs(args []string) (checkpointArgs, error) {
	var a checkpointArgs
	i := 0
	for i < len(args) {
		s := args[i]
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			i++
			continue
		}
		v, err := checkpointFlag(&a, s, args, &i)
		if err != nil {
			return a, err
		}
		if !v {
			return a, fmt.Errorf("unknown flag %q", s)
		}
		i++
	}
	return a, nil
}

func checkpointFlag(a *checkpointArgs, s string, args []string, i *int) (bool, error) {
	value := func(name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for _, f := range []struct {
		name string
		dst  *string
	}{{"--data-dir", &a.dataDir}, {"--session", &a.session}, {"--older-than", &a.olderThan}} {
		if s == f.name {
			v, err := value(f.name)
			*f.dst = v
			return true, err
		}
		if v, ok := strings.CutPrefix(s, f.name+"="); ok {
			if v == "" {
				return true, fmt.Errorf("%s needs a value", f.name)
			}
			*f.dst = v
			return true, nil
		}
	}
	switch {
	case s == "--limit":
		v, err := value(s)
		if err != nil {
			return true, err
		}
		return true, checkpointLimit(a, v)
	case strings.HasPrefix(s, "--limit="):
		return true, checkpointLimit(a, strings.TrimPrefix(s, "--limit="))
	case s == "--yes" || s == "-y":
		a.yes = true
	case s == "--dry-run":
		a.dryRun = true
	default:
		return false, nil
	}
	return true, nil
}

// checkpointLimit parses a positive --limit value.
func checkpointLimit(a *checkpointArgs, v string) error {
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return fmt.Errorf("--limit %q: want a positive integer", cleanArg(v))
	}
	a.limit = n
	return nil
}

// checkpointCutoff is the undo horizon T_cut, unless --older-than overrides it.
func checkpointCutoff(a *checkpointArgs) (time.Duration, int64, error) {
	d := time.Duration(journalstore.DefaultRetentionDays) * 24 * time.Hour
	if a.olderThan != "" {
		var err error
		if d, err = parseAge(a.olderThan); err != nil {
			return 0, 0, err
		}
	}
	return d, time.Now().Add(-d).UnixMilli(), nil
}

func checkpointsStatus(args []string, stdout, stderr io.Writer) int {
	a, err := parseCheckpointArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "checkpoints status: %v\n", err)
		}
		checkpointUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "checkpoints status", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints status", stderr)
	retention, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints status: %v\n", cerr)
		return 2
	}
	st, serr := db.CheckpointStats(ctx, cutoff)
	if serr != nil {
		return sessionsFail(stderr, "checkpoints status", serr, "")
	}
	sessionsPrintf(stdout, "actions: %d\nexpired: %d\nundone: %d\nbytes: %d\nretention: %s\n",
		st.Total, st.Expired, st.Undone, st.BytesHeld, retention)
	return 0
}

func checkpointsList(args []string, stdout, stderr io.Writer) int {
	a, err := parseCheckpointArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "checkpoints list: %v\n", err)
		}
		checkpointUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "checkpoints list", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints list", stderr)
	_, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints list: %v\n", cerr)
		return 2
	}
	list, lerr := db.Checkpoints(ctx, cutoff, a.session, a.limit)
	if lerr != nil {
		return sessionsFail(stderr, "checkpoints list", lerr, a.session)
	}
	for _, c := range list {
		status := "ok"
		switch {
		case c.Status == "undone":
			status = "undone"
		case c.Expired:
			status = "expired"
		}
		sessionsPrintf(stdout, "%d\t%s\t%s\t%s\t%s\t%s\n", c.ID, time.UnixMilli(c.CreatedAt).UTC().Format(time.RFC3339),
			cleanArg(c.Kind), status, cleanArg(c.SessionID), cleanArg(c.Subject))
	}
	return 0
}

func checkpointsPrune(args []string, stdout, stderr io.Writer) int {
	a, err := parseCheckpointArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "checkpoints prune: %v\n", err)
		}
		checkpointUsage(stderr)
		return 2
	}
	retention, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints prune: %v\n", cerr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "checkpoints prune", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints prune", stderr)
	st, serr := db.CheckpointStats(ctx, cutoff)
	if serr != nil {
		return sessionsFail(stderr, "checkpoints prune", serr, "")
	}
	if a.dryRun {
		sessionsPrintf(stdout, "would prune %d checkpoints older than %s\n", st.Expired, retention)
		return 0
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints prune: refusing to delete without --yes (use --dry-run to preview)\n")
		return 2
	}
	n, perr := db.PruneCheckpoints(ctx, cutoff)
	if perr != nil {
		return sessionsFail(stderr, "checkpoints prune", perr, "")
	}
	sessionsPrintf(stdout, "pruned %d checkpoints older than %s; blob files are reclaimed by `damnbot doctor gc`\n", n, retention)
	return 0
}

func checkpointsClear(args []string, stdout, stderr io.Writer) int {
	a, err := parseCheckpointArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "checkpoints clear: %v\n", err)
		}
		checkpointUsage(stderr)
		return 2
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints clear: refusing to delete all checkpoints without --yes\n")
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "checkpoints clear", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints clear", stderr)
	n, cerr := db.ClearCheckpoints(ctx)
	if cerr != nil {
		return sessionsFail(stderr, "checkpoints clear", cerr, "")
	}
	sessionsPrintf(stdout, "cleared %d checkpoints\n", n)
	return 0
}

// checkpointsClearLegacy removes checkpoint directories left by pre-journal
// versions (<data-dir>/checkpoints, <config-dir>/checkpoints). Damn-Bot has no
// such layout today, so the command normally reports nothing to do.
func checkpointsClearLegacy(args []string, stdout, stderr io.Writer) int {
	a, err := parseCheckpointArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "checkpoints clear-legacy: %v\n", err)
		}
		checkpointUsage(stderr)
		return 2
	}
	dirs, derr := legacyCheckpointDirs(a.dataDir)
	if derr != nil {
		sessionsPrintf(stderr, "checkpoints clear-legacy: %v\n", derr)
		return 1
	}
	if len(dirs) == 0 {
		sessionsPrintf(stdout, "no legacy checkpoints\n")
		return 0
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints clear-legacy: refusing to delete %s without --yes\n", strings.Join(dirs, ", "))
		return 2
	}
	for _, dir := range dirs {
		if rerr := os.RemoveAll(dir); rerr != nil {
			sessionsPrintf(stderr, "checkpoints clear-legacy: %v\n", rerr)
			return 1
		}
	}
	sessionsPrintf(stdout, "cleared legacy checkpoints: %s\n", strings.Join(dirs, ", "))
	return 0
}

// legacyCheckpointDirs lists existing legacy checkpoint directories.
func legacyCheckpointDirs(dataDir string) ([]string, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	if dataDir == "" {
		dataDir = paths.DataDir
	}
	var out []string
	for _, dir := range []string{filepath.Join(dataDir, "checkpoints"), filepath.Join(paths.ConfigDir, "checkpoints")} {
		fi, serr := os.Stat(dir)
		if serr == nil && fi.IsDir() {
			out = append(out, dir)
		}
	}
	return out, nil
}
