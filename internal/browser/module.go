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
	"path/filepath"
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
		newTool("browser_snapshot", "Return the accessibility tree with stable element refs as JSON", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_console", "Read the buffered page console log; bounded, never evaluates page script", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_get_images", "List the images of the current page with their natural size", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_click", "Click the current page at viewport coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"integer"},"y":{"type":"integer"}},"required":["x","y"],"additionalProperties":false}`),
		newTool("browser_type", "Insert text into the focused element after approval", "non_idempotent", `{"type":"object","properties":{"text":{"type":"string","maxLength":4096}},"required":["text"],"additionalProperties":false}`),
		newTool("browser_back", "Go back one history entry after approval", "non_idempotent", `{"type":"object","properties":{},"additionalProperties":false}`),
		newTool("browser_scroll", "Scroll the page at coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"},"delta_x":{"type":"number","minimum":-10000,"maximum":10000},"delta_y":{"type":"number","minimum":-10000,"maximum":10000}},"required":["delta_x","delta_y"],"additionalProperties":false}`),
		newTool("browser_hover", "Move the pointer to viewport coordinates after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"}},"required":["x","y"],"additionalProperties":false}`),
		newTool("browser_drag", "Drag from one viewport point to another after approval", "non_idempotent", `{"type":"object","properties":{"x":{"type":"number"},"y":{"type":"number"},"end_x":{"type":"number"},"end_y":{"type":"number"}},"required":["x","y","end_x","end_y"],"additionalProperties":false}`),
		newTool("browser_press", "Press one keyboard key after approval", "non_idempotent", `{"type":"object","properties":{"key":{"type":"string","maxLength":32}},"required":["key"],"additionalProperties":false}`),
		newTool("browser_select", "Set a control value by selector after approval", "non_idempotent", `{"type":"object","properties":{"selector":{"type":"string","maxLength":1024},"value":{"type":"string","maxLength":4096}},"required":["selector","value"],"additionalProperties":false}`),
		newTool("browser_upload", "Set a file input from a workspace file after approval", "non_idempotent", `{"type":"object","properties":{"selector":{"type":"string","maxLength":1024},"path":{"type":"string","maxLength":1024}},"required":["selector","path"],"additionalProperties":false}`),
		newTool("browser_download", "Download an allowlisted URL into a workspace file after approval", "non_idempotent", `{"type":"object","properties":{"url":{"type":"string","maxLength":2048},"path":{"type":"string","maxLength":1024}},"required":["url","path"],"additionalProperties":false}`),
		newTool("browser_wait_for", "Wait until a JavaScript expression is truthy or the timeout expires", "read", `{"type":"object","properties":{"expression":{"type":"string","maxLength":4096},"timeout_ms":{"type":"integer","minimum":0,"maximum":30000}},"required":["expression"],"additionalProperties":false}`),
		newTool("browser_pdf", "Render the current page as PDF", "read", `{"type":"object","properties":{},"additionalProperties":false}`),
	}
}

func newTool(name, description, effect, schema string) Tool {
	return Tool{Name: name, Description: description, Effect: effect, Schema: json.RawMessage(schema)}
}

// ModuleConfig is the normalised [modules.browser] section plus the engine
// endpoints. A selected engine without an endpoint fails closed at Invoke;
// there is never a guessed port or a fallback to another engine.
type ModuleConfig struct {
	Enabled       bool
	Engine        string
	CDPURL        string
	CamofoxURL    string
	LightpandaBin string
	CloudURL      string
	AllowOrigins  []string
	LocalProfile  bool
	Offline       bool
	NoBackground  bool
}

// Deps carries the core-owned seams; every field is optional in tests.
type Deps struct {
	Approvals host.Approvals
	Dial      DialFunc
	Run       RunFunc
	HTTP      *http.Client
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
	host   string
	status host.Status
}

// New validates the engine selection, endpoint shapes and allowlist, even when
// disabled; an empty endpoint stays legal and fails closed at Invoke.
func New(cfg *ModuleConfig, deps Deps) (*Module, error) {
	if cfg == nil {
		return nil, fmt.Errorf("%w: nil config", errBadConfig)
	}
	if err := validateEngineConfig(cfg); err != nil {
		return nil, err
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
	return &Module{cfg: *cfg, deps: deps, status: host.StatusStopped}, nil
}

// validateEngineConfig keeps the engine and every endpoint honest. More than
// one endpoint without an explicit engine is ambiguous, not picked silently.
func validateEngineConfig(cfg *ModuleConfig) error {
	switch cfg.Engine {
	case "", "cdp", "camofox", "lightpanda", "cloud":
	default:
		return fmt.Errorf("%w: engine %q", errBadConfig, cfg.Engine)
	}
	if err := validateEndpoints(cfg); err != nil {
		return err
	}
	if cfg.Engine == "" {
		n := 0
		for _, on := range []bool{cfg.CDPURL != "", cfg.CamofoxURL != "", cfg.LightpandaBin != "", cfg.CloudURL != ""} {
			if on {
				n++
			}
		}
		if n > 1 {
			return fmt.Errorf("%w: several browser endpoints configured without engine", errBadConfig)
		}
	}
	return nil
}

func validateEndpoints(cfg *ModuleConfig) error {
	if cfg.CDPURL != "" {
		raw, err := endpointURL(cfg.CDPURL)
		if err != nil {
			return err
		}
		u, perr := url.Parse(raw)
		if perr != nil || !localHost(u.Hostname()) {
			return fmt.Errorf("%w: cdp_url must be loopback", errBadConfig)
		}
	}
	if cfg.CamofoxURL != "" {
		if err := httpEndpoint(cfg.CamofoxURL, true); err != nil {
			return fmt.Errorf("%w: camofox_url", err)
		}
	}
	if cfg.CloudURL != "" {
		if err := httpEndpoint(cfg.CloudURL, false); err != nil {
			return fmt.Errorf("%w: cloud_url", err)
		}
	}
	if cfg.LightpandaBin != "" && !filepath.IsAbs(cfg.LightpandaBin) {
		return fmt.Errorf("%w: lightpanda_bin must be absolute", errBadConfig)
	}
	return nil
}

// httpEndpoint accepts http(s); loopback sidecars must be local and a public
// cloud endpoint must use TLS.
func httpEndpoint(raw string, loopback bool) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" ||
		(u.Scheme != "http" && u.Scheme != "https") {
		return errBadConfig
	}
	local := localHost(u.Hostname())
	if loopback && !local {
		return errBadConfig
	}
	if !loopback && !local && u.Scheme != "https" {
		return errBadConfig
	}
	return nil
}

// engineName: an explicit engine wins, otherwise the single endpoint selects
// the backend; empty means no engine.
func (m *Module) engineName() string {
	if m.cfg.Engine != "" {
		return m.cfg.Engine
	}
	switch {
	case m.cfg.CDPURL != "":
		return "cdp"
	case m.cfg.CamofoxURL != "":
		return "camofox"
	case m.cfg.LightpandaBin != "":
		return "lightpanda"
	case m.cfg.CloudURL != "":
		return "cloud"
	}
	return ""
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

// Stop closes the engine and is idempotent.
func (m *Module) Stop(context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var err error
	if m.page != nil {
		err = m.page.Close()
	}
	m.page, m.host, m.status = nil, "", host.StatusStopped
	if err != nil {
		return fmt.Errorf("browser: close: %w", err)
	}
	return nil
}

// Invoke dispatches one browser call. Reads run directly; click and type
// always raise a host approval request first, with a preview that never
// contains page content, and only allow-once permits the action.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m == nil || !m.cfg.Enabled || m.engineName() == "" {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "browser module is unavailable: " + tool}
	}
	if res, ok, err := m.invokeRead(ctx, tool, args); ok {
		return res, err
	}
	return m.invokeAction(ctx, tool, args, env)
}

func (m *Module) invokeRead(ctx context.Context, tool string, args json.RawMessage) (host.Result, bool, error) {
	switch tool {
	case "browser_navigate":
		res, err := m.navigate(ctx, args)
		return res, true, err
	case "browser_eval":
		res, err := m.evaluate(ctx, args)
		return res, true, err
	case "browser_screenshot":
		res, err := m.screenshot(ctx)
		return res, true, err
	case "browser_console":
		res, err := m.console(ctx)
		return res, true, err
	case "browser_get_images":
		res, err := m.getImages(ctx)
		return res, true, err
	case "browser_snapshot":
		res, err := m.snapshot(ctx)
		return res, true, err
	case "browser_wait_for":
		res, err := m.waitFor(ctx, args)
		return res, true, err
	case "browser_pdf":
		res, err := m.pdf(ctx)
		return res, true, err
	}
	return host.Result{}, false, nil
}

func (m *Module) invokeAction(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	switch tool {
	case "browser_click":
		return m.click(ctx, args, env)
	case "browser_type":
		return m.typeText(ctx, args, env)
	case "browser_back":
		return m.back(ctx, env)
	case "browser_hover", "browser_drag", "browser_scroll", "browser_press", "browser_select":
		return m.action(ctx, tool, args, env)
	case "browser_upload":
		return m.upload(ctx, args, env)
	case "browser_download":
		return m.download(ctx, args, env)
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
func (m *Module) withPage(ctx context.Context, fn func(*Page) (host.Result, error)) (host.Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	res, err := fn(p)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	res.UntrustedSource = m.taint()
	return res, nil
}

func (m *Module) snapshot(ctx context.Context) (host.Result, error) {
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		reply, err := p.read(ctx, ReadOp{Kind: "snapshot", Limit: p.cfg.MaxResult})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if len(reply.Value) > p.cfg.MaxResult {
			return host.Result{}, fmt.Errorf("%w: snapshot %d > %d bytes", errTooLarge, len(reply.Value), p.cfg.MaxResult)
		}
		return host.Result{Content: string(reply.Value)}, nil
	})
}

func (m *Module) action(ctx context.Context, tool string, raw json.RawMessage, env host.Env) (host.Result, error) {
	a, preview, err := decodeAction(tool, raw)
	if err != nil {
		return host.Result{}, invalidArgs(err)
	}
	class := "browser:" + strings.TrimPrefix(tool, "browser_")
	if aerr := m.approve(ctx, tool, class, preview, env, raw); aerr != nil {
		return host.Result{}, aerr
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		if err := p.Run(ctx, tool, a, true); err != nil {
			return host.Result{}, err
		}
		content := preview
		switch tool {
		case "browser_click":
			content = fmt.Sprintf("clicked at (%.0f, %.0f)", a.X, a.Y)
		case "browser_type":
			content = fmt.Sprintf("typed %d characters", len(a.Text))
		}
		return host.Result{Content: content}, nil
	})
}

func decodeAction(tool string, raw json.RawMessage) (*Action, string, error) {
	switch tool {
	case "browser_click", "browser_type", "browser_hover", "browser_drag":
		return decodePointer(tool, raw)
	case "browser_scroll", "browser_press", "browser_select":
		return decodeInput(tool, raw)
	}
	return nil, "", fmt.Errorf("%w: %s", errBadConfig, tool)
}

func decodePointer(tool string, raw json.RawMessage) (*Action, string, error) {
	var a struct {
		X    float64 `json:"x"`
		Y    float64 `json:"y"`
		EndX float64 `json:"end_x"`
		EndY float64 `json:"end_y"`
		Text string  `json:"text"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, "", err
	}
	switch tool {
	case "browser_click", "browser_hover":
		return &Action{X: a.X, Y: a.Y}, fmt.Sprintf("%s at (%.0f, %.0f)", tool, a.X, a.Y), nil
	case "browser_type":
		return &Action{Text: a.Text}, fmt.Sprintf("browser_type %d characters", len(a.Text)), nil
	}
	return &Action{X: a.X, Y: a.Y, EndX: a.EndX, EndY: a.EndY},
		fmt.Sprintf("browser_drag (%.0f,%.0f)->(%.0f,%.0f)", a.X, a.Y, a.EndX, a.EndY), nil
}

func decodeInput(tool string, raw json.RawMessage) (*Action, string, error) {
	var a struct {
		X        float64 `json:"x"`
		Y        float64 `json:"y"`
		DeltaX   float64 `json:"delta_x"`
		DeltaY   float64 `json:"delta_y"`
		Key      string  `json:"key"`
		Selector string  `json:"selector"`
		Value    string  `json:"value"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return nil, "", err
	}
	switch tool {
	case "browser_scroll":
		if a.DeltaX < -10000 || a.DeltaX > 10000 || a.DeltaY < -10000 || a.DeltaY > 10000 {
			return nil, "", fmt.Errorf("%w: scroll deltas", errBadConfig)
		}
		return &Action{X: a.X, Y: a.Y, DeltaX: a.DeltaX, DeltaY: a.DeltaY},
			fmt.Sprintf("browser_scroll (%.0f,%.0f)", a.DeltaX, a.DeltaY), nil
	case "browser_press":
		if !knownKey(a.Key) {
			return nil, "", fmt.Errorf("%w: key %q", errBadConfig, a.Key)
		}
		return &Action{Key: a.Key}, "browser_press " + a.Key, nil
	}
	return &Action{Selector: a.Selector, Value: a.Value}, "browser_select " + a.Selector, nil
}

func (m *Module) back(ctx context.Context, env host.Env) (host.Result, error) {
	if err := m.approve(ctx, "browser_back", "browser:back", "browser_back one history entry", env, nil); err != nil {
		return host.Result{}, err
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		final, err := p.Back(ctx)
		if err != nil {
			return host.Result{}, err
		}
		m.host = urlHost(final)
		return host.Result{Content: "back to " + final, Meta: map[string]any{"url": final}}, nil
	})
}

func (m *Module) waitFor(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		Expression string `json:"expression"`
		TimeoutMS  int    `json:"timeout_ms"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		if err := p.WaitFor(ctx, a.Expression, time.Duration(a.TimeoutMS)*time.Millisecond); err != nil {
			return host.Result{}, err
		}
		return host.Result{Content: "condition met"}, nil
	})
}

func (m *Module) pdf(ctx context.Context) (host.Result, error) {
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		reply, err := p.read(ctx, ReadOp{Kind: "pdf", Limit: p.cfg.MaxBlob})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if len(reply.Blob) > p.cfg.MaxBlob {
			return host.Result{}, fmt.Errorf("%w: pdf %d > %d bytes", errTooLarge, len(reply.Blob), p.cfg.MaxBlob)
		}
		return host.Result{Content: fmt.Sprintf("pdf %d bytes", len(reply.Blob)), Meta: map[string]any{"bytes": len(reply.Blob)}}, nil
	})
}

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

// open returns the shared page, constructing the engine on first use (caller
// holds m.mu). A missing endpoint fails with module_unavailable; no fallback.
func (m *Module) open(ctx context.Context) (*Page, error) {
	if m.page != nil {
		return m.page, nil
	}
	cfg := Config{AllowOrigins: m.cfg.AllowOrigins, LocalProfile: m.cfg.LocalProfile}.normalize()
	if m.engineName() == "cdp" {
		if m.cfg.CDPURL == "" {
			return nil, fmt.Errorf("%w: cdp_url is empty", errUnavailable)
		}
		conn, err := m.dialCDP(ctx)
		if err != nil {
			return nil, err
		}
		p, err := NewCDPPage(conn, cfg)
		if err != nil {
			return nil, errors.Join(err, conn.Close())
		}
		m.page = p
		return p, nil
	}
	eng, err := m.newEngine(cfg)
	if err != nil {
		return nil, err
	}
	p, err := NewPage(eng, cfg)
	if err != nil {
		return nil, errors.Join(err, eng.Close())
	}
	m.page = p
	return p, nil
}

func (m *Module) newEngine(cfg Config) (Engine, error) {
	switch name := m.engineName(); name {
	case "":
		return nil, fmt.Errorf("%w: no browser engine configured", errUnavailable)
	case "camofox":
		if m.cfg.CamofoxURL == "" {
			return nil, fmt.Errorf("%w: camofox_url is empty", errUnavailable)
		}
		return newHTTPEngine(name, m.cfg.CamofoxURL, cfg, m.httpDoer(netx.LoopbackClient()))
	case "cloud":
		if m.cfg.CloudURL == "" {
			return nil, fmt.Errorf("%w: cloud_url is empty", errUnavailable)
		}
		return newHTTPEngine(name, m.cfg.CloudURL, cfg, m.httpDoer(m.toolClient(m.cfg.CloudURL)))
	case "lightpanda":
		if m.cfg.LightpandaBin == "" {
			return nil, fmt.Errorf("%w: lightpanda_bin is empty", errUnavailable)
		}
		return newCLIEngine(m.cfg.LightpandaBin, cfg, m.deps.Run)
	}
	return nil, fmt.Errorf("%w: engine %q", errBadConfig, m.engineName())
}

// httpDoer prefers the injected test client over the production lane.
func (m *Module) httpDoer(fallback Doer) Doer {
	if m.deps.HTTP != nil {
		return m.deps.HTTP
	}
	return fallback
}

// toolClient is the Tool-egress lane; a loopback gateway stays local.
func (m *Module) toolClient(base string) Doer {
	if u, err := url.Parse(base); err == nil && localHost(u.Hostname()) {
		return netx.LoopbackClient()
	}
	network := netx.New(netx.Config{Offline: m.cfg.Offline, NoBackground: m.cfg.NoBackground})
	return network.Client("browser", netx.Tool)
}

// dialCDP resolves the discovery base and dials the page WebSocket.
func (m *Module) dialCDP(ctx context.Context) (Transport, error) {
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
		return nil, err
	}
	return conn, nil
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
