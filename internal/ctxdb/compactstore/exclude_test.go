package compactstore

import (
	"reflect"
	"testing"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestLoadSkipsExcluded: context_exclude removes a failed turn's range from
// the compaction view, so a later summary can never reintroduce it
// (GAP-REL-01). The rows stay in the transcript.
func TestLoadSkipsExcluded(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	png := seed(t, db) // t1, seq 1..5
	_, err := db.Append(t.Context(), sid, nil,
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "poison", TurnID: "t2"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "partial", TurnID: "t2"},
		ctxdb.Event{Kind: "tool_result", Role: "tool", Content: "poison result", TurnID: "t2", ToolCallID: "c2", ToolName: "shell"},
		// Span-marked turn without a turn_id on the rows.
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "span poison"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "span partial"},
		ctxdb.Event{Kind: "context_exclude", TurnID: "t2", Payload: `{"turn_id":"t2","reason":"provider_error"}`},
		ctxdb.Event{Kind: "context_exclude", Payload: `{"from_seq":9,"to_seq":10,"reason":"cancelled"}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	want := []compaction.Event{
		{Seq: 1, Kind: "user_message", TurnID: "t1", Role: "user", Content: "look", Payload: "{}", Images: []string{png}},
		{
			Seq: 4, Kind: "assistant_message", TurnID: "t1", Role: "assistant",
			Payload: `{"tool_calls":[{"id":"c1","name":"read_file","arguments":"{\"path\":\"a\"}"}]}`, Images: []string{},
		},
		{
			Seq: 5, Kind: "tool_result", TurnID: "t1", Role: "tool", Content: "data", ToolCallID: "c1", ToolName: "read_file",
			Payload: "{}", Images: []string{},
		},
		// SEC11-4: a span marker with no turn_id excludes nothing.
		{Seq: 9, Kind: "user_message", Role: "user", Content: "span poison", Payload: "{}", Images: []string{}},
		{Seq: 10, Kind: "assistant_message", Role: "assistant", Content: "span partial", Payload: "{}", Images: []string{}},
	}
	if !reflect.DeepEqual(snap.Events, want) {
		t.Fatalf("events = %+v", snap.Events)
	}
	if snap.Task != nil {
		t.Fatalf("task = %+v, want the newest non-excluded user message (none after boundary)", snap.Task)
	}
	// The excluded rows still exist (P9): nothing is deleted.
	if n := count(t, db, "SELECT count(*) FROM events WHERE session_id = ? AND content LIKE '%poison%'"); n != 3 {
		t.Fatalf("poison rows = %d, want 3", n)
	}
}

// TestLoadDropsExcludedSummary: a summary created inside a turn that later
// failed is not handed to compaction again (SEC11-1).
func TestLoadDropsExcludedSummary(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db)
	sum := &compaction.Summary{FromSeq: 1, ToSeq: 4, Content: `{"goal":"g"}`, Model: "m", InputTokens: 9, OutputTokens: 1}
	_, err := s.Commit(t.Context(), sid, &compaction.Commit{
		TurnID: "t1", Attempt: compaction.Attempt{Outcome: compaction.OutcomeSummarized}, Summary: sum,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, aerr := db.Append(t.Context(), sid, nil, ctxdb.Event{
		Kind: "context_exclude", TurnID: "t1", Payload: `{"turn_id":"t1","reason":"provider_error"}`,
	}); aerr != nil {
		t.Fatal(aerr)
	}
	snap, err := s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Summaries) != 0 || len(snap.Events) != 0 {
		t.Fatalf("snapshot after exclusion = %+v", snap)
	}
}
