//go:build minimal

package provider

// Minimal-build retry loop and Response; kept small for the P13 size gate.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

//
//line client.go:1
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
		resp, err = o.send(ctx, rt, r, est, onChunk, nil)
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
		billed++ // the upstream accepted the request: it may have been billed
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

type Response struct {
	Content, FinishReason, Model  string
	ToolCalls                     []ToolCall
	Reasoning                     json.RawMessage
	ReasoningField, ReasoningText string
	Usage                         budget.Usage
	EstInputTokens                int64
	Attempts, BilledFailed        int
	FallbackFrom, CredentialID    string
	TTFT                          time.Duration
	Price                         *budget.Price
	responded                     bool
	Carriers, RejectedCarriers    []Carrier
	CarrierDrops                  []CarrierDrop
}

func wireForAttempt(w Wire, prev *Response) Wire {
	return w
}
