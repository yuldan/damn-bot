package ctxdb

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestHistoryForkAware(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "P", "")
	for i := 1; i <= 3; i++ {
		appendMsg(t, s, "P", "parent "+itoa(int64(i)))
	}
	child, err := s.Fork(ctx, "P", 2)
	must(t, err)
	if child == "" || child == "P" {
		t.Fatalf("fork id %q", child)
	}
	must(t, s.Append2(ctx, child, Event{Kind: "user_message", Content: "child one"}, Event{Kind: "assistant_message", Content: "child two"}))

	got, err := s.History(ctx, child, 0)
	must(t, err)
	want := []string{"parent 1", "parent 2", "child one", "child two"}
	if len(got) != len(want) {
		t.Fatalf("history len %d, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].Content != w {
			t.Errorf("history[%d] = %q, want %q", i, got[i].Content, w)
		}
	}
	// Seq stays the per-session events.seq (the space fork_seq uses).
	if got[0].SessionID != "P" || got[0].Seq != 1 || got[2].SessionID != child || got[2].Seq != 1 {
		t.Fatalf("row identity = %+v / %+v", got[0], got[2])
	}

	// Incremental tail: only the target session's own events after fromSeq.
	tail, err := s.History(ctx, child, 1)
	must(t, err)
	if len(tail) != 1 || tail[0].Content != "child two" || tail[0].Seq != 2 {
		t.Fatalf("incremental history = %+v", tail)
	}

	// Parent history is unchanged by the fork.
	parent, err := s.History(ctx, "P", 0)
	must(t, err)
	if len(parent) != 3 {
		t.Fatalf("parent history len %d", len(parent))
	}
}

func TestHistoryBoundedAndVisible(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	for range 20 {
		appendMsg(t, s, "S", "msg")
	}
	got, err := s.History(ctx, "S", 0, WithHistoryLimit(5))
	must(t, err)
	if len(got) != 5 || got[0].Seq != 1 || got[4].Seq != 5 {
		t.Fatalf("bounded history = %d rows, first %d last %d", len(got), got[0].Seq, got[len(got)-1].Seq)
	}
	if _, err := s.History(ctx, "missing", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing session: %v", err)
	}
	if _, err := s.History(ctx, "S", -1); !errors.Is(err, ErrInvalidSeq) {
		t.Fatalf("negative fromSeq: %v", err)
	}

	// A labelled (purging) session is not visible.
	must(t, s.tx(ctx, modePurge, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('S','purge_state','tx',0)`)
		return err
	}))
	if _, err := s.History(ctx, "S", 0); !errors.Is(err, ErrNotFound) {
		t.Fatalf("purging session: %v", err)
	}
}

func TestHistorySummaries(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "P", "")
	first := appendMsg(t, s, "P", "covered one")
	appendMsg(t, s, "P", "covered two")
	appendMsg(t, s, "P", "kept")
	child, err := s.Fork(ctx, "P", 3)
	must(t, err)
	appendMsg(t, s, child, "child")
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, werr := tx.Exec(`INSERT INTO summaries(session_id, from_seq, to_seq, content, model, input_tokens, output_tokens, event_id, created_at)
			VALUES ('P',1,2,'{"goal":"g","facts":["f"]}','m',10,5,?,0)`, first)
		return werr
	}))

	got, err := s.History(ctx, child, 0)
	must(t, err)
	if len(got) != 3 {
		t.Fatalf("history len %d: %+v", len(got), got)
	}
	if got[0].Kind != "summary_ref" || !strings.Contains(got[0].Content, `"goal"`) {
		t.Fatalf("first row = %+v, want summary_ref", got[0])
	}
	if got[1].Content != "kept" || got[2].Content != "child" {
		t.Fatalf("rows after summary = %q / %q", got[1].Content, got[2].Content)
	}
	if !strings.Contains(got[0].Payload, `"to_seq":2`) {
		t.Fatalf("summary payload = %s", got[0].Payload)
	}

	raw, err := s.History(ctx, child, 0, WithHistoryRaw())
	must(t, err)
	if len(raw) != 4 || raw[0].Content != "covered one" {
		t.Fatalf("raw history = %+v", raw)
	}
}

func TestHistoryContextExclude(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	must(t, s.Append2(ctx, "S",
		Event{Kind: "user_message", TurnID: "T1", Content: "poison"},
		Event{Kind: "assistant_message", TurnID: "T1", Content: "bad answer"},
		Event{Kind: "user_message", TurnID: "T2", Content: "good"},
		Event{Kind: "context_exclude", Payload: `{"turn_id":"T1","reason":"provider_error"}`},
	))
	got, err := s.History(ctx, "S", 0)
	must(t, err)
	if len(got) != 2 || got[0].Content != "good" || got[1].Kind != "context_exclude" {
		t.Fatalf("excluded history = %+v", got)
	}
	all, err := s.History(ctx, "S", 0, WithHistoryExcluded())
	must(t, err)
	if len(all) != 4 {
		t.Fatalf("include-excluded history = %d rows", len(all))
	}
}

// Append2 appends events in one transaction (test shorthand).
func (s *Store) Append2(ctx context.Context, sid string, evs ...Event) error {
	_, err := s.Append(ctx, sid, nil, evs...)
	return err
}

func searchCorpus(t *testing.T) (*Store, context.Context) {
	t.Helper()
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "A", "")
	session(t, s, "B", "")
	for range 5 {
		appendMsg(t, s, "A", strings.Repeat("filler ", 60)+"needle in the haystack")
		appendMsg(t, s, "B", "needle elsewhere")
	}
	return s, ctx
}

func TestSearchTypedHits(t *testing.T) {
	s, ctx := searchCorpus(t)
	hits, err := s.Search(ctx, Query{Text: "needle", Limit: 20})
	must(t, err)
	if len(hits) != 10 {
		t.Fatalf("hits = %d, want 10", len(hits))
	}
	if hits[0].Snippet == "" || !strings.Contains(hits[0].Snippet, "needle") {
		t.Fatalf("snippet = %q", hits[0].Snippet)
	}
	if hits[0].EventID == 0 || hits[0].SessionID == "" || hits[0].Seq == 0 || hits[0].Kind == "" {
		t.Fatalf("hit identity = %+v", hits[0])
	}
	for i := 1; i < len(hits); i++ {
		if hits[i].Score < hits[i-1].Score {
			t.Fatalf("hits not ordered by bm25: %v then %v", hits[i-1].Score, hits[i].Score)
		}
	}
	top, err := s.Search(ctx, Query{Text: "needle", Limit: 2})
	must(t, err)
	if len(top) != 2 {
		t.Fatalf("limit k = %d hits", len(top))
	}
}

func TestSearchSessionFilter(t *testing.T) {
	s, ctx := searchCorpus(t)
	one, err := s.Search(ctx, Query{Text: "needle", SessionID: "B"})
	must(t, err)
	if len(one) != 5 || one[0].SessionID != "B" {
		t.Fatalf("session filter = %+v", one)
	}
	none, err := s.Search(ctx, Query{Text: "absent-term"})
	must(t, err)
	if len(none) != 0 {
		t.Fatalf("absent term returned %d hits", len(none))
	}
	if empty, serr := s.Search(ctx, Query{}); serr != nil || empty != nil {
		t.Fatalf("empty query = %v / %v", empty, serr)
	}
}

func TestSearchPurgingHidden(t *testing.T) {
	s, ctx := searchCorpus(t)
	must(t, s.tx(ctx, modePurge, func(tx *Tx) error {
		_, werr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at) VALUES ('B','purge_state','tx',0)`)
		return werr
	}))
	hits, err := s.Search(ctx, Query{Text: "needle", Limit: 20})
	must(t, err)
	if len(hits) != 5 {
		t.Fatalf("purging session leaked: %d hits", len(hits))
	}
	for _, h := range hits {
		if h.SessionID == "B" {
			t.Fatalf("purging session in hits: %+v", h)
		}
	}
}

func TestSearchSummariesAndCJK(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "S", "")
	first := appendMsg(t, s, "S", "天气预报很准")
	appendMsg(t, s, "S", "股票行情今天上涨")
	appendMsg(t, s, "S", "plain english text")
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, err := tx.Exec(`INSERT INTO summaries(session_id, from_seq, to_seq, content, model, input_tokens, output_tokens, event_id, created_at)
			VALUES ('S',1,1,'{"goal":"天气预报"}','m',1,1,?,0)`, first)
		return err
	}))
	hits, err := s.Search(ctx, Query{Text: "天气预报", Limit: 10})
	must(t, err)
	if len(hits) == 0 {
		t.Fatal("CJK query returned no hits")
	}
	var sawEvent, sawSummary bool
	for _, h := range hits {
		sawEvent = sawEvent || h.Kind == "user_message"
		sawSummary = sawSummary || h.Kind == "summary_ref"
	}
	if !sawEvent || !sawSummary {
		t.Fatalf("CJK hits event=%v summary=%v: %+v", sawEvent, sawSummary, hits)
	}
	hits, err = s.Search(ctx, Query{Text: "股票行情", Limit: 5})
	must(t, err)
	if len(hits) != 1 || !strings.Contains(hits[0].Snippet, "股票") {
		t.Fatalf("CJK bigram hit = %+v", hits)
	}
}

func TestForkAndRewind(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	session(t, s, "P", "")
	for i := 1; i <= 5; i++ {
		appendMsg(t, s, "P", "e"+itoa(int64(i)))
	}
	child, err := s.Fork(ctx, "P", 5)
	must(t, err)
	if aerr := s.Append2(ctx, child, Event{Kind: "user_message", Content: "branch"}); aerr != nil {
		t.Fatalf("fork lease not acquired: %v", aerr)
	}
	if n := count(t, s, "SELECT count(*) FROM events WHERE session_id = ?", child); n != 1 {
		t.Fatalf("child events = %d, want 1 (parent events are not copied)", n)
	}
	var parent string
	var fork int64
	must(t, s.get(ctx, "SELECT parent_id, fork_seq FROM sessions WHERE id = ?", []any{child}, &parent, &fork))
	if parent != "P" || fork != 5 {
		t.Fatalf("child lineage = %q@%d", parent, fork)
	}

	// Rewind: another child at an earlier seq; the old branch stays.
	rew, err := s.Fork(ctx, "P", 2)
	must(t, err)
	hist, err := s.History(ctx, rew, 0)
	must(t, err)
	if len(hist) != 2 || hist[1].Content != "e2" {
		t.Fatalf("rewind history = %+v", hist)
	}
	if n := count(t, s, "SELECT count(*) FROM sessions WHERE parent_id = 'P'"); n != 2 {
		t.Fatalf("forks of P = %d, want 2", n)
	}

	if _, ferr := s.Fork(ctx, "P", 6); !errors.Is(ferr, ErrInvalidSeq) {
		t.Fatalf("fork past head: %v", ferr)
	}
	if _, ferr := s.Fork(ctx, "P", -1); !errors.Is(ferr, ErrInvalidSeq) {
		t.Fatalf("fork negative: %v", ferr)
	}
	if _, ferr := s.Fork(ctx, "missing", 0); !errors.Is(ferr, ErrNotFound) {
		t.Fatalf("fork missing: %v", ferr)
	}

	must(t, s.ReleaseLease(ctx, rew))
	must(t, s.ReleaseLease(ctx, child))
	must(t, s.ReleaseLease(ctx, "P"))
	_, err = s.Purge(ctx, PurgeRequest{IDs: []string{"P"}, WithForks: true})
	must(t, err)
	if _, err := s.Fork(ctx, "P", 1); !errors.Is(err, ErrNotFound) {
		t.Fatalf("fork purged: %v", err)
	}
}

func TestForkContextTierCopied(t *testing.T) {
	e := newEnv(t)
	s := e.open(1, "tui")
	ctx := context.Background()
	must(t, s.Write(ctx, func(tx *Tx) error {
		_, err := tx.CreateSession(Session{ID: "S", Origin: "tui", Cwd: "/w", Model: "m", ConfigSHA: "c", ContextTier: "tier-1"})
		return err
	}))
	child, err := s.Fork(ctx, "S", 0)
	must(t, err)
	var tier, cfg string
	must(t, s.get(ctx, "SELECT context_tier, config_sha FROM sessions WHERE id = ?", []any{child}, &tier, &cfg))
	if tier != "tier-1" || cfg != "c" {
		t.Fatalf("fork metadata = %q/%q", tier, cfg)
	}
}
