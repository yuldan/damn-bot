//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider"
)

// adapterPool is empty: the minimal build links no pooled client.
type adapterPool struct{}

// newRunAdapter returns the single-credential adapter: the minimal build
// rejects providers.custom.credentials, so no pool is linked.
func newRunAdapter(openai *provider.OpenAI, _ *config.Config, modelRef, lightRef string, notes *budgetNotes, provName, origin string) (loop.Provider, error) {
	return &providerAdapter{client: openai, model: modelRef, lightRef: lightRef, notes: notes, provider: provName, origin: origin}, nil
}
