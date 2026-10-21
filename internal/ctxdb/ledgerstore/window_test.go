package ledgerstore

import (
	"fmt"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestWindowTotals: the guard aggregate is a half-open [from,to) range,
// optionally per provider, and counts unknown-cost rows separately.
func TestWindowTotals(t *testing.T) {
	t.Parallel()
	const sid = "w"
	s, ids := open(t, sid, 1)
	rows := []Row{
		{SessionID: sid, EventID: ids[0], Purpose: "turn", Provider: "a", CostMicros: new(int64(100)), CostSource: "pricing_table", PricingVersion: "v", CreatedAt: 1000},
		{SessionID: sid, EventID: ids[0], Purpose: "turn", Provider: "a", CostSource: "unknown", PricingVersion: "none", CreatedAt: 1500},
		{SessionID: sid, EventID: ids[0], Purpose: "turn", Provider: "b", CostMicros: new(int64(200)), CostSource: "provider", PricingVersion: "v", CreatedAt: 2000},
		{SessionID: sid, EventID: ids[0], Purpose: "turn", Provider: "a", CostMicros: new(int64(300)), CostSource: "pricing_table", PricingVersion: "v", CreatedAt: 3000},
	}
	insert(t, s, rows...)
	cases := []struct {
		name                string
		from, to            int64
		provider            string
		wantCost, wantUnkwn int64
	}{
		{"first half, provider a", 1000, 2000, "a", 100, 1},
		{"first half, all", 1000, 2000, "", 100, 1},
		{"two windows, all", 1000, 3000, "", 300, 1},
		{"second half", 2000, 4000, "", 500, 0},
		{"provider filter", 0, 4000, "b", 200, 0},
		{"unknown provider", 0, 4000, "c", 0, 0},
		{"empty window", 0, 1000, "", 0, 0},
		{"to is exclusive", 3000, 3000, "", 0, 0},
	}
	for _, c := range cases {
		cost, unknown, err := WindowTotals(t.Context(), s, c.from, c.to, c.provider)
		if err != nil || cost != c.wantCost || unknown != c.wantUnkwn {
			t.Errorf("%s: WindowTotals(%d,%d,%q) = %d,%d,%v; want %d,%d", c.name, c.from, c.to, c.provider, cost, unknown, err, c.wantCost, c.wantUnkwn)
		}
	}
	// Source has the consumer-side method set (budget.TotalsSource).
	src := Source{Store: s}
	if cost, _, err := src.Totals(t.Context(), 0, 4000, ""); err != nil || cost != 600 {
		t.Errorf("Source.Totals = %d, %v; want 600", cost, err)
	}
}

// seedMillion fills the ledger with one million rows spread over 30 days.
// It runs only under -bench (the benchmark below), never in the test suite.
func seedMillion(tb testing.TB, s *ctxdb.Store) {
	tb.Helper()
	ctx := tb.Context()
	if _, err := s.CreateSession(ctx, ctxdb.Session{ID: "bench", Origin: "headless", Cwd: "/"}); err != nil {
		tb.Fatal(err)
	}
	ids, err := s.Append(ctx, "bench", nil, ctxdb.Event{Kind: "llm_call", Role: "system", Payload: "{}"})
	if err != nil {
		tb.Fatal(err)
	}
	const rows = 1_000_000
	err = s.Write(ctx, func(tx *ctxdb.Tx) error {
		for i := range rows {
			r := Row{
				SessionID: "bench", EventID: ids[0], Purpose: "turn", Provider: "p", Model: "m",
				InputTokens: 1000, EstInputTokens: 1100, OutputTokens: 100,
				CostMicros: new(int64(1234)), CostSource: "pricing_table", PricingVersion: "v",
				Attempts: 1, LatencyMS: 10, TTFTMS: -1,
				CreatedAt: int64(i%30) * 86_400_000,
			}
			if _, ierr := Insert(tx, &r); ierr != nil {
				return fmt.Errorf("row %d: %w", i, ierr)
			}
		}
		return nil
	})
	if err != nil {
		tb.Fatal(err)
	}
}

// BenchmarkWindowTotals measures the daily/provider guard aggregate on a
// million-row ledger. Seeding is excluded from the timer.
func BenchmarkWindowTotals(b *testing.B) {
	ctx := b.Context()
	s, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: b.TempDir(), Kind: "run"})
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			b.Error(cerr)
		}
	})
	seedMillion(b, s)
	b.ResetTimer()
	b.ReportAllocs()
	for b.Loop() {
		if _, _, err := WindowTotals(ctx, s, 0, 86_400_000, "p"); err != nil {
			b.Fatal(err)
		}
	}
}
