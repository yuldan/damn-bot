package gwpeer

import (
	"context"
	"errors"
	"fmt"
)

// ErrModuleUnavailable is the GW-025 verdict when managed-bot provisioning is
// off or has no credential. It renders as `module_unavailable`.
var ErrModuleUnavailable = errors.New("module_unavailable")

// ManagedBot provisions the optional managed Telegram bot (GW-025). The zero
// value is disabled and performs no network call: enabling is an explicit
// operator action, never an env autodetect.
type ManagedBot struct {
	Enabled bool
	// Credential returns the provisioned bot token; found=false is the
	// ordinary "not configured" case.
	Credential func(ctx context.Context) (token string, found bool, err error)
	// Provision performs the single network call; nil means unavailable.
	Provision func(ctx context.Context, token string) error
}

// ProvisionBot runs the opt-in provisioning path. Every unavailable state is
// ErrModuleUnavailable and makes zero network calls.
func (m *ManagedBot) ProvisionBot(ctx context.Context) error {
	if !m.Enabled || m.Credential == nil || m.Provision == nil {
		return ErrModuleUnavailable
	}
	token, found, err := m.Credential(ctx)
	if err != nil {
		return fmt.Errorf("%w: %w", ErrModuleUnavailable, err)
	}
	if !found || token == "" {
		return ErrModuleUnavailable
	}
	if err := m.Provision(ctx, token); err != nil {
		return fmt.Errorf("gwpeer: managed bot: %w", err)
	}
	return nil
}
