//go:build !minimal

package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

// Config is [modules.web] as decoded by the composition root.
type Config struct {
	Enabled, Offline bool
	Engines          []string
	UserAgent        string
	MaxOutput        int
	policy           hostPolicy
}

// Tool is one deferred catalog entry with effect "read".
type Tool struct {
	Name, Description, Effect string
	Schema                    json.RawMessage
}

// Tools returns the module's deferred tools in stable order.
func Tools() []Tool {
	return []Tool{
		{
			"web_fetch", "Fetch one public http(s) page and return its main text as markdown", "read",
			json.RawMessage(`{"type":"object","properties":{"url":{"type":"string","maxLength":2048}},"required":["url"],"additionalProperties":false}`),
		},
		{
			"web_search", "Search the web with the configured engine", "read",
			json.RawMessage(`{"type":"object","properties":{"query":{"type":"string","maxLength":512},"limit":{"type":"integer","minimum":1,"maximum":20}},"required":["query"],"additionalProperties":false}`),
		},
	}
}

// Module is the F6 web module; Start/Status/Invoke/Stop match modules.Module.
type Module struct {
	client   *netx.Client
	cfg      Config
	registry *registry
	extract  *extractor
	fetcher  *fetcher
	status   host.Status
}

// New validates cfg; unknown engines fail even when the module is disabled.
func New(client *netx.Client, cfg Config) (*Module, error) {
	if cfg.Enabled && client == nil {
		return nil, fmt.Errorf("%w: nil netx client", errBadConfig)
	}
	reg, err := newRegistry(cfg.Engines)
	if err != nil {
		return nil, err
	}
	return &Module{
		client: client, cfg: cfg, registry: reg,
		extract: newExtractor(0), status: host.StatusStopped,
	}, nil
}

// Start binds the fetcher; a disabled module stays stopped.
func (m *Module) Start(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("webtools: start: %w", err)
	}
	if !m.cfg.Enabled {
		m.status = host.StatusStopped
		return nil
	}
	fetcher, err := newFetcher(m.client, fetchConfig{
		Offline:   m.cfg.Offline,
		UserAgent: m.cfg.UserAgent, policy: m.cfg.policy,
	})
	if err != nil {
		m.status = host.StatusFailed
		return err
	}
	m.fetcher, m.status = fetcher, host.StatusReady
	return nil
}

// Status reports the lifecycle state.
func (m *Module) Status() host.Status { return m.status }

// Stop is idempotent and bounded; the module owns no goroutines.
func (m *Module) Stop(context.Context) error {
	m.fetcher, m.status = nil, host.StatusStopped
	return nil
}

// Invoke dispatches one approved read call.
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, _ host.Env) (host.Result, error) {
	if m.status != host.StatusReady || m.fetcher == nil {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "web module is not enabled: " + tool}
	}
	switch tool {
	case "web_fetch":
		return m.fetch(ctx, args)
	case "web_search":
		return m.search(ctx, args)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown web tool: " + tool}
}

func (m *Module) fetch(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if a.URL == "" || len(a.URL) > 2048 {
		return host.Result{}, invalidArgs(errors.New("url must be 1..2048 characters"))
	}
	page, err := m.fetcher.Fetch(ctx, a.URL)
	if err != nil {
		return host.Result{}, fetchError(err)
	}
	doc, err := m.extract.Extract(page.ContentType, page.Body)
	if err != nil {
		return host.Result{}, fetchError(err)
	}
	return host.Result{Content: doc.Markdown, Meta: map[string]any{
		"url": page.URL, "status": page.Status, "content_type": page.ContentType,
		"charset": doc.Charset, "truncated": doc.Truncated,
	}, UntrustedSource: "web:" + page.Host}, nil
}

func (m *Module) search(ctx context.Context, raw json.RawMessage) (host.Result, error) {
	var a struct {
		Query string `json:"query"`
		Limit int    `json:"limit"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	query := strings.TrimSpace(a.Query)
	if query == "" || len(query) > 512 {
		return host.Result{}, invalidArgs(errors.New("query must be 1..512 characters"))
	}
	limit := a.Limit
	if limit == 0 {
		limit = 5
	}
	if limit < 1 || limit > 20 {
		return host.Result{}, invalidArgs(errors.New("limit must be in 1..20"))
	}
	hits, engine, err := m.registry.Search(ctx, query, limit)
	if err != nil {
		if errors.Is(err, errNoEngine) {
			return host.Result{}, &host.Error{Code: "module_unavailable", Message: "no search engine is configured"}
		}
		return host.Result{}, &host.Error{Code: "network", Message: err.Error()}
	}
	var b strings.Builder
	for i, hit := range hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, hit.Title, hit.URL, hit.Snippet)
	}
	return host.Result{Content: strings.TrimRight(b.String(), "\n"), Meta: map[string]any{
		"count": len(hits), "engine": engine,
	}, UntrustedSource: "web_search:" + engine}, nil
}

func decodeArgs(raw json.RawMessage, dst any) error {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("webtools: arguments: %w", err)
	}
	return nil
}
func invalidArgs(err error) error { return &host.Error{Code: "invalid_args", Message: err.Error()} }
func fetchError(err error) error {
	code := "network"
	switch {
	case errors.Is(err, errOffline):
		code = "offline_mode"
	case errors.Is(err, errBlockedHost):
		code = "blocked_host"
	case errors.Is(err, errTimeout):
		code = "timeout"
	case errors.Is(err, errTooLarge):
		code = "too_large"
	case errors.Is(err, errBadURL), errors.Is(err, errScheme), errors.Is(err, errBinary):
		code = "invalid_args"
	}
	return &host.Error{Code: code, Message: err.Error()}
}
