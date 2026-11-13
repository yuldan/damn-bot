package dingtalk

import (
	"context"
	"encoding/json"
	"errors"
	"io"
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

func dingtalkSecrets() secrets {
	return secrets{"dingtalk_client_id": "id", "dingtalk_client_secret": "s"}
}

func okHandler() gwplat.Handler {
	return gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
}

func writeBody(w http.ResponseWriter, body string) {
	if _, err := w.Write([]byte(body)); err != nil {
		return
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		return
	}
}

func newDingtalk(t *testing.T, cfg Config, h gwplat.Handler) *Adapter {
	t.Helper()
	return newDingtalkWithOutbox(t, cfg, newOutbox(t), h)
}

func newDingtalkWithOutbox(t *testing.T, cfg Config, out *gwplat.Outbox, h gwplat.Handler) *Adapter {
	t.Helper()
	desc := mustDesc(t, "dingtalk")
	cfg.Enabled = true
	a, err := New(t.Context(), &cfg, &desc, dingtalkSecrets(), out, h)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return a
}

func errorOutbox(t *testing.T) (*gwplat.Outbox, <-chan error) {
	t.Helper()
	desc := mustDesc(t, "dingtalk")
	errs := make(chan error, 8)
	sched, err := outq.New(&outq.Options{
		Pacer: allowPacer{}, MaxAttempts: 1,
		OnError: func(_ outq.Message, sendErr error) { errs <- sendErr },
	})
	if err != nil {
		t.Fatalf("scheduler: %v", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		t.Fatalf("outbox: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if rerr := sched.Run(ctx); rerr != nil {
			return
		}
	}()
	return out, errs
}

func TestNewRejectsInvalidConfig(t *testing.T) {
	desc := mustDesc(t, "dingtalk")
	other := mustDesc(t, "wecom")
	out := newOutbox(t)
	h := okHandler()
	on := &Config{Enabled: true}
	cases := []struct {
		name string
		cfg  *Config
		desc *gwplat.Descriptor
		out  *gwplat.Outbox
		h    gwplat.Handler
		want error
	}{
		{name: "nil descriptor", cfg: on, out: out, h: h, want: ErrConfig},
		{name: "wrong descriptor", cfg: on, desc: &other, out: out, h: h, want: ErrConfig},
		{name: "nil config", desc: &desc, out: out, h: h, want: ErrConfig},
		{name: "disabled", cfg: &Config{}, desc: &desc, out: out, h: h, want: gwplat.ErrUnavailable},
		{name: "nil outbox", cfg: on, desc: &desc, h: h, want: ErrConfig},
		{name: "nil handler", cfg: on, desc: &desc, out: out, want: ErrConfig},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := New(t.Context(), tc.cfg, tc.desc, dingtalkSecrets(), tc.out, tc.h); !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestNewRequiresSecrets(t *testing.T) {
	desc := mustDesc(t, "dingtalk")
	cases := []struct {
		name string
		src  gwplat.SecretSource
	}{
		{name: "nil source"},
		{name: "missing key", src: secrets{"dingtalk_client_id": "id"}},
		{name: "blank value", src: secrets{"dingtalk_client_id": " ", "dingtalk_client_secret": "s"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(t.Context(), &Config{Enabled: true}, &desc, tc.src,
				newOutbox(t), okHandler())
			if !errors.Is(err, gwplat.ErrMissingSecret) {
				t.Fatalf("err = %v, want ErrMissingSecret", err)
			}
		})
	}
}

func TestNewDefaults(t *testing.T) {
	a := newDingtalk(t, Config{}, okHandler())
	if a.api != "https://api.dingtalk.com" {
		t.Fatalf("api = %q", a.api)
	}
	if a.now == nil || a.http.Client == nil {
		t.Fatal("nil default seam")
	}
	fixed := time.Unix(1700000000, 0)
	b := newDingtalk(t, Config{Now: func() time.Time { return fixed }}, okHandler())
	if !b.now().Equal(fixed) {
		t.Fatalf("now = %s", b.now())
	}
}

func tokenAdapter(t *testing.T, body string) *Adapter {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte(body)); err != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)
	return newDingtalk(t, Config{API: srv.URL}, okHandler())
}

func assertBadToken(t *testing.T, a *Adapter) {
	t.Helper()
	if _, err := a.accessToken(t.Context()); !errors.Is(err, gwplat.ErrBadPayload) {
		t.Fatalf("err = %v, want ErrBadPayload", err)
	}
}

func TestAccessTokenFailures(t *testing.T) {
	t.Run("bad json", func(t *testing.T) {
		assertBadToken(t, tokenAdapter(t, `{"accessToken":`))
	})
	t.Run("empty token", func(t *testing.T) {
		assertBadToken(t, tokenAdapter(t, `{"accessToken":"","expireIn":7200}`))
	})
	t.Run("status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusServiceUnavailable)
		}))
		t.Cleanup(srv.Close)
		a := newDingtalk(t, Config{API: srv.URL}, okHandler())
		var se *gwplat.StatusError
		if _, err := a.accessToken(t.Context()); !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
			t.Fatalf("err = %v, want status 503", err)
		}
	})
	t.Run("transport", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		url := srv.URL
		srv.Close()
		a := newDingtalk(t, Config{API: url}, okHandler())
		if _, err := a.accessToken(t.Context()); err == nil {
			t.Fatal("closed server must fail")
		}
	})
}

func TestAccessTokenCacheAndRefresh(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Header.Get("Content-Type") != "application/json" {
			http.Error(w, "content type", http.StatusBadRequest)
			return
		}
		writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
	}))
	t.Cleanup(srv.Close)
	now := time.Unix(1700000000, 0)
	a := newDingtalk(t, Config{API: srv.URL, Now: func() time.Time { return now }}, okHandler())
	for range 2 {
		if _, err := a.accessToken(t.Context()); err != nil {
			t.Fatalf("token: %v", err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("token calls = %d, want 1 (cached)", got)
	}
	now = now.Add(3 * time.Hour)
	if _, err := a.accessToken(t.Context()); err != nil {
		t.Fatalf("refresh: %v", err)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2 after expiry", got)
	}
}

func TestAccessTokenShortExpiryRefreshes(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		writeBody(w, `{"accessToken":"tok","expireIn":30}`)
	}))
	t.Cleanup(srv.Close)
	a := newDingtalk(t, Config{API: srv.URL}, okHandler())
	for range 2 {
		if _, err := a.accessToken(t.Context()); err != nil {
			t.Fatalf("token: %v", err)
		}
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("token calls = %d, want 2 (inside refresh window)", got)
	}
}

func TestSendFailures(t *testing.T) {
	t.Run("token", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "nope", http.StatusInternalServerError)
		}))
		t.Cleanup(srv.Close)
		out, errs := errorOutbox(t)
		a := newDingtalkWithOutbox(t, Config{API: srv.URL}, out, okHandler())
		if err := a.Send(t.Context(), "c", "x"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		if err := awaitSendError(t, errs); err == nil {
			t.Fatal("token failure was not reported")
		}
	})
	t.Run("send status", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
				writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
				return
			}
			http.Error(w, "nope", http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)
		out, errs := errorOutbox(t)
		a := newDingtalkWithOutbox(t, Config{API: srv.URL}, out, okHandler())
		if err := a.Send(t.Context(), "c", "x"); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
		var se *gwplat.StatusError
		if err := awaitSendError(t, errs); !errors.As(err, &se) || se.Code != http.StatusBadGateway {
			t.Fatalf("err = %v, want status 502", err)
		}
	})
	t.Run("empty chat", func(t *testing.T) {
		a := newDingtalk(t, Config{}, okHandler())
		if err := a.Send(t.Context(), "", "x"); !errors.Is(err, gwplat.ErrNoReply) {
			t.Fatalf("err = %v, want ErrNoReply", err)
		}
	})
}

func awaitSendError(t *testing.T, errs <-chan error) error {
	t.Helper()
	select {
	case err := <-errs:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("send failure was never reported")
		return nil
	}
}

func TestParseFrames(t *testing.T) {
	cases := []struct {
		name   string
		raw    string
		ok     bool
		chat   string
		sender string
		text   string
		group  bool
		ment   bool
	}{
		{name: "invalid json", raw: `{`},
		{name: "missing msg id", raw: `{"conversationId":"c"}`},
		{name: "missing conversation", raw: `{"msgId":"m"}`},
		{
			name: "dm", raw: `{"msgId":"m","conversationId":"c","senderId":"u","conversationType":"1","text":{"content":"hi"}}`,
			ok: true, chat: "c", sender: "u", text: "hi", ment: true,
		},
		{
			name: "empty type is dm", raw: `{"msgId":"m","conversationId":"c","conversationType":""}`,
			ok: true, chat: "c", ment: true,
		},
		{
			name: "group without mention", raw: `{"msgId":"m","conversationId":"c","conversationType":"2","text":{"content":"hi"}}`,
			ok: true, chat: "c", text: "hi", group: true,
		},
		{
			name: "group with mention", raw: `{"msgId":"m","conversationId":"c","conversationType":"2","text":{"content":"@bot hi"}}`,
			ok: true, chat: "c", text: "@bot hi", group: true, ment: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in, got := parse(json.RawMessage(tc.raw))
			if got != tc.ok {
				t.Fatalf("ok = %v, want %v", got, tc.ok)
			}
			if !got {
				return
			}
			if in.Platform != "dingtalk" || in.ChatID != tc.chat || in.SenderID != tc.sender ||
				in.Text != tc.text || in.Group != tc.group || in.Mentioned != tc.ment {
				t.Fatalf("inbound = %+v", in)
			}
			if in.UpdateID != "m" || in.Payload != tc.raw {
				t.Fatalf("id/payload = %q/%q", in.UpdateID, in.Payload)
			}
		})
	}
}

func streamFrames(ctx context.Context, ws *websocket.Conn, valid string, acked chan<- struct{}) {
	frames := []string{`{"headers":{}}`, `{"headers":{"messageId":"a"},"data":"not-json"}`, valid, valid}
	for i, frame := range frames {
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
		if i == 0 {
			continue
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
	}
	close(acked)
	<-ctx.Done()
}

func TestRunIgnoresBadFramesAndDedupes(t *testing.T) {
	const valid = `{"headers":{"messageId":"b"},"data":{"msgId":"b","conversationId":"c","senderId":"u","conversationType":"1","text":{"content":"hi"}}}`
	acked := make(chan struct{})
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		streamFrames(ctx, ws, valid, acked)
	})
	defer wsSrv.Close()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
			return
		}
		writeJSON(w, map[string]string{
			"endpoint": "ws" + strings.TrimPrefix(wsSrv.URL, "http"), "ticket": "t",
		})
	}))
	defer api.Close()
	got := make(chan gateway.Inbound, 2)
	a := newDingtalk(t, Config{API: api.URL, Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond}},
		gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error { got <- *in; return nil }))
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	select {
	case in := <-got:
		if in.UpdateID != "b" || in.ChatID != "c" {
			t.Fatalf("inbound = %+v", in)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("no dispatch")
	}
	select {
	case <-acked:
	case <-time.After(5 * time.Second):
		t.Fatal("server did not see every ack")
	}
	awaitNoInbound(t, got)
	cancel()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("cancelled run returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
}

func TestSessionGatewayErrors(t *testing.T) {
	t.Run("bad payload", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
				writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
				return
			}
			writeBody(w, `{"endpoint":""}`)
		}))
		t.Cleanup(srv.Close)
		a := newDingtalk(t, Config{API: srv.URL}, okHandler())
		if err := a.session(t.Context()); !errors.Is(err, gwplat.ErrBadPayload) {
			t.Fatalf("err = %v, want ErrBadPayload", err)
		}
	})
	t.Run("dial failure", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
				writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
				return
			}
			writeJSON(w, map[string]string{"endpoint": "ws://127.0.0.1:1/x", "ticket": "t"})
		}))
		t.Cleanup(srv.Close)
		a := newDingtalk(t, Config{API: srv.URL}, okHandler())
		if err := a.session(t.Context()); err == nil {
			t.Fatal("dial failure must surface")
		}
	})
}

func TestServeDispatchError(t *testing.T) {
	wsSrv := wsServer(t, func(ctx context.Context, ws *websocket.Conn) {
		frame := `{"headers":{"messageId":"m"},"data":{"msgId":"m","conversationId":"c","conversationType":"1"}}`
		if err := ws.Write(ctx, websocket.MessageText, []byte(frame)); err != nil {
			return
		}
		if _, _, err := ws.Read(ctx); err != nil {
			return
		}
		<-ctx.Done()
	})
	defer wsSrv.Close()
	desc := mustDesc(t, "dingtalk")
	a, err := New(t.Context(), &Config{Enabled: true, API: "http://127.0.0.1:1"}, &desc, dingtalkSecrets(),
		newOutbox(t), gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error {
			return errors.New("boom")
		}))
	if err != nil {
		t.Fatal(err)
	}
	ws, resp, derr := websocket.Dial(t.Context(), "ws"+strings.TrimPrefix(wsSrv.URL, "http"), nil)
	closeDialBody(resp)
	if derr != nil {
		t.Fatal(derr)
	}
	defer closeWS(ws)
	if err := a.serve(t.Context(), ws); err == nil || !strings.Contains(err.Error(), "dispatch") {
		t.Fatalf("err = %v, want dispatch error", err)
	}
}

func TestRunReconnectThenCancel(t *testing.T) {
	var opens atomic.Int32
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
			return
		}
		if opens.Add(1) == 3 {
			cancel()
		}
		http.Error(w, "gateway down", http.StatusServiceUnavailable)
	}))
	t.Cleanup(srv.Close)
	a := newDingtalk(t, Config{API: srv.URL, Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond}}, okHandler())
	errCh := make(chan error, 1)
	go func() { errCh <- a.Run(ctx) }()
	select {
	case err := <-errCh:
		if err == nil {
			t.Fatal("cancelled run returned nil")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("run did not stop")
	}
	if got := opens.Load(); got < 2 {
		t.Fatalf("gateway attempts = %d, want retry", got)
	}
}

func TestRunCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	a := newDingtalk(t, Config{API: "http://127.0.0.1:1"}, okHandler())
	if err := a.Run(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestSendChunksAtSizeLimit(t *testing.T) {
	sent := make(chan string, 8)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/oauth2/accessToken") {
			writeBody(w, `{"accessToken":"tok","expireIn":7200}`)
			return
		}
		var body struct {
			MsgParam string `json:"msgParam"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			return
		}
		var param struct {
			Content string `json:"content"`
		}
		if err := json.Unmarshal([]byte(body.MsgParam), &param); err != nil {
			return
		}
		sent <- param.Content
		writeBody(w, `{"processQueryKey":"x"}`)
	}))
	t.Cleanup(srv.Close)
	a := newDingtalk(t, Config{API: srv.URL}, okHandler())
	markdown := strings.Repeat("a", 9000)
	if err := a.Send(t.Context(), "c", markdown); err != nil {
		t.Fatal(err)
	}
	total := 0
	for range 3 {
		part := awaitValue(t, sent)
		if len(part) > gateway.DefaultMaxLen {
			t.Fatalf("part = %d bytes, over cap %d", len(part), gateway.DefaultMaxLen)
		}
		total += len(part)
	}
	if total < len(markdown) {
		t.Fatalf("chunks cover %d < %d", total, len(markdown))
	}
}

type failingCloser struct{}

func (failingCloser) Read([]byte) (int, error) { return 0, io.EOF }
func (failingCloser) Close() error             { return errors.New("close failure") }

func TestCloseDialBody(t *testing.T) {
	closeDialBody(nil)
	closeDialBody(&http.Response{})
	closeDialBody(&http.Response{Body: failingCloser{}})
}
