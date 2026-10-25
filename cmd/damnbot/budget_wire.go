package main

import (
	"errors"
	"fmt"
	"sync"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider"
)

// budgetNotes collects the warn_at crossings the guard reports inside
// provider.Do (OnBudget runs synchronously before the request) so the next
// reply carries them to the loop as budget_warning events (SPEC §18.4).
type budgetNotes struct {
	mu   sync.Mutex
	caps []string
}

func (b *budgetNotes) observe(d *budget.Decision) {
	if b == nil {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, c := range d.Warnings {
		b.caps = append(b.caps, string(c))
	}
}

func (b *budgetNotes) take() []string {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	out := b.caps
	b.caps = nil
	return out
}

// budgetStop maps a denied cap to its SPEC §3.5 stop ("" = not a budget
// stop). An unknown-cost denial (ADR-123 butir 3) stops on the cap it could
// not prove.
func budgetStop(c budget.CapKind) event.StopDetail {
	switch c {
	case budget.CapDaily:
		return event.DetailBudgetDaily
	case budget.CapMonthly:
		return event.DetailBudgetMonthly
	case budget.CapProvider:
		return event.DetailBudgetProvider
	}
	return ""
}

// streamError classifies a provider failure for the loop: a guard denial
// is ClassBudget with its typed stop, a context overflow ClassContextLength,
// anything else ClassProvider.
func streamError(err error) *loop.StreamError {
	if de, ok := errors.AsType[*budget.DeniedError](err); ok {
		if stop := budgetStop(de.Cap); stop != "" {
			return &loop.StreamError{Class: loop.ClassBudget, Detail: stop, Err: err}
		}
	}
	class := loop.ClassProvider
	if retry.ClassifyErr(err) == retry.ContextLength {
		class = loop.ClassContextLength
	}
	return &loop.StreamError{Class: class, Err: err}
}

// newBudgetGuard builds the run budget guard and the warning sink the adapter
// drains into budget_warning events (ADR-123 wiring). The guard runs inside
// provider.Do, so a refusal never reaches the provider.
func newBudgetGuard(cfg *config.Config, db *ctxdb.Store) (*budget.Guard, *budgetNotes, error) {
	guard, err := budget.NewGuard(budget.GuardConfig{
		DailyUSD: cfg.Budget.DailyUSD, MonthlyUSD: cfg.Budget.MonthlyUSD, Providers: cfg.Budget.Providers, WarnAt: cfg.Budget.WarnAt,
	}, ledgerstore.Source{Store: db}, nil)
	if err != nil {
		return nil, nil, fmt.Errorf("budget guard: %w", err)
	}
	return guard, &budgetNotes{}, nil
}

// budgetCounter adapts COST-09 to assemble.Counter (one formula, one owner).
type budgetCounter struct{}

func (budgetCounter) Count(s string) int { return int(budget.Count(s).Raw()) }

// providerAdapter drains the budget notes the guard collected inside
// provider.Do and hands them to the loop as budget_warning events.
type providerAdapter struct {
	client   *provider.OpenAI
	model    string
	notes    *budgetNotes
	provider string
}
