package ledgerstore

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"go.uber.org/goleak"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestMain(m *testing.M) {
	goleak.VerifyTestMain(m)
}

// open returns a store with session sid and n llm_call events.
func open(t *testing.T, sid string, n int) (*ctxdb.Store, []int64) {
	t.Helper()
	ctx := t.Context()
	s, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := s.Close(); cerr != nil {
			t.Error(cerr)
		}
	})
	if _, err = s.CreateSession(ctx, ctxdb.Session{ID: sid, Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	evs := make([]ctxdb.Event, n)
	for i := range evs {
		evs[i] = ctxdb.Event{Kind: "llm_call", Role: "system", Payload: "{}"}
	}
	ids, err := s.Append(ctx, sid, nil, evs...)
	if err != nil {
		t.Fatal(err)
	}
	return s, ids
}

func insert(t *testing.T, s *ctxdb.Store, rows ...Row) {
	t.Helper()
	err := s.Write(t.Context(), func(tx *ctxdb.Tx) error {
		for i := range rows {
			if _, err := Insert(tx, &rows[i]); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestLedgerRoundTrip: rows are stored and give back calibration samples
// and totals.
func TestLedgerRoundTrip(t *testing.T) {
	t.Parallel()
	const sid = "s1"
	s, ids := open(t, sid, 4)
	row := func(i int, in, cached, out, est int64, cost *int64) Row {
		src := "pricing_table"
		if cost == nil {
			src = "unknown"
		}
		return Row{
			SessionID: sid, TurnID: "t1", EventID: ids[i], Purpose: "turn", Provider: "p", Model: "m",
			InputTokens: in, EstInputTokens: est, CachedInputTokens: cached, OutputTokens: out, CostMicros: cost,
			CostSource: src, PricingVersion: "v1", Attempts: 1, LatencyMS: 900, TTFTMS: -1, CreatedAt: int64(i),
		}
	}
	compaction := row(3, 9000, 0, 300, 9500, nil)
	compaction.Purpose, compaction.TurnID, compaction.TTFTMS = "compaction", "", 40
	insert(t, s, row(0, 1000, 0, 100, 1200, new(int64(1200))), row(1, 1500, 1000, 50, 1800, new(int64(1600))),
		row(2, 1800, 0, 10, 2400, new(int64(1820))), compaction)

	samples, last, err := Calibration(t.Context(), s, sid, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(samples) != 2 || samples[0] != (Sample{1500, 1800}) || samples[1] != (Sample{1800, 2400}) || last != 1800 {
		t.Errorf("Calibration = %v last %d", samples, last)
	}
	checkTotals(t, s, sid)
}

func checkTotals(t *testing.T, s *ctxdb.Store, sid string) {
	t.Helper()
	tot, err := SessionTotals(t.Context(), s, sid)
	want := Totals{Calls: 4, Input: 13300, CachedInput: 1000, Output: 460, CostMicros: (1000 + 100*2) + (500 + 1000 + 50*2) + (1800 + 10*2), UnknownCost: 1}
	if err != nil || tot != want {
		t.Errorf("SessionTotals = %+v, %v; want %+v", tot, err, want)
	}
	n, err := s.Count(context.WithoutCancel(t.Context()), `SELECT COUNT(*) FROM usage_ledger WHERE ttft_ms IS NULL AND turn_id = 't1'`)
	if err != nil || n != 3 {
		t.Errorf("NULL ttft rows = %d, %v", n, err)
	}
}

// TestLedgerAppendOnly: the ledger rejects rows bound to another
// session's event and never allows UPDATE (P9).
func TestLedgerAppendOnly(t *testing.T) {
	t.Parallel()
	s, ids := open(t, "a", 1)
	if _, err := s.CreateSession(t.Context(), ctxdb.Session{ID: "b", Origin: "headless", Cwd: "/"}); err != nil {
		t.Fatal(err)
	}
	err := s.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, err := Insert(tx, &Row{SessionID: "b", EventID: ids[0], Purpose: "turn", CostSource: "unknown", PricingVersion: "none", TTFTMS: -1})
		return err
	})
	if err == nil {
		t.Error("cross-session ledger row accepted")
	}
	insert(t, s, Row{SessionID: "a", EventID: ids[0], Purpose: "turn", CostSource: "unknown", PricingVersion: "none", TTFTMS: -1})
	// LED-1: both statements are valid SQL that succeed without the
	// usage_no_update/usage_no_delete triggers; the error is returned as is
	// and the row must be unchanged afterwards.
	for _, stmt := range []string{`UPDATE usage_ledger SET input_tokens = 7`, `DELETE FROM usage_ledger`} {
		err = s.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, uerr := tx.Exec(stmt); uerr != nil {
				return fmt.Errorf("%s: %w", stmt, uerr)
			}
			return nil
		})
		if !errors.Is(err, ctxdb.ErrInternal) {
			t.Errorf("%s: err = %v, want the append-only trigger (ErrInternal)", stmt, err)
		}
		if tot, terr := SessionTotals(t.Context(), s, "a"); terr != nil || tot.Calls != 1 || tot.Input != 0 {
			t.Errorf("%s changed the ledger: %+v %v", stmt, tot, terr)
		}
	}
	if samples, last, err := Calibration(t.Context(), s, "a", 50); err != nil || len(samples) != 0 || last != 0 {
		t.Errorf("no-estimate rows: %v %d %v", samples, last, err)
	}
}
