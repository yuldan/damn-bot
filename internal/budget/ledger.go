package budget

import (
	"errors"
	"fmt"
	"math"
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

// ErrInvalidUsage rejects negative token counts or a cached count larger
// than the input (a malformed provider usage block).
var ErrInvalidUsage = errors.New("budget: invalid usage")

// Cost prices u (SPEC §18.4): the provider-reported cost wins, else the
// price table, else unknown (micros = nil, the token caps still apply).
func Cost(u Usage, p *Price) (micros *int64, src CostSource, err error) {
	if u.Input < 0 || u.Output < 0 || u.CachedInput < 0 || u.CachedInput > u.Input {
		return nil, CostUnknown, fmt.Errorf("%w: input=%d cached=%d output=%d", ErrInvalidUsage, u.Input, u.CachedInput, u.Output)
	}
	switch {
	case u.CostUSD != nil && *u.CostUSD >= 0 && !math.IsInf(*u.CostUSD, 0):
		m := int64(math.Round(*u.CostUSD * 1e6))
		return &m, CostProvider, nil
	case p != nil:
		cached := p.CachedInputPerMTok
		if cached == 0 {
			cached = p.InputPerMTok
		}
		// Σ tokens × micros/MTok, rounded up to whole micro-USD.
		num := (u.Input-u.CachedInput)*p.InputPerMTok + u.CachedInput*cached + u.Output*p.OutputPerMTok
		m := (num + 999_999) / 1_000_000
		return &m, CostPricingTable, nil
	default:
		return nil, CostUnknown, nil
	}
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
