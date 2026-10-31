//go:build minimal

package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// adapterGate refuses a protocol whose adapter the minimal build omits (P13:
// core + code tools + TUI + ACP only). A provider that needs it fails at
// resolution instead of reaching a missing encoder.
func adapterGate(p registry.Protocol) error {
	name := string(p)
	if p == registry.ProtocolAnthropicMessages {
		name = "anthropic"
	}
	return fmt.Errorf("adapter %s tidak tersedia di build minimal; gunakan build penuh", name)
}

// buildWires has no non-chat adapter: the minimal surface is core + code
// tools + TUI + ACP (P13).
func buildWires() map[registry.Protocol]provider.Wire {
	return map[registry.Protocol]provider.Wire{}
}
