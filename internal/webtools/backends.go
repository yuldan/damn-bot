//go:build !minimal

package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/netx"
)

var errEngineUnavailable = errors.New("webtools: search engine needs a credential")

// backendConfig carries the process-wide dependencies of the HTTP backends.
type backendConfig struct {
	client *netx.Client
	key    func(engine string) (string, error)
	base   map[string]string // test override per engine
}

// keyedBackend is the shared HTTP implementation of the paid backends.
type keyedBackend struct {
	id     string
	client *netx.Client
	key    func(string) (string, error)
	base   string
}

func (b keyedBackend) credential() (string, error) {
	if b.key == nil {
		return "", errEngineUnavailable
	}
	key, err := b.key(b.id)
	if err != nil {
		return "", fmt.Errorf("webtools: %s key: %w", b.id, err)
	}
	if key == "" {
		return "", errEngineUnavailable
	}
	return key, nil
}

func (b keyedBackend) request(ctx context.Context, path string, payload any, hdr map[string]string) ([]byte, error) {
	var body io.Reader
	if payload != nil {
		raw, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("webtools: %s encode: %w", b.id, err)
		}
		body = bytes.NewReader(raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.base+path, body)
	if err != nil {
		return nil, fmt.Errorf("webtools: %s request: %w", b.id, err)
	}
	if payload != nil {
		req.Method = http.MethodPost
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.client.DoOK(req)
	if err != nil {
		return nil, fmt.Errorf("webtools: %s: %w", b.id, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("webtools: %s body: %w", b.id, err)
	}
	return raw, nil
}

func (b keyedBackend) get(ctx context.Context, path string, hdr map[string]string) ([]byte, error) {
	return b.request(ctx, path, nil, hdr)
}

func (b keyedBackend) post(ctx context.Context, path string, payload any, hdr map[string]string) ([]byte, error) {
	return b.request(ctx, path, payload, hdr)
}

type braveBackend struct{ keyedBackend }

func (b braveBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	key, err := b.credential()
	if err != nil {
		return nil, err
	}
	path := "/res/v1/web/search?q=" + url.QueryEscape(query) + "&count=" + strconv.Itoa(limit)
	raw, err := b.get(ctx, path, map[string]string{"X-Subscription-Token": key, "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Web struct {
			Results []struct{ Title, URL, Description string }
		}
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("webtools: brave decode: %w", err)
	}
	hits := make([]searchResult, 0, len(out.Web.Results))
	for _, r := range out.Web.Results {
		hits = append(hits, searchResult{Title: clip(r.Title), URL: r.URL, Snippet: clip(r.Description)})
	}
	return hits, nil
}

type tavilyBackend struct{ keyedBackend }

func (b tavilyBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	key, err := b.credential()
	if err != nil {
		return nil, err
	}
	raw, err := b.post(ctx, "/search", map[string]any{
		"api_key": key, "query": query, "max_results": limit,
	}, map[string]string{"Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct{ Title, URL, Content string }
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("webtools: tavily decode: %w", err)
	}
	hits := make([]searchResult, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, searchResult{Title: clip(r.Title), URL: r.URL, Snippet: clip(r.Content)})
	}
	return hits, nil
}

type exaBackend struct{ keyedBackend }

func (b exaBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	key, err := b.credential()
	if err != nil {
		return nil, err
	}
	raw, err := b.post(ctx, "/search", map[string]any{
		"query": query, "numResults": limit,
	}, map[string]string{"x-api-key": key, "Accept": "application/json"})
	if err != nil {
		return nil, err
	}
	var out struct {
		Results []struct{ Title, URL, Text string }
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("webtools: exa decode: %w", err)
	}
	hits := make([]searchResult, 0, len(out.Results))
	for _, r := range out.Results {
		hits = append(hits, searchResult{Title: clip(r.Title), URL: r.URL, Snippet: clip(r.Text)})
	}
	return hits, nil
}

// defaultBase is the production endpoint of one backend.
func defaultBase(id string) string {
	switch id {
	case "brave":
		return "https://api.search.brave.com"
	case "tavily":
		return "https://api.tavily.com"
	case "exa":
		return "https://api.exa.ai"
	}
	return ""
}

// clip bounds every model-facing title/snippet (SPEC §9.4: ≤ 300 chars).
func clip(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 300 {
		return string(r)
	}
	return string(r[:300])
}
