package computeruse

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

	"github.com/damnbot/damnbot/internal/netx"
)

// httpBackend talks to an optional loopback cua control endpoint:
//
//	POST /start      -> 2xx
//	POST /act        -> {"output": "..."}
//	GET  /screenshot -> image/png
//	POST /stop       -> 2xx
//
// The transport is netx.LoopbackClient, so a non-loopback endpoint is refused
// at dial time and no proxy or public egress is possible.
type httpBackend struct {
	base   string
	client *http.Client
	max    int
}

func newHTTPBackend(cfg *Config) (*httpBackend, error) {
	b := &httpBackend{client: netx.LoopbackClient(), max: cfg.MaxScreenshotBytes}
	if cfg.Endpoint == "" {
		return b, nil
	}
	u, err := url.Parse(cfg.Endpoint)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: endpoint", ErrInvalid)
	}
	b.base = strings.TrimRight(u.String(), "/")
	return b, nil
}

// Start probes the endpoint. No endpoint configured means the module is
// unavailable; there is no fallback to a process backend.
func (b *httpBackend) Start(ctx context.Context) error {
	if b.base == "" {
		return fmt.Errorf("%w: no endpoint configured", ErrUnavailable)
	}
	if _, err := b.call(ctx, http.MethodPost, "/start", nil, 0); err != nil {
		return err
	}
	return nil
}

// Act sends one non-screenshot action as JSON.
func (b *httpBackend) Act(ctx context.Context, a *Action) (Result, error) {
	if a.Kind == ActionScreenshot {
		png, err := b.Screenshot(ctx)
		if err != nil {
			return Result{}, err
		}
		return Result{Image: png}, nil
	}
	body, err := json.Marshal(a)
	if err != nil {
		return Result{}, fmt.Errorf("%w: encode action: %w", ErrInvalid, err)
	}
	out, err := b.call(ctx, http.MethodPost, "/act", body, maxOutput)
	if err != nil {
		return Result{}, err
	}
	if len(bytes.TrimSpace(out)) == 0 {
		return Result{}, nil
	}
	var res struct {
		Output string `json:"output"`
	}
	if uerr := json.Unmarshal(out, &res); uerr != nil {
		return Result{}, fmt.Errorf("%w: decode response: %w", ErrBackend, uerr)
	}
	return Result{Output: res.Output}, nil
}

// Screenshot fetches one PNG bounded by the configured byte limit.
func (b *httpBackend) Screenshot(ctx context.Context) ([]byte, error) {
	png, err := b.call(ctx, http.MethodGet, "/screenshot", nil, b.max)
	if err != nil {
		return nil, err
	}
	if !bytes.HasPrefix(png, []byte("\x89PNG\r\n\x1a\n")) {
		return nil, fmt.Errorf("%w: screenshot is not PNG", ErrBackend)
	}
	return png, nil
}

// Stop asks the endpoint to stop. It is idempotent: an absent endpoint or a
// vanished server is already the unavailable state.
func (b *httpBackend) Stop(ctx context.Context) error {
	if b.base == "" {
		return nil
	}
	if _, err := b.call(ctx, http.MethodPost, "/stop", nil, 0); err != nil {
		if errors.Is(err, ErrUnavailable) {
			return nil
		}
		return err
	}
	return nil
}

// call performs one bounded request. A transport failure is ErrUnavailable;
// an HTTP refusal is ErrInvalid/ErrDenied/ErrUnavailable as appropriate.
func (b *httpBackend) call(ctx context.Context, method, path string, body []byte, limit int) ([]byte, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, rdr)
	if err != nil {
		return nil, fmt.Errorf("%w: request: %w", ErrInvalid, err)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := b.client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("%w: %w", ErrTimeout, ctx.Err())
		}
		return nil, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	if serr := statusError(resp); serr != nil {
		if cerr := resp.Body.Close(); cerr != nil {
			return nil, errors.Join(serr, cerr)
		}
		return nil, serr
	}
	return readBody(resp, limit)
}

func statusError(resp *http.Response) error {
	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		return nil
	case resp.StatusCode == http.StatusBadRequest:
		return fmt.Errorf("%w: endpoint rejected the action", ErrInvalid)
	case resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w: endpoint denied the action", ErrDenied)
	case resp.StatusCode == http.StatusNotFound:
		return fmt.Errorf("%w: endpoint route %s", ErrUnavailable, resp.Request.URL.Path)
	default:
		return fmt.Errorf("%w: endpoint status %d", ErrBackend, resp.StatusCode)
	}
}

func readBody(resp *http.Response, limit int) ([]byte, error) {
	if limit <= 0 {
		if cerr := resp.Body.Close(); cerr != nil {
			return nil, fmt.Errorf("%w: close body: %w", ErrBackend, cerr)
		}
		return nil, nil
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, int64(limit)+1))
	if cerr := resp.Body.Close(); cerr != nil {
		return nil, fmt.Errorf("%w: close body: %w", ErrBackend, cerr)
	}
	if err != nil {
		return nil, fmt.Errorf("%w: read body: %w", ErrBackend, err)
	}
	if len(data) > limit {
		return nil, fmt.Errorf("%w: response %d > %d bytes", ErrTooLarge, len(data), limit)
	}
	return data, nil
}
