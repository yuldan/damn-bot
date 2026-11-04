//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// dialog inspects or answers one native dialog (TOOL-018). Status is free; an
// answer always raises one allow-once approval whose preview names only the
// dialog type, never its page-controlled message. The module lock is held
// across the approval so the answered dialog is the one that was inspected.
func (m *Module) dialog(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		Action string `json:"action"`
		Text   string `json:"text"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if a.Action == "" {
		a.Action = "status"
	}
	switch a.Action {
	case "status", "accept", "dismiss":
	default:
		return host.Result{}, invalidArgs(fmt.Errorf("%w: dialog action %q", errBadConfig, a.Action))
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	d, open, err := p.DialogStatus(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	if a.Action == "status" {
		if !open {
			return host.Result{Content: "no dialog is open", UntrustedSource: m.taint()}, nil
		}
		return host.Result{
			Content: fmt.Sprintf("open %s dialog: %s", d.Type, d.Message),
			Meta:    map[string]any{"type": d.Type}, UntrustedSource: m.taint(),
		}, nil
	}
	if !open {
		return host.Result{Content: "no dialog is open", UntrustedSource: m.taint()}, nil
	}
	preview := fmt.Sprintf("browser_dialog %s the open %s dialog", a.Action, d.Type)
	if err := m.approve(ctx, "browser_dialog", "browser:dialog", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	if _, err := p.Dialog(ctx, a.Action == "accept", a.Text); err != nil {
		return host.Result{}, pageError(err)
	}
	verb := "dismissed"
	if a.Action == "accept" {
		verb = "accepted"
	}
	return host.Result{
		Content: fmt.Sprintf("%s the %s dialog", verb, d.Type),
		Meta:    map[string]any{"type": d.Type, "action": a.Action}, UntrustedSource: m.taint(),
	}, nil
}
