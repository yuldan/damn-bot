package loop

import (
	"testing"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// TestLedgerAnchorsLLMCallEvent: the usage row receives the storage id of the
// llm_call event, not a placeholder.
func TestLedgerAnchorsLLMCallEvent(t *testing.T) {
	t.Parallel()
	f := newFixture()
	f.run(t)
	var want int64
	f.store.mu.Lock()
	for i, ev := range f.store.events {
		if ev.Kind == event.LLMCall {
			want = int64(i + 1)
			break
		}
	}
	f.store.mu.Unlock()
	if want == 0 {
		t.Fatal("no llm_call event appended")
	}
	f.ledger.mu.Lock()
	defer f.ledger.mu.Unlock()
	if len(f.ledger.eventIDs) != 1 || f.ledger.eventIDs[0] != want {
		t.Fatalf("ledger event ids = %v, want [%d]", f.ledger.eventIDs, want)
	}
}
