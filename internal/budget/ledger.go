package budget

import (
	"errors"
	"fmt"
	"math"
	"math/bits"
)

// Usage is the token and cost usage of one LLM call as reported by the
// provider (SPEC §4.1 Response.Usage).
type Usage struct {
	Input, CachedInput, Output int64
	// CostUSD is the provider-reported cost (OpenRouter usage.cost); nil
	// when the provider sent none.
	CostUSD *float64
}

// Price is one registry/config price entry in micro-USD per million tokens
// (SPEC §4.1 registry, GAP-COST-01).
type Price struct {
	InputPerMTok       int64
	CachedInputPerMTok int64 // 0 = not discounted: billed at InputPerMTok
	OutputPerMTok      int64
	Version            string // usage_ledger.pricing_version
}

// CostSource is usage_ledger.cost_source.
type CostSource string

// Cost sources (SPEC §11.2 CHECK).
const (
	CostProvider     CostSource = "provider"
	CostPricingTable CostSource = "pricing_table"
	CostUnknown      CostSource = "unknown"
)

// PricingNone is the pricing_version of a call without a known price.
const PricingNone = "none"

// ErrInvalidUsage rejects a usage block that cannot be accounted exactly:
// negative or implausible token counts (> MaxTokens), a cached count larger
// than the input, a provider cost above MaxProviderCostUSD, a negative
// price, or a price-table cost that overflows int64 micro-USD. Callers fail
// closed on it (REVIEW-F1-SEC3 LED-2/3): the call is never recorded as
// cheaper than it was.
var ErrInvalidUsage = errors.New("budget: invalid usage")

// Plausibility bounds of one call (ADR-101 butir 4).
const (
	// MaxTokens bounds each token count of one call (10^12; context
	// windows are ≤ 10^7).
	MaxTokens = 1_000_000_000_000
	// MaxProviderCostUSD bounds a provider-reported cost of one call.
	MaxProviderCostUSD = 1_000_000
)

// Cost prices u (SPEC §18.4): the provider-reported cost wins, else the
// price table, else unknown (micros = nil, the token caps still apply).
func Cost(u Usage, p *Price) (micros *int64, src CostSource, err error) {
	if u.Input < 0 || u.Output < 0 || u.CachedInput < 0 || u.CachedInput > u.Input ||
		u.Input > MaxTokens || u.Output > MaxTokens {
		return nil, CostUnknown, fmt.Errorf("%w: input=%d cached=%d output=%d", ErrInvalidUsage, u.Input, u.CachedInput, u.Output)
	}
	if u.CostUSD != nil && *u.CostUSD > MaxProviderCostUSD { // incl. +Inf
		return nil, CostUnknown, fmt.Errorf("%w: provider cost %g USD", ErrInvalidUsage, *u.CostUSD)
	}
	switch {
	case u.CostUSD != nil && *u.CostUSD >= 0 && *u.CostUSD <= MaxProviderCostUSD:
		m := int64(math.Round(*u.CostUSD * 1e6)) // ≤ 10^12: exact conversion
		return &m, CostProvider, nil
	case p != nil:
		m, err := tableCost(u, p)
		if err != nil {
			return nil, CostUnknown, err
		}
		return &m, CostPricingTable, nil
	default:
		return nil, CostUnknown, nil
	}
}

// tableCost is Σ tokens × micros/MTok rounded up to whole micro-USD, in
// checked arithmetic: any overflow is ErrInvalidUsage, never a wrapped
// (negative or tiny) cost.
func tableCost(u Usage, p *Price) (int64, error) {
	cached := p.CachedInputPerMTok
	if cached == 0 {
		cached = p.InputPerMTok
	}
	if p.InputPerMTok < 0 || cached < 0 || p.OutputPerMTok < 0 {
		return 0, fmt.Errorf("%w: negative price %+v", ErrInvalidUsage, *p)
	}
	var sum uint64
	for _, t := range [...][2]int64{{u.Input - u.CachedInput, p.InputPerMTok}, {u.CachedInput, cached}, {u.Output, p.OutputPerMTok}} {
		hi, lo := bits.Mul64(nonNeg(t[0]), nonNeg(t[1]))
		var carry uint64
		sum, carry = bits.Add64(sum, lo, 0)
		if hi != 0 || carry != 0 {
			return 0, fmt.Errorf("%w: cost overflows (tokens %d × %d µUSD/MTok)", ErrInvalidUsage, t[0], t[1])
		}
	}
	m := sum/1_000_000 + min(sum%1_000_000, 1)
	if m > math.MaxInt64 {
		return 0, fmt.Errorf("%w: cost overflows int64", ErrInvalidUsage)
	}
	return int64(m), nil
}

// Entry is one usage_ledger row (SPEC §11.2), written in the same
// transaction as its llm_call event (ctxdb/ledgerstore.Row has the same
// fields, so the composition root converts with a plain conversion).
type Entry struct {
	SessionID, TurnID string
	EventID           int64
	Purpose, Feature  string
	Provider, Model   string
	CredentialID      string
	GatewayIdentity   string
	InputTokens       int64
	EstInputTokens    int64 // Counts.Raw of the request (ADR-101)
	CachedInputTokens int64
	OutputTokens      int64
	CostMicros        *int64 // nil = unknown
	CostSource        string
	PricingVersion    string
	Attempts          int64
	LatencyMS, TTFTMS int64 // TTFTMS < 0 = not measured (NULL)
	CreatedAt         int64 // unix ms
}

// Account fills the usage and cost columns of e from u, p, and raw (the
// Counts.Raw estimate of the request).
func (e *Entry) Account(u Usage, p *Price, raw int64) error {
	micros, src, err := Cost(u, p)
	if err != nil {
		return err
	}
	e.InputTokens, e.CachedInputTokens, e.OutputTokens, e.EstInputTokens = u.Input, u.CachedInput, u.Output, raw
	e.CostMicros, e.CostSource, e.PricingVersion = micros, string(src), PricingNone
	if p != nil && src != CostUnknown {
		e.PricingVersion = p.Version
	}
	return nil
}

// nonNeg converts a count or price already checked to be ≥ 0.
func nonNeg(v int64) uint64 {
	if v < 0 {
		return 0
	}
	return uint64(v)
}
