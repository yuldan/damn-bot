package budget

import (
	"errors"
	"fmt"
	"slices"
	"time"
)

// PriceEntry is one dated price of a model (GAP-COST-01): it is in effect on
// the half-open interval [From, Until). Registry and config dates are UTC; a
// zero Until is open-ended (no announced successor yet).
type PriceEntry struct {
	Price
	From, Until time.Time
}

// ErrPricing rejects an invalid price schedule.
var ErrPricing = errors.New("budget: invalid pricing")

// Prices is the validated price schedule of one model, ordered by From and
// free of overlaps. The zero value has no price: At returns nil, the call
// is unknown-cost, and a configured monetary cap fails closed (ADR-123
// butir 3). Prices is immutable after NewPrices and safe to share.
type Prices struct{ entries []PriceEntry }

// NewPrices validates entries and orders them by From. It rejects a zero
// From, an Until not after From, an empty or "none" Version, a negative
// price, and overlapping ranges: two prices for one instant would make the
// bill ambiguous, so the schedule is refused rather than resolved silently.
func NewPrices(entries ...PriceEntry) (Prices, error) {
	es := slices.Clone(entries)
	slices.SortStableFunc(es, func(a, b PriceEntry) int { return a.From.Compare(b.From) })
	for i := range es {
		e := &es[i]
		switch {
		case e.From.IsZero():
			return Prices{}, fmt.Errorf("%w: %q has no effective_from", ErrPricing, e.Version)
		case !e.Until.IsZero() && !e.Until.After(e.From):
			return Prices{}, fmt.Errorf("%w: %q effective_until %s is not after effective_from %s",
				ErrPricing, e.Version, e.Until.UTC().Format(time.RFC3339), e.From.UTC().Format(time.RFC3339))
		case e.Version == "" || e.Version == PricingNone:
			return Prices{}, fmt.Errorf("%w: entry from %s needs a pricing_version", ErrPricing, e.From.UTC().Format(time.RFC3339))
		case e.InputPerMTok < 0 || e.CachedInputPerMTok < 0 || e.OutputPerMTok < 0:
			return Prices{}, fmt.Errorf("%w: %q has a negative price", ErrPricing, e.Version)
		}
		// Sorted by From, so only the predecessor can overlap: an open-ended
		// predecessor or one ending after this start covers a shared instant.
		if i > 0 {
			if p := &es[i-1]; p.Until.IsZero() || p.Until.After(e.From) {
				return Prices{}, fmt.Errorf("%w: %q overlaps %q from %s", ErrPricing, e.Version, p.Version, e.From.UTC().Format(time.RFC3339))
			}
		}
	}
	return Prices{entries: es}, nil
}

// At returns the price in effect at t (the call time), or nil when no entry
// covers t: before the first entry, in a gap, or at/after the last Until
// (expired). The pointer refers to the schedule and must not be modified.
func (p Prices) At(t time.Time) *Price {
	for i := range p.entries {
		e := &p.entries[i]
		if !t.Before(e.From) && (e.Until.IsZero() || t.Before(e.Until)) {
			return &e.Price
		}
	}
	return nil
}
