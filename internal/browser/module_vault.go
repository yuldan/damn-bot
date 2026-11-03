//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// maxVaultSelector bounds one CSS selector of the login form.
const maxVaultSelector = 1024

// vault fills a login form from the credential seam (TOOL-015). The values only
// ever travel from host.Secrets into Input.insertText: they are never printed,
// never returned and never part of an approval preview. The tool requires an
// explicit local profile (a real browser session) and one allow-once approval.
func (m *Module) vault(ctx context.Context, raw json.RawMessage, env host.Env) (host.Result, error) {
	var a struct {
		UserSelector     string `json:"user_selector"`
		PasswordSelector string `json:"password_selector"`
		Submit           bool   `json:"submit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	for _, selector := range []struct{ field, value string }{
		{"user_selector", a.UserSelector}, {"password_selector", a.PasswordSelector},
	} {
		if selector.value == "" || len(selector.value) > maxVaultSelector {
			return host.Result{}, invalidArgs(fmt.Errorf("%w: %s must be 1-%d bytes", errBadConfig, selector.field, maxVaultSelector))
		}
	}
	if !m.cfg.LocalProfile {
		return host.Result{}, &host.Error{
			Code: "denied", Message: "browser_vault needs local_profile = true",
			Hint: "enable modules.browser.local_profile for the real browser session",
		}
	}
	if m.cfg.VaultUserEnv == "" || m.cfg.VaultPassEnv == "" {
		return host.Result{}, &host.Error{
			Code: "module_unavailable", Message: "browser_vault has no vault_user_env/vault_password_env",
		}
	}
	preview := fmt.Sprintf("browser_vault fills 2 credential fields from %s/%s; submit=%t",
		m.cfg.VaultUserEnv, m.cfg.VaultPassEnv, a.Submit)
	if err := m.approve(ctx, "browser_vault", "browser:vault", preview, env, raw); err != nil {
		return host.Result{}, err
	}
	user, err := m.secret(m.cfg.VaultUserEnv)
	if err != nil {
		return host.Result{}, err
	}
	password, err := m.secret(m.cfg.VaultPassEnv)
	if err != nil {
		return host.Result{}, err
	}
	if user == "" || password == "" {
		return host.Result{}, &host.Error{
			Code: "module_unavailable", Message: "browser_vault: credential is not configured",
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, err := m.open(ctx)
	if err != nil {
		return host.Result{}, pageError(err)
	}
	if err := p.FillSecret(ctx, a.UserSelector, user); err != nil {
		return host.Result{}, vaultError(err)
	}
	if err := p.FillSecret(ctx, a.PasswordSelector, password); err != nil {
		return host.Result{}, vaultError(err)
	}
	if a.Submit {
		if err := p.SubmitForm(ctx, a.PasswordSelector); err != nil {
			return host.Result{}, vaultError(err)
		}
	}
	return host.Result{
		Content: fmt.Sprintf("filled the login form; submitted=%t", a.Submit),
		Meta:    map[string]any{"fields": 2, "submitted": a.Submit}, UntrustedSource: m.taint(),
	}, nil
}

// secret reads one named credential through the host seam. The value never
// reaches a log, a result or an error message.
func (m *Module) secret(name string) (string, error) {
	reader := m.deps.Secrets
	if reader == nil {
		return "", &host.Error{Code: "module_unavailable", Message: "browser_vault: no secret seam"}
	}
	pairs, err := reader.ChildEnv(host.AllowPolicy{Inherit: []string{name}})
	if err != nil {
		return "", &host.Error{Code: "module_unavailable", Message: "browser_vault: secret seam: " + err.Error()}
	}
	for _, pair := range pairs {
		if key, value, ok := cutPair(pair); ok && key == name {
			return value, nil
		}
	}
	return "", nil
}

// vaultError maps a fill failure without echoing any value.
func vaultError(err error) error {
	return pageError(fmt.Errorf("browser: vault: %w", err))
}
