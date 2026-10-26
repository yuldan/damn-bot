package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
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
	subs := map[string]func([]string, io.Writer, io.Writer) int{
		"status":       checkpointsStatus,
		"list":         checkpointsList,
		"prune":        checkpointsPrune,
		"clear":        checkpointsClear,
		"clear-legacy": checkpointsClearLegacy,
	}
	if sub, ok := subs[args[0]]; ok {
		return sub(args[1:], stdout, stderr)
	}
	sessionsPrintf(stderr, "checkpoints: unknown subcommand %q\n", cleanArg(args[0]))
	checkpointUsage(stderr)
	return 2
}

func checkpointUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot checkpoints status [--session ID]\n")
	sessionsPrintf(w, "       damnbot checkpoints list [--limit N] [--session ID]\n")
	sessionsPrintf(w, "       damnbot checkpoints prune [--older-than 30d] [--dry-run] [--yes]\n")
	sessionsPrintf(w, "       damnbot checkpoints clear|clear-legacy [--dry-run] --yes\n")
}

// checkpointArgs parses flags for the checkpoint subcommands. seen records the
// flags the operator actually passed, so a subcommand can refuse a filter it
// does not implement instead of silently ignoring it (ACC-62).
type checkpointArgs struct {
	dataDir, session, olderThan string
	limit                       int
	yes, dryRun                 bool
	pos                         []string
	seen                        map[string]bool
}

func parseCheckpointArgs(args []string) (checkpointArgs, error) {
	a := checkpointArgs{seen: map[string]bool{}}
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
			a.seen[f.name] = true
			return true, err
		}
		if v, ok := strings.CutPrefix(s, f.name+"="); ok {
			if v == "" {
				return true, fmt.Errorf("%s needs a value", f.name)
			}
			*f.dst = v
			a.seen[f.name] = true
			return true, nil
		}
	}
	switch {
	case s == "--limit":
		v, err := value(s)
		if err != nil {
			return true, err
		}
		a.seen["--limit"] = true
		return true, checkpointLimit(a, v)
	case strings.HasPrefix(s, "--limit="):
		a.seen["--limit"] = true
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

// checkpointsReject refuses flags a subcommand does not implement: a filter the
// operator asked for must never be ignored silently, least of all on the
// destructive subcommands.
func checkpointsReject(a *checkpointArgs, name string, stderr io.Writer, unsupported ...string) bool {
	for _, f := range unsupported {
		if a.seen[f] {
			sessionsPrintf(stderr, "checkpoints %s: %s is not supported by this subcommand\n", name, f)
			checkpointUsage(stderr)
			return true
		}
	}
	return false
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
	if checkpointsReject(&a, "status", stderr, "--limit") {
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return checkpointsFail(stderr, "checkpoints status", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints status", stderr)
	retention, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints status: %v\n", cerr)
		return 2
	}
	st, serr := db.CheckpointStats(ctx, cutoff, a.session)
	if serr != nil {
		return checkpointsFail(stderr, "checkpoints status", serr, "")
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
		return checkpointsFail(stderr, "checkpoints list", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints list", stderr)
	_, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints list: %v\n", cerr)
		return 2
	}
	list, lerr := db.Checkpoints(ctx, cutoff, a.session, a.limit)
	if lerr != nil {
		return checkpointsFail(stderr, "checkpoints list", lerr, a.session)
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
	if checkpointsReject(&a, "prune", stderr, "--session", "--limit") {
		return 2
	}
	retention, cutoff, cerr := checkpointCutoff(&a)
	if cerr != nil {
		sessionsPrintf(stderr, "checkpoints prune: %v\n", cerr)
		return 2
	}
	ctx := context.Background()
	if a.dryRun {
		db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
		if derr != nil {
			return checkpointsFail(stderr, "checkpoints prune", derr, "")
		}
		defer closeSessionsDB(db, "checkpoints prune", stderr)
		st, serr := db.CheckpointStats(ctx, cutoff, "")
		if serr != nil {
			return checkpointsFail(stderr, "checkpoints prune", serr, "")
		}
		sessionsPrintf(stdout, "would prune %d checkpoints older than %s\n", st.Expired, retention)
		sessionsPrintf(stderr, "checkpoints prune: sessions with an open turn or live lease are refused during prune\n")
		return 0
	}
	if maintenanceBlocked(stderr, "checkpoints prune") {
		return 2
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints prune: refusing to delete without --yes (use --dry-run to preview)\n")
		return 2
	}
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return checkpointsFail(stderr, "checkpoints prune", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints prune", stderr)
	n, perr := db.PruneCheckpoints(ctx, cutoff)
	if perr != nil {
		return checkpointsFail(stderr, "checkpoints prune", perr, "")
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
	if checkpointsReject(&a, "clear", stderr, "--session", "--limit", "--older-than") {
		return 2
	}
	ctx := context.Background()
	// ACC-63: --dry-run always previews and never deletes, with or without --yes.
	if a.dryRun {
		db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
		if derr != nil {
			return checkpointsFail(stderr, "checkpoints clear", derr, "")
		}
		defer closeSessionsDB(db, "checkpoints clear", stderr)
		st, serr := db.CheckpointStats(ctx, time.Now().UnixMilli(), "")
		if serr != nil {
			return checkpointsFail(stderr, "checkpoints clear", serr, "")
		}
		sessionsPrintf(stdout, "would clear %d checkpoints\n", st.Total)
		return 0
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints clear: refusing to delete all checkpoints without --yes\n")
		return 2
	}
	if maintenanceBlocked(stderr, "checkpoints clear") {
		return 2
	}
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return checkpointsFail(stderr, "checkpoints clear", derr, "")
	}
	defer closeSessionsDB(db, "checkpoints clear", stderr)
	n, cerr := db.ClearCheckpoints(ctx)
	if cerr != nil {
		return checkpointsFail(stderr, "checkpoints clear", cerr, "")
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
	if checkpointsReject(&a, "clear-legacy", stderr, "--session", "--limit", "--older-than") {
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
	// --dry-run never removes a directory, with or without --yes (ACC-63).
	if a.dryRun {
		sessionsPrintf(stdout, "would clear legacy checkpoints: %s\n", strings.Join(dirs, ", "))
		return 0
	}
	if !a.yes {
		sessionsPrintf(stderr, "checkpoints clear-legacy: refusing to delete %s without --yes\n", strings.Join(dirs, ", "))
		return 2
	}
	if maintenanceBlocked(stderr, "checkpoints clear-legacy") {
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

// checkpointsFail maps maintenance refusals: a busy session (open turn or
// live lease) is exit 4 so callers can retry after the turn ends. The prefix is
// the subcommand name (not "sessions …": these errors belong to `checkpoints`).
func checkpointsFail(stderr io.Writer, name string, err error, id string) int {
	if errors.Is(err, ctxdb.ErrSessionBusy) {
		sessionsPrintf(stderr, "%s: a session with checkpoints is busy (open turn or live lease); finish it first\n", name)
		return 4
	}
	sessionsPrintf(stderr, "%s: %s\n", name, sessionsMessage(err, id))
	return sessionsErr(err)
}
