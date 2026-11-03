//go:build !minimal

package netx

import (
	"context"
	"net/http"
)

// authHeadersKey marks request headers installed by the runtime itself.
type authHeadersKey struct{}

// WithAuthHeaders exempts the named request headers from the Tool DLP scan for
// this request only. The caller must set the values from its own credential
// source; model-supplied arguments never reach this path (GAP-SEC-16).
func WithAuthHeaders(ctx context.Context, names ...string) context.Context {
	if len(names) == 0 {
		return ctx
	}
	set := make(map[string]struct{}, len(names))
	for _, name := range names {
		set[http.CanonicalHeaderKey(name)] = struct{}{}
	}
	return context.WithValue(ctx, authHeadersKey{}, set)
}

func authHeaders(ctx context.Context) map[string]struct{} {
	set, ok := ctx.Value(authHeadersKey{}).(map[string]struct{})
	if !ok {
		return nil
	}
	return set
}
