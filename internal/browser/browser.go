// Package browser is the F6 browser module (SPEC §3.2.4): an engine seam over
// CDP, Camoufox, Lightpanda and cloud HTTP providers, a default-deny origin
// allowlist and approval-gated page actions (ADR-313, ADR-321).
package browser

import (
	"cmp"
	"context"
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

// Transport is the subset of *cdp.Conn the CDP engine uses.
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

func (c Config) normalize() Config {
	c.LoadTimeout = cmp.Or(c.LoadTimeout, 20*time.Second)
	c.MaxResult = cmp.Or(c.MaxResult, 64<<10)
	c.MaxScreenshot = cmp.Or(c.MaxScreenshot, 2<<20)
	return c
}

// Page is one bound engine plus the module policy: allowlist, bounds, approval.
type Page struct {
	eng   Engine
	allow *Allowlist
	cfg   Config
}

// NewPage binds an engine and validates the allowlist.
func NewPage(eng Engine, cfg Config) (*Page, error) {
	allow, err := NewAllowlist(cfg.AllowOrigins)
	if err != nil {
		return nil, err
	}
	return &Page{eng: eng, allow: allow, cfg: cfg.normalize()}, nil
}

// NewCDPPage binds a CDP transport to the page policy.
func NewCDPPage(conn Transport, cfg Config) (*Page, error) {
	cfg = cfg.normalize()
	return NewPage(newCDPEngine(conn, cfg), cfg)
}

// Navigate opens an allowlisted http(s) URL and returns the final URL. The
// guard runs before the call and on every redirect the engine reports.
func (p *Page) Navigate(ctx context.Context, rawURL string) (string, error) {
	u, err := p.checkURL(rawURL)
	if err != nil {
		return "", err
	}
	final, err := p.eng.Navigate(ctx, u.String(), p.guard)
	if err != nil {
		return "", fmt.Errorf("browser: navigate: %w", err)
	}
	if _, err := p.checkURL(final); err != nil {
		return "", err
	}
	return final, nil
}

// guard adapts checkURL to the engine callback.
func (p *Page) guard(u *url.URL) error {
	_, err := p.checkURL(u.String())
	return err
}

// checkURL is the default-deny policy shared by the page and its engines.
func (p *Page) checkURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return nil, fmt.Errorf("%w: %s", errBadConfig, raw)
	}
	if !p.cfg.LocalProfile && localHost(u.Hostname()) {
		return nil, fmt.Errorf("%w: %s", errBlockedHost, u.Hostname())
	}
	if !p.allow.set[originOf(u)] {
		return nil, fmt.Errorf("%w: %s", errBlockedOrigin, originOf(u))
	}
	return u, nil
}

// WaitLoad waits for the next load event when the engine reports them; the
// synchronous engines have nothing to wait for. Bounded by LoadTimeout.
func (p *Page) WaitLoad(ctx context.Context) error {
	waiter, ok := p.eng.(interface{ WaitLoad(context.Context) error })
	if !ok {
		return nil
	}
	if err := waiter.WaitLoad(ctx); err != nil {
		return fmt.Errorf("browser: wait load: %w", err)
	}
	return nil
}

// Eval evaluates expression and returns the JSON value, capped at MaxResult.
func (p *Page) Eval(ctx context.Context, expression string) (string, error) {
	value, err := p.eng.Eval(ctx, expression)
	if err != nil {
		return "", fmt.Errorf("browser: evaluate: %w", err)
	}
	if len(value) > p.cfg.MaxResult {
		return "", fmt.Errorf("%w: %d > %d bytes", errTooLarge, len(value), p.cfg.MaxResult)
	}
	return string(value), nil
}

// Screenshot captures the viewport as PNG, capped at MaxScreenshot bytes.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	png, err := p.eng.Screenshot(ctx)
	if err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
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
	if err := p.eng.Act(ctx, Action{Kind: "click", X: x, Y: y}); err != nil {
		return fmt.Errorf("browser: click: %w", err)
	}
	return nil
}

// Type inserts text into the focused element; it is non-read and needs approval.
func (p *Page) Type(ctx context.Context, text string, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_type %d characters", errApproval, len(text))
	}
	if err := p.eng.Act(ctx, Action{Kind: "type", Text: text}); err != nil {
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
	errDenied        = errors.New("browser: engine denied the request")
	errUnavailable   = errors.New("browser: engine unavailable")
	errUnsupported   = errors.New("browser: engine does not support the operation")
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
	if p == nil || p.eng == nil || len(p.cfg.AllowOrigins) == 0 {
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
	if _, ok := errors.AsType[*host.Error](err); ok {
		return err
	}
	code := "network"
	switch {
	case errors.Is(err, errApproval), errors.Is(err, errDenied):
		code = "denied"
	case errors.Is(err, errBadConfig):
		code = "invalid_args"
	case errors.Is(err, errBlockedHost), errors.Is(err, errBlockedOrigin):
		code = "blocked_host"
	case errors.Is(err, errTooLarge):
		code = "too_large"
	case errors.Is(err, errTimeout):
		code = "timeout"
	case errors.Is(err, errUnavailable), errors.Is(err, errUnsupported):
		code = "module_unavailable"
	}
	return &host.Error{Code: code, Message: err.Error()}
}
