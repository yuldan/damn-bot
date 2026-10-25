package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// adminArgs parses the sessions admin subcommands. Flags may appear before or
// after the positional id (Go's flag package stops at the first positional).
type adminArgs struct {
	dataDir, format, out, olderThan string
	pinAt                           string
	archive, dryRun, yes, noRedact  bool
	off                             bool
	pos                             []string
}

func parseAdminArgs(args []string) (adminArgs, error) {
	var a adminArgs
	i := 0
	for i < len(args) {
		s := args[i]
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			i++
			continue
		}
		handled, err := adminFlag(&a, s, args, &i)
		if err != nil {
			return a, err
		}
		if !handled {
			return a, fmt.Errorf("unknown flag %q", s)
		}
		i++
	}
	return a, nil
}

// adminFlag parses one flag (and its value) at args[*i].
func adminFlag(a *adminArgs, s string, args []string, i *int) (bool, error) {
	value := func(name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	switch {
	case s == "--data-dir":
		v, err := value(s)
		a.dataDir = v
		return true, err
	case strings.HasPrefix(s, "--data-dir="):
		a.dataDir = strings.TrimPrefix(s, "--data-dir=")
	case s == "--format":
		v, err := value(s)
		a.format = v
		return true, err
	case strings.HasPrefix(s, "--format="):
		a.format = strings.TrimPrefix(s, "--format=")
	case s == "--out":
		v, err := value(s)
		a.out = v
		return true, err
	case strings.HasPrefix(s, "--out="):
		a.out = strings.TrimPrefix(s, "--out=")
	case s == "--older-than":
		v, err := value(s)
		a.olderThan = v
		return true, err
	case strings.HasPrefix(s, "--older-than="):
		a.olderThan = strings.TrimPrefix(s, "--older-than=")
	case s == "--at":
		v, err := value(s)
		a.pinAt = v
		return true, err
	case strings.HasPrefix(s, "--at="):
		a.pinAt = strings.TrimPrefix(s, "--at=")
	case adminBoolFlag(a, s):
	default:
		return false, nil
	}
	return true, nil
}

// adminBoolFlag parses one boolean admin flag; false means "not a boolean flag".
func adminBoolFlag(a *adminArgs, s string) bool {
	switch s {
	case "--archive":
		a.archive = true
	case "--dry-run":
		a.dryRun = true
	case "--yes", "-y":
		a.yes = true
	case "--off":
		a.off = true
	case "--no-redact":
		a.noRedact = true
	default:
		return false
	}
	return true
}

// sessionsRename sets or prints the session title (MEM-016).
func sessionsRename(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) == 0 {
		if err != nil {
			sessionsPrintf(stderr, "sessions rename: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	id := a.pos[0]
	if len(a.pos) == 1 {
		db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
		if derr != nil {
			return sessionsFail(stderr, "rename", derr, id)
		}
		defer closeSessionsDB(db, "sessions rename", stderr)
		title, ok, derr := db.SessionLabel(ctx, id, ctxdb.LabelTitle)
		if derr != nil {
			return sessionsFail(stderr, "rename", derr, id)
		}
		if !ok {
			sessionsPrintf(stdout, "title: (none)\n")
			return 0
		}
		sessionsPrintf(stdout, "title: %s\n", title)
		return 0
	}
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "rename", derr, id)
	}
	defer closeSessionsDB(db, "sessions rename", stderr)
	if derr := db.SetSessionLabel(ctx, id, ctxdb.LabelTitle, strings.Join(a.pos[1:], " ")); derr != nil {
		return sessionsFail(stderr, "rename", derr, id)
	}
	sessionsPrintf(stdout, "renamed %s\n", cleanArg(id))
	return 0
}

// sessionsExport writes the fork-aware transcript (MEM-014).
func sessionsExport(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			sessionsPrintf(stderr, "sessions export: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	format := a.format
	if format == "" {
		format = "md"
	}
	if !exportFormatOK(format) {
		sessionsPrintf(stderr, "sessions export: unknown format %q (want md, jsonl, or html)\n", cleanArg(format))
		return 2
	}
	ctx := context.Background()
	id := a.pos[0]
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "export", derr, id)
	}
	defer closeSessionsDB(db, "sessions export", stderr)
	info, events, title, derr := exportTranscript(ctx, db, id, stderr)
	if derr != nil {
		return sessionsFail(stderr, "export", derr, id)
	}
	var red *redact.Redactor
	if !a.noRedact {
		red = redact.New(redact.Options{})
	}
	field := func(s string) string {
		if red != nil {
			return red.String(s)
		}
		return s
	}
	out := stdout
	f, ferr := exportOutFile(a.out)
	if ferr != nil {
		sessionsPrintf(stderr, "sessions export: %v\n", ferr)
		return 1
	}
	if f != nil {
		defer func() {
			if cerr := f.Close(); cerr != nil {
				sessionsPrintf(stderr, "sessions export: close: %v\n", cerr)
			}
		}()
		out = f
	}
	meta := exportMeta{ID: info.ID, Title: field(title), Model: field(info.Model), Origin: info.Origin, Cwd: field(info.Cwd), CreatedAt: info.CreatedAt}
	if werr := writeExport(out, format, &meta, events, red); werr != nil {
		sessionsPrintf(stderr, "sessions export: %v\n", werr)
		return 1
	}
	return 0
}

// exportTranscript reads the session row, its full transcript and its title.
// Reaching the limit prints a truncation note because the export is partial.
func exportTranscript(ctx context.Context, db *ctxdb.Store, id string, stderr io.Writer) (ctxdb.SessionInfo, []ctxdb.Event, string, error) {
	info, err := db.SessionInfo(ctx, id)
	if err != nil {
		return ctxdb.SessionInfo{}, nil, "", fmt.Errorf("session info: %w", err)
	}
	events, err := db.History(ctx, id, 0, ctxdb.WithHistoryRaw(), ctxdb.WithHistoryExcluded(), ctxdb.WithHistoryLimit(exportHistoryLimit))
	if err != nil {
		return ctxdb.SessionInfo{}, nil, "", fmt.Errorf("history: %w", err)
	}
	if len(events) >= exportHistoryLimit {
		sessionsPrintf(stderr, "sessions export: transcript truncated at %d events\n", exportHistoryLimit)
	}
	title, _, err := db.SessionLabel(ctx, id, ctxdb.LabelTitle)
	if err != nil {
		return ctxdb.SessionInfo{}, nil, "", fmt.Errorf("title: %w", err)
	}
	return info, events, title, nil
}

// exportHistoryLimit bounds one export; reaching it prints a truncation note.
const exportHistoryLimit = 100000

// exportOutFile opens --out (nil for stdout or "-"). It refuses to overwrite
// an existing path (including a symlink) and forces 0600 on the new file, so
// a transcript is never written through a link or into a world-readable file.
func exportOutFile(path string) (*os.File, error) {
	if path == "" || path == "-" {
		return nil, nil
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600) //nolint:gosec // --out is the user's chosen export path
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return nil, fmt.Errorf("%s already exists; refusing to overwrite", cleanArg(path))
		}
		return nil, fmt.Errorf("open %s: %w", cleanArg(path), err)
	}
	if cerr := f.Chmod(0o600); cerr != nil {
		if closeErr := f.Close(); closeErr != nil {
			return nil, fmt.Errorf("close %s after chmod: %w", cleanArg(path), closeErr)
		}
		return nil, fmt.Errorf("chmod %s: %w", cleanArg(path), cerr)
	}
	return f, nil
}

// sessionsPrune archives or purges sessions older than --older-than (MEM-015).
// Purging requires --yes; nothing is deleted without it, even headless.
func sessionsPrune(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || a.olderThan == "" {
		if err != nil {
			sessionsPrintf(stderr, "sessions prune: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	age, err := parseAge(a.olderThan)
	if err != nil {
		sessionsPrintf(stderr, "sessions prune: %v\n", err)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "prune", derr, "")
	}
	defer closeSessionsDB(db, "sessions prune", stderr)
	if a.archive {
		return pruneArchive(ctx, db, age, a.dryRun, stdout, stderr)
	}
	return prunePurge(ctx, db, age, &a, stdout, stderr)
}

// pruneArchive labels sessions created before the cutoff as archived; a
// dry-run only reports the candidates.
func pruneArchive(ctx context.Context, db *ctxdb.Store, age time.Duration, dryRun bool, stdout, stderr io.Writer) int {
	ids, err := db.SessionsOlderThan(ctx, time.Now().Add(-age).UnixMilli())
	if err != nil {
		return sessionsFail(stderr, "prune", err, "")
	}
	if dryRun {
		sessionsPrintf(stdout, "would archive %d sessions\n", len(ids))
		for _, id := range ids {
			sessionsPrintf(stdout, "%s\n", cleanArg(id))
		}
		return 0
	}
	for _, id := range ids {
		if serr := db.SetSessionLabel(ctx, id, ctxdb.LabelArchived, "1"); serr != nil {
			return sessionsFail(stderr, "prune", serr, id)
		}
	}
	sessionsPrintf(stdout, "archived %d sessions older than %s\n", len(ids), age)
	return 0
}

// prunePurge previews or deletes the §11.6 P set and vacuums afterwards.
func prunePurge(ctx context.Context, db *ctxdb.Store, age time.Duration, a *adminArgs, stdout, stderr io.Writer) int {
	req := ctxdb.PurgeRequest{OlderThan: age, WithForks: true}
	if a.dryRun {
		rep, err := db.PurgeCandidates(ctx, req)
		if err != nil {
			return sessionsFail(stderr, "prune", err, "")
		}
		sessionsPrintf(stdout, "would purge %d sessions\n", len(rep.P))
		for _, id := range rep.P {
			sessionsPrintf(stdout, "%s\n", cleanArg(id))
		}
		reportPurgeSkips(&rep, stderr)
		sessionsPrintf(stderr, "sessions prune: active leases and open turns are checked during purge\n")
		return 0
	}
	if !a.yes {
		sessionsPrintf(stderr, "sessions prune: refusing to delete without --yes (use --dry-run to preview or --archive)\n")
		return 2
	}
	rep, err := db.Purge(ctx, req)
	if err != nil {
		return sessionsFail(stderr, "prune", err, "")
	}
	reportPurgeSkips(&rep, stderr)
	sessionsPrintf(stdout, "purged %d sessions (txid %s)\n", len(rep.P), cleanArg(rep.TxID))
	if len(rep.P) == 0 {
		return 0
	}
	if verr := db.Vacuum(ctx); verr != nil {
		sessionsPrintf(stderr, "sessions prune: vacuum: %s\n", sessionsMessage(verr, ""))
		return 1
	}
	return 0
}

// reportPurgeSkips explains why candidates stayed in the database.
func reportPurgeSkips(rep *ctxdb.PurgeReport, stderr io.Writer) {
	for _, id := range rep.Pinned {
		sessionsPrintf(stderr, "sessions prune: skipped pinned %s\n", cleanArg(id))
	}
	for _, id := range rep.Skipped {
		sessionsPrintf(stderr, "sessions prune: skipped %s (has forks)\n", cleanArg(id))
	}
}

// sessionsFail prints one mapped store error and returns its exit code.
func sessionsFail(stderr io.Writer, name string, err error, id string) int {
	sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
	return sessionsErr(err)
}

// parseAge accepts Go durations and the day suffix of SPEC §10 ("90d").
// maxPruneAge bounds --older-than so day values cannot overflow time.Duration.
const maxPruneAge = 3650 * 24 * time.Hour

func parseAge(v string) (time.Duration, error) {
	var d time.Duration
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.ParseFloat(days, 64)
		if err != nil || !(n > 0 && n <= 3650) {
			return 0, fmt.Errorf("--older-than %q: want a positive duration such as 90d", cleanArg(v))
		}
		d = time.Duration(n * float64(24*time.Hour))
	} else {
		var err error
		d, err = time.ParseDuration(v)
		if err != nil {
			return 0, fmt.Errorf("--older-than %q: want a positive duration such as 90d", cleanArg(v))
		}
	}
	if d <= 0 || d > maxPruneAge {
		return 0, fmt.Errorf("--older-than %q: want a positive duration such as 90d", cleanArg(v))
	}
	return d, nil
}

// autoPrune runs at `run` startup when [sessions] auto_prune is enabled. It
// never fails the turn: a busy database or an aborted purge is reported and
// the run continues (the row stays for the next start).
func autoPrune(ctx context.Context, db *ctxdb.Store, cfg config.Sessions, stderr io.Writer) {
	if !cfg.AutoPrune {
		return
	}
	age := time.Duration(cfg.PruneAfterDays) * 24 * time.Hour
	rep, err := db.Purge(ctx, ctxdb.PurgeRequest{OlderThan: age, WithForks: true})
	if err != nil {
		if !errors.Is(err, ctxdb.ErrHasForks) {
			sessionsPrintf(stderr, "run: auto-prune: %s\n", sessionsMessage(err, ""))
		}
		return
	}
	if len(rep.P) == 0 {
		return
	}
	if verr := db.Vacuum(ctx); verr != nil {
		sessionsPrintf(stderr, "run: auto-prune: vacuum: %s\n", sessionsMessage(verr, ""))
	}
	sessionsPrintf(stderr, "run: auto-pruned %d sessions older than %d days\n", len(rep.P), cfg.PruneAfterDays)
}
