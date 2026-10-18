// Package ledgerstore is the usage_ledger storage of internal/budget (SPEC
// §11.2, ADR-074). Row and Sample have exactly the fields of budget.Entry and
// budget.Sample, so the composition root converts between them without
// either package importing the other.
package ledgerstore

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Row is one usage_ledger row; field-identical to budget.Entry.
type Row struct {
	SessionID, TurnID string
	EventID           int64
	Purpose, Feature  string
	Provider, Model   string
	CredentialID      string
	GatewayIdentity   string
	InputTokens       int64
	EstInputTokens    int64
	CachedInputTokens int64
	OutputTokens      int64
	CostMicros        *int64 // nil = NULL (cost unknown)
	CostSource        string
	PricingVersion    string
	Attempts          int64
	LatencyMS, TTFTMS int64 // TTFTMS < 0 = NULL
	CreatedAt         int64
}

// Sample is one calibration sample; field-identical to budget.Sample.
type Sample struct {
	Real, Est int64
}

// Totals sums a session's ledger (UI cost = Σ ledger, SPEC §18.4).
type Totals struct {
	Calls, Input, CachedInput, Output int64
	CostMicros                        int64 // sum of known costs
	UnknownCost                       int64 // calls with cost_usd_micros NULL
}

// Insert appends r inside tx, normally the transaction that appends its
// llm_call event (the ledger is append-only, P9). It returns the row id.
func Insert(tx *ctxdb.Tx, r *Row) (int64, error) {
	var ttft any
	if r.TTFTMS >= 0 {
		ttft = r.TTFTMS
	}
	var turn any
	if r.TurnID != "" {
		turn = r.TurnID
	}
	var id int64
	err := tx.Get(`INSERT INTO usage_ledger(session_id, turn_id, event_id, purpose, feature, provider, model,
		credential_id, gateway_identity, input_tokens, est_input_tokens, cached_input_tokens, output_tokens,
		cost_usd_micros, cost_source, pricing_version, attempts, latency_ms, ttft_ms, created_at)
		VALUES (?1,?2,?3,?4,?5,?6,?7,?8,?9,?10,?11,?12,?13,?14,?15,?16,?17,?18,?19,?20) RETURNING id`,
		[]any{
			r.SessionID, turn, r.EventID, r.Purpose, r.Feature, r.Provider, r.Model, r.CredentialID,
			r.GatewayIdentity, r.InputTokens, r.EstInputTokens, r.CachedInputTokens, r.OutputTokens,
			r.CostMicros, r.CostSource, r.PricingVersion, r.Attempts, r.LatencyMS, ttft, r.CreatedAt,
		}, &id)
	if err != nil {
		return 0, fmt.Errorf("ledgerstore insert: %w", err)
	}
	return id, nil
}

// Calibration returns the newest ≤ n purpose='turn' samples of sid with an
// estimate, oldest first, and the input_tokens of the newest turn call
// (0 when none): the inputs of budget.NewEstimator (GAP-COST-09).
func Calibration(ctx context.Context, s *ctxdb.Store, sid string, n int) (samples []Sample, last int64, err error) {
	err = s.Write(ctx, func(tx *ctxdb.Tx) error {
		samples, last, err = calibration(tx, sid, n)
		return err
	})
	if err != nil {
		return nil, 0, fmt.Errorf("ledgerstore calibration: %w", err)
	}
	return samples, last, nil
}

func calibration(tx *ctxdb.Tx, sid string, n int) ([]Sample, int64, error) {
	const window = `SELECT id, input_tokens, est_input_tokens FROM usage_ledger
		WHERE session_id = ?1 AND purpose = 'turn' AND est_input_tokens > 0 ORDER BY id DESC LIMIT ?2`
	real, err := tx.Ints(`SELECT input_tokens FROM (`+window+`) ORDER BY id`, sid, n)
	if err != nil {
		return nil, 0, fmt.Errorf("input_tokens: %w", err)
	}
	est, err := tx.Ints(`SELECT est_input_tokens FROM (`+window+`) ORDER BY id`, sid, n)
	if err != nil {
		return nil, 0, fmt.Errorf("est_input_tokens: %w", err)
	}
	samples := make([]Sample, len(real))
	for i := range real {
		samples[i] = Sample{Real: real[i], Est: est[i]}
	}
	var last int64
	if err := tx.Get(`SELECT COALESCE((SELECT input_tokens FROM usage_ledger
		WHERE session_id = ?1 AND purpose = 'turn' ORDER BY id DESC LIMIT 1), 0)`, []any{sid}, &last); err != nil {
		return nil, 0, fmt.Errorf("last prompt: %w", err)
	}
	return samples, last, nil
}

// SessionTotals sums every ledger row of sid.
func SessionTotals(ctx context.Context, s *ctxdb.Store, sid string) (Totals, error) {
	var t Totals
	err := s.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT COUNT(*), COALESCE(SUM(input_tokens),0), COALESCE(SUM(cached_input_tokens),0),
			COALESCE(SUM(output_tokens),0), COALESCE(SUM(cost_usd_micros),0), COUNT(*) - COUNT(cost_usd_micros)
			FROM usage_ledger WHERE session_id = ?1`, []any{sid},
			&t.Calls, &t.Input, &t.CachedInput, &t.Output, &t.CostMicros, &t.UnknownCost)
	})
	if err != nil {
		return Totals{}, fmt.Errorf("ledgerstore totals: %w", err)
	}
	return t, nil
}
