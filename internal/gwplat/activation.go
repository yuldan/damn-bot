package gwplat

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/gateway"
)

// Activation errors. Compared with errors.Is.
var (
	ErrUnavailable   = errors.New("gwplat: module_unavailable")
	ErrMissingSecret = errors.New("gwplat: missing secret")
	ErrWeakIdentity  = errors.New("gwplat: weak identity cannot pair")
)

// SecretSource is the consumer-side seam of internal/secret.Store (named secrets only).
type SecretSource interface {
	Get(ctx context.Context, name string) (string, error)
}

// Unavailable is the typed module_unavailable reason; its text carries no secret.
type Unavailable struct {
	Platform string
	Reason   string
}

// Error implements error.
func (u *Unavailable) Error() string {
	return "module_unavailable{" + u.Reason + "}"
}

// Unwrap makes errors.Is(err, ErrUnavailable) true.
func (u *Unavailable) Unwrap() error { return ErrUnavailable }

// Unavailablef builds a typed unavailable error.
func Unavailablef(platform, format string, args ...any) error {
	return &Unavailable{Platform: platform, Reason: fmt.Sprintf(format, args...)}
}

// RequireEnabled is the first half of explicit activation: zero config is
// disabled, and nothing dials the network until the operator enables it.
func RequireEnabled(platform string, enabled bool) error {
	if enabled {
		return nil
	}
	return Unavailablef(platform, "%s_disabled", platform)
}

// ResolveSecrets loads the declared secrets; a nil source or blank value is
// module_unavailable `<platform>_missing_secret`. The map must not be logged.
func ResolveSecrets(ctx context.Context, d *Descriptor, src SecretSource) (map[string]string, error) {
	if d == nil {
		return nil, fmt.Errorf("%w: nil descriptor", ErrMissingSecret)
	}
	out := make(map[string]string, len(d.Secrets))
	for _, name := range d.Secrets {
		if src == nil {
			return nil, fmt.Errorf("%w: %w", ErrMissingSecret, Unavailablef(d.Name, "%s_missing_secret", d.Name))
		}
		value, err := src.Get(ctx, name)
		if err != nil || strings.TrimSpace(value) == "" {
			return nil, fmt.Errorf("%w: %w", ErrMissingSecret, Unavailablef(d.Name, "%s_missing_secret", d.Name))
		}
		out[name] = value
	}
	return out, nil
}

// Pairable enforces GAP-SEC-10: weak or unknown identities never pair.
func Pairable(d *Descriptor) error {
	if d == nil {
		return fmt.Errorf("%w: nil descriptor", ErrWeakIdentity)
	}
	if gateway.CanPair(d.Caps.IdentityStrength) {
		return nil
	}
	return fmt.Errorf("%w: %s strength %q", ErrWeakIdentity, d.Name, d.Caps.IdentityStrength)
}

// SessionKey builds the account-scoped session identity; a nickname is never one.
func SessionKey(platform, account string) (string, bool) {
	if platform == "" || strings.TrimSpace(account) == "" || strings.ContainsAny(account, "\x00\r\n") {
		return "", false
	}
	return platform + ":" + account, true
}
