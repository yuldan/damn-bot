package budget

import (
	"context"
	"errors"
	"math"
	"testing"
	"time"
)

// totalsFunc adapts a function to TotalsSource in tests.
type totalsFunc func(ctx context.Context, fromMS, toMS int64, provider string) (int64, int64, error)

func (f totalsFunc) Totals(ctx context.Context, fromMS, toMS int64, provider string) (int64, int64, error) {
	return f(ctx, fromMS, toMS, provider)
}

func jakarta() *time.Location { return time.FixedZone("UTC+7", 7*60*60) }

func fixedNow(t time.Time) func() time.Time {
	return func() time.Time { return t }
}

func mustGuard(t *testing.T, cfg GuardConfig, src TotalsSource, now func() time.Time) *Guard {
	t.Helper()
	g, err := NewGuard(cfg, src, now)
	if err != nil {
		t.Fatal(err)
	}
	return g
}

// TestBudgetGuardDaily: 100% (with the pending estimate) is a hard stop,
// the 80% warning is emitted once per period, and the reset is local
// midnight of the injected clock.
func TestBudgetGuardDaily(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 15, 30, 0, 0, jakarta())
	used := int64(9_000_000)
	g := mustGuard(t, GuardConfig{DailyUSD: 10},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
			return used, 0, nil
		}), fixedNow(now))

	d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: 1_000_000})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != ReasonDaily || d.Cap != CapDaily ||
		d.LimitMicros != 10_000_000 || d.UsedMicros != used || d.EstimateMicros != 1_000_000 {
		t.Fatalf("denied decision = %+v", d)
	}
	wantReset := time.Date(2026, 10, 22, 0, 0, 0, 0, jakarta()).UnixMilli()
	if d.ResetAt != wantReset {
		t.Errorf("reset = %d, want %d", d.ResetAt, wantReset)
	}
	if len(d.Warnings) != 1 || d.Warnings[0] != CapDaily {
		t.Errorf("warnings = %v, want [daily] at 100%%", d.Warnings)
	}
	if err := d.Err(); !errors.As(err, new(*DeniedError)) {
		t.Errorf("Err = %v, want *DeniedError", err)
	}
}

func TestBudgetGuardWarningOncePerPeriod(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 9, 0, 0, 0, jakarta())
	used := int64(7_000_000) // 70%; +10% pending = exactly 80%
	g := mustGuard(t, GuardConfig{DailyUSD: 10},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
			return used, 0, nil
		}), fixedNow(now))

	for i, want := range []int{1, 0} {
		d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: 1_000_000})
		if err != nil || !d.Allowed {
			t.Fatalf("call %d: %+v %v", i, d, err)
		}
		if len(d.Warnings) != want {
			t.Errorf("call %d: warnings = %v, want %d", i, d.Warnings, want)
		}
	}
	// A new local day is a new period: the warning fires again.
	g.now = fixedNow(now.AddDate(0, 0, 1))
	d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: 1_000_000})
	if err != nil || !d.Allowed || len(d.Warnings) != 1 {
		t.Errorf("next day: %+v %v", d, err)
	}
}

func TestBudgetGuardMonthly(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 15, 30, 0, 0, jakarta())
	g := mustGuard(t, GuardConfig{MonthlyUSD: 100},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
			return 99_500_000, 0, nil
		}), fixedNow(now))
	d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: 500_000})
	if err != nil {
		t.Fatal(err)
	}
	if d.Allowed || d.Reason != ReasonMonthly || d.Cap != CapMonthly {
		t.Fatalf("decision = %+v", d)
	}
	wantReset := time.Date(2026, 11, 1, 0, 0, 0, 0, jakarta()).UnixMilli()
	if d.ResetAt != wantReset {
		t.Errorf("reset = %d, want %d", d.ResetAt, wantReset)
	}
}

func TestBudgetGuardProvider(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 15, 30, 0, 0, jakarta())
	var queries int
	var gotProvider string
	g := mustGuard(t, GuardConfig{Providers: map[string]float64{"openrouter": 20}},
		totalsFunc(func(_ context.Context, _, _ int64, provider string) (int64, int64, error) {
			queries++
			gotProvider = provider
			return 19_500_000, 0, nil
		}), fixedNow(now))

	d, err := g.Check(t.Context(), Request{Provider: "openrouter", EstimateMicros: 500_000})
	if err != nil || d.Allowed || d.Reason != ReasonProvider || d.Cap != CapProvider {
		t.Fatalf("openrouter: %+v %v", d, err)
	}
	if queries != 1 || gotProvider != "openrouter" {
		t.Errorf("queries = %d provider %q", queries, gotProvider)
	}
	// A provider without a cap is unlimited and needs no ledger query.
	d, err = g.Check(t.Context(), Request{Provider: "local", EstimateMicros: 1 << 40})
	if err != nil || !d.Allowed || queries != 1 {
		t.Errorf("uncapped provider: %+v %v (queries %d)", d, err, queries)
	}
}

// TestBudgetGuardZeroIsUnlimited: SPEC §12 says 0 means no cap; unknown
// costs are then not a reason to deny and the source is never consulted.
func TestBudgetGuardZeroIsUnlimited(t *testing.T) {
	t.Parallel()
	g := mustGuard(t, GuardConfig{}, totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
		t.Error("source consulted without a configured cap")
		return 1, 1, nil
	}), fixedNow(time.Unix(0, 0)))
	d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: -1})
	if err != nil || !d.Allowed || d.Reason != "" {
		t.Errorf("decision = %+v, %v", d, err)
	}
}

func TestBudgetGuardUnknownCostFailsClosed(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 10, 21, 1, 0, 0, 0, jakarta())
	cases := []struct {
		name    string
		used    int64
		unknown int64
		est     int64
	}{
		{"ledger row without cost", 0, 1, 0},
		{"pending call without price", 0, 0, -1},
		{"negative ledger sum", -1, 0, 0},
	}
	for _, c := range cases {
		g := mustGuard(t, GuardConfig{DailyUSD: 10},
			totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
				return c.used, c.unknown, nil
			}), fixedNow(now))
		d, err := g.Check(t.Context(), Request{Provider: "p", EstimateMicros: c.est})
		if err != nil || d.Allowed || d.Reason != ReasonCostUnknown || d.Cap != CapDaily {
			t.Errorf("%s: %+v %v", c.name, d, err)
		}
	}
}

func TestBudgetGuardSourceError(t *testing.T) {
	t.Parallel()
	boom := errors.New("ledger unavailable")
	g := mustGuard(t, GuardConfig{DailyUSD: 10},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
			return 0, 0, boom
		}), fixedNow(time.Now()))
	if _, err := g.Check(t.Context(), Request{Provider: "p"}); !errors.Is(err, boom) {
		t.Errorf("error = %v, want %v", err, boom)
	}
}

func TestBudgetGuardContextCancelled(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	g := mustGuard(t, GuardConfig{DailyUSD: 10},
		totalsFunc(func(ctx context.Context, _, _ int64, _ string) (int64, int64, error) {
			return 0, 0, ctx.Err()
		}), fixedNow(time.Now()))
	if _, err := g.Check(ctx, Request{Provider: "p"}); !errors.Is(err, context.Canceled) {
		t.Errorf("error = %v, want context.Canceled", err)
	}
}

func TestBudgetGuardDeterministicOrder(t *testing.T) {
	t.Parallel()
	g := mustGuard(t, GuardConfig{DailyUSD: 1, MonthlyUSD: 1},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) {
			return 1_000_000, 0, nil
		}), fixedNow(time.Now()))
	d, err := g.Check(t.Context(), Request{Provider: "p"})
	if err != nil || d.Reason != ReasonDaily {
		t.Errorf("decision = %+v, %v; daily must win the fixed order", d, err)
	}
}

func TestGuardConfigValidation(t *testing.T) {
	t.Parallel()
	src := totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) { return 0, 0, nil })
	cases := []struct {
		name string
		cfg  GuardConfig
		now  func() time.Time
	}{
		{"negative daily", GuardConfig{DailyUSD: -1}, nil},
		{"NaN daily", GuardConfig{DailyUSD: math.NaN()}, nil},
		{"infinite monthly", GuardConfig{MonthlyUSD: math.Inf(1)}, nil},
		{"empty provider", GuardConfig{Providers: map[string]float64{"": 1}}, nil},
		{"negative provider", GuardConfig{Providers: map[string]float64{"p": -1}}, nil},
		{"warn_at zero means default", GuardConfig{WarnAt: 0}, nil},
		{"warn_at above one", GuardConfig{WarnAt: 1.5}, nil},
	}
	for _, c := range cases {
		_, err := NewGuard(c.cfg, src, c.now)
		if c.name == "warn_at zero means default" {
			if err != nil {
				t.Errorf("%s: %v", c.name, err)
			}
			continue
		}
		if !errors.Is(err, ErrGuardConfig) {
			t.Errorf("%s: err = %v, want ErrGuardConfig", c.name, err)
		}
	}
	if _, err := NewGuard(GuardConfig{}, nil, nil); !errors.Is(err, ErrGuardConfig) {
		t.Errorf("nil source: %v", err)
	}
}

func BenchmarkGuard(b *testing.B) {
	now := time.Date(2026, 10, 21, 12, 0, 0, 0, jakarta())
	g, err := NewGuard(GuardConfig{DailyUSD: 10, MonthlyUSD: 100, Providers: map[string]float64{"p": 20}},
		totalsFunc(func(context.Context, int64, int64, string) (int64, int64, error) { return 1, 0, nil }),
		fixedNow(now))
	if err != nil {
		b.Fatal(err)
	}
	req := Request{Provider: "p", EstimateMicros: 2}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := g.Check(context.Background(), req); err != nil {
			b.Fatal(err)
		}
	}
}
