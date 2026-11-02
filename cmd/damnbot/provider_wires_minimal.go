//go:build minimal

package main

import (
	"fmt"
	"log/slog"
	"strings"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// providerAdapter is the single-credential adapter of the minimal build.
type providerAdapter struct {
	client   *provider.OpenAI
	model    string
	lightRef string
	notes    *budgetNotes
	provider string
	origin   string
}

//line run.go:377

// newRunProviderWithTable is newRunProvider with an explicit price table.
func newRunProviderWithTable(cfg *config.Config, db *ctxdb.Store, modelRef string, o *runOptions, table registry.PriceTable) (loop.Provider, error) {
	if o.prov != nil {
		return o.prov, nil
	}
	resolver, err := newRunResolver(cfg, table, o)
	if err != nil {
		return nil, err
	}
	if verr := validateCustomOverrides(cfg, resolver.catalog); verr != nil {
		return nil, verr
	}
	routes, fallback, err := resolver.routes(modelRef)
	if err != nil {
		return nil, err
	}
	lightRef := lightRoute(cfg, modelRef, &routes, resolver, fallback)
	guard, notes, err := newBudgetGuard(cfg, db)
	if err != nil {
		return nil, err
	}
	provName, _, _ := strings.Cut(modelRef, "/")
	net := netx.New(netx.Config{Offline: o.offline || cfg.Network.Offline, NoBackground: !cfg.Network.Background})
	openai, err := provider.NewOpenAI(net.Client("inference", netx.Inference), wireCircuit(&provider.Config{
		Routes: routes, Fallback: fallback, UserAgent: userAgent(),
		Attempts: o.providerAttempts,
		Wires:    providerWires(),
		Guard:    guard, OnBudget: func(d budget.Decision) { notes.observe(&d) },
		OnFallback: func(fb provider.Fallback) {
			if fb.Done {
				slog.Warn("provider fallback", "from", fb.From, "to", fb.To, "error", fb.Outcome)
			}
		},
	}, db))
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return &providerAdapter{client: openai, model: modelRef, lightRef: lightRef, notes: notes, provider: provName, origin: o.sessionOrigin}, nil
}

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
