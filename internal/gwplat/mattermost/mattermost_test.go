package mattermost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

func TestF7PLAT012Mattermost(t *testing.T) {
	authSeen := make(chan string, 1)
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		_, raw, err := ws.Read(ctx)
		if err != nil {
			return
		}
		var auth struct {
			Action string            `json:"action"`
			Data   map[string]string `json:"data"`
		}
		if err := json.Unmarshal(raw, &auth); err != nil {
			return
		}
		authSeen <- auth.Action + ":" + auth.Data["token"]
		post, perr := json.Marshal(map[string]string{"id": "p1", "message": "halo", "channel_id": "c1", "user_id": "u1"})
		if perr != nil {
			return
		}
		event, eerr := json.Marshal(map[string]any{"event": "posted", "data": map[string]string{"post": string(post)}})
		if eerr != nil {
			return
		}
		if err := ws.Write(ctx, websocket.MessageText, event); err != nil {
			return
		}
		<-ctx.Done()
	})
	defer wsSrv.Close()
	sent := make(chan map[string]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return
		}
		sent <- body
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("mattermost")
	if derr != nil {
		t.Fatal(derr)
	}
	got := make(chan gateway.Inbound, 2)
	a, err := New(t.Context(), &Config{
		Enabled: true, BaseURL: srv.URL,
		WSURL: "ws" + strings.TrimPrefix(wsSrv.URL, "http"),
	}, &desc,
		secrets{"mattermost_token": "secret"}, newOutbox(t),
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
	auth := awaitValue(t, authSeen)
	if auth != "authentication_challenge:secret" {
		t.Fatalf("auth = %q", auth)
	}
	in := awaitInbound(t, got)
	if in.UpdateID != "p1" || in.ChatID != "c1" {
		t.Fatalf("inbound = %+v", in)
	}
	sendMattermost(t, a, sent)
}

func sendMattermost(t *testing.T, a *Adapter, sent chan map[string]string) {
	if err := a.Send(t.Context(), "c1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if !strings.Contains(body["message"], "halo") {
		t.Fatalf("sent = %v", body)
	}
}
