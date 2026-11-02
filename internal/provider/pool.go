//go:build !minimal

package provider

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

// CredentialEvent reports one rotation inside a provider's credential pool
// (event credential_rotated, P7). It carries identities and the reason,
// never a credential value.
type CredentialEvent struct {
	Provider string
	From, To string
	Reason   credpool.Reason
	Cooldown time.Duration
	Attempt  int
}

// Pooled wraps an OpenAI client with the SPEC §4.1 credential pool per
// provider. It lives in a !minimal file, so the minimal build keeps exactly
// one credential and its binary is unchanged.
type Pooled struct {
	o      *OpenAI
	pools  map[string]*credpool.Pool
	onCred func(CredentialEvent)
}

// NewPooled binds pools (keyed by provider id) to an existing client. A
// route whose provider has no pool keeps the plain retry/fallback path.
func NewPooled(o *OpenAI, pools map[string]*credpool.Pool, onCred func(CredentialEvent)) (*Pooled, error) {
	if o == nil {
		return nil, fmt.Errorf("%w: nil client for credential pool", ErrConfig)
	}
	for name, p := range pools {
		if name == "" || p == nil || p.Len() == 0 {
			return nil, fmt.Errorf("%w: provider %q has an empty credential pool", ErrConfig, name)
		}
	}
	return &Pooled{o: o, pools: maps.Clone(pools), onCred: onCred}, nil
}

// Do mirrors [OpenAI.Do] and adds credential rotation on the routes that
// have a pool.
func (p *Pooled) Do(ctx context.Context, purpose Purpose, r *Request, onChunk func(Chunk)) (Response, error) {
	o := p.o
	chain := append([]string{r.Model}, o.fallback...)
	est, at := rawEstimate(r), o.now()
	var lastErr error
	var billed int
	for i, ref := range chain {
		if i > 0 && ref == r.Model {
			continue
		}
		rt, ok := o.routes[ref]
		switch {
		case !ok && i == 0:
			return Response{BilledFailed: billed}, &Error{Class: retry.BadRequest, Err: fmt.Errorf("%w %q", ErrNoRoute, ref)}
		case !ok, i > 0 && rt.ContextWindow > 0 && est > rt.ContextWindow:
			continue
		}
		price := rt.Prices.At(at)
		fb := &Fallback{From: r.Model, To: ref, Err: lastErr}
		o.notify(i, fb)
		var resp Response
		var err error
		if pool := p.pools[rt.Provider]; pool != nil {
			resp, err = p.callPool(ctx, &rt, price, r, est, billed, onChunk, pool)
		} else {
			resp, err = o.call(ctx, &rt, price, r, est, onChunk)
		}
		resp.Price = price
		billed += resp.BilledFailed
		resp.BilledFailed = billed
		fb.Done, fb.Outcome = true, err
		o.notify(i, fb)
		if err == nil {
			if i > 0 {
				resp.FallbackFrom = r.Model
			}
			return resp, nil
		}
		if !retry.ClassifyErr(err).Retryable() {
			return resp, err
		}
		lastErr = err
	}
	return Response{BilledFailed: billed}, lastErr
}

// callPool rotates across one provider's credentials on credential failures
// only. 400/5xx/timeout/network keep the current key and follow the normal
// retry/fallback policy. At most Len() credentials are tried.
func (p *Pooled) callPool(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, priorBilled int, onChunk func(Chunk), pool *credpool.Pool) (Response, error) {
	var last Response
	attempts, billed := 0, 0
	var from string
	var reason credpool.Reason
	var cooled time.Duration
	for range pool.Len() {
		now := p.o.now()
		cred, ok := pool.Acquire(now)
		if !ok {
			break
		}
		if from != "" {
			p.notifyCredential(rt.Provider, from, cred.ID, reason, cooled, attempts+1)
		}
		rtc := *rt
		rtc.APIKey = cred.Secret()
		rtc.CredentialID = cred.ID
		resp, err := p.callCredential(ctx, &rtc, price, r, est, priorBilled+billed, onChunk)
		attempts += resp.Attempts
		billed += resp.BilledFailed
		resp.Attempts, resp.BilledFailed, resp.CredentialID = attempts, billed, cred.ID
		last = resp
		if err == nil {
			pool.Success(cred.ID, p.o.now())
			return last, nil
		}
		nextReason, rotate := poolReason(err)
		if !rotate {
			return last, err
		}
		reason, from = nextReason, cred.ID
		cooled = pool.Fail(cred.ID, reason, retryAfter(err), p.o.now())
		// A rotation is another logical call; keep the monetary projection
		// cumulative across the credentials already billed in this Do.
		if derr := p.o.retryDenied(ctx, &rtc, p.o.retryProjection(&rtc, &resp, r, est, priorBilled+billed)); derr != nil {
			return last, derr
		}
	}
	return last, &Error{Class: retry.Auth, Err: &credpool.ExhaustedError{
		Provider: rt.Provider, Count: pool.Len(), RetryAfter: pool.NextAvailable(p.o.now()),
	}}
}

// callCredential is the per-credential retry loop. A credential failure is
// returned as a non-retryable signal so the same key is not retried.
func (p *Pooled) callCredential(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, priorBilled int, onChunk func(Chunk)) (Response, error) {
	o := p.o
	br := o.breaker(rt)
	var resp Response
	attempts := 0
	limit := o.attempts
	if limit <= 0 {
		limit = retry.MaxAttempts
	}
	billed := 0
	err := retry.Do(ctx, retry.Policy{Notify: o.onRetry, Wait: o.wait, Attempts: o.attempts}, func(ctx context.Context, n int) error {
		var err error
		resp, err = p.tryAttempt(ctx, rt, price, r, est, br, onChunk)
		attempts = n
		if err == nil {
			return nil
		}
		if _, rotate := poolReason(err); rotate {
			return &poolStop{err: err}
		}
		if !resp.responded {
			return err
		}
		billed++
		if n == limit || !retry.ClassifyErr(err).Retryable() {
			return err
		}
		if derr := o.retryDenied(ctx, rt, o.retryProjection(rt, &resp, r, est, billed+priorBilled)); derr != nil {
			return derr
		}
		return err
	})
	if stop, ok := errors.AsType[*poolStop](err); ok {
		err = stop.err
	}
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

// tryAttempt runs one policy attempt for a pooled credential.
func (p *Pooled) tryAttempt(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, br retry.Gate, onChunk func(Chunk)) (Response, error) {
	o := p.o
	if err := br.Allow(ctx); err != nil {
		return Response{}, fmt.Errorf("%s: %w", rt.Ref(), err)
	}
	if err := o.checkBudget(ctx, rt, price, r, est); err != nil {
		return Response{}, err
	}
	resp, err := o.send(ctx, rt, r, est, onChunk, nil)
	if rerr := br.Record(ctx, retry.ClassifyErr(err)); rerr != nil {
		return Response{}, fmt.Errorf("%s: circuit: %w", rt.Ref(), rerr)
	}
	if err == nil {
		captureCarriers(&resp, rt)
	}
	return resp, err
}

// notifyCredential reports a rotation without any credential value.
func (p *Pooled) notifyCredential(provider, from, to string, reason credpool.Reason, cooled time.Duration, attempt int) {
	if p.onCred == nil {
		return
	}
	p.onCred(CredentialEvent{Provider: provider, From: from, To: to, Reason: reason, Cooldown: cooled, Attempt: attempt})
}

// poolStop marks a credential failure that must rotate instead of retrying
// the same key. It reports a non-retryable class to the retry policy.
type poolStop struct{ err error }

func (p *poolStop) Error() string           { return p.err.Error() }
func (p *poolStop) Unwrap() error           { return p.err }
func (p *poolStop) RetryClass() retry.Class { return retry.Unclassified }

// poolReason maps a provider failure to its rotation reason. Only 401/403
// and 429 rotate; 400, 5xx, timeout, and network errors do not.
func poolReason(err error) (credpool.Reason, bool) {
	var pe *Error
	if !errors.As(err, &pe) {
		return "", false
	}
	switch pe.Class {
	case retry.Auth:
		return credpool.ReasonAuth, true
	case retry.RateLimited:
		return credpool.ReasonRateLimited, true
	case retry.None, retry.Overloaded, retry.Server, retry.Timeout, retry.Network, retry.StreamBroken,
		retry.ContextLength, retry.EmptyResponse, retry.BadRequest, retry.OfflineMode, retry.Cancelled,
		retry.Unclassified:
		return "", false
	}
	return "", false
}

// retryAfter exposes the provider Retry-After hint, if any.
func retryAfter(err error) time.Duration {
	var hint retry.ServerHint
	if errors.As(err, &hint) {
		return hint.RetryAfter()
	}
	return 0
}
