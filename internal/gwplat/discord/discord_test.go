package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/outq"
	"github.com/damnbot/damnbot/internal/gwplat"
)

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	value, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return value, nil
}

type recorder struct {
	mu   sync.Mutex
	ins  []gateway.Inbound
	seen chan gateway.Inbound
}

func newRecorder() *recorder { return &recorder{seen: make(chan gateway.Inbound, 16)} }

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	r.ins = append(r.ins, *in)
	r.mu.Unlock()
	r.seen <- *in
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.ins)
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return raw
}

// fakeGateway speaks the Discord gateway handshake and replays scripted events.
type fakeGateway struct {
	t         *testing.T
	srv       *httptest.Server
	events    chan []byte
	identify  chan map[string]any
	closeWith websocket.StatusCode
	connects  atomic.Int64
	stop      chan struct{}
}

func newFakeGateway(t *testing.T, closeWith websocket.StatusCode) *fakeGateway {
	t.Helper()
	g := &fakeGateway{t: t, events: make(chan []byte, 16), identify: make(chan map[string]any, 4), closeWith: closeWith, stop: make(chan struct{})}
	g.srv = httptest.NewServer(http.HandlerFunc(g.serve))
	t.Cleanup(func() {
		close(g.stop)
		g.srv.Close()
	})
	return g
}

func (g *fakeGateway) serve(rw http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(rw, r, nil)
	if err != nil {
		return
	}
	g.connects.Add(1)
	defer func() {
		if cerr := conn.CloseNow(); cerr != nil {
			return
		}
	}()
	if !g.handshake(r.Context(), conn) {
		return
	}
	g.replay(r.Context(), conn)
}

// handshake sends HELLO, records IDENTIFY and sends READY.
func (g *fakeGateway) handshake(ctx context.Context, conn *websocket.Conn) bool {
	hello := mustJSON(g.t, map[string]any{"op": opHello, "d": map[string]any{"heartbeat_interval": 60000}})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		return false
	}
	_, raw, err := conn.Read(ctx)
	if err != nil {
		return false
	}
	var frame struct {
		Op int            `json:"op"`
		D  map[string]any `json:"d"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		return false
	}
	g.identify <- frame.D
	ready := mustJSON(g.t, map[string]any{
		"op": opDispatch, "t": "READY", "s": 1,
		"d": map[string]any{"user": map[string]any{"id": "bot-1"}, "session_id": "s1"},
	})
	return conn.Write(ctx, websocket.MessageText, ready) == nil
}

func (g *fakeGateway) replay(ctx context.Context, conn *websocket.Conn) {
	if g.closeWith != 0 {
		if err := conn.Close(g.closeWith, "auth failed"); err != nil {
			return
		}
		return
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-g.stop:
			return
		case event := <-g.events:
			if err := conn.Write(ctx, websocket.MessageText, event); err != nil {
				return
			}
		}
	}
}

func (g *fakeGateway) url() string { return "ws" + strings.TrimPrefix(g.srv.URL, "http") }

type fakeREST struct {
	srv  *httptest.Server
	mu   sync.Mutex
	auth []string
	body []map[string]any
}

func newFakeREST(t *testing.T) *fakeREST {
	t.Helper()
	f := &fakeREST{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		payload := map[string]any{}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.auth = append(f.auth, r.Header.Get("Authorization"))
		f.body = append(f.body, payload)
		f.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if _, err := rw.Write([]byte(`{"id":"m1"}`)); err != nil {
			return
		}
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fakeREST) sent() (int, string, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.body) == 0 {
		return 0, "", ""
	}
	text, ok := f.body[0]["content"].(string)
	if !ok {
		text = ""
	}
	return len(f.body), f.auth[0], text
}

func newScheduler(t *testing.T) *outq.Scheduler {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	return sched
}

func buildAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, *gwplat.Descriptor, error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return nil, nil, fmt.Errorf("registry: %w", err)
	}
	desc, err := reg.Lookup("discord")
	if err != nil {
		return nil, nil, fmt.Errorf("lookup: %w", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		return nil, nil, fmt.Errorf("outbox: %w", err)
	}
	ad, err := New(t.Context(), cfg, &desc, src, out, h)
	if err != nil {
		return nil, &desc, fmt.Errorf("new: %w", err)
	}
	return ad, &desc, nil
}

func newAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, *gwplat.Descriptor) {
	t.Helper()
	ad, desc, err := buildAdapter(t, cfg, src, h, sched)
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	return ad, desc
}

func event(t *testing.T, typ string, seq int, payload any) []byte {
	t.Helper()
	return mustJSON(t, map[string]any{"op": opDispatch, "t": typ, "s": seq, "d": payload})
}

// TestF7PLAT005Discord covers the PLAT-005 row end to end against a fake
// gateway and REST server.
func TestF7PLAT005Discord(t *testing.T) {
	t.Run("activation_gate", testDiscordActivation)
	t.Run("inbound_mapping", testDiscordInbound)
	t.Run("close_4004_needs_attention", testDiscordClose)
}

func testDiscordActivation(t *testing.T) {
	t.Helper()
	gw := newFakeGateway(t, 0)
	cfg := &Config{Enabled: true, GatewayURL: gw.url(), BaseURL: gw.srv.URL}
	if _, _, err := buildAdapter(t, cfg, secrets{}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if got := gw.connects.Load(); got != 0 {
		t.Fatalf("missing secret opened %d gateway connections", got)
	}
	disabled := &Config{Enabled: false}
	if _, _, err := buildAdapter(t, disabled, secrets{"discord_bot_token": "tok"}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled adapter = %v, want module_unavailable", err)
	}
}

func testDiscordInbound(t *testing.T) {
	t.Helper()
	gw := newFakeGateway(t, 0)
	rest := newFakeREST(t)
	rec := newRecorder()
	ad, _ := newAdapter(t, &Config{Enabled: true, GatewayURL: gw.url(), BaseURL: rest.srv.URL}, secrets{"discord_bot_token": "tok"}, rec, newScheduler(t))
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := ad.Run(ctx); err != nil {
			t.Errorf("run: %v", err)
		}
	}()
	defer func() {
		cancel()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("Run did not stop")
		}
	}()
	scriptDiscordEvents(t, gw)
	assertDiscordInbounds(t, rec)
	assertDiscordSend(t, ad, rest)
}

func scriptDiscordEvents(t *testing.T, gw *fakeGateway) {
	t.Helper()
	gw.events <- event(t, "GUILD_CREATE", 2, map[string]any{
		"id": "guild-1",
		"channels": []map[string]any{
			{"id": "chan-1", "type": 0},
			{"id": "thread-1", "type": 11, "parent_id": "chan-1"},
		},
	})
	gw.events <- event(t, "MESSAGE_CREATE", 3, map[string]any{
		"id": "m-dm", "channel_id": "dm-1", "content": "hello", "author": map[string]any{"id": "user-1"},
	})
	gw.events <- event(t, "MESSAGE_CREATE", 4, map[string]any{
		"id": "m-group", "channel_id": "chan-1", "guild_id": "guild-1",
		"content": "no mention", "author": map[string]any{"id": "user-1"},
	})
	gw.events <- event(t, "MESSAGE_CREATE", 5, map[string]any{
		"id": "m-thread", "channel_id": "thread-1", "guild_id": "guild-1",
		"content": "<@bot-1> ping", "author": map[string]any{"id": "user-2"},
		"mentions": []map[string]any{{"id": "bot-1"}},
	})
	// A repeated event id is one turn; a bot author is never a turn.
	gw.events <- event(t, "MESSAGE_CREATE", 6, map[string]any{
		"id": "m-dm", "channel_id": "dm-1", "content": "hello", "author": map[string]any{"id": "user-1"},
	})
	gw.events <- event(t, "MESSAGE_CREATE", 7, map[string]any{
		"id": "m-bot", "channel_id": "dm-1", "content": "echo", "author": map[string]any{"id": "bot-2", "bot": true},
	})
	gw.events <- event(t, "INTERACTION_CREATE", 8, map[string]any{
		"id": "i-1", "type": 2, "channel_id": "chan-1", "guild_id": "guild-1",
		"member": map[string]any{"user": map[string]any{"id": "user-3"}},
		"data":   map[string]any{"name": "voice", "options": []map[string]any{{"value": "on"}}},
	})
}

func assertDiscordInbounds(t *testing.T, rec *recorder) {
	t.Helper()
	want := []struct {
		update, chat, thread, sender, text string
		group, mentioned, command          bool
	}{
		{"discord:m-dm", "dm-1", "", "user-1", "hello", false, false, false},
		{"discord:m-thread", "chan-1", "thread-1", "user-2", "<@bot-1> ping", true, true, false},
		{"discord:i-1", "chan-1", "", "user-3", "/voice on", true, true, true},
	}
	for _, w := range want {
		select {
		case got := <-rec.seen:
			if got.UpdateID != w.update || got.ChatID != w.chat || got.ThreadID != w.thread ||
				got.SenderID != w.sender || got.Text != w.text || got.Group != w.group ||
				got.Mentioned != w.mentioned || got.Command != w.command {
				t.Fatalf("inbound = %+v, want %+v", got, w)
			}
			if got.Platform != "discord" || got.Account != "bot-1" {
				t.Fatalf("platform/account = %s/%s", got.Platform, got.Account)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("no inbound for %s", w.update)
		}
	}
	if got := rec.count(); got != len(want) {
		t.Fatalf("turns = %d, want %d (replay/bot/mention gates)", got, len(want))
	}
}

func assertDiscordSend(t *testing.T, ad *Adapter, rest *fakeREST) {
	t.Helper()
	if err := ad.Send(t.Context(), "dm-1", "hello **world**"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for n, _, _ := rest.sent(); n == 0 && time.Now().Before(deadline); n, _, _ = rest.sent() {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	n, auth, text := rest.sent()
	if n != 1 || auth != "Bot tok" || !strings.Contains(text, "**world**") {
		t.Fatalf("REST send = n=%d auth=%q text=%q", n, auth, text)
	}
}

func testDiscordClose(t *testing.T) {
	t.Helper()
	gw := newFakeGateway(t, closeAuthFailed)
	ad, desc := newAdapter(t, &Config{Enabled: true, GatewayURL: gw.url()}, secrets{"discord_bot_token": "tok"}, newRecorder(), newScheduler(t))
	if desc.Name != "discord" || !desc.Caps.SupportsEditStream() {
		t.Fatalf("descriptor = %+v", desc)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := ad.Run(ctx)
	if !errors.Is(err, ErrNeedsAttention) || !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("4004 = %v, want needs_attention + module_unavailable", err)
	}
	if !strings.Contains(err.Error(), "discord_auth_failed") {
		t.Fatalf("4004 reason = %v", err)
	}
	if gw.connects.Load() != 1 {
		t.Fatalf("permanent close retried %d times", gw.connects.Load())
	}
}

// TestF7DiscordIntents locks the exact intent bitmask: the privileged
// MESSAGE_CONTENT bit is never implicit.
func TestF7DiscordIntents(t *testing.T) {
	gw := newFakeGateway(t, closeAuthFailed)
	ad, _ := newAdapter(t, &Config{Enabled: true, GatewayURL: gw.url()}, secrets{"discord_bot_token": "tok"}, newRecorder(), newScheduler(t))
	want := IntentGuilds | IntentGuildMessages | IntentDirectMessages
	if got := ad.Intents(); got != want {
		t.Fatalf("minimal intents = %d, want %d", got, want)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := ad.Run(ctx); !errors.Is(err, ErrNeedsAttention) {
		t.Fatalf("run = %v, want needs_attention", err)
	}
	select {
	case identify := <-gw.identify:
		intents, ok := identify["intents"].(float64)
		if !ok || int(intents) != want {
			t.Fatalf("identify intents = %v, want %d", identify["intents"], want)
		}
		if _, ok := identify["token"]; !ok {
			t.Fatal("identify carried no token")
		}
	default:
		t.Fatal("adapter never identified")
	}

	privileged, _ := newAdapter(t, &Config{Enabled: true, GatewayURL: gw.url(), MessageContent: true, Intents: IntentGuilds}, secrets{"discord_bot_token": "tok"}, newRecorder(), newScheduler(t))
	if got := privileged.Intents(); got != want|IntentMessageContent {
		t.Fatalf("content intents = %d, want %d", got, want|IntentMessageContent)
	}
}
