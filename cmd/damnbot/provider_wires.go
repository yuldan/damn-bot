package main

import (
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/provider/responses"
)

// providerWires is the protocol → wire adapter table of the composition root
// (F5-04). openai-chat stays the default for a route without Protocols; a
// catalog preset that speaks openai-responses selects this adapter.
func providerWires() map[registry.Protocol]provider.Wire {
	return map[registry.Protocol]provider.Wire{
		registry.ProtocolOpenAIResponses: responses.New(),
	}
}
