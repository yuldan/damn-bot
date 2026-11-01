package e2e

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

// mcpAS is a fake authorization server for the MCP OAuth path: device grant
// with PKCE, DCR, refresh rotation, revocation, and an optional dead refresh
// token so `invalid_grant` is observable.
type mcpAS struct {
	srv         *httptest.Server
	mu          sync.Mutex
	challenge   string
	refreshDead bool
	refreshN    int
	revoked     map[string]bool
	access      string
	refreshTok  string
}

func newMCPAS(t *testing.T) *mcpAS {
	t.Helper()
	a := &mcpAS{
		revoked: map[string]bool{}, access: "access-TOKEN-SECRET", refreshTok: "refresh-TOKEN-SECRET",
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/oauth-authorization-server", func(w http.ResponseWriter, _ *http.Request) {
		authJSON(w, map[string]any{
			"issuer": a.srv.URL, "token_endpoint": a.srv.URL + "/token",
			"device_authorization_endpoint": a.srv.URL + "/device",
			"registration_endpoint":         a.srv.URL + "/register",
			"revocation_endpoint":           a.srv.URL + "/revoke",
		})
	})
	mux.HandleFunc("/register", func(w http.ResponseWriter, _ *http.Request) {
		authJSON(w, map[string]any{"client_id": "mcp-client"})
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
			"device_code": "dev-1", "user_code": "MCP-1234",
			"verification_uri": a.srv.URL + "/verify", "expires_in": 30, "interval": 1,
		})
	})
	mux.HandleFunc("/token", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		defer a.mu.Unlock()
		switch r.Form.Get("grant_type") {
		case "urn:ietf:params:oauth:grant-type:device_code":
			if !pkceOK(r.Form.Get("code_verifier"), a.challenge) {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			authJSON(w, map[string]any{
				"access_token": a.access, "refresh_token": a.refreshTok, "token_type": "Bearer",
				"expires_in": 1, "scope": "read",
			})
		case "refresh_token":
			if a.refreshDead || a.revoked[r.Form.Get("refresh_token")] {
				http.Error(w, `{"error":"invalid_grant"}`, http.StatusBadRequest)
				return
			}
			a.refreshN++
			a.access, a.refreshTok = "access-TOKEN-SECRET-2", "refresh-TOKEN-SECRET-2"
			authJSON(w, map[string]any{
				"access_token": a.access, "refresh_token": a.refreshTok, "token_type": "Bearer",
				"expires_in": 3600, "scope": "read",
			})
		default:
			http.Error(w, `{"error":"unsupported_grant_type"}`, http.StatusBadRequest)
		}
	})
	mux.HandleFunc("/revoke", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, "bad form", http.StatusBadRequest)
			return
		}
		a.mu.Lock()
		a.revoked[r.Form.Get("token")] = true
		a.mu.Unlock()
		w.WriteHeader(http.StatusOK)
	})
	a.srv = httptest.NewServer(mux)
	t.Cleanup(a.srv.Close)
	return a
}

// exec runs one child of the OAuth e2e environment: file token backend, a
// private data root, and the secret-tagged binary.
func (a *mcpAS) exec(e *Env, dataHome string, args ...string) Result {
	env := []string{"DAMNBOT_TEST_KEYRING=fail"}
	if runtime.GOOS != "windows" && runtime.GOOS != "darwin" {
		env = append(env, "XDG_DATA_HOME="+dataHome)
	}
	return e.ExecEnv(env, args...)
}

// fakeMCPHTTP is a Streamable HTTP MCP server accepting exactly one bearer.
func fakeMCPHTTP(t *testing.T, want string, seen *[]string) *httptest.Server {
	t.Helper()
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		*seen = append(*seen, r.Header.Get("Authorization"))
		expected := want
		mu.Unlock()
		if r.Header.Get("Authorization") != "Bearer "+expected {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		reply := func(result any) {
			raw, _ := json.Marshal(result)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(msg.ID) + `,"result":` + string(raw) + `}`)) //nolint:errcheck // test writer
		}
		switch msg.Method {
		case "initialize":
			reply(map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}})
		case "notifications/initialized":
			w.WriteHeader(http.StatusAccepted)
		case "tools/list":
			reply(map[string]any{"tools": []map[string]any{{
				"name": "echo", "description": "echo", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false},
			}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestMCPOAuthLoginRefreshRevoke is the security path: login stores a 0600
// token, refresh rotates it after a 401, revoke kills it, and no token ever
// reaches output, config, or status JSON.
func TestMCPOAuthLoginRefreshRevoke(t *testing.T) {
	as := newMCPAS(t)
	want := "access-TOKEN-SECRET-2"
	var seen []string
	mcpSrv := fakeMCPHTTP(t, want, &seen)
	e, dataHome := authEnv(t)
	add := as.exec(e, dataHome, "mcp", "add", "remote", "--url", mcpSrv.URL, "--issuer", as.srv.URL, "--scope", "read")
	add.RequireOK(t)
	login := as.exec(e, dataHome, "mcp", "auth", "login", "remote")
	login.RequireOK(t)
	if !strings.Contains(login.Stdout, "login remote: ok") {
		t.Fatalf("login = %q", login.Stdout)
	}
	refresh := as.exec(e, dataHome, "mcp", "refresh", "remote")
	if refresh.Code != 0 || !strings.Contains(refresh.Stdout, "remote: 1 tools") {
		t.Fatalf("refresh = %d %q %q", refresh.Code, refresh.Stdout, refresh.Stderr)
	}
	as.mu.Lock()
	refreshN := as.refreshN
	as.mu.Unlock()
	if refreshN != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshN)
	}
	if len(seen) < 2 || seen[0] != "Bearer access-TOKEN-SECRET" || seen[1] != "Bearer access-TOKEN-SECRET-2" {
		t.Fatalf("Authorization headers = %q", seen)
	}
	status := as.exec(e, dataHome, "mcp", "auth", "status", "--json", "remote")
	status.RequireOK(t)
	var rows []map[string]any
	if err := json.Unmarshal([]byte(status.Stdout), &rows); err != nil || len(rows) != 1 || rows[0]["state"] != "ok" {
		t.Fatalf("status = %q (%v)", status.Stdout, err)
	}
	logout := as.exec(e, dataHome, "mcp", "auth", "logout", "remote")
	logout.RequireOK(t)
	if !strings.Contains(logout.Stdout, "remote: revoked") {
		t.Fatalf("logout = %q", logout.Stdout)
	}
	as.mu.Lock()
	revoked := as.revoked["refresh-TOKEN-SECRET-2"]
	as.mu.Unlock()
	if !revoked {
		t.Fatal("refresh token was not revoked")
	}
	after := as.exec(e, dataHome, "mcp", "auth", "status", "remote")
	if after.Code == 0 || !strings.Contains(after.Stdout+after.Stderr, "missing") {
		t.Fatalf("status after logout = %d %q %q", after.Code, after.Stdout, after.Stderr)
	}
	for _, out := range []string{add.Stdout, add.Stderr, login.Stdout, login.Stderr, refresh.Stdout, refresh.Stderr, status.Stdout, status.Stderr, logout.Stdout, logout.Stderr, after.Stdout, after.Stderr} {
		for _, secret := range []string{"access-TOKEN-SECRET", "refresh-TOKEN-SECRET"} {
			if strings.Contains(out, secret) {
				t.Fatalf("output leaked %s: %q", secret, out)
			}
		}
		for _, key := range []string{"access_token", "refresh_token"} {
			if strings.Contains(out, key) {
				t.Fatalf("output leaked key %s: %q", key, out)
			}
		}
	}
	cfg, err := os.ReadFile(e.ConfigDir() + "/config.toml") // #nosec G304 -- e2e temp config
	if err == nil && (strings.Contains(string(cfg), "access_token") || strings.Contains(string(cfg), "refresh_token")) {
		t.Fatalf("config holds a token:\n%s", cfg)
	}
	if runtime.GOOS != "windows" {
		path := tokenPathFor(dataHome, "mcp.remote")
		if fi, serr := os.Stat(path); serr != nil || fi.Mode().Perm() != 0o600 {
			t.Fatalf("token file = %v, %v (want 0600)", fi, serr)
		}
	}
}

// TestMCPOAuthInvalidGrant: a dead refresh token is reported as invalid_grant
// and never as success.
func TestMCPOAuthInvalidGrant(t *testing.T) {
	as := newMCPAS(t)
	want := "access-TOKEN-SECRET"
	var seen []string
	mcpSrv := fakeMCPHTTP(t, want, &seen)
	e, dataHome := authEnv(t)
	as.exec(e, dataHome, "mcp", "add", "remote", "--url", mcpSrv.URL, "--issuer", as.srv.URL, "--scope", "read").RequireOK(t)
	as.exec(e, dataHome, "mcp", "auth", "login", "remote").RequireOK(t)
	as.mu.Lock()
	as.refreshDead = true
	as.mu.Unlock()
	time.Sleep(1100 * time.Millisecond) // device token expires_in = 1 s
	status := as.exec(e, dataHome, "mcp", "auth", "status", "remote")
	if status.Code == 0 || !strings.Contains(status.Stdout+status.Stderr, "invalid_grant") {
		t.Fatalf("status = %d %q %q", status.Code, status.Stdout, status.Stderr)
	}
}

// TestMCPOAuthWrongTokenDenied: a server that rejects every token fails the
// refresh closed and prints no credential.
func TestMCPOAuthWrongTokenDenied(t *testing.T) {
	as := newMCPAS(t)
	want := "never-issued"
	var seen []string
	mcpSrv := fakeMCPHTTP(t, want, &seen)
	e, dataHome := authEnv(t)
	as.exec(e, dataHome, "mcp", "add", "remote", "--url", mcpSrv.URL, "--issuer", as.srv.URL, "--scope", "read").RequireOK(t)
	as.exec(e, dataHome, "mcp", "auth", "login", "remote").RequireOK(t)
	refresh := as.exec(e, dataHome, "mcp", "refresh", "remote")
	if refresh.Code == 0 {
		t.Fatalf("refresh succeeded against a rejecting server: %q", refresh.Stdout)
	}
	if strings.Contains(refresh.Stdout+refresh.Stderr, "TOKEN-SECRET") || strings.Contains(refresh.Stdout+refresh.Stderr, "never-issued") {
		t.Fatalf("failure leaked a token: %q %q", refresh.Stdout, refresh.Stderr)
	}
}

func tokenPathFor(dataHome, provider string) string {
	return filepath.Join(dataHome, "damnbot", "auth", provider+".json")
}
