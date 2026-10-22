package ctxdb

import (
	"context"
	"strings"
	"testing"
)

// TestCrashExcludesDanglingTurn: recovery closes an open journal with
// turn_end{crashed} and appends context_exclude{crashed}, so the half-run
// turn stays persisted and recallable but leaves later requests (GAP-REL-01,
// SPEC §3.3, §11.7).
func TestCrashExcludesDanglingTurn(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	a := e.open(1, "tui")
	session(t, a, "S", "")
	ctx := context.Background()
	must(t, a.Write(ctx, func(tx *Tx) error {
		if err := tx.StartTurn("S", "T1"); err != nil {
			return err
		}
		if _, err := tx.AppendEvent("S", &Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "poison ask"}); err != nil {
			return err
		}
		_, err := tx.AppendEvent("S", &Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "partial"})
		return err
	}))
	e.procs.kill(1)
	b := e.open(2, "tui") // startup recovery, case (a)

	if n := count(t, b, "SELECT count(*) FROM events WHERE session_id = 'S' AND kind = 'turn_end' AND json_extract(payload, '$.stop_reason') = 'crashed'"); n != 1 {
		t.Fatalf("turn_end{crashed} = %d", n)
	}
	var marker, reason string
	must(t, b.Write(ctx, func(tx *Tx) error {
		return tx.Get(`SELECT payload, json_extract(payload, '$.reason') FROM events
			WHERE session_id = 'S' AND kind = 'context_exclude' AND json_extract(payload, '$.turn_id') = 'T1'`, nil, &marker, &reason)
	}))
	if reason != "crashed" || !strings.Contains(marker, `"turn_id":"T1"`) {
		t.Fatalf("marker = %s", marker)
	}
	// Both rows are still present: nothing is deleted or rewritten (P9).
	if n := count(t, b, "SELECT count(*) FROM events WHERE session_id = 'S' AND kind IN ('user_message','assistant_message')"); n != 2 {
		t.Fatalf("turn rows = %d, want 2", n)
	}
	got, err := b.History(ctx, "S", 0)
	must(t, err)
	for _, ev := range got {
		if ev.TurnID == "T1" {
			t.Fatalf("excluded turn still in context: %+v", ev)
		}
	}
	all, err := b.History(ctx, "S", 0, WithHistoryExcluded())
	must(t, err)
	if len(all) != 6 { // user, assistant, turn_start, turn_end, marker, crash_recovered
		t.Fatalf("stored history = %d rows: %+v", len(all), all)
	}
	// Recall still finds it (SPEC §22.1: tetap dapat di-recall).
	hits, err := b.Search(ctx, Query{Text: "poison", SessionID: "S"})
	must(t, err)
	if len(hits) == 0 || hits[0].Kind != "user_message" || !strings.Contains(hits[0].Snippet, "poison") || !hits[0].Excluded {
		t.Fatalf("recall = %+v, want an excluded hit (SEC11-5)", hits)
	}
}

// TestHistoryDropsExcludedSummary: a summary covering an excluded turn must
// not re-introduce it (SEC11-1); exclusion is applied before summaries.
func TestHistoryDropsExcludedSummary(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	must(t, s.Append2(ctx, "S",
		Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "poison"},
		Event{Kind: "assistant_message", TurnID: "T1", Role: "assistant", Content: "poison answer"},
		Event{Kind: "tool_result", TurnID: "T1", Role: "tool", Content: "poison result", ToolCallID: "c1", ToolName: "shell"},
	))
	must(t, s.Write(ctx, func(tx *Tx) error {
		id, err := tx.AppendEvent("S", &Event{Kind: "summary_ref", Payload: `{"summary_id":1,"from_seq":1,"to_seq":3}`})
		if err != nil {
			return err
		}
		_, err = tx.Exec(`INSERT INTO summaries(session_id, from_seq, to_seq, content, model, input_tokens, output_tokens, event_id, created_at)
			VALUES ('S', 1, 3, '{"goal":"poison"}', 'm', 1, 1, ?, 0)`, id)
		return err
	}))
	must(t, s.Append2(ctx, "S", Event{Kind: "context_exclude", Payload: `{"turn_id":"T1","reason":"provider_error"}`}))
	got, err := s.History(ctx, "S", 0)
	must(t, err)
	for _, ev := range got {
		if strings.Contains(ev.Content, "poison") || ev.TurnID == "T1" {
			t.Fatalf("excluded summary or turn returned: %+v", ev)
		}
	}
	all, err := s.History(ctx, "S", 0, WithHistoryExcluded())
	must(t, err)
	found := false
	for _, ev := range all {
		found = found || ev.Kind == "summary_ref"
	}
	if !found {
		t.Fatal("audit view must still show the summary")
	}
}

// TestSpanMarkerIgnored: a from_seq-only marker never hides later turns
// (SEC11-4).
func TestSpanMarkerIgnored(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	must(t, s.Append2(ctx, "S",
		Event{Kind: "user_message", TurnID: "T1", Role: "user", Content: "first"},
		Event{Kind: "context_exclude", Payload: `{"from_seq":1,"reason":"cancelled"}`},
		Event{Kind: "user_message", TurnID: "T2", Role: "user", Content: "later"},
	))
	got, err := s.History(ctx, "S", 0)
	must(t, err)
	seen := map[string]bool{}
	for _, ev := range got {
		seen[ev.Content] = true
	}
	if !seen["first"] || !seen["later"] {
		t.Fatalf("span marker hid history: %+v", got)
	}
}
