package gwpeer

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases is the F7 edge-case catalog entry for this package (ADR-326):
// the id, phase, and covered status here are what `make phase-acceptance`
// reads. Each subtest calls the same helper as its named F7 test above, so a
// row cannot be cataloged without its assertions.
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	t.Run("EC-STATE-004", func(t *testing.T) {
		if !g.Item(t, "EC-STATE-004", "F7", "covered") {
			return
		}
		checkReadPool(t)
	})
	t.Run("EC-TERM-004", func(t *testing.T) {
		if !g.Item(t, "EC-TERM-004", "F7", "covered") {
			return
		}
		checkSandboxKey(t)
	})
	t.Run("EC-TERM-005", func(t *testing.T) {
		if !g.Item(t, "EC-TERM-005", "F7", "covered") {
			return
		}
		checkPolicyResolve(t)
	})
}
