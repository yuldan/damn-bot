//go:build minimal

package config

import (
	"strings"
	"testing"
)

// TestF6ModuleSectionsUnavailable: the minimal binary does not compile the F6
// modules, so their sections stay ErrModuleUnavailable even when disabled
// (ADR-269 pattern, ADR-310). BurntSushi reports the unknown table itself, so
// the key is checked by prefix.
func TestF6ModuleSectionsUnavailable(t *testing.T) {
	t.Parallel()
	for _, id := range []string{"web", "browser", "media", "voice", "computer_use"} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, "[modules."+id+"]\nenabled = false\n")
			ke := keyErrorOf(t, err, ErrModuleUnavailable)
			if !strings.HasPrefix(ke.Key, "modules."+id) {
				t.Errorf("key = %q, want prefix modules.%s", ke.Key, id)
			}
		})
	}
}
