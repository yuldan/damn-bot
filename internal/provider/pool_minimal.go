//go:build minimal

package provider

import (
	"context"
	"fmt"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// poolFields is empty: the minimal build compiles no credential pool (P13).
type poolFields struct{}

// credentialHooks is empty: the minimal build has no rotation observer.
type credentialHooks struct{}

// credentialState is empty: the minimal build has no rotation observer.
type credentialState struct{}

// credentialStateOf returns the zero observer of the minimal build.
func credentialStateOf(*Config) credentialState { return credentialState{} }

// validatePoolRoute has no pool to validate in the minimal build.
func validatePoolRoute(*Route) error { return nil }

// call runs one route through the retry policy and its breaker. The budget
// guard is consulted inside the policy closure, so every attempt - not just
// the first one of the route - is authorised before it can spend anything
// (REVIEW-F2-SEC5-27). The minimal build compiles no credential pool (P13).
func (o *OpenAI) call(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	br := o.breaker(rt)
	var resp Response
	attempts := 0
	limit := o.attempts
	if limit <= 0 {
		limit = retry.MaxAttempts
	}
	billed := 0
	err := retry.Do(ctx, retry.Policy{Notify: o.onRetry, Wait: o.wait, Attempts: o.attempts}, func(ctx context.Context, n int) error {
		if err := br.Allow(ctx); err != nil {
			resp = Response{}
			return fmt.Errorf("%s: %w", rt.Ref(), err)
		}
		if err := o.checkBudget(ctx, rt, price, r, est); err != nil {
			return err
		}
		attempts = n
		var err error
		resp, err = o.send(ctx, rt, r, est, onChunk)
		if rerr := br.Record(ctx, retry.ClassifyErr(err)); rerr != nil {
			return fmt.Errorf("%s: circuit: %w", rt.Ref(), rerr)
		}
		if err == nil {
			captureCarriers(&resp, rt)
			return nil
		}
		if !resp.responded {
			return err
		}
		billed++
		if n == limit || !retry.ClassifyErr(err).Retryable() {
			return err
		}
		if derr := o.retryDenied(ctx, rt, o.retryProjection(rt, &resp, r, est, billed)); derr != nil {
			return derr
		}
		return err
	})
	resp.Attempts, resp.EstInputTokens, resp.CredentialID = attempts, est, rt.CredentialID
	resp.BilledFailed = billed
	if resp.Model == "" {
		resp.Model = rt.Model
	}
	if err != nil {
		return resp, fmt.Errorf("%s: %w", rt.Ref(), err)
	}
	return resp, nil
}
