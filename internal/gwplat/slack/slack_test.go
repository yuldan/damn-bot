package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
)

func wsServer(t *testing.T, fn func(context.Context, *websocket.Conn)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() {
			if err := ws.CloseNow(); err != nil {
				return
			}
		}()
		fn(r.Context(), ws)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func TestF7PLAT019Slack(t *testing.T) {
	latency := make(chan time.Duration, 1)
	posted := make(chan map[string]string, 1)
	srv, wsSrv := slackServers(t, latency, posted)
	defer srv.Close()
	defer wsSrv.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("slack")
	if derr != nil {
		t.Fatal(derr)
	}
	got := make(chan gateway.Inbound, 2)
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL}, &desc,
		secrets{"slack_bot_token": "bot", "slack_app_token": "app"}, newOutbox(t, "slack"),
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if err := a.Run(ctx); err != nil {
			return
		}
	}()
	in := awaitInbound(t, got)
	if in.UpdateID != "Ev-1" || in.ChatID != "C1" {
		t.Fatalf("inbound = %+v", in)
	}
	d := awaitValue(t, latency)
	if d > 3*time.Second {
		t.Fatalf("ack %s > 3s", d)
	}
	awaitNoInbound(t, got)
	sendSlack(t, a, posted)
}

func sendSlack(t *testing.T, a *Adapter, posted chan map[string]string) {
	if err := a.Send(t.Context(), "C1", "[dokumen](https://example.com)"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, posted)
	if !strings.Contains(body["text"], "<https://example.com|dokumen>") {
		t.Fatalf("mrkdwn = %v", body)
	}
}

func TestF7SlackAck(t *testing.T) {
	block := make(chan struct{})
	acked := make(chan struct{}, 1)
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if err := ws.Write(ctx, websocket.MessageText, []byte(`{"envelope_id":"e1","type":"events_api","payload":{"event_id":"Ev","event":{"type":"message","channel":"C","user":"U","text":"x"}}}`)); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err == nil {
			acked <- struct{}{}
		}
		<-ctx.Done()
	})
	defer wsSrv.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(wsSrv.URL, "http")}); err != nil {
			return
		}
	}))
	defer srv.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("slack")
	if derr != nil {
		t.Fatal(derr)
	}
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL}, &desc,
		secrets{"slack_bot_token": "b", "slack_app_token": "a"}, newOutbox(t, "slack"),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { <-block; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if err := a.Run(ctx); err != nil {
			return
		}
	}()
	select {
	case <-acked:
		close(block)
	case <-time.After(3 * time.Second):
		t.Fatal("ack waited for the turn")
	}
}

func slackServers(t *testing.T, latency chan time.Duration, posted chan map[string]string) (*httptest.Server, *httptest.Server) {
	t.Helper()
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		envelope := []byte(`{"envelope_id":"env-1","type":"events_api","payload":{"event_id":"Ev-1","event":{"type":"message","text":"halo","channel":"C1","user":"U1","ts":"1.1"}}}`)
		start := time.Now()
		if err := ws.Write(ctx, websocket.MessageText, envelope); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err == nil {
			latency <- time.Since(start)
		}
		if err := ws.Write(ctx, websocket.MessageText, envelope); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		<-ctx.Done()
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/apps.connections.open":
			if err := json.NewEncoder(w).Encode(map[string]any{"ok": true, "url": "ws" + strings.TrimPrefix(wsSrv.URL, "http")}); err != nil {
				return
			}
		case "/chat.postMessage":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return
			}
			posted <- body
			if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, wsSrv
}
