//go:build !minimal

package provider

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/netx/retry"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

// poolFields carries the optional SPEC §4.1 credential pool of a route. The
// minimal build omits the pool (P13), so the field lives behind this seam.
type poolFields struct {
	// Pool, when set, replaces APIKey: every attempt takes the next usable
	// credential and sets CredentialID. A route with both Pool and Auth is
	// rejected, because a signer owns the auth headers.
	Pool *credpool.Pool
}

// credentialHooks carries the pool observer of Config.
type credentialHooks struct {
	// OnCredential observes every credential rotation of a pooled route
	// (event credential_rotated, P7). It never receives a secret.
	OnCredential func(CredentialEvent)
}

// credentialState is the client's pool observer.
type credentialState struct {
	onCred func(CredentialEvent)
}

// CredentialEvent reports one rotation inside a route's credential pool
// (event credential_rotated, P7). It carries identities and the reason,
// never a credential value.
type CredentialEvent struct {
	Provider string
	From, To string
	Reason   credpool.Reason
	Cooldown time.Duration
	Attempt  int
}

// WithCredentialPool binds rt to pool (SPEC §4.1). The pool replaces the
// static APIKey; the endpoint and model stay unchanged, so rotation never
// moves a session to another host (EC-PROV-017).
func WithCredentialPool(rt *Route, pool *credpool.Pool) {
	rt.Pool = pool
	rt.APIKey = ""
}

// credentialStateOf copies the observer into the client.
func credentialStateOf(cfg *Config) credentialState {
	return credentialState{onCred: cfg.OnCredential}
}

// validatePoolRoute rejects a pool combined with a signer, because the signer
// owns the auth header and would silently ignore the pool.
func validatePoolRoute(rt *Route) error {
	if rt.Pool != nil && rt.Auth != nil {
		return fmt.Errorf("%w: route %q has both a credential pool and an auth signer", ErrConfig, rt.Ref())
	}
	return nil
}

// credentialRotation turns a credential failure into a non-retryable signal
// so the same key is not retried; every other class keeps the retry policy.
func (o *OpenAI) credentialRotation(rt *Route, err error) error {
	if rt.Pool == nil {
		return nil
	}
	if _, rotate := poolReason(err); rotate {
		return &poolStop{err: err}
	}
	return nil
}

// unwrapCredential restores the original error after a rotation signal.
func unwrapCredential(err error) error {
	if stop, ok := errors.AsType[*poolStop](err); ok {
		return stop.err
	}
	return err
}

// callPooled rotates across the route's credentials on credential failures
// only. A 400 or 5xx keeps the current credential: the request is the
// problem, not the key. Rotation stops after at most Len() attempts, and
// every credential ends in cooldown rather than an infinite loop.
func (o *OpenAI) callPooled(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	pool := rt.Pool
	var last Response
	prior, attempts := 0, 0
	var from string
	var reason credpool.Reason
	var cooled time.Duration
	for range pool.Len() {
		now := o.now()
		cred, ok := pool.Acquire(now)
		if !ok {
			break
		}
		if from != "" {
			o.notifyCredential(rt.Provider, from, cred.ID, reason, cooled, attempts+1)
		}
		rtc := *rt
		rtc.APIKey = cred.Secret()
		rtc.CredentialID = cred.ID
		resp, err := o.callRoute(ctx, &rtc, price, r, est, prior, onChunk)
		prior += resp.BilledFailed
		attempts += resp.Attempts
		resp.Attempts, resp.BilledFailed, resp.CredentialID = attempts, prior, cred.ID
		last = resp
		if err == nil {
			pool.Success(cred.ID, o.now())
			return last, nil
		}
		nextReason, rotate := poolReason(err)
		if !rotate {
			return last, err
		}
		reason, from = nextReason, cred.ID
		cooled = pool.Fail(cred.ID, reason, retryAfter(err), o.now())
	}
	return last, o.credentialExhausted(rt, pool)
}

// credentialExhausted is the typed credential_exhausted failure of a pool
// whose entries all cool down.
func (o *OpenAI) credentialExhausted(rt *Route, pool *credpool.Pool) error {
	return &Error{Class: retry.Auth, Err: &credpool.ExhaustedError{
		Provider: rt.Provider, Count: pool.Len(), RetryAfter: pool.NextAvailable(o.now()),
	}}
}

// notifyCredential reports a rotation without any credential value.
func (o *OpenAI) notifyCredential(provider, from, to string, reason credpool.Reason, cooled time.Duration, attempt int) {
	if o.onCred == nil {
		return
	}
	o.onCred(CredentialEvent{Provider: provider, From: from, To: to, Reason: reason, Cooldown: cooled, Attempt: attempt})
}

// poolStop marks a credential failure that must rotate instead of retrying
// the same key. It reports a non-retryable class to the retry policy.
type poolStop struct{ err error }

func (p *poolStop) Error() string           { return p.err.Error() }
func (p *poolStop) Unwrap() error           { return p.err }
func (p *poolStop) RetryClass() retry.Class { return retry.Unclassified }

// poolReason maps a provider failure to its rotation reason. Only 401/403
// and 429 rotate; 400, 5xx, timeouts, and network errors do not.
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

// call runs one route through its credential pool, or the retry policy
// directly when the route has no pool (SPEC §4.1).
func (o *OpenAI) call(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, onChunk func(Chunk)) (Response, error) {
	if rt.Pool == nil {
		return o.callRoute(ctx, rt, price, r, est, 0, onChunk)
	}
	return o.callPooled(ctx, rt, price, r, est, onChunk)
}

// callRoute runs one credential of a route through the retry policy and its
// breaker. priorBilled is the number of attempts already billed by this Do
// on other credentials; it only feeds the cumulative budget projection. The
// budget guard is consulted inside the policy closure, so every attempt —
// not just the first one of the route — is authorised before it can spend
// anything (REVIEW-F2-SEC5-27).
func (o *OpenAI) callRoute(ctx context.Context, rt *Route, price *budget.Price, r *Request, est int64, priorBilled int, onChunk func(Chunk)) (Response, error) {
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
		resp, err = o.routeAttempt(ctx, rt, price, r, est, br, onChunk)
		attempts = n
		if err == nil {
			return nil
		}
		if !resp.responded {
			return err
		}
		billed++ // the upstream accepted the request: it may have been billed
		if sig := o.credentialRotation(rt, err); sig != nil {
			return sig
		}
		if n == limit || !retry.ClassifyErr(err).Retryable() {
			return err
		}
		// A retry is another logical call: project this route's price for
		// every attempt already billed in this Do plus the next one, so a
		// monetary cap cannot be overshot by sequential retries (PRC-5).
		if derr := o.retryDenied(ctx, rt, o.retryProjection(rt, &resp, r, est, billed+priorBilled)); derr != nil {
			return derr
		}
		return err
	})
	err = unwrapCredential(err)
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
