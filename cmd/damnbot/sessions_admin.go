package main

import (
	"context"
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
	value := func(i *int, name string) (string, error) {
		if *i+1 >= len(args) {
			return "", fmt.Errorf("%s needs a value", name)
		}
		*i++
		return args[*i], nil
	}
	for i := 0; i < len(args); i++ {
		s := args[i]
		var err error
		switch {
		case s == "--data-dir" || s == "-data-dir":
			a.dataDir, err = value(&i, s)
		case strings.HasPrefix(s, "--data-dir="):
			a.dataDir = strings.TrimPrefix(s, "--data-dir=")
		case s == "--format" || s == "-format":
			a.format, err = value(&i, s)
		case strings.HasPrefix(s, "--format="):
			a.format = strings.TrimPrefix(s, "--format=")
		case s == "--out" || s == "-out":
			a.out, err = value(&i, s)
		case strings.HasPrefix(s, "--out="):
			a.out = strings.TrimPrefix(s, "--out=")
		case s == "--no-redact":
			a.noRedact = true
		case strings.HasPrefix(s, "-") && s != "-":
			err = fmt.Errorf("unknown flag %q", s)
		default:
			a.pos = append(a.pos, s)
		}
		if err != nil {
			return a, err
		}
	}
	return a, nil
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
	if !exportFormats[format] {
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
	info, derr := db.SessionInfo(ctx, id)
	if derr != nil {
		return sessionsFail(stderr, "export", derr, id)
	}
	events, derr := db.History(ctx, id, 0, ctxdb.WithHistoryRaw(), ctxdb.WithHistoryExcluded(), ctxdb.WithHistoryLimit(100000))
	if derr != nil {
		return sessionsFail(stderr, "export", derr, id)
	}
	title, _, _ := db.SessionLabel(ctx, id, ctxdb.LabelTitle)
	var red *redact.Redactor
	if !a.noRedact {
		red = redact.New(redact.Options{})
	}
	out := stdout
	if a.out != "" && a.out != "-" {
		f, ferr := os.OpenFile(a.out, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
		if ferr != nil {
			sessionsPrintf(stderr, "sessions export: %v\n", ferr)
			return 1
		}
		defer func() {
			if cerr := f.Close(); cerr != nil {
				sessionsPrintf(stderr, "sessions export: close: %v\n", cerr)
			}
		}()
		out = f
	}
	meta := exportMeta{ID: info.ID, Title: title, Model: info.Model, Origin: info.Origin, Cwd: info.Cwd, CreatedAt: info.CreatedAt}
	if werr := writeExport(out, format, meta, events, red); werr != nil {
		sessionsPrintf(stderr, "sessions export: %v\n", werr)
		return 1
	}
	return 0
}

// sessionsFlag sets one boolean label (archive/unarchive/pin/unpin).
func sessionsFlag(name string, args []string, stdout, stderr io.Writer, key, value string) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			sessionsPrintf(stderr, "sessions %s: %v\n", name, err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, name, derr, a.pos[0])
	}
	defer closeSessionsDB(db, "sessions "+name, stderr)
	if derr := db.SetSessionLabel(ctx, a.pos[0], key, value); derr != nil {
		return sessionsFail(stderr, name, derr, a.pos[0])
	}
	sessionsPrintf(stdout, "%s %s\n", name, cleanArg(a.pos[0]))
	return 0
}

// sessionsPinned lists pinned sessions (CLI-057).
func sessionsPinned(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "sessions pinned: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "pinned", derr, "")
	}
	defer closeSessionsDB(db, "sessions pinned", stderr)
	rows, derr := db.SessionRows(ctx)
	if derr != nil {
		return sessionsFail(stderr, "pinned", derr, "")
	}
	for _, r := range rows {
		if r.Pinned {
			sessionsPrintf(stdout, "%s\t%s\n", cleanArg(r.ID), r.Title)
		}
	}
	return 0
}

// sessionsStats prints the session/event totals of the data dir.
func sessionsStats(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "sessions stats: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, derr := openSessionsDBReadOnly(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "stats", derr, "")
	}
	defer closeSessionsDB(db, "sessions stats", stderr)
	rows, derr := db.SessionRows(ctx)
	if derr != nil {
		return sessionsFail(stderr, "stats", derr, "")
	}
	var archived, pinned, events int64
	for _, r := range rows {
		if r.Archived {
			archived++
		}
		if r.Pinned {
			pinned++
		}
		events += r.Events
	}
	sessionsPrintf(stdout, "sessions: %d\narchived: %d\npinned: %d\nevents: %d\n", len(rows), archived, pinned, events)
	return 0
}

// sessionsFail prints one mapped store error and returns its exit code.
func sessionsFail(stderr io.Writer, name string, err error, id string) int {
	sessionsPrintf(stderr, "sessions %s: %s\n", name, sessionsMessage(err, id))
	return sessionsErr(err)
}
