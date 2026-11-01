//go:build !minimal

package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

// newRunAdapter wraps the client with one credential pool per provider. Models,
// fallback, and light routes of the same provider share that pool.
func newRunAdapter(openai *provider.OpenAI, cfg *config.Config, modelRef, lightRef string, notes *budgetNotes, provName, origin string) (loop.Provider, error) {
	pools, err := providerPools(cfg)
	if err != nil {
		return nil, err
	}
	adapter := &providerAdapter{client: openai, model: modelRef, lightRef: lightRef, notes: notes, provider: provName, origin: origin}
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
	adapter.pooled = pooled
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

// adapterPool carries the optional pooled client of the full build.
type adapterPool struct {
	pooled *provider.Pooled
}
