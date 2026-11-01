//go:build !minimal

package provider

import (
	"context"
	"errors"

	"github.com/damnbot/damnbot/internal/netx/retry"
)

// TokenRefresher renews the credential behind a Route.Auth signer after a
// 401. The signer that implements it is passed through Route.Auth, so the
// Route struct and the minimal build are unchanged (EC-PROV-014).
type TokenRefresher interface {
	Epoch() uint64
	Refresh(ctx context.Context, since uint64) error
}

// Auth errors keep one sentinel per outcome so callers never parse text.
var (
	// ErrAuthDenied is the second 401 within one logical request.
	ErrAuthDenied = errors.New("provider: auth denied")
	// ErrRefreshFailed hides the refresher's cause, which may name a token.
	ErrRefreshFailed = errors.New("provider: credential refresh failed")
)

// unauthorized reports exactly HTTP 401; 403 and every other class are not a
// refresh trigger.
func unauthorized(err error) bool {
	e, ok := errors.AsType[*Error](err)
	return ok && e.HTTPStatus == 401
}

// refreshEpoch returns the signer's generation, 0 when it cannot refresh.
func refreshEpoch(rt *Route) uint64 {
	r, ok := rt.Auth.(TokenRefresher)
	if !ok {
		return 0
	}
	return r.Epoch()
}

// afterAuth refreshes once after a 401 and retries the same body once. The
// refresher's epoch dedupes parallel requests to one refresh.
func (o *OpenAI) afterAuth(ctx context.Context, rt *Route, resp *Response, err error,
	body []byte, est int64, onChunk func(Chunk), drops []CarrierDrop, epoch uint64,
) (Response, error) {
	r, ok := rt.Auth.(TokenRefresher)
	if !unauthorized(err) || !ok {
		return *resp, err
	}
	if rerr := r.Refresh(ctx, epoch); rerr != nil {
		return *resp, &Error{Class: retry.Auth, HTTPStatus: 401, Code: "denied", Err: ErrRefreshFailed}
	}
	retried, rerr := attempt(ctx, o.c, rt, body, est, onChunk)
	retried.CarrierDrops = mergeDrops(drops, retried.CarrierDrops)
	if rerr != nil && unauthorized(rerr) {
		return retried, &Error{Class: retry.Auth, HTTPStatus: 401, Code: "denied", Err: ErrAuthDenied}
	}
	return retried, rerr
}
