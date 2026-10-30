package render

import (
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases catalogs the F4 Telegram rendering rows owned by this package
// (ADR-061, ADR-077, ADR-080): a CRLF fence closes (EC-PLAT-009), a triple
// backtick inside a line is inline code (EC-PLAT-010), the 4096 limit counts
// UTF-16 units without splitting a surrogate pair (EC-PLAT-011), the approval
// card is measured after escaping (EC-PLAT-012) and an invalid anchor is
// dropped instead of failing the send (EC-PLAT-018). Each row runs the test
// that already proves the behaviour.
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	if g.Item(t, "EC-PLAT-009", "F4", "covered") {
		t.Run("EC-PLAT-009", TestFenceClosingCRLF)
	}
	if g.Item(t, "EC-PLAT-010", "F4", "covered") {
		t.Run("EC-PLAT-010", TestInlineBackticksNotPre)
	}
	if g.Item(t, "EC-PLAT-011", "F4", "covered") {
		t.Run("EC-PLAT-011", TestChunkUTF16)
	}
	if g.Item(t, "EC-PLAT-012", "F4", "covered") {
		t.Run("EC-PLAT-012", TestMeasureCardAfterEscape)
	}
	if g.Item(t, "EC-PLAT-018", "F4", "covered") {
		t.Run("EC-PLAT-018", TestAnchorInvalid)
	}
}
