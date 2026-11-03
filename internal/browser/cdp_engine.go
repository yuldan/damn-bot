package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"path"

	"github.com/damnbot/damnbot/internal/browser/cdp"
)

// cdpEngine is the DevTools transport (ADR-313); it is the only engine that
// sees navigation events and can enforce the guard mid-flight.
type cdpEngine struct {
	conn Transport
	cfg  Config
}

func newCDPEngine(conn Transport, cfg Config) *cdpEngine {
	return &cdpEngine{conn: conn, cfg: cfg}
}

// Navigate opens a validated URL and waits for the load event.
func (e *cdpEngine) Navigate(ctx context.Context, rawURL string, guard func(*url.URL) error) (string, error) {
	raw, err := e.conn.Call(ctx, "Page.navigate", map[string]string{"url": rawURL})
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
	if err := e.waitLoad(ctx, guard); err != nil {
		return "", err
	}
	return rawURL, nil
}

// WaitLoad waits for the next load event without a redirect guard; it stays
// optional (Page finds it by type assertion).
func (e *cdpEngine) WaitLoad(ctx context.Context) error {
	if err := e.waitLoad(ctx, func(*url.URL) error { return nil }); err != nil {
		return fmt.Errorf("browser: wait load: %w", err)
	}
	return nil
}

func (e *cdpEngine) waitLoad(ctx context.Context, guard func(*url.URL) error) error {
	ctx, cancel := context.WithTimeout(ctx, e.cfg.LoadTimeout)
	defer cancel()
	for {
		select {
		case ev, ok := <-e.conn.Events():
			if !ok {
				return errClosed
			}
			if ev.Method == "Page.loadEventFired" {
				return nil
			}
			if err := target(ev, guard); err != nil {
				if _, serr := e.conn.Call(ctx, "Page.stopLoading", nil); serr != nil {
					return err
				}
				return fmt.Errorf("%w: redirect", errBlockedOrigin)
			}
		case <-ctx.Done():
			return fmt.Errorf("%w: no load event: %w", errTimeout, ctx.Err())
		}
	}
}

func target(ev cdp.Event, guard func(*url.URL) error) error {
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
	return guard(u)
}

// Read dispatches one page read.
func (e *cdpEngine) Read(ctx context.Context, op ReadOp) (Reply, error) {
	switch op.Kind {
	case "eval":
		value, err := e.eval(ctx, op.Expression)
		return Reply{Value: value}, err
	case "screenshot":
		return e.image(ctx, "Page.captureScreenshot", map[string]string{"format": "png"})
	case "pdf":
		return e.image(ctx, "Page.printToPDF", map[string]any{"printBackground": true})
	case "snapshot":
		value, err := e.snapshot(ctx)
		return Reply{Value: value}, err
	case "back":
		final, err := e.back(ctx, op.Guard)
		return Reply{Name: final}, err
	case "download":
		return e.download(ctx, op)
	}
	return Reply{}, fmt.Errorf("%w: %s", errBadConfig, op.Kind)
}

func (e *cdpEngine) eval(ctx context.Context, expression string) (json.RawMessage, error) {
	raw, err := e.conn.Call(ctx, "Runtime.evaluate", map[string]any{
		"expression": expression, "returnByValue": true, "awaitPromise": true,
	})
	if err != nil {
		return nil, fmt.Errorf("browser: evaluate: %w", err)
	}
	var res struct {
		Result struct {
			Value json.RawMessage `json:"value"`
		} `json:"result"`
	}
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, fmt.Errorf("%w: evaluate: %w", errPage, err)
	}
	if len(res.Result.Value) == 0 {
		return json.RawMessage("null"), nil
	}
	return res.Result.Value, nil
}

func (e *cdpEngine) image(ctx context.Context, method string, params any) (Reply, error) {
	raw, err := e.conn.Call(ctx, method, params)
	if err != nil {
		return Reply{}, fmt.Errorf("browser: %s: %w", method, err)
	}
	var res struct {
		Data string `json:"data"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return Reply{}, errPage
	}
	blob, derr := base64.StdEncoding.DecodeString(res.Data)
	if derr != nil {
		return Reply{}, errPage
	}
	return Reply{Blob: blob}, nil
}

func (e *cdpEngine) snapshot(ctx context.Context) (json.RawMessage, error) {
	raw, err := e.conn.Call(ctx, "Accessibility.getFullAXTree", nil)
	if err != nil {
		return nil, fmt.Errorf("browser: snapshot: %w", err)
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
		return nil, fmt.Errorf("%w: snapshot: %w", errPage, uerr)
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
		return nil, fmt.Errorf("%w: snapshot encode: %w", errPage, merr)
	}
	return value, nil
}

// sensitiveRole marks input roles whose accessible name can carry a value.
func sensitiveRole(role string) bool {
	switch role {
	case "textbox", "searchbox", "password", "spinbutton":
		return true
	}
	return false
}

func (e *cdpEngine) back(ctx context.Context, guard func(*url.URL) error) (string, error) {
	raw, err := e.conn.Call(ctx, "Page.getNavigationHistory", nil)
	if err != nil {
		return "", fmt.Errorf("browser: back: %w", err)
	}
	var res struct {
		CurrentIndex int `json:"currentIndex"`
		Entries      []struct {
			ID  int    `json:"id"`
			URL string `json:"url"`
		} `json:"entries"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil {
		return "", fmt.Errorf("%w: back: %w", errPage, uerr)
	}
	if res.CurrentIndex <= 0 || res.CurrentIndex > len(res.Entries) {
		return "", errNoHistory
	}
	target := res.Entries[res.CurrentIndex-1]
	u, perr := url.Parse(target.URL)
	if perr != nil || u.Host == "" {
		return "", fmt.Errorf("%w: %s", errBlockedOrigin, target.URL)
	}
	if guard != nil {
		if gerr := guard(u); gerr != nil {
			return "", gerr
		}
	}
	if _, cerr := e.conn.Call(ctx, "Page.navigateToHistoryEntry", map[string]int{"entryId": target.ID}); cerr != nil {
		return "", fmt.Errorf("browser: back: %w", cerr)
	}
	return target.URL, nil
}

func (e *cdpEngine) download(ctx context.Context, op ReadOp) (Reply, error) {
	u, err := url.Parse(op.URL)
	if err != nil || u.Host == "" {
		return Reply{}, fmt.Errorf("%w: download url", errBadConfig)
	}
	expr := `(async () => { const r = await fetch(` + quote(op.URL) + `); const b = await r.arrayBuffer();` +
		` const u = new Uint8Array(b); let s = ""; for (let i = 0; i < u.length; i += 0x8000) { s += String.fromCharCode.apply(null, u.subarray(i, i + 0x8000)); } return btoa(s); })()`
	value, err := e.eval(ctx, expr)
	if err != nil {
		return Reply{}, err
	}
	var b64 string
	if jerr := json.Unmarshal(value, &b64); jerr != nil {
		return Reply{}, fmt.Errorf("%w: download reply", errPage)
	}
	blob, derr := base64.StdEncoding.DecodeString(b64)
	if derr != nil {
		return Reply{}, fmt.Errorf("%w: download decode", errPage)
	}
	if op.Limit > 0 && len(blob) > op.Limit {
		return Reply{}, fmt.Errorf("%w: download %d > %d bytes", errTooLarge, len(blob), op.Limit)
	}
	return Reply{Blob: blob, Name: path.Base(u.Path)}, nil
}

// Act dispatches one input action.
func (e *cdpEngine) Act(ctx context.Context, a *Action) error {
	switch a.Kind {
	case "click":
		return e.click(ctx, a)
	case "hover":
		return e.mouse(ctx, "mouseMoved", a.X, a.Y, "")
	case "drag":
		return e.drag(ctx, a)
	case "scroll":
		return e.scroll(ctx, a)
	case "press":
		return e.press(ctx, a.Key)
	case "type":
		return e.typeText(ctx, a.Text)
	}
	return fmt.Errorf("%w: %s", errBadConfig, a.Kind)
}

func (e *cdpEngine) click(ctx context.Context, a *Action) error {
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		if err := e.mouse(ctx, kind, a.X, a.Y, ""); err != nil {
			return err
		}
	}
	return nil
}

func (e *cdpEngine) drag(ctx context.Context, a *Action) error {
	if err := e.mouse(ctx, "mousePressed", a.X, a.Y, "left"); err != nil {
		return err
	}
	if err := e.mouse(ctx, "mouseMoved", a.EndX, a.EndY, "left"); err != nil {
		return err
	}
	return e.mouse(ctx, "mouseReleased", a.EndX, a.EndY, "left")
}

func (e *cdpEngine) scroll(ctx context.Context, a *Action) error {
	_, err := e.conn.Call(ctx, "Input.dispatchMouseEvent", map[string]any{
		"type": "mouseWheel", "x": a.X, "y": a.Y, "deltaX": a.DeltaX, "deltaY": a.DeltaY,
	})
	if err != nil {
		return fmt.Errorf("browser: scroll: %w", err)
	}
	return nil
}

func (e *cdpEngine) press(ctx context.Context, key string) error {
	for _, kind := range []string{"keyDown", "keyUp"} {
		if _, err := e.conn.Call(ctx, "Input.dispatchKeyEvent", map[string]string{"type": kind, "key": key}); err != nil {
			return fmt.Errorf("browser: press: %w", err)
		}
	}
	return nil
}

func (e *cdpEngine) typeText(ctx context.Context, text string) error {
	if _, err := e.conn.Call(ctx, "Input.insertText", map[string]string{"text": text}); err != nil {
		return fmt.Errorf("browser: type: %w", err)
	}
	return nil
}

func (e *cdpEngine) mouse(ctx context.Context, kind string, x, y float64, button string) error {
	params := map[string]any{"type": kind, "x": x, "y": y, "clickCount": 1}
	if button != "" {
		params["button"] = button
	}
	if _, err := e.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
		return fmt.Errorf("browser: %s: %w", kind, err)
	}
	return nil
}

func (e *cdpEngine) Close() error {
	if err := e.conn.Close(); err != nil {
		return fmt.Errorf("browser: close: %w", err)
	}
	return nil
}
