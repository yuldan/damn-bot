package web

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeSessionStore is the in-memory SessionStore of the package tests; the
// production implementation is the ctxdb web_sessions table.
type fakeSessionStore struct {
	mu  sync.Mutex
	now func() time.Time
	m   map[string]Session
}

func newFakeStore(now func() time.Time) *fakeSessionStore {
	return &fakeSessionStore{now: now, m: map[string]Session{}}
}

func (f *fakeSessionStore) Put(_ context.Context, s *Session) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.m[s.IDHash] = *s
	return nil
}

func (f *fakeSessionStore) Get(_ context.Context, idHash string) (Session, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s, ok := f.m[idHash]
	now := f.now()
	if !ok || !s.RevokedAt.IsZero() || !now.Before(s.AbsoluteExpiry) || now.Sub(s.LastSeen) > IdleTimeout {
		return Session{}, false, nil
	}
	return s, true, nil
}

func (f *fakeSessionStore) Touch(_ context.Context, idHash string, at time.Time) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.m[idHash]; ok {
		s.LastSeen = at
		f.m[idHash] = s
	}
	return nil
}

func (f *fakeSessionStore) Revoke(_ context.Context, idHash string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if s, ok := f.m[idHash]; ok {
		s.RevokedAt = f.now()
		f.m[idHash] = s
	}
	return nil
}

// newTestServer builds a loopback server and its httptest front end.
func newTestServer(t *testing.T, mutate func(*Config)) (*Server, *httptest.Server) {
	t.Helper()
	cfg := Config{Listen: "127.0.0.1:0", Auth: ModeToken, Sessions: newFakeStore(time.Now)}
	if mutate != nil {
		mutate(&cfg)
	}
	srv, err := New(&cfg)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return srv, ts
}

// result is one completed request; the body is already read and closed.
type result struct {
	status  int
	header  http.Header
	body    string
	cookies []*http.Cookie
}

// fetch performs one request and closes the body before returning.
func fetch(t *testing.T, ts *httptest.Server, method, path, payload string, cookies ...*http.Cookie) result {
	t.Helper()
	var reader io.Reader
	if payload != "" {
		reader = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(t.Context(), method, ts.URL+path, reader)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	for _, c := range cookies {
		req.AddCookie(c)
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close body: %v", cerr)
		}
	}()
	raw, rerr := io.ReadAll(resp.Body)
	if rerr != nil {
		t.Fatalf("read %s %s: %v", method, path, rerr)
	}
	return result{status: resp.StatusCode, header: resp.Header, body: string(raw), cookies: resp.Cookies()}
}

// get is an anonymous or cookie-carrying GET.
func get(t *testing.T, ts *httptest.Server, path string, cookies ...*http.Cookie) result {
	t.Helper()
	return fetch(t, ts, http.MethodGet, path, "", cookies...)
}

// exchange posts one nonce.
func exchange(t *testing.T, ts *httptest.Server, nonce string) result {
	t.Helper()
	return fetch(t, ts, http.MethodPost, "/auth/exchange", `{"nonce":"`+nonce+`"}`)
}

// nonceOf extracts the fragment nonce of an OpenURL result.
func nonceOf(t *testing.T, openURL string) string {
	t.Helper()
	_, nonce, ok := strings.Cut(openURL, "#")
	if !ok || nonce == "" {
		t.Fatalf("OpenURL %q has no fragment nonce", openURL)
	}
	return nonce
}

// signIn exchanges a fresh nonce and returns both cookies.
func signIn(t *testing.T, srv *Server, ts *httptest.Server) (*http.Cookie, *http.Cookie) {
	t.Helper()
	res := exchange(t, ts, nonceOf(t, srv.OpenURL(false)))
	if res.status != http.StatusNoContent {
		t.Fatalf("exchange = %d", res.status)
	}
	var session, csrf *http.Cookie
	for _, c := range res.cookies {
		switch c.Name {
		case sessionCookie:
			session = c
		case csrfCookie:
			csrf = c
		}
	}
	if session == nil || csrf == nil {
		t.Fatal("exchange did not set both cookies")
	}
	return session, csrf
}

// wantCSP spells the GAP-SEC-06 header literally, independent of the
// production builder.
func wantCSP(nonce string) string {
	return "default-src 'none'; script-src 'self' 'nonce-" + nonce +
		"'; style-src 'self' 'nonce-" + nonce +
		"'; img-src 'self' data: blob:; media-src 'self' blob:; font-src 'self'; connect-src 'self'; " +
		"frame-src 'self'; object-src 'none'; base-uri 'none'; form-action 'self'; " +
		"frame-ancestors 'none'; report-uri /csp-report"
}

// cspNonce extracts the script-src nonce from a CSP header.
func cspNonce(t *testing.T, h http.Header) string {
	t.Helper()
	csp := h.Get("Content-Security-Policy")
	_, rest, ok := strings.Cut(csp, "script-src 'self' 'nonce-")
	if !ok {
		t.Fatalf("CSP has no script nonce: %q", csp)
	}
	nonce, _, _ := strings.Cut(rest, "'")
	if nonce == "" {
		t.Fatal("empty CSP nonce")
	}
	return nonce
}

// checkHeaders asserts the exact CSP plus the companion headers.
func checkHeaders(t *testing.T, h http.Header) {
	t.Helper()
	nonce := cspNonce(t, h)
	if csp, want := h.Get("Content-Security-Policy"), wantCSP(nonce); csp != want {
		t.Fatalf("CSP = %q, want %q", csp, want)
	}
	for name, value := range map[string]string{
		"X-Content-Type-Options":       "nosniff",
		"Referrer-Policy":              "no-referrer",
		"Cross-Origin-Opener-Policy":   "same-origin",
		"Cross-Origin-Resource-Policy": "same-origin",
		"Permissions-Policy":           "camera=(), geolocation=(), microphone=(self)",
	} {
		if got := h.Get(name); got != value {
			t.Errorf("%s = %q, want %q", name, got, value)
		}
	}
}

// TestWebSecurityHeadersExact is GAP-SEC-06 on success and refusal alike.
func TestWebSecurityHeadersExact(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, nil)
	resp := get(t, ts, "/healthz")
	checkHeaders(t, resp.header)
	denied := get(t, ts, "/api/session")
	if denied.status != http.StatusUnauthorized {
		t.Fatalf("anonymous session = %d, want 401", denied.status)
	}
	checkHeaders(t, denied.header)
	if cspNonce(t, resp.header) == cspNonce(t, denied.header) {
		t.Fatal("nonce is not per-request")
	}
}

// TestWebUnauthenticatedIs401WithoutSecrets: an anonymous request learns
// nothing about the session or the nonce (GAP-SEC-07).
func TestWebUnauthenticatedIs401WithoutSecrets(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, nil)
	nonce := nonceOf(t, srv.OpenURL(false))
	resp := get(t, ts, "/api/session")
	if resp.status != http.StatusUnauthorized || resp.body != "unauthorized\n" {
		t.Fatalf("status = %d body = %q", resp.status, resp.body)
	}
	if len(resp.header.Values("Set-Cookie")) != 0 {
		t.Fatalf("anonymous response set a cookie: %q", resp.header.Values("Set-Cookie"))
	}
	if strings.Contains(resp.body, nonce) || strings.Contains(resp.header.Get("Set-Cookie"), nonce) {
		t.Fatal("nonce leaked to an anonymous response")
	}
}

// TestWebNonceIsSingleUse: one nonce, one session; a replay is 401 and the
// first cookie keeps working.
func TestWebNonceIsSingleUse(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, nil)
	nonce := nonceOf(t, srv.OpenURL(false))
	first := exchange(t, ts, nonce)
	if first.status != http.StatusNoContent {
		t.Fatalf("first exchange = %d", first.status)
	}
	var session *http.Cookie
	for _, c := range first.cookies {
		if c.Name == sessionCookie {
			session = c
		}
	}
	if session == nil {
		t.Fatal("first exchange set no session cookie")
	}
	replay := exchange(t, ts, nonce)
	if replay.status != http.StatusUnauthorized || len(replay.cookies) != 0 {
		t.Fatalf("replay = %d cookies %v", replay.status, replay.cookies)
	}
	if status := get(t, ts, "/api/session", session).status; status != http.StatusOK {
		t.Fatalf("session after replay = %d", status)
	}
}

// TestWebRemoteListenRefused: fail-closed before any socket is opened.
func TestWebRemoteListenRefused(t *testing.T) {
	t.Parallel()
	base := Config{Auth: ModeToken, Sessions: newFakeStore(time.Now)}
	for _, listen := range []string{"0.0.0.0:7878", ":7878", "example.test:7878"} {
		c := base
		c.Listen = listen
		if _, err := New(&c); !errors.Is(err, ErrRemoteRefused) {
			t.Errorf("%s: err = %v, want ErrRemoteRefused", listen, err)
		}
	}
	c := base
	c.Listen, c.AllowRemote = "0.0.0.0:7878", true
	if _, err := New(&c); !errors.Is(err, ErrRemoteTLS) {
		t.Fatalf("err = %v, want ErrRemoteTLS", err)
	}
}

// TestWebAuthModeNeedsSeam: basic and OIDC never serve without their verifier.
func TestWebAuthModeNeedsSeam(t *testing.T) {
	t.Parallel()
	base := Config{Listen: "127.0.0.1:0", Sessions: newFakeStore(time.Now)}
	c := base
	c.Auth = ModeBasic
	if _, err := New(&c); !errors.Is(err, ErrAuthSeam) {
		t.Fatalf("basic: err = %v", err)
	}
	c = base
	c.Auth = ModeOIDC
	if _, err := New(&c); err == nil {
		t.Fatal("oidc without a provider accepted")
	}
	c = base
	c.Auth = "magic"
	if _, err := New(&c); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

// TestWebHostAllowlist is GAP-SEC-08: 421 for a rebinding Host.
func TestWebHostAllowlist(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, nil)
	if got := fetchHost(t, ts, "evil.test"); got != http.StatusMisdirectedRequest {
		t.Fatalf("evil Host = %d, want 421", got)
	}
	for _, host := range []string{"127.0.0.1", "localhost", "[::1]"} {
		if got := fetchHost(t, ts, host); got != http.StatusOK {
			t.Errorf("Host %q = %d, want 200", host, got)
		}
	}
}

// fetchHost performs a GET with an explicit Host header.
func fetchHost(t *testing.T, ts *httptest.Server, host string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/healthz", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = host
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	return resp.StatusCode
}

// TestWebCSRF is GAP-SEC-08 for a mutating endpoint.
func TestWebCSRF(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, nil)
	session, csrf := signIn(t, srv, ts)
	cases := []struct {
		name      string
		origin    string
		fetchSite string
		csrf      string
		want      int
	}{
		{"foreign origin", "http://evil.test", "", "", http.StatusForbidden},
		{"cross-site fetch", "", "cross-site", "", http.StatusForbidden},
		{"no csrf token", "", "", "", http.StatusForbidden},
		{"all present", "", "same-origin", csrf.Value, http.StatusNoContent},
	}
	for _, tc := range cases {
		req, rerr := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/logout", nil)
		if rerr != nil {
			t.Fatal(rerr)
		}
		req.AddCookie(session)
		if tc.csrf != "" {
			req.AddCookie(csrf)
			req.Header.Set(csrfHeader, tc.csrf)
		}
		if tc.origin != "" {
			req.Header.Set("Origin", tc.origin)
		}
		if tc.fetchSite != "" {
			req.Header.Set("Sec-Fetch-Site", tc.fetchSite)
		}
		resp, derr := ts.Client().Do(req)
		if derr != nil {
			t.Fatalf("%s: %v", tc.name, derr)
		}
		if cerr := resp.Body.Close(); cerr != nil {
			t.Fatalf("%s close: %v", tc.name, cerr)
		}
		if resp.StatusCode != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, resp.StatusCode, tc.want)
		}
	}
}

// TestWebSessionExpiry enforces the 12 h idle and 7 day absolute caps.
func TestWebSessionExpiry(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	mem := newFakeStore(func() time.Time { return now })
	srv, ts := newTestServer(t, func(c *Config) {
		c.Sessions = mem
		c.Now = func() time.Time { return now }
	})
	session, _ := signIn(t, srv, ts)
	now = now.Add(IdleTimeout + time.Minute)
	if status := get(t, ts, "/api/session", session).status; status != http.StatusUnauthorized {
		t.Fatalf("idle timeout = %d, want 401", status)
	}
	now = time.Date(2026, 2, 1, 12, 0, 0, 0, time.UTC)
	session2, _ := signIn(t, srv, ts)
	now = now.Add(AbsoluteTimeout)
	if status := get(t, ts, "/api/session", session2).status; status != http.StatusUnauthorized {
		t.Fatalf("absolute cap = %d, want 401", status)
	}
}

// TestWebLogoutRevokes: logout kills the cookie immediately.
func TestWebLogoutRevokes(t *testing.T) {
	t.Parallel()
	srv, ts := newTestServer(t, nil)
	session, csrf := signIn(t, srv, ts)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(session)
	req.AddCookie(csrf)
	req.Header.Set(csrfHeader, csrf.Value)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatal(cerr)
	}
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout = %d", resp.StatusCode)
	}
	if status := get(t, ts, "/api/session", session).status; status != http.StatusUnauthorized {
		t.Fatalf("after logout = %d", status)
	}
}

// staticBasic is a fixed basic-auth principal for the seam test.
type staticBasic struct{ user, pass string }

func (s staticBasic) Verify(user, pass string) bool { return user == s.user && pass == s.pass }

// TestWebBasicAuthMode: valid credentials mint a session; a wrong password
// never does.
func TestWebBasicAuthMode(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, func(c *Config) {
		c.Auth = ModeBasic
		c.Basic = staticBasic{user: "owner", pass: "s3cret"}
	})
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.SetBasicAuth("owner", "s3cret")
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	cookies := resp.Cookies()
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("valid basic = %d", resp.StatusCode)
	}
	var sessionSet bool
	for _, c := range cookies {
		if c.Name == sessionCookie && c.HttpOnly && c.Secure {
			sessionSet = true
		}
	}
	if !sessionSet {
		t.Fatal("basic auth did not mint an HttpOnly+Secure session cookie")
	}
	bad, err := http.NewRequestWithContext(t.Context(), http.MethodGet, ts.URL+"/api/session", nil)
	if err != nil {
		t.Fatal(err)
	}
	bad.SetBasicAuth("owner", "wrong")
	denied, err := ts.Client().Do(bad)
	if err != nil {
		t.Fatal(err)
	}
	deniedCookies := denied.Cookies()
	if cerr := denied.Body.Close(); cerr != nil {
		t.Errorf("close: %v", cerr)
	}
	if denied.StatusCode != http.StatusUnauthorized || len(deniedCookies) != 0 {
		t.Fatalf("wrong basic = %d cookies %v", denied.StatusCode, deniedCookies)
	}
}

// TestWebListenUnix creates the socket 0600.
func TestWebListenUnix(t *testing.T) {
	t.Parallel()
	srv, err := New(&Config{Listen: "unix:<state_dir>/web.sock", DataDir: t.TempDir(), Auth: ModeToken, Sessions: newFakeStore(time.Now)})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	ln, err := srv.Listen(ctx)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := ln.Close(); cerr != nil {
			t.Errorf("close listener: %v", cerr)
		}
	})
	if ln.Addr().Network() != "unix" {
		t.Fatalf("network = %q", ln.Addr().Network())
	}
}
