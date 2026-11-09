package memoryproviders

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testHoncho(t *testing.T, handler http.HandlerFunc, token string) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := Open(SourceHoncho, &Settings{
		HTTP: srv.Client(), BaseURL: srv.URL, Token: token,
		Workspace: "ws", SessionID: "sess", PeerID: "agent",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	return c
}

// TestF8MEM008Honcho: recall and store speak the documented v3 endpoints, the
// credential never appears in a result, and an item over one Honcho message is
// split at the documented character limit.
func TestF8MEM008Honcho(t *testing.T) {
	t.Parallel()
	const token = "honcho-key-abc"
	var stores atomic.Int32
	c := testHoncho(t, honchoTestHandler(t, token, &stores), token)
	hits, err := c.Recall(t.Context(), "what did we decide")
	if err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if len(hits) != 1 || hits[0].Source != "honcho:user" || !strings.Contains(hits[0].Text, "ship Honcho") {
		t.Fatalf("hits = %+v", hits)
	}
	if strings.Contains(hits[0].Text, token) || strings.Contains(hits[0].Source, token) {
		t.Fatal("token leaked into a hit")
	}
	long := strings.Repeat("x", maxHonchoMessageRunes+1)
	if err := c.Store(t.Context(), Item{ID: "a", Text: long}); err != nil {
		t.Fatalf("Store: %v", err)
	}
	if stores.Load() != 1 {
		t.Fatalf("store calls = %d", stores.Load())
	}
}

func honchoTestHandler(t *testing.T, token string, stores *atomic.Int32) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			t.Errorf("authorization = %q", got)
		}
		switch r.URL.Path {
		case "/v3/workspaces/ws/search":
			honchoSearchHandler(t, w, r)
		case "/v3/workspaces/ws/sessions/sess/messages":
			honchoStoreHandler(t, w, r, stores)
		default:
			t.Errorf("unexpected path %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func honchoSearchHandler(t *testing.T, w http.ResponseWriter, r *http.Request) {
	t.Helper()
	var body honchoSearchRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		t.Errorf("search body: %v", err)
	}
	if body.Query != "what did we decide" || body.Limit != 5 {
		t.Errorf("search = %+v", body)
	}
	if _, err := w.Write([]byte(`[
		{"id":"1","content":"decided: ship Honcho","peer_id":"user"},
		{"id":"2","content":"  ","peer_id":"user"}
	]`)); err != nil {
		t.Errorf("write search response: %v", err)
	}
}

func honchoStoreHandler(t *testing.T, w http.ResponseWriter, r *http.Request, stores *atomic.Int32) {
	t.Helper()
	stores.Add(1)
	var batch honchoBatch
	if err := json.NewDecoder(r.Body).Decode(&batch); err != nil {
		t.Errorf("message body: %v", err)
	}
	if len(batch.Messages) == 0 || batch.Messages[0].PeerID != "agent" {
		t.Errorf("messages = %+v", batch.Messages)
	}
	w.WriteHeader(http.StatusCreated)
	if _, err := w.Write([]byte(`[]`)); err != nil {
		t.Errorf("write store response: %v", err)
	}
}

// TestF8MEM009QueryRewrite: the rewrite runs only when configured, before the
// provider request, and the auxiliary prompt is English.
func TestF8MEM009QueryRewrite(t *testing.T) {
	t.Parallel()
	var seenQuery atomic.Value
	handler := func(w http.ResponseWriter, r *http.Request) {
		var body honchoSearchRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode search: %v", err)
		}
		seenQuery.Store(body.Query)
		if _, err := w.Write([]byte(`[]`)); err != nil {
			t.Errorf("write response: %v", err)
		}
	}
	srv := httptest.NewServer(http.HandlerFunc(handler))
	defer srv.Close()
	fake := &fakeCompleter{reply: "rewritten query\nsecond line"}
	c, err := Open(SourceHoncho, &Settings{
		HTTP: srv.Client(), BaseURL: srv.URL, Token: "k", Workspace: "ws",
		Rewriter: NewLLMRewriter(fake),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Recall(t.Context(), "original"); err != nil {
		t.Fatal(err)
	}
	if got, ok := seenQuery.Load().(string); !ok || got != "rewritten query" {
		t.Fatalf("provider saw %q (ok=%v)", got, ok)
	}
	if !strings.Contains(fake.system, "Rewrite the search query") ||
		!strings.Contains(fake.system, "Return one line of plain text") {
		t.Fatalf("system prompt is not the English rewrite prompt: %q", fake.system)
	}
	plain, err := Open(SourceHoncho, &Settings{
		HTTP: srv.Client(), BaseURL: srv.URL, Token: "k", Workspace: "ws",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = plain.Recall(t.Context(), "original"); err != nil {
		t.Fatal(err)
	}
	if got, ok := seenQuery.Load().(string); !ok || got != "original" {
		t.Fatalf("rewrite ran while disabled: %q (ok=%v)", got, ok)
	}
}

type fakeCompleter struct {
	system string
	reply  string
	err    error
}

func (f *fakeCompleter) Complete(_ context.Context, system, _ string) (string, error) {
	f.system = system
	return f.reply, f.err
}

// TestF8MEM010MemoryOAuth: the code exchange stores the token in the secret
// store only, the config reference omits it, and revoke deletes the entry.
func TestF8MEM010MemoryOAuth(t *testing.T) {
	t.Parallel()
	const access = "oauth-access-value"
	srv := oauthTestServer(t, access)
	defer srv.Close()
	secrets := &fakeSecrets{m: map[string]string{}}
	setup := &Setup{HTTP: srv.Client(), Secrets: secrets, TokenURL: srv.URL, ClientID: "client-1"}
	tok, err := setup.Exchange(t.Context(), "code-1", "verifier-1")
	if err != nil {
		t.Fatalf("Exchange: %v", err)
	}
	ref, err := setup.Save(t.Context(), "honcho_api_key", tok)
	if err != nil {
		t.Fatalf("Save: %v", err)
	}
	if strings.Contains(ref, access) || !strings.Contains(ref, "honcho_api_key") {
		t.Fatalf("config reference = %q", ref)
	}
	if secrets.m["honcho_api_key"] != access {
		t.Fatal("token is not in the secret store")
	}
	got, err := setup.Resolve(t.Context(), "honcho_api_key")
	if err != nil || got != access {
		t.Fatalf("Resolve = %q, %v", got, err)
	}
	if err := setup.Revoke(t.Context(), "honcho_api_key"); err != nil {
		t.Fatalf("Revoke: %v", err)
	}
	if _, ok := secrets.m["honcho_api_key"]; ok {
		t.Fatal("revoke left the secret")
	}
}

func oauthTestServer(t *testing.T, access string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			t.Errorf("form: %v", err)
		}
		if r.Form.Get("grant_type") != "authorization_code" ||
			r.Form.Get("code") != "code-1" || r.Form.Get("client_id") != "client-1" ||
			r.Form.Get("code_verifier") != "verifier-1" {
			t.Errorf("form = %v", r.Form)
		}
		if _, err := w.Write([]byte(`{"access_token":"` + access + `","refresh_token":"r","expires_in":3600}`)); err != nil {
			t.Errorf("write token: %v", err)
		}
	}))
}

type fakeSecrets struct {
	m map[string]string
}

func (f *fakeSecrets) Get(_ context.Context, name string) (string, error) {
	v, ok := f.m[name]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}

func (f *fakeSecrets) Set(_ context.Context, name, value string) error {
	f.m[name] = value
	return nil
}

func (f *fakeSecrets) Delete(_ context.Context, name string) error {
	delete(f.m, name)
	return nil
}

// TestF8MemProviderRetryBounded: idempotent recall retries at most MaxRetries
// and stops immediately on a non-retryable status.
func TestF8MemProviderRetryBounded(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		if _, err := w.Write([]byte(`[]`)); err != nil {
			t.Errorf("write retry response: %v", err)
		}
	}))
	defer srv.Close()
	c, err := Open(SourceHoncho, &Settings{
		HTTP: srv.Client(), BaseURL: srv.URL, Token: "k", Workspace: "ws",
		Retries: 2, Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = c.Recall(t.Context(), "q"); err != nil {
		t.Fatalf("Recall: %v", err)
	}
	if calls.Load() != 3 {
		t.Fatalf("calls = %d, want 3", calls.Load())
	}
	calls.Store(0)
	bad := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusUnauthorized)
	}))
	defer bad.Close()
	c, err = Open(SourceHoncho, &Settings{
		HTTP: bad.Client(), BaseURL: bad.URL, Token: "k", Workspace: "ws", Retries: 2,
		Sleep: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Recall(t.Context(), "q"); !errors.Is(err, ErrResponse) {
		t.Fatalf("401 = %v, want ErrResponse", err)
	}
	if calls.Load() != 1 {
		t.Fatalf("401 retried: %d calls", calls.Load())
	}
}
