package main

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// wireStore opens a store with one session for the adapter tests.
func wireStore(t *testing.T) (*ctxdb.Store, string) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	const sid = "S1"
	if _, err := db.CreateSession(t.Context(), ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	return db, sid
}

// TestMemoryToolBackendEvidence: an agent write uses the dispatch event of the
// calling tool as evidence and is quarantined until approved.
func TestMemoryToolBackendEvidence(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, sid := wireStore(t)
	ids, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "tool_dispatch", ToolCallID: "c1", ToolName: "memory", Payload: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	svc, err := newMemoryService(db, redact.New(redact.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	be := &memoryToolBackend{svc: svc, db: db}
	id, status, err := be.Add(ctx, tools.MemoryAdd{CallID: "c1", Category: "fact", Scope: "user", Content: "likes tabs"})
	if err != nil || id == 0 || status != "scanned" {
		t.Fatalf("Add = %d/%q/%v, want a scanned entry", id, status, err)
	}
	var eventID int64
	var source, origin string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT evidence_event_id, source_session, origin FROM memory WHERE id = ?1", []any{id}, &eventID, &source, &origin)
	}); err != nil {
		t.Fatal(err)
	}
	if eventID != ids[0] || source != sid || origin != "agent" {
		t.Fatalf("evidence = %d/%s/%s, want %d/%s/agent", eventID, source, origin, ids[0], sid)
	}
	// Quarantined entries are not readable, and a call id without a dispatch
	// event fails closed.
	if entries, rerr := be.Read(ctx, "tabs", "user", 5); rerr != nil || len(entries) != 0 {
		t.Fatalf("read scanned = %d/%v, want none", len(entries), rerr)
	}
	if _, _, aerr := be.Add(ctx, tools.MemoryAdd{CallID: "missing", Category: "fact", Scope: "user", Content: "x"}); !errors.Is(aerr, tools.ErrMemoryInvalid) {
		t.Fatalf("missing evidence = %v, want ErrMemoryInvalid", aerr)
	}
	if rerr := be.Remove(ctx, 9999, "user"); !errors.Is(rerr, tools.ErrMemoryNotFound) {
		t.Fatalf("remove missing = %v, want ErrMemoryNotFound", rerr)
	}
}

// TestSessionSearchBackend: adapter returns FTS hits for a stored message.
func TestSessionSearchBackend(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db, sid := wireStore(t)
	if _, err := db.Append(ctx, sid, nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "needle in the transcript"}); err != nil {
		t.Fatal(err)
	}
	hits, err := ctxSearch{db: db}.Search(ctx, "needle", 5)
	if err != nil {
		t.Fatal(err)
	}
	if len(hits) != 1 || hits[0].SessionID != sid || hits[0].Kind != "user_message" {
		t.Fatalf("hits = %+v", hits)
	}
}
