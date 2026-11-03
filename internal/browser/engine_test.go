//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func camofoxConfig(url string) ModuleConfig {
	return ModuleConfig{
		Enabled: true, Engine: "camofox", CamofoxURL: url,
		AllowOrigins: []string{"https://app.example"},
	}
}

type recorded struct {
	mu     sync.Mutex
	method string
	path   string
	body   string
}

func (r *recorded) add(req *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.method, r.path = req.Method, req.URL.Path
	data, err := io.ReadAll(req.Body)
	if err != nil {
		r.body = "read: " + err.Error()
		return
	}
	r.body = string(data)
}

func (r *recorded) last() (string, string, string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.method, r.path, r.body
}

// TestEngineSelection: the explicit engine wins; otherwise exactly one
// endpoint selects the backend deterministically; several endpoints without an
// explicit engine are a load error, not a silent pick.
func TestEngineSelection(t *testing.T) {
	t.Parallel()
	base := ModuleConfig{Enabled: true, AllowOrigins: []string{"https://app.example"}}
	cases := []struct {
		name string
		cfg  ModuleConfig
		want string
		err  bool
	}{
		{"none", base, "", false},
		{"implicit cdp", func() ModuleConfig { c := base; c.CDPURL = "ws://127.0.0.1:9222/devtools/page/x"; return c }(), "cdp", false},
		{"implicit camofox", func() ModuleConfig { c := base; c.CamofoxURL = "http://127.0.0.1:9377"; return c }(), "camofox", false},
		{"implicit lightpanda", func() ModuleConfig { c := base; c.LightpandaBin = "/usr/local/bin/lightpanda"; return c }(), "lightpanda", false},
		{"implicit cloud", func() ModuleConfig { c := base; c.CloudURL = "https://browser.example"; return c }(), "cloud", false},
		{"explicit wins", func() ModuleConfig {
			c := base
			c.Engine = "cloud"
			c.CloudURL = "https://browser.example"
			c.CamofoxURL = "http://127.0.0.1:9377"
			return c
		}(), "cloud", false},
		{"ambiguous", func() ModuleConfig {
			c := base
			c.CamofoxURL = "http://127.0.0.1:9377"
			c.CloudURL = "https://browser.example"
			return c
		}(), "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := tc.cfg
			m, err := New(&cfg, Deps{})
			if tc.err {
				if !errors.Is(err, errBadConfig) {
					t.Fatalf("New = %v, want errBadConfig", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			if got := m.engineName(); got != tc.want {
				t.Fatalf("engine = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestNewRejectsEngineConfig: every malformed endpoint is a load error, even
// when the module is disabled.
func TestNewRejectsEngineConfig(t *testing.T) {
	t.Parallel()
	base := ModuleConfig{AllowOrigins: []string{"https://app.example"}}
	for _, tc := range []struct {
		name string
		edit func(*ModuleConfig)
	}{
		{"unknown engine", func(c *ModuleConfig) { c.Engine = "webkit" }},
		{"public camofox", func(c *ModuleConfig) { c.Engine = "camofox"; c.CamofoxURL = "http://camofox.example" }},
		{"public cloud http", func(c *ModuleConfig) { c.Engine = "cloud"; c.CloudURL = "http://browser.example" }},
		{"cloud credentials", func(c *ModuleConfig) { c.Engine = "cloud"; c.CloudURL = "https://u:p@browser.example" }},
		{"relative bin", func(c *ModuleConfig) { c.Engine = "lightpanda"; c.LightpandaBin = "lightpanda" }},
		{"bad cdp", func(c *ModuleConfig) { c.Engine = "cdp"; c.CDPURL = "ws://public.example/x" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := base
			tc.edit(&cfg)
			if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
				t.Fatalf("New(%+v) = %v, want errBadConfig", cfg, err)
			}
		})
	}
}

// TestEngineWithoutEndpointFailsClosed: an explicit engine whose endpoint is
// missing never falls back to another configured endpoint.
func TestEngineWithoutEndpointFailsClosed(t *testing.T) {
	t.Parallel()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusTeapot)
	}))
	defer srv.Close()
	cfg := camofoxConfig(srv.URL)
	cfg.Engine = "cdp"
	cfg.CamofoxURL = srv.URL
	cfg.CDPURL = ""
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
	_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
	if hits.Load() != 0 {
		t.Fatalf("camofox endpoint was called %d times", hits.Load())
	}
}

// TestCamofoxWire: the Camoufox engine speaks the documented page wire and
// the module maps its replies into the same results as CDP.
func TestCamofoxWire(t *testing.T) {
	t.Parallel()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		switch r.URL.Path {
		case "/v1/navigate":
			writeBody(t, w, `{"url":"https://app.example/dashboard"}`)
		case "/v1/eval":
			writeBody(t, w, `{"value":{"title":"ok"}}`)
		case "/v1/screenshot":
			w.Header().Set("Content-Type", "image/png")
			writeBody(t, w, "\x89PNG\r\n\x1a\nxx")
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	cfg := camofoxConfig(srv.URL)
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
	res, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil || !strings.Contains(res.Content, "https://app.example/dashboard") || res.UntrustedSource != "browser:app.example" {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
	if method, path, body := rec.last(); method != http.MethodPost || path != "/v1/navigate" || !strings.Contains(body, `"url":"https://app.example/"`) {
		t.Fatalf("navigate request = %s %s %s", method, path, body)
	}
	res, err = m.Invoke(t.Context(), "browser_eval", json.RawMessage(`{"expression":"document.title"}`), host.Env{})
	if err != nil || res.Content != `{"title":"ok"}` {
		t.Fatalf("eval = %+v, %v", res, err)
	}
	if _, _, body := rec.last(); !strings.Contains(body, `"expression":"document.title"`) {
		t.Fatalf("eval body = %s", body)
	}
	res, err = m.Invoke(t.Context(), "browser_screenshot", json.RawMessage(`{}`), host.Env{})
	if err != nil || !strings.Contains(res.Content, "10 bytes") {
		t.Fatalf("screenshot = %+v, %v", res, err)
	}
}

// TestCamofoxActions: approved click and type reach the provider as one act
// body each, with the same approval gate as the CDP engine.
func TestCamofoxActions(t *testing.T) {
	t.Parallel()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer srv.Close()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := camofoxConfig(srv.URL)
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client(), Approvals: appr})
	if _, err := m.Invoke(t.Context(), "browser_click", json.RawMessage(`{"x":5,"y":6}`), host.Env{CallID: "c1"}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if _, _, body := rec.last(); !strings.Contains(body, `"kind":"click"`) || !strings.Contains(body, `"x":5`) {
		t.Fatalf("click body = %s", body)
	}
	if _, err := m.Invoke(t.Context(), "browser_type", json.RawMessage(`{"text":"hi"}`), host.Env{CallID: "c2"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if _, _, body := rec.last(); !strings.Contains(body, `"kind":"type"`) || !strings.Contains(body, `"text":"hi"`) {
		t.Fatalf("type body = %s", body)
	}
	if len(appr.requests()) != 2 {
		t.Fatalf("approvals = %d, want 2", len(appr.requests()))
	}
}

func writeBody(t *testing.T, w http.ResponseWriter, body string) {
	t.Helper()
	if _, err := w.Write([]byte(body)); err != nil {
		t.Errorf("fixture write: %v", err)
	}
}

// TestCamofoxErrorClasses: an HTTP refusal maps onto the host vocabulary and
// never reaches the page as a silent success.
func TestCamofoxErrorClasses(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		status int
		code   string
	}{
		{http.StatusBadRequest, "invalid_args"},
		{http.StatusForbidden, "denied"},
		{http.StatusTooManyRequests, "module_unavailable"},
		{http.StatusInternalServerError, "network"},
	} {
		t.Run(fmt.Sprint(tc.status), func(t *testing.T) {
			t.Parallel()
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()
			cfg := camofoxConfig(srv.URL)
			m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
			_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
			var he *host.Error
			if !errors.As(err, &he) || he.Code != tc.code {
				t.Fatalf("err = %v, want %s", err, tc.code)
			}
		})
	}
}

// TestCamofoxRedirectBlocked: the provider's final URL is re-checked against
// the default-deny allowlist.
func TestCamofoxRedirectBlocked(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeBody(t, w, `{"url":"https://evil.example/steal"}`)
	}))
	defer srv.Close()
	cfg := camofoxConfig(srv.URL)
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
	_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "blocked_host" {
		t.Fatalf("err = %v, want blocked_host", err)
	}
}

// TestHTTPEngineBounds: an oversized eval or screenshot reply is refused.
func TestHTTPEngineBounds(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/screenshot" {
			writeBody(t, w, "\x89PNG\r\n\x1a\n"+strings.Repeat("x", 64))
			return
		}
		writeBody(t, w, `{"value":"`+strings.Repeat("y", 64)+`"}`)
	}))
	defer srv.Close()
	eng, err := newHTTPEngine("cloud", srv.URL, Config{MaxResult: 8, MaxScreenshot: 8}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	p, err := NewPage(eng, Config{AllowOrigins: []string{"https://app.example"}, MaxResult: 8, MaxScreenshot: 8})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Eval(t.Context(), "1"); !errors.Is(err, errTooLarge) {
		t.Fatalf("eval err = %v, want errTooLarge", err)
	}
	if _, err := p.Screenshot(t.Context()); !errors.Is(err, errTooLarge) {
		t.Fatalf("screenshot err = %v, want errTooLarge", err)
	}
}

// TestLightpandaEngine: the CLI engine runs `fetch --dump` without a shell,
// bounds the dump, and reports every interactive op as unsupported.
func TestLightpandaEngine(t *testing.T) {
	t.Parallel()
	var argv []string
	cfg := ModuleConfig{
		Enabled: true, Engine: "lightpanda", LightpandaBin: "/opt/lightpanda",
		AllowOrigins: []string{"https://app.example"},
	}
	m := newModule(t.Context(), t, &cfg, Deps{Run: func(_ context.Context, name string, args ...string) ([]byte, error) {
		argv = append([]string{name}, args...)
		return []byte("<html>ok</html>"), nil
	}})
	res, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil || res.UntrustedSource != "browser:app.example" {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
	if strings.Join(argv, " ") != "/opt/lightpanda fetch --dump https://app.example/" {
		t.Fatalf("argv = %v", argv)
	}
	for _, tool := range []string{"browser_eval", "browser_screenshot"} {
		_, err := m.Invoke(t.Context(), tool, json.RawMessage(`{}`), host.Env{})
		var he *host.Error
		if !errors.As(err, &he) || he.Code != "module_unavailable" {
			t.Fatalf("%s err = %v, want module_unavailable", tool, err)
		}
	}
	if _, err := m.Invoke(t.Context(), "browser_click", json.RawMessage(`{"x":1,"y":2}`), host.Env{Approved: true}); err == nil {
		t.Fatal("lightpanda click succeeded")
	}
}

// TestLightpandaFailure: a missing binary or a failing command is a typed
// network/unavailable error, never a stub page.
func TestLightpandaFailure(t *testing.T) {
	t.Parallel()
	cfg := ModuleConfig{
		Enabled: true, Engine: "lightpanda", LightpandaBin: "/opt/lightpanda",
		AllowOrigins: []string{"https://app.example"},
	}
	m := newModule(t.Context(), t, &cfg, Deps{Run: func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exit 1")
	}})
	_, err := m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "network" {
		t.Fatalf("err = %v, want network", err)
	}
	big := ModuleConfig{Enabled: true, Engine: "lightpanda", LightpandaBin: "/opt/lightpanda", AllowOrigins: []string{"https://app.example"}}
	m = newModule(t.Context(), t, &big, Deps{Run: func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Repeat("z", 64<<10+1)), nil
	}})
	_, err = m.Invoke(t.Context(), "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("err = %v, want too_large", err)
	}
}

// TestCloudEngineUsesInjectedClient: the cloud engine is the same wire as
// Camoufox but on the public Tool lane; a loopback URL stays local.
func TestCloudEngineUsesInjectedClient(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/eval" {
			http.NotFound(w, r)
			return
		}
		writeBody(t, w, `{"value":7}`)
	}))
	defer srv.Close()
	cfg := ModuleConfig{
		Enabled: true, Engine: "cloud", CloudURL: srv.URL,
		AllowOrigins: []string{"https://app.example"}, Offline: true,
	}
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
	res, err := m.Invoke(t.Context(), "browser_eval", json.RawMessage(`{"expression":"6+1"}`), host.Env{})
	if err != nil || res.Content != "7" {
		t.Fatalf("eval = %+v, %v", res, err)
	}
}

// TestCamofoxReadWire: snapshot, back, pdf and download use the shared
// /v1/read wire; a provider-reported back URL is re-checked by the guard.
func TestCamofoxReadWire(t *testing.T) {
	t.Parallel()
	rec := &recorded{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		writeBody(t, w, `{"value":[{"ref":1,"role":"button","name":"Go"}],"data":"cGRm","name":"https://app.example/a"}`)
	}))
	defer srv.Close()
	cfg := camofoxConfig(srv.URL)
	m := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client()})
	res, err := invoke(t, m, host.Env{}, "browser_snapshot", `{}`)
	if err != nil || !strings.Contains(res.Content, `"role":"button"`) {
		t.Fatalf("snapshot = %+v, %v", res, err)
	}
	if _, _, body := rec.last(); !strings.Contains(body, `"kind":"snapshot"`) {
		t.Fatalf("snapshot body = %s", body)
	}
	res, err = invoke(t, m, host.Env{}, "browser_pdf", `{}`)
	if err != nil || !strings.Contains(res.Content, "pdf 3 bytes") {
		t.Fatalf("pdf = %+v, %v", res, err)
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m.deps.Approvals = appr
	res, err = invoke(t, m, host.Env{}, "browser_back", `{}`)
	if err != nil || !strings.Contains(res.Content, "https://app.example/a") {
		t.Fatalf("back = %+v, %v", res, err)
	}
	srv.Config.Handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec.add(r)
		writeBody(t, w, `{"name":"https://evil.example/x"}`)
	})
	cfg = camofoxConfig(srv.URL)
	m2 := newModule(t.Context(), t, &cfg, Deps{HTTP: srv.Client(), Approvals: appr})
	_, err = invoke(t, m2, host.Env{}, "browser_back", `{}`)
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "blocked_host" {
		t.Fatalf("foreign back = %v, want blocked_host", err)
	}
}
