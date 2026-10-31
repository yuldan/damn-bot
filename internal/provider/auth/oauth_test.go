package auth

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"
)

// fakeAS is a loopback authorization server: RFC 8414 metadata, RFC 7591
// registration, RFC 8628 device flow with PKCE, refresh and RFC 7009 revoke.
type fakeAS struct {
	t          *testing.T
	srv        *httptest.Server
	mu         sync.Mutex
	device     bool
	challenge  string
	clientID   string
	registered bool
	pending    bool
	revoked    map[string]bool
	failBody   string
}

func newFakeAS(t *testing.T, device bool) *fakeAS {
	t.Helper()
	f := &fakeAS{t: t, device: device, revoked: map[string]bool{}, pending: true}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		md := map[string]any{
			"issuer": f.srv.URL, "token_endpoint": f.srv.URL + "/token",
			"registration_endpoint": f.srv.URL + "/register", "revocation_endpoint": f.srv.URL + "/revoke",
			"code_challenge_methods_supported": []string{"S256"},
		}
		if f.device {
			md["device_authorization_endpoint"] = f.srv.URL + "/device"
		}
		writeJSON(w, md)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.registered = true
		f.mu.Unlock()
		writeJSON(w, map[string]any{"client_id": "dcr-client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.challenge = r.Form.Get("code_challenge")
		f.clientID = r.Form.Get("client_id")
		f.mu.Unlock()
		if r.Form.Get("code_challenge_method") != "S256" {
			http.Error(w, "pkce required", http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{
			"device_code": "dev-1", "user_code": "ABCD-EFGH",
			"verification_uri": f.srv.URL + "/verify", "expires_in": 60, "interval": 1,
		})
	})
	mux.HandleFunc("/token", f.tokenHandler)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.revoked[r.Form.Get("token")] = true
		f.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	f.srv = httptest.NewServer(mux)
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeAS) validVerifier(v string) bool {
	sum := sha256.Sum256([]byte(v))
	return f.challenge != "" && base64.RawURLEncoding.EncodeToString(sum[:]) == f.challenge
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // test encoder
}

func oauthFor(t *testing.T, f *fakeAS, store *Store, offline bool, out *strings.Builder) *OAuth {
	t.Helper()
	o, err := NewOAuth(&OAuthConfig{
		Provider: "fake", Issuer: f.srv.URL, Store: store, Client: testClient(),
		Now:    func() time.Time { return time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC) },
		Sleep:  func(context.Context, time.Duration) error { return nil },
		Output: out, Offline: offline,
	})
	if err != nil {
		t.Fatal(err)
	}
	return o
}

func fileStore(t *testing.T) *Store {
	t.Helper()
	dir := t.TempDir()
	if err := os.Chmod(dir, 0o700); err != nil { // #nosec G302 -- test dir needs 0700
		t.Fatal(err)
	}
	st, nerr := NewStore(nil, dir)
	if nerr != nil {
		t.Fatal(nerr)
	}
	return st
}

// TestOAuthDeviceLoginDCRPKCE: discovery, DCR, PKCE on device+poll, storage.
func TestOAuthDeviceLoginDCRPKCE(t *testing.T) {
	f := newFakeAS(t, true)
	store := fileStore(t)
	var out strings.Builder
	tok, err := oauthFor(t, f, store, false, &out).Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if tok.Provider != "fake" || tok.AccessToken != "access-SECRET" || tok.RefreshToken != "refresh-SECRET" {
		t.Fatalf("token = %+v", tok)
	}
	if !f.registered || f.clientID != "dcr-client" || f.challenge == "" {
		t.Fatalf("DCR registered=%t client=%q challenge=%q", f.registered, f.clientID, f.challenge)
	}
	if strings.Contains(out.String(), "access-SECRET") || strings.Contains(out.String(), "refresh-SECRET") {
		t.Fatalf("instructions leaked a token: %q", out.String())
	}
	if !strings.Contains(out.String(), "ABCD-EFGH") {
		t.Fatalf("user code missing: %q", out.String())
	}
	if _, err := store.Load(context.Background(), "fake"); err != nil {
		t.Fatalf("token not stored: %v", err)
	}
}

// TestOAuthRefreshRevocation: refresh an expired token, revoke both tokens,
// then invalid_grant.
func TestOAuthRefreshRevocation(t *testing.T) {
	f := newFakeAS(t, true)
	store := fileStore(t)
	o := oauthFor(t, f, store, false, &strings.Builder{})
	tok, err := o.Login(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got, gerr := store.Load(context.Background(), "fake")
	if gerr != nil || !got.Expired(o.cfg.Now()) {
		t.Fatalf("stored token = %+v %v", got, gerr)
	}
	if rerr := o.Refresh(context.Background(), &got); rerr != nil {
		t.Fatal(rerr)
	}
	if verr := o.Revoke(context.Background(), &tok); verr != nil {
		t.Fatal(verr)
	}
	if !f.revoked["refresh-SECRET"] || !f.revoked["access-SECRET"] {
		t.Fatalf("revoked = %v", f.revoked)
	}
	stored, serr := store.Load(context.Background(), "fake")
	if serr != nil {
		t.Fatal(serr)
	}
	if rerr := o.Refresh(context.Background(), &stored); !errors.Is(rerr, ErrInvalidGrant) {
		t.Fatalf("refresh after revoke = %v", rerr)
	}
}

// TestOAuthOffline: every network step refuses offline, no false success.
func TestOAuthOffline(t *testing.T) {
	f := newFakeAS(t, true)
	store := fileStore(t)
	offlineTok := sampleToken("fake")
	if err := store.Save(context.Background(), &offlineTok); err != nil {
		t.Fatal(err)
	}
	o := oauthFor(t, f, store, true, &strings.Builder{})
	if _, err := o.Login(context.Background()); !errors.Is(err, ErrOffline) {
		t.Fatalf("login offline = %v", err)
	}
	tok, terr := store.Load(context.Background(), "fake")
	if terr != nil {
		t.Fatal(terr)
	}
	if err := o.Revoke(context.Background(), &tok); !errors.Is(err, ErrOffline) {
		t.Fatalf("revoke offline = %v", err)
	}
	if err := o.Refresh(context.Background(), &tok); !errors.Is(err, ErrOffline) {
		t.Fatalf("refresh offline = %v", err)
	}
}

// TestOAuthNoDeviceEndpoint: no device flow is refused, not downgraded.
func TestOAuthNoDeviceEndpoint(t *testing.T) {
	f := newFakeAS(t, false)
	o := oauthFor(t, f, fileStore(t), false, &strings.Builder{})
	if _, err := o.Login(context.Background()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("login without device endpoint = %v", err)
	}
}

// TestOAuthPKCE pins the RFC 7636 relation.
func TestOAuthPKCE(t *testing.T) {
	t.Parallel()
	v, c, err := PKCE()
	if err != nil {
		t.Fatal(err)
	}
	if len(v) != 43 {
		t.Fatalf("verifier length %d", len(v))
	}
	sum := sha256.Sum256([]byte(v))
	if c != base64.RawURLEncoding.EncodeToString(sum[:]) {
		t.Fatal("challenge is not S256(verifier)")
	}
}

// TestOAuthDiscoveryNegative: plaintext remote issuer and issuer mismatch.
func TestOAuthDiscoveryNegative(t *testing.T) {
	t.Parallel()
	if _, err := Discover(context.Background(), testClient(), "http://example.com"); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("plaintext issuer = %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, map[string]any{"issuer": "https://other.example", "token_endpoint": "https://other.example/token"}) //nolint:gosec // metadata fixture
	}))
	defer srv.Close()
	if _, err := Discover(context.Background(), testClient(), srv.URL); !errors.Is(err, ErrEndpoint) {
		t.Fatalf("issuer mismatch = %v", err)
	}
}

// TestOAuthErrorDoesNotLeakBody: a server error body is never echoed.
func TestOAuthErrorDoesNotLeakBody(t *testing.T) {
	f := newFakeAS(t, true)
	f.mu.Lock()
	f.failBody = `{"error":"server_error","detail":"refresh-SECRET"}`
	f.mu.Unlock()
	store := fileStore(t)
	if err := store.Save(context.Background(), &Token{
		Provider: "fake", AccessToken: "a", RefreshToken: "refresh-SECRET",
		TokenEndpoint: f.srv.URL + "/token", ClientID: "c",
	}); err != nil {
		t.Fatal(err)
	}
	tok, lerr := store.Load(context.Background(), "fake")
	if lerr != nil {
		t.Fatal(lerr)
	}
	err := oauthFor(t, f, store, false, &strings.Builder{}).Refresh(context.Background(), &tok)
	if err == nil || strings.Contains(err.Error(), "SECRET") || strings.Contains(err.Error(), "detail") {
		t.Fatalf("error leaked body: %v", err)
	}
}

func (f *fakeAS) tokenHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failBody != "" {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(f.failBody)) //nolint:errcheck // test response
		return
	}
	switch r.Form.Get("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		if r.Form.Get("device_code") != "dev-1" || !f.validVerifier(r.Form.Get("code_verifier")) {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		if f.pending {
			f.pending = false
			writeJSON(w, map[string]any{"error": "authorization_pending"})
			return
		}
	case "refresh_token":
		if f.revoked[r.Form.Get("refresh_token")] {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{
		"access_token": "access-SECRET", "refresh_token": "refresh-SECRET",
		"token_type": "Bearer", "expires_in": 1, "scope": "read",
	})
}

// TestOAuthSign covers the Route.Auth path: a stored, fresh token becomes the
// bearer header without a network call.
func TestOAuthSign(t *testing.T) {
	f := newFakeAS(t, true)
	store := fileStore(t)
	future := sampleToken("fake")
	future.Expiry = time.Date(2026, 10, 31, 12, 0, 0, 0, time.UTC).Add(time.Hour)
	if err := store.Save(context.Background(), &future); err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, "https://example.invalid/v1", nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := oauthFor(t, f, store, false, &strings.Builder{}).Sign(context.Background(), req, nil); err != nil {
		t.Fatal(err)
	}
	if got := req.Header.Get("Authorization"); got != "Bearer access-SECRET" {
		t.Fatalf("Authorization = %q", got)
	}
}
