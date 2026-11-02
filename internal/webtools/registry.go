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

type registry struct{ engines []backend }

func newRegistry(ids []string) (*registry, error) {
	reg := &registry{}
	for _, id := range ids {
		if id != "fake" {
			return nil, fmt.Errorf("%w: %q", errUnknownEngine, id)
		}
		reg.engines = append(reg.engines, fakeSearch{})
	}
	return reg, nil
}

func (r *registry) Search(ctx context.Context, query string, limit int) ([]searchResult, string, error) {
	if len(r.engines) == 0 {
		return nil, "", errNoEngine
	}
	hits, err := r.engines[0].Search(ctx, query, limit)
	if err != nil {
		return nil, "fake", fmt.Errorf("webtools: search: %w", err)
	}
	return hits, "fake", nil
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
