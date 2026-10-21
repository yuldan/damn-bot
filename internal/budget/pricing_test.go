package budget

import (
	"errors"
	"testing"
	"time"
)

func day(y int, m time.Month, d int) time.Time { return time.Date(y, m, d, 0, 0, 0, 0, time.UTC) }

// sonnet5 mirrors the GAP-COST-01 evidence: $2/$10 through 2026-08-31,
// then $3/$15.
func sonnet5(t *testing.T) Prices {
	t.Helper()
	p, err := NewPrices(
		// Deliberately out of order: NewPrices sorts by From.
		PriceEntry{Price: Price{InputPerMTok: 3_000_000, OutputPerMTok: 15_000_000, Version: "2026-09-01"}, From: day(2026, 9, 1)},
		PriceEntry{Price: Price{InputPerMTok: 2_000_000, OutputPerMTok: 10_000_000, Version: "2026-05-01"}, From: day(2026, 5, 1), Until: day(2026, 9, 1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// TestPricingEffectiveDate (GAP-COST-01): the price is selected by the
// request timestamp on [effective_from, effective_until); the ledger row
// records the pricing_version of the selected entry.
func TestPricingEffectiveDate(t *testing.T) {
	t.Parallel()
	p := sonnet5(t)
	jakarta := time.FixedZone("WIB", 7*3600)
	cases := []struct {
		name    string
		at      time.Time
		version string // "" = no price
	}{
		{"SPEC example 2026-10-17", day(2026, 10, 17), "2026-09-01"},
		{"before first entry", day(2026, 5, 1).Add(-time.Nanosecond), ""},
		{"first instant", day(2026, 5, 1), "2026-05-01"},
		{"last instant of old price", day(2026, 9, 1).Add(-time.Nanosecond), "2026-05-01"},
		{"until is exclusive", day(2026, 9, 1), "2026-09-01"},
		// 2026-09-01 06:59 WIB is still 2026-08-31 UTC.
		{"dates are UTC instants", time.Date(2026, 9, 1, 6, 59, 0, 0, jakarta), "2026-05-01"},
		{"open-ended successor", day(2099, 1, 1), "2026-09-01"},
	}
	for _, c := range cases {
		got := p.At(c.at)
		switch {
		case c.version == "" && got != nil:
			t.Errorf("%s: price %+v, want none", c.name, *got)
		case c.version != "" && (got == nil || got.Version != c.version):
			t.Errorf("%s: price %+v, want %s", c.name, got, c.version)
		}
	}
	e := Entry{}
	if err := e.Account(Usage{Input: 1_000_000, Output: 1_000_000}, p.At(day(2026, 10, 17)), 1); err != nil ||
		*e.CostMicros != 18_000_000 || e.PricingVersion != "2026-09-01" || e.CostSource != string(CostPricingTable) {
		t.Errorf("ledger entry %+v, %v; want $3/$15 under 2026-09-01", e, err)
	}
}

// TestPricingExpiredFailsClosed: after the last effective_until, and in a
// gap between entries, there is no price, the cost is unknown, and the
// ledger records pricing_version 'none' (ADR-123 butir 3).
func TestPricingExpiredFailsClosed(t *testing.T) {
	t.Parallel()
	p, err := NewPrices(
		PriceEntry{Price: Price{InputPerMTok: 1, Version: "a"}, From: day(2026, 1, 1), Until: day(2026, 2, 1)},
		PriceEntry{Price: Price{InputPerMTok: 2, Version: "b"}, From: day(2026, 3, 1), Until: day(2026, 4, 1)},
	)
	if err != nil {
		t.Fatal(err)
	}
	for _, at := range []time.Time{day(2026, 2, 1), day(2026, 2, 15), day(2026, 4, 1), day(2027, 1, 1)} {
		if got := p.At(at); got != nil {
			t.Errorf("At(%s) = %+v, want none", at.Format(time.DateOnly), *got)
		}
		e := Entry{}
		if err := e.Account(Usage{Input: 10}, p.At(at), 10); err != nil || e.CostMicros != nil || e.PricingVersion != PricingNone {
			t.Errorf("At(%s): entry %+v, %v; want unknown cost", at.Format(time.DateOnly), e, err)
		}
	}
	if got := (Prices{}).At(day(2026, 1, 1)); got != nil {
		t.Errorf("zero Prices has price %+v", *got)
	}
}

// TestPricingRejectsInvalid: overlapping ranges and malformed entries are
// refused when the schedule is built, never resolved at call time.
func TestPricingRejectsInvalid(t *testing.T) {
	t.Parallel()
	ok := Price{InputPerMTok: 1, OutputPerMTok: 1, Version: "v"}
	named := func(v string) Price { p := ok; p.Version = v; return p }
	cases := []struct {
		name    string
		entries []PriceEntry
	}{
		{"overlap by one day", []PriceEntry{
			{Price: named("a"), From: day(2026, 1, 1), Until: day(2026, 3, 2)},
			{Price: named("b"), From: day(2026, 3, 1)},
		}},
		{"open-ended then later entry", []PriceEntry{
			{Price: named("a"), From: day(2026, 1, 1)},
			{Price: named("b"), From: day(2026, 6, 1), Until: day(2026, 7, 1)},
		}},
		{"same start", []PriceEntry{
			{Price: named("a"), From: day(2026, 1, 1), Until: day(2026, 2, 1)},
			{Price: named("b"), From: day(2026, 1, 1), Until: day(2026, 3, 1)},
		}},
		{"nested", []PriceEntry{
			{Price: named("a"), From: day(2026, 1, 1), Until: day(2026, 12, 1)},
			{Price: named("b"), From: day(2026, 3, 1), Until: day(2026, 4, 1)},
		}},
		{"no effective_from", []PriceEntry{{Price: ok}}},
		{"until equals from", []PriceEntry{{Price: ok, From: day(2026, 1, 1), Until: day(2026, 1, 1)}}},
		{"until before from", []PriceEntry{{Price: ok, From: day(2026, 2, 1), Until: day(2026, 1, 1)}}},
		{"empty version", []PriceEntry{{Price: named(""), From: day(2026, 1, 1)}}},
		{"version none", []PriceEntry{{Price: named(PricingNone), From: day(2026, 1, 1)}}},
		{"negative price", []PriceEntry{{Price: Price{OutputPerMTok: -1, Version: "v"}, From: day(2026, 1, 1)}}},
	}
	for _, c := range cases {
		if _, err := NewPrices(c.entries...); !errors.Is(err, ErrPricing) {
			t.Errorf("%s: err = %v, want ErrPricing", c.name, err)
		}
	}
	// Adjacent ranges (until == next from) are not an overlap.
	if _, err := NewPrices(
		PriceEntry{Price: named("a"), From: day(2026, 1, 1), Until: day(2026, 2, 1)},
		PriceEntry{Price: named("b"), From: day(2026, 2, 1)},
	); err != nil {
		t.Errorf("adjacent ranges rejected: %v", err)
	}
}

func BenchmarkPricesAt(b *testing.B) {
	p, err := NewPrices(
		PriceEntry{Price: Price{InputPerMTok: 1, Version: "a"}, From: day(2026, 1, 1), Until: day(2026, 5, 1)},
		PriceEntry{Price: Price{InputPerMTok: 2, Version: "b"}, From: day(2026, 5, 1), Until: day(2026, 9, 1)},
		PriceEntry{Price: Price{InputPerMTok: 3, Version: "c"}, From: day(2026, 9, 1)},
	)
	if err != nil {
		b.Fatal(err)
	}
	at := day(2026, 10, 17)
	b.ReportAllocs()
	for b.Loop() {
		if p.At(at) == nil {
			b.Fatal("no price")
		}
	}
}
