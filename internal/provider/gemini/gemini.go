//go:build !minimal

// Package gemini is the Gemini native (generateContent) wire adapter (F5-06).
// It only translates a request and a 2xx event stream; retry, circuit, budget,
// fallback, and carrier projection/capture stay in provider.OpenAI over the
// injected netx client (E-05). The package is excluded from the minimal build
// (P13).
package gemini

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
)

// New returns the Gemini wire adapter. It holds no state and never opens a
// connection.
func New() provider.Wire { return wire{} }

type wire struct{}

// Encode builds the :streamGenerateContent request body.
func (wire) Encode(rt *provider.Route, r *provider.Request, drop map[string]bool) ([]byte, []provider.Carrier, []provider.CarrierDrop, error) {
	return encode(rt, r, drop)
}

// Path is the model-scoped endpoint appended to Route.BaseURL, which holds
// the API root (for example https://generativelanguage.googleapis.com/v1beta).
func (wire) Path(rt *provider.Route) string {
	return "/models/" + url.PathEscape(rt.Model) + ":streamGenerateContent?alt=sse"
}

// Sign applies the native transport auth: x-goog-api-key instead of a bearer
// token. A Route.Auth signer (Google JWT/OAuth) takes precedence for
// Vertex-style gateways.
func (wire) Sign(ctx context.Context, rt *provider.Route, req *http.Request, body []byte) error {
	if rt.Auth != nil {
		return fmt.Errorf("gemini auth: %w", rt.Auth.Sign(ctx, req, body))
	}
	if rt.APIKey != "" {
		req.Header.Set("x-goog-api-key", rt.APIKey)
	}
	return nil
}

// Read decodes a 2xx generateContent event stream.
func (wire) Read(st *netx.Stream, start time.Time, onChunk func(provider.Chunk)) (provider.Response, error) {
	return readStream(st, start, onChunk)
}
