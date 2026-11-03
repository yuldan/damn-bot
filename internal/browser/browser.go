// Package browser is the F6 CDP browser module (SPEC §3.2.4): a page
// transport over internal/browser/cdp, a default-deny origin allowlist and
// approval-gated page actions (ADR-313).
package browser

import (
	"cmp"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/browser/cdp"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// Transport is the subset of *Conn the page uses.
type Transport interface {
	Call(ctx context.Context, method string, params any) (json.RawMessage, error)
	Events() <-chan cdp.Event
	Close() error
}

// Config is [modules.browser].
type Config struct {
	AllowOrigins  []string
	LocalProfile  bool
	LoadTimeout   time.Duration
	MaxResult     int
	MaxScreenshot int
}

// Page is one CDP page target and the module-facing browser engine.
type Page struct {
	conn  Transport
	allow *Allowlist
	cfg   Config
}

// NewPage binds the transport and validates the allowlist.
func NewPage(conn Transport, cfg Config) (*Page, error) {
	allow, err := NewAllowlist(cfg.AllowOrigins)
	if err != nil {
		return nil, err
	}
	cfg.LoadTimeout = cmp.Or(cfg.LoadTimeout, 20*time.Second)
	cfg.MaxResult = cmp.Or(cfg.MaxResult, 64<<10)
	cfg.MaxScreenshot = cmp.Or(cfg.MaxScreenshot, 2<<20)
	return &Page{conn: conn, allow: allow, cfg: cfg}, nil
}

// Navigate opens an allowlisted http(s) URL and waits for the load event.
func (p *Page) Navigate(ctx context.Context, rawURL string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", fmt.Errorf("%w: %s", errBadConfig, rawURL)
	}
	if !p.cfg.LocalProfile && localHost(u.Hostname()) {
		return "", fmt.Errorf("%w: %s", errBlockedHost, u.Hostname())
	}
	if !p.allow.set[originOf(u)] {
		return "", fmt.Errorf("%w: %s", errBlockedOrigin, originOf(u))
	}
	raw, err := p.conn.Call(ctx, "Page.navigate", map[string]string{"url": u.String()})
	if err != nil {
		return "", fmt.Errorf("browser: navigate: %w", err)
	}
	var res struct {
		ErrorText string `json:"errorText"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: navigate: %w", errPage, err)
	}
	if res.ErrorText != "" {
		return "", fmt.Errorf("%w: %s", errPage, res.ErrorText)
	}
	if err := p.WaitLoad(ctx); err != nil {
		return "", err
	}
	return u.String(), nil
}

// WaitLoad waits for the next load event, bounded by LoadTimeout.
func (p *Page) WaitLoad(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, p.cfg.LoadTimeout)
	defer cancel()
	for {
		select {
		case ev, ok := <-p.conn.Events():
			if !ok {
				return errClosed
			}
			if ev.Method == "Page.loadEventFired" {
				return nil
			}
			if err := p.target(ev); err != nil {
				if _, serr := p.conn.Call(ctx, "Page.stopLoading", nil); serr != nil {
					return err
				}
				return fmt.Errorf("%w: redirect", errBlockedOrigin)
			}
		case <-ctx.Done():
			return fmt.Errorf("%w: no load event: %w", errTimeout, ctx.Err())
		}
	}
}

func (p *Page) target(ev cdp.Event) error {
	if ev.Method != "Page.frameNavigated" {
		return nil
	}
	var frame struct {
		Frame struct {
			ParentID string `json:"parentId"`
			URL      string `json:"url"`
		} `json:"frame"`
	}
	if err := json.Unmarshal(ev.Params, &frame); err != nil {
		return errPage
	}
	if frame.Frame.ParentID != "" {
		return nil
	}
	u, err := url.Parse(frame.Frame.URL)
	if err != nil || u.Host == "" || !p.allow.set[originOf(u)] {
		return errBlockedOrigin
	}
	return nil
}

// Eval evaluates expression and returns the JSON value, capped at MaxResult.
func (p *Page) Eval(ctx context.Context, expression string) (string, error) {
	raw, err := p.conn.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return "", fmt.Errorf("browser: evaluate: %w", err)
	}
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", fmt.Errorf("%w: evaluate: %w", errPage, err)
	}
	if len(res.Result.Value) == 0 {
		return "null", nil
	}
	if len(res.Result.Value) > p.cfg.MaxResult {
		return "", fmt.Errorf("%w: %d > %d bytes", errTooLarge, len(res.Result.Value), p.cfg.MaxResult)
	}
	return string(res.Result.Value), nil
}

// Screenshot captures the viewport as PNG, capped at MaxScreenshot bytes.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	raw, err := p.conn.Call(ctx, "Page.captureScreenshot", map[string]string{"format": "png"})
	if err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
	}
	var res struct {
		Data string `json:"data"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return nil, errPage
	}
	png, err := base64.StdEncoding.DecodeString(res.Data)
	if err != nil {
		return nil, errPage
	}
	if len(png) > p.cfg.MaxScreenshot {
		return nil, fmt.Errorf("%w: screenshot %d > %d bytes", errTooLarge, len(png), p.cfg.MaxScreenshot)
	}
	return png, nil
}

// Click dispatches a left click; without approval it fails with a preview.
func (p *Page) Click(ctx context.Context, x, y float64, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_click at (%.0f, %.0f)", errApproval, x, y)
	}
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1}
		if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
			return fmt.Errorf("browser: click: %w", err)
		}
	}
	return nil
}

// Type inserts text into the focused element; it is non-read and needs approval.
func (p *Page) Type(ctx context.Context, text string, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_type %d characters", errApproval, len(text))
	}
	if _, err := p.conn.Call(ctx, "Input.insertText", map[string]string{"text": text}); err != nil {
		return fmt.Errorf("browser: type: %w", err)
	}
	return nil
}

var (
	errBadConfig     = errors.New("browser: invalid input")
	errBlockedHost   = errors.New("browser: host is not allowed")
	errBlockedOrigin = errors.New("browser: origin is not allowed")
	errTooLarge      = errors.New("browser: result too large")
	errTimeout       = errors.New("browser: timed out")
	errClosed        = errors.New("browser: page connection closed")
	errPage          = errors.New("browser: protocol failure")
	errApproval      = errors.New("browser: approval required")
)

// Allowlist is a default-deny set of canonical origins.
type Allowlist struct{ set map[string]bool }

// NewAllowlist parses absolute http(s) origins; an empty list denies all.
func NewAllowlist(raw []string) (*Allowlist, error) {
	a := &Allowlist{set: make(map[string]bool, len(raw))}
	for _, entry := range raw {
		origin, err := parseOrigin(entry)
		if err != nil {
			return nil, fmt.Errorf("%w: origin %q", errBadConfig, entry)
		}
		a.set[origin] = true
	}
	return a, nil
}

func parseOrigin(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Path != "" && u.Path != "/") || (u.Scheme != "http" && u.Scheme != "https") {
		return "", errBadConfig
	}
	return originOf(u), nil
}

func originOf(u *url.URL) string {
	h, port := strings.ToLower(u.Hostname()), u.Port()
	if (u.Scheme == "http" && port == "80") || (u.Scheme == "https" && port == "443") {
		port = ""
	}
	if port != "" {
		h = net.JoinHostPort(h, port)
	} else if strings.Contains(h, ":") {
		h = "[" + h + "]"
	}
	return strings.ToLower(u.Scheme) + "://" + h
}

func localHost(host string) bool {
	h := strings.ToLower(host)
	if h == "" || h == "localhost" || strings.HasSuffix(h, ".localhost") {
		return true
	}
	ip, err := netip.ParseAddr(h)
	if err != nil {
		return false
	}
	ip = ip.Unmap()
	return !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified()
}

// Invoke dispatches one approval-gated page action. A nil page or an empty
// allowlist returns module_unavailable.
func (p *Page) Invoke(ctx context.Context, tool string, raw json.RawMessage, env host.Env) (host.Result, error) {
	if p == nil || p.conn == nil || len(p.cfg.AllowOrigins) == 0 {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "browser module is unavailable: " + tool}
	}
	switch tool {
	case "browser_click":
		var a struct{ X, Y float64 }
		if err := json.Unmarshal(raw, &a); err != nil {
			return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
		}
		if err := p.Click(ctx, a.X, a.Y, env.Approved); err != nil {
			return host.Result{}, pageError(err)
		}
		return host.Result{Content: fmt.Sprintf("clicked at (%.0f, %.0f)", a.X, a.Y)}, nil
	case "browser_type":
		var a struct {
			Text string `json:"text"`
		}
		if err := json.Unmarshal(raw, &a); err != nil {
			return host.Result{}, &host.Error{Code: "invalid_args", Message: err.Error()}
		}
		if err := p.Type(ctx, a.Text, env.Approved); err != nil {
			return host.Result{}, pageError(err)
		}
		return host.Result{Content: fmt.Sprintf("typed %d characters", len(a.Text))}, nil
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown browser tool: " + tool}
}

func pageError(err error) error {
	if errors.Is(err, errApproval) {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	return &host.Error{Code: "network", Message: err.Error()}
}
