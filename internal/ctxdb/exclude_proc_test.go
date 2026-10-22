package ctxdb

import (
	"context"
	"testing"
)

// TestKill9MidTurnExcludes is the multi-process crash path: a real child
// process writes a user message, starts a turn and is SIGKILLed before
// turn_end. Startup recovery in the parent closes it with
// turn_end{crashed} + context_exclude{crashed}; the rows stay and later
// requests skip the turn (GAP-REL-01, SPEC §3.3).
func TestKill9MidTurnExcludes(t *testing.T) {
	dir := t.TempDir()
	c := startChild(t, "turn", dir)
	c.kill9(t)
	s, err := Open(context.Background(), &Options{Dir: dir, Kind: "tui"})
	must(t, err)
	defer func() { must(t, s.Close()) }()

	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'crash' AND kind = 'turn_end' AND json_extract(payload, '$.stop_reason') = 'crashed'"); n != 1 {
		t.Fatalf("turn_end{crashed} = %d", n)
	}
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'crash' AND kind = 'context_exclude' AND json_extract(payload, '$.reason') = 'crashed'"); n != 1 {
		t.Fatalf("context_exclude{crashed} = %d", n)
	}
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = 'crash' AND kind IN ('user_message','assistant_message')"); n != 2 {
		t.Fatalf("turn rows = %d, want 2", n)
	}
	got, err := s.History(context.Background(), "crash", 0)
	must(t, err)
	for _, ev := range got {
		if ev.TurnID == "T1" {
			t.Fatalf("crashed turn still in context: %+v", ev)
		}
	}
}
