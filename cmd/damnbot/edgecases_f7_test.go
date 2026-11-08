package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCasesF7 catalogs the F7 platform rows proven in the serve package:
// an empty provider reply still notifies (EC-PLAT-004), a message after /stop
// runs as a new generation (EC-PLAT-005), and a webhook turn can neither
// execute a shell command nor write a file (EC-SEC-006).
func TestEdgeCasesF7(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-004", "F7", "covered") {
		t.Run("EC-PLAT-004", TestF7ECPLAT004EmptyReplyNotice)
	}
	if g.Item(t, "EC-PLAT-005", "F7", "covered") {
		t.Run("EC-PLAT-005", TestF7ECPLAT005AfterStop)
	}
	if g.Item(t, "EC-SEC-006", "F7", "covered") {
		t.Run("EC-SEC-006", TestF7ECSEC006WebhookToolset)
	}
}
