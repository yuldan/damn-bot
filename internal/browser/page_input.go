package browser

import (
	"context"
	"encoding/json"
	"fmt"
)

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
	return p.clickAt(ctx, x, y)
}

// ClickRef clicks the centre of one element from the latest snapshot. A ref
// that is not in that snapshot (stale after a navigation, or from another
// page) is refused before any input reaches the page.
func (p *Page) ClickRef(ctx context.Context, ref int, approved bool) error {
	if !approved {
		return fmt.Errorf("%w: browser_click ref %d", errApproval, ref)
	}
	if !p.refKnown(ref) {
		return fmt.Errorf("%w: element ref %d is stale or unknown", errBadConfig, ref)
	}
	if p.eng != nil {
		return fmt.Errorf("%w: ref click needs a CDP page", errUnsupported)
	}
	raw, err := p.conn.Call(ctx, "DOM.getBoxModel", map[string]any{"backendNodeId": ref})
	if err != nil {
		return fmt.Errorf("browser: click ref: %w", err)
	}
	var res struct {
		Model struct {
			Content []float64 `json:"content"`
		} `json:"model"`
	}
	if uerr := json.Unmarshal(raw, &res); uerr != nil || len(res.Model.Content) != 8 {
		return fmt.Errorf("%w: click ref %d: no box model", errPage, ref)
	}
	q := res.Model.Content
	if q[0] == q[2] || q[1] == q[5] {
		return fmt.Errorf("%w: click ref %d: empty box", errPage, ref)
	}
	x, y := (q[0]+q[4])/2, (q[1]+q[5])/2
	return p.clickAt(ctx, x, y)
}

func (p *Page) clickAt(ctx context.Context, x, y float64) error {
	for _, kind := range []string{"mouseMoved", "mousePressed", "mouseReleased"} {
		params := map[string]any{"type": kind, "x": x, "y": y, "button": "left", "clickCount": 1}
		if _, err := p.conn.Call(ctx, "Input.dispatchMouseEvent", params); err != nil {
			return fmt.Errorf("browser: click: %w", err)
		}
	}
	return nil
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

// scrollLayoutExpr reads the current scroll offset and the maximum offset of
// the scrolling element. It is the only layout the scroll clamp needs.
const scrollLayoutExpr = `(() => { const d = document.scrollingElement || document.documentElement;` +
	` if (!d) return null; return {x: d.scrollLeft, y: d.scrollTop,` +
	` maxX: Math.max(0, d.scrollWidth - window.innerWidth),` +
	` maxY: Math.max(0, d.scrollHeight - window.innerHeight)}; })()`

// clampScroll bounds one wheel delta to the document range; a page is never
// asked to scroll past its layout. A missing layout is an error, not a silent
// no-op that would look like a scroll that worked.
func (p *Page) clampScroll(ctx context.Context, dx, dy float64) (float64, float64, error) {
	value, err := p.Eval(ctx, scrollLayoutExpr)
	if err != nil {
		return 0, 0, fmt.Errorf("%w: scroll layout: %w", errPage, err)
	}
	var layout struct {
		X, Y, MaxX, MaxY float64
	}
	if value == "" || value == "null" || json.Unmarshal([]byte(value), &layout) != nil {
		return 0, 0, fmt.Errorf("%w: scroll layout is unavailable", errPage)
	}
	return boundedDelta(layout.X, dx, layout.MaxX), boundedDelta(layout.Y, dy, layout.MaxY), nil
}

func boundedDelta(current, delta, maxOffset float64) float64 {
	target := min(max(current+delta, 0), maxOffset)
	return target - current
}
