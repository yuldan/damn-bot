package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// inspectArgs parses the read-only subcommands: positional id(s), --data-dir
// and --format before or after the positional.
func inspectArgs(args []string) (pos []string, dataDir, format string, err error) {
	format = "jsonl"
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch {
		case a == "--data-dir" || a == "-data-dir":
			if i+1 >= len(args) {
				return nil, "", "", fmt.Errorf("--data-dir needs a value")
			}
			i++
			dataDir = args[i]
		case strings.HasPrefix(a, "--data-dir="):
			dataDir = strings.TrimPrefix(a, "--data-dir=")
		case a == "--format" || a == "-format":
			if i+1 >= len(args) {
				return nil, "", "", fmt.Errorf("--format needs a value")
			}
			i++
			format = args[i]
		case strings.HasPrefix(a, "--format="):
			format = strings.TrimPrefix(a, "--format=")
		case strings.HasPrefix(a, "-"):
			return nil, "", "", fmt.Errorf("unknown flag %q", a)
		default:
			pos = append(pos, a)
		}
	}
	return pos, dataDir, format, nil
}

// sessionsList prints visible session metadata (newest first).
func sessionsList(args []string, stdout, stderr io.Writer) int {
	pos, dataDir, _, err := inspectArgs(args)
	if err != nil || len(pos) != 0 {
		if err != nil {
			sessionsPrintf(stderr, "sessions ls: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	db, err := openSessionsDBReadOnly(ctx, dataDir)
	if err != nil {
		sessionsPrintf(stderr, "sessions ls: %s\n", sessionsMessage(err, ""))
		return 1
	}
	defer closeSessionsDB(db, "sessions ls", stderr)
	ids, err := db.ListSessions(ctx)
	if err != nil {
		sessionsPrintf(stderr, "sessions ls: %s\n", sessionsMessage(err, ""))
		return 1
	}
	for _, id := range ids {
		in, ierr := db.SessionInfo(ctx, id)
		if ierr != nil {
			continue // purged between list and lookup
		}
		sessionsPrintf(stdout, "%s\t%s\t%s\n", cleanArg(in.ID), cleanArg(in.Cwd), cleanArg(in.Model))
	}
	return 0
}

// sessionsShow prints metadata and the visible transcript size.
func sessionsShow(args []string, stdout, stderr io.Writer) int {
	pos, dataDir, _, err := inspectArgs(args)
	if err != nil || len(pos) != 1 {
		if err != nil {
			sessionsPrintf(stderr, "sessions show: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	id := pos[0]
	ctx := context.Background()
	db, err := openSessionsDBReadOnly(ctx, dataDir)
	if err != nil {
		sessionsPrintf(stderr, "sessions show: %s\n", sessionsMessage(err, id))
		return 1
	}
	defer closeSessionsDB(db, "sessions show", stderr)
	in, err := db.SessionInfo(ctx, id)
	if err != nil {
		sessionsPrintf(stderr, "sessions show: %s\n", sessionsMessage(err, id))
		return sessionsErr(err)
	}
	events, err := db.History(ctx, id, 0, ctxdb.WithHistoryRaw(), ctxdb.WithHistoryExcluded())
	if err != nil {
		sessionsPrintf(stderr, "sessions show: %s\n", sessionsMessage(err, id))
		return sessionsErr(err)
	}
	sessionsPrintf(stdout, "id: %s\norigin: %s\ncwd: %s\nmodel: %s\nevents: %d\n", cleanArg(in.ID), cleanArg(in.Origin), cleanArg(in.Cwd), cleanArg(in.Model), len(events))
	return 0
}

// sessionsExport writes the raw transcript as jsonl or markdown.
func sessionsExport(args []string, stdout, stderr io.Writer) int {
	pos, dataDir, format, err := inspectArgs(args)
	if err != nil || len(pos) != 1 {
		if err != nil {
			sessionsPrintf(stderr, "sessions export: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	if format != "jsonl" && format != "md" {
		sessionsPrintf(stderr, "sessions export: --format must be jsonl or md\n")
		return 2
	}
	id := pos[0]
	ctx := context.Background()
	db, err := openSessionsDBReadOnly(ctx, dataDir)
	if err != nil {
		sessionsPrintf(stderr, "sessions export: %s\n", sessionsMessage(err, id))
		return 1
	}
	defer closeSessionsDB(db, "sessions export", stderr)
	events, err := db.History(ctx, id, 0, ctxdb.WithHistoryRaw(), ctxdb.WithHistoryExcluded())
	if err != nil {
		sessionsPrintf(stderr, "sessions export: %s\n", sessionsMessage(err, id))
		return sessionsErr(err)
	}
	if format == "md" {
		sessionsPrintf(stdout, "# session %s\n\n", cleanArg(id))
	}
	for i := range events {
		e := &events[i]
		if format == "md" {
			sessionsPrintf(stdout, "## %d %s\n\n%s\n\n", e.Seq, cleanArg(e.Kind), e.Content)
			continue
		}
		line, merr := json.Marshal(e)
		if merr != nil {
			sessionsPrintf(stderr, "sessions export: encode event %d\n", e.Seq)
			return 1
		}
		if _, werr := fmt.Fprintln(stdout, string(line)); werr != nil {
			return 1
		}
	}
	return 0
}

func closeSessionsDB(db *ctxdb.Store, name string, stderr io.Writer) {
	if err := db.Close(); err != nil {
		sessionsPrintf(stderr, "%s: close db: %v\n", name, err)
	}
}
