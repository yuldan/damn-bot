//go:build !minimal

// Package anthropic is the Anthropic Messages wire adapter (F5-05). It only
// translates a request and a 2xx event stream; retry, circuit, budget,
// fallback, and carrier projection/capture stay in provider.OpenAI over the
// injected netx client (E-05). The package is excluded from the minimal
// build (P13).
package anthropic

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
)

// Version is the Anthropic API version header of every request.
const Version = "2023-06-01"

// extendedCacheBeta enables the 1h cache_control TTL on endpoints that still
// gate it behind a beta header.
const extendedCacheBeta = "extended-cache-ttl-2025-04-11"

// New returns the Messages wire adapter. It holds no state and never opens a
// connection.
func New() provider.Wire { return wire{} }

type wire struct{}

// Encode builds the /v1/messages request body.
func (wire) Encode(rt *provider.Route, r *provider.Request, drop map[string]bool) ([]byte, []provider.Carrier, []provider.CarrierDrop, error) {
	return encode(rt, r, drop)
}

// Path is the Messages endpoint appended to Route.BaseURL.
func (wire) Path(*provider.Route) string { return "/v1/messages" }

// Sign applies the Messages transport auth: x-api-key instead of a bearer
// token, plus the API version. A Route.Auth signer (SigV4/JWT/OAuth) takes
// precedence for gateways that need it.
func (wire) Sign(ctx context.Context, rt *provider.Route, req *http.Request, body []byte) error {
	req.Header.Set("anthropic-version", Version)
	if bytes.Contains(body, []byte(`"ttl":"1h"`)) {
		req.Header.Set("anthropic-beta", extendedCacheBeta)
	}
	if rt.Auth != nil {
		return fmt.Errorf("anthropic auth: %w", rt.Auth.Sign(ctx, req, body))
	}
	if rt.APIKey != "" {
		req.Header.Set("x-api-key", rt.APIKey)
	}
	return nil
}

// Read decodes a 2xx Messages event stream.
func (wire) Read(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	return readStream(st, start, onChunk)
}
