//go:build !minimal

package webtools

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

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
	Key              func(engine string) (string, error)
	Base             map[string]string
	blocklist        []string
	cacheEntries     int
	cacheBytes       int
	now              func() time.Time
	searchTTL        time.Duration
	fetchTTL         time.Duration
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
	cache    *cache
	status   host.Status
}

type fetched struct {
	page page
	doc  document
}

type searched struct {
	hits   []searchResult
	engine string
}

// New validates cfg; unknown engines fail even when the module is disabled.
func New(client *netx.Client, cfg *Config) (*Module, error) {
	if cfg == nil || cfg.Enabled && client == nil {
		return nil, fmt.Errorf("%w: nil config or client", errBadConfig)
	}
	reg, err := newRegistry(cfg.Engines, backendConfig{client: client, key: cfg.Key, base: cfg.Base})
	if err != nil {
		return nil, err
	}
	return &Module{
		client: client, cfg: *cfg, registry: reg, extract: newExtractor(cfg.MaxOutput),
		cache:  newCache(cfg.cacheEntries, cfg.cacheBytes, cfg.now),
		status: host.StatusStopped,
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
func (m *Module) Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error) {
	if m.status != host.StatusReady || m.fetcher == nil {
		return host.Result{}, &host.Error{Code: "module_unavailable", Message: "web module is not enabled: " + tool}
	}
	switch tool {
	// SPEC §3.2.4 names the extract tool web_fetch; parity TOOL-086 calls it
	// web_extract. The catalog keeps the SPEC name and the alias dispatches to
	// the same implementation (ADR-323 mapping, documented in the PR).
	case "web_fetch", "web_extract":
		return m.fetch(ctx, env, args)
	case "web_search":
		return m.search(ctx, env, args)
	}
	return host.Result{}, &host.Error{Code: "unknown_tool", Message: "unknown web tool: " + tool}
}

func (m *Module) fetch(ctx context.Context, env host.Env, raw json.RawMessage) (host.Result, error) {
	var a struct {
		URL string `json:"url"`
	}
	if err := decodeArgs(raw, &a); err != nil {
		return host.Result{}, invalidArgs(err)
	}
	if a.URL == "" || len(a.URL) > 2048 {
		return host.Result{}, invalidArgs(errors.New("url must be 1..2048 characters"))
	}
	if m.blocked(a.URL) {
		return host.Result{}, &host.Error{Code: "blocked_host", Message: "URL is blocked by [modules.web].blocklist"}
	}
	value, cached, err := m.load(env, "fetch", a.URL, m.ttl(m.cfg.fetchTTL, 60*time.Minute), func() (any, int, error) {
		page, ferr := m.fetcher.Fetch(ctx, a.URL)
		if ferr != nil {
			return nil, 0, ferr
		}
		doc, xerr := m.extract.Extract(page.ContentType, page.Body)
		if xerr != nil {
			return nil, 0, xerr
		}
		return fetched{page: page, doc: doc}, len(page.Body) + len(doc.Markdown), nil
	})
	if err != nil {
		return host.Result{}, fetchError(err)
	}
	f, ok := value.(fetched)
	if !ok {
		return host.Result{}, &host.Error{Code: "internal", Message: "web cache type mismatch"}
	}
	return host.Result{Content: f.doc.Markdown, Meta: map[string]any{
		"url": f.page.URL, "status": f.page.Status, "content_type": f.page.ContentType,
		"charset": f.doc.Charset, "truncated": f.doc.Truncated, "cached": cached,
		// GAP-TOOL-09: every result names the backend that produced it. The
		// extractor is the built-in Go pipeline, not a search vendor.
		"backend": "native",
	}, UntrustedSource: "web:" + f.page.Host}, nil
}

func (m *Module) search(ctx context.Context, env host.Env, raw json.RawMessage) (host.Result, error) {
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
	detail := query + "|" + strconv.Itoa(limit)
	value, cached, err := m.load(env, "search", detail, m.ttl(m.cfg.searchTTL, 20*time.Minute), func() (any, int, error) {
		hits, engine, serr := m.registry.Search(ctx, query, limit)
		if serr != nil {
			return nil, 0, serr
		}
		size := 0
		for _, hit := range hits {
			size += len(hit.Title) + len(hit.URL) + len(hit.Snippet)
		}
		return searched{hits: hits, engine: engine}, size, nil
	})
	if err != nil {
		return host.Result{}, searchError(err)
	}
	s, ok := value.(searched)
	if !ok {
		return host.Result{}, &host.Error{Code: "internal", Message: "web cache type mismatch"}
	}
	var b strings.Builder
	for i, hit := range s.hits {
		fmt.Fprintf(&b, "%d. %s\n   %s\n   %s\n", i+1, hit.Title, hit.URL, hit.Snippet)
	}
	meta := map[string]any{"count": len(s.hits), "backend": s.engine, "cached": cached}
	if s.engine == "ddgs" {
		meta["tos_risk"] = "unofficial"
	}
	return host.Result{
		Content: strings.TrimRight(b.String(), "\n"), Meta: meta,
		UntrustedSource: "web_search:" + s.engine,
	}, nil
}

// searchError maps the engine error chain onto the closed tool-code set.
func searchError(err error) error {
	code, msg, hint := "network", err.Error(), ""
	switch {
	case errors.Is(err, errNoEngine), errors.Is(err, errEngineUnavailable):
		code = "module_unavailable"
	case errors.Is(err, errSearchAuth):
		code = "auth_required"
	case errors.Is(err, errRateLimited):
		msg, hint = "rate_limited: "+msg, "retry later"
	case errors.Is(err, errTimeout):
		code = "timeout"
	case errors.Is(err, errTooLarge):
		code = "too_large"
	}
	return &host.Error{Code: code, Message: msg, Hint: hint}
}

// load caches by (session, kind, detail); without a valid session view it
// never caches, so tenants cannot share a hit (GAP-COST-12).
func (m *Module) load(env host.Env, kind, detail string, ttl time.Duration, fn func() (any, int, error)) (any, bool, error) {
	if !env.Session.Valid() {
		value, _, err := fn()
		return value, false, err
	}
	// GAP-COST-12 keys the cache per profile, not per agent session: delegate
	// children share their parent's cache entry (siblings hit the same vendor
	// once), while two independent root sessions never see each other's hits.
	scope := env.Session.SessionID()
	if parent := env.Session.ParentID(); parent != "" {
		scope = parent
	}
	digest := sha256.Sum256([]byte(scope + "|" + kind + "|" + detail))
	return m.cache.load(string(digest[:]), ttl, fn)
}

func (m *Module) ttl(value, fallback time.Duration) time.Duration {
	if value <= 0 {
		return fallback
	}
	return value
}

// blocked matches a host (and subdomains) or host/path prefix from config.
func (m *Module) blocked(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	host, path := strings.ToLower(u.Hostname()), u.EscapedPath()
	for _, entry := range m.cfg.blocklist {
		entry = strings.ToLower(strings.TrimSpace(entry))
		blockHost, blockPath, hasPath := strings.Cut(entry, "/")
		if host != blockHost && !strings.HasSuffix(host, "."+blockHost) {
			continue
		}
		if !hasPath || strings.HasPrefix(path, "/"+blockPath) {
			return true
		}
	}
	return false
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
