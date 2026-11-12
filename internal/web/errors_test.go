package web

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"testing/fstest"
	"time"
)

// errorStore is a SessionStore whose seams fail, to exercise the fail-closed
// and logged-error branches of the auth handlers.
type errorStore struct {
	get, put, touch, revoke error
}

func (e errorStore) Get(context.Context, string) (Session, bool, error) {
	return Session{}, false, e.get
}

func (e errorStore) Put(context.Context, *Session) error            { return e.put }
func (e errorStore) Touch(context.Context, string, time.Time) error { return e.touch }
func (e errorStore) Revoke(context.Context, string) error           { return e.revoke }

// badPanes is a PaneBackend whose every call fails.
type badPanes struct{}

func (badPanes) List(context.Context) ([]Pane, error) { return nil, errors.New("list down") }

func (badPanes) Open(context.Context, PaneOpen) (Pane, error) { return Pane{}, errors.New("open down") }
func (badPanes) Input(context.Context, string, []byte) error  { return errors.New("input down") }
func (badPanes) Output(context.Context, string, int) (PaneChunk, error) {
	return PaneChunk{}, errors.New("output down")
}
func (badPanes) Close(context.Context, string) error { return errors.New("close down") }

// badProjects is a ProjectSource that fails.
type badProjects struct{}

func (badProjects) Projects(context.Context) ([]Project, error) {
	return nil, errors.New("projects down")
}

// failingSettings fails the preview or the apply seam.
type failingSettings struct{ preview, apply error }

func (f failingSettings) Preview(context.Context, SettingsPatch) (string, error) {
	return "", f.preview
}
func (f failingSettings) Apply(context.Context, SettingsPatch) error { return f.apply }

// fakeDrain verifies the shared drain bearer.
type fakeDrain struct{ token string }

func (d fakeDrain) VerifyBearer(token string) bool { return token == d.token }

// failingOIDC is an authorization-code seam with an injected exchange result.
type failingOIDC struct {
	principal string
	err       error
}

func (failingOIDC) AuthorizeURL(state, _ string) string { return "/idp?state=" + state }
func (f failingOIDC) Exchange(context.Context, string, string) (string, error) {
	return f.principal, f.err
}
func (failingOIDC) EndSessionURL(string) string { return "" }

// TestWebNewValidationBranches covers the fail-closed construction paths and
// the defaulted language/locale/log seams.
func TestWebNewValidationBranches(t *testing.T) {
	base := func() Config {
		return Config{Listen: "127.0.0.1:0", Auth: ModeToken, Sessions: newFakeStore(time.Now)}
	}
	for _, tc := range []struct {
		name string
		cfg  func(*Config)
		want error
	}{
		{"missing listen", func(c *Config) { c.Listen = "" }, ErrListen},
		{"relative unix", func(c *Config) { c.Listen = "unix:relative.sock" }, ErrListen},
		{"control char unix", func(c *Config) { c.Listen = "unix:/tmp/a\nb" }, ErrListen},
		{"missing session store", func(c *Config) { c.Sessions = nil }, nil},
		{"broken assets", func(c *Config) { c.Assets = fstest.MapFS{} }, nil},
	} {
		cfg := base()
		tc.cfg(&cfg)
		_, err := New(&cfg)
		if err == nil {
			t.Errorf("%s: New accepted an invalid config", tc.name)
			continue
		}
		if tc.want != nil && !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	cfg := base()
	cfg.Language = "id"
	cfg.Locales = []string{"id"}
	cfg.Log = slog.New(slog.DiscardHandler)
	srv, err := New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv.cfg.Language != "id" || len(srv.cfg.Locales) != 1 || srv.Addr() != "" {
		t.Fatalf("resolved config = %+v addr %q", srv.cfg, srv.Addr())
	}
	cfg = base()
	srv, err = New(&cfg)
	if err != nil {
		t.Fatal(err)
	}
	if srv.cfg.Language == "" || len(srv.cfg.Locales) == 0 {
		t.Fatalf("defaults = %+v", srv.cfg)
	}
}

// TestWebListenServeAndShutdown exercises the production TCP listener, the
// running server, OpenURL against a bound address and the drain stop.
func TestWebListenServeAndShutdown(t *testing.T) {
	srv, err := New(&Config{Listen: "127.0.0.1:0", Auth: ModeToken, Sessions: newFakeStore(time.Now)})
	if err != nil {
		t.Fatal(err)
	}
	ln, err := srv.Listen(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	addr := srv.Addr()
	if addr == "" || addr != ln.Addr().String() {
		t.Fatalf("addr = %q listener = %q", addr, ln.Addr())
	}
	if open := srv.OpenURL(false); !strings.Contains(open, addr) || !strings.HasPrefix(open, "http://") {
		t.Fatalf("open url = %q", open)
	}
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	ts := httptest.NewServer(http.NotFoundHandler())
	defer ts.Close()
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz = %d", resp.StatusCode)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if serr := srv.Shutdown(ctx); serr != nil {
		t.Fatalf("shutdown = %v", serr)
	}
	if serr := <-served; serr != nil {
		t.Fatalf("serve = %v", serr)
	}
}

// TestWebServeTLSLoadError: a configured certificate pair that cannot be read
// fails before any connection is accepted.
func TestWebServeTLSLoadError(t *testing.T) {
	tlsSrv, err := New(&Config{
		Listen: "127.0.0.1:0", TLSKey: t.TempDir() + "/missing.key",
		TLSCert: t.TempDir() + "/missing.crt", Auth: ModeToken, Sessions: newFakeStore(time.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	var lc net.ListenConfig
	raw, err := lc.Listen(t.Context(), "tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := raw.Close(); cerr != nil {
			t.Errorf("close raw listener: %v", cerr)
		}
	}()
	if serr := tlsSrv.Serve(raw); serr == nil || !strings.Contains(serr.Error(), "TLS") {
		t.Fatalf("serve without a certificate = %v, want a TLS load error", serr)
	}
}

// TestWebListenUnixErrors: a state dir that is a file and a non-empty stale
// socket both fail before the listener is created.
func TestWebListenUnixErrors(t *testing.T) {
	file := t.TempDir() + "/not-a-dir"
	if err := writeTempFile(file); err != nil {
		t.Fatal(err)
	}
	srv, err := New(&Config{
		Listen: "unix:<state_dir>/sub/web.sock", DataDir: file,
		Auth: ModeToken, Sessions: newFakeStore(time.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, lerr := srv.Listen(t.Context()); lerr == nil {
		t.Fatal("unix listen under a file succeeded")
	}

	dir := t.TempDir()
	stale := dir + "/web.sock"
	if merr := mkdirWithFile(stale); merr != nil {
		t.Fatal(merr)
	}
	srv, err = New(&Config{
		Listen: "unix:<state_dir>/web.sock", DataDir: dir,
		Auth: ModeToken, Sessions: newFakeStore(time.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, lerr := srv.Listen(t.Context()); lerr == nil {
		t.Fatal("non-empty stale socket was removed")
	}
}

// TestWebDrainRoute is UI-015: the shared bearer is the whole authorization,
// a browser session is refused, and the surface is absent outside drain mode.
func TestWebDrainRoute(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) {
		c.Auth = ModeDrain
		c.Drain = fakeDrain{token: "shared"}
	})
	post := func(auth string, cookies ...*http.Cookie) result {
		req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/gateway/drain", nil)
		if err != nil {
			t.Fatal(err)
		}
		if auth != "" {
			req.Header.Set("Authorization", "Bearer "+auth)
		}
		for _, c := range cookies {
			req.AddCookie(c)
		}
		resp, err := ts.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		raw, rerr := io.ReadAll(resp.Body)
		if rerr != nil {
			t.Fatalf("read drain body: %v", rerr)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
		return result{status: resp.StatusCode, body: string(raw)}
	}
	if got := post("shared"); got.status != http.StatusAccepted || !strings.Contains(got.body, "draining") {
		t.Fatalf("drain = %d %q", got.status, got.body)
	}
	if got := post("wrong"); got.status != http.StatusUnauthorized {
		t.Fatalf("wrong bearer = %d", got.status)
	}
	if got := get(t, ts, "/api/gateway/drain"); got.status != http.StatusMethodNotAllowed {
		t.Fatalf("drain GET = %d", got.status)
	}
	session, _ := signIn(t, srv, ts)
	if got := post("", session); got.status != http.StatusForbidden {
		t.Fatalf("browser session = %d, want 403", got.status)
	}
	_, tokenMode := newTestServer(t, nil)
	if got := fetch(t, tokenMode, http.MethodPost, "/api/gateway/drain", ""); got.status != http.StatusNotFound {
		t.Fatalf("drain outside drain mode = %d", got.status)
	}
}

// TestWebMethodGuards: wrong verbs are refused uniformly and the CSP report
// sink accepts only POST.
func TestWebMethodGuards(t *testing.T) {
	_, ts := newTestServer(t, nil)
	for _, tc := range []struct {
		method, path string
		want         int
	}{
		{http.MethodGet, "/auth/exchange", http.StatusMethodNotAllowed},
		{http.MethodGet, "/csp-report", http.StatusMethodNotAllowed},
		{http.MethodPost, "/csp-report", http.StatusNoContent},
		{http.MethodPost, "/api/session", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/events", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/panes", http.StatusMethodNotAllowed},
		{http.MethodPost, "/api/projects", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/panes/open", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/panes/input", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/panes/close", http.StatusMethodNotAllowed},
		{http.MethodGet, "/api/logout", http.StatusMethodNotAllowed},
		{http.MethodPost, "/auth", http.StatusMethodNotAllowed},
		{http.MethodPost, "/", http.StatusMethodNotAllowed},
		{http.MethodPost, "/settings", http.StatusMethodNotAllowed},
		{http.MethodGet, "/missing", http.StatusNotFound},
	} {
		got := fetch(t, ts, tc.method, tc.path, "")
		if got.status != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, got.status, tc.want)
		}
	}
}

// TestWebPaneUnavailable: a build without pane/project seams answers 501
// instead of pretending a pane exists.
func TestWebPaneUnavailable(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	session, csrf := signIn(t, srv, ts)
	if got := get(t, ts, "/api/panes", session).status; got != http.StatusNotImplemented {
		t.Fatalf("nil panes list = %d", got)
	}
	if got := get(t, ts, "/api/projects", session).status; got != http.StatusNotImplemented {
		t.Fatalf("nil projects = %d", got)
	}
	if status, _ := panePost(t, ts, "/api/panes/open", `{"kind":"terminal"}`, session, csrf); status != http.StatusNotImplemented {
		t.Fatalf("nil panes open = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/input", `{"id":"x","data":"y"}`, session, csrf); status != http.StatusNotImplemented {
		t.Fatalf("nil panes input = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":"x"}`, session, csrf); status != http.StatusNotImplemented {
		t.Fatalf("nil panes close = %d", status)
	}
}

// TestWebPaneBackendFailures: every backend error has a typed status and the
// guards (bad JSON, unknown kind, missing id, bad offset) answer 400.
func TestWebPaneBackendFailures(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) {
		c.Panes = badPanes{}
		c.Projects = badProjects{}
	})
	session, csrf := signIn(t, srv, ts)
	if got := get(t, ts, "/api/panes", session).status; got != http.StatusServiceUnavailable {
		t.Fatalf("list error = %d", got)
	}
	if got := get(t, ts, "/api/projects", session).status; got != http.StatusServiceUnavailable {
		t.Fatalf("projects error = %d", got)
	}
	if status, _ := panePost(t, ts, "/api/panes/open", `not-json`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("open bad json = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/open", `{"kind":"preview"}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("open refused = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/input", `not-json`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("input bad json = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/input", `{"id":""}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("input empty id = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/input", `{"id":"p1","data":"x"}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("input refused = %d", status)
	}
	if got := get(t, ts, "/api/panes/output", session).status; got != http.StatusBadRequest {
		t.Fatalf("output without id = %d", got)
	}
	if got := get(t, ts, "/api/panes/output?id=p1&offset=x", session).status; got != http.StatusBadRequest {
		t.Fatalf("output bad offset = %d", got)
	}
	if got := get(t, ts, "/api/panes/output?id=p1&offset=99999999", session).status; got != http.StatusBadRequest {
		t.Fatalf("output huge offset = %d", got)
	}
	if got := get(t, ts, "/api/panes/output?id=p1", session).status; got != http.StatusServiceUnavailable {
		t.Fatalf("output error = %d", got)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `not-json`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("close bad json = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":""}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("close empty id = %d", status)
	}
	if status, _ := panePost(t, ts, "/api/panes/close", `{"id":"p1"}`, session, csrf); status != http.StatusBadRequest {
		t.Fatalf("close refused = %d", status)
	}
}

// TestWebSettingsErrorSeams: read-only builds refuse writes; bad, rejected,
// unknown and expired previews each have a typed response.
func TestWebSettingsErrorSeams(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	session, csrf := signIn(t, srv, ts)
	if got := settingsPost(t, ts, session, csrf, "/api/settings/preview", `{"language":"en"}`); got != http.StatusNotImplemented {
		t.Fatalf("read-only preview = %d", got)
	}
	if got := settingsPost(t, ts, session, csrf, "/api/settings/apply", `{"token":"x"}`); got != http.StatusNotImplemented {
		t.Fatalf("read-only apply = %d", got)
	}

	writer := failingSettings{preview: errors.New("bad patch"), apply: errors.New("disk full")}
	now := time.Date(2026, 5, 1, 0, 0, 0, 0, time.UTC)
	srv, ts = newTestServer(t, func(c *Config) {
		c.Settings = writer
		c.Locales = []string{"en", "id"}
		c.Now = func() time.Time { return now }
		c.Sessions = newFakeStore(c.Now)
	})
	session, csrf = signIn(t, srv, ts)
	if got := settingsPost(t, ts, session, csrf, "/api/settings/preview", `not-json`); got != http.StatusBadRequest {
		t.Fatalf("preview bad json = %d", got)
	}
	if got := settingsPost(t, ts, session, csrf, "/api/settings/preview", `{"language":"zz"}`); got != http.StatusBadRequest {
		t.Fatalf("unknown language = %d", got)
	}
	if got := settingsPost(t, ts, session, csrf, "/api/settings/preview", `{"language":"id"}`); got != http.StatusBadRequest {
		t.Fatalf("rejected preview = %d", got)
	}
	if got := settingsPost(t, ts, session, csrf, "/api/settings/apply", `not-json`); got != http.StatusBadRequest {
		t.Fatalf("apply bad json = %d", got)
	}
	if got := settingsPost(t, ts, session, csrf, "/api/settings/apply", `{"token":"unknown"}`); got != http.StatusConflict {
		t.Fatalf("unknown token = %d", got)
	}

	expiring := &expiringSettings{}
	srv, ts = newTestServer(t, func(c *Config) {
		c.Settings = expiring
		c.Locales = []string{"en", "id"}
		c.Now = func() time.Time { return now }
		c.Sessions = newFakeStore(c.Now)
	})
	session, csrf = signIn(t, srv, ts)
	token, _ := settingsPreview(t, ts, session, csrf, "auto")
	now = now.Add(settingsTokenTTL + time.Second)
	if got := settingsPost(t, ts, session, csrf, "/api/settings/apply", `{"token":"`+token+`"}`); got != http.StatusConflict {
		t.Fatalf("expired token = %d", got)
	}
	token, _ = settingsPreview(t, ts, session, csrf, "en")
	if got := settingsPost(t, ts, session, csrf, "/api/settings/apply", `{"token":"`+token+`"}`); got != http.StatusInternalServerError {
		t.Fatalf("failing apply = %d", got)
	}
}

// expiringSettings accepts previews and fails every apply.
type expiringSettings struct{}

func (expiringSettings) Preview(context.Context, SettingsPatch) (string, error) {
	return "- auto\n+ en\n", nil
}

func (expiringSettings) Apply(context.Context, SettingsPatch) error { return errors.New("disk full") }

// TestWebExchangeSessionErrors: a malformed exchange, a failing session store
// and a broken touch are handled without minting or echoing a token.
func TestWebExchangeSessionErrors(t *testing.T) {
	srv, ts := newTestServer(t, func(c *Config) { c.Sessions = errorStore{put: errors.New("db down")} })
	if got := fetch(t, ts, http.MethodPost, "/auth/exchange", `not-json`); got.status != http.StatusBadRequest {
		t.Fatalf("malformed exchange = %d", got.status)
	}
	if got := exchange(t, ts, nonceOf(t, srv.OpenURL(false))); got.status != http.StatusServiceUnavailable {
		t.Fatalf("store failure = %d", got.status)
	}

	_, ts = newTestServer(t, func(c *Config) { c.Sessions = errorStore{get: errors.New("db down")} })
	stale := &http.Cookie{Name: sessionCookie, Value: "stale", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode}
	if got := get(t, ts, "/api/session", stale); got.status != http.StatusUnauthorized {
		t.Fatalf("store get failure = %d", got.status)
	}

	srv, _ = newTestServer(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/logout", nil)
	srv.logout(rec, req, &Session{Principal: "local"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout without a cookie = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	req.AddCookie(&http.Cookie{Name: sessionCookie, Value: "tok", Path: "/", Secure: true, HttpOnly: true, SameSite: http.SameSiteStrictMode})
	srv.cfg.Sessions = errorStore{revoke: errors.New("db down")}
	srv.logout(rec, req, &Session{Principal: "local"})
	if rec.Code != http.StatusNoContent {
		t.Fatalf("logout with a revoke failure = %d", rec.Code)
	}
	rec = httptest.NewRecorder()
	srv.issueTicket(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/ticket", nil), &Session{})
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("ticket without a session cookie = %d", rec.Code)
	}
}

// TestWebCallbackErrors: the OIDC callback is fail-closed for a missing
// provider, a consumed state, an exchange failure and a store failure.
func TestWebCallbackErrors(t *testing.T) {
	_, ts := newTestServer(t, nil)
	if got := get(t, ts, "/auth/callback?state=x&code=y"); got.status != http.StatusUnauthorized {
		t.Fatalf("callback without a provider = %d", got.status)
	}
	cases := []struct {
		name string
		oidc OIDCProvider
		put  error
		want int
	}{
		{"exchange error", failingOIDC{err: errors.New("idp down")}, nil, http.StatusUnauthorized},
		{"empty principal", failingOIDC{}, nil, http.StatusUnauthorized},
		{"store failure", failingOIDC{principal: "alice"}, errors.New("db down"), http.StatusServiceUnavailable},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, ts := newTestServer(t, func(c *Config) {
				c.Auth = ModeOIDC
				c.OIDC = tc.oidc
				if tc.put != nil {
					c.Sessions = errorStore{put: tc.put}
				}
			})
			client := ts.Client()
			client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
			location := doGet(t, client, ts.URL+"/auth").header.Get("Location")
			_, state, ok := strings.Cut(location, "state=")
			if !ok || state == "" {
				t.Fatalf("authorize redirect = %q", location)
			}
			if got := doGet(t, client, ts.URL+"/auth/callback?state="+state+"&code=abc"); got.status != tc.want {
				t.Fatalf("callback = %d, want %d", got.status, tc.want)
			}
			if got := doGet(t, client, ts.URL+"/auth/callback?state="+state+"&code=abc"); got.status != http.StatusUnauthorized {
				t.Fatalf("consumed state = %d", got.status)
			}
		})
	}
}

// TestWebAssetGuards: only a known extension inside the embedded tree is
// served; traversal, empty and template names answer 404.
func TestWebAssetGuards(t *testing.T) {
	srv, ts := newTestServer(t, nil)
	for _, path := range []string{"/assets/", "/assets/index.html", "/assets/app.txt"} {
		if got := get(t, ts, path); got.status != http.StatusNotFound {
			t.Errorf("%s = %d, want 404", path, got.status)
		}
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/assets/../secret.js", nil)
	srv.asset(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("traversal = %d, want 404", rec.Code)
	}
	for _, tc := range []struct {
		name string
		want bool
	}{
		{"app.js", true},
		{"app.css", true},
		{"x.svg", true},
		{"x.png", true},
		{"x.woff2", true},
		{"index.html", false},
		{"", false},
		{"../x.js", false},
		{"/x.js", false},
		{"x.txt", false},
	} {
		if got := servableAsset(tc.name); got != tc.want {
			t.Errorf("servableAsset(%q) = %v, want %v", tc.name, got, tc.want)
		}
	}
	if got := fetch(t, ts, http.MethodPost, "/manifest.webmanifest", ""); got.status != http.StatusMethodNotAllowed {
		t.Fatalf("manifest POST = %d", got.status)
	}
}

// TestWebRenderFailure: a template execution failure is a plain 500 and leaks
// no partial page.
func TestWebRenderFailure(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	bad, err := template.New("bad").Parse("{{.Nope}}")
	if err != nil {
		t.Fatal(err)
	}
	rec := httptest.NewRecorder()
	srv.render(rec, bad, &pageData{})
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("render failure = %d", rec.Code)
	}
}

// TestWebHelperGuards covers locale direction, origin parsing and bounded
// integers.
func TestWebHelperGuards(t *testing.T) {
	if dirOf("ar") != "rtl" || dirOf("en") != "ltr" {
		t.Fatalf("dirOf = %q/%q", dirOf("ar"), dirOf("en"))
	}
	if got := originHost("http://[::1"); got != "" {
		t.Fatalf("invalid origin = %q", got)
	}
	if got := originHost("https://x.test:8443"); got != "x.test:8443" {
		t.Fatalf("origin = %q", got)
	}
	for _, tc := range []struct {
		raw  string
		max  int
		want int
		ok   bool
	}{
		{"0", 10, 0, true},
		{"12", 12, 12, true},
		{"", 10, 0, false},
		{"-1", 10, 0, false},
		{"x", 10, 0, false},
		{"99", 10, 0, false},
	} {
		got, err := atoiBounded(tc.raw, tc.max)
		if (err == nil) != tc.ok || (tc.ok && got != tc.want) {
			t.Errorf("atoiBounded(%q, %d) = %d, %v", tc.raw, tc.max, got, err)
		}
	}
}

// TestWebHostAllowlistModes: a loopback bind allows only loopback Hosts, while
// a remote bind requires an exact allowlist entry.
func TestWebHostAllowlistModes(t *testing.T) {
	srv, _ := newTestServer(t, nil)
	if !srv.hostAllowed("") || !srv.hostAllowed("127.0.0.1:8080") || srv.hostAllowed("evil.test") {
		t.Fatal("loopback host allowlist misbehaves")
	}
	remote, err := New(&Config{
		Listen: "0.0.0.0:0", AllowRemote: true, TLSCert: "cert", TLSKey: "key",
		AllowedHosts: []string{"Example.Test"}, Auth: ModeToken, Sessions: newFakeStore(time.Now),
	})
	if err != nil {
		t.Fatal(err)
	}
	if !remote.hostAllowed("example.test") || !remote.hostAllowed("example.test:8443") {
		t.Fatal("allowed remote host refused")
	}
	if remote.hostAllowed("other.test") {
		t.Fatal("foreign remote host accepted")
	}
}

// writeTempFile creates one regular file for the unix-listen failure tests.
func writeTempFile(path string) error {
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		return fmt.Errorf("write test file: %w", err)
	}
	return nil
}

// mkdirWithFile creates a non-empty directory at path so os.Remove refuses it.
func mkdirWithFile(path string) error {
	if err := os.Mkdir(path, 0o700); err != nil {
		return fmt.Errorf("mkdir test dir: %w", err)
	}
	if err := os.WriteFile(path+"/x", []byte("x"), 0o600); err != nil {
		return fmt.Errorf("write test file: %w", err)
	}
	return nil
}
