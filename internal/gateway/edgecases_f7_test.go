package gateway_test

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCasesF7 catalogs the F7 gateway row this package owns: a new turn
// never inherits the previous turn's media (EC-PLAT-006).
func TestEdgeCasesF7(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-006", "F7", "covered") {
		t.Run("EC-PLAT-006", TestF7ECPLAT006MediaIsolation)
	}
}
