package memory

import (
	"errors"
	"testing"
)

// TestScopeNormalization: project scopes are portable on every OS (leading
// "/" accepted on Windows, slashes canonical, cleaned); relative paths and an
// empty set are rejected.
func TestScopeNormalization(t *testing.T) {
	got, err := normalizeScope("project:/portable/repo/")
	if err != nil || got != "project:/portable/repo" {
		t.Fatalf("normalize = %q %v", got, err)
	}
	if _, err = normalizeScope("project:relative"); !errors.Is(err, ErrInvalid) {
		t.Fatalf("relative scope = %v", err)
	}
	for _, s := range []string{"user", "project:/a", "gateway:telegram:42"} {
		if err = validateScope(s); err != nil {
			t.Errorf("scope %q rejected: %v", s, err)
		}
	}
}
