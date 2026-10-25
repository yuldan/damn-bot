package compactstore

import (
	"testing"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop/event"
)

// hasContent reports whether any loaded event or message carries want.
func hasContent(snap *compaction.Snapshot, want string) bool {
	for i := range snap.Events {
		if snap.Events[i].Content == want {
			return true
		}
	}
	return false
}

// TestLoadKeepsPinnedTurn (MEM-025, ADR-186): a summary boundary that passed a
// pinned transcript position still loads its whole turn back into the view,
// because the store expands pins to the turn they belong to; without the pin
// the same turn leaves the view (control).
func TestLoadKeepsPinnedTurn(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db) // turn t1: seqs 1..5, the tool result "data" sits at seq 5
	if _, err := db.Append(t.Context(), sid, nil,
		ctxdb.Event{Kind: string(event.UserMessage), Role: "user", Content: "second", TurnID: "t2"},
	); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Commit(t.Context(), sid, &compaction.Commit{
		TurnID:  "t1",
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized, Reason: compaction.ReasonContextLength, AtMS: 7},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 5, Content: `{"goal":"g"}`, Model: "m", InputTokens: 90, OutputTokens: 9},
		Call:    &compaction.Call{Provider: "p", Model: "m", Usage: event.Usage{Input: 90, Output: 9}, LatencyMS: 7},
	}); err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if hasContent(&snap, "data") {
		t.Errorf("control: the unpinned turn survived the boundary: %+v", snap.Events)
	}
	if len(snap.Pins) != 0 {
		t.Errorf("unpinned snapshot carries pins: %v", snap.Pins)
	}
	if err := db.SetSessionPin(t.Context(), sid, 5, true); err != nil {
		t.Fatal(err)
	}
	loaded, lerr := s.Load(t.Context(), sid)
	if lerr != nil {
		t.Fatal(lerr)
	}
	snap = loaded
	if !hasContent(&snap, "data") {
		t.Errorf("the pinned turn is missing from the view: %+v", snap.Events)
	}
	if len(snap.Pins) != 1 || snap.Pins[0] != 5 {
		t.Errorf("snapshot pins = %v, want [5]", snap.Pins)
	}
}
