package compactstore

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/compaction"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/loop/event"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

const sid = "s1"

func open(t *testing.T) (*ctxdb.Store, *Store) {
	t.Helper()
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if _, err = db.CreateSession(t.Context(), ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	return db, New(db, func() time.Time { return time.UnixMilli(42) })
}

// seed appends a user message with a png and a text attachment, a tool
// round, and bookkeeping events; it returns the png sha.
func seed(t *testing.T, db *ctxdb.Store) (png string) {
	t.Helper()
	img, err := db.PutBlob([]byte("png bytes"), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	txt, err := db.PutBlob([]byte("notes"), "text/plain")
	if err != nil {
		t.Fatal(err)
	}
	_, err = db.Append(t.Context(), sid, []*ctxdb.PendingBlob{img, txt},
		ctxdb.Event{Kind: "user_message", Role: "user", Content: "look", TurnID: "t1", Attach: []string{txt.Sha, img.Sha}},
		ctxdb.Event{Kind: "turn_start", TurnID: "t1", Payload: `{}`},
		ctxdb.Event{Kind: "llm_call", TurnID: "t1", Payload: `{"model":"m"}`},
		ctxdb.Event{
			Kind: "assistant_message", Role: "assistant", TurnID: "t1",
			Payload: `{"tool_calls":[{"id":"c1","name":"read_file","arguments":"{\"path\":\"a\"}"}]}`,
		},
		ctxdb.Event{Kind: "tool_result", Role: "tool", Content: "data", TurnID: "t1", ToolCallID: "c1", ToolName: "read_file", Payload: `{}`},
	)
	if err != nil {
		t.Fatal(err)
	}
	return img.Sha
}

func count(t *testing.T, db *ctxdb.Store, q string) int64 {
	t.Helper()
	n, err := db.Count(t.Context(), q, sid)
	if err != nil {
		t.Fatal(err)
	}
	return n
}

func TestLoadCommitRoundTrip(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	png := seed(t, db)
	snap, err := s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	want := []compaction.Event{
		{Seq: 1, Kind: event.UserMessage, TurnID: "t1", Role: "user", Content: "look", Payload: "{}", Images: []string{png}},
		{
			Seq: 4, Kind: event.AssistantMessage, TurnID: "t1", Role: "assistant",
			Payload: `{"tool_calls":[{"id":"c1","name":"read_file","arguments":"{\"path\":\"a\"}"}]}`, Images: []string{},
		},
		{
			Seq: 5, Kind: event.ToolResult, TurnID: "t1", Role: "tool", Content: "data", ToolCallID: "c1", ToolName: "read_file",
			Payload: "{}", Images: []string{},
		},
	}
	if len(snap.Summaries) != 0 || snap.Task != nil || snap.Compacted || snap.LastCall != nil || !reflect.DeepEqual(snap.Events, want) {
		t.Fatalf("snapshot = %+v", snap)
	}
	cost := 0.25
	sum := &compaction.Summary{FromSeq: 1, ToSeq: 4, Content: `{"goal":"g"}`, Model: "m", InputTokens: 90, OutputTokens: 9, Tainted: true}
	attempt := compaction.Attempt{Outcome: compaction.OutcomeSummarized, Reason: compaction.ReasonContextLength, AtMS: 7}
	id, err := s.Commit(t.Context(), sid, &compaction.Commit{
		TurnID: "t1", Attempt: attempt, Summary: sum,
		Call: &compaction.Call{Provider: "p", Model: "m", Usage: event.Usage{Input: 90, Output: 9, CostUSD: &cost}, LatencyMS: 7},
	})
	if err != nil || id == 0 {
		t.Fatalf("commit = %d, %v", id, err)
	}
	snap, err = s.Load(t.Context(), sid)
	if err != nil {
		t.Fatal(err)
	}
	sum.ID = id
	if !reflect.DeepEqual(snap.Summaries, []compaction.Summary{*sum}) || !reflect.DeepEqual(snap.Events, want[2:]) ||
		!reflect.DeepEqual(snap.Task, &want[0]) || !snap.Compacted || !reflect.DeepEqual(snap.LastCall, &attempt) {
		t.Fatalf("after commit = %+v", snap)
	}
	checkCommitRows(t, db)
}

// checkCommitRows: the ledger row hangs off the llm_call event and the
// summary row off the summary_ref event.
func checkCommitRows(t *testing.T, db *ctxdb.Store) {
	t.Helper()
	tot, err := ledgerstore.SessionTotals(t.Context(), db, sid)
	if err != nil || tot.Calls != 1 || tot.Input != 90 || tot.CostMicros != 250000 {
		t.Fatalf("ledger = %+v, %v", tot, err)
	}
	if n := count(t, db, `SELECT count(*) FROM usage_ledger l JOIN events e ON e.id = l.event_id
		WHERE l.session_id = ? AND l.purpose = 'compaction' AND e.kind = 'llm_call' AND l.created_at = 42`); n != 1 {
		t.Errorf("compaction ledger rows anchored to llm_call = %d", n)
	}
	if n := count(t, db, `SELECT count(*) FROM summaries s JOIN events e ON e.id = s.event_id WHERE s.session_id = ?
		AND e.kind = 'summary_ref' AND json_extract(e.payload, '$.to_seq') = 4`); n != 1 {
		t.Errorf("summary rows anchored to summary_ref = %d", n)
	}
}

// TestCommitRejectedSummary: a rejected reply still leaves its llm_call
// trace and ledger row (P11) but no summary.
func TestCommitRejectedSummary(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db)
	id, err := s.Commit(t.Context(), sid, &compaction.Commit{
		Attempt: compaction.Attempt{Outcome: compaction.OutcomeRejected},
		Call:    &compaction.Call{Provider: "p", Model: "m", Usage: event.Usage{Input: 5}},
	})
	if err != nil || id != 0 {
		t.Fatalf("commit = %d, %v", id, err)
	}
	if n := count(t, db, "SELECT count(*) FROM usage_ledger WHERE session_id = ? AND purpose = 'compaction' AND cost_usd_micros IS NULL"); n != 1 {
		t.Errorf("ledger rows = %d", n)
	}
	if n := count(t, db, "SELECT count(*) FROM summaries WHERE session_id = ?"); n != 0 {
		t.Errorf("summaries = %d", n)
	}
}

// TestCommitAtomic: a failing summary insert rolls back the call trace too,
// so a retry never duplicates rows or leaves a dangling summary_ref.
func TestCommitAtomic(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db)
	before := count(t, db, "SELECT count(*) FROM events WHERE session_id = ?")
	_, err := s.Commit(t.Context(), sid, &compaction.Commit{
		Call:    &compaction.Call{Provider: "p", Model: "m"},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 4, Content: "not json", Model: "m"},
	})
	if err == nil {
		t.Fatal("invalid summary content was stored")
	}
	if n := count(t, db, "SELECT count(*) FROM events WHERE session_id = ?"); n != before {
		t.Errorf("events %d -> %d after a failed commit", before, n)
	}
	if n := count(t, db, "SELECT count(*) FROM usage_ledger WHERE session_id = ?"); n != 0 {
		t.Errorf("ledger rows = %d after a failed commit", n)
	}
}

// TestInvisibleSession: a missing or purged session is not_found for Load,
// its summaries go with it, and a Commit is refused.
func TestInvisibleSession(t *testing.T) {
	t.Parallel()
	db, s := open(t)
	seed(t, db)
	if _, err := s.Load(t.Context(), "nope"); !errors.Is(err, ctxdb.ErrNotFound) {
		t.Errorf("missing session: %v", err)
	}
	if _, err := s.Commit(t.Context(), sid, &compaction.Commit{
		Call:    &compaction.Call{Provider: "p", Model: "m"},
		Summary: &compaction.Summary{FromSeq: 1, ToSeq: 4, Content: `{"goal":"g"}`, Model: "m"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReleaseLease(t.Context(), sid); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Purge(t.Context(), ctxdb.PurgeRequest{IDs: []string{sid}}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Load(t.Context(), sid); !errors.Is(err, ctxdb.ErrNotFound) {
		t.Errorf("purged session: %v", err)
	}
	if n := count(t, db, "SELECT count(*) FROM summaries WHERE session_id = ?"); n != 0 {
		t.Errorf("summaries left after purge = %d", n)
	}
	if _, err := s.Commit(t.Context(), sid, &compaction.Commit{Attempt: compaction.Attempt{Outcome: compaction.OutcomePruned}}); err == nil {
		t.Error("commit into a purged session succeeded")
	}
}

func TestCost(t *testing.T) {
	t.Parallel()
	for _, c := range []struct {
		usd    *float64
		micros int64
		source string
	}{
		{nil, -1, "unknown"},
		{new(-1.0), -1, "unknown"},
		{new(math.NaN()), -1, "unknown"},
		{new(2e6), -1, "unknown"},
		{new(0.000002), 2, "provider"},
		{new(1.5), 1500000, "provider"},
	} {
		m, src := cost(c.usd)
		got := int64(-1)
		if m != nil {
			got = *m
		}
		if got != c.micros || src != c.source {
			t.Errorf("cost(%v) = %d %s", c.usd, got, src)
		}
	}
}
