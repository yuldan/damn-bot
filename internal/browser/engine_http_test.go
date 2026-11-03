//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// providerFixture is the fake camofox/cloud sidecar: one wire, scripted reply.
type providerFixture struct {
	mu     sync.Mutex
	reqs   []string
	bodies []string
	status int    // non-zero: every route answers with it
	final  string // /v1/navigate reply url
	value  string // /v1/eval value, JSON or empty
	png    []byte
}

func (f *providerFixture) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "unreadable body", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.reqs = append(f.reqs, r.Method+" "+r.URL.Path)
	f.bodies = append(f.bodies, string(body))
	status, final, value, png := f.status, f.final, f.value, f.png
	f.mu.Unlock()
	if status != 0 {
		w.WriteHeader(status)
		return
	}
	switch r.URL.Path {
	case "/v1/navigate":
		f.write(w, `{"url":`+quote(final)+`}`)
	case "/v1/eval":
		if value == "" {
			f.write(w, `{}`)
			return
		}
		f.write(w, `{"value":`+value+`}`)
	case "/v1/screenshot":
		w.Header().Set("Content-Type", "image/png")
		f.write(w, string(png))
	case "/v1/act":
		f.write(w, `{}`)
	default:
		http.NotFound(w, r)
	}
}

func (f *providerFixture) write(w http.ResponseWriter, body string) {
	if _, err := w.Write([]byte(body)); err != nil {
		return
	}
}

func quote(s string) string {
	out, err := json.Marshal(s)
	if err != nil {
		return `""`
	}
	return string(out)
}

func (f *providerFixture) lastBody() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.bodies) == 0 {
		return ""
	}
	return f.bodies[len(f.bodies)-1]
}

// wirePage builds a page over the HTTP engine with the module policy.
func wirePage(t *testing.T, kind, final string) (*Page, *providerFixture) {
	t.Helper()
	fx := &providerFixture{final: final, value: `"ok"`, png: []byte("\x89PNG\r\n\x1a\nrest")}
	srv := httptest.NewServer(fx)
	t.Cleanup(srv.Close)
	cfg := Config{AllowOrigins: []string{"https://app.example"}, LocalProfile: true}.normalize()
	eng, err := newHTTPEngine(kind, srv.URL, cfg, srv.Client())
	if err != nil {
		t.Fatalf("newHTTPEngine: %v", err)
	}
	p, err := NewPage(eng, cfg)
	if err != nil {
		t.Fatalf("NewPage: %v", err)
	}
	t.Cleanup(func() {
		if cerr := p.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return p, fx
}

func TestHTTPEngineNavigate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, fx := wirePage(t, "camofox", "https://app.example/final")
	final, err := p.Navigate(ctx, "https://app.example/")
	if err != nil || final != "https://app.example/final" {
		t.Fatalf("Navigate = %q, %v", final, err)
	}
	if got := fx.lastBody(); got != `{"url":"https://app.example/"}` {
		t.Fatalf("navigate body = %s", got)
	}
	p, _ = wirePage(t, "cloud", "https://evil.example/landing")
	if _, err := p.Navigate(ctx, "https://app.example/"); !errors.Is(err, errBlockedOrigin) {
		t.Fatalf("off-allowlist final err = %v, want errBlockedOrigin", err)
	}
}

func TestHTTPEngineEval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, fx := wirePage(t, "camofox", "https://app.example/")
	value, err := p.Eval(ctx, "1+1")
	if err != nil || value != `"ok"` {
		t.Fatalf("Eval = %q, %v", value, err)
	}
	if got := fx.lastBody(); got != `{"expression":"1+1"}` {
		t.Fatalf("eval body = %s", got)
	}
	fx.mu.Lock()
	fx.value = ""
	fx.mu.Unlock()
	if value, err := p.Eval(ctx, "void 0"); err != nil || value != "null" {
		t.Fatalf("Eval(empty) = %q, %v", value, err)
	}
}

func TestHTTPEngineScreenshot(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, fx := wirePage(t, "camofox", "https://app.example/")
	png, err := p.Screenshot(ctx)
	if err != nil || !strings.HasPrefix(string(png), "\x89PNG") {
		t.Fatalf("Screenshot = %q, %v", png, err)
	}
	fx.mu.Lock()
	fx.png = []byte("not a png")
	fx.mu.Unlock()
	if _, err := p.Screenshot(ctx); !errors.Is(err, errPage) {
		t.Fatalf("non-PNG err = %v, want errPage", err)
	}
}

func TestHTTPEngineBounds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := &providerFixture{value: `"` + strings.Repeat("x", 200) + `"`}
	srv := httptest.NewServer(fx)
	defer srv.Close()
	cfg := Config{AllowOrigins: []string{"https://app.example"}, MaxResult: 64, MaxScreenshot: 8}.normalize()
	eng, err := newHTTPEngine("cloud", srv.URL, cfg, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := eng.Eval(ctx, "1"); !errors.Is(err, errTooLarge) {
		t.Fatalf("eval err = %v, want errTooLarge", err)
	}
	fx.png = []byte("\x89PNG\r\n\x1a\n0123456789")
	if _, err := eng.Screenshot(ctx); !errors.Is(err, errTooLarge) {
		t.Fatalf("screenshot err = %v, want errTooLarge", err)
	}
}

func TestHTTPEngineAct(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	p, fx := wirePage(t, "camofox", "https://app.example/")
	if err := p.Click(ctx, 10, 20, true); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := fx.lastBody(); !strings.Contains(got, `"kind":"click"`) || !strings.Contains(got, `"x":10`) {
		t.Fatalf("click body = %s", got)
	}
	if err := p.Type(ctx, "hello", true); err != nil {
		t.Fatalf("type: %v", err)
	}
	if got := fx.lastBody(); !strings.Contains(got, `"kind":"type"`) || !strings.Contains(got, `"text":"hello"`) {
		t.Fatalf("type body = %s", got)
	}
	eng, ok := p.eng.(*httpEngine)
	if !ok {
		t.Fatalf("engine = %T, want *httpEngine", p.eng)
	}
	serr := eng.Act(ctx, Action{Kind: "scroll"})
	if !errors.Is(serr, errUnsupported) {
		t.Fatalf("scroll err = %v, want errUnsupported", serr)
	}
}

func TestHTTPEngineErrorClasses(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		status int
		want   error
	}{
		{http.StatusBadRequest, errBadConfig},
		{http.StatusUnauthorized, errDenied},
		{http.StatusForbidden, errDenied},
		{http.StatusNotFound, errUnavailable},
		{http.StatusTooManyRequests, errUnavailable},
		{http.StatusInternalServerError, errPage},
		{http.StatusBadGateway, errPage},
		{http.StatusTeapot, errBadConfig},
	} {
		fx := &providerFixture{status: tc.status}
		srv := httptest.NewServer(fx)
		cfg := Config{AllowOrigins: []string{"https://app.example"}, LocalProfile: true}.normalize()
		eng, err := newHTTPEngine("camofox", srv.URL, cfg, srv.Client())
		if err != nil {
			t.Fatalf("newHTTPEngine: %v", err)
		}
		p, err := NewPage(eng, cfg)
		if err != nil {
			t.Fatalf("NewPage: %v", err)
		}
		_, nerr := p.Navigate(ctx, "https://app.example/")
		if !errors.Is(nerr, tc.want) {
			t.Errorf("status %d err = %v, want %v", tc.status, nerr, tc.want)
		}
		if cerr := p.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		srv.Close()
	}
	if _, err := newHTTPEngine("camofox", "ftp://127.0.0.1/x", Config{}, http.DefaultClient); !errors.Is(err, errBadConfig) {
		t.Errorf("ftp endpoint err = %v, want errBadConfig", err)
	}
	if _, err := newHTTPEngine("cloud", "https://browser.example", Config{}, nil); !errors.Is(err, errUnavailable) {
		t.Errorf("nil client err = %v, want errUnavailable", err)
	}
}

func TestModuleCamofox(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	fx := &providerFixture{final: "https://app.example/dashboard", value: `{"ok":true}`, png: []byte("\x89PNG\r\n\x1a\nrest")}
	srv := httptest.NewServer(fx)
	defer srv.Close()
	cfg := ModuleConfig{
		Enabled: true, CamofoxURL: srv.URL, AllowOrigins: []string{"https://app.example"},
		LocalProfile: true,
	}
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	m := newModule(ctx, t, &cfg, Deps{Approvals: appr})
	res, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil || res.UntrustedSource != "browser:app.example" || !strings.Contains(res.Content, "/dashboard") {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
	if res, err := m.Invoke(ctx, "browser_eval", json.RawMessage(`{"expression":"x"}`), host.Env{}); err != nil || res.Content != `{"ok":true}` {
		t.Fatalf("eval = %+v, %v", res, err)
	}
	if res, err := m.Invoke(ctx, "browser_screenshot", json.RawMessage(`{}`), host.Env{}); err != nil || !strings.Contains(res.Content, "12 bytes") {
		t.Fatalf("screenshot = %+v, %v", res, err)
	}
	if _, err := m.Invoke(ctx, "browser_click", json.RawMessage(`{"x":3,"y":4}`), host.Env{CallID: "c1"}); err != nil {
		t.Fatalf("click: %v", err)
	}
	if got := fx.lastBody(); !strings.Contains(got, `"kind":"click"`) {
		t.Fatalf("act body = %s", got)
	}
	if _, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://evil.example/"}`), host.Env{}); err == nil {
		t.Fatal("off-allowlist navigate succeeded")
	}
}

func TestModuleCloudLane(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	doer := doerFunc(func(req *http.Request) (*http.Response, error) {
		body := `{"url":"https://browser.example/final"}`
		if strings.HasSuffix(req.URL.Path, "/v1/eval") {
			body = `{"value":1}`
		}
		return &http.Response{
			StatusCode: http.StatusOK, Request: req,
			Body: io.NopCloser(strings.NewReader(body)), Header: http.Header{},
		}, nil
	})
	for _, tc := range []struct {
		url  string
		want bool
	}{
		{"https://browser.example/api", false},
		{"https://127.0.0.1:9444", true},
	} {
		cfg := ModuleConfig{
			Enabled: true, Engine: "cloud", CloudURL: tc.url,
			AllowOrigins: []string{"https://browser.example"},
		}
		var lanes []bool
		m := newModule(ctx, t, &cfg, Deps{HTTP: func(loopback bool) Doer {
			lanes = append(lanes, loopback)
			return doer
		}})
		if _, err := m.Invoke(ctx, "browser_eval", json.RawMessage(`{"expression":"1"}`), host.Env{}); err != nil {
			t.Fatalf("eval over %s: %v", tc.url, err)
		}
		if len(lanes) != 1 || lanes[0] != tc.want {
			t.Fatalf("lanes(%s) = %v, want [%t]", tc.url, lanes, tc.want)
		}
	}
}

func TestModuleLightpanda(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var mu sync.Mutex
	var runs [][]string
	run := func(_ context.Context, name string, args ...string) ([]byte, error) {
		mu.Lock()
		runs = append(runs, append([]string{name}, args...))
		mu.Unlock()
		return []byte("<html>ok</html>"), nil
	}
	cfg := ModuleConfig{
		Enabled: true, Engine: "lightpanda", LightpandaBin: "/usr/bin/lightpanda",
		AllowOrigins: []string{"https://app.example"},
	}
	m := newModule(ctx, t, &cfg, Deps{Run: run})
	res, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil || res.UntrustedSource != "browser:app.example" {
		t.Fatalf("navigate = %+v, %v", res, err)
	}
	mu.Lock()
	got := append([][]string(nil), runs...)
	mu.Unlock()
	if len(got) != 1 || strings.Join(got[0], " ") != "/usr/bin/lightpanda fetch --dump https://app.example/" {
		t.Fatalf("runs = %v", got)
	}
	for _, tool := range []string{"browser_eval", "browser_screenshot"} {
		_, ierr := m.Invoke(ctx, tool, json.RawMessage(`{"expression":"1"}`), host.Env{})
		var he *host.Error
		if !errors.As(ierr, &he) || he.Code != "module_unavailable" {
			t.Errorf("%s err = %v, want module_unavailable", tool, ierr)
		}
	}
	if _, ierr := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://evil.example/"}`), host.Env{}); ierr == nil {
		t.Fatal("off-allowlist fetch ran")
	}
	mu.Lock()
	if len(runs) != 1 {
		t.Errorf("guard let the CLI run: %v", runs)
	}
	mu.Unlock()

	big := ModuleConfig{
		Enabled: true, Engine: "lightpanda", LightpandaBin: "/usr/bin/lightpanda",
		AllowOrigins: []string{"https://app.example"},
	}
	bigRun := func(context.Context, string, ...string) ([]byte, error) {
		return []byte(strings.Repeat("x", (64<<10)+1)), nil
	}
	mbig := newModule(ctx, t, &big, Deps{Run: bigRun})
	_, berr := mbig.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(berr, &he) || he.Code != "too_large" {
		t.Fatalf("oversized dump err = %v, want too_large", berr)
	}
}

func TestCLIEngineFailures(t *testing.T) {
	t.Parallel()
	if _, err := newCLIEngine("", Config{}, nil); !errors.Is(err, errUnavailable) {
		t.Errorf("empty bin err = %v, want errUnavailable", err)
	}
	if _, err := newCLIEngine("lightpanda", Config{}, nil); !errors.Is(err, errBadConfig) {
		t.Errorf("relative bin err = %v, want errBadConfig", err)
	}
	cfg := Config{AllowOrigins: []string{"https://app.example"}}.normalize()
	eng, err := newCLIEngine("/usr/bin/lightpanda", cfg, func(context.Context, string, ...string) ([]byte, error) {
		return nil, errors.New("exit 1")
	})
	if err != nil {
		t.Fatal(err)
	}
	guard := func(*url.URL) error { return nil }
	if _, nerr := eng.Navigate(context.Background(), "https://app.example/", guard); nerr == nil {
		t.Fatal("failed CLI run succeeded")
	}
	w := &capWriter{max: 3}
	if _, werr := w.Write([]byte("abcd")); !errors.Is(werr, errTooLarge) {
		t.Fatalf("capWriter err = %v, want errTooLarge", werr)
	}
	if _, werr := w.Write([]byte("abc")); werr != nil || w.buf.String() != "abc" {
		t.Fatalf("capWriter = %q, %v", w.buf.String(), werr)
	}
}

func TestNewRejectsHTTPEndpoints(t *testing.T) {
	t.Parallel()
	base := ModuleConfig{Enabled: true, AllowOrigins: []string{"https://app.example"}}
	for _, tc := range []struct {
		name string
		edit func(*ModuleConfig)
	}{
		{"camofox public", func(c *ModuleConfig) { c.Engine = "camofox"; c.CamofoxURL = "https://sidecar.example" }},
		{"cloud plain http", func(c *ModuleConfig) { c.Engine = "cloud"; c.CloudURL = "http://browser.example" }},
		{"lightpanda relative", func(c *ModuleConfig) { c.Engine = "lightpanda"; c.LightpandaBin = "bin/lightpanda" }},
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

// doerFunc adapts a function to the engine's request seam.
type doerFunc func(*http.Request) (*http.Response, error)

func (f doerFunc) Do(req *http.Request) (*http.Response, error) { return f(req) }
