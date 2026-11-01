package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
	"github.com/damnbot/damnbot/internal/netx"
)

type fakeTokens struct {
	mu        sync.Mutex
	access    string
	refreshed string
	refresh   int
	fail      error
}

func (f *fakeTokens) AccessToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.access, nil
}

func (f *fakeTokens) RefreshToken(context.Context) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.refresh++
	if f.fail != nil {
		return "", f.fail
	}
	f.access = f.refreshed
	return f.access, nil
}

func oauthServer(t *testing.T, want string, seen *[]string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if seen != nil {
			*seen = append(*seen, auth)
		}
		if auth != "Bearer "+want {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg jsonrpcMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		switch msg.Method {
		case methodInitialize:
			writeOAuthJSON(w, msg.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}})
		case notifyInitialized:
			w.WriteHeader(http.StatusAccepted)
		case methodListTools:
			writeOAuthJSON(w, msg.ID, listResult{Tools: []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// jsonrpcMessage is the response id the fake needs; the transport encodes the
// request with jsonrpc.Message, but only these two fields matter here.
type jsonrpcMessage struct {
	ID     json.RawMessage `json:"id"`
	Method string          `json:"method"`
}

func writeOAuthJSON(w http.ResponseWriter, id json.RawMessage, result any) {
	raw, err := json.Marshal(result)
	if err != nil {
		http.Error(w, "encode", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":` + string(id) + `,"result":` + string(raw) + `}`)) //nolint:errcheck // test encoder
}

func TestOAuthRefreshOn401AndBearerHeader(t *testing.T) {
	var seen []string
	var mu sync.Mutex
	record := func(w http.ResponseWriter, r *http.Request) bool {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, r.Header.Get("Authorization"))
		return r.Header.Get("Authorization") == "Bearer fresh"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !record(w, r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		var msg jsonrpcMessage
		if err := json.NewDecoder(r.Body).Decode(&msg); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		switch msg.Method {
		case methodInitialize:
			writeOAuthJSON(w, msg.ID, map[string]any{"protocolVersion": ProtocolVersion, "capabilities": map[string]any{}})
		case notifyInitialized:
			w.WriteHeader(http.StatusAccepted)
		case methodListTools:
			writeOAuthJSON(w, msg.ID, listResult{Tools: []Tool{{Name: "echo", InputSchema: json.RawMessage(`{"type":"object"}`)}}})
		}
	}))
	t.Cleanup(srv.Close)

	tokens := &fakeTokens{access: "stale", refreshed: "fresh"}
	c := newOAuthClient(t, srv.URL, tokens)
	tools, err := c.ListTools(context.Background())
	if err != nil || len(tools) != 1 {
		t.Fatalf("ListTools = %d tools, %v", len(tools), err)
	}
	tokens.mu.Lock()
	refreshes := tokens.refresh
	tokens.mu.Unlock()
	if refreshes != 1 {
		t.Fatalf("refresh calls = %d, want 1", refreshes)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(seen) < 2 || seen[0] != "Bearer stale" || seen[1] != "Bearer fresh" {
		t.Fatalf("Authorization headers = %q", seen)
	}
}

func TestOAuthRefreshFailureIsCredentialDenial(t *testing.T) {
	srv := oauthServer(t, "fresh", nil)
	tokens := &fakeTokens{access: "stale", fail: errors.New("invalid_grant")}
	c := newOAuthClient(t, srv.URL, tokens)
	_, err := c.ListTools(context.Background())
	var te *host.Error
	if !errors.As(err, &te) || te.Code != "denied" {
		t.Fatalf("ListTools error = %v, want denied tool error", err)
	}
	for _, text := range []string{"stale", "fresh", "invalid_grant", "Bearer"} {
		if strings.Contains(err.Error(), text) {
			t.Fatalf("error leaked %q: %v", text, err)
		}
	}
}

func TestClientRequiresExactlyOneTokenSource(t *testing.T) {
	h := &fakeHost{store: newStore()}
	srv := Server{Name: "remote", URL: "https://mcp.example.test/v1", OAuth: &OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}}
	if _, err := NewClient(h, srv, nil, newPins()); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewClient without token source = %v, want ErrInvalidConfig", err)
	}
	plain := Server{Name: "remote", URL: "https://mcp.example.test/v1"}
	if _, err := NewClient(h, plain, nil, newPins(), WithTokenSource(&fakeTokens{})); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("NewClient with stray token source = %v, want ErrInvalidConfig", err)
	}
}

func TestOAuthSectionValidation(t *testing.T) {
	good := []*OAuth{
		{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}},
		{Issuer: "http://127.0.0.1:9999/as", ClientID: "dcr-1", Scopes: []string{"mcp.read", "mcp.tools"}},
	}
	for i, o := range good {
		s := Server{Name: "remote", URL: "https://mcp.example.test/v1", OAuth: o}
		s.Normalize()
		if err := s.Validate(); err != nil {
			t.Fatalf("good[%d] rejected: %v", i, err)
		}
	}
	bad := map[string]*OAuth{
		"plaintext_issuer":  {Issuer: "http://as.example.test", Scopes: []string{"mcp.read"}},
		"no_scopes":         {Issuer: "https://as.example.test"},
		"wildcard":          {Issuer: "https://as.example.test", Scopes: []string{"*"}},
		"duplicate":         {Issuer: "https://as.example.test", Scopes: []string{"mcp.read", "mcp.read"}},
		"too_many":          {Issuer: "https://as.example.test", Scopes: []string{"a", "b", "c", "d", "e", "f", "g", "h", "i", "j", "k", "l", "m", "n", "o", "p", "q"}},
		"bad_scope":         {Issuer: "https://as.example.test", Scopes: []string{"mcp read"}},
		"long_client":       {Issuer: "https://as.example.test", ClientID: strings.Repeat("x", 300), Scopes: []string{"mcp.read"}},
		"issuer_with_query": {Issuer: "https://as.example.test/?x=1", Scopes: []string{"mcp.read"}},
	}
	for name, o := range bad {
		s := Server{Name: "remote", URL: "https://mcp.example.test/v1", OAuth: o}
		s.Normalize()
		if err := s.Validate(); !errors.Is(err, ErrInvalidConfig) {
			t.Fatalf("%s = %v, want ErrInvalidConfig", name, err)
		}
	}
	stdio := Server{Name: "local", Command: []string{"srv"}, OAuth: &OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}}
	stdio.Normalize()
	if err := stdio.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("stdio oauth = %v, want ErrInvalidConfig", err)
	}
	header := Server{Name: "remote", URL: "https://mcp.example.test/v1", Headers: map[string]string{"Authorization": "keychain:tok"},
		OAuth: &OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}}
	header.Normalize()
	if err := header.Validate(); !errors.Is(err, ErrInvalidConfig) {
		t.Fatalf("header+oauth = %v, want ErrInvalidConfig", err)
	}
}

func TestOAuthCheckScopeIsMinimal(t *testing.T) {
	o := &OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}
	if err := o.CheckScope("mcp.read"); err != nil {
		t.Fatalf("exact scope rejected: %v", err)
	}
	if err := o.CheckScope(""); err != nil {
		t.Fatalf("empty grant treated as over-grant: %v", err)
	}
	if err := o.CheckScope("mcp.read mcp.admin"); !errors.Is(err, ErrScope) {
		t.Fatalf("over-grant = %v, want ErrScope", err)
	}
}

func newOAuthClient(t *testing.T, url string, tokens TokenSource) *Client {
	t.Helper()
	h := &fakeHost{store: newStore()}
	cfg := Server{Name: "remote", URL: url, TimeoutS: 10, OAuth: &OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}}
	doer := netx.New(netx.Config{}).Client("mcp-oauth-test", netx.Inference)
	c, err := NewClient(h, cfg, doer, newPins(), WithTokenSource(tokens))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	t.Cleanup(func() {
		if cerr := c.Close(); cerr != nil {
			t.Errorf("close: %v", cerr)
		}
	})
	return c
}
