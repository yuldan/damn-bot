package retry

import (
	"context"
	"errors"
	"testing"
	"time"
)

// memCircuit is one shared store; two Shared values model two processes.
type memCircuit struct {
	c  Circuit
	ok bool
}

func (m *memCircuit) Load(context.Context, string, string) (Circuit, bool, error) {
	return m.c, m.ok, nil
}

func (m *memCircuit) Save(_ context.Context, _, _ string, c Circuit) error {
	m.c, m.ok = c, true
	return nil
}

// TestSharedCircuitCrossProcess (COST-06): five retryable failures spread
// over two processes open the shared circuit, the window is enforced on a
// fake clock, one half-open probe is admitted, and success closes it.
func TestSharedCircuitCrossProcess(t *testing.T) {
	store := &memCircuit{}
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	a := &Shared{Store: store, Now: func() time.Time { return now }, Cache: time.Nanosecond}
	b := &Shared{Store: store, Now: func() time.Time { return now }, Cache: time.Nanosecond}
	for i := range BreakerFailures {
		g := a
		if i%2 == 1 {
			g = b
		}
		if err := g.Record(t.Context(), RateLimited); err != nil {
			t.Fatal(err)
		}
		now = now.Add(time.Second)
	}
	if err := a.Allow(t.Context()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("Allow after 5 failures = %v, want open", err)
	}
	now = now.Add(BreakerOpen)
	if err := a.Allow(t.Context()); err != nil {
		t.Fatalf("half-open probe = %v", err)
	}
	if err := b.Allow(t.Context()); !errors.Is(err, ErrCircuitOpen) {
		t.Fatalf("second concurrent probe = %v, want open", err)
	}
	if err := a.Record(t.Context(), None); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Nanosecond) // cross-process cache window
	if err := b.Allow(t.Context()); err != nil {
		t.Fatalf("closed circuit = %v", err)
	}
}

func TestSharedCircuitWindowAndNonRetryable(t *testing.T) {
	store := &memCircuit{}
	now := time.Date(2026, 10, 30, 10, 0, 0, 0, time.UTC)
	s := &Shared{Store: store, Now: func() time.Time { return now }, Cache: time.Nanosecond}
	for range BreakerFailures {
		if err := s.Record(t.Context(), Server); err != nil {
			t.Fatal(err)
		}
	}
	if len(store.c.Fails) != 0 || store.c.State != Open {
		t.Fatalf("state = %+v, want open with cleared ring", store.c)
	}
	now = now.Add(BreakerOpen)
	if err := s.Allow(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := s.Record(t.Context(), Auth); err != nil {
		t.Fatal(err)
	}
	if store.c.State != Closed {
		t.Fatalf("auth probe = %+v, want closed", store.c)
	}
}
