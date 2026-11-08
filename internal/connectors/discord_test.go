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

func discordModule(t *testing.T, srv *httptest.Server, admin bool) *Module {
	t.Helper()
	raw, err := json.Marshal(map[string]any{
		"enabled": true, "bot_token_env": "DISCORD_TOKEN", "base_url": srv.URL, "admin": admin,
	})
	if err != nil {
		t.Fatalf("discord raw: %v", err)
	}
	cfg := Config{Enabled: true, Timeout: 5 * time.Second, Discord: raw}
	m := New(&cfg, Deps{Secrets: testSecrets, Now: time.Now})
	if err := m.Start(t.Context()); err != nil {
		t.Fatalf("start: %v", err)
	}
	return m
}

func discordTestServer(t *testing.T, wrote *map[string]any) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bot discord-token-123456" {
			t.Errorf("authorization = %q", got)
		}
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/channels/c1/messages"):
			if got := r.URL.Query().Get("limit"); got != "5" {
				t.Errorf("limit = %q", got)
			}
			writeBody(t, w, `[{"id":"m1","content":"hi"}]`)
		case r.Method == http.MethodGet && strings.Contains(r.URL.Path, "/guilds/g1/members/search"):
			writeBody(t, w, `[{"user":{"id":"u1"}}]`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/channels/c1/threads"):
			decodeJSON(t, r.Body, wrote)
			writeBody(t, w, `{"id":"t1"}`)
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/channels/c1/messages"):
			decodeJSON(t, r.Body, wrote)
			writeBody(t, w, `{"id":"m2"}`)
		default:
			http.NotFound(w, r)
		}
	}))
}

func TestF8TOOL038Discord(t *testing.T) {
	t.Parallel()
	var wrote map[string]any
	srv := discordTestServer(t, &wrote)
	defer srv.Close()
	m := discordModule(t, srv, false)
	res, err := m.Invoke(t.Context(), "discord", json.RawMessage(`{"action":"messages","channel_id":"c1","limit":5}`), host.Env{})
	if err != nil || !strings.Contains(res.Content, "m1") || res.UntrustedSource != "connector:discord" {
		t.Fatalf("messages = %+v, %v", res, err)
	}
	if _, err := m.Invoke(t.Context(), "discord", json.RawMessage(`{"action":"members","guild_id":"g1","query":"bob"}`), host.Env{}); err != nil {
		t.Fatalf("members: %v", err)
	}
	if _, err := m.Invoke(t.Context(), "discord", json.RawMessage(`{"action":"thread","channel_id":"c1","query":"topic"}`), host.Env{}); err != nil {
		t.Fatalf("thread: %v", err)
	}
	if wrote["name"] != "topic" {
		t.Fatalf("thread body = %v", wrote)
	}
	if _, err := m.Invoke(t.Context(), "discord", json.RawMessage(`{"action":"send","channel_id":"c1","content":"hello"}`), host.Env{}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if wrote["content"] != "hello" {
		t.Fatalf("send body = %v", wrote)
	}
}

func TestF8TOOL039DiscordAdmin(t *testing.T) {
	t.Parallel()
	var pinned atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/guilds/g1/channels"):
			writeBody(t, w, `[{"id":"c1"}]`)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/guilds/g1/roles"):
			writeBody(t, w, `[{"id":"r1"}]`)
		case strings.Contains(r.URL.Path, "/channels/c1/pins/m1"):
			pinned.Add(1)
			if r.Method == http.MethodDelete {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			writeBody(t, w, `{}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	off := discordModule(t, srv, false)
	_, err := off.Invoke(t.Context(), "discord_admin", json.RawMessage(`{"action":"channels","guild_id":"g1"}`), host.Env{})
	var he *host.Error
	if !errors.As(err, &he) || he.Code != "denied" {
		t.Fatalf("admin off = %v", err)
	}
	on := discordModule(t, srv, true)
	for _, call := range []string{
		`{"action":"channels","guild_id":"g1"}`,
		`{"action":"roles","guild_id":"g1"}`,
		`{"action":"pin","guild_id":"g1","channel_id":"c1","message_id":"m1"}`,
		`{"action":"unpin","guild_id":"g1","channel_id":"c1","message_id":"m1"}`,
	} {
		if _, err := on.Invoke(t.Context(), "discord_admin", json.RawMessage(call), host.Env{}); err != nil {
			t.Fatalf("%s: %v", call, err)
		}
	}
	if pinned.Load() != 2 {
		t.Fatalf("pin calls = %d", pinned.Load())
	}
}
