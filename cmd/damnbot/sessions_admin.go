package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
)

// adminArgs parses the sessions admin subcommands. Flags may appear before or
// after the positional id (Go's flag package stops at the first positional).
type adminArgs struct {
	dataDir, format, out string
	noRedact             bool
	pos                  []string
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
	case s == "--no-redact":
		a.noRedact = true
	default:
		return false, nil
	}
	return true, nil
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

// sessionsFail prints one mapped store error and returns its exit code.
func sessionsFail(stderr io.Writer, name string, err error, id string) int {
	sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
	return sessionsErr(err)
}
