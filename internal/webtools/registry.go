//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

var (
	errNoEngine      = errors.New("webtools: no search engine configured")
	errUnknownEngine = errors.New("webtools: unknown search engine")
)

type searchResult struct{ Title, URL, Snippet string }

type backend interface {
	Search(ctx context.Context, query string, limit int) ([]searchResult, error)
}

type engine struct {
	id      string
	backend backend
}

type registry struct{ engines []engine }

func newRegistry(ids []string, cfg backendConfig) (*registry, error) {
	reg := &registry{}
	for _, id := range ids {
		b, err := builtinBackend(id, cfg)
		if err != nil {
			return nil, err
		}
		reg.engines = append(reg.engines, engine{id: id, backend: b})
	}
	return reg, nil
}

func builtinBackend(id string, cfg backendConfig) (backend, error) {
	if id == "fake" {
		return fakeSearch{}, nil
	}
	base := cfg.base[id]
	if base == "" {
		base = defaultBase(id)
	}
	if base == "" {
		return nil, fmt.Errorf("%w: %q", errUnknownEngine, id)
	}
	shared := keyedBackend{id: id, client: cfg.client, key: cfg.key, base: strings.TrimRight(base, "/")}
	switch id {
	case "brave":
		return braveBackend{shared}, nil
	case "tavily":
		return tavilyBackend{shared}, nil
	case "exa":
		return exaBackend{shared}, nil
	}
	return nil, fmt.Errorf("%w: %q", errUnknownEngine, id)
}

// Search tries the configured engines in order. An engine without a
// credential is skipped; the last error is returned when all fail.
func (r *registry) Search(ctx context.Context, query string, limit int) ([]searchResult, string, error) {
	if len(r.engines) == 0 {
		return nil, "", errNoEngine
	}
	var last error
	for _, e := range r.engines {
		hits, err := e.backend.Search(ctx, query, min(limit, 10))
		if err == nil {
			return hits, e.id, nil
		}
		last = fmt.Errorf("webtools: %s: %w", e.id, err)
	}
	return nil, "", last
}

type fakeSearch struct{}

func (fakeSearch) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("webtools: search: %w", err)
	}
	hits := make([]searchResult, 0, min(limit, 3))
	for i := 1; i <= min(limit, 3); i++ {
		hits = append(hits, searchResult{
			Title: fmt.Sprintf("Result %d", i),
			URL:   fmt.Sprintf("https://fake.invalid/%d", i), Snippet: strings.TrimSpace(query),
		})
	}
	return hits, nil
}
