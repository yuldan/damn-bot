package e2e

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestCacheBreakpointGenericWire (MEM-034) is the negative control for the
// family gate: the production runner marks the system message as the cache
// boundary, but the fake provider's route is not the anthropic family, so the
// wire body keeps plain string content and carries no cache_control marker.
func TestCacheBreakpointGenericWire(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("ok"))
	e.Run("cache probe").RequireOK(t)
	reqs := e.Requests()
	if len(reqs) != 1 {
		t.Fatalf("got %d provider requests, want 1", len(reqs))
	}
	if strings.Contains(string(reqs[0]), "cache_control") {
		t.Errorf("generic route emitted cache_control:\n%s", reqs[0])
	}
	var body struct {
		Messages []struct {
			Content any `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqs[0], &body); err != nil {
		t.Fatal(err)
	}
	if len(body.Messages) == 0 {
		t.Fatal("request body has no messages")
	}
	for i, m := range body.Messages {
		if _, ok := m.Content.(string); !ok {
			t.Errorf("message %d content changed shape: %#v", i, m.Content)
		}
	}
}
