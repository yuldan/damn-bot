package feishu

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

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gwplat"
	"github.com/damnbot/damnbot/internal/netx"
)

// feishuDesc returns the registered descriptor of the adapter.
func feishuDesc(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("feishu")
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// feishuAdapter builds a valid adapter without opening a connection.
func feishuAdapter(t *testing.T, cfg *Config, h gwplat.Handler) *Adapter {
	t.Helper()
	desc := feishuDesc(t)
	if h == nil {
		h = gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	}
	a, err := New(t.Context(), cfg, &desc,
		secrets{"feishu_app_id": "app", "feishu_app_secret": "secret", "feishu_verification_token": "verify"},
		newOutbox(t), h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestFeishuNewRejectsInvalidActivation covers every fail-closed branch of New.
func TestFeishuNewRejectsInvalidActivation(t *testing.T) {
	desc := feishuDesc(t)
	other := desc
	other.Name = "slack"
	out := newOutbox(t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	okCfg := func() *Config { return &Config{Enabled: true} }
	okSecrets := secrets{"feishu_app_id": "a", "feishu_app_secret": "s", "feishu_verification_token": "v"}
	cases := []struct {
		name string
		cfg  *Config
		d    *gwplat.Descriptor
		o    *gwplat.Outbox
		h    gwplat.Handler
		src  gwplat.SecretSource
		want error
	}{
		{"nil config", nil, &desc, out, h, okSecrets, ErrConfig},
		{"nil descriptor", okCfg(), nil, out, h, okSecrets, ErrConfig},
		{"foreign descriptor", okCfg(), &other, out, h, okSecrets, ErrConfig},
		{"disabled", &Config{}, &desc, out, h, okSecrets, gwplat.ErrUnavailable},
		{"nil outbox", okCfg(), &desc, nil, h, okSecrets, ErrConfig},
		{"nil handler", okCfg(), &desc, out, nil, okSecrets, ErrConfig},
		{"missing secret", okCfg(), &desc, out, h, secrets{}, gwplat.ErrMissingSecret},
	}
	for _, tc := range cases {
		if _, err := New(t.Context(), tc.cfg, tc.d, tc.src, tc.o, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestFeishuTenantToken covers the token cache, refresh after expiry and both
// corrupt/error answers of the auth endpoint.
func TestFeishuTenantToken(t *testing.T) {
	var hits atomic.Int32
	var body atomic.Value
	body.Store([]byte(`{"tenant_access_token":"tenant","expire":7200}`))
	status := atomic.Int32{}
	status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(int(status.Load()))
		payload, ok := body.Load().([]byte)
		if !ok {
			return
		}
		if _, err := w.Write(payload); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	now := time.Unix(1_800_000_000, 0)
	clock := func() time.Time { return now }
	a := feishuAdapter(t, &Config{Enabled: true, API: srv.URL, Now: clock, HTTPClient: netx.LoopbackClient()}, nil)
	tok, err := a.tenantToken(t.Context())
	if err != nil || tok != "tenant" {
		t.Fatalf("token = %q err %v", tok, err)
	}
	if _, err := a.tenantToken(t.Context()); err != nil || hits.Load() != 1 {
		t.Fatalf("cached token hits = %d err = %v", hits.Load(), err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := a.tenantToken(t.Context()); err != nil || hits.Load() != 2 {
		t.Fatalf("expired token hits = %d err = %v", hits.Load(), err)
	}
	for _, tc := range []struct {
		status int32
		body   []byte
	}{
		{http.StatusInternalServerError, []byte(`{}`)},
		{http.StatusOK, []byte(`not-json`)},
		{http.StatusOK, []byte(`{"tenant_access_token":""}`)},
	} {
		status.Store(tc.status)
		body.Store(tc.body)
		fresh := feishuAdapter(t, &Config{Enabled: true, API: srv.URL, Now: clock, HTTPClient: netx.LoopbackClient()}, nil)
		if _, err := fresh.tenantToken(t.Context()); err == nil {
			t.Errorf("status %d body %q accepted", tc.status, tc.body)
		}
	}
}

// TestFeishuHandlerRejects covers the callback's read guards: wrong verb,
// oversized body, corrupt JSON and a payload that parses to no message.
func TestFeishuHandlerRejects(t *testing.T) {
	a := feishuAdapter(t, &Config{Enabled: true}, nil)
	h := a.Handler()
	cases := []struct {
		name   string
		method string
		body   string
		want   int
	}{
		{"wrong verb", http.MethodPut, `{}`, http.StatusMethodNotAllowed},
		{"oversized", http.MethodPost, strings.Repeat("x", (1<<20)+1), http.StatusRequestEntityTooLarge},
		{"corrupt json", http.MethodPost, `not-json`, http.StatusBadRequest},
		{"missing event id", http.MethodPost, `{"header":{"token":"verify"},"event":{"message":{"chat_id":"c1","content":"{\"text\":\"x\"}"}}}`, http.StatusOK},
		{"corrupt content", http.MethodPost, `{"header":{"event_id":"e","token":"verify"},"event":{"message":{"chat_id":"c1","content":"not-json"}}}`, http.StatusOK},
	}
	for _, tc := range cases {
		req := httptest.NewRequestWithContext(t.Context(), tc.method, "/feishu", strings.NewReader(tc.body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != tc.want {
			t.Errorf("%s = %d, want %d", tc.name, rec.Code, tc.want)
		}
	}
	challenge := `{"challenge":"abc","header":{"token":"verify"}}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/feishu", strings.NewReader(challenge))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"challenge":"abc"`) {
		t.Fatalf("challenge = %d %q", rec.Code, rec.Body.String())
	}
}

// TestFeishuHandlerDispatchError: a core failure is reported as 500 so Feishu
// retries the event instead of losing it.
func TestFeishuHandlerDispatchError(t *testing.T) {
	a := feishuAdapter(t, &Config{Enabled: true}, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		return errors.New("boom")
	}))
	body := `{"header":{"event_id":"e1","token":"verify"},"event":{"message":{"chat_id":"c1","chat_type":"p2p","content":"{\"text\":\"x\"}"}}}`
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/feishu", strings.NewReader(body))
	rec := httptest.NewRecorder()
	a.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("dispatch failure = %d, want 500", rec.Code)
	}
}

// TestFeishuParse covers the envelope mapping independently of HTTP.
func TestFeishuParse(t *testing.T) {
	if _, ok := parse("", json.RawMessage(`{}`)); ok {
		t.Error("empty event id accepted")
	}
	if _, ok := parse("e", json.RawMessage(`{`)); ok {
		t.Error("corrupt event accepted")
	}
	if _, ok := parse("e", json.RawMessage(`{"message":{"chat_id":""}}`)); ok {
		t.Error("empty chat accepted")
	}
	in, ok := parse("e2", json.RawMessage(`{"message":{"chat_id":"g1","chat_type":"group","content":"{\"text\":\"hi @bot\"}"},"sender":{"sender_id":{"open_id":"ou"}}}`))
	if !ok || !in.Group || !in.Mentioned || in.Text != "hi @bot" || in.SenderID != "ou" || in.Payload == "" {
		t.Fatalf("group parse = %+v ok=%v", in, ok)
	}
	in, ok = parse("e3", json.RawMessage(`{"message":{"chat_id":"c1","chat_type":"p2p","content":"{\"text\":\"hi\"}"}}`))
	if !ok || in.Group || !in.Mentioned {
		t.Fatalf("dm parse = %+v ok=%v", in, ok)
	}
}

// TestFeishuSessionFiltersAndDispatch: a malformed WS frame is skipped, the
// valid event is acked and dispatched, and a handler failure ends the session.
func TestFeishuSessionFiltersAndDispatch(t *testing.T) {
	acks := make(chan struct{}, 2)
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		if err := ws.Write(ctx, websocket.MessageText, []byte(`not-json`)); err != nil {
			return
		}
		frame := []byte(`{"header":{"event_id":"ev-1"},"event":{"message":{"chat_id":"c1","chat_type":"p2p","content":"{\"text\":\"halo\"}"},"sender":{"sender_id":{"open_id":"ou"}}}}`)
		if err := ws.Write(ctx, websocket.MessageText, frame); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		acks <- struct{}{}
		<-ctx.Done()
	})
	t.Cleanup(wsSrv.Close)
	got := make(chan gateway.Inbound, 1)
	a := feishuAdapter(t, &Config{Enabled: true, WSURL: "ws" + strings.TrimPrefix(wsSrv.URL, "http")},
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	in := awaitInbound(t, got)
	if in.UpdateID != "ev-1" || in.ChatID != "c1" || in.Text != "halo" {
		t.Fatalf("inbound = %+v", in)
	}
	select {
	case <-acks:
	case <-time.After(5 * time.Second):
		t.Fatal("valid frame was not acked")
	}
	cancel()
	if rerr := <-errCh; rerr != nil {
		t.Logf("run after cancellation: %v", rerr)
	}

	failing := feishuAdapter(t, &Config{Enabled: true, WSURL: "ws" + strings.TrimPrefix(wsSrv.URL, "http")},
		gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return errors.New("boom") }))
	if serr := failing.session(t.Context()); serr == nil || !strings.Contains(serr.Error(), "dispatch") {
		t.Fatalf("dispatch failure = %v", serr)
	}
}

// TestFeishuRunRequiresWSAndRetries: Run refuses an empty WS URL and keeps
// reconnecting through the configured backoff until the context ends.
func TestFeishuRunRequiresWSAndRetries(t *testing.T) {
	a := feishuAdapter(t, &Config{Enabled: true}, nil)
	if err := a.Run(t.Context()); !errors.Is(err, ErrConfig) {
		t.Fatalf("empty ws url = %v, want ErrConfig", err)
	}
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	cfg := &Config{Enabled: true, WSURL: "ws" + strings.TrimPrefix(url, "http"), Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond}}
	retrying := feishuAdapter(t, cfg, nil)
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	if err := retrying.Run(ctx); err == nil {
		t.Fatal("Run with a dead websocket returned nil")
	}
}
