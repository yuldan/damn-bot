package budget

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
)

// CapKind identifies one monetary cap of SPEC §18.4.
type CapKind string

// Monetary caps owned by this guard (F2). Turn/session/tool/gateway caps
// live with their consumers (loop, gateway, tools) but use the same ledger.
const (
	CapDaily    CapKind = "daily"
	CapMonthly  CapKind = "monthly"
	CapProvider CapKind = "provider"
)

// Reason is the typed stop reason of a denied call (SPEC §3.5 budget_*).
// Callers map it to turn_end.detail and to the human message.
type Reason string

// Denial reasons. budget_unknown_cost is the conservative extension for a
// period whose spend cannot be proven: a ledger row with NULL cost or an
// unpriced pending call. SPEC §18.4 keeps the token caps in force in that
// case; the monetary cap fails closed instead of silently under-counting.
const (
	ReasonDaily       Reason = "budget_daily"
	ReasonMonthly     Reason = "budget_monthly"
	ReasonProvider    Reason = "budget_provider"
	ReasonCostUnknown Reason = "budget_unknown_cost"
)

// ErrGuardConfig rejects an unusable guard configuration.
var ErrGuardConfig = errors.New("budget: invalid guard config")

// TotalsSource is the consumer-side storage seam (E-09, one method): the
// summed known cost in micro-USD and the number of rows whose cost is NULL
// in [fromMS, toMS) for provider ("" = every provider). A non-nil error is
// fail-closed: the caller must not make the call.
//
// internal/ctxdb/ledgerstore.Source implements the method set without
// importing this package (ADR-074); the composition root only wires it.
type TotalsSource interface {
	Totals(ctx context.Context, fromMS, toMS int64, provider string) (costMicros, unknown int64, err error)
}

// GuardConfig is the SPEC §12 [budget] subset used by the F2 guard.
// Zero limits mean "no cap for that level" (SPEC §12), not "deny".
// Providers is USD per calendar month, like the provider rows of §18.4.
type GuardConfig struct {
	DailyUSD   float64
	MonthlyUSD float64
	Providers  map[string]float64
	WarnAt     float64 // 0 = 0.80
}

// Request is one pending provider call. EstimateMicros is the conservative
// projection (tokens × registry price, rounded up); a negative value means
// the price is unknown.
type Request struct {
	Provider       string
	EstimateMicros int64
}

// Decision is the guard verdict. Warnings lists the caps that crossed
// warn_at in this evaluation (once per cap per period); Allowed is false
// when a cap is already at or above 100% after adding the projection.
type Decision struct {
	Allowed        bool
	Reason         Reason
	Cap            CapKind
	LimitMicros    int64
	UsedMicros     int64
	EstimateMicros int64
	ResetAt        int64 // unix ms of the next period boundary
	Warnings       []CapKind
}

// Err returns nil when the call is allowed, otherwise a *DeniedError that
// carries the typed reason and the SPEC §18.4 message (cap, numbers, reset,
// and the command that raises the cap).
func (d *Decision) Err() error {
	if d.Allowed {
		return nil
	}
	return &DeniedError{Decision: *d}
}

// DeniedError is the error form of a denied Decision.
type DeniedError struct{ Decision }

// Error implements error. It never includes prompt content.
func (e *DeniedError) Error() string {
	if e.Reason == ReasonCostUnknown {
		return fmt.Sprintf("budget: cost unknown for cap %s; token caps still apply", e.Cap)
	}
	reset := time.UnixMilli(e.ResetAt).UTC().Format(time.RFC3339)
	return fmt.Sprintf("budget: %s cap reached (%d of %d µUSD, reset %s); raise it with `damnbot usage budget set %s 15`",
		e.Cap, e.UsedMicros, e.LimitMicros, reset, e.Cap)
}

// Checker is the one-method seam consulted before a provider call (P27).
// *Guard implements it; the provider client accepts any implementation.
type Checker interface {
	Check(ctx context.Context, r Request) (Decision, error)
}

// Guard evaluates the daily, monthly, and per-provider caps over the usage
// ledger. It holds no global state: the warning dedupe map belongs to the
// instance, and the clock is injected. Safe for concurrent use.
type Guard struct {
	daily     int64
	monthly   int64
	providers map[string]int64
	warnAt    float64
	source    TotalsSource
	now       func() time.Time

	mu     sync.Mutex
	warned map[string]int64 // cap|provider|reset → reset (unix ms)
}

var _ Checker = (*Guard)(nil)

// NewGuard validates cfg and builds a guard over source. now may be nil
// (time.Now); tests inject a fake clock.
func NewGuard(cfg GuardConfig, source TotalsSource, now func() time.Time) (*Guard, error) {
	if source == nil {
		return nil, fmt.Errorf("%w: nil totals source", ErrGuardConfig)
	}
	if now == nil {
		now = time.Now
	}
	warnAt := cfg.WarnAt
	if warnAt == 0 {
		warnAt = 0.8
	}
	if !(warnAt > 0 && warnAt <= 1) {
		return nil, fmt.Errorf("%w: warn_at %g outside (0,1]", ErrGuardConfig, cfg.WarnAt)
	}
	daily, err := limitMicros(cfg.DailyUSD)
	if err != nil {
		return nil, fmt.Errorf("%w: daily_usd: %w", ErrGuardConfig, err)
	}
	monthly, err := limitMicros(cfg.MonthlyUSD)
	if err != nil {
		return nil, fmt.Errorf("%w: monthly_usd: %w", ErrGuardConfig, err)
	}
	g := &Guard{
		daily: daily, monthly: monthly, warnAt: warnAt, source: source, now: now,
		providers: make(map[string]int64, len(cfg.Providers)), warned: map[string]int64{},
	}
	for name, usd := range cfg.Providers {
		if name == "" {
			return nil, fmt.Errorf("%w: empty provider name", ErrGuardConfig)
		}
		m, err := limitMicros(usd)
		if err != nil {
			return nil, fmt.Errorf("%w: provider %q: %w", ErrGuardConfig, name, err)
		}
		if m > 0 {
			g.providers[name] = m
		}
	}
	return g, nil
}

// limitMicros converts a USD limit to micro-USD, rounding up so a fractional
// micro-USD never loosens the cap. 0 keeps "no cap" (SPEC §12).
func limitMicros(usd float64) (int64, error) {
	if usd == 0 {
		return 0, nil
	}
	if !(usd > 0) || math.IsInf(usd, 0) || usd > float64(math.MaxInt64)/1e6 {
		return 0, fmt.Errorf("limit %g USD is not a finite value in range", usd)
	}
	return int64(math.Ceil(usd * 1e6)), nil
}

// period is one configured cap for one call.
type period struct {
	kind     CapKind
	limit    int64
	from, to time.Time
	provider string
}

// Check evaluates every configured cap for r. Caps are checked in the fixed
// order daily → monthly → provider; the first denial wins, so the reported
// reason is deterministic. A ledger error is returned (fail-closed) and no
// call may proceed.
func (g *Guard) Check(ctx context.Context, r Request) (Decision, error) {
	now := g.now()
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	month := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, now.Location())
	periods := make([]period, 0, 3)
	periods = append(periods,
		period{kind: CapDaily, limit: g.daily, from: day, to: day.AddDate(0, 0, 1)},
		period{kind: CapMonthly, limit: g.monthly, from: month, to: month.AddDate(0, 1, 0)})
	if limit := g.providers[r.Provider]; limit > 0 {
		periods = append(periods, period{kind: CapProvider, limit: limit, from: month, to: month.AddDate(0, 1, 0), provider: r.Provider})
	}

	d := Decision{Allowed: true, EstimateMicros: r.EstimateMicros}
	for i := range periods {
		p := &periods[i]
		if p.limit <= 0 {
			continue
		}
		used, unknown, err := g.source.Totals(ctx, p.from.UnixMilli(), p.to.UnixMilli(), p.provider)
		if err != nil {
			return Decision{}, fmt.Errorf("budget guard %s: %w", p.kind, err)
		}
		d.LimitMicros, d.UsedMicros, d.ResetAt = p.limit, used, p.to.UnixMilli()
		if unknown > 0 || used < 0 || r.EstimateMicros < 0 {
			d.Allowed, d.Reason, d.Cap = false, ReasonCostUnknown, p.kind
			return d, nil
		}
		projected := used + r.EstimateMicros
		if projected < used { // int64 overflow: cannot prove the spend
			d.Allowed, d.Reason, d.Cap = false, ReasonCostUnknown, p.kind
			return d, nil
		}
		if g.crossed(p, projected) {
			d.Warnings = append(d.Warnings, p.kind)
		}
		if projected >= p.limit { // 100% is a hard stop; 80% only warns
			d.Allowed, d.Reason, d.Cap = false, reason(p.kind), p.kind
			return d, nil
		}
	}
	return d, nil
}

// crossed reports whether projected reached warn_at for p and records the
// warning so it is emitted once per cap per period.
func (g *Guard) crossed(p *period, projected int64) bool {
	if float64(projected) < g.warnAt*float64(p.limit) {
		return false
	}
	key := string(p.kind) + "\x00" + p.provider
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.warned[key] == p.to.UnixMilli() {
		return false
	}
	g.warned[key] = p.to.UnixMilli()
	return true
}

// reason maps a cap to its stop reason.
func reason(kind CapKind) Reason {
	switch kind {
	case CapDaily:
		return ReasonDaily
	case CapMonthly:
		return ReasonMonthly
	case CapProvider:
		return ReasonProvider
	}
	return ReasonProvider
}
