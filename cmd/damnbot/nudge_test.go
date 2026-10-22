package main

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestNudgeInterval (MEM-003): nudgeFacts fires on the 10th, 20th, … user turn —
// the count read before the turn is the number of turns already stored, so the
// reminder lands when that count is one short of a multiple of the interval.
func TestNudgeInterval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := recapStore(t)
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	model := &runModel{sessionID: "S1", store: &ctxStore{db: db}}
	for n := 0; n <= 2*memoryNudgeInterval; n++ {
		got := nudgeFacts(ctx, model)
		if want := n%memoryNudgeInterval == memoryNudgeInterval-1; (got != "") != want {
			t.Errorf("nudgeFacts with %d stored user turns = %q, want nudge=%v", n, got, want)
		}
		if _, err := db.Append(ctx, "S1", nil, ctxdb.Event{Kind: "user_message", Role: "user", Content: "t"}); err != nil {
			t.Fatal(err)
		}
	}
}
