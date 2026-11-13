package dingtalk

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

func TestF7PLAT004DingTalk(t *testing.T) {
	frame := []byte(`{"headers":{"messageId":"msg-1"},"data":{"msgId":"msg-1","conversationId":"cid-1","senderId":"u-1","conversationType":"1","text":{"content":"halo"}}}`)
	latency := make(chan time.Duration, 1)
	sent := make(chan map[string]any, 1)
	srv, wsSrv := dingtalkServers(t, frame, latency, sent)
	defer srv.Close()
	defer wsSrv.Close()
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("dingtalk")
	if derr != nil {
		t.Fatal(derr)
	}
	out := newOutbox(t)
	got := make(chan gateway.Inbound, 2)
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL}, &desc,
		secrets{"dingtalk_client_id": "id", "dingtalk_client_secret": "secret"}, out,
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
	if in.UpdateID != "msg-1" {
		t.Fatalf("inbound = %+v", in)
	}
	d := awaitValue(t, latency)
	if d > 3*time.Second {
		t.Fatalf("ack %s > 3s", d)
	}
	awaitNoInbound(t, got)
	sendDingTalk(t, a, sent)
}

func sendDingTalk(t *testing.T, a *Adapter, sent chan map[string]any) {
	if err := a.Send(t.Context(), "cid-1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if !strings.Contains(string(mustJSON(t, body)), "halo") {
		t.Fatalf("sent = %v", body)
	}
}

func TestF7DingTalkDedupe(t *testing.T) {
	desc := mustDesc(t, "dingtalk")
	a, err := New(t.Context(), &Config{Enabled: true, API: "http://127.0.0.1:1"}, &desc,
		secrets{"dingtalk_client_id": "id", "dingtalk_client_secret": "s"}, newOutbox(t),
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil }))
	if err != nil {
		t.Fatal(err)
	}
	in := gateway.Inbound{Platform: "dingtalk", ChatID: "c", SenderID: "u", UpdateID: "same", Mentioned: true}
	turns := 0
	a.handler = gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { turns++; return nil })
	for range 3 {
		if a.dedupe.Claim("dingtalk:"+in.UpdateID, a.now()) {
			if err := gwplat.Dispatch(t.Context(), a.handler, &in); err != nil {
				t.Fatal(err)
			}
		}
	}
	if turns != 1 {
		t.Fatalf("turns = %d", turns)
	}
}

func mustDesc(t *testing.T, name string) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func dingtalkServers(t *testing.T, frame []byte, latency chan time.Duration, sent chan map[string]any) (*httptest.Server, *httptest.Server) {
	t.Helper()
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		start := time.Now()
		if err := ws.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err == nil {
			latency <- time.Since(start)
		}
		if err := ws.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		<-ctx.Done()
	})
	srv := httptest.NewServer(dingtalkAPI(t, wsSrv.URL, sent))
	return srv, wsSrv
}

func dingtalkAPI(t *testing.T, wsURL string, sent chan map[string]any) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1.0/oauth2/accessToken":
			if _, err := w.Write([]byte(`{"accessToken":"tok","expireIn":7200}`)); err != nil {
				return
			}
		case "/v1.0/gateway/connections/open":
			if err := json.NewEncoder(w).Encode(map[string]string{"endpoint": "ws" + strings.TrimPrefix(wsURL, "http"), "ticket": "t"}); err != nil {
				return
			}
		case "/v1.0/robot/oToMessages/batchSend":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				return
			}
			sent <- body
			if _, err := w.Write([]byte(`{"processQueryKey":"x"}`)); err != nil {
				return
			}
		default:
			http.NotFound(w, r)
		}
	})
}
