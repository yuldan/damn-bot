package feishu

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

func TestF7PLAT007Feishu(t *testing.T) {
	tokenHits := make(chan struct{}, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/open-apis/auth/v3/tenant_access_token/internal":
			select {
			case tokenHits <- struct{}{}:
			default:
			}
			if _, err := w.Write([]byte(`{"tenant_access_token":"tenant","expire":7200}`)); err != nil {
				return
			}
		case "/open-apis/im/v1/messages":
			if _, err := w.Write([]byte(`{"code":0}`)); err != nil {
				return
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	var got []gateway.Inbound
	reg, rerr := gwplat.NewDefaultRegistry()
	if rerr != nil {
		t.Fatal(rerr)
	}
	desc, derr := reg.Lookup("feishu")
	if derr != nil {
		t.Fatal(derr)
	}
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL}, &desc,
		secrets{"feishu_app_id": "app", "feishu_app_secret": "secret", "feishu_verification_token": "verify"},
		newOutbox(t, "feishu"), gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
			got = append(got, *in)
			return nil
		}))
	if err != nil {
		t.Fatal(err)
	}
	h := a.Handler()
	body := `{"header":{"event_id":"ev-1","token":"verify"},"event":{"message":{"message_id":"m-1","chat_id":"oc-1","chat_type":"p2p","content":"{\"text\":\"halo\"}"},"sender":{"sender_id":{"open_id":"ou-1"}}}}`
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/feishu", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook = %d", rec.Code)
		}
	}
	if len(got) != 1 {
		t.Fatalf("turns = %d", len(got))
	}
	bad := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/feishu", strings.NewReader(`{"header":{"event_id":"x","token":"wrong"},"event":{}}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, bad)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong token = %d", rec.Code)
	}
	feishuSend(t, a, tokenHits)

	feishuWS(t, &desc, srv.URL)
}

var _ = json.Marshal

func feishuWS(t *testing.T, desc *gwplat.Descriptor, api string) {
	// WS long connection dedupes the same event id.
	ch := make(chan gateway.Inbound, 2)
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		frame := []byte(`{"header":{"event_id":"ev-ws"},"event":{"message":{"message_id":"m-ws","chat_id":"oc-1","chat_type":"p2p","content":"{\"text\":\"ws\"}"},"sender":{"sender_id":{"open_id":"ou-1"}}}}`)
		if werr := ws.Write(ctx, websocket.MessageText, frame); werr != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		if werr := ws.Write(ctx, websocket.MessageText, frame); werr != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		<-ctx.Done()
	})
	defer wsSrv.Close()
	wa, err := New(t.Context(), &Config{Enabled: true, API: api, WSURL: "ws" + strings.TrimPrefix(wsSrv.URL, "http")}, desc,
		secrets{"feishu_app_id": "app", "feishu_app_secret": "secret", "feishu_verification_token": "verify"},
		newOutbox(t, "feishu"), gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { ch <- *in; return nil }))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if err := wa.Run(ctx); err != nil {
			return
		}
	}()
	in := awaitInbound(t, ch)
	if in.UpdateID != "ev-ws" {
		t.Fatalf("ws inbound = %+v", in)
	}
	awaitNoInbound(t, ch)
}

func feishuSend(t *testing.T, a *Adapter, tokenHits chan struct{}) {
	if serr := a.Send(t.Context(), "oc-1", "**satu**"); serr != nil {
		t.Fatal(serr)
	}
	if serr := a.Send(t.Context(), "oc-1", "**dua**"); serr != nil {
		t.Fatal(serr)
	}
	select {
	case <-tokenHits:
	case <-time.After(5 * time.Second):
		t.Fatal("no tenant token request")
	}
}
