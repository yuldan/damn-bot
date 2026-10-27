package main

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider"
)

// maxContextWindow is the ADR-194 butir 3 upper bound for a per-model
// context_length; a larger value is a typo, not a model.
const maxContextWindow = 10_000_000

// routeLimits builds the static config windows of one run route from
// [[providers.custom]] (SPEC §12; ADR-194 butir 3/4). An entry applies only
// when its normalized base_url and model match the route, so a model with the
// same id on another endpoint is never honored. context_length 0 is the
// documented "no override" (the resolver then uses registry/learned/32768).
//
// Duplicate matching entries must agree, and a matching window above the
// ADR-194 maximum is refused. A window below the resolver's minimum is left
// to registry.NewLimits, which rejects it as well: a wrong override fails the
// start instead of being silently replaced by the fallback.
func routeLimits(cfg *config.Config, providerName, baseURL, model string) (map[string]int64, error) {
	if cfg == nil || baseURL == "" || model == "" {
		return nil, nil
	}
	route, err := provider.NormalizeBaseURL(baseURL)
	if err != nil {
		return nil, fmt.Errorf("route base_url: %w", err)
	}
	key := providerName + "/" + model
	var out map[string]int64
	for i := range cfg.Providers.Custom {
		window, matched, err := providerWindow(&cfg.Providers.Custom[i], i, route, model)
		if err != nil {
			return nil, err
		}
		if !matched {
			continue
		}
		if prev, ok := out[key]; ok && prev != window {
			return nil, fmt.Errorf("providers.custom: conflicting context_length %d and %d for %s on %s", prev, window, key, route)
		}
		if out == nil {
			out = make(map[string]int64, 1)
		}
		out[key] = window
	}
	return out, nil
}

// providerWindow returns the override one custom provider declares for model
// on route. matched is false when the provider is for another endpoint or
// does not override this model.
func providerWindow(p *config.CustomProvider, i int, route, model string) (window int64, matched bool, err error) {
	base, err := provider.NormalizeBaseURL(p.BaseURL)
	if err != nil {
		return 0, false, fmt.Errorf("providers.custom[%d].base_url: %w", i, err)
	}
	if base != route {
		return 0, false, nil
	}
	for j := range p.Models {
		m := &p.Models[j]
		if m.ID != model || m.ContextLength == 0 {
			continue
		}
		if m.ContextLength > maxContextWindow {
			return 0, false, fmt.Errorf("providers.custom[%d].models[%d]: context_length %d > %d", i, j, m.ContextLength, maxContextWindow)
		}
		if window != 0 && window != int64(m.ContextLength) {
			return 0, false, fmt.Errorf("providers.custom[%d]: conflicting context_length %d and %d for %s", i, window, m.ContextLength, model)
		}
		window = int64(m.ContextLength)
	}
	return window, window != 0, nil
}
