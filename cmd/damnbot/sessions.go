package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// runSessions implements the `sessions` command (SPEC §10): fork and rewind,
// with SPEC's names and no aliases.
func runSessions(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		sessionsUsage(stderr)
		return 2
	}
	switch args[0] {
	case "fork":
		return sessionsBranch(args[1:], stdout, stderr, false)
	case "rewind":
		return sessionsBranch(args[1:], stdout, stderr, true)
	case "ls":
		return sessionsList(args[1:], stdout, stderr)
	case "show":
		return sessionsShow(args[1:], stdout, stderr)
	case "rename":
		return sessionsRename(args[1:], stdout, stderr)
	case "export":
		return sessionsExport(args[1:], stdout, stderr)
	default:
		sessionsPrintf(stderr, "sessions: unknown subcommand %q\n", cleanArg(args[0]))
		sessionsUsage(stderr)
		return 2
	}
}

// sessionsPrintf writes and reports success; CLI output errors are not fatal.
func sessionsPrintf(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return // CLI output errors are not actionable
	}
}

func sessionsUsage(w io.Writer) {
	sessionsPrintf(w, "usage: damnbot sessions fork <id> [--at <seq|turn:ID>]\n")
	sessionsPrintf(w, "       damnbot sessions rewind <id> --at <seq|turn:ID>\n")
	sessionsPrintf(w, "       damnbot sessions ls|show <id> [--data-dir DIR]\n")
	sessionsPrintf(w, "       damnbot sessions rename <id> [<title>]\n")
	sessionsPrintf(w, "       damnbot sessions export <id> [--format md|jsonl|html] [--out FILE] [--no-redact]\n")
}

// sessionsBranch creates a child of id with ctxdb.Fork. Fork defaults to the
// current head; rewind requires an earlier point and never deletes the old
// branch (SPEC §4.6: rewind = fork at an earlier seq).
func sessionsBranch(args []string, stdout, stderr io.Writer, rewind bool) int {
	name := "fork"
	if rewind {
		name = "rewind"
	}
	id, at, code := branchArgs(args, stderr, rewind)
	if code != 0 {
		return code
	}
	ctx := context.Background()
	db, err := openSessionsDB(ctx, at.dataDir)
	if err != nil {
		sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
		return 1
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			sessionsPrintf(stderr, "sessions %s: close db: %v\n", name, cerr)
		}
	}()

	target, usage, err := branchPoint(ctx, db, id, at.at, rewind)
	if err != nil {
		if usage {
			sessionsPrintf(stderr, "sessions %s: %s\n", name, err)
			return 2
		}
		sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
		return sessionsErr(err)
	}
	child, err := db.Fork(ctx, id, target)
	if err != nil {
		sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
		return sessionsErr(err)
	}
	if rewind {
		sessionsPrintf(stdout, "rewound %s to seq %d: %s\n", cleanArg(id), target, child)
	} else {
		sessionsPrintf(stdout, "forked %s at seq %d: %s\n", cleanArg(id), target, child)
	}
	return 0
}

// branchArgs parses and validates the subcommand arguments.
func branchArgs(args []string, stderr io.Writer, rewind bool) (string, branchOpts, int) {
	opts, pos, err := parseBranchArgs(args)
	if err != nil || len(pos) != 1 || pos[0] == "" {
		if err != nil {
			sessionsPrintf(stderr, "sessions: %v\n", err)
		}
		sessionsUsage(stderr)
		return "", opts, 2
	}
	if rewind && opts.at == "" {
		sessionsPrintf(stderr, "sessions rewind: --at is required\n")
		return "", opts, 2
	}
	return pos[0], opts, 0
}

// branchPoint resolves the fork seq. usage reports a bad --at (exit 2)
// rather than a store error.
func branchPoint(ctx context.Context, db *ctxdb.Store, id, at string, rewind bool) (target int64, usage bool, err error) {
	head, err := db.Head(ctx, id)
	if err != nil {
		return 0, false, fmt.Errorf("head: %w", err)
	}
	if at == "" {
		return head, false, nil
	}
	target, err = parseAt(ctx, db, id, at, rewind)
	if err != nil {
		return 0, true, err
	}
	if rewind && target >= head {
		return 0, true, fmt.Errorf("--at %d is not earlier than head %d", target, head)
	}
	return target, false, nil
}

type branchOpts struct{ at, dataDir string }

// parseBranchArgs accepts --at/--data-dir before or after the session id
// (Go's flag package stops at the first positional).
func parseBranchArgs(args []string) (branchOpts, []string, error) {
	var o branchOpts
	var pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--at" || a == "-at":
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("--at needs a value")
			}
			i++
			o.at = args[i]
		case strings.HasPrefix(a, "--at="):
			o.at = strings.TrimPrefix(a, "--at=")
		case a == "--data-dir":
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("--data-dir needs a value")
			}
			i++
			o.dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			o.dataDir = strings.TrimPrefix(a, "--data-dir=")
		case strings.HasPrefix(a, "-"):
			return o, nil, fmt.Errorf("unknown flag %q", a)
		default:
			pos = append(pos, a)
		}
	}
	return o, pos, nil
}

// parseAt resolves --at. `turn:ID` means the end of that turn for fork (the
// turn is inherited) and the point just before it for rewind (the turn is
// undone); a raw integer is an event seq of this session (ADR-136).
func parseAt(ctx context.Context, db *ctxdb.Store, id, v string, rewind bool) (int64, error) {
	if turn, ok := strings.CutPrefix(v, "turn:"); ok {
		return parseTurnAt(ctx, db, id, cleanArg(turn), rewind)
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("--at %q: want a non-negative seq or turn:<id>", cleanArg(v))
	}
	return n, nil
}

func parseTurnAt(ctx context.Context, db *ctxdb.Store, id, turn string, rewind bool) (int64, error) {
	first, last, err := db.TurnRange(ctx, id, turn)
	if errors.Is(err, ctxdb.ErrNotFound) {
		return 0, fmt.Errorf("turn %s not found", turn)
	}
	if err != nil {
		return 0, fmt.Errorf("turn %s: lookup failed", turn)
	}
	if !rewind {
		return last, nil
	}
	if first <= 1 {
		return 0, nil
	}
	return first - 1, nil
}

var errNoDataDir = errors.New("data directory not found")

// cleanArg strips control characters and bounds one echoed user argument.
func cleanArg(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, s)
	if len(s) > 80 {
		s = s[:80] + "…"
	}
	return s
}

// sessionsMessage maps a store error to a clean CLI message; SQL driver text
// never reaches the user (SEC13-3).
func sessionsMessage(err error, id string) string {
	switch {
	case errors.Is(err, errNoDataDir):
		return err.Error()
	case errors.Is(err, ctxdb.ErrNotFound):
		return "session " + cleanArg(id) + " not found"
	case errors.Is(err, ctxdb.ErrSessionPurging):
		return "session " + cleanArg(id) + " is being purged"
	case errors.Is(err, ctxdb.ErrSessionBusy):
		return "session " + cleanArg(id) + " has an open turn; use --at before that turn, or finish/cancel it first"
	case errors.Is(err, ctxdb.ErrDBBusy):
		return "database is busy; retry"
	case errors.Is(err, ctxdb.ErrInvalidSeq):
		return strings.TrimPrefix(err.Error(), "ctxdb: ")
	}
	return "internal error"
}

// openSessionsDB opens the session store for a CLI command. A missing data
// directory is an error; the CLI never creates one as a side effect (SEC13-5).
func openSessionsDB(ctx context.Context, dataDir string) (*ctxdb.Store, error) {
	if dataDir == "" {
		loader, err := config.New(config.OS())
		if err != nil {
			return nil, fmt.Errorf("config deps: %w", err)
		}
		paths, err := loader.Paths("")
		if err != nil {
			return nil, fmt.Errorf("config paths: %w", err)
		}
		dataDir = paths.DataDir
	}
	return openSessionsStore(ctx, dataDir, false)
}

// openSessionsDBReadOnly opens the store for inspection (mode=ro): a newer
// schema is readable and no data dir/layout is created (ADR-132/ADR-140).
func openSessionsDBReadOnly(ctx context.Context, dataDir string) (*ctxdb.Store, error) {
	return openSessionsStore(ctx, dataDir, true)
}

func openSessionsStore(ctx context.Context, dataDir string, readOnly bool) (*ctxdb.Store, error) {
	fi, err := os.Stat(dataDir) //nolint:gosec // operator-selected data dir, not request input
	if err != nil || !fi.IsDir() {
		return nil, fmt.Errorf("%w: %s", errNoDataDir, cleanArg(dataDir))
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "cli", ReadOnly: readOnly})
	if err != nil {
		return nil, fmt.Errorf("open sessions db: %w", err)
	}
	return db, nil
}

// sessionsErr maps a store error to the SPEC §11.6 CLI exit codes.
func sessionsErr(err error) int {
	switch {
	case errors.Is(err, ctxdb.ErrSessionBusy), errors.Is(err, ctxdb.ErrDBBusy):
		return 4
	case errors.Is(err, ctxdb.ErrInvalidSeq):
		return 2
	}
	return 1
}
