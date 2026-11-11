package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeOIDC is the authorization-code seam used by the GAP-SEC-09 test.
type fakeOIDC struct{ end string }

func (f fakeOIDC) AuthorizeURL(state, _ string) string { return "/idp?state=" + state }
func (f fakeOIDC) Exchange(context.Context, string, string) (string, error) {
	return "alice", nil
}
func (f fakeOIDC) EndSessionURL(principal string) string { return f.end + principal }

// TestWebSessionRevoke is GAP-SEC-09: a server-side revoke (logout, revoke-all
// or password change) kills the old cookie on the very next request, the 7-day
// absolute cap never extends, and OIDC logout hands back end_session.
func TestWebSessionRevoke(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 3, 1, 12, 0, 0, 0, time.UTC)
	mem := newFakeStore(func() time.Time { return now })
	srv, ts := newTestServer(t, func(c *Config) {
		c.Sessions = mem
		c.Now = func() time.Time { return now }
	})
	session, _ := signIn(t, srv, ts)
	if got := get(t, ts, "/api/session", session).status; got != http.StatusOK {
		t.Fatalf("signed in = %d", got)
	}
	// `serve sessions revoke --all` / a password change write revoked_at; the
	// very next request must fail.
	mem.mu.Lock()
	for hash := range mem.m {
		mem.m[hash] = Session{IDHash: hash, RevokedAt: now}
	}
	mem.mu.Unlock()
	if got := get(t, ts, "/api/session", session).status; got != http.StatusUnauthorized {
		t.Fatalf("after revoke-all = %d, want 401", got)
	}

	// Absolute cap: 7 days + 1 s is refused even for an active session.
	session2, _ := signIn(t, srv, ts)
	now = now.Add(AbsoluteTimeout + time.Second)
	if got := get(t, ts, "/api/session", session2).status; got != http.StatusUnauthorized {
		t.Fatalf("7d+1s = %d, want 401", got)
	}

	// Idle cap: 12 hours + 1 s.
	session3, _ := signIn(t, srv, ts)
	now = now.Add(IdleTimeout + time.Second)
	if got := get(t, ts, "/api/session", session3).status; got != http.StatusUnauthorized {
		t.Fatalf("12h+1s = %d, want 401", got)
	}
}

// TestWebSessionRevokeOIDC: logout revokes locally and returns the provider's
// end_session URL so the upstream SSO session also ends (GAP-SEC-09).
func TestWebSessionRevokeOIDC(t *testing.T) {
	t.Parallel()
	_, ts := newTestServer(t, func(c *Config) {
		c.Auth = ModeOIDC
		c.OIDC = fakeOIDC{end: "https://idp.test/end?principal="}
	})
	client := ts.Client()
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	session, csrf := oidcSignIn(t, ts, client)
	if got := oidcLogout(t, ts, client, session, csrf); got != "https://idp.test/end?principal=alice" {
		t.Errorf("%s = %q", endSessionHeader, got)
	}
	if got := get(t, ts, "/api/session", session).status; got != http.StatusUnauthorized {
		t.Errorf("after OIDC logout = %d, want 401", got)
	}
}

// httpResult is the completed response the OIDC helpers need; the body is
// already closed.
type httpResult struct {
	status  int
	header  http.Header
	cookies []*http.Cookie
}

// doGet performs one GET with a request context and closes the body before
// returning.
func doGet(t *testing.T, client *http.Client, url string) httpResult {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cerr := resp.Body.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	}()
	return httpResult{status: resp.StatusCode, header: resp.Header, cookies: resp.Cookies()}
}

// oidcSignIn walks authorize → callback and returns the session and CSRF
// cookies minted by the callback.
func oidcSignIn(t *testing.T, ts *httptest.Server, client *http.Client) (*http.Cookie, *http.Cookie) {
	t.Helper()
	location := doGet(t, client, ts.URL+"/auth").header.Get("Location")
	_, state, ok := strings.Cut(location, "state=")
	if !ok || state == "" {
		t.Fatalf("authorize redirect = %q", location)
	}
	callback := doGet(t, client, ts.URL+"/auth/callback?state="+state+"&code=abc")
	var session, csrf *http.Cookie
	for _, c := range callback.cookies {
		switch c.Name {
		case sessionCookie:
			session = c
		case csrfCookie:
			csrf = c
		}
	}
	if callback.status != http.StatusSeeOther || session == nil || csrf == nil {
		t.Fatalf("callback = %d cookies %v", callback.status, callback.cookies)
	}
	return session, csrf
}

// oidcLogout posts the logout and returns the end_session header.
func oidcLogout(t *testing.T, ts *httptest.Server, client *http.Client, session, csrf *http.Cookie) string {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ts.URL+"/api/logout", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.AddCookie(session)
	req.AddCookie(csrf)
	req.Header.Set(csrfHeader, csrf.Value)
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Fatalf("close: %v", cerr)
	}
	return resp.Header.Get(endSessionHeader)
}

var _ OIDCProvider = fakeOIDC{}
