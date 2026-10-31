//go:build minimal

package main

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/registry"
)

// TestMinimalAdapterRefusal proves the minimal build (core + code tools +
// TUI + ACP) compiles only openai-chat and refuses every other protocol with
// an explicit full-build message instead of reaching a missing encoder.
func TestMinimalAdapterRefusal(t *testing.T) {
	for _, p := range []registry.Protocol{
		registry.ProtocolAnthropicMessages, registry.ProtocolOpenAIResponses,
		registry.ProtocolGemini, registry.ProtocolBedrock,
	} {
		if _, ok := providerWires()[p]; ok {
			t.Fatalf("minimal build registered %s", p)
		}
		err := adapterGate(p)
		if err == nil || !strings.Contains(err.Error(), "tidak tersedia di build minimal; gunakan build penuh") {
			t.Fatalf("%s gate = %v", p, err)
		}
	}
	if err := adapterGate(registry.ProtocolAnthropicMessages); !strings.Contains(err.Error(), "adapter anthropic ") {
		t.Fatalf("anthropic gate = %v", err)
	}
}
