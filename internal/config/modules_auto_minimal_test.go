//go:build minimal

package config

import (
	"errors"
	"testing"
)

// TestAutoConfigUnavailableMinimal: the minimal binary never accepts
// [modules.auto] (ADR-269/ADR-333); the key is ErrModuleUnavailable.
func TestAutoConfigUnavailableMinimal(t *testing.T) {
	t.Parallel()
	if _, err := parse(t, "[modules.auto.title]\nenabled = true\n"); !errors.Is(err, ErrModuleUnavailable) {
		t.Fatalf("err = %v, want ErrModuleUnavailable", err)
	}
}
