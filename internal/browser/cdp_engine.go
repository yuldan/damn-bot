package browser

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"

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

// Eval returns the JSON value; Page applies the cap.
func (e *cdpEngine) Eval(ctx context.Context, expression string) (json.RawMessage, error) {
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

// Screenshot captures the viewport as PNG.
func (e *cdpEngine) Screenshot(ctx context.Context) ([]byte, error) {
	raw, err := e.conn.Call(ctx, "Page.captureScreenshot", map[string]string{"format": "png"})
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
	return png, nil
}

// Act dispatches one input action at viewport coordinates.
func (e *cdpEngine) Act(ctx context.Context, a Action) error {
	switch a.Kind {
	case "click":
		for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
			params := map[string]any{"type": kind, "x": a.X, "y": a.Y, "button": "left", "clickCount": 1}
			if _, err := e.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
				return fmt.Errorf("browser: click: %w", err)
			}
		}
		return nil
	case "type":
		if _, err := e.conn.Call(ctx, "Input.insertText", map[string]string{"text": a.Text}); err != nil {
			return fmt.Errorf("browser: type: %w", err)
		}
		return nil
	}
	return fmt.Errorf("%w: %s", errBadConfig, a.Kind)
}

func (e *cdpEngine) Close() error {
	if err := e.conn.Close(); err != nil {
		return fmt.Errorf("browser: close: %w", err)
	}
	return nil
}
