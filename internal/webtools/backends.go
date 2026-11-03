//go:build !minimal

package webtools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

const (
	maxSearchBody = 1 << 20
	searchTimeout = 15 * time.Second
	maxSearchHits = 10
	ddgsUserAgent = "Mozilla/5.0 (compatible; damnbot/1.0; +https://github.com/yuldan/damn-bot)"
)

var (
	errEngineUnavailable = errors.New("webtools: search engine is not configured")
	errSearchAuth        = errors.New("webtools: search credential rejected")
	errRateLimited       = errors.New("webtools: search rate limited")
)

type httpDoer = interface {
	DoOK(*http.Request) (*http.Response, error)
}
type backendConfig struct {
	client  httpDoer
	key     func(engine string) (string, error)
	base    map[string]string
	timeout time.Duration
}
type httpBackend struct {
	id      string
	client  httpDoer
	base    string
	timeout time.Duration
}
type keyedBackend struct {
	httpBackend
	key func(string) (string, error)
}
type searchItem struct {
	Title       string   `json:"title"`
	URL         string   `json:"url"`
	Description string   `json:"description"`
	Content     string   `json:"content"`
	Snippet     string   `json:"snippet"`
	Text        string   `json:"text"`
	Excerpts    []string `json:"excerpts"`
}

func (i *searchItem) hit() searchResult {
	snippet := firstNonEmpty(i.Description, i.Content, i.Snippet, i.Text)
	if snippet == "" {
		snippet = strings.Join(i.Excerpts, " ")
	}
	return searchResult{Title: i.Title, URL: i.URL, Snippet: snippet}
}

func knownEngines() []string {
	return []string{"brave", "ddgs", "exa", "firecrawl", "tavily", "searxng", "perplexity", "parallel", "xai", "keenable", "openai-native"}
}
func knownEngine(id string) bool { return slices.Contains(knownEngines(), id) }
func defaultBase(id string) string {
	return map[string]string{
		"brave": "https://api.search.brave.com", "ddgs": "https://html.duckduckgo.com",
		"exa": "https://api.exa.ai", "firecrawl": "https://api.firecrawl.dev",
		"tavily": "https://api.tavily.com", "perplexity": "https://api.perplexity.ai",
		"parallel": "https://api.parallel.ai", "xai": "https://api.x.ai",
		"keenable": "https://api.keenable.ai", "openai-native": "https://api.openai.com",
	}[id]
}

func (b keyedBackend) credential() (string, error) {
	if b.key == nil {
		return "", errEngineUnavailable
	}
	key, err := b.key(b.id)
	if err != nil {
		return "", fmt.Errorf("%w: %s credential: %w", errEngineUnavailable, b.id, err)
	}
	if key == "" {
		return "", errEngineUnavailable
	}
	return key, nil
}

func (b httpBackend) do(ctx context.Context, method, path string, payload []byte, contentType string, hdr map[string]string) ([]byte, int, error) {
	if b.client == nil {
		return nil, 0, fmt.Errorf("%w: nil search client", errBadConfig)
	}
	if b.base == "" {
		return nil, 0, fmt.Errorf("%w: %s base URL", errEngineUnavailable, b.id)
	}
	timeout := b.timeout
	if timeout <= 0 {
		timeout = searchTimeout
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var body io.Reader
	if payload != nil {
		body = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, method, b.base+path, body)
	if err != nil {
		return nil, 0, fmt.Errorf("webtools: %s request: %w", b.id, err)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	req.Header.Set("Accept", "application/json")
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := b.client.DoOK(req)
	if err != nil {
		return nil, 0, searchHTTPError(ctx, b.id, err)
	}
	raw, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSearchBody+1))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, 0, fmt.Errorf("webtools: %s body: %w", b.id, err)
	}
	if len(raw) > maxSearchBody {
		return nil, resp.StatusCode, fmt.Errorf("%w: %s response exceeds %d bytes", errTooLarge, b.id, maxSearchBody)
	}
	return raw, resp.StatusCode, nil
}

func (b keyedBackend) get(ctx context.Context, path string, hdr map[string]string) ([]byte, error) {
	raw, _, err := b.do(ctx, http.MethodGet, path, nil, "", hdr)
	return raw, err
}

func (b keyedBackend) post(ctx context.Context, path string, payload any, hdr map[string]string) ([]byte, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("webtools: %s encode: %w", b.id, err)
	}
	body, _, err := b.do(ctx, http.MethodPost, path, raw, "application/json", hdr)
	return body, err
}

func decodeJSON(id string, raw []byte, dst any) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		return fmt.Errorf("webtools: %s decode: %w", id, err)
	}
	return nil
}

func searchHTTPError(ctx context.Context, id string, err error) error {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || errors.Is(err, context.DeadlineExceeded) {
		return fmt.Errorf("%w: %s: %w", errTimeout, id, err)
	}
	if status, ok := errors.AsType[*netx.StatusError](err); ok {
		if status.StatusCode == http.StatusUnauthorized || status.StatusCode == http.StatusForbidden {
			return fmt.Errorf("%w: %s: %w", errSearchAuth, id, err)
		}
		if status.StatusCode == http.StatusTooManyRequests {
			return fmt.Errorf("%w: %s: %w", errRateLimited, id, err)
		}
	}
	return fmt.Errorf("webtools: %s: %w", id, err)
}

func collectHits(items []searchItem, limit int) []searchResult {
	out := make([]searchResult, 0, min(len(items), limit))
	for i := range items {
		if i >= limit {
			break
		}
		out = append(out, items[i].hit())
	}
	return out
}

type restBackend struct {
	keyedBackend
	path   string
	isGet  bool
	header string
	prefix string
	body   func(query string, limit int, key string) any
	nested string
}

func (b *restBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	key, err := b.credential()
	if err != nil {
		return nil, err
	}
	hdr := map[string]string{}
	if b.header != "" {
		hdr[b.header] = b.prefix + key
	}
	var raw []byte
	if b.isGet {
		path := b.path + "?q=" + url.QueryEscape(query)
		switch b.id {
		case "brave":
			path += "&count=" + strconv.Itoa(limit)
		case "searxng":
			path += "&format=json&categories=general"
		}
		raw, err = b.get(ctx, path, hdr)
	} else {
		raw, err = b.post(ctx, b.path, b.body(query, limit, key), hdr)
	}
	if err != nil {
		return nil, err
	}
	return b.decode(raw, limit)
}

func (b *restBackend) decode(raw []byte, limit int) ([]searchResult, error) {
	switch b.nested {
	case "brave":
		var out struct {
			Web struct {
				Results []searchItem `json:"results"`
			} `json:"web"`
		}
		if err := decodeJSON(b.id, raw, &out); err != nil {
			return nil, err
		}
		return collectHits(out.Web.Results, limit), nil
	case "firecrawl":
		var out struct {
			Success bool   `json:"success"`
			Error   string `json:"error"`
			Data    struct {
				Web []searchItem `json:"web"`
			} `json:"data"`
		}
		if err := decodeJSON(b.id, raw, &out); err != nil {
			return nil, err
		}
		if !out.Success {
			return nil, fmt.Errorf("webtools: firecrawl: search failed: %s", out.Error)
		}
		return collectHits(out.Data.Web, limit), nil
	default:
		var out struct {
			Results []searchItem `json:"results"`
		}
		if err := decodeJSON(b.id, raw, &out); err != nil {
			return nil, err
		}
		return collectHits(out.Results, limit), nil
	}
}

func restBackendFor(id string, shared keyedBackend) backend {
	switch id {
	case "brave":
		return &restBackend{keyedBackend: shared, path: "/res/v1/web/search", isGet: true, header: "X-Subscription-Token", nested: "brave"}
	case "tavily":
		return &restBackend{keyedBackend: shared, path: "/search", body: func(q string, n int, key string) any {
			return map[string]any{"api_key": key, "query": q, "max_results": n}
		}}
	case "exa":
		return &restBackend{keyedBackend: shared, path: "/search", header: "x-api-key", body: func(q string, n int, _ string) any {
			return map[string]any{"query": q, "numResults": n}
		}}
	case "firecrawl":
		return &restBackend{keyedBackend: shared, path: "/v2/search", header: "Authorization", prefix: "Bearer ", nested: "firecrawl", body: func(q string, n int, _ string) any {
			return map[string]any{"query": q, "limit": n}
		}}
	case "perplexity":
		return &restBackend{keyedBackend: shared, path: "/search", header: "Authorization", prefix: "Bearer ", body: func(q string, n int, _ string) any {
			return map[string]any{"query": q, "max_results": n, "max_tokens_per_page": 512}
		}}
	case "parallel":
		return &restBackend{keyedBackend: shared, path: "/v1/search", header: "x-api-key", body: func(q string, n int, _ string) any {
			return map[string]any{"objective": q, "search_queries": []string{q}, "advanced_settings": map[string]any{"max_results": n}}
		}}
	case "keenable":
		return &restBackend{keyedBackend: shared, path: "/v1/search", header: "X-API-Key", body: func(q string, _ int, _ string) any {
			return map[string]any{"query": q, "snippet_max_length": 300}
		}}
	case "searxng":
		return &restBackend{keyedBackend: shared, path: "/search", isGet: true}
	case "ddgs":
		return newDDGSBackend(shared.httpBackend)
	case "xai":
		return &responsesBackend{keyedBackend: shared, path: "/v1/responses", model: "grok-4"}
	case "openai-native":
		return &responsesBackend{keyedBackend: shared, path: "/v1/responses", model: "gpt-4.1-mini"}
	}
	return nil
}

type responsesBackend struct {
	keyedBackend
	path  string
	model string
}

func (b *responsesBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	key, err := b.credential()
	if err != nil {
		return nil, err
	}
	raw, err := b.post(ctx, b.path, map[string]any{
		"model": b.model, "input": query,
		"tools":   []map[string]string{{"type": "web_search"}},
		"include": []string{"web_search_call.action.sources"},
	}, map[string]string{"Authorization": "Bearer " + key})
	if err != nil {
		return nil, err
	}
	var out struct {
		Citations []string `json:"citations"`
		Output    []struct {
			Action struct {
				Sources []searchItem `json:"sources"`
			} `json:"action"`
			Content []struct {
				Text        string `json:"text"`
				Annotations []struct {
					Type       string `json:"type"`
					URL        string `json:"url"`
					Title      string `json:"title"`
					StartIndex int    `json:"start_index"`
					EndIndex   int    `json:"end_index"`
				} `json:"annotations"`
			} `json:"content"`
		} `json:"output"`
	}
	if err := decodeJSON(b.id, raw, &out); err != nil {
		return nil, err
	}
	hits := make([]searchResult, 0, limit)
	for _, item := range out.Output {
		for _, part := range item.Content {
			for _, ann := range part.Annotations {
				if ann.Type == "url_citation" {
					hits = append(hits, searchResult{Title: ann.Title, URL: ann.URL, Snippet: annotationSpan(part.Text, ann.StartIndex, ann.EndIndex)})
				}
			}
		}
	}
	for _, item := range out.Output {
		for i := range item.Action.Sources {
			hits = append(hits, item.Action.Sources[i].hit())
		}
	}
	for _, citation := range out.Citations {
		hits = append(hits, searchResult{URL: citation})
	}
	return hits[:min(len(hits), limit)], nil
}

func annotationSpan(text string, start, end int) string {
	if start < 0 || end <= start {
		return ""
	}
	runes := []rune(text)
	if start >= len(runes) {
		return ""
	}
	return string(runes[start:min(end, len(runes))])
}

type ddgsBackend struct {
	httpBackend
	anchor, snippet, attr *regexp.Regexp
}

func newDDGSBackend(h httpBackend) *ddgsBackend {
	return &ddgsBackend{
		httpBackend: h,
		anchor:      regexp.MustCompile(`(?is)<a\b([^>]*)>(.*?)</a\s*>`),
		snippet: regexp.MustCompile(`(?is)<a\b[^>]*class\s*=\s*(?:"[^"]*result__snippet[^"]*"|'[^']*result__snippet[^']*')[^>]*>(.*?)</a\s*>` +
			`|<div\b[^>]*class\s*=\s*(?:"[^"]*result__snippet[^"]*"|'[^']*result__snippet[^']*')[^>]*>(.*?)</div\s*>`),
		attr: regexp.MustCompile(`(?is)([a-zA-Z_:][-a-zA-Z0-9_:.]*)\s*=\s*(?:"([^"]*)"|'([^']*)')`),
	}
}

func (b *ddgsBackend) Search(ctx context.Context, query string, limit int) ([]searchResult, error) {
	form := url.Values{"q": {query}}.Encode()
	raw, status, err := b.do(ctx, http.MethodPost, "/html/", []byte(form), "application/x-www-form-urlencoded",
		map[string]string{"Accept": "text/html", "User-Agent": ddgsUserAgent})
	if err != nil {
		return nil, err
	}
	if status == http.StatusAccepted {
		return nil, fmt.Errorf("%w: ddgs http 202", errRateLimited)
	}
	return b.parse(string(raw), limit), nil
}

func (b *ddgsBackend) parse(body string, limit int) []searchResult {
	anchors := b.anchor.FindAllStringSubmatchIndex(body, -1)
	out := make([]searchResult, 0, min(len(anchors), limit))
	for i, m := range anchors {
		if len(out) >= limit {
			break
		}
		attrs, inner := body[m[2]:m[3]], body[m[4]:m[5]]
		if !slices.Contains(strings.Fields(b.attrValue(attrs, "class")), "result__a") {
			continue
		}
		target := ddgTarget(b.attrValue(attrs, "href"))
		if target == "" {
			continue
		}
		end := len(body)
		for j := i + 1; j < len(anchors); j++ {
			if slices.Contains(strings.Fields(b.attrValue(body[anchors[j][2]:anchors[j][3]], "class")), "result__a") {
				end = anchors[j][0]
				break
			}
		}
		snippet := ""
		if sm := b.snippet.FindStringSubmatch(body[m[1]:end]); len(sm) > 0 {
			snippet = firstNonEmpty(sm[1:]...)
		}
		out = append(out, searchResult{Title: inner, URL: target, Snippet: snippet})
	}
	return out
}

func (b *ddgsBackend) attrValue(attrs, name string) string {
	for _, m := range b.attr.FindAllStringSubmatch(attrs, -1) {
		if strings.EqualFold(m[1], name) {
			return firstNonEmpty(m[2], m[3])
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	if i := slices.IndexFunc(values, func(v string) bool { return v != "" }); i >= 0 {
		return values[i]
	}
	return ""
}

func ddgTarget(href string) string {
	href = html.UnescapeString(strings.TrimSpace(href))
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if target := u.Query().Get("uddg"); target != "" &&
		(u.Hostname() == "" || strings.HasSuffix(strings.ToLower(u.Hostname()), "duckduckgo.com")) {
		return target
	}
	return href
}

func clip(s string) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= 300 {
		return string(r)
	}
	return string(r[:300])
}
