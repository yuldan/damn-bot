package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

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

// sessionsContextPin pins a transcript position against compaction or lists the
// pinned ones (MEM-025). Pins are append-only rows in session_pins, so the pin
// survives a resume and nothing is deleted: `--off` appends a released state.
func sessionsContextPin(args []string, stdout, stderr io.Writer) int {
	a, err := parseAdminArgs(args)
	if err != nil || len(a.pos) != 1 {
		if err != nil {
			sessionsPrintf(stderr, "sessions context-pin: %v\n", err)
		}
		sessionsUsage(stderr)
		return 2
	}
	ctx := context.Background()
	if a.pinAt == "" {
		return contextPinList(ctx, a.dataDir, a.pos[0], stdout, stderr)
	}
	return contextPinSet(ctx, &a, stdout, stderr)
}

// contextPinList prints the pinned positions of one session (read-only).
func contextPinList(ctx context.Context, dataDir, id string, stdout, stderr io.Writer) int {
	db, derr := openSessionsDBReadOnly(ctx, dataDir)
	if derr != nil {
		return sessionsFail(stderr, "context-pin", derr, id)
	}
	defer func() {
		if cerr := db.CloseContext(ctx); cerr != nil {
			sessionsPrintf(stderr, "sessions context-pin: close: %v\n", cerr)
		}
	}()
	pins, perr := db.SessionPinSeqs(ctx, id)
	if perr != nil {
		return sessionsFail(stderr, "context-pin", perr, id)
	}
	if len(pins) == 0 {
		sessionsPrintf(stdout, "no pinned context\n")
		return 0
	}
	for _, n := range pins {
		sessionsPrintf(stdout, "pin %d\n", n)
	}
	return 0
}

// contextPinSet pins (or releases, with --off) one transcript position.
func contextPinSet(ctx context.Context, a *adminArgs, stdout, stderr io.Writer) int {
	id := a.pos[0]
	if id == "newest" { // "newest" names the newest session (useful for scripts)
		var code int
		if id, code = newestSessionAlias(ctx, a.dataDir, stderr); code != 0 {
			return code
		}
	}
	db, derr := openSessionsDB(ctx, a.dataDir)
	if derr != nil {
		return sessionsFail(stderr, "context-pin", derr, id)
	}
	defer func() {
		if cerr := db.CloseContext(ctx); cerr != nil {
			sessionsPrintf(stderr, "sessions context-pin: close: %v\n", cerr)
		}
	}()
	seq, serr := pinSeq(ctx, db, id, a.pinAt)
	if serr != nil {
		sessionsPrintf(stderr, "sessions context-pin: %v\n", serr)
		return 2
	}
	if werr := db.SetSessionPin(ctx, id, seq, !a.off); werr != nil {
		return sessionsFail(stderr, "context-pin", werr, id)
	}
	if a.off {
		sessionsPrintf(stdout, "unpinned %d\n", seq)
	} else {
		sessionsPrintf(stdout, "pinned %d\n", seq)
	}
	return 0
}

// newestSessionAlias resolves the "newest" session alias (renamed on rebase: main
// added newestSession(ctx, db, cwd) for resume, ADR-138).
func newestSessionAlias(ctx context.Context, dataDir string, stderr io.Writer) (string, int) {
	db, derr := openSessionsDBReadOnly(ctx, dataDir)
	if derr != nil {
		return "", sessionsFail(stderr, "context-pin", derr, "newest")
	}
	ids, lerr := db.ListSessions(ctx)
	if cerr := db.CloseContext(ctx); cerr != nil {
		sessionsPrintf(stderr, "sessions context-pin: close: %v\n", cerr)
	}
	if lerr != nil || len(ids) == 0 {
		sessionsPrintf(stderr, "sessions context-pin: no session to pin\n")
		return "", 1
	}
	return ids[0], 0
}

// pinSeq resolves --at for context-pin: a transcript seq, or turn:<id> meaning
// the first event of that turn (the message that opened it).
func pinSeq(ctx context.Context, db *ctxdb.Store, id, v string) (int64, error) {
	if turn, ok := strings.CutPrefix(v, "turn:"); ok {
		first, _, err := db.TurnRange(ctx, id, cleanArg(turn))
		if errors.Is(err, ctxdb.ErrNotFound) {
			return 0, fmt.Errorf("turn %s not found", cleanArg(turn))
		}
		if err != nil {
			return 0, fmt.Errorf("turn %s: lookup failed", cleanArg(turn))
		}
		return first, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("--at %q: want a positive seq or turn:<id>", cleanArg(v))
	}
	return n, nil
}
