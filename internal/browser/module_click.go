//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func (m *Module) click(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		X   *float64 `json:"x"`
		Y   *float64 `json:"y"`
		Ref *int     `json:"ref"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	switch {
	case a.Ref != nil:
		if a.X != nil || a.Y != nil || *a.Ref <= 0 {
			return host.Result{}, invalidArgs(fmt.Errorf("%w: browser_click takes x+y or ref", errBadConfig))
		}
		if err := m.checkRef(ctx, *a.Ref); err != nil {
			return host.Result{}, err
		}
		preview := fmt.Sprintf("browser_click element ref %d in the current page", *a.Ref)
		if err := m.approve(ctx, "browser_click", "browser:click", preview, env, raw); err != nil {
			return host.Result{}, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		p, err := m.open(ctx)
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if err := p.ClickRef(ctx, *a.Ref, true); err != nil {
			return host.Result{}, pageError(err)
		}
		return host.Result{Content: fmt.Sprintf("clicked element ref %d", *a.Ref), UntrustedSource: m.taint()}, nil
	case a.X != nil && a.Y != nil:
		preview := fmt.Sprintf("browser_click at (%.0f, %.0f) in the current page", *a.X, *a.Y)
		if err := m.approve(ctx, "browser_click", "browser:click", preview, env, raw); err != nil {
			return host.Result{}, err
		}
		m.mu.Lock()
		defer m.mu.Unlock()
		p, err := m.open(ctx)
		if err != nil {
			return host.Result{}, pageError(err)
		}
		if err := p.Click(ctx, *a.X, *a.Y, true); err != nil {
			return host.Result{}, pageError(err)
		}
		return host.Result{Content: fmt.Sprintf("clicked at (%.0f, %.0f)", *a.X, *a.Y), UntrustedSource: m.taint()}, nil
	}
	return host.Result{}, invalidArgs(fmt.Errorf("%w: browser_click takes x+y or ref", errBadConfig))
}

// checkRef validates the element ref before an approval request is raised, so
// a stale or foreign ref never reaches the page or the approver.
func (m *Module) checkRef(ctx context.Context, ref int) error {
	m.mu.Lock()
	p, err := m.open(ctx)
	m.mu.Unlock()
	if err != nil {
		return pageError(err)
	}
	if !p.refKnown(ref) {
		return invalidArgs(fmt.Errorf("%w: element ref %d is stale or unknown", errBadConfig, ref))
	}
	return nil
}
