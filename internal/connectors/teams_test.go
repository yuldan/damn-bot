package connectors

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

type teamsPipelineCapture struct {
	created  map[string]any
	renewed  map[string]any
	resource string
}

func teamsPipelineServer(t *testing.T, c *teamsPipelineCapture) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/token":
			writeBody(t, w, `{"access_token":"graph-token-abc123","expires_in":3600}`)
		case r.Method == http.MethodPost && r.URL.Path == "/v1.0/subscriptions":
			decodeJSON(t, r.Body, &c.created)
			writeBody(t, w, `{"id":"sub-1","resource":"communications/onlineMeetings/getAllTranscripts"}`)
		case r.Method == http.MethodPatch && strings.HasSuffix(r.URL.Path, "/sub-1"):
			decodeJSON(t, r.Body, &c.renewed)
			writeBody(t, w, `{"id":"sub-1","expirationDateTime":"2026-11-08T13:00:00Z"}`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/sub-1"):
			writeBody(t, w, `{"id":"sub-1","expirationDateTime":"2026-11-08T12:55:00Z"}`)
		case strings.Contains(r.URL.Path, "/onlineMeetings/meeting-1/transcripts"):
			c.resource = r.URL.RawQuery
			writeBody(t, w, `{"value":[{"id":"tr-1"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func teamsModule(t *testing.T, srv *httptest.Server) *Module {
	t.Helper()
	graphRaw, err := json.Marshal(map[string]any{
		"enabled": true, "tenant_id": "tenant", "client_id": "client",
		"client_secret_env": "GRAPH_SECRET", "base_url": srv.URL + "/v1.0", "token_url": srv.URL + "/token",
	})
	if err != nil {
		t.Fatalf("graph raw: %v", err)
	}
	teamsRaw, err := json.Marshal(map[string]any{"enabled": true, "notification_url": "https://example.test/notify"})
	if err != nil {
		t.Fatalf("teams raw: %v", err)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, Retries: 1, Graph: graphRaw, Teams: teamsRaw}
	m := New(&cfg, Deps{Secrets: testSecrets, Now: func() time.Time {
		return time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	}})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func TestF8PLUG026TeamsPipeline(t *testing.T) {
	t.Parallel()
	var cap teamsPipelineCapture
	srv := teamsPipelineServer(t, &cap)
	defer srv.Close()
	m := teamsModule(t, srv)
	for _, call := range []string{
		`{"action":"subscribe"}`,
		`{"action":"renew","subscription_id":"sub-1"}`,
		`{"action":"status","subscription_id":"sub-1"}`,
		`{"action":"transcripts","meeting_id":"meeting-1"}`,
	} {
		if _, err := m.Invoke(t.Context(), "teams_pipeline", json.RawMessage(call), host.Env{}); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
	}
	if cap.created["changeType"] != "created" || cap.created["notificationUrl"] != "https://example.test/notify" ||
		cap.created["resource"] != "communications/onlineMeetings/getAllTranscripts" {
		t.Fatalf("subscription body = %v", cap.created)
	}
	if cap.created["expirationDateTime"] != "2026-11-08T12:55:00Z" || cap.renewed["expirationDateTime"] != "2026-11-08T12:55:00Z" {
		t.Fatalf("expiry created=%v renewed=%v", cap.created["expirationDateTime"], cap.renewed["expirationDateTime"])
	}
	if cap.resource != "%24top=25" {
		t.Fatalf("transcripts query = %q", cap.resource)
	}
	if _, err := m.Invoke(t.Context(), "teams_pipeline", json.RawMessage(`{"action":"nope"}`), host.Env{}); err == nil {
		t.Fatal("unknown action was accepted")
	}
}
