// Package browser is the F6 browser module (SPEC §3.2.4): an engine seam over
// CDP, Camoufox, Lightpanda and cloud HTTP providers, a default-deny origin
// allowlist and approval-gated page actions (ADR-313, ADR-321).
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
	"path"
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
	MaxBlob       int
}

func (c Config) normalize() Config {
	c.LoadTimeout = cmp.Or(c.LoadTimeout, 20*time.Second)
	c.MaxResult = cmp.Or(c.MaxResult, 64<<10)
	c.MaxScreenshot = cmp.Or(c.MaxScreenshot, 2<<20)
	c.MaxBlob = cmp.Or(c.MaxBlob, 8<<20)
	return c
}

// Page is one bound engine plus the module policy: allowlist, bounds, approval.
type Page struct {
	eng    Engine
	conn   Transport
	allow  *Allowlist
	cfg    Config
	parity *parityState
}

// NewPage binds an engine and validates the allowlist.
func NewPage(eng Engine, cfg Config) (*Page, error) {
	allow, err := NewAllowlist(cfg.AllowOrigins)
	if err != nil {
		return nil, err
	}
	return &Page{eng: eng, allow: allow, cfg: cfg.normalize(), parity: newParityState()}, nil
}

// NewCDPPage binds a CDP transport to the page policy.
func NewCDPPage(conn Transport, cfg Config) (*Page, error) {
	p, err := NewPage(nil, cfg)
	if err != nil {
		return nil, err
	}
	p.conn = conn
	return p, nil
}

// Close closes the bound engine or CDP transport.
func (p *Page) Close() error {
	if p.eng != nil {
		if err := p.eng.Close(); err != nil {
			return fmt.Errorf("browser: close: %w", err)
		}
		return nil
	}
	if p.conn == nil {
		return nil
	}
	if err := p.conn.Close(); err != nil {
		return fmt.Errorf("browser: close: %w", err)
	}
	return nil
}

// Navigate opens an allowlisted http(s) URL and returns the final URL. The
// guard runs before the call and on every redirect the engine reports.
func (p *Page) Navigate(ctx context.Context, rawURL string) (string, error) {
	u, err := p.checkURL(rawURL)
	if err != nil {
		return "", err
	}
	if p.eng != nil {
		final, nerr := p.eng.Navigate(ctx, u.String(), p.guard)
		if nerr != nil {
			return "", fmt.Errorf("browser: navigate: %w", nerr)
		}
		if _, cerr := p.checkURL(final); cerr != nil {
			return "", cerr
		}
		return final, nil
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
	if err := p.waitLoad(ctx); err != nil {
		return "", err
	}
	return u.String(), nil
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
	if p.eng == nil {
		return p.waitLoad(ctx)
	}
	waiter, ok := p.eng.(interface{ WaitLoad(context.Context) error })
	if !ok {
		return nil
	}
	if err := waiter.WaitLoad(ctx); err != nil {
		return fmt.Errorf("browser: wait load: %w", err)
	}
	return nil
}

func (p *Page) waitLoad(ctx context.Context) error {
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
	if err != nil || u.Host == "" {
		return errBlockedOrigin
	}
	return p.guard(u)
}

// Eval evaluates expression and returns the JSON value, capped at MaxResult.
func (p *Page) Eval(ctx context.Context, expression string) (string, error) {
	reply, err := p.read(ctx, ReadOp{Kind: "eval", Expression: expression, Limit: p.cfg.MaxResult})
	if err != nil {
		return "", fmt.Errorf("browser: evaluate: %w", err)
	}
	return p.evalValue(reply.Value)
}

// read dispatches a read to the engine, or to the CDP transport when the page
// is bound directly to CDP.
func (p *Page) read(ctx context.Context, op ReadOp) (Reply, error) {
	if p.eng != nil {
		reply, err := p.eng.Read(ctx, op)
		if err != nil {
			return Reply{}, fmt.Errorf("browser: read: %w", err)
		}
		return reply, nil
	}
	return p.cdpRead(ctx, op)
}

func (p *Page) evalValue(value []byte) (string, error) {
	if len(value) == 0 {
		value = []byte("null")
	}
	if len(value) > p.cfg.MaxResult {
		return "", fmt.Errorf("%w: %d > %d bytes", errTooLarge, len(value), p.cfg.MaxResult)
	}
	return string(value), nil
}

// Screenshot captures the viewport as PNG, capped at MaxScreenshot bytes.
func (p *Page) Screenshot(ctx context.Context) ([]byte, error) {
	reply, err := p.read(ctx, ReadOp{Kind: "screenshot", Limit: p.cfg.MaxScreenshot})
	if err != nil {
		return nil, fmt.Errorf("browser: screenshot: %w", err)
	}
	return p.screenshotValue(reply.Blob)
}

func (p *Page) screenshotValue(png []byte) ([]byte, error) {
	if len(png) > p.cfg.MaxScreenshot {
		return nil, fmt.Errorf("%w: screenshot %d > %d bytes", errTooLarge, len(png), p.cfg.MaxScreenshot)
	}
	return png, nil
}

func (p *Page) cdpRead(ctx context.Context, op ReadOp) (Reply, error) {
	switch op.Kind {
	case "eval":
		raw, err := p.conn.Call(ctx, "Runtime.evaluate", map[string]any{
			"expression": op.Expression, "returnByValue": true, "awaitPromise": true,
		})
		if err != nil {
			return Reply{}, fmt.Errorf("browser: evaluate: %w", err)
		}
		var res struct {
			Result struct {
				Value json.RawMessage `json:"value"`
			} `json:"result"`
		}
		if uerr := json.Unmarshal(raw, &res); uerr != nil {
			return Reply{}, fmt.Errorf("%w: evaluate: %w", errPage, uerr)
		}
		return Reply{Value: res.Result.Value}, nil
	case "screenshot":
		raw, err := p.conn.Call(ctx, "Page.captureScreenshot", map[string]string{"format": "png"})
		if err != nil {
			return Reply{}, fmt.Errorf("browser: screenshot: %w", err)
		}
		var res struct {
			Data string `json:"data"`
		}
		if uerr := json.Unmarshal(raw, &res); uerr != nil {
			return Reply{}, errPage
		}
		png, derr := base64.StdEncoding.DecodeString(res.Data)
		if derr != nil {
			return Reply{}, errPage
		}
		return Reply{Blob: png}, nil
	case "snapshot":
		return p.cdpSnapshot(ctx)
	case "back":
		return p.cdpBack(ctx, op.Guard)
	case "pdf":
		return p.cdpPDF(ctx)
	case "download":
		return p.cdpDownload(ctx, op)
	}
	return Reply{}, fmt.Errorf("%w: %s", errBadConfig, op.Kind)
}

// Snapshot returns the accessibility tree as compact JSON.
func (p *Page) Snapshot(ctx context.Context) (string, error) {
	reply, err := p.read(ctx, ReadOp{Kind: "snapshot", Limit: p.cfg.MaxResult})
	if err != nil {
		return "", fmt.Errorf("browser: snapshot: %w", err)
	}
	if len(reply.Value) > p.cfg.MaxResult {
		return "", fmt.Errorf("%w: snapshot %d > %d bytes", errTooLarge, len(reply.Value), p.cfg.MaxResult)
	}
	return string(reply.Value), nil
}

// Back navigates one history entry back; no entry is a typed error.
func (p *Page) Back(ctx context.Context) (string, error) {
	reply, err := p.read(ctx, ReadOp{Kind: "back", Guard: p.guard})
	if err != nil {
		return "", fmt.Errorf("browser: back: %w", err)
	}
	if reply.Name == "" {
		return "", errNoHistory
	}
	return reply.Name, nil
}

func (p *Page) cdpPDF(ctx context.Context) (Reply, error) {
	raw, err := p.conn.Call(ctx, "Page.printToPDF", map[string]any{"printBackground": true})
	if err != nil {
		return Reply{}, fmt.Errorf("browser: pdf: %w", err)
	}
	var res struct {
		Data string `json:"data"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return Reply{}, errPage
	}
	pdf, derr := base64.StdEncoding.DecodeString(res.Data)
	if derr != nil {
		return Reply{}, errPage
	}
	return Reply{Blob: pdf}, nil
}

func (p *Page) cdpDownload(ctx context.Context, op ReadOp) (Reply, error) {
	u, err := url.Parse(op.URL)
	if err != nil || u.Host == "" {
		return Reply{}, fmt.Errorf("%w: download url", errBadConfig)
	}
	expr := `(async () => { const r = await fetch(` + quote(op.URL) + `); const b = await r.arrayBuffer();` +
		` const u = new Uint8Array(b); let s = ""; for (let i = 0; i < u.length; i += 0x8000) { s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000)); } return btoa(s); })()`
	value, err := p.conn.Call(ctx, "Runtime.evaluate", map[string]any{"expression": expr, "returnByValue": true, "awaitPromise": true})
	if err != nil {
		return Reply{}, fmt.Errorf("browser: download: %w", err)
	}
	var res struct {
		Result struct {
			Value string `json:"value"`
		} `json:"result"`
	}
	if uerr := json.Unmarshal(value, &res); uerr != nil {
		return Reply{}, fmt.Errorf("%w: download reply", errPage)
	}
	blob, derr := base64.StdEncoding.DecodeString(res.Result.Value)
	if derr != nil {
		return Reply{}, fmt.Errorf("%w: download decode", errPage)
	}
	if op.Limit > 0 && len(blob) > op.Limit {
		return Reply{}, fmt.Errorf("%w: download %d > %d bytes", errTooLarge, len(blob), op.Limit)
	}
	return Reply{Blob: blob, Name: path.Base(u.Path)}, nil
}

func (p *Page) cdpSnapshot(ctx context.Context) (Reply, error) {
	raw, err := p.conn.Call(ctx, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return Reply{}, fmt.Errorf("browser: snapshot: %w", err)
	}
	var res struct {
		Nodes []struct {
			Role struct {
				Value string `json:"value"`
			} `json:"role"`
			Name struct {
				Value string `json:"value"`
			} `json:"name"`
			Backend int `json:"backendDOMNodeId"`
		} `json:"nodes"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return Reply{}, fmt.Errorf("%w: snapshot: %w", errPage, uerr)
	}
	out := make([]map[string]any, 0, len(res.Nodes))
	for _, n := range res.Nodes {
		if n.Role.Value == "" && n.Name.Value == "" {
			continue
		}
		node := map[string]any{"ref": n.Backend, "role": n.Role.Value}
		if !sensitiveRole(n.Role.Value) {
			node["name"] = n.Name.Value
		}
		out = append(out, node)
	}
	value, merr := json.Marshal(out)
	if merr != nil {
		return Reply{}, fmt.Errorf("%w: snapshot encode: %w", errPage, merr)
	}
	return Reply{Value: value}, nil
}

func sensitiveRole(role string) bool {
	switch role {
	case "textbox", "searchbox", "password", "spinbutton":
		return true
	}
	return false
}

func (p *Page) cdpBack(ctx context.Context, guard func(*url.URL) error) (Reply, error) {
	raw, err := p.conn.Call(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return Reply{}, fmt.Errorf("browser: back: %w", err)
	}
	var res struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return Reply{}, fmt.Errorf("%w: back: %w", errPage, uerr)
	}
	if res.CurrentIndex <= 0 || res.CurrentIndex > len(res.Entries) {
		return Reply{}, errNoHistory
	}
	target := res.Entries[res.CurrentIndex-1]
	u, perr := url.Parse(target.URL)
	if perr != nil || u.Host == "" {
		return Reply{}, fmt.Errorf("%w: %s", errBlockedOrigin, target.URL)
	}
	if guard != nil {
		if gerr := guard(u); gerr != nil {
			return Reply{}, gerr
		}
	}
	if _, cerr := p.conn.Call(ctx, "Page.navigateToHistoryEntry", map[string]int{"entryId": target.ID}); cerr != nil {
		return Reply{}, fmt.Errorf("browser: back: %w", cerr)
	}
	return Reply{Name: target.URL}, nil
}

// Click dispatches a left click; without approval it fails with a preview.
func (p *Page) Click(ctx context.Context, x, y float64, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_click at (%.0f, %.0f)", errApproval, x, y)
	}
	if p.eng != nil {
		if err := p.eng.Act(ctx, &Action{Kind: "click", X: x, Y: y}); err != nil {
			return fmt.Errorf("browser: click: %w", err)
		}
		return nil
	}
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1}
		if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
			return fmt.Errorf("browser: click: %w", err)
		}
	}
	return nil
}

// Run performs one non-read pointer or keyboard action after approval.
func (p *Page) Run(ctx context.Context, tool string, a *Action, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: %s", errApproval, tool)
	}
	if tool == "browser_select" {
		return p.selectValue(ctx, a.Selector, a.Value)
	}
	if err := actionKind(tool, a); err != nil {
		return err
	}
	if p.eng != nil {
		if err := p.eng.Act(ctx, a); err != nil {
			return fmt.Errorf("browser: %s: %w", tool, err)
		}
		return nil
	}
	return p.cdpAct(ctx, a)
}

func actionKind(tool string, a *Action) error {
	switch tool {
	case "browser_click":
		a.Kind = "click"
	case "browser_type":
		a.Kind = "type"
	case "browser_hover":
		a.Kind = "hover"
	case "browser_drag":
		a.Kind = "drag"
	case "browser_scroll":
		a.Kind = "scroll"
		if a.DeltaX < -10000 || a.DeltaX > 10000 || a.DeltaY < -10000 || a.DeltaY > 10000 {
			return fmt.Errorf("%w: scroll deltas", errBadConfig)
		}
	case "browser_press":
		if !knownKey(a.Key) {
			return fmt.Errorf("%w: key %q", errBadConfig, a.Key)
		}
		a.Kind = "press"
	default:
		return fmt.Errorf("%w: %s", errBadConfig, tool)
	}
	return nil
}

func (p *Page) cdpAct(ctx context.Context, a *Action) error {
	switch a.Kind {
	case "hover":
		return p.mouse(ctx, "mouseMoved", a.X, a.Y, "")
	case "drag":
		if err := p.mouse(ctx, "mousePressed", a.X, a.Y, "left"); err != nil {
			return err
		}
		if err := p.mouse(ctx, "mouseMoved", a.EndX, a.EndY, "left"); err != nil {
			return err
		}
		return p.mouse(ctx, "mouseReleased", a.EndX, a.EndY, "left")
	case "press":
		for _, kind := range []string{"keyDown", "keyUp"} {
			if _, err := p.conn.Call(ctx, "Input.dispatchKeyEvent", map[string]string{"type": kind, "key": a.Key}); err != nil {
				return fmt.Errorf("browser: press: %w", err)
			}
		}
		return nil
	case "scroll":
		_, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
			"type": "mouseWheel", "x": a.X, "y": a.Y, "deltaX": a.DeltaX, "deltaY": a.DeltaY,
		})
		if err != nil {
			return fmt.Errorf("browser: scroll: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", errBadConfig, a.Kind)
}

func (p *Page) mouse(ctx context.Context, kind string, x, y float64, button string) error {
	params := map[string]any{"type": kind, "x": x, "y": y, "clickCount": 1}
	if button != "" {
		params["button"] = button
	}
	if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
		return fmt.Errorf("browser: %s: %w", kind, err)
	}
	return nil
}

func truthy(value string) bool {
	switch value {
	case "", "null", "false", "0", `""`, "[]":
		return false
	}
	return true
}

func (p *Page) selectValue(ctx context.Context, selector, value string) error {
	expr := `(() => { const e = document.querySelector(` + quote(selector) + `); if (!e) return false;` +
		` e.value = ` + quote(value) + `; e.dispatchEvent(new Event("change", {bubbles: true})); return true; })()`
	got, err := p.Eval(ctx, expr)
	if err != nil {
		return err
	}
	if got != "true" {
		return fmt.Errorf("%w: selector %s", errBadConfig, selector)
	}
	return nil
}

func quote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}

func knownKey(key string) bool {
	if len(key) == 1 && (key[0] >= 'a' && key[0] <= 'z' || key[0] >= '0' && key[0] <= '9') {
		return true
	}
	switch key {
	case "Enter", "Tab", "Escape", "Backspace", "Delete", "ArrowUp", "ArrowDown", "ArrowLeft", "ArrowRight", "Home", "End", "PageUp", "PageDown", "Space":
		return true
	}
	return false
}

// FileRef is one upload payload: the module bounds and reads it.
type FileRef struct {
	Name string
	Data []byte
}

// WaitFor polls expression until it is truthy or the timeout expires.
func (p *Page) WaitFor(ctx context.Context, expression string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, cmp.Or(timeout, 5*time.Second))
	defer cancel()
	for {
		value, err := p.Eval(ctx, expression)
		if err != nil {
			return err
		}
		if truthy(value) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: wait_for: %w", errTimeout, ctx.Err())
		case <-time.After(100 * time.Millisecond):
		}
	}
}

// Upload sets a file input from a workspace file already bounded by the module.
func (p *Page) Upload(ctx context.Context, selector string, file FileRef, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_upload", errApproval)
	}
	if selector == "" || len(file.Data) > p.cfg.MaxBlob {
		return fmt.Errorf("%w: upload", errBadConfig)
	}
	b64 := base64.StdEncoding.EncodeToString(file.Data)
	expr := `(() => { const e = document.querySelector(` + quote(selector) + `); if (!e) return false;` +
		` const s = atob(` + quote(b64) + `); const u = new Uint8Array(s.length);` +
		` for (let i = 0; i < s.length; i++) u[i] = s.charCodeAt(i);` +
		` const d = new DataTransfer(); d.items.add(new File([u], ` + quote(file.Name) + `));` +
		` e.files = d.files; e.dispatchEvent(new Event("change", {bubbles: true})); return true; })()`
	got, err := p.Eval(ctx, expr)
	if err != nil {
		return err
	}
	if got != "true" {
		return fmt.Errorf("%w: upload selector", errBadConfig)
	}
	return nil
}

// Type inserts text into the focused element; it is non-read and needs approval.
// Type inserts text into the focused element; it is non-read and needs approval.
func (p *Page) Type(ctx context.Context, text string, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_type %d characters", errApproval, len(text))
	}
	if p.eng != nil {
		if err := p.eng.Act(ctx, &Action{Kind: "type", Text: text}); err != nil {
			return fmt.Errorf("browser: type: %w", err)
		}
		return nil
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
	errDenied        = errors.New("browser: engine denied the request")
	errNoHistory     = errors.New("browser: no history entry")
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
	if p == nil || (p.eng == nil && p.conn == nil) || len(p.cfg.AllowOrigins) == 0 {
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
	case errors.Is(err, errBadConfig), errors.Is(err, errNoHistory):
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
