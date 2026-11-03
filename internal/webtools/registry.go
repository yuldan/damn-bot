//go:build !minimal

package webtools

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
)

var (
	errNoEngine      = errors.New("webtools: no search engine configured")
	errUnknownEngine = errors.New("webtools: unknown search engine")
)

type (
	searchResult struct{ Title, URL, Snippet string }
	backend      interface {
		Search(ctx context.Context, query string, limit int) ([]searchResult, error)
	}
)

type engine struct {
	id      string
	backend backend
}
type textCleaner struct {
	block, tag *regexp.Regexp
}

func newTextCleaner() textCleaner {
	return textCleaner{
		block: regexp.MustCompile(`(?is)<(?:script|style)\b[^>]*>.*?</(?:script|style)\s*>`),
		tag:   regexp.MustCompile(`(?is)<[^>]*>`),
	}
}

func (c textCleaner) text(s string) string {
	s = c.block.ReplaceAllString(s, " ")
	s = c.tag.ReplaceAllString(s, " ")
	return strings.Join(strings.Fields(html.UnescapeString(s)), " ")
}

type registry struct {
	engines []engine
	clean   textCleaner
}

func newRegistry(ids []string, cfg backendConfig) (*registry, error) {
	reg := &registry{clean: newTextCleaner()}
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
	if !knownEngine(id) {
		return nil, fmt.Errorf("%w: %q", errUnknownEngine, id)
	}
	base := cfg.base[id]
	if base == "" {
		base = defaultBase(id)
	}
	shared := keyedBackend{
		httpBackend: httpBackend{id: id, client: cfg.client, base: strings.TrimRight(base, "/"), timeout: cfg.timeout},
		key:         cfg.key,
	}
	if b := restBackendFor(id, shared); b != nil {
		return b, nil
	}
	return nil, fmt.Errorf("%w: %q", errUnknownEngine, id)
}

// Search tries engines in order. Runtime failures fall through; a missing
// credential is terminal so the selected engine is never silently replaced.
func (r *registry) Search(ctx context.Context, query string, limit int) ([]searchResult, string, error) {
	if len(r.engines) == 0 {
		return nil, "", errNoEngine
	}
	limit = min(limit, maxSearchHits)
	var last error
	for _, e := range r.engines {
		hits, err := e.backend.Search(ctx, query, limit)
		if err == nil {
			return normalizeHits(r.clean, hits, limit), e.id, nil
		}
		last = fmt.Errorf("webtools: %s: %w", e.id, err)
		if errors.Is(err, errEngineUnavailable) || ctx.Err() != nil {
			return nil, "", last
		}
	}
	return nil, "", last
}

func normalizeHits(clean textCleaner, hits []searchResult, limit int) []searchResult {
	out := make([]searchResult, 0, min(len(hits), limit))
	seen := make(map[string]struct{}, len(hits))
	for _, hit := range hits {
		if len(out) >= limit {
			break
		}
		link, ok := httpResultURL(hit.URL)
		if !ok {
			continue
		}
		if _, duplicate := seen[link]; duplicate {
			continue
		}
		seen[link] = struct{}{}
		title := clip(clean.text(hit.Title))
		if title == "" {
			title = clip(link)
		}
		out = append(out, searchResult{Title: title, URL: link, Snippet: clip(clean.text(hit.Snippet))})
	}
	return out
}

func httpResultURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return "", false
	}
	return u.String(), true
}

type fakeSearch struct{}

func (fakeSearch) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("webtools: search: %w", err)
	}
	hits := make([]searchResult, 0, min(limit, 3))
	for i := 1; i <= min(limit, 3); i++ {
		hits = append(hits, searchResult{Title: fmt.Sprintf("Result %d", i), URL: fmt.Sprintf("https://fake.invalid/%d", i), Snippet: strings.TrimSpace(query)})
	}
	return hits, nil
}
