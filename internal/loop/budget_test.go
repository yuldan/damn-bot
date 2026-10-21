package loop

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestBudgetDenialStops: a ClassBudget failure ends the turn on its typed
// budget_* stop (SPEC §3.5, coarse max_tokens) with budget_exhausted and no
// retry; without a Detail it stays a provider error.
func TestBudgetDenialStops(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(context.Context, *Request, func(Delta)) (Reply, error) {
		return Reply{}, &StreamError{Class: ClassBudget, Detail: event.DetailBudgetDaily, Err: errors.New("budget: daily cap reached")}
	}}
	got := terminal(t, f.run(t))
	if got.Stop != event.StopMaxTokens || got.Detail != event.DetailBudgetDaily || !strings.Contains(got.Err, "daily cap reached") {
		t.Fatalf("stop = %s/%s %q", got.Stop, got.Detail, got.Err)
	}
	if ev, ok := f.store.find(event.BudgetExhausted); !ok || !strings.Contains(string(ev.Payload), `"code":"budget_daily"`) {
		t.Fatalf("budget_exhausted = %+v %v", ev, ok)
	}
	f.provider.mu.Lock()
	n := f.provider.calls
	f.provider.mu.Unlock()
	if n != 1 {
		t.Fatalf("provider calls = %d, want 1 (no retry after a denial)", n)
	}
	f = newFixture()
	f.provider.scripts = []streamFunc{errorScript(ClassBudget, errors.New("untyped"))}
	if got := terminal(t, f.run(t)); got.Stop != event.StopError || got.Detail != event.DetailProviderError {
		t.Fatalf("untyped budget class = %s/%s", got.Stop, got.Detail)
	}
}

// TestBudgetWarningsRecorded: warn_at crossings on a reply become
// budget_warning events before the answer.
func TestBudgetWarningsRecorded(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.provider.scripts = []streamFunc{func(_ context.Context, _ *Request, emit func(Delta)) (Reply, error) {
		emit(Delta{Text: "ok"})
		return Reply{Content: "ok", FinishReason: FinishStop, Model: "fake/m", BudgetWarnings: []string{"daily", "provider"}}, nil
	}}
	if got := terminal(t, f.run(t)); got.Detail != event.DetailCompleted {
		t.Fatalf("stop = %s/%s", got.Stop, got.Detail)
	}
	var warned []string
	f.store.mu.Lock()
	for _, e := range f.store.events {
		if e.Kind == event.BudgetWarning {
			warned = append(warned, string(e.Payload))
		}
	}
	f.store.mu.Unlock()
	if len(warned) != 2 || !strings.Contains(warned[0], `"kind":"daily"`) || !strings.Contains(warned[1], `"kind":"provider"`) {
		t.Fatalf("budget_warning events = %q", warned)
	}
}
