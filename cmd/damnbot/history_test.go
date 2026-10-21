package main

import (
	"context"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestHistoryDedupeToolResults: the dispatcher journal and the loop both
// append tool_result; history keeps one message per tool_call_id with the
// loop's non-empty content and the id intact.
func TestHistoryDedupeToolResults(t *testing.T) {
	t.Parallel()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	}()
	const sid = "S"
	if _, serr := db.CreateSession(context.Background(), ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); serr != nil {
		t.Fatal(serr)
	}
	_, err = db.Append(context.Background(), sid, nil,
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "go"},
		ctxdb.Event{Kind: "assistant_message", Role: "assistant", Payload: `{"tool_calls":[{"id":"c1","name":"glob","arguments":"{}"}]}`},
		ctxdb.Event{Kind: "tool_result", Role: "tool", ToolCallID: "c1", Payload: `{"ok":true}`},
		ctxdb.Event{Kind: "tool_result", Role: "tool", ToolCallID: "c1", Content: "result"},
	)
	if err != nil {
		t.Fatal(err)
	}
	msgs, err := (&ctxStore{db: db}).History(context.Background(), sid)
	if err != nil {
		t.Fatal(err)
	}
	var tools int
	for _, m := range msgs {
		if m.Role == "tool" {
			tools++
			if m.ToolCallID != "c1" || m.Content != "result" {
				t.Errorf("tool message = %+v", m)
			}
		}
	}
	if tools != 1 {
		t.Fatalf("tool messages = %d, want 1", tools)
	}
}
