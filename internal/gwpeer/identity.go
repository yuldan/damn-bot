package gwpeer

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
)

// ErrTokenMissing reports a secret reference that resolved to no value. The
// transport never falls back to an empty token.
var ErrTokenMissing = errors.New("gwpeer: peer token unavailable")

// TokenSource resolves a token reference to its secret value at send time.
// Implementations must not cache a value beyond the operation.
type TokenSource interface {
	Token(ctx context.Context, ref string) (string, error)
}

// EnvTokens resolves a reference as an environment variable name. This is the
// CLI default; the serve composition root can supply the key store instead.
type EnvTokens struct{}

// Token implements TokenSource.
func (EnvTokens) Token(_ context.Context, ref string) (string, error) {
	if !envName(ref) {
		return "", fmt.Errorf("%w: %q is not an environment variable name", ErrTokenMissing, ref)
	}
	v := strings.TrimSpace(config.OS().Getenv(ref))
	if v == "" {
		return "", fmt.Errorf("%w: %s", ErrTokenMissing, ref)
	}
	return v, nil
}

// envName reports whether s is a portable environment variable name.
func envName(s string) bool {
	if s == "" || len(s) > 128 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c == '_':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
