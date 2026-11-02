//go:build !minimal

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
	"github.com/damnbot/damnbot/internal/provider/registry"
)

// wireClient is the client seam of the full build: the single-credential
// OpenAI client or the rotation-aware Pooled wrapper.
type wireClient interface {
	Do(context.Context, provider.Purpose, *provider.Request, func(provider.Chunk)) (provider.Response, error)
}

// providerAdapter is the full-build adapter; client carries the pooled client
// when the provider declares credentials.
type providerAdapter struct {
	client   wireClient
	model    string
	lightRef string
	notes    *budgetNotes
	provider string
	origin   string
}

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
	adapter := &providerAdapter{client: openai, model: modelRef, lightRef: lightRef, notes: notes, provider: provName, origin: o.sessionOrigin}
	pools, err := providerPools(cfg)
	if err != nil {
		return nil, err
	}
	if len(pools) == 0 {
		return adapter, nil
	}
	pooled, err := provider.NewPooled(openai, pools, func(e provider.CredentialEvent) {
		slog.Warn("credential rotated", "provider", e.Provider, "from", e.From, "to", e.To,
			"reason", e.Reason, "cooldown", e.Cooldown, "attempt", e.Attempt)
	})
	if err != nil {
		return nil, fmt.Errorf("credential pool: %w", err)
	}
	adapter.client = pooled
	return adapter, nil
}

// providerPools resolves one pool per provider that declares credentials.
// The map is keyed by provider id, so every model/fallback/light route of
// that provider observes the same cooldowns.
func providerPools(cfg *config.Config) (map[string]*credpool.Pool, error) {
	return providerPoolsWith(cfg, keychainValue)
}

// providerPoolsWith is the injectable form used by tests.
func providerPoolsWith(cfg *config.Config, get func(string) (string, error)) (map[string]*credpool.Pool, error) {
	pools := make(map[string]*credpool.Pool)
	for i := range cfg.Providers.Custom {
		c := &cfg.Providers.Custom[i]
		if c.Credentials == nil {
			continue
		}
		creds := make([]credpool.Credential, 0, len(c.Credentials))
		for _, pc := range c.Credentials {
			id, ok := strings.CutPrefix(pc.Secret, "keychain:")
			if !ok || id == "" {
				return nil, fmt.Errorf("providers.custom[%q].credentials[%q]: must be a keychain:<id> reference", c.Name, pc.ID)
			}
			value, err := get(id)
			if err != nil {
				return nil, fmt.Errorf("providers.custom[%q].credentials[%q]: %w", c.Name, pc.ID, err)
			}
			creds = append(creds, credpool.NewCredential(pc.ID, value))
		}
		pool, err := credpool.New(creds)
		if err != nil {
			return nil, fmt.Errorf("providers.custom[%q]: %w", c.Name, err)
		}
		pools[c.Name] = pool
	}
	return pools, nil
}

// keychainValue resolves one named secret. The caller has no context at
// startup; the store applies its own 10 s cap.
func keychainValue(name string) (string, error) {
	st, err := secretsStore()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	value, err := st.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("keychain %q: %w", name, err)
	}
	return value, nil
}
