package gateway

import (
	"context"
	"errors"
	"fmt"
	"math"
	"time"
)

// ErrInvalidBudget rejects a daily USD cap the gateway cannot enforce.
var ErrInvalidBudget = errors.New("gateway: invalid budget")

// SpendSource is the consumer-side ledger seam (E-09): the summed known cost
// in micro-USD and the number of NULL-cost rows of one gateway identity in
// [fromMS, toMS) (UTC). A non-nil error is fail-closed: the turn is not run.
type SpendSource interface {
	Spend(ctx context.Context, identity string, fromMS, toMS int64) (costMicros, unknown int64, err error)
}

// DefaultUserDailyUSD is the SPEC §18.4 cap for one role user identity; a zero
// BudgetOptions.DailyUSD takes this default (config already supplies the key).
const DefaultUserDailyUSD = 1.00

// BudgetOptions configures the daily USD cap of role user identities
// (SPEC §18.4, GAP-GW-02). A zero Clock is SystemClock.
type BudgetOptions struct {
	DailyUSD float64
	Clock    Clock
	Spend    SpendSource
}

// BudgetDecision is Check's answer. Reset is the next UTC midnight, the only
// point at which an exceeded cap can admit again.
type BudgetDecision struct {
	Allowed     bool
	UsedMicros  int64
	LimitMicros int64
	Reset       time.Time
}

// BudgetGuard enforces the daily USD cap of one role user gateway identity.
// It is a pre-turn admission gate: it only reads the ledger and never calls
// the model or a tool. Owner identities are never capped here (SPEC §18.4:
// the owner uses the global caps).
type BudgetGuard struct {
	daily int64
	clock Clock
	spend SpendSource
}

// NewBudgetGuard validates opts and builds a guard. The cap must be a finite
// USD value in (0, 100], the range of the config key (SPEC §12).
func NewBudgetGuard(opts *BudgetOptions) (*BudgetGuard, error) {
	if opts == nil || opts.Spend == nil {
		return nil, fmt.Errorf("%w: nil spend source", ErrInvalidBudget)
	}
	daily := opts.DailyUSD
	if daily == 0 {
		daily = DefaultUserDailyUSD
	}
	limit, err := usdMicros(daily)
	if err != nil {
		return nil, fmt.Errorf("%w: daily_usd: %w", ErrInvalidBudget, err)
	}
	clock := opts.Clock
	if clock == nil {
		clock = SystemClock{}
	}
	return &BudgetGuard{daily: limit, clock: clock, spend: opts.Spend}, nil
}

// usdMicros converts a positive USD cap to micro-USD, rounding up so a
// fractional micro-USD never loosens the cap.
func usdMicros(usd float64) (int64, error) {
	if !(usd > 0) || usd > 100 || math.IsInf(usd, 0) {
		return 0, fmt.Errorf("limit %g USD is not in (0, 100]", usd)
	}
	return int64(math.Ceil(usd * 1e6)), nil
}

// Check evaluates the UTC day of the injected clock. Only role user identities
// are capped. The turn is denied when the day's known spend already exceeds
// the cap (spend equal to the cap still admits, SPEC §18.4) or when a NULL-cost
// row makes the spend unprovable (fail closed).
func (b *BudgetGuard) Check(ctx context.Context, id *Identity) (BudgetDecision, error) {
	now := b.clock.Now().UTC()
	reset := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	d := BudgetDecision{Allowed: true, LimitMicros: b.daily, Reset: reset}
	if id == nil || id.Role != RoleUser {
		return d, nil
	}
	identity := id.Platform + ":" + id.ExternalID
	used, unknown, err := b.spend.Spend(ctx, identity, reset.AddDate(0, 0, -1).UnixMilli(), reset.UnixMilli())
	if err != nil {
		return BudgetDecision{}, fmt.Errorf("gateway: budget: %w", err)
	}
	d.UsedMicros = used
	if unknown > 0 || used < 0 || used > b.daily {
		d.Allowed = false
	}
	return d, nil
}
