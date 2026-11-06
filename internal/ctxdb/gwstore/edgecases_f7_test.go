package gwstore

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCasesF7 catalogs the F7 pairing row: concurrent redemption from two
// processes is serialized (EC-PLAT-008).
func TestEdgeCasesF7(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-008", "F7", "covered") {
		t.Run("EC-PLAT-008", TestF7ECPLAT008PairingLock)
	}
}
