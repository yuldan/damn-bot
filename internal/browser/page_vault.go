package browser

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// FillSecret focuses selector and inserts value into it. It is the only write
// path for a credential: the value is sent as input text, never as part of an
// expression or a result, and a missing element is a typed failure.
func (p *Page) FillSecret(ctx context.Context, selector, value string) error {
	if value == "" {
		return fmt.Errorf("%w: empty credential", errBadConfig)
	}
	if err := p.focus(ctx, selector); err != nil {
		return err
	}
	if p.eng != nil {
		if err := p.eng.Act(ctx, &Action{Kind: "type", Text: value}); err != nil {
			return fmt.Errorf("browser: fill: %w", err)
		}
		return nil
	}
	if _, err := p.conn.Call(ctx, "Input.insertText", map[string]string{"text": value}); err != nil {
		return fmt.Errorf("browser: fill: %w", err)
	}
	return nil
}

// SubmitForm submits the form that owns selector. It is opt-in and follows the
// same selector syntax as FillSecret.
func (p *Page) SubmitForm(ctx context.Context, selector string) error {
	quoted, err := json.Marshal(selector)
	if err != nil {
		return fmt.Errorf("%w: selector", errBadConfig)
	}
	expression := `(function(){var e=document.querySelector(` + string(quoted) +
		`);if(!e||!e.form)return false;e.form.requestSubmit();return true})()`
	value, err := p.Eval(ctx, expression)
	if err != nil {
		return fmt.Errorf("browser: submit: %w", err)
	}
	if value != "true" {
		return fmt.Errorf("%w: selector %q has no form", errBadConfig, selector)
	}
	return nil
}

// focus moves the caret to selector and fails when the element is absent. The
// selector is page input; the value never enters the expression.
func (p *Page) focus(ctx context.Context, selector string) error {
	quoted, err := json.Marshal(selector)
	if err != nil {
		return fmt.Errorf("%w: selector", errBadConfig)
	}
	expression := `(function(){var e=document.querySelector(` + string(quoted) +
		`);if(!e)return false;e.focus();return true})()`
	value, err := p.Eval(ctx, expression)
	if err != nil {
		return fmt.Errorf("browser: focus: %w", err)
	}
	if value != "true" {
		return fmt.Errorf("%w: selector %q not found", errBadConfig, selector)
	}
	return nil
}

// cutPair splits one NAME=value environment pair.
func cutPair(pair string) (string, string, bool) {
	key, value, ok := strings.Cut(pair, "=")
	if !ok || key == "" {
		return "", "", false
	}
	return key, value, true
}
