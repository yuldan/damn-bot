package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestBilledAttemptsRecorded (SEC6-9): a provider error that reports billed
// attempts leaves one unknown-cost ledger row per attempt, and a successful
// call after failed attempts keeps those rows plus its own usage row.
func TestBilledAttemptsRecorded(t *testing.T) {
	t.Parallel()
	t.Run("failure", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
			return Reply{}, &StreamError{Class: ClassProvider, Err: errors.New("stream_broken"), Billed: 2}
		}}
		f.run(t)
		if len(f.ledger.usage) != 2 {
			t.Fatalf("ledger rows = %d, want 2 unknown-cost attempts", len(f.ledger.usage))
		}
		for i, u := range f.ledger.usage {
			if u.Input != 0 || u.CachedInput != 0 || u.Output != 0 || u.CostUSD != nil || u.Price != nil {
				t.Errorf("row %d = %+v, want an empty unknown-cost row", i, u)
			}
		}
	})
	t.Run("success after failed attempts", func(t *testing.T) {
		t.Parallel()
		f := newFixture()
		f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
			return Reply{Content: "ok", FinishReason: FinishStop, Usage: event.Usage{Input: 3, Output: 1}, BilledFailed: 2}, nil
		}}
		f.run(t)
		if len(f.ledger.usage) != 3 {
			t.Fatalf("ledger rows = %d, want 2 unknown + 1 call", len(f.ledger.usage))
		}
		if last := f.ledger.usage[len(f.ledger.usage)-1]; last.Input != 3 || last.Output != 1 {
			t.Errorf("call row = %+v, want the reply usage", last)
		}
	})
}
