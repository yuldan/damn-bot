package e2e

import (
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// nudgeText is the exact memory reminder (MEM-003).
const nudgeText = "memory_nudge=consider storing durable facts with the memory tool"

// TestMemoryNudge (MEM-003): the memory nudge lands in the frozen context facts
// of the 10th and 20th user turn of a resumed session and of no other turn, and
// it costs no provider call (exactly one request per turn). Every turn is a
// fresh process, so the count is read back from the stored transcript.
func TestMemoryNudge(t *testing.T) {
	t.Parallel()
	const turns = 21
	replies := make([]providertest.Reply, turns)
	for i := range replies {
		replies[i] = providertest.Text("ok")
	}
	e := New(t, replies...)
	for i := 1; i <= turns; i++ {
		var extra []string
		if i > 1 {
			extra = []string{"--continue"}
		}
		e.Run(fmt.Sprintf("turn %d", i), extra...).RequireOK(t)
	}
	reqs := e.Requests()
	if len(reqs) != turns {
		t.Fatalf("got %d provider requests for %d turns; the nudge must not add calls", len(reqs), turns)
	}
	for i, body := range reqs {
		turn := i + 1
		got := strings.Contains(string(body), nudgeText)
		if want := turn%10 == 0; got != want {
			t.Errorf("turn %d: nudge=%v, want %v", turn, got, want)
		}
	}
	if !strings.Contains(string(reqs[9]), "turn 10") || !strings.Contains(string(reqs[20]), "turn 21") {
		t.Error("request bodies do not carry the prompts they answered")
	}
}
