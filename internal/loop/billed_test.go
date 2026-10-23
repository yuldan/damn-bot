package loop

import (
	"context"
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestBilledAttemptsUnknown (SEC6-9): a provider error that reports billed
// attempts on an unpriced route leaves one NULL/unknown ledger row each.
func TestBilledAttemptsUnknown(t *testing.T) {
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
}

// TestBilledAttemptsPriced (SEC6-11): a priced route records the
// conservative projection supplied by the adapter, not NULL.
func TestBilledAttemptsPriced(t *testing.T) {
	t.Parallel()
	f := newFixture()
	price := &event.Price{InputPerMTok: 150_000, OutputPerMTok: 600_000, Version: "v"}
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{}, &StreamError{
			Class: ClassProvider, Err: errors.New("stream_broken"), Billed: 2,
			Usage: &event.Usage{Input: 10, Output: 100, Price: price},
		}
	}}
	f.run(t)
	if len(f.ledger.usage) != 2 {
		t.Fatalf("ledger rows = %d, want 2", len(f.ledger.usage))
	}
	for i, u := range f.ledger.usage {
		if u.Input != 10 || u.Output != 100 || u.Price != price {
			t.Errorf("row %d = %+v, want the priced projection", i, u)
		}
	}
}

// TestBilledAttemptsCancelled (SEC6-10): a cancel that races the end of the
// stream still records the billed attempt and the paid call.
func TestBilledAttemptsCancelled(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		f.cancel() // the caller cancels as the stream finishes
		return Reply{
			Content: "ok", FinishReason: FinishStop, Usage: event.Usage{Input: 3, Output: 1},
			BilledFailed: 1, BilledUsage: &event.Usage{Input: 5, Output: 2},
		}, nil
	}}
	f.run(t)
	if len(f.ledger.usage) != 2 {
		t.Fatalf("ledger rows = %d, want the billed attempt and the paid call despite cancel", len(f.ledger.usage))
	}
	if last := f.ledger.usage[len(f.ledger.usage)-1]; last.Input != 3 || last.Output != 1 {
		t.Errorf("call row = %+v, want the reply usage", last)
	}
}

// TestBilledAttemptsSuccessAfterFailure (SEC6-9): failed billed attempts are
// kept as rows even when the call ultimately succeeds.
func TestBilledAttemptsSuccessAfterFailure(t *testing.T) {
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
}
