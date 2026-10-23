package memory

import (
	"fmt"
	"path"
	"path/filepath"
	"strings"
)

// normalizeScope accepts the three SPEC §4.8 scope shapes and returns a
// portable form: "project:" paths are cleaned and slash-separated (C:\x →
// C:/x on Windows, /x unchanged), so the same scope compares equal on every
// OS. A leading "/" is accepted as an absolute portable path even on
// Windows, where filepath.IsAbs would reject it.
func normalizeScope(scope string) (string, error) {
	if scope == "user" {
		return scope, nil
	}
	if rest, ok := strings.CutPrefix(scope, "project:"); ok {
		if rest == "" {
			return "", fmt.Errorf("%w: empty project path", ErrInvalid)
		}
		p := filepath.ToSlash(filepath.Clean(rest))
		if !path.IsAbs(p) && !filepath.IsAbs(filepath.FromSlash(p)) {
			return "", fmt.Errorf("%w: project scope needs an absolute path", ErrInvalid)
		}
		return "project:" + p, nil
	}
	if rest, ok := strings.CutPrefix(scope, "gateway:"); ok {
		platform, identity, found := strings.Cut(rest, ":")
		if found && platform != "" && identity != "" {
			return scope, nil
		}
	}
	return "", fmt.Errorf("%w: scope %q", ErrInvalid, scope)
}

// validateScope is normalizeScope without keeping the normalized form.
func validateScope(scope string) error {
	_, err := normalizeScope(scope)
	return err
}

// NormalizeScope returns the portable form of scope the store compares
// (ErrInvalid for anything else), so callers can match a record's scope.
func NormalizeScope(scope string) (string, error) { return normalizeScope(scope) }
