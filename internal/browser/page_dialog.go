package browser

import (
	"context"
	"errors"
	"fmt"
)

// errNoDialog reports that no native dialog is waiting for an answer.
var errNoDialog = errors.New("browser: no dialog is open")

// maxDialogMessage and maxDialogPrompt bound the page-controlled dialog text.
const (
	maxDialogMessage = 2048
	maxDialogPrompt  = 512
	maxPromptText    = 1024
)

// Dialog is one native JavaScript dialog (TOOL-018). The message is page
// content: it is returned as bounded text and never evaluated or re-injected.
type Dialog struct {
	Type          string `json:"type"`
	Message       string `json:"message"`
	DefaultPrompt string `json:"default_prompt,omitempty"`
}

// dialogAllowed reports whether the operator policy lets this dialog type stay
// open for an explicit answer. The zero policy allows nothing, so every dialog
// is dismissed automatically (default deny).
func (p *Page) dialogAllowed(kind string) bool {
	for _, allowed := range p.cfg.DialogsAllow {
		if allowed == kind {
			return true
		}
	}
	return false
}

// enableDialogs turns the Page domain on once so dialog opening events arrive.
func (p *Page) enableDialogs(ctx context.Context) error {
	p.parity.mu.Lock()
	on := p.parity.dialogsOn
	p.parity.mu.Unlock()
	if on {
		return nil
	}
	if _, err := p.conn.Call(ctx, "Page.enable", nil); err != nil {
		return fmt.Errorf("browser: dialog: %w", err)
	}
	p.parity.mu.Lock()
	p.parity.dialogsOn = true
	p.parity.mu.Unlock()
	return nil
}

// openDialog records one `Page.javascriptDialogOpening` event. A type outside
// the policy is answered `accept=false` at once: prompt/confirm can act on the
// page, so silence is the safe default and the agent must opt in via
// `[modules.browser].dialog_allow`.
func (p *Page) openDialog(ctx context.Context, d Dialog) {
	p.parity.mu.Lock()
	keep := p.dialogAllowed(d.Type) && p.parity.dialog == nil
	if keep {
		p.parity.dialog = &d
	}
	p.parity.mu.Unlock()
	if keep {
		return
	}
	// The dialog may already be gone (page navigated away); a failed dismiss
	// only means nothing is left to answer, so it is not an error of the call
	// that happened to drain the event.
	_, _ = p.conn.Call(ctx, "Page.handleJavaScriptDialog", map[string]any{"accept": false})
}

// DialogStatus drains queued events and reports the dialog that policy keeps
// open, if any. It never answers one: an answer is a page action.
func (p *Page) DialogStatus(ctx context.Context) (Dialog, bool, error) {
	if p.conn == nil {
		return Dialog{}, false, fmt.Errorf("%w: dialog", errUnsupported)
	}
	if err := p.enableDialogs(ctx); err != nil {
		return Dialog{}, false, err
	}
	p.drain(ctx)
	p.parity.mu.Lock()
	defer p.parity.mu.Unlock()
	if p.parity.dialog == nil {
		return Dialog{}, false, nil
	}
	return *p.parity.dialog, true, nil
}

// Dialog answers the open dialog. Accepting a confirm or a prompt requires the
// type in the policy; the prompt text is page input, never a routing decision.
func (p *Page) Dialog(ctx context.Context, accept bool, text string) (Dialog, error) {
	if p.conn == nil {
		return Dialog{}, fmt.Errorf("%w: dialog", errUnsupported)
	}
	if err := p.enableDialogs(ctx); err != nil {
		return Dialog{}, err
	}
	p.drain(ctx)
	p.parity.mu.Lock()
	var (
		dialog  Dialog
		present = p.parity.dialog != nil
	)
	if present {
		dialog = *p.parity.dialog
	}
	p.parity.mu.Unlock()
	if !present {
		return Dialog{}, errNoDialog
	}
	pending := &dialog
	if accept && (pending.Type == "confirm" || pending.Type == "prompt") && !p.dialogAllowed(pending.Type) {
		return Dialog{}, fmt.Errorf("%w: %s dialog is not allowed by policy", errDenied, pending.Type)
	}
	params := map[string]any{"accept": accept}
	if accept && pending.Type == "prompt" {
		params["promptText"] = clip(text, maxPromptText)
	}
	if _, err := p.conn.Call(ctx, "Page.handleJavaScriptDialog", params); err != nil {
		return Dialog{}, fmt.Errorf("browser: dialog: %w", err)
	}
	p.parity.mu.Lock()
	p.parity.dialog = nil
	p.parity.mu.Unlock()
	return *pending, nil
}
