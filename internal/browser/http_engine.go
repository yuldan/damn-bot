//go:build !minimal

package browser

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// Doer is the request seam of the HTTP engines: *http.Client in production
// (one netx lane per endpoint), a fake in tests.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// httpEngine is the page wire shared by Camoufox and the cloud providers
// (ADR-321): POST /v1/navigate, POST /v1/eval, GET /v1/screenshot,
// POST /v1/act. Camoufox is a loopback sidecar; a cloud provider is a public
// Tool-egress endpoint. The engine performs only raw page operations: the
// page keeps the allowlist, the result bounds and approval.
type httpEngine struct {
	kind   string
	base   string
	client Doer
	cfg    Config
}

var _ Engine = (*httpEngine)(nil)

// newHTTPEngine validates the endpoint shape and requires a client, so a
// selected engine without a lane fails closed instead of dialling directly.
func newHTTPEngine(kind, base string, cfg Config, client Doer) (*httpEngine, error) {
	u, err := url.Parse(strings.TrimSpace(base))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: %s_url", errBadConfig, kind)
	}
	if client == nil {
		return nil, fmt.Errorf("%w: %s client", errUnavailable, kind)
	}
	return &httpEngine{kind: kind, base: strings.TrimRight(u.String(), "/"), client: client, cfg: cfg}, nil
}

// pngMagic is the signature every screenshot reply must carry.
const pngMagic = "\x89PNG\r\n\x1a\n"

// Navigate opens rawURL and re-checks the final URL the provider reports, so
// a redirect or a rewritten target cannot leave the allowlist.
func (e *httpEngine) Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	if err := e.call(ctx, http.MethodPost, "/v1/navigate", map[string]string{"url": rawURL}, &res, e.cfg.MaxResult); err != nil {
		return "", err
	}
	final := res.URL
	if final == "" {
		final = rawURL
	}
	u, err := url.Parse(final)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %s navigate reply", errPage, e.kind)
	}
	if err := guard(u); err != nil {
		return "", err
	}
	return final, nil
}

// WaitLoad is synchronous: the wire replies after the page has loaded.
func (e *httpEngine) WaitLoad(context.Context) error { return nil }

// Eval returns the JSON value of one expression; an absent value is null.
func (e *httpEngine) Eval(ctx context.Context, expression string) (json.RawMessage, error) {
	var res struct {
		Value json.RawMessage `json:"value"`
	}
	if err := e.call(ctx, http.MethodPost, "/v1/eval", map[string]string{"expression": expression}, &res, e.cfg.MaxResult); err != nil {
		return nil, err
	}
	if len(bytes.TrimSpace(res.Value)) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Value, nil
}

// Screenshot captures the viewport as PNG, bounded by MaxScreenshot.
func (e *httpEngine) Screenshot(ctx context.Context) ([]byte, error) {
	png, err := e.do(ctx, http.MethodGet, "/v1/screenshot", nil, e.cfg.MaxScreenshot)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(png, []byte(pngMagic)) {
		return nil, fmt.Errorf("%w: %s screenshot is not PNG", errPage, e.kind)
	}
	return png, nil
}

// Act sends one input action. The wire knows click and type; every other kind
// is unsupported rather than silently dropped.
func (e *httpEngine) Act(ctx context.Context, a Action) error {
	switch a.Kind {
	case "click", "type":
		return e.call(ctx, http.MethodPost, "/v1/act", a, nil, 0)
	}
	return fmt.Errorf("%w: %s %q", errUnsupported, e.kind, a.Kind)
}

// Close is a no-op: the provider owns the page and the shared client.
func (e *httpEngine) Close() error { return nil }

// call marshals body, performs one bounded request and decodes the reply.
func (e *httpEngine) call(ctx context.Context, method, path string, body, dst any, limit int) error {
	var data []byte
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: %s encode: %w", errBadConfig, e.kind, err)
		}
		data = enc
	}
	out, err := e.do(ctx, method, path, data, limit)
	if err != nil {
		return err
	}
	if dst == nil || limit <= 0 {
		return nil
	}
	if err := json.Unmarshal(out, dst); err != nil {
		return fmt.Errorf("%w: %s decode: %w", errPage, e.kind, err)
	}
	return nil
}

// do performs one bounded request and maps refusals onto the ADR-321 classes.
func (e *httpEngine) do(ctx context.Context, method, path string, body []byte, limit int) (out []byte, err error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, e.base+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("%w: %s request: %w", errBadConfig, e.kind, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := e.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %s: %w", errTimeout, e.kind, ctx.Err())
		}
		return nil, fmt.Errorf("browser: %s: %w", e.kind, err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if serr := e.statusError(resp); serr != nil {
		return nil, serr
	}
	if limit <= 0 {
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if err != nil {
		return nil, fmt.Errorf("%w: %s read: %w", errPage, e.kind, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: %s reply %d > %d bytes", errTooLarge, e.kind, len(data), limit)
	}
	return data, nil
}

// statusError maps one provider status onto the typed error classes.
func (e *httpEngine) statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w: %s rejected the request", errBadConfig, e.kind)
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: %s denied the request", errDenied, e.kind)
	case resp.StatusCode == http.StatusNotFound, resp.StatusCode == http.StatusTooManyRequests:
		return fmt.Errorf("%w: %s route %s", errUnavailable, e.kind, resp.Request.URL.Path)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w: %s status %d", errPage, e.kind, resp.StatusCode)
	default:
		return fmt.Errorf("%w: %s status %d", errBadConfig, e.kind, resp.StatusCode)
	}
}
