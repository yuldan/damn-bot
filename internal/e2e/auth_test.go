// Package e2e acceptance for F5-03: the real binary logs in against a fake
// authorization server, stores the token off-keychain (DAMNBOT_TEST_KEYRING=
// fail forces the 0600 file backend), revokes on logout and reports refresh
// failures without ever printing a token.
package e2e

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
)

type authAS struct {
	t           *testing.T
	srv         *httptest.Server
	mu          sync.Mutex
	device      bool
	challenge   string
	pending     bool
	refreshDead bool
	revokeFail  bool
	revoked     map[string]bool
	refreshTok  string
	revokes     int
}

func newAuthAS(t *testing.T) *authAS {
	t.Helper()
	a := &authAS{t: t, device: true, revoked: map[string]bool{}}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		md := map[string]any{
			"issuer": a.srv.URL, "token_endpoint": a.srv.URL + "/token",
			"registration_endpoint": a.srv.URL + "/register", "revocation_endpoint": a.srv.URL + "/revoke",
			"code_challenge_methods_supported": []string{"S256"},
		}
		if a.device {
			md["device_authorization_endpoint"] = a.srv.URL + "/device"
		}
		authJSON(w, md)
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		authJSON(w, map[string]any{"client_id": "dcr-client"})
	})
	mux.HandleFunc("/device", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.challenge = r.Form.Get("code_challenge")
		a.mu.Unlock()
		authJSON(w, map[string]any{
			"device_code": "dev-1", "user_code": "WXYZ-1234",
			"verification_uri": a.srv.URL + "/verify", "expires_in": 30, "interval": 1,
		})
	})
	mux.HandleFunc("/token", a.tokenHandler)
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.revokes++
		a.revoked[r.Form.Get("token")] = true
		fail := a.revokeFail
		a.mu.Unlock()
		if fail {
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

func authJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v) //nolint:errcheck // test encoder
}

func pkceOK(verifier, challenge string) bool {
	sum := sha256.Sum256([]byte(verifier))
	return base64.RawURLEncoding.EncodeToString(sum[:]) == challenge
}

// authEnv is a child environment with the file token backend and a private
// data root matching config.Paths on every GOOS (Linux XDG, macOS Application
// Support, Windows LOCALAPPDATA), so the token path is deterministic.
func authEnv(t *testing.T) (*Env, string) {
	t.Helper()
	e := newEnv(t)
	e.bin = binPaths.secret
	switch runtime.GOOS {
	case "windows":
		return e, filepath.Join(e.home, "AppData", "Local")
	case "darwin":
		return e, filepath.Join(e.home, "Library", "Application Support")
	default:
		return e, filepath.Join(e.home, "data")
	}
}

func (a *authAS) exec(e *Env, dataHome string, args ...string) Result {
	env := []string{"DAMNBOT_TEST_KEYRING=fail"}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		env = append(env, "XDG_DATA_HOME="+dataHome)
	}
	return e.ExecEnv(env, args...)
}

func tokenPath(dataHome string) string {
	return filepath.Join(dataHome, "damnbot", "auth", "fake.json")
}

// TestAuthLoginStatusLogoutRevoke is the primary security path: login stores
// a 0600 token in a 0700 dir, status never prints it, logout revokes it and
// removes the file, and the revoked refresh token is dead at the server.
func TestAuthLoginStatusLogoutRevoke(t *testing.T) { //nolint:gocognit,gocyclo // one end-to-end security scenario
	as := newAuthAS(t)
	e, dataHome := authEnv(t)
	login := as.exec(e, dataHome, "setup", "auth", "login", "--issuer", as.srv.URL, "--scope", "read", "fake")
	login.RequireOK(t)
	if !strings.Contains(login.Stdout, "login fake: ok") {
		t.Fatalf("login output = %q", login.Stdout)
	}
	for _, secret := range []string{"access-TOKEN-SECRET", "refresh-TOKEN-SECRET"} {
		if strings.Contains(login.Stdout+login.Stderr, secret) {
			t.Fatalf("login printed %s", secret)
		}
	}
	path := tokenPath(dataHome)
	if runtime.GOOS != "windows" {
		if fi, err := os.Stat(path); err != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file = %v, %v (want 0600)", fi, err)
		}
		if di, err := os.Stat(filepath.Dir(path)); err != nil || di.Mode().Perm() != 0o700 {
			t.Fatalf("token dir = %v, %v (want 0700)", di, err)
		}
	}
	status := as.exec(e, dataHome, "setup", "auth", "status", "--json", "fake")
	status.RequireOK(t)
	var row map[string]any
	if err := json.Unmarshal([]byte(status.Stdout), &row); err != nil || row["state"] != "ok" || row["provider"] != "fake" {
		t.Fatalf("status json = %q (%v)", status.Stdout, err)
	}
	for _, forbidden := range []string{"access-TOKEN-SECRET", "refresh-TOKEN-SECRET", "access_token", "refresh_token"} {
		if strings.Contains(status.Stdout+status.Stderr, forbidden) {
			t.Fatalf("status leaked %q: %s", forbidden, status.Stdout)
		}
	}
	logout := as.exec(e, dataHome, "setup", "auth", "logout", "fake")
	logout.RequireOK(t)
	if !strings.Contains(logout.Stdout, "fake: revoked") {
		t.Fatalf("logout output = %q", logout.Stdout)
	}
	if _, err := os.Stat(path); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("token file survived logout: %v", err)
	}
	as.mu.Lock()
	revokes, refresh := as.revokes, as.refreshTok
	as.mu.Unlock()
	if revokes == 0 || !as.revoked[refresh] {
		t.Fatalf("revocation not observed (calls=%d)", revokes)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {refresh}, "client_id": {"dcr-client"}}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, as.srv.URL+"/token", strings.NewReader(form.Encode()))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := as.srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }() //nolint:errcheck // body fully decoded below
	var body map[string]string
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || body["error"] != "invalid_grant" {
		t.Fatalf("refresh after revoke = %d %v", resp.StatusCode, body)
	}
	assertNoSecretFiles(t, e.home, "access-TOKEN-SECRET", "refresh-TOKEN-SECRET")
}

// TestAuthOfflineFails: offline login and logout exit non-zero with a clear
// message and never fake success. A failed logout keeps the local token.
func TestAuthOfflineFails(t *testing.T) {
	as := newAuthAS(t)
	e, dataHome := authEnv(t)
	off := as.exec(e, dataHome, "setup", "auth", "login", "--offline", "--issuer", as.srv.URL, "fake")
	if off.Code == 0 || !strings.Contains(off.Stderr, "offline") {
		t.Fatalf("offline login = %d %q", off.Code, off.Stderr)
	}
	if _, err := os.Stat(tokenPath(dataHome)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("offline login wrote a token")
	}
	as.exec(e, dataHome, "setup", "auth", "login", "--issuer", as.srv.URL, "fake").RequireOK(t)
	logout := as.exec(e, dataHome, "setup", "auth", "logout", "--offline", "fake")
	if logout.Code == 0 || !strings.Contains(logout.Stdout+logout.Stderr, "failed") ||
		!strings.Contains(logout.Stdout+logout.Stderr, "offline") {
		t.Fatalf("offline logout = %d %q %q", logout.Code, logout.Stdout, logout.Stderr)
	}
	if _, err := os.Stat(tokenPath(dataHome)); err != nil {
		t.Fatalf("offline logout removed the token: %v", err)
	}
}

// TestAuthRefreshInvalidGrantFails: a revoked/rotated grant makes status
// report failed and exit non-zero (EC-PROV-018).
func TestAuthRefreshInvalidGrantFails(t *testing.T) {
	as := newAuthAS(t)
	as.refreshDead = true
	e, dataHome := authEnv(t)
	as.exec(e, dataHome, "setup", "auth", "login", "--issuer", as.srv.URL, "fake").RequireOK(t)
	status := as.exec(e, dataHome, "setup", "auth", "status", "fake")
	if status.Code == 0 || !strings.Contains(status.Stdout, "fake: failed") {
		t.Fatalf("status = %d %q %q", status.Code, status.Stdout, status.Stderr)
	}
	if strings.Contains(status.Stdout+status.Stderr, "TOKEN-SECRET") {
		t.Fatal("status printed a token")
	}
}

// TestAuthRevokeFailureKeepsToken: a server-side revoke failure is reported
// as failed with a non-zero exit and the local token is not deleted.
func TestAuthRevokeFailureKeepsToken(t *testing.T) {
	as := newAuthAS(t)
	e, dataHome := authEnv(t)
	as.exec(e, dataHome, "setup", "auth", "login", "--issuer", as.srv.URL, "fake").RequireOK(t)
	as.mu.Lock()
	as.revokeFail = true
	as.mu.Unlock()
	logout := as.exec(e, dataHome, "setup", "auth", "logout", "fake")
	if logout.Code == 0 || !strings.Contains(logout.Stdout, "fake: failed") {
		t.Fatalf("logout = %d %q", logout.Code, logout.Stdout)
	}
	if _, err := os.Stat(tokenPath(dataHome)); err != nil {
		t.Fatalf("failed revoke deleted the token: %v", err)
	}
}

// TestAuthLoginUnreachable: a dead issuer fails login and writes nothing.
func TestAuthLoginUnreachable(t *testing.T) {
	as := newAuthAS(t)
	e, dataHome := authEnv(t)
	dead := as.srv.URL
	as.srv.Close()
	r := as.exec(e, dataHome, "setup", "auth", "login", "--issuer", dead, "fake")
	if r.Code == 0 || !strings.Contains(r.Stderr, "setup auth login:") {
		t.Fatalf("unreachable login = %d %q %q", r.Code, r.Stdout, r.Stderr)
	}
	if strings.Contains(r.Stdout, "ok") {
		t.Fatalf("unreachable login reported success: %q", r.Stdout)
	}
	if _, err := os.Stat(tokenPath(dataHome)); !errors.Is(err, fs.ErrNotExist) {
		t.Fatal("failed login wrote a token")
	}
}

// TestAuthMissingInputs: no issuer, no stored provider, and an unknown
// subcommand all fail without touching the store.
func TestAuthMissingInputs(t *testing.T) {
	as := newAuthAS(t)
	e, dataHome := authEnv(t)
	if r := as.exec(e, dataHome, "setup", "auth", "login", "fake"); r.Code != 2 || !strings.Contains(r.Stderr, "--issuer") {
		t.Fatalf("login without issuer = %d %q", r.Code, r.Stderr)
	}
	if r := as.exec(e, dataHome, "setup", "auth", "status", "fake"); r.Code == 0 {
		t.Fatalf("status without token = %d", r.Code)
	}
	if r := as.exec(e, dataHome, "setup", "auth", "bogus"); r.Code != 2 {
		t.Fatalf("bogus subcommand = %d", r.Code)
	}
}

// assertNoSecretFiles walks the child's private home and fails when any file
// (log, JSON, DB, config) contains a token value.
func assertNoSecretFiles(t *testing.T, root string, secrets ...string) {
	t.Helper()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		f, oerr := os.Open(path) //nolint:gosec // G304/G122: private test home, no attacker-controlled symlink
		if oerr != nil {
			return fmt.Errorf("open %s: %w", path, oerr)
		}
		defer func() { _ = f.Close() }() //nolint:errcheck // read-only probe
		b, rerr := io.ReadAll(io.LimitReader(f, 1<<20))
		if rerr != nil {
			return fmt.Errorf("read %s: %w", path, rerr)
		}
		for _, secret := range secrets {
			if strings.Contains(string(b), secret) {
				t.Errorf("%s contains %s", path, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk %s: %v", root, err)
	}
}

func (a *authAS) tokenHandler(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, "bad form", http.StatusBadRequest)
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	switch r.Form.Get("grant_type") {
	case "urn:ietf:params:oauth:grant-type:device_code":
		if a.challenge != "" && !pkceOK(r.Form.Get("code_verifier"), a.challenge) {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
		if a.pending {
			a.pending = false
			authJSON(w, map[string]any{"error": "authorization_pending"})
			return
		}
	case "refresh_token":
		if a.refreshDead || a.revoked[r.Form.Get("refresh_token")] {
			http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
			return
		}
	default:
		http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		return
	}
	a.refreshTok = "refresh-TOKEN-SECRET"
	authJSON(w, map[string]any{
		"access_token": "access-TOKEN-SECRET", "refresh_token": a.refreshTok,
		"token_type": "Bearer", "expires_in": 1, "scope": "read",
	})
}
