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
	"os"
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
	eng    Engine
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
	if m.eng != nil {
		err = m.eng.Close()
	}
	m.page, m.eng, m.host, m.status = nil, nil, "", host.StatusStopped
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
	case "browser_click", "browser_type":
		return m.action(ctx, tool, args, env)
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
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		final, err := p.Navigate(ctx, a.URL)
		if err != nil {
			return host.Result{}, err
		}
		m.host = urlHost(final)
		return host.Result{Content: "navigated to " + final, Meta: map[string]any{"url": final}}, nil
	})
}

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

func (m *Module) evaluate(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		Expression string `json:"expression"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		value, err := p.Eval(ctx, a.Expression)
		return host.Result{Content: value}, err
	})
}

func (m *Module) screenshot(ctx context.Context) (host.Result, error) {
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		png, err := p.Screenshot(ctx)
		if err != nil {
			return host.Result{}, err
		}
		return host.Result{Content: fmt.Sprintf("screenshot %d bytes", len(png)), Meta: map[string]any{"bytes": len(png)}}, nil
	})
}

func (m *Module) snapshot(ctx context.Context) (host.Result, error) {
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		reply, err := p.eng.Read(ctx, ReadOp{Kind: "snapshot", Limit: p.cfg.MaxResult})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if len(reply.Value) > p.cfg.MaxResult {
			return host.Result{}, fmt.Errorf("%w: snapshot %d > %d bytes", errTooLarge, len(reply.Value), p.cfg.MaxResult)
		}
		return host.Result{Content: string(reply.Value)}, nil
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
		reply, err := p.eng.Read(ctx, ReadOp{Kind: "pdf", Limit: p.cfg.MaxBlob})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if len(reply.Blob) > p.cfg.MaxBlob {
			return host.Result{}, fmt.Errorf("%w: pdf %d > %d bytes", errTooLarge, len(reply.Blob), p.cfg.MaxBlob)
		}
		return host.Result{Content: fmt.Sprintf("pdf %d bytes", len(reply.Blob)), Meta: map[string]any{"bytes": len(reply.Blob)}}, nil
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

func (m *Module) upload(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Selector, Path string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	file, err := readWorkspaceFile(env, a.Path)
	if err != nil {
		return host.Result{}, err
	}
	if aerr := m.approve(ctx, "browser_upload", "browser:upload", fmt.Sprintf("browser_upload %s (%d bytes)", a.Path, len(file.Data)), env, raw); aerr != nil {
		return host.Result{}, aerr
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		if err := p.Upload(ctx, a.Selector, file, true); err != nil {
			return host.Result{}, err
		}
		return host.Result{Content: fmt.Sprintf("uploaded %s (%d bytes)", a.Path, len(file.Data))}, nil
	})
}

func (m *Module) download(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		URL, Path string
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if err := m.approve(ctx, "browser_download", "browser:download", "browser_download "+a.Path, env, raw); err != nil {
		return host.Result{}, err
	}
	return m.withPage(ctx, func(p *Page) (host.Result, error) {
		u, err := p.checkURL(a.URL)
		if err != nil {
			return host.Result{}, err
		}
		reply, err := p.eng.Read(ctx, ReadOp{Kind: "download", URL: u.String(), Limit: p.cfg.MaxBlob, Guard: p.guard})
		if err != nil {
			return host.Result{}, pageError(err)
		}
		blob := reply.Blob
		if len(blob) > p.cfg.MaxBlob {
			return host.Result{}, fmt.Errorf("%w: download %d > %d bytes", errTooLarge, len(blob), p.cfg.MaxBlob)
		}
		if werr := writeWorkspaceFile(env, a.Path, blob); werr != nil {
			return host.Result{}, werr
		}
		return host.Result{
			Content: fmt.Sprintf("downloaded %d bytes to %s", len(blob), a.Path),
			Meta:    map[string]any{"bytes": len(blob), "name": reply.Name},
		}, nil
	})
}

// maxWorkspaceBlob bounds upload and download payloads (ADR-322).
const maxWorkspaceBlob = 8 << 20

// workspaceRoot opens the session workspace; every upload/download path is
// resolved through it, so an absolute or escaping path cannot leave the tree.
func workspaceRoot(env host.Env) (*os.Root, error) {
	cwd := env.Cwd
	if cwd == "" {
		cwd = env.Session.Cwd()
	}
	if cwd == "" {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	root, err := os.OpenRoot(cwd)
	if err != nil {
		return nil, &host.Error{Code: "invalid_args", Message: "workspace is not accessible"}
	}
	return root, nil
}

func readWorkspaceFile(env host.Env, path string) (out FileRef, err error) {
	root, err := workspaceRoot(env)
	if err != nil {
		return FileRef{}, err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if path == "" || filepath.IsAbs(path) {
		return FileRef{}, &host.Error{Code: "invalid_args", Message: "upload path must be relative"}
	}
	data, rerr := root.ReadFile(path)
	if rerr != nil {
		return FileRef{}, &host.Error{Code: "invalid_args", Message: "upload path is not readable"}
	}
	if len(data) > maxWorkspaceBlob {
		return FileRef{}, &host.Error{Code: "too_large", Message: "upload exceeds 8 MiB"}
	}
	return FileRef{Name: filepath.Base(path), Data: data}, nil
}

func writeWorkspaceFile(env host.Env, path string, blob []byte) (err error) {
	if len(blob) > maxWorkspaceBlob {
		return &host.Error{Code: "too_large", Message: "download exceeds 8 MiB"}
	}
	if path == "" || filepath.IsAbs(path) {
		return &host.Error{Code: "invalid_args", Message: "download path must be relative"}
	}
	root, err := workspaceRoot(env)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, root.Close()) }()
	if werr := root.WriteFile(path, blob, 0o600); werr != nil {
		return &host.Error{Code: "invalid_args", Message: "download path is not writable"}
	}
	return nil
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
	eng, err := m.newEngine(ctx, cfg)
	if err != nil {
		return nil, err
	}
	p, err := NewPage(eng, cfg)
	if err != nil {
		return nil, errors.Join(err, eng.Close())
	}
	m.page, m.eng = p, eng
	return p, nil
}

func (m *Module) newEngine(ctx context.Context, cfg Config) (Engine, error) {
	switch name := m.engineName(); name {
	case "":
		return nil, fmt.Errorf("%w: no browser engine configured", errUnavailable)
	case "cdp":
		if m.cfg.CDPURL == "" {
			return nil, fmt.Errorf("%w: cdp_url is empty", errUnavailable)
		}
		conn, err := m.dialCDP(ctx)
		if err != nil {
			return nil, err
		}
		return newCDPEngine(conn, cfg), nil
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
