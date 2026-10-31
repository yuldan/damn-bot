package main

import (
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// providerWires is the protocol → adapter table of the composition root
// (F5-04/F5-05). openai-chat is the zero-config default; a build-tagged
// implementation adds the optional non-chat adapters (P13).
func providerWires() map[registry.Protocol]provider.Wire { return buildWires() }
