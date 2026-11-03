//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// plainDoer bypasses netx's Tool DLP gate: these tests pin vendor request
// shapes, while TestDLPAuthHeaderExemption pins the exemption in PR2.
type plainDoer struct{ client *http.Client }

func (d plainDoer) DoOK(req *http.Request) (*http.Response, error) {
	resp, err := d.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("plain doer: %w", err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp, nil
	}
	body, readErr := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	closeErr := resp.Body.Close()
	if err := errors.Join(readErr, closeErr); err != nil {
		return nil, fmt.Errorf("plain doer body: %w", err)
	}
	return nil, &netx.StatusError{StatusCode: resp.StatusCode, Header: resp.Header, Body: body}
}

func writeTestBody(t *testing.T, w io.Writer, body string) {
	t.Helper()
	if _, err := io.WriteString(w, body); err != nil {
		t.Error(err)
	}
}

func backendConfigFor(srv *httptest.Server, ids ...string) backendConfig {
	base := make(map[string]string, len(ids))
	for _, id := range ids {
		base[id] = srv.URL
	}
	return backendConfig{
		client: plainDoer{client: srv.Client()},
		key:    func(string) (string, error) { return "secret", nil },
		base:   base, timeout: 2 * time.Second,
	}
}

// searchWithServer drives one engine through the registry and applies the
// cross-backend result contract.
func searchWithServer(t *testing.T, id string, srv *httptest.Server, wantURL, wantSnippet string) {
	t.Helper()
	reg, err := newRegistry([]string{id}, backendConfigFor(srv, id))
	if err != nil {
		t.Fatalf("newRegistry: %v", err)
	}
	hits, engine, err := reg.Search(context.Background(), "golang", 1)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	if engine != id || len(hits) != 1 {
		t.Fatalf("engine=%q hits=%+v", engine, hits)
	}
	got := hits[0]
	if got.URL != wantURL || !strings.Contains(got.Snippet, wantSnippet) || got.Title == "" {
		t.Fatalf("hit = %+v, want url=%q snippet~%q", got, wantURL, wantSnippet)
	}
	for _, bad := range []string{"<b>", "<script"} {
		if strings.Contains(got.Title+got.Snippet, bad) {
			t.Errorf("markup leaked into %+v", got)
		}
	}
}

func braveHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/res/v1/web/search" ||
		r.URL.Query().Get("q") != "golang" || r.URL.Query().Get("count") != "1" ||
		r.Header.Get("X-Subscription-Token") != "secret" {
		t.Errorf("request = %s %s?%s headers=%v", r.Method, r.URL.Path, r.URL.RawQuery, r.Header)
	}
	return `{"web":{"results":[{"title":"T <b>x</b>","url":"https://e.test/1","description":"<b>S</b> one"}]}}`
}

func tavilyHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	if r.Method != http.MethodPost || r.URL.Path != "/search" ||
		body["query"] != "golang" || body["api_key"] != "secret" {
		t.Errorf("request = %s %s body=%v", r.Method, r.URL.Path, body)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","content":"<b>S</b> one"}]}`
}

func exaHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/search" || r.Header.Get("x-api-key") != "secret" {
		t.Errorf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","text":"<b>S</b> one"}]}`
}

func firecrawlHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/v2/search" ||
		r.Header.Get("Authorization") != "Bearer secret" {
		t.Errorf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	return `{"success":true,"data":{"web":[{"title":"T","url":"https://e.test/1","description":"<b>S</b> one"}]}}`
}

func perplexityHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/search" ||
		r.Header.Get("Authorization") != "Bearer secret" {
		t.Errorf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","snippet":"<b>S</b> one"}]}`
}

func parallelHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Error(err)
	}
	if r.Method != http.MethodPost || r.URL.Path != "/v1/search" ||
		r.Header.Get("x-api-key") != "secret" || body["objective"] != "golang" {
		t.Errorf("request = %s %s body=%v headers=%v", r.Method, r.URL.Path, body, r.Header)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","excerpts":["<b>S</b> one","two"]}]}`
}

func keenableHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/v1/search" ||
		r.Header.Get("X-API-Key") != "secret" {
		t.Errorf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","description":"<b>S</b> one"}]}`
}

func searxngHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodGet || r.URL.Path != "/search" ||
		r.URL.Query().Get("q") != "golang" || r.URL.Query().Get("format") != "json" {
		t.Errorf("request = %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
	}
	return `{"results":[{"title":"T","url":"https://e.test/1","content":"<b>S</b> one"}]}`
}

func ddgsHandler(t *testing.T, _ http.ResponseWriter, r *http.Request) string {
	t.Helper()
	if r.Method != http.MethodPost || r.URL.Path != "/html/" ||
		!strings.Contains(r.Header.Get("User-Agent"), "damnbot/") {
		t.Errorf("request = %s %s headers=%v", r.Method, r.URL.Path, r.Header)
	}
	if err := r.ParseForm(); err != nil {
		t.Error(err)
	} else if r.PostForm.Get("q") != "golang" {
		t.Errorf("form = %v", r.PostForm)
	}
	return `<div class="result"><a class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fe.test%2Fone">T &amp; <b>x</b></a>` +
		`<a class="result__snippet"> <b>S</b> &lt;ok&gt; </a></div>`
}

func TestSearchBackends(t *testing.T) {
	t.Parallel()
	cases := []struct {
		id, wantURL, wantSnippet, contentType string
		handler                               func(*testing.T, http.ResponseWriter, *http.Request) string
	}{
		{"brave", "https://e.test/1", "S", "application/json", braveHandler},
		{"tavily", "https://e.test/1", "S", "application/json", tavilyHandler},
		{"exa", "https://e.test/1", "S", "application/json", exaHandler},
		{"firecrawl", "https://e.test/1", "S", "application/json", firecrawlHandler},
		{"perplexity", "https://e.test/1", "S", "application/json", perplexityHandler},
		{"parallel", "https://e.test/1", "S one two", "application/json", parallelHandler},
		{"keenable", "https://e.test/1", "S", "application/json", keenableHandler},
		{"searxng", "https://e.test/1", "S", "application/json", searxngHandler},
		{"ddgs", "https://e.test/one", "S <ok>", "text/html", ddgsHandler},
	}
	for _, tc := range cases {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", tc.contentType)
				writeTestBody(t, w, tc.handler(t, w, r))
			}))
			defer srv.Close()
			searchWithServer(t, tc.id, srv, tc.wantURL, tc.wantSnippet)
		})
	}
}

func responsesServer(t *testing.T, model string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/responses" ||
			r.Header.Get("Authorization") != "Bearer secret" || body["model"] != model {
			t.Errorf("request = %s %s body=%v headers=%v", r.Method, r.URL.Path, body, r.Header)
		}
		writeTestBody(t, w, `{"citations":["https://source.test/a"],"output":[`+
			`{"type":"web_search_call","action":{"sources":[{"url":"https://source.test/a"}]}},`+
			`{"type":"message","content":[{"type":"output_text","text":"answer S one","annotations":[`+
			`{"type":"url_citation","url":"https://e.test/1","title":"T <b>x</b>","start_index":7,"end_index":8}]}]}]}`)
	}))
}

func TestResponsesBackends(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ id, model string }{
		{"xai", "grok-4"},
		{"openai-native", "gpt-4.1-mini"},
	} {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			srv := responsesServer(t, tc.model)
			defer srv.Close()
			reg, err := newRegistry([]string{tc.id}, backendConfigFor(srv, tc.id))
			if err != nil {
				t.Fatalf("newRegistry: %v", err)
			}
			hits, engine, err := reg.Search(context.Background(), "golang", 10)
			if err != nil || engine != tc.id {
				t.Fatalf("Search = %+v %q %v", hits, engine, err)
			}
			if len(hits) != 2 || hits[0].URL != "https://e.test/1" || hits[0].Snippet != "S" ||
				hits[1].URL != "https://source.test/a" {
				t.Fatalf("hits = %+v", hits)
			}
		})
	}
}

func TestBackendMissingCredential(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls.Add(1) }))
	defer srv.Close()
	for _, id := range []string{"brave", "tavily", "exa", "firecrawl", "perplexity", "parallel", "keenable", "xai", "openai-native"} {
		t.Run(id, func(t *testing.T) {
			cfg := backendConfigFor(srv, id)
			cfg.key = func(string) (string, error) { return "", nil }
			reg, err := newRegistry([]string{id}, cfg)
			if err != nil {
				t.Fatal(err)
			}
			if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errEngineUnavailable) {
				t.Fatalf("err = %v, want errEngineUnavailable", serr)
			}
		})
	}
	if calls.Load() != 0 {
		t.Errorf("requests without a credential = %d", calls.Load())
	}
}

func TestBackendHTTPErrorClasses(t *testing.T) {
	t.Parallel()
	for _, id := range knownEngines() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			for _, tc := range []struct {
				name   string
				status int
				want   error
			}{
				{"unauthorized", http.StatusUnauthorized, errSearchAuth},
				{"rate_limited", http.StatusTooManyRequests, errRateLimited},
			} {
				t.Run(tc.name, func(t *testing.T) {
					t.Parallel()
					srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
						http.Error(w, "no", tc.status)
					}))
					defer srv.Close()
					reg, err := newRegistry([]string{id}, backendConfigFor(srv, id))
					if err != nil {
						t.Fatal(err)
					}
					if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, tc.want) {
						t.Fatalf("err = %v, want %v", serr, tc.want)
					}
				})
			}
		})
	}
}

func TestBackendOversizedBody(t *testing.T) {
	t.Parallel()
	for _, id := range knownEngines() {
		t.Run(id, func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				writeTestBody(t, w, strings.Repeat("x", maxSearchBody+1))
			}))
			defer srv.Close()
			reg, err := newRegistry([]string{id}, backendConfigFor(srv, id))
			if err != nil {
				t.Fatal(err)
			}
			if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errTooLarge) {
				t.Fatalf("err = %v, want errTooLarge", serr)
			}
		})
	}
}

func TestBackendTimeout(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer srv.Close()
	cfg := backendConfigFor(srv, "perplexity")
	cfg.timeout = 20 * time.Millisecond
	reg, err := newRegistry([]string{"perplexity"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errTimeout) {
		t.Fatalf("err = %v, want errTimeout", serr)
	}
}

func TestBackendCredentialAndFallback(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := backendConfigFor(srv, "brave", "fake")
	reg, err := newRegistry([]string{"brave", "fake"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	hits, engine, serr := reg.Search(context.Background(), "x", 1)
	if serr != nil || engine != "fake" || len(hits) != 1 {
		t.Fatalf("runtime fallback = %+v %q %v", hits, engine, serr)
	}
	cfg.key = func(string) (string, error) { return "", nil }
	reg, err = newRegistry([]string{"brave", "fake"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, engine, serr := reg.Search(context.Background(), "x", 1); !errors.Is(serr, errEngineUnavailable) || engine != "" {
		t.Fatalf("missing-key fallback = %q %v; a configured engine must not be replaced silently", engine, serr)
	}
}

type staticBackend []searchResult

func (s staticBackend) Search(ctx context.Context, _ string, _ int) ([]searchResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("webtools: static search: %w", err)
	}
	return s, nil
}

func TestSearchNormalization(t *testing.T) {
	t.Parallel()
	hits := make([]searchResult, 0, 14)
	hits = append(hits, searchResult{URL: "javascript:alert(1)"},
		searchResult{URL: "ftp://example.test/x"},
		searchResult{URL: "/relative"},
		searchResult{URL: "https://e.test/dup", Title: "<b>First</b>", Snippet: "<script>x</script> S &amp; ok"},
		searchResult{URL: "https://e.test/dup", Title: "duplicate"})
	for i := range 12 {
		hits = append(hits, searchResult{
			URL: "https://e.test/" + string(rune('a'+i)), Title: "<i>T</i>", Snippet: "<b>S</b>",
		})
	}
	reg := &registry{
		clean:   newTextCleaner(),
		engines: []engine{{id: "stub", backend: staticBackend(hits)}},
	}
	got, engine, err := reg.Search(context.Background(), "x", 20)
	if err != nil || engine != "stub" {
		t.Fatalf("Search = %v %q", err, engine)
	}
	if len(got) != maxSearchHits {
		t.Fatalf("hits = %d, want %d", len(got), maxSearchHits)
	}
	if got[0].URL != "https://e.test/dup" || got[0].Title != "First" || got[0].Snippet != "S & ok" {
		t.Fatalf("first = %+v", got[0])
	}
	for _, hit := range got {
		if !strings.HasPrefix(hit.URL, "https://e.test/") {
			t.Errorf("non-http URL kept: %q", hit.URL)
		}
		if strings.ContainsAny(hit.Title+hit.Snippet, "<>") {
			t.Errorf("markup kept: %+v", hit)
		}
	}
}
