package qqbot

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

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

func wsURL(srv *httptest.Server) string { return "ws" + strings.TrimPrefix(srv.URL, "http") }

func TestF7PLAT015QQBot(t *testing.T) {
	events := []byte(`{"op":0,"t":"AT_MESSAGE_CREATE","id":"evt-1","d":{"id":"m1","content":"halo","channel_id":"c1","author":{"id":"u1"}}}`)
	sent := make(chan map[string]any, 1)
	srv, wsSrv := qqServers(t, events, sent)
	defer srv.Close()
	defer wsSrv.Close()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("qqbot")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if rerr := sched.Run(ctx); rerr != nil {
			return
		}
	}()
	got := make(chan gateway.Inbound, 2)
	a, err := New(ctx, &Config{Enabled: true, API: srv.URL, TokenAPI: srv.URL}, &desc,
		secrets{"qqbot_app_id": "app", "qqbot_client_secret": "secret"}, out,
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if err := a.Run(ctx); err != nil {
			return
		}
	}()
	in := awaitInbound(t, got)
	if in.UpdateID != "evt-1" || in.ChatID != "c1" {
		t.Fatalf("inbound = %+v", in)
	}
	awaitNoInbound(t, got)
	sendQQBot(t, a, sent)
}

func sendQQBot(t *testing.T, a *Adapter, sent chan map[string]any) {
	if err := a.Send(t.Context(), "c1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if !strings.Contains(string(mustJSON(t, body)), "halo") {
		t.Fatalf("sent = %v", body)
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func awaitInbound(t *testing.T, ch <-chan gateway.Inbound) gateway.Inbound {
	t.Helper()
	select {
	case in := <-ch:
		return in
	case <-time.After(5 * time.Second):
		t.Fatal("no inbound")
		return gateway.Inbound{}
	}
}

func awaitNoInbound(t *testing.T, ch <-chan gateway.Inbound) {
	t.Helper()
	select {
	case in := <-ch:
		t.Fatalf("unexpected inbound: %+v", in)
	case <-time.After(200 * time.Millisecond):
	}
}

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}

func qqServers(t *testing.T, events []byte, sent chan map[string]any) (*httptest.Server, *httptest.Server) {
	t.Helper()
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, events); err != nil {
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, events); err != nil {
			return
		}
		<-ctx.Done()
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/app/getAppAccessToken":
			if _, err := w.Write([]byte(`{"access_token":"qtok","expires_in":7200}`)); err != nil {
				return
			}
		case "/gateway":
			if err := json.NewEncoder(w).Encode(map[string]string{"url": wsURL(wsSrv)}); err != nil {
				return
			}
		case "/v2/channels/c1/messages":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return
			}
			sent <- body
			if _, err := w.Write([]byte(`{"id":"x"}`)); err != nil {
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	return srv, wsSrv
}
