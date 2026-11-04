//go:build !minimal

package webtools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// f6HTML serves one deterministic page for extract tests.
func f6HTML(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		writeTestBody(t, w, `<title>Docs</title><h1>Halo</h1><script>evil()</script><p>dunia</p>`)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// f6BraveBody renders n Brave-shaped hits; the first snippet is HTML-heavy and
// longer than the 300-rune cap, and the last URL is not http(s).
func f6BraveBody(n int) string {
	var b strings.Builder
	b.WriteString(`{"web":{"results":[`)
	for i := 1; i <= n; i++ {
		if i > 1 {
			b.WriteByte(',')
		}
		link := fmt.Sprintf("https://example.test/%d", i)
		if i == 1 {
			link = "javascript:alert(1)"
		}
		fmt.Fprintf(&b, `{"title":"Result <b>%d</b>","url":%q,"description":"%s<b>bold</b>"}`,
			i, link, strings.Repeat("x", 350))
	}
	b.WriteString(`]}}`)
	return b.String()
}

// TestF6TOOL086WebExtract: the parity name web_extract maps to the SPEC
// web_fetch implementation, returns markdown with backend/cached metadata and
// an untrusted source, and keeps every SSRF refusal closed.
func TestF6TOOL086WebExtract(t *testing.T) {
	t.Parallel()
	srv := f6HTML(t)
	m := startModule(t, webConfig())
	env := session(t, "tool-086")
	raw := json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL))

	res, err := m.Invoke(context.Background(), "web_extract", raw, env)
	if err != nil {
		t.Fatalf("Invoke web_extract: %v", err)
	}
	if !strings.Contains(res.Content, "Halo") || !strings.Contains(res.Content, "dunia") || strings.Contains(res.Content, "evil()") {
		t.Fatalf("markdown = %q", res.Content)
	}
	if res.UntrustedSource != "web:127.0.0.1" || res.Meta["backend"] != "native" || res.Meta["cached"] != false {
		t.Fatalf("taint/meta = %q %v", res.UntrustedSource, res.Meta)
	}
	if again, err := m.Invoke(context.Background(), "web_extract", raw, env); err != nil || again.Meta["cached"] != true {
		t.Fatalf("cached extract = %v %v", again.Meta, err)
	}

	// The catalog keeps the SPEC name; the alias is dispatch-only.
	for _, tool := range Tools() {
		if tool.Name == "web_extract" {
			t.Error("web_extract advertised instead of the SPEC web_fetch name")
		}
	}

	// Negative control: production policy refuses loopback, private, link-local
	// and metadata endpoints before any dial.
	prod := webConfig()
	prod.policy = publicHost
	guarded := startModule(t, prod)
	for _, target := range []string{
		"http://127.0.0.1/", "http://localhost/", "http://10.1.2.3/",
		"http://192.168.1.1/", "http://169.254.169.254/latest/meta-data/", "http://[::1]/",
	} {
		_, err := guarded.Invoke(context.Background(), "web_extract",
			json.RawMessage(fmt.Sprintf(`{"url":%q}`, target)), host.Env{})
		wantCode(t, err, "blocked_host")
	}
}

// TestF6TOOL087WebSearch: the search result is bounded to ten hits, snippets
// are clipped to 300 runes, non-http results are dropped, and identical
// concurrent queries make one vendor call.
func TestF6TOOL087WebSearch(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeTestBody(t, w, f6BraveBody(15))
	}))
	defer srv.Close()
	cfg := webConfig()
	cfg.Engines = []string{"brave"}
	cfg.Key = func(string) (string, error) { return "secret", nil }
	cfg.Base = map[string]string{"brave": srv.URL}
	m := startModule(t, cfg)
	env := session(t, "tool-087")

	res, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"f6","limit":20}`), env)
	if err != nil {
		t.Fatalf("Invoke: %v", err)
	}
	if n := strings.Count(res.Content, "https://example.test/"); n != 9 || n > 10 {
		t.Fatalf("hits = %d, want 9 accepted (one non-http result dropped, cap 10)\n%s", n, res.Content)
	}
	if strings.Contains(res.Content, "javascript:") || strings.Contains(res.Content, "<b>") {
		t.Fatalf("unsanitized result: %q", res.Content)
	}
	if !strings.Contains(res.Content, strings.Repeat("x", 300)) || strings.Contains(res.Content, strings.Repeat("x", 301)) {
		t.Error("snippet was not clipped to 300 runes")
	}
	if res.Meta["backend"] != "brave" || res.UntrustedSource != "web_search:brave" || res.Meta["cached"] != false {
		t.Fatalf("taint/meta = %q %v", res.UntrustedSource, res.Meta)
	}

	calls.Store(0)
	raw := json.RawMessage(`{"query":"single-flight","limit":5}`)
	start := make(chan struct{})
	var wg sync.WaitGroup
	for range 3 {
		wg.Go(func() {
			<-start
			if _, err := m.Invoke(context.Background(), "web_search", raw, env); err != nil {
				t.Errorf("concurrent search: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("3 concurrent searches made %d vendor calls, want 1", got)
	}
}

// runWebBackendMatrix pins the closed eleven-backend matrix and proves that
// backend selection comes from config, never from environment autodetection.
func runWebBackendMatrix(t *testing.T) {
	t.Setenv("BRAVE_API_KEY", "env-brave")
	t.Setenv("EXA_API_KEY", "env-exa")
	t.Setenv("TAVILY_API_KEY", "env-tavily")
	t.Setenv("OPENAI_API_KEY", "env-openai")
	want := []string{"brave", "ddgs", "exa", "firecrawl", "tavily", "searxng", "perplexity", "parallel", "xai", "keenable", "openai-native"}
	if got := knownEngines(); !slices.Equal(got, want) {
		t.Fatalf("engines = %v, want %v", got, want)
	}
	for _, id := range want {
		m, err := New(testClient(), &Config{Enabled: true, Engines: []string{id}, Key: func(string) (string, error) { return "k", nil }})
		if err != nil {
			t.Fatalf("engine %q: %v", id, err)
		}
		if len(m.registry.engines) != 1 || m.registry.engines[0].id != id {
			t.Fatalf("engine %q selected %+v", id, m.registry.engines)
		}
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `{"results":[{"title":"T","url":"https://e.test/1","description":"S"}]}`)
	}))
	defer srv.Close()
	cfg := webConfig()
	cfg.Engines = []string{"exa"}
	cfg.Key = func(string) (string, error) { return "config-key", nil }
	cfg.Base = map[string]string{"exa": srv.URL}
	m := startModule(t, cfg)
	for _, envKey := range []string{"BRAVE_API_KEY", "EXA_API_KEY", "TAVILY_API_KEY"} {
		t.Setenv(envKey, "rotated-"+envKey)
		res, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"env"}`), host.Env{})
		if err != nil || res.Meta["backend"] != "exa" {
			t.Fatalf("%s: backend = %v err = %v", envKey, res.Meta["backend"], err)
		}
	}

	// Keyed engines require an explicit credential; env keys never substitute.
	keyless := webConfig()
	keyless.Engines = []string{"exa"}
	keyless.Base = map[string]string{"exa": srv.URL}
	unkeyed := startModule(t, keyless)
	_, err := unkeyed.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")

	// Keyless ddgs works without a key only when explicitly configured.
	dgs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `<a class="result__a" href="https://e.test/1">T</a><a class="result__snippet">S</a>`)
	}))
	defer dgs.Close()
	keyless.Engines, keyless.Base = []string{"ddgs"}, map[string]string{"ddgs": dgs.URL}
	res, err := startModule(t, keyless).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	if err != nil || res.Meta["backend"] != "ddgs" {
		t.Fatalf("keyless ddgs = %v %v", res.Meta, err)
	}

	for _, id := range []string{"bing", "openai_native", "Brave"} {
		if _, err := New(testClient(), &Config{Engines: []string{id}}); !errors.Is(err, errUnknownEngine) {
			t.Errorf("engine %q registered: %v", id, err)
		}
	}
}

func TestF6EXEC042WebBackendsMatrix(t *testing.T) { runWebBackendMatrix(t) }

func TestF6EXEC042WebBackendMatrix(t *testing.T) { runWebBackendMatrix(t) }

// runSiteBlocklist proves the configured blocklist is consulted before dial,
// wins over an allowlisting policy, and that a redirect cannot reach a
// blocked domain.
func runSiteBlocklist(t *testing.T) {
	t.Parallel()
	var dials atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		dials.Add(1)
		writeTestBody(t, w, "<h1>ok</h1>")
	}))
	defer srv.Close()

	cfg := webConfig() // policy allows loopback: the blocklist must still win
	cfg.blocklist = []string{"127.0.0.1", "blocked.invalid"}
	m := startModule(t, cfg)
	env := session(t, "f6-blocked") // a valid session would cache if the gate came later
	for range 2 {                   // repeated blocked calls never reach the dialer or the cache
		_, err := m.Invoke(context.Background(), "web_fetch",
			json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL)), env)
		wantCode(t, err, "blocked_host")
	}
	_, err := m.Invoke(context.Background(), "web_fetch",
		json.RawMessage(`{"url":"http://x.blocked.invalid/"}`), env)
	wantCode(t, err, "blocked_host")
	if got := dials.Load(); got != 0 {
		t.Fatalf("blocked hosts dialed %d times", got)
	}

	pathCfg := webConfig()
	pathCfg.blocklist = []string{"127.0.0.1/private"}
	pathM := startModule(t, pathCfg)
	if _, perr := pathM.Invoke(context.Background(), "web_fetch",
		json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL+"/private/x")), host.Env{}); perr == nil {
		t.Fatal("blocked path was fetched")
	}
	if got := dials.Load(); got != 0 {
		t.Fatalf("blocked path dialed %d times", got)
	}
	if _, perr := pathM.Invoke(context.Background(), "web_fetch",
		json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL+"/public")), host.Env{}); perr != nil {
		t.Fatalf("allowed path: %v", perr)
	}

	// The list comes from the module section, not from a hard-coded host set.
	fromConfig, err := NewFromConfig(json.RawMessage(`{"enabled":false,"blocklist":["Blocked.TEST/private"]}`))
	if err != nil {
		t.Fatalf("NewFromConfig: %v", err)
	}
	if !slices.Equal(fromConfig.cfg.blocklist, []string{"Blocked.TEST/private"}) {
		t.Fatalf("config blocklist = %v", fromConfig.cfg.blocklist)
	}

	// A redirect to a blocked domain is refused as a cross-host redirect; the
	// target is never dialed.
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://blocked.invalid/", http.StatusFound)
	}))
	defer redir.Close()
	redirM := startModule(t, webConfig())
	_, err = redirM.Invoke(context.Background(), "web_fetch",
		json.RawMessage(fmt.Sprintf(`{"url":%q}`, redir.URL)), host.Env{})
	he, ok := errors.AsType[*host.Error](err)
	if !ok || !strings.Contains(he.Message, "redirect") {
		t.Fatalf("redirect err = %v, want a refused redirect", err)
	}
}

func TestF6EXEC044WebsiteBlocklist(t *testing.T) { runSiteBlocklist(t) }

func TestF6EXEC044SiteBlocklist(t *testing.T) { runSiteBlocklist(t) }

// runWebPlugin checks the plugin-facing contract: eleven closed backends, an
// explicit backend per call, and tos_risk only on the unofficial keyless
// engine. A label never turns into approval or trust.
func runWebPlugin(t *testing.T) {
	t.Parallel()
	want := []string{"brave", "ddgs", "exa", "firecrawl", "tavily", "searxng", "perplexity", "parallel", "xai", "keenable", "openai-native"}
	if got := knownEngines(); !slices.Equal(got, want) {
		t.Fatalf("plugin backends = %v, want %v", got, want)
	}
	for _, tool := range Tools() {
		if tool.Effect != "read" {
			t.Fatalf("%s effect = %q; a tos label can never grant approval", tool.Name, tool.Effect)
		}
	}

	// No engine is selected by default, so the unofficial one is opt-in.
	empty := webConfig()
	empty.Engines = nil
	_, err := startModule(t, empty).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")

	dgs := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `<a class="result__a" href="https://e.test/1">T</a><a class="result__snippet">S</a>`)
	}))
	defer dgs.Close()
	unofficial := webConfig()
	unofficial.Engines = []string{"ddgs"}
	unofficial.Base = map[string]string{"ddgs": dgs.URL}
	res, err := startModule(t, unofficial).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	if err != nil || res.Meta["tos_risk"] != "unofficial" || res.UntrustedSource == "" {
		t.Fatalf("ddgs result = %v %q %v", res.Meta, res.UntrustedSource, err)
	}

	official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `{"results":[{"title":"T","url":"https://e.test/1","description":"S"}]}`)
	}))
	defer official.Close()
	cfg := webConfig()
	cfg.Engines = []string{"exa"}
	cfg.Key = func(string) (string, error) { return "secret", nil }
	cfg.Base = map[string]string{"exa": official.URL}
	res, err = startModule(t, cfg).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	if err != nil || res.Meta["backend"] != "exa" || res.Meta["tos_risk"] != nil || res.UntrustedSource == "" {
		t.Fatalf("official result = %v %q %v", res.Meta, res.UntrustedSource, err)
	}
}

func TestF6PLUG028WebBackends(t *testing.T) { runWebPlugin(t) }

func TestF6PLUG028WebPlugin(t *testing.T) { runWebPlugin(t) }

// TestF6BackendSelection: every result names its backend, fallback follows the
// configured engine order only, and nothing is auto-detected from environment.
func TestF6BackendSelection(t *testing.T) {
	t.Setenv("EXA_API_KEY", "env-exa")
	t.Setenv("BRAVE_API_KEY", "env-brave")
	mux := http.NewServeMux()
	mux.HandleFunc("/res/v1/web/search", func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "down", http.StatusInternalServerError)
	})
	mux.HandleFunc("/search", func(w http.ResponseWriter, _ *http.Request) {
		writeTestBody(t, w, `{"results":[{"title":"T","url":"https://e.test/1","description":"S"}]}`)
	})
	mux.HandleFunc("/page", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		writeTestBody(t, w, "<h1>page</h1>")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	cfg := webConfig()
	cfg.Engines = []string{"brave", "tavily"}
	cfg.Key = func(string) (string, error) { return "config-key", nil }
	cfg.Base = map[string]string{"brave": srv.URL, "tavily": srv.URL}
	m := startModule(t, cfg)
	res, err := m.Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"fallback"}`), host.Env{})
	if err != nil || res.Meta["backend"] != "tavily" {
		t.Fatalf("fallback = %v %v, want the configured second engine", res.Meta, err)
	}
	fetched, err := m.Invoke(context.Background(), "web_fetch",
		json.RawMessage(fmt.Sprintf(`{"url":%q}`, srv.URL+"/page")), host.Env{})
	if err != nil || fetched.Meta["backend"] != "native" {
		t.Fatalf("fetch backend = %v %v", fetched.Meta, err)
	}

	// Environment credentials never select or unlock an engine.
	noKey := webConfig()
	noKey.Engines = []string{"exa"}
	noKey.Base = map[string]string{"exa": srv.URL}
	_, err = startModule(t, noKey).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")

	// No configured engine means no silent fallback, least of all to ddgs.
	none := webConfig()
	none.Engines = nil
	_, err = startModule(t, none).Invoke(context.Background(), "web_search", json.RawMessage(`{"query":"x"}`), host.Env{})
	wantCode(t, err, "module_unavailable")
}
