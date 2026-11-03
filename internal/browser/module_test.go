//go:build !minimal

package browser

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type fakeApprovals struct {
	mu     sync.Mutex
	answer host.Approval
	reqs   []*host.ApprovalRequest
}

func (f *fakeApprovals) Approve(_ context.Context, req *host.ApprovalRequest) (host.Approval, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reqs = append(f.reqs, req)
	return f.answer, nil
}

func (f *fakeApprovals) requests() []*host.ApprovalRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*host.ApprovalRequest(nil), f.reqs...)
}

func dialTo(f Transport) DialFunc {
	return func(context.Context, string, time.Duration) (Transport, error) { return f, nil }
}

func newModule(ctx context.Context, t *testing.T, cfg *ModuleConfig, deps Deps) *Module {
	t.Helper()
	m, err := New(cfg, deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := m.Start(ctx); err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(func() {
		if err := m.Stop(ctx); err != nil {
			t.Errorf("Stop: %v", err)
		}
	})
	return m
}

func moduleConfig() ModuleConfig {
	return ModuleConfig{
		Enabled: true, CDPURL: "ws://127.0.0.1:9222/devtools/page/x",
		AllowOrigins: []string{"https://app.example"}, LocalProfile: true,
	}
}

func TestModuleTools(t *testing.T) {
	t.Parallel()
	want := map[string]string{
		"browser_navigate": "read", "browser_eval": "read",
		"browser_screenshot": "read", "browser_click": "non_idempotent",
		"browser_type": "non_idempotent",
	}
	tools := Tools()
	if len(tools) != len(want) {
		t.Fatalf("tools = %d, want %d", len(tools), len(want))
	}
	for _, tool := range tools {
		if want[tool.Name] != tool.Effect || len(tool.Schema) == 0 {
			t.Errorf("%s: effect %q, schema %q", tool.Name, tool.Effect, tool.Schema)
		}
	}
}

func TestModuleUnavailableWithoutEndpoint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := moduleConfig()
	cfg.CDPURL = ""
	f := newFake()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f)})
	_, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("err = %v, want module_unavailable", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("CDP calls = %v, want none", f.calls)
	}
}

func TestModuleNewRejectsEndpoint(t *testing.T) {
	t.Parallel()
	for _, raw := range []string{"ftp://127.0.0.1/x", "ws:///x", "ws://user@127.0.0.1/x", "not a url"} {
		cfg := moduleConfig()
		cfg.CDPURL = raw
		if _, err := New(&cfg, Deps{}); !errors.Is(err, errBadConfig) {
			t.Errorf("New(%q) = %v, want errBadConfig", raw, err)
		}
	}
}

func TestModuleNavigateAndScreenshotTaint(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake()
	navEvents("https://app.example/dashboard")(f)
	f.results["Page.captureScreenshot"] = json.RawMessage(`{"data":"cG5n"}`)
	cfg := moduleConfig()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f)})

	res, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{})
	if err != nil {
		t.Fatalf("navigate: %v", err)
	}
	if res.UntrustedSource != "browser:app.example" || !strings.Contains(res.Content, "https://app.example/") {
		t.Fatalf("navigate result = %+v", res)
	}
	res, err = m.Invoke(ctx, "browser_screenshot", json.RawMessage(`{}`), host.Env{})
	if err != nil || res.UntrustedSource != "browser:app.example" || !strings.Contains(res.Content, "3 bytes") {
		t.Fatalf("screenshot = %+v, %v", res, err)
	}
	if !f.called("Page.captureScreenshot") {
		t.Fatalf("calls = %v", f.calls)
	}
	if got := m.Status(); got != host.StatusReady {
		t.Fatalf("status = %q, want ready", got)
	}
}

func TestModuleBlockedOrigin(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake()
	cfg := moduleConfig()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f)})
	_, err := m.Invoke(context.Background(), "browser_navigate", json.RawMessage(`{"url":"https://evil.example/"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "blocked_host" {
		t.Fatalf("err = %v, want blocked_host", err)
	}
	if len(f.calls) != 0 {
		t.Fatalf("CDP calls = %v, want none", f.calls)
	}
}

func TestModuleClickApproval(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, tc := range []struct {
		name   string
		answer host.Approval
		sent   bool
	}{
		{"denied", host.ApprovalDeny, false},
		{"session is not enough", host.ApprovalAllowSession, false},
		{"allow once", host.ApprovalAllowOnce, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f := newFake()
			appr := &fakeApprovals{answer: tc.answer}
			cfg := moduleConfig()
			m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
			_, err := m.Invoke(ctx, "browser_click", json.RawMessage(`{"x":10,"y":20}`), host.Env{CallID: "c1"})
			if !tc.sent {
				var he *host.Error
				if !errors.As(err, &he) || he.Code != "denied" {
					t.Fatalf("err = %v, want denied", err)
				}
			} else if err != nil {
				t.Fatalf("click: %v", err)
			}
			if got := f.called("Input.dispatchMouseEvent"); got != tc.sent {
				t.Fatalf("mouse dispatched = %v, want %v (calls %v)", got, tc.sent, f.calls)
			}
			reqs := appr.requests()
			if len(reqs) != 1 {
				t.Fatalf("approval requests = %d, want 1", len(reqs))
			}
			req := reqs[0]
			if req.Tool != "browser_click" || req.Effect != host.EffectNonIdempotent ||
				req.CallID != "c1" || !strings.Contains(req.Preview, "browser_click") {
				t.Fatalf("request = %+v", req)
			}
		})
	}
}

func TestModuleTypeApprovalHidesContent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalDeny}
	cfg := moduleConfig()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	if _, err := m.Invoke(ctx, "browser_type", json.RawMessage(`{"text":"hunter2"}`), host.Env{CallID: "c2"}); err == nil {
		t.Fatal("unapproved type succeeded")
	}
	reqs := appr.requests()
	if len(reqs) != 1 || strings.Contains(reqs[0].Preview, "hunter2") {
		t.Fatalf("preview leaked text: %+v", reqs)
	}
	if f.called("Input.insertText") {
		t.Fatalf("calls = %v", f.calls)
	}
}

func TestModuleDecodeArgsStrict(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := moduleConfig()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(newFake())})
	_, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/","x":1}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "invalid_args" {
		t.Fatalf("err = %v, want invalid_args", err)
	}
}

func TestModuleEvaluate(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	f := newFake()
	f.results["Runtime.evaluate"] = json.RawMessage(`{"result":{"type":"string","value":"ok"}}`)
	cfg := moduleConfig()
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f)})
	res, err := m.Invoke(ctx, "browser_eval", json.RawMessage(`{"expression":"1+1"}`), host.Env{})
	if err != nil || res.Content != `"ok"` || res.UntrustedSource != "browser:page" {
		t.Fatalf("eval = %+v, %v", res, err)
	}
	f.results["Runtime.evaluate"] = json.RawMessage(`{bad`)
	if _, err := m.Invoke(ctx, "browser_eval", json.RawMessage(`{"expression":"1"}`), host.Env{}); err == nil {
		t.Fatal("malformed evaluate reply succeeded")
	}
}

func TestModuleDiscover(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	page := "https://app.example/"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/devtools/json/list" {
			http.NotFound(w, r)
			return
		}
		if _, werr := w.Write([]byte(`[{"type":"page","url":"https://app.example/","webSocketDebuggerUrl":"ws://127.0.0.1:9222/devtools/page/F"}]`)); werr != nil {
			t.Errorf("discovery fixture: %v", werr)
		}
	}))
	defer srv.Close()
	cfg := moduleConfig()
	cfg.CDPURL = srv.URL + "/devtools"
	f := newFake()
	navEvents(page)(f)
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(f)})
	if _, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{}); err != nil {
		t.Fatalf("discovered navigate: %v", err)
	}
}

func TestModuleDiscoverFailures(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, body := range []string{`{bad`, `[]`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if _, werr := w.Write([]byte(body)); werr != nil {
				t.Errorf("fixture: %v", werr)
			}
		}))
		cfg := moduleConfig()
		cfg.CDPURL = srv.URL
		m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(newFake())})
		if _, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{}); err == nil {
			t.Errorf("discovery %q succeeded", body)
		}
		srv.Close()
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "no", http.StatusInternalServerError)
	}))
	defer srv.Close()
	cfg := moduleConfig()
	cfg.CDPURL = srv.URL
	m := newModule(ctx, t, &cfg, Deps{Dial: dialTo(newFake())})
	if _, err := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{}); err == nil {
		t.Fatal("500 discovery succeeded")
	}
}

func TestNewFromConfig(t *testing.T) {
	t.Parallel()
	raw := json.RawMessage(`{"enabled":true,"cdp_url":"ws://127.0.0.1:9222/devtools/page/x","allow_origins":["https://app.example"],"local_profile":true}`)
	m, err := NewFromConfig(raw, Deps{Dial: dialTo(newFake())})
	if err != nil || !m.cfg.Enabled || m.cfg.CDPURL == "" {
		t.Fatalf("NewFromConfig = %+v, %v", m, err)
	}
	for _, bad := range []json.RawMessage{
		nil, json.RawMessage(`{`), json.RawMessage(`{"bogus":1}`),
		json.RawMessage(`{"enabled":true,"cdp_url":"ftp://127.0.0.1/x"}`),
	} {
		if _, err := NewFromConfig(bad, Deps{}); err == nil {
			t.Errorf("NewFromConfig(%q) succeeded", bad)
		}
	}
}

func TestModuleDisabledAndDialError(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	cfg := moduleConfig()
	cfg.Enabled = false
	m, err := New(&cfg, Deps{})
	if err != nil {
		t.Fatal(err)
	}
	if serr := m.Start(ctx); serr != nil {
		t.Fatal(serr)
	}
	if m.Status() != host.StatusStopped {
		t.Fatalf("disabled status = %q", m.Status())
	}
	if _, ierr := m.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{}); ierr == nil {
		t.Fatal("disabled module served a call")
	}
	if cerr := m.Stop(ctx); cerr != nil {
		t.Fatal(cerr)
	}

	dialErr := errors.New("dial refused")
	mc := moduleConfig()
	broken, err := New(&mc, Deps{Dial: func(context.Context, string, time.Duration) (Transport, error) {
		return nil, dialErr
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := broken.Start(ctx); err != nil {
		t.Fatal(err)
	}
	var he *host.Error
	if _, err := broken.Invoke(ctx, "browser_navigate", json.RawMessage(`{"url":"https://app.example/"}`), host.Env{}); !errors.As(err, &he) || he.Code != "network" {
		t.Fatalf("dial error = %v", err)
	}
}

func TestModuleTypeApprovalAllowOnce(t *testing.T) {
	t.Parallel()
	f := newFake()
	appr := &fakeApprovals{answer: host.ApprovalAllowOnce}
	cfg := moduleConfig()
	m := newModule(t.Context(), t, &cfg, Deps{Dial: dialTo(f), Approvals: appr})
	if _, err := m.Invoke(t.Context(), "browser_type", json.RawMessage(`{"text":"ok"}`), host.Env{CallID: "c3"}); err != nil {
		t.Fatalf("type: %v", err)
	}
	if !f.called("Input.insertText") || len(appr.requests()) != 1 {
		t.Fatalf("calls = %v, approvals = %d", f.calls, len(appr.requests()))
	}
}

func TestModuleNewAllowlistError(t *testing.T) {
	t.Parallel()
	for _, cfg := range []ModuleConfig{
		func() ModuleConfig { c := moduleConfig(); c.AllowOrigins = []string{"not an origin"}; return c }(),
		func() ModuleConfig { c := moduleConfig(); c.CDPURL = "ws://127.0.0.1/x?q=1"; return c }(),
	} {
		if _, err := New(&cfg, Deps{}); err == nil {
			t.Errorf("New(%+v) succeeded", cfg)
		}
	}
}
