package main

import (
	"errors"
	"sync"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/netx/retry"
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
