//go:build !minimal

package main

import (
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/anthropic"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/provider/responses"
)

// adapterGate reports why a catalog protocol has no adapter in this build;
// the full build wires every supported non-chat protocol, so it never denies.
func adapterGate(registry.Protocol) error { return nil }

// buildWires wires the optional protocol adapters of the full build.
func buildWires() map[registry.Protocol]provider.Wire {
	return map[registry.Protocol]provider.Wire{
		registry.ProtocolOpenAIResponses:   responses.New(),
		registry.ProtocolAnthropicMessages: anthropic.New(),
	}
}
