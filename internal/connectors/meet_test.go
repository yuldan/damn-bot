package connectors

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/modules/host"
)

func meetModule(t *testing.T, node, goos string) *Module {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"enabled": true, "node_url": node,
		"consent_text": "This meeting is recorded and transcribed.",
	})
	if err != nil {
		t.Fatalf("meet raw: %v", err)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, Meet: raw}
	m := New(&cfg, Deps{Secrets: testSecrets, GOOS: goos, Now: time.Now, Manage: &ManageDeps{InsecureTestNode: true}})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func TestF8PLUG018GoogleMeet(t *testing.T) {
	t.Parallel()
	var spoke map[string]string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/join":
			writeBody(t, w, `{"session_id":"s1"}`)
		case "/sessions/s1/captions":
			writeBody(t, w, `{"captions":[{"text":"hello"}]}`)
		case "/sessions/s1/speak":
			decodeJSON(t, r.Body, &spoke)
			writeBody(t, w, `{"ok":true}`)
		case "/sessions/s1/leave":
			writeBody(t, w, `{"ok":true}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := meetModule(t, srv.URL, "linux")
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"transcript","session_id":"s1"}`), host.Env{}); err == nil {
		t.Fatal("transcript before join was served")
	}
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"join","meeting_url":"https://meet.google.com/abc-defg-hij"}`), host.Env{}); err != nil {
		t.Fatalf("join: %v", err)
	}
	res, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"transcript","session_id":"s1","since":10}`), host.Env{})
	if err != nil || !strings.Contains(res.Content, "hello") {
		t.Fatalf("transcript = %+v, %v", res, err)
	}
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"speak","session_id":"s1","text":"hi"}`), host.Env{}); err != nil {
		t.Fatalf("speak: %v", err)
	}
	if spoke["text"] != "hi" {
		t.Fatalf("speak body = %v", spoke)
	}
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"leave","session_id":"s1"}`), host.Env{}); err != nil {
		t.Fatalf("leave: %v", err)
	}
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"join","meeting_url":"https://example.com/x"}`), host.Env{}); err == nil {
		t.Fatal("non-Meet URL was accepted")
	}
}

func TestF8MeetConsent(t *testing.T) {
	t.Parallel()
	start := time.Now()
	var join map[string]any
	var consentAt time.Duration
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/join" {
			consentAt = time.Since(start)
			decodeJSON(t, r.Body, &join)
			writeBody(t, w, `{"session_id":"s1"}`)
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()
	m := meetModule(t, srv.URL, "linux")
	if _, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"join","meeting_url":"https://meet.google.com/abc"}`), host.Env{}); err != nil {
		t.Fatalf("join: %v", err)
	}
	if consentAt > 10*time.Second {
		t.Fatalf("consent took %s", consentAt)
	}
	if join["consent_text"] != "This meeting is recorded and transcribed." || join["consent_announcement"] != true {
		t.Fatalf("consent payload = %v", join)
	}
}

func TestF8MeetNodeTLS(t *testing.T) {
	t.Parallel()
	for _, node := range []string{"ws://node.example/x", "http://node.example/x"} {
		raw, err := json.Marshal(map[string]any{"enabled": true, "node_url": node, "consent_text": "recorded"})
		if err != nil {
			t.Fatalf("raw: %v", err)
		}
		m := New(&Config{Enabled: true, Meet: raw}, Deps{})
		if err := m.Start(t.Context()); err == nil {
			t.Errorf("%s was accepted", node)
		}
	}
	raw, err := json.Marshal(map[string]any{"enabled": true, "node_url": "wss://node.example/x", "consent_text": "recorded"})
	if err != nil {
		t.Fatalf("raw: %v", err)
	}
	if err := New(&Config{Enabled: true, Meet: raw}, Deps{}).Start(t.Context()); err != nil {
		t.Fatalf("wss node: %v", err)
	}
}

func TestConnectorsMeetNonLinux(t *testing.T) {
	t.Parallel()
	m := meetModule(t, "https://node.example", "windows")
	_, err := m.Invoke(t.Context(), "google_meet", json.RawMessage(`{"action":"join","meeting_url":"https://meet.google.com/abc"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "module_unavailable" {
		t.Fatalf("non-Linux = %v", err)
	}
}
