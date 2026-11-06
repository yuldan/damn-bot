package gwpeer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
)

// ErrPolicyUnresolved is the EC-TERM-005 fail-closed verdict: a terminal
// policy that cannot be resolved for the requested profile is never replaced
// by an ambient/default value.
var ErrPolicyUnresolved = errors.New("gwpeer: terminal policy unresolved")

// ErrInvalidSandbox rejects a profile/session/task key that could escape the
// qualified namespace.
var ErrInvalidSandbox = errors.New("gwpeer: invalid sandbox qualification")

// SandboxKey qualifies a task key by profile and session, so the same task id
// in two profiles can never share a sandbox, cwd, or lock (EC-TERM-004).
func SandboxKey(profile, session, task string) (string, error) {
	return sandboxQualified("key", profile, session, task)
}

// SandboxDir returns the profile/session-qualified directory under root. It is
// the cwd a terminal task may use; the task id never reuses a sibling
// profile's directory.
func SandboxDir(root, profile, session string) (string, error) {
	if root == "" {
		return "", fmt.Errorf("%w: root is required", ErrInvalidSandbox)
	}
	q, err := sandboxQualified("dir", profile, session, "")
	if err != nil {
		return "", err
	}
	parts := strings.Split(q, "\x1f")
	return filepath.Join(root, parts[0], parts[1]), nil
}

func sandboxQualified(kind, profile, session, task string) (string, error) {
	for _, f := range [...]struct{ name, value string }{
		{"profile", profile}, {"session", session},
	} {
		if f.value == "" || len(f.value) > 256 || strings.ContainsAny(f.value, "\x00\r\n/\\") || f.value == "." || f.value == ".." {
			return "", fmt.Errorf("%w: %s is malformed", ErrInvalidSandbox, f.name)
		}
	}
	if kind == "key" && task == "" {
		return "", fmt.Errorf("%w: task is required", ErrInvalidSandbox)
	}
	sum := sha256.Sum256([]byte(profile + "\x00" + session + "\x00" + task))
	return profile + "\x1f" + session + "\x1f" + hex.EncodeToString(sum[:16]), nil
}

// TerminalPolicy is the per-profile shell policy a turn may use.
type TerminalPolicy struct {
	Profile          string
	Shell            string
	AllowDestructive bool
}

// PolicyResolver resolves a policy for exactly one profile. found=false is an
// ordinary "no policy for this profile" answer.
type PolicyResolver interface {
	ResolveTerminalPolicy(ctx context.Context, profile string) (policy TerminalPolicy, found bool, err error)
}

// ResolveTerminalPolicy enforces EC-TERM-005: a missing resolver, a resolver
// error, an absent policy, or a policy of another profile yields
// ErrPolicyUnresolved and the zero policy. There is no ambient fallback.
func ResolveTerminalPolicy(ctx context.Context, resolver PolicyResolver, profile string) (TerminalPolicy, error) {
	if resolver == nil || profile == "" {
		return TerminalPolicy{}, ErrPolicyUnresolved
	}
	policy, found, err := resolver.ResolveTerminalPolicy(ctx, profile)
	if err != nil {
		return TerminalPolicy{}, fmt.Errorf("%w: %w", ErrPolicyUnresolved, err)
	}
	if !found || policy.Profile != profile {
		return TerminalPolicy{}, fmt.Errorf("%w: %s", ErrPolicyUnresolved, profile)
	}
	return policy, nil
}
