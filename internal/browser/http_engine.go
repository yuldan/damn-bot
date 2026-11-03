package browser

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

// httpEngine is the page wire shared by camofox and cloud providers (ADR-321):
// POST /v1/navigate, POST /v1/eval, GET /v1/screenshot, POST /v1/act.
// Camoufox is a loopback sidecar; a cloud provider is a public Tool-egress
// endpoint.
type httpEngine struct {
	kind   string
	base   string
	client Doer
	cfg    Config
}

// Doer is the request seam: *http.Client in tests, netx in production.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

func newHTTPEngine(kind, base string, cfg Config, client Doer) (*httpEngine, error) {
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: %s_url", errBadConfig, kind)
	}
	if client == nil {
		return nil, fmt.Errorf("%w: %s client", errUnavailable, kind)
	}
	return &httpEngine{kind: kind, base: strings.TrimRight(u.String(), "/"), client: client, cfg: cfg}, nil
}

// Navigate opens rawURL and guards the reported final URL.
func (e *httpEngine) Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error) {
	var res struct {
		URL string `json:"url"`
	}
	if err := e.call(ctx, "/v1/navigate", map[string]string{"url": rawURL}, &res, 64<<10); err != nil {
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

// WaitLoad is synchronous: Navigate returns after load.
func (e *httpEngine) WaitLoad(context.Context) error { return nil }

// Read dispatches one page read over the provider wire.
func (e *httpEngine) Read(ctx context.Context, op ReadOp) (Reply, error) {
	switch op.Kind {
	case "eval":
		var res struct {
			Value json.RawMessage `json:"value"`
		}
		if err := e.call(ctx, "/v1/eval", map[string]string{"expression": op.Expression}, &res, e.cfg.MaxResult); err != nil {
			return Reply{}, err
		}
		if len(res.Value) == 0 {
			res.Value = json.RawMessage("null")
		}
		return Reply{Value: res.Value}, nil
	case "screenshot":
		png, err := e.do(ctx, http.MethodGet, "/v1/screenshot", nil, e.cfg.MaxScreenshot)
		if err != nil {
			return Reply{}, err
		}
		if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
			return Reply{}, fmt.Errorf("%w: %s screenshot is not PNG", errPage, e.kind)
		}
		return Reply{Blob: png}, nil
	case "pdf", "snapshot", "back", "download":
		return e.callRead(ctx, op)
	}
	return Reply{}, fmt.Errorf("%w: %s", errBadConfig, op.Kind)
}

func (e *httpEngine) callRead(ctx context.Context, op ReadOp) (Reply, error) {
	var res struct {
		Value json.RawMessage `json:"value"`
		Data  string          `json:"data"`
		Name  string          `json:"name"`
	}
	limit := op.Limit
	if limit <= 0 {
		limit = e.cfg.MaxResult
	}
	if err := e.call(ctx, "/v1/read", op, &res, limit); err != nil {
		return Reply{}, err
	}
	reply := Reply{Value: res.Value, Name: res.Name}
	if res.Data != "" {
		blob, err := base64.StdEncoding.DecodeString(res.Data)
		if err != nil {
			return Reply{}, fmt.Errorf("%w: %s read data", errPage, e.kind)
		}
		if op.Limit > 0 && len(blob) > op.Limit {
			return Reply{}, fmt.Errorf("%w: %s read %d > %d bytes", errTooLarge, e.kind, len(blob), op.Limit)
		}
		reply.Blob = blob
	}
	if op.Kind == "back" {
		if err := e.guardBack(op, res.Name); err != nil {
			return Reply{}, err
		}
	}
	return reply, nil
}

func (e *httpEngine) guardBack(op ReadOp, raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return fmt.Errorf("%w: %s back url", errBadConfig, e.kind)
	}
	if op.Guard == nil {
		return nil
	}
	if err := op.Guard(u); err != nil {
		return fmt.Errorf("browser: %s back: %w", e.kind, err)
	}
	return nil
}

// Act sends one input action.
func (e *httpEngine) Act(ctx context.Context, a *Action) error {
	switch a.Kind {
	case "click", "type", "hover", "drag", "scroll", "press":
		return e.call(ctx, "/v1/act", a, nil, 0)
	}
	return fmt.Errorf("%w: %s", errBadConfig, a.Kind)
}

func (e *httpEngine) Close() error { return nil }

func (e *httpEngine) call(ctx context.Context, path string, body, dst any, limit int) error {
	var data []byte
	if body != nil {
		enc, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("%w: %s encode: %w", errBadConfig, e.kind, err)
		}
		data = enc
	}
	out, err := e.do(ctx, http.MethodPost, path, data, limit)
	if err != nil {
		return err
	}
	if dst == nil {
		return nil
	}
	if err := json.Unmarshal(out, dst); err != nil {
		return fmt.Errorf("%w: %s decode: %w", errPage, e.kind, err)
	}
	return nil
}

// do performs one bounded request and maps refusals onto the typed classes.
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
	default:
		if resp.StatusCode >= 500 {
			return fmt.Errorf("%w: %s status %d", errPage, e.kind, resp.StatusCode)
		}
		return fmt.Errorf("%w: %s status %d", errBadConfig, e.kind, resp.StatusCode)
	}
}
