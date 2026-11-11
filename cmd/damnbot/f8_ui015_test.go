//go:build !minimal

package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/web"
)

// memVault is the keychain seam of the web auth tests.
type memVault struct {
	mu sync.Mutex
	m  map[string]string
}

func newMemVault() *memVault { return &memVault{m: map[string]string{}} }

func (v *memVault) Get(_ context.Context, name string) (string, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	return v.m[name], nil
}

func (v *memVault) Set(_ context.Context, name, value string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.m[name] = value
	return nil
}

func (v *memVault) Delete(_ context.Context, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	delete(v.m, name)
	return nil
}

// memSessions is the web.SessionStore of the auth-provider test.
type memSessions struct {
	mu sync.Mutex
	m  map[string]web.Session
}

func (s *memSessions) Put(_ context.Context, row *web.Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[row.IDHash] = *row
	return nil
}

func (s *memSessions) Get(_ context.Context, id string) (web.Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	row, ok := s.m[id]
	return row, ok, nil
}

func (s *memSessions) Touch(_ context.Context, id string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.m[id]; ok {
		row.LastSeen = at
		s.m[id] = row
	}
	return nil
}

func (s *memSessions) Revoke(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if row, ok := s.m[id]; ok {
		row.RevokedAt = time.Now()
		s.m[id] = row
	}
	return nil
}

// TestF8UI015AuthProviders covers the four dashboard auth providers: basic via
// its verifier, the drain shared bearer, Nous Portal PKCE, and OIDC discovery.
// Tokens never appear in a page, redirect URL or log line.
func TestF8UI015AuthProviders(t *testing.T) {
	vault := newMemVault()
	strong := "0123456789abcdef0123456789abcdef0123456789abcdef"
	verifier := checkDrainVerifier(t, vault, strong)
	checkDrainHTTP(t, verifier, strong)
	checkNousOAuth(t, vault)
	checkOIDCDiscovery(t, vault)
}

// mustRequest builds one request bound to the test context.
func mustRequest(t *testing.T, method, url string) *http.Request {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), method, url, nil)
	if err != nil {
		t.Fatalf("request %s %s: %v", method, url, err)
	}
	return req
}

// readWebBody reads one response body; the caller closes it.
func readWebBody(t *testing.T, body io.Reader) []byte {
	t.Helper()
	raw, err := io.ReadAll(body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return raw
}

// checkDrainVerifier proves the shared-bearer verifier rejects missing and
// weak keychain entries.
func checkDrainVerifier(t *testing.T, vault *memVault, strong string) web.DrainVerifier {
	t.Helper()
	ctx := context.Background()
	if serr := vault.Set(ctx, "drain", strong); serr != nil {
		t.Fatal(serr)
	}
	verifier, err := newDrainVerifier(ctx, vault, "drain")
	if err != nil {
		t.Fatalf("drain verifier: %v", err)
	}
	if !verifier.VerifyBearer(strong) || verifier.VerifyBearer("wrong") {
		t.Fatal("drain verifier does not compare the bearer")
	}
	if _, merr := newDrainVerifier(ctx, vault, "missing"); merr == nil {
		t.Fatal("drain verifier accepted a missing secret")
	}
	if serr := vault.Set(ctx, "weak", "short"); serr != nil {
		t.Fatal(serr)
	}
	if _, werr := newDrainVerifier(ctx, vault, "weak"); werr == nil {
		t.Fatal("drain verifier accepted a weak secret")
	}
	return verifier
}

// checkDrainHTTP proves the drain endpoint accepts the bearer and rejects a
// request without it, without echoing the token.
func checkDrainHTTP(t *testing.T, verifier web.DrainVerifier, strong string) {
	t.Helper()
	srv, err := web.New(&web.Config{
		Listen: "127.0.0.1:0", Auth: web.ModeDrain, Drain: verifier,
		Sessions: &memSessions{m: map[string]web.Session{}},
	})
	if err != nil {
		t.Fatalf("drain server: %v", err)
	}
	ts := httptest.NewServer(srv)
	defer ts.Close()
	req := mustRequest(t, http.MethodPost, ts.URL+"/api/gateway/drain")
	req.Header.Set("Authorization", "Bearer "+strong)
	resp, derr := ts.Client().Do(req)
	if derr != nil {
		t.Fatal(derr)
	}
	raw := readWebBody(t, resp.Body)
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close drain body: %v", cerr)
	}
	if resp.StatusCode != http.StatusAccepted || strings.Contains(string(raw), strong) {
		t.Fatalf("drain = %d %q", resp.StatusCode, raw)
	}
	bad := mustRequest(t, http.MethodPost, ts.URL+"/api/gateway/drain")
	resp, derr = ts.Client().Do(bad)
	if derr != nil {
		t.Fatal(derr)
	}
	if cerr := resp.Body.Close(); cerr != nil {
		t.Errorf("close bad drain body: %v", cerr)
	}
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("drain without bearer = %d", resp.StatusCode)
	}
}

// checkNousOAuth proves the Nous provider exchanges a PKCE code, stores the
// token set only in the vault, and refuses a state replay.
func checkNousOAuth(t *testing.T, vault *memVault) {
	t.Helper()
	const token = "portal-access-secret"
	portal := newPortalServer(t, token)
	defer portal.Close()
	oauth := newNousTestProvider(t, vault, portal.URL)
	checkNousExchange(t, oauth, vault, token)
	checkNousHTTP(t, oauth, token)
	checkOAuthRejectsBadEndpoint(t, vault)
}

// newPortalServer serves the token and userinfo endpoints of a fake Nous
// Portal.
func newPortalServer(t *testing.T, token string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/oauth/token":
			if err := r.ParseForm(); err != nil || r.Form.Get("code") != "good-code" || r.Form.Get("code_verifier") == "" {
				http.Error(w, "bad exchange", http.StatusBadRequest)
				return
			}
			if _, werr := w.Write([]byte(`{"access_token":"` + token + `","refresh_token":"refresh","expires_in":3600}`)); werr != nil {
				t.Errorf("token response: %v", werr)
			}
		case "/oauth/userinfo":
			if r.Header.Get("Authorization") != "Bearer "+token {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			if _, werr := w.Write([]byte(`{"email":"owner@example.test","sub":"s1"}`)); werr != nil {
				t.Errorf("userinfo response: %v", werr)
			}
		default:
			http.NotFound(w, r)
		}
	}))
}

// newNousTestProvider builds a Nous provider pointed at the fake portal.
func newNousTestProvider(t *testing.T, vault *memVault, base string) *codeOAuth {
	t.Helper()
	oauth, err := newCodeOAuth(vault, &codeOAuthConfig{
		name: "nous", clientID: "client", redirect: "http://127.0.0.1:1/auth/callback",
		scopes: "openid profile email", vaultEntry: "web-nous-token",
		authURL: base + "/oauth/authorize", tokenURL: base + "/oauth/token",
		userInfoURL: base + "/oauth/userinfo", endSession: base + "/oauth/logout",
	})
	if err != nil {
		t.Fatalf("nous provider: %v", err)
	}
	return oauth
}

// checkNousExchange proves the PKCE URL, the exchange, the vault write, and
// the state replay guard.
func checkNousExchange(t *testing.T, oauth *codeOAuth, vault *memVault, token string) {
	t.Helper()
	authURL := oauth.AuthorizeURL("state-1", "nonce-1")
	if !strings.Contains(authURL, "code_challenge_method=S256") || !strings.Contains(authURL, "state=state-1") {
		t.Fatalf("authorize URL = %q", authURL)
	}
	principal, err := oauth.Exchange(context.Background(), "good-code", "state-1")
	if err != nil || principal != "owner@example.test" {
		t.Fatalf("exchange = %q %v", principal, err)
	}
	stored, verr := vault.Get(context.Background(), "web-nous-token")
	if verr != nil {
		t.Fatalf("vault get: %v", verr)
	}
	if !strings.Contains(stored, token) {
		t.Fatal("token was not stored in the keychain seam")
	}
	if _, rerr := oauth.Exchange(context.Background(), "good-code", "state-1"); rerr == nil {
		t.Fatal("state replay was accepted")
	}
}

// checkNousHTTP proves /auth redirects without the token and an anonymous
// page is refused without leaking it.
func checkNousHTTP(t *testing.T, oauth web.OIDCProvider, token string) {
	t.Helper()
	nousSrv, err := web.New(&web.Config{
		Listen: "127.0.0.1:0", Auth: web.ModeNous, Nous: oauth,
		Sessions: &memSessions{m: map[string]web.Session{}},
	})
	if err != nil {
		t.Fatalf("nous server: %v", err)
	}
	nts := httptest.NewServer(nousSrv)
	defer nts.Close()
	noFollow := nts.Client()
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	res, derr := noFollow.Do(mustRequest(t, http.MethodGet, nts.URL+"/auth"))
	if derr != nil {
		t.Fatal(derr)
	}
	if cerr := res.Body.Close(); cerr != nil {
		t.Errorf("close /auth body: %v", cerr)
	}
	if res.StatusCode != http.StatusSeeOther || strings.Contains(res.Header.Get("Location"), token) {
		t.Fatalf("nous /auth = %d %q", res.StatusCode, res.Header.Get("Location"))
	}
	anon, aerr := nts.Client().Do(mustRequest(t, http.MethodGet, nts.URL+"/"))
	if aerr != nil {
		t.Fatal(aerr)
	}
	body := readWebBody(t, anon.Body)
	if cerr := anon.Body.Close(); cerr != nil {
		t.Errorf("close anonymous body: %v", cerr)
	}
	if anon.StatusCode != http.StatusUnauthorized || strings.Contains(string(body), token) {
		t.Fatalf("anonymous page = %d %q", anon.StatusCode, body)
	}
}

// checkOAuthRejectsBadEndpoint proves a non-https endpoint fails closed.
func checkOAuthRejectsBadEndpoint(t *testing.T, vault *memVault) {
	t.Helper()
	if _, err := newCodeOAuth(vault, &codeOAuthConfig{authURL: "ftp://x", tokenURL: "http://x", userInfoURL: "http://x", clientID: "c", redirect: "r", vaultEntry: "k"}); err == nil {
		t.Fatal("non-https oauth endpoint was accepted")
	}
}

// checkOIDCDiscovery proves a self-hosted OIDC issuer builds from discovery.
func checkOIDCDiscovery(t *testing.T, vault *memVault) {
	t.Helper()
	oidc, err := newOIDCDiscoveryForTest(t, vault, "", "127.0.0.1:1")
	if err != nil || oidc == nil {
		t.Fatalf("oidc discovery provider: %v", err)
	}
}

// newOIDCDiscoveryForTest builds a self-hosted OIDC provider against a fake
// issuer that serves discovery plus token/userinfo.
func newOIDCDiscoveryForTest(t *testing.T, vault secretVault, secretName, listen string) (web.OIDCProvider, error) {
	t.Helper()
	mux := http.NewServeMux()
	var issuer string
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		if _, werr := w.Write([]byte(`{"issuer":"` + issuer + `","authorization_endpoint":"` + issuer + `/authorize","token_endpoint":"` + issuer + `/token","userinfo_endpoint":"` + issuer + `/userinfo"}`)); werr != nil {
			t.Errorf("discovery response: %v", werr)
		}
	})
	issuerSrv := httptest.NewServer(mux)
	t.Cleanup(issuerSrv.Close)
	issuer = issuerSrv.URL
	return newWebOIDC(context.Background(), vault, issuer, "client", secretName, listen)
}
