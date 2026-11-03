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
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/browser/cdp"
	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// Tool is one deferred catalog entry of the browser module.
type Tool struct {
	Name, Description, Effect string
	Schema                    json.RawMessage
}

// Tools returns the module's deferred tools in stable order: the three reads
// first, then the two approval-gated page actions.
func Tools() []Tool {
	return []Tool{
		newTool("browser_navigate", "Open one allowlisted http(s) URL in the CDP page", "read", `{"type":"object","properties":{"url":{"type":"string","maxLength":2048}},"required":["url"],"additionalProperties":false}`),
		newTool("browser_eval", "Evaluate a JavaScript expression in the current page and return its JSON value", "read", `{"type":"object","properties":{"expression":{"type":"string","maxLength":4096}},"required":["expression"],"additionalProperties":false}`),
		newTool("browser_screenshot", "Capture the current viewport as a PNG", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_click", "Click the current page at viewport coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}`),
		newTool("browser_type", "Insert text into the focused element after approval", "non_idempotent", `{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`),
	}
}

func newTool(name, description, effect, schema string) Tool {
	return Tool{Name: name, Description: description, Effect: effect, Schema: json.RawMessage(schema)}
}

// ModuleConfig is the normalised [modules.browser] section plus the CDP
// endpoint. An enabled module without CDPURL starts but every call fails
// closed with module_unavailable (never a guessed port).
type ModuleConfig struct {
	Enabled      bool
	CDPURL       string
	AllowOrigins []string
	LocalProfile bool
}

// Deps carries the core-owned seam and the transport constructor; both are
// optional in tests.
type Deps struct {
	Approvals host.Approvals
	Dial      DialFunc
}

// DialFunc opens one CDP page transport, so tests can inject a fake.
type DialFunc func(ctx context.Context, rawURL string, timeout time.Duration) (Transport, error)

// Module is the F6 browser module; Start/Status/Invoke/Stop match
// modules.Module. One page connection is shared by every tool call and closed
// by Stop.
type Module struct {
	cfg    ModuleConfig
	deps   Deps
	mu     sync.Mutex
	page   *Page
	conn   Transport
	host   string
	status host.Status
}

// New validates the endpoint shape and the origin allowlist. Validation runs
// even when the module is disabled, so a broken section fails at wiring time.
func New(cfg ModuleConfig, deps Deps) (*Module, error) {
	if cfg.CDPURL != "" {
		if _, err := endpointURL(cfg.CDPURL); err != nil {
			return nil, err
		}
	}
	if _, err := NewAllowlist(cfg.AllowOrigins); err != nil {
		return nil, err
	}
	if deps.Dial == nil {
		deps.Dial = func(ctx context.Context, rawURL string, timeout time.Duration) (Transport, error) {
			conn, err := cdp.Dial(ctx, rawURL, timeout)
			if err != nil {
				return nil, fmt.Errorf("browser: dial: %w", err)
			}
			return conn, nil
		}
	}
	return &Module{cfg: cfg, deps: deps, status: host.StatusStopped}, nil
}

// Start publishes the configured endpoint; it dials on the first call so a
// slow or absent browser never blocks activation (SPEC §3.2.1, 200 ms).
func (m *Module) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("browser: start: %w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.cfg.Enabled {
		m.status = host.StatusStopped
		return nil
	}
	m.status = host.StatusReady
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.status
}

// Stop closes the page connection and is idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.conn != nil {
		err = m.conn.Close()
	}
	m.page, m.conn, m.host, m.status = nil, nil, "", host.StatusStopped
	if err != nil {
		return fmt.Errorf("browser: close: %w", err)
	}
	return nil
}

// Invoke dispatches one browser call. Reads run directly; click and type
// always raise a host approval request first, with a preview that never
// contains page content, and only allow-once permits the action.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || !m.cfg.Enabled || m.cfg.CDPURL == "" {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "browser module is unavailable: " + tool}
	}
	switch tool {
	case "browser_navigate":
		return m.navigate(ctx, args)
	case "browser_eval":
		return m.evaluate(ctx, args)
	case "browser_screenshot":
		return m.screenshot(ctx)
	case "browser_click":
		return m.click(ctx, args, env)
	case "browser_type":
		return m.typeText(ctx, args, env)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown browser tool: " + tool}
}

func (m *Module) navigate(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	final, err := p.Navigate(ctx, a.URL)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	m.host = urlHost(final)
	return host.Result{
		Content: "navigated to " + final, Meta: map[string]any{"url": final},
		UntrustedSource: m.taint(),
	}, nil
}

func (m *Module) evaluate(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		Expression string `json:"expression"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	value, err := p.Eval(ctx, a.Expression)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{Content: value, UntrustedSource: m.taint()}, nil
}

func (m *Module) screenshot(ctx context.Context) (host.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	png, err := p.Screenshot(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{
		Content: fmt.Sprintf("screenshot %d bytes", len(png)), Meta: map[string]any{"bytes": len(png)},
		UntrustedSource: m.taint(),
	}, nil
}

func (m *Module) click(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct{ X, Y float64 }
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	preview := fmt.Sprintf("browser_click at (%.0f, %.0f) in the current page", a.X, a.Y)
	if err := m.approve(ctx, "browser_click", "browser:click", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	if err := p.Click(ctx, a.X, a.Y, true); err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{Content: fmt.Sprintf("clicked at (%.0f, %.0f)", a.X, a.Y), UntrustedSource: m.taint()}, nil
}

func (m *Module) typeText(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Text string `json:"text"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	preview := fmt.Sprintf("browser_type %d characters into the focused element", len(a.Text))
	if err := m.approve(ctx, "browser_type", "browser:type", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	if err := p.Type(ctx, a.Text, true); err != nil {
		return host.Result{}, pageError(err)
	}
	return host.Result{Content: fmt.Sprintf("typed %d characters", len(a.Text)), UntrustedSource: m.taint()}, nil
}

// approve raises one approval request. Only allow-once passes: a session
// approval can never widen a page action (SPEC §18.2, ADR-313).
func (m *Module) approve(ctx context.Context, tool, class, preview string, env host.Env, args json.RawMessage) error {
	req := &host.ApprovalRequest{
		Tool: tool, CallID: env.CallID, Effect: host.EffectNonIdempotent,
		Class: class, Preview: preview, Args: args,
	}
	if err := req.Validate(); err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	approver := m.deps.Approvals
	if approver == nil {
		approver = host.DenyApprovals{}
	}
	answer, err := approver.Approve(ctx, req)
	if err != nil {
		return &host.Error{Code: "denied", Message: err.Error()}
	}
	if answer != host.ApprovalAllowOnce {
		return &host.Error{Code: "denied", Message: "approval denied for " + tool}
	}
	return nil
}

// open returns the shared page, dialing (and for an HTTP endpoint,
// discovering) on first use. The caller holds m.mu.
func (m *Module) open(ctx context.Context) (*Page, error) {
	if m.page != nil {
		return m.page, nil
	}
	rawURL, err := endpointURL(m.cfg.CDPURL)
	if err != nil {
		return nil, err
	}
	if strings.HasPrefix(rawURL, "http://") || strings.HasPrefix(rawURL, "https://") {
		rawURL, err = discover(ctx, rawURL)
		if err != nil {
			return nil, err
		}
	}
	conn, err := m.deps.Dial(ctx, rawURL, cdpCallTimeout)
	if err != nil {
		return nil, fmt.Errorf("browser: dial: %w", err)
	}
	p, err := NewPage(conn, Config{
		AllowOrigins: m.cfg.AllowOrigins, LocalProfile: m.cfg.LocalProfile,
	})
	if err != nil {
		return nil, errors.Join(err, conn.Close())
	}
	m.page, m.conn = p, conn
	return p, nil
}

// taint labels every browser result with the navigated host of the current
// page (SPEC §14.5, ADR-070).
func (m *Module) taint() string {
	host := m.host
	if host == "" {
		host = "page"
	}
	return "browser:" + host
}

func urlHost(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	return strings.ToLower(u.Hostname())
}

const (
	// cdpCallTimeout bounds one dial and one discovery request.
	cdpCallTimeout = 10 * time.Second
	// discoveryLimit bounds the /json/list body.
	discoveryLimit = 1 << 20
)

// endpointURL validates the configured endpoint and returns it verbatim. Only
// a page WebSocket or an HTTP discovery base is a CDP endpoint.
func endpointURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("%w: cdp_url", errBadConfig)
	}
	switch u.Scheme {
	case "ws", "wss", "http", "https":
		return u.String(), nil
	}
	return "", fmt.Errorf("%w: cdp_url scheme %q", errBadConfig, u.Scheme)
}

// discover resolves the first page target of an HTTP DevTools endpoint.
func discover(ctx context.Context, base string) (out string, err error) {
	ctx, cancel := context.WithTimeout(ctx, cdpCallTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimRight(base, "/")+"/json/list", nil)
	if err != nil {
		return "", fmt.Errorf("%w: discovery request", errBadConfig)
	}
	resp, err := netx.LoopbackClient().Do(req)
	if err != nil {
		return "", fmt.Errorf("browser: discovery: %w", err)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("browser: discovery: %w", errPage)
	}
	var targets []struct {
		Type                 string `json:"type"`
		URL                  string `json:"url"`
		WebSocketDebuggerURL string `json:"webSocketDebuggerUrl"`
	}
	dec := json.NewDecoder(io.LimitReader(resp.Body, discoveryLimit))
	if err := dec.Decode(&targets); err != nil {
		return "", fmt.Errorf("%w: discovery decode: %w", errPage, err)
	}
	for _, target := range targets {
		if target.Type != "page" || target.WebSocketDebuggerURL == "" {
			continue
		}
		return endpointURL(target.WebSocketDebuggerURL)
	}
	return "", fmt.Errorf("%w: no page target", errPage)
}

func decodeArgs(raw json.RawMessage, dst any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("%w: %w", errBadConfig, err)
	}
	return nil
}

func invalidArgs(err error) error {
	return &host.Error{Code: "invalid_args", Message: err.Error()}
}
