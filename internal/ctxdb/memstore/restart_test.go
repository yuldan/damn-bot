package memstore

import (
	"context"
	"reflect"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestRecallDeterministicAcrossRestart: the same query over the same
// persisted state returns the identical ordered result set on a rerun,
// including after the store is closed and reopened. This is the recall path
// the E4 eval scores (ADR-137); the eval runner/nightly score diff of
// GAP-INF-06 is still open, so this test is a proxy, not INF-06 closure.
func TestRecallDeterministicAcrossRestart(t *testing.T) {
	dir := t.TempDir()
	openRaw := func() *ctxdb.Store {
		s, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: dir, Kind: "run"})
		if err != nil {
			t.Fatalf("open: %v", err)
		}
		return s
	}
	first := openRaw()
	const scope = "user"
	for _, content := range []string{
		"my locker number is 4417",
		"locker code reminder for the gym",
		"the number of runs is three",
		"locker number 4417 is pinned for the season",
	} {
		put(t, first, mkrow(scope, content, "user"), approved())
	}
	// Two identical rows produce equal bm25 scores; the id tie-break must keep
	// the order stable across a reopen.
	put(t, first, mkrow(scope, "locker number 4417 duplicate", "user"), approved())
	put(t, first, mkrow(scope, "locker number 4417 duplicate", "user"), approved())
	q := &Query{Text: "what is my locker number", Scopes: []string{scope}, Now: 200, Limit: 10}
	before := search(t, first, q)
	if len(before) < 2 {
		t.Fatalf("hits = %d, want at least 2", len(before))
	}
	if again := search(t, first, q); !reflect.DeepEqual(before, again) {
		t.Fatal("rerun in the same process changed the result")
	}
	if err := first.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reopened := openRaw()
	t.Cleanup(func() {
		if cerr := reopened.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	after := search(t, reopened, q)
	if !reflect.DeepEqual(before, after) {
		t.Fatalf("recall changed after restart:\n before=%+v\n after=%+v", before, after)
	}
}
