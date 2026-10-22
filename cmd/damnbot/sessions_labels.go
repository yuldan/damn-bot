package main

import (
	"context"
	"io"
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
