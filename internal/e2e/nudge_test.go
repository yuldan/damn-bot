package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestMemoryNudge (MEM-003): after ten user turns the next resumed request
// carries the memory nudge in the frozen context facts.
func TestMemoryNudge(t *testing.T) {
	t.Parallel()
	replies := make([]providertest.Reply, 11)
	for i := range replies {
		replies[i] = providertest.Text("ok")
	}
	e := New(t, replies...)
	e.Run("turn 1").RequireOK(t)
	for i := 2; i <= 11; i++ {
		e.Run("turn "+itoa(i), "--continue").RequireOK(t)
	}
	last := len(e.Requests()) - 1
	if !e.Contains(t, last, "memory_nudge") {
		t.Errorf("request %d lacks the memory nudge:\n%s", last, e.RequestText(t, last))
	}
	if e.Contains(t, 1, "memory_nudge") {
		t.Error("nudge appeared before the interval")
	}
	if !strings.Contains(e.RequestText(t, last), "turn 11") {
		t.Error("last prompt not present")
	}
}

// itoa formats a small int without importing strconv in the test file.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [4]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
