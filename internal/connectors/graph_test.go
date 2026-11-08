package connectors

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

const graphTestSecret = "graph-client-secret-value"

// testSecrets resolves the env names used by the connector tests.
func testSecrets(name string) (string, error) {
	switch name {
	case "GRAPH_SECRET":
		return graphTestSecret, nil
	case "DISCORD_TOKEN":
		return "discord-token-123456", nil
	case "XAI_KEY":
		return "xai-key-123456", nil
	case "FEISHU_SECRET":
		return "feishu-app-secret-123456", nil
	}
	return "test-secret-123456", nil
}

func graphModule(t *testing.T, srv *httptest.Server) *Module {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"enabled": true, "tenant_id": "tenant", "client_id": "client",
		"client_secret_env": "GRAPH_SECRET", "base_url": srv.URL + "/v1.0",
		"token_url": srv.URL + "/token",
	})
	if err != nil {
		t.Fatalf("graph raw: %v", err)
	}
	cfg := Config{
		Enabled: true, Timeout: 5 * time.Second, Retries: 1, Graph: raw,
	}
	m := New(&cfg, Deps{Secrets: testSecrets, Now: func() time.Time {
		return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	}})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func graphResult(t *testing.T, res host.Result) []map[string]any {
	t.Helper()
	var out struct {
		Items     []map[string]any `json:"items"`
		Truncated bool             `json:"truncated"`
	}
	if err := json.Unmarshal([]byte(res.Content), &out); err != nil {
		t.Fatalf("result %q: %v", res.Content, err)
	}
	return out.Items
}

func TestMSGraphMailPagination(t *testing.T) {
	t.Parallel()
	var tokens atomic.Int32
	var messages atomic.Int32
	var skips []string
	inner := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			tokens.Add(1)
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/messages"):
			messages.Add(1)
			skips = append(skips, r.URL.Query().Get("$skip"))
			if got := r.URL.Query().Get("$top"); got != "2" {
				t.Errorf("$top = %q", got)
			}
			if got := r.URL.Query().Get("$select"); got != "subject,from" {
				t.Errorf("$select = %q", got)
			}
			if got := r.Header.Get("Authorization"); got != "Bearer graph-token-abc123" {
				t.Errorf("authorization = %q", got)
			}
			writeBody(t, w, `{"value":[{"subject":"a"},{"subject":"b"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer inner.Close()
	m := graphModule(t, inner)
	res, err := m.Invoke(t.Context(), "ms_graph_mail",
		json.RawMessage(`{"folder":"Inbox","top":2,"page":2,"select":["subject","from"]}`), host.Env{})
	if err != nil {
		t.Fatalf("mail: %v", err)
	}
	if items := graphResult(t, res); len(items) != 4 {
		t.Fatalf("items = %d, want 4 (%s)", len(items), res.Content)
	}
	if messages.Load() != 2 || len(skips) != 2 || skips[0] != "0" || skips[1] != "2" {
		t.Fatalf("skips = %v, calls %d", skips, messages.Load())
	}
	if res.UntrustedSource != "connector:graph" {
		t.Fatalf("untrusted source = %q", res.UntrustedSource)
	}
	// A third page is beyond the hard cap and never reaches the server.
	if _, err := m.Invoke(t.Context(), "ms_graph_mail", json.RawMessage(`{"page":6}`), host.Env{}); err == nil {
		t.Fatal("page 6 was accepted")
	}
}

func TestMSGraphCalendarAndFiles(t *testing.T) {
	t.Parallel()
	var calendarQuery, filesPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
		case strings.HasSuffix(r.URL.Path, "/calendarView"):
			calendarQuery = r.URL.RawQuery
			writeBody(t, w, `{"value":[{"subject":"standup"}]}`)
		case strings.Contains(r.URL.Path, "/drive/root:/"):
			filesPath = r.URL.EscapedPath()
			writeBody(t, w, `{"value":[{"name":"q4.xlsx"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := graphModule(t, srv)
	if _, err := m.Invoke(t.Context(), "ms_graph_calendar", json.RawMessage(`{"days":3}`), host.Env{}); err != nil {
		t.Fatalf("calendar: %v", err)
	}
	if !strings.Contains(calendarQuery, "startDateTime=2026-11-08T12") ||
		!strings.Contains(calendarQuery, "endDateTime=2026-11-11T12") {
		t.Fatalf("calendar query = %q", calendarQuery)
	}
	res, err := m.Invoke(t.Context(), "ms_graph_files", json.RawMessage(`{"path":"Reports/2026 Q4"}`), host.Env{})
	if err != nil {
		t.Fatalf("files: %v", err)
	}
	if !strings.Contains(filesPath, "/drive/root:/Reports/2026%20Q4:/children") {
		t.Fatalf("files path = %q", filesPath)
	}
	if items := graphResult(t, res); len(items) != 1 {
		t.Fatalf("files items = %d", len(items))
	}
}

func TestMSGraphTokenCache(t *testing.T) {
	t.Parallel()
	var tokens atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			tokens.Add(1)
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
			return
		}
		writeBody(t, w, `{"value":[]}`)
	}))
	defer srv.Close()
	m := graphModule(t, srv)
	for range 2 {
		if _, err := m.Invoke(t.Context(), "ms_graph_mail", json.RawMessage(`{}`), host.Env{}); err != nil {
			t.Fatalf("mail: %v", err)
		}
	}
	if tokens.Load() != 1 {
		t.Fatalf("token calls = %d, want 1", tokens.Load())
	}
}

func TestMSGraphErrors(t *testing.T) {
	t.Parallel()
	var status atomic.Int32
	status.Store(http.StatusForbidden)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
			return
		}
		w.WriteHeader(int(status.Load()))
		writeBody(t, w, "permission denied for "+graphTestSecret)
	}))
	defer srv.Close()
	m := graphModule(t, srv)
	_, err := m.Invoke(t.Context(), "ms_graph_mail", json.RawMessage(`{}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" || strings.Contains(err.Error(), graphTestSecret) {
		t.Fatalf("403 = %v", err)
	}
	status.Store(http.StatusRequestEntityTooLarge)
	_, err = m.Invoke(t.Context(), "ms_graph_mail", json.RawMessage(`{}`), host.Env{})
	if !errors.As(err, &he) || he.Code != "too_large" {
		t.Fatalf("413 = %v", err)
	}
}

func TestMSGraphRetry429(t *testing.T) {
	t.Parallel()
	var messages atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/token":
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
		default:
			if messages.Add(1) == 1 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			writeBody(t, w, `{"value":[{"subject":"ok"}]}`)
		}
	}))
	defer srv.Close()
	m := graphModule(t, srv)
	res, err := m.Invoke(t.Context(), "ms_graph_mail", json.RawMessage(`{"top":1}`), host.Env{})
	if err != nil {
		t.Fatalf("retry: %v", err)
	}
	if messages.Load() != 2 {
		t.Fatalf("message calls = %d, want 2", messages.Load())
	}
	if items := graphResult(t, res); len(items) != 1 {
		t.Fatalf("items = %d", len(items))
	}
}
