package e2e

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases catalogs the F4 Telegram entries proven end to end: the topic
// target keeps its thread id through the real adapter (EC-PLAT-003).
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-003", "F4", "covered") {
		t.Run("EC-PLAT-003", TestTelegramAdapterPollsAndRepliesInTopic)
	}
}
