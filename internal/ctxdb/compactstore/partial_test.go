package compactstore

import (
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestLoadSkipsPartialAssistant (EC-PROV-008): a length-truncated answer the
// loop marked partial never enters a compaction snapshot, so a summary cannot
// reintroduce it; the transcript keeps the row.
func TestLoadSkipsPartialAssistant(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db) // t1, seq 1..5
	_, err := db.Append(t.Context(), sid, nil,
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "next", TurnID: "t2"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "cut", TurnID: "t2", Payload: `{"partial":true,"finish_reason":"length"}`},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Content: "final", TurnID: "t2"},
	)
	if err != nil {
		t.Fatal(err)
	}
	snap, err := s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range snap.Events {
		if e.Content == "cut" {
			t.Fatalf("partial event in snapshot: %+v", snap.Events)
		}
	}
	if n := count(t, db, "SELECT count(*) FROM events WHERE session_id = ? AND content = 'cut'"); n != 1 {
		t.Fatalf("partial row = %d, want 1 (transcript keeps it)", n)
	}
}
