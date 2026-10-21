package ctxdb

import "testing"

// TestRecall: the recall query orders by bm25, honours k, and treats free
// text operators as separators instead of a MATCH syntax error.
func TestRecall(t *testing.T) {
	ctx := t.Context()
	e := newEnv(t)
	s := e.open(1, "run")
	if _, err := s.CreateSession(ctx, Session{ID: "S", Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	// Append real events (hash chain included) so events_fts is populated by
	// the schema trigger, not by a test-only insert.
	if _, err := s.Append(ctx, "S", nil,
		Event{Kind: "user_message", Role: "user", Content: "alpha beta gamma"},
		Event{Kind: "assistant_message", Role: "assistant", Content: "delta alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		text string
		k    int
		want int
	}{
		{"term", "alpha", 10, 2}, // one row per matching event, same session
		{"k limit", "alpha", 1, 1},
		{"operators", `alpha - ("beta`, 10, 1},
		{"no match", "omega", 10, 0},
		{"empty", "!!!", 10, 0},
		{"zero k defaults", "gamma", 0, 1},
	} {
		got, err := s.Recall(ctx, c.text, c.k)
		if err != nil {
			t.Fatalf("%s: %v", c.name, err)
		}
		if len(got) != c.want {
			t.Errorf("%s: %d hits, want %d", c.name, len(got), c.want)
		}
		for _, h := range got {
			if h.SessionID != "S" || h.EventID == 0 {
				t.Errorf("%s: hit %+v", c.name, h)
			}
		}
	}
}

// TestRecallPurgedStarvation: a purged session's 300 matches must not consume
// the candidate budget; the 10 visible matches still come back for k=10/50
// (JN3-12).
func TestRecallPurgedStarvation(t *testing.T) {
	ctx := t.Context()
	e := newEnv(t)
	s := e.open(1, "run")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	for _, id := range []string{"S", "B"} {
		_, err := s.CreateSession(ctx, Session{ID: id, Origin: "headless", Cwd: "/"})
		must(err)
	}
	hidden := make([]Event, 300)
	for i := range hidden {
		hidden[i] = Event{Kind: "user_message", Role: "user", Content: "purgeword hidden"}
	}
	_, err := s.Append(ctx, "S", nil, hidden...)
	must(err)
	visible := make([]Event, 10)
	for i := range visible {
		visible[i] = Event{Kind: "user_message", Role: "user", Content: "purgeword visible"}
	}
	_, err = s.Append(ctx, "B", nil, visible...)
	must(err)
	err = s.tx(ctx, modePurge, func(tx *Tx) error {
		_, xerr := tx.Exec(`INSERT INTO session_labels(session_id, key, value, created_at)
			VALUES ('S', 'purge_state', 'tx-test', 1)`)
		return xerr
	})
	must(err)
	for _, k := range []int{10, 50} {
		got, gerr := s.Recall(ctx, "purgeword", k)
		if gerr != nil {
			t.Fatal(gerr)
		}
		if len(got) != 10 {
			t.Fatalf("k=%d: %d hits, want 10", k, len(got))
		}
		for _, h := range got {
			if h.SessionID != "B" {
				t.Fatalf("k=%d: purged hit %+v", k, h)
			}
		}
	}
}

// TestRecallHonorsLargeK: k above one page is honoured (JN3-12).
func TestRecallHonorsLargeK(t *testing.T) {
	ctx := t.Context()
	e := newEnv(t)
	s := e.open(1, "run")
	if _, err := s.CreateSession(ctx, Session{ID: "B", Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	evs := make([]Event, 600)
	for i := range evs {
		evs[i] = Event{Kind: "user_message", Role: "user", Content: "bigk event"}
	}
	if _, err := s.Append(ctx, "B", nil, evs...); err != nil {
		t.Fatal(err)
	}
	got, err := s.Recall(ctx, "bigk", 500)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 500 {
		t.Fatalf("k=500: %d hits", len(got))
	}
}

// TestRecallBroadNewestFirst: above the broad threshold the fallback orders
// newest-first; at the threshold the matches are still ranked by bm25
// (JN3-13). The threshold is a parameter of recall, so both sides are
// reached with a few dozen events instead of broadMatches+1 (15.001 appends
// cost ~100 s under -race and pushed the package past go test's 10 min on
// macOS/Windows runners). The scale of the broad path itself is covered by
// TestRecallP95Broad100k in the perf build.
func TestRecallBroadNewestFirst(t *testing.T) {
	ctx := t.Context()
	e := newEnv(t)
	s := e.open(1, "run")
	if _, err := s.CreateSession(ctx, Session{ID: "B", Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	// The oldest events are dense (high bm25 relevance), the newest sparse,
	// so bm25 order and newest-first order disagree.
	const dense, sparse = 10, 11
	evs := make([]Event, dense+sparse)
	for i := range evs {
		c := "broadword broadword broadword"
		if i >= dense {
			c = "broadword lorem ipsum dolor sit amet consectetur adipiscing"
		}
		evs[i] = Event{Kind: "user_message", Role: "user", Content: c}
	}
	ids, err := s.Append(ctx, "B", nil, evs...)
	if err != nil {
		t.Fatal(err)
	}
	lastDense := ids[dense-1]
	hits := func(name string, got []RecallHit, err error) []RecallHit {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(got) != 5 {
			t.Fatalf("%s: hits = %d", name, len(got))
		}
		return got
	}
	// len(evs) matches > threshold: broad, the 5 newest in descending order.
	got, err := s.recall(ctx, "broadword", 5, len(evs)-1)
	for i, h := range hits("broad", got, err) {
		if h.EventID != ids[len(ids)-1-i] {
			t.Fatalf("broad: not the newest first: %+v", got)
		}
	}
	// Exactly at the threshold, and through Recall's production threshold:
	// bm25, so the dense (oldest) events rank first.
	for name, f := range map[string]func() ([]RecallHit, error){
		"at threshold": func() ([]RecallHit, error) { return s.recall(ctx, "broadword", 5, len(evs)) },
		"Recall":       func() ([]RecallHit, error) { return s.Recall(ctx, "broadword", 5) },
	} {
		got, err := f()
		for _, h := range hits(name, got, err) {
			if h.EventID > lastDense {
				t.Fatalf("%s: sparse event ranked above dense ones (not bm25): %+v", name, got)
			}
		}
	}
}
