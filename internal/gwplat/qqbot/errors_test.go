package qqbot

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
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

// qqScheduler starts the shared outq scheduler used by Send.
func qqScheduler(t *testing.T) *outq.Scheduler {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if rerr := sched.Run(ctx); rerr != nil {
			return
		}
	}()
	return sched
}

// qqDesc returns the registered descriptor of the adapter.
func qqDesc(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("qqbot")
	if err != nil {
		t.Fatal(err)
	}
	return desc
}

// qqAdapter builds a valid adapter against srv without starting Run.
func qqAdapter(t *testing.T, srv *httptest.Server, now func() time.Time, h gwplat.Handler) *Adapter {
	t.Helper()
	desc := qqDesc(t)
	sched := qqScheduler(t)
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	if h == nil {
		h = gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	}
	a, err := New(t.Context(), &Config{Enabled: true, API: srv.URL, TokenAPI: srv.URL, Now: now},
		&desc, secrets{"qqbot_app_id": "app", "qqbot_client_secret": "secret"}, out, h)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

// TestQQBotNewRejectsInvalidActivation covers every fail-closed branch of New.
func TestQQBotNewRejectsInvalidActivation(t *testing.T) {
	desc := qqDesc(t)
	other := desc
	other.Name = "slack"
	sched := qqScheduler(t)
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatal(err)
	}
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	okCfg := func() *Config { return &Config{Enabled: true} }
	cases := []struct {
		name string
		cfg  *Config
		d    *gwplat.Descriptor
		o    *gwplat.Outbox
		h    gwplat.Handler
		src  gwplat.SecretSource
		want error
	}{
		{"nil config", nil, &desc, out, h, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, ErrConfig},
		{"nil descriptor", okCfg(), nil, out, h, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, ErrConfig},
		{"foreign descriptor", okCfg(), &other, out, h, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, ErrConfig},
		{"disabled", &Config{}, &desc, out, h, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, gwplat.ErrUnavailable},
		{"nil outbox", okCfg(), &desc, nil, h, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, ErrConfig},
		{"nil handler", okCfg(), &desc, out, nil, secrets{"qqbot_app_id": "a", "qqbot_client_secret": "s"}, ErrConfig},
		{"missing secret", okCfg(), &desc, out, h, secrets{}, gwplat.ErrMissingSecret},
	}
	for _, tc := range cases {
		if _, err := New(t.Context(), tc.cfg, tc.d, tc.src, tc.o, tc.h); !errors.Is(err, tc.want) {
			t.Errorf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

// TestQQBotAccessToken covers the token cache and both corrupt-payload paths.
func TestQQBotAccessToken(t *testing.T) {
	var hits atomic.Int32
	var body atomic.Value
	body.Store([]byte(`{"access_token":"qtok","expires_in":7200}`))
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
	a := qqAdapter(t, srv, clock, nil)
	tok, err := a.accessToken(t.Context())
	if err != nil || tok != "qtok" {
		t.Fatalf("token = %q err %v", tok, err)
	}
	if _, err := a.accessToken(t.Context()); err != nil || hits.Load() != 1 {
		t.Fatalf("cached token hits = %d err = %v", hits.Load(), err)
	}
	now = now.Add(2 * time.Hour)
	if _, err := a.accessToken(t.Context()); err != nil || hits.Load() != 2 {
		t.Fatalf("expired token hits = %d err = %v", hits.Load(), err)
	}
	for _, tc := range []struct {
		status int32
		body   []byte
	}{
		{http.StatusInternalServerError, []byte(`{}`)},
		{http.StatusOK, []byte(`not-json`)},
		{http.StatusOK, []byte(`{"access_token":""}`)},
	} {
		status.Store(tc.status)
		body.Store(tc.body)
		fresh := qqAdapter(t, srv, clock, nil)
		if _, err := fresh.accessToken(t.Context()); err == nil {
			t.Errorf("status %d body %q accepted", tc.status, tc.body)
		}
	}
	status.Store(http.StatusOK)
	body.Store([]byte(`{"access_token":"qtok"}`))
}

// qqSessionServers wires a token endpoint, a /gateway endpoint and a WS server
// that runs fn once the identify frame arrived.
func qqSessionServers(t *testing.T, fn func(context.Context, *websocket.Conn)) *httptest.Server {
	t.Helper()
	wsSrv := wsServer(t, fn)
	t.Cleanup(wsSrv.Close)
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
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

// TestQQBotSessionGatewayPayloadErrors rejects a missing/invalid gateway URL
// and a non-2xx gateway before any websocket is dialed.
func TestQQBotSessionGatewayPayloadErrors(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"empty url", http.StatusOK, `{}`},
		{"corrupt json", http.StatusOK, `not-json`},
		{"server error", http.StatusServiceUnavailable, `{}`},
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/app/getAppAccessToken" {
				if _, err := w.Write([]byte(`{"access_token":"qtok"}`)); err != nil {
					return
				}
				return
			}
			w.WriteHeader(tc.status)
			if _, err := w.Write([]byte(tc.body)); err != nil {
				return
			}
		}))
		t.Cleanup(srv.Close)
		if err := qqAdapter(t, srv, nil, nil).session(t.Context()); err == nil {
			t.Errorf("%s accepted", tc.name)
		}
	}
}

// TestQQBotSessionDeadWebsocket: a gateway URL that cannot be dialed fails the
// session instead of retrying inline.
func TestQQBotSessionDeadWebsocket(t *testing.T) {
	dead := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := dead.URL
	dead.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/app/getAppAccessToken" {
			if _, err := w.Write([]byte(`{"access_token":"qtok"}`)); err != nil {
				return
			}
			return
		}
		if err := json.NewEncoder(w).Encode(map[string]string{"url": "ws" + strings.TrimPrefix(url, "http")}); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	if err := qqAdapter(t, srv, nil, nil).session(t.Context()); err == nil {
		t.Fatal("dead websocket accepted")
	}
}

// TestQQBotSessionFiltersFrames: unknown opcodes/types and replays never reach
// the handler; the id falls back to d.id when the envelope has none.
func TestQQBotSessionFiltersFrames(t *testing.T) {
	srv := qqSessionServers(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		frames := []string{
			`not-json`,
			`{"op":11,"t":"HEARTBEAT_ACK"}`,
			`{"op":0,"t":"MESSAGE_CREATE","id":"evt-x"}`,
			`{"op":0,"t":"AT_MESSAGE_CREATE","id":"evt-1","d":{"id":"m1","content":"halo","channel_id":"c1","author":{"id":"u1"}}}`,
			`{"op":0,"t":"AT_MESSAGE_CREATE","id":"evt-1","d":{"id":"m1","content":"halo","channel_id":"c1","author":{"id":"u1"}}}`,
			`{"op":0,"t":"AT_MESSAGE_CREATE","d":{"id":"m2","content":"dua","channel_id":"c2","author":{"id":"u2"}}}`,
		}
		for _, frame := range frames {
			if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
				return
			}
		}
		<-ctx.Done()
	})
	got := make(chan gateway.Inbound, 4)
	a := qqAdapter(t, srv, nil, gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		got <- *in
		return nil
	}))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		if err := a.Run(ctx); err != nil {
			return
		}
	}()
	first := awaitInbound(t, got)
	if first.UpdateID != "evt-1" || first.ChatID != "c1" || first.Text != "halo" {
		t.Fatalf("first = %+v", first)
	}
	second := awaitInbound(t, got)
	if second.UpdateID != "m2" || second.ChatID != "c2" || !second.Group {
		t.Fatalf("second = %+v", second)
	}
	awaitNoInbound(t, got)
}

// TestQQBotRunReturnsHandlerError: a dispatch failure ends the session with a
// wrapped error instead of dropping the turn.
func TestQQBotRunReturnsHandlerError(t *testing.T) {
	srv := qqSessionServers(t, func(ctx context.Context, ws *websocket.Conn) {
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		frame := `{"op":0,"t":"AT_MESSAGE_CREATE","id":"evt-1","d":{"id":"m1","content":"halo","channel_id":"c1","author":{"id":"u1"}}}`
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
		<-ctx.Done()
	})
	a := qqAdapter(t, srv, nil, gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
		return errors.New("boom")
	}))
	if err := a.session(t.Context()); err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("session = %v, want dispatch error", err)
	}
}

// TestQQBotRunCancelledContext: Run surfaces the context failure before it can
// schedule a reconnect.
func TestQQBotRunCancelledContext(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	t.Cleanup(srv.Close)
	a := qqAdapter(t, srv, nil, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := a.Run(ctx); err == nil {
		t.Fatal("Run with a cancelled context returned nil")
	}
	if serr := a.Send(t.Context(), "", "hi"); !errors.Is(serr, gwplat.ErrNoReply) {
		t.Fatalf("empty chat = %v, want ErrNoReply", serr)
	}
}
