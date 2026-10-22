package compactstore

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/redact"
)

// scripted is a Summarizer returning reply (or err) and counting calls.
type scripted struct {
	reply string
	err   error
	calls int
}

func (s *scripted) Summarize(context.Context, *compaction.SummaryRequest) (compaction.Reply, error) {
	s.calls++
	if s.err != nil {
		return compaction.Reply{}, s.err
	}
	return compaction.Reply{Content: s.reply, Call: compaction.Call{Provider: "p", Model: "m", Usage: event.Usage{Input: 10, Output: 5}}}, nil
}

// charMeter estimates 4 chars per token over the view.
type charMeter struct{ window int64 }

func (m charMeter) Measure(_ context.Context, _ string, v *compaction.View) (est, window int64, err error) {
	for _, msg := range v.Messages {
		est += int64(len(msg.Content)/4 + 4)
	}
	return est, m.window, nil
}

// seedRounds appends a request and 12 grep rounds of turn t1.
func seedRounds(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	evs := []ctxdb.Event{{Kind: "user_message", Role: "user", Content: "port the parser", TurnID: "t1"}}
	for i := range 12 {
		id := fmt.Sprintf("c%d", i)
		evs = append(evs, ctxdb.Event{
			Kind: "assistant_message", Role: "assistant", TurnID: "t1",
			Payload: fmt.Sprintf(`{"tool_calls":[{"id":%q,"name":"grep","arguments":"{}"}]}`, id),
		}, ctxdb.Event{
			Kind: "tool_result", Role: "tool", TurnID: "t1", ToolCallID: id, ToolName: "grep",
			Content: strings.Repeat(id+" hit ", 200), Payload: "{}",
		})
	}
	if _, err := db.Append(t.Context(), sid, nil, evs...); err != nil {
		t.Fatal(err)
	}
}

// compactor builds a Compactor over a fresh Store on db: two calls share
// nothing but the database, like two processes.
func compactor(t *testing.T, db *ctxdb.Store, sum compaction.Summarizer, now func() time.Time) *compaction.Compactor {
	t.Helper()
	c, err := compaction.NewCompactor(&compaction.Deps{
		Store: New(db, now), Summarizer: sum, Meter: charMeter{window: 2_400},
		Redactor: redact.New(redact.Options{}), Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// TestCompactorOverCtxdb: both stages run against real storage; a fresh
// Compactor renders the stored, redacted summary over the pruned view and
// restates the summarised request, and the call left its ledger trace.
func TestCompactorOverCtxdb(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	seedRounds(t, db)
	secret := "ghp_" + strings.Repeat("Qw3r", 9)
	sum := &scripted{reply: `{"goal":"port the parser with ` + secret + `","decisions":[],"files":["p.go"],"open_items":[],"facts":[]}`}
	if res, err := compactor(t, db, sum, time.Now).Run(t.Context(), sid, compaction.ReasonEstimate); err != nil ||
		res.Outcome != compaction.OutcomeSummarized {
		t.Fatalf("Run = %+v, %v", res, err)
	}
	v, err := compactor(t, db, &scripted{}, time.Now).View(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	if len(v.Summaries) != 1 || !v.Pruned || !strings.HasPrefix(v.Messages[0].Content, "[summary of events 1-") ||
		v.Messages[1].Content != "port the parser" || v.Messages[2].Role != "assistant" {
		t.Fatalf("view = %+v", v.Messages[:3])
	}
	if n := count(t, db, "SELECT count(*) FROM usage_ledger WHERE session_id = ? AND purpose = 'compaction' AND turn_id = 't1'"); n != 1 {
		t.Errorf("compaction ledger rows = %d", n)
	}
	if n := count(t, db, "SELECT count(*) FROM summaries WHERE session_id = ? AND content LIKE '%[REDACTED:%' AND instr(content, '"+secret+"') = 0"); n != 1 {
		t.Errorf("redacted summaries = %d", n)
	}
}

// TestCooldownAcrossProcesses: after a failed summary, a Compactor that
// shares only the database pays for no call until the cooldown ends, then
// for exactly one probe (EC-CTX-003, P11).
func TestCooldownAcrossProcesses(t *testing.T) {
	t.Parallel()
	db, _ := open(t)
	seedRounds(t, db)
	at := time.UnixMilli(1_000_000)
	now := func() time.Time { return at }
	down := &scripted{err: errors.New("503")}
	if res, err := compactor(t, db, down, now).Run(t.Context(), sid, compaction.ReasonEstimate); err != nil || res.Outcome != compaction.OutcomeFailed {
		t.Fatalf("first Run = %+v, %v", res, err)
	}
	up := &scripted{reply: `{"goal":"g","decisions":[],"files":[],"open_items":[],"facts":[]}`}
	at = at.Add(compaction.DefaultCooldown - time.Millisecond)
	for _, reason := range []compaction.Reason{compaction.ReasonEstimate, compaction.ReasonContextLength} {
		res, err := compactor(t, db, up, now).Run(t.Context(), sid, reason)
		if err != nil || res.Outcome != compaction.OutcomeCooldown || up.calls != 0 {
			t.Fatalf("%s inside the cooldown = %+v, %v, calls %d", reason, res, err, up.calls)
		}
	}
	at = at.Add(time.Millisecond)
	if res, err := compactor(t, db, up, now).Run(t.Context(), sid, compaction.ReasonEstimate); err != nil || up.calls != 1 {
		t.Fatalf("probe = %+v, %v, calls %d", res, err, up.calls)
	}
	if n := count(t, db, "SELECT count(*) FROM usage_ledger WHERE session_id = ? AND purpose = 'compaction'"); n != 1 {
		t.Errorf("paid calls recorded = %d, want 1", n)
	}
	if n := count(t, db, "SELECT count(*) FROM events WHERE session_id = ? AND kind = 'compaction'"); n != 4 {
		t.Errorf("compaction events = %d, want 4", n)
	}
}
