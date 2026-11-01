//go:build !minimal

package provider

import (
	"context"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

func (o *OpenAI) call(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	br := o.breaker(rt)
	var resp Response
	attempts := 0
	limit := o.attempts
	if limit <= 0 {
		limit = retry.MaxAttempts
	}
	billed := 0
	var prev *Response
	var prevAt time.Time
	resumes := 0
	err := retry.Do(ctx, retry.Policy{Notify: o.onRetry, Wait: o.wait, Attempts: o.attempts}, func(ctx context.Context, n int) error {
		if err := br.Allow(ctx); err != nil {
			resp = Response{}
			return fmt.Errorf("%s: %w", rt.Ref(), err)
		}
		if err := o.checkBudget(ctx, rt, price, r, est); err != nil {
			return err
		}
		if err := o.resumeReady(prev, prevAt); err != nil {
			return err
		}
		attempts = n
		var err error
		resp, err = o.send(ctx, rt, r, est, onChunk, prev)
		prev, prevAt, resumes, err = o.observeAttempt(ctx, rt, br, &resp, err, prev, prevAt, resumes, n, limit, r, est, &billed)
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
