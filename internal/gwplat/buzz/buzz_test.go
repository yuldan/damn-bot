package buzz

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n, err := s.b.Write(p)
	if err != nil {
		return n, fmt.Errorf("buffer: %w", err)
	}
	return n, nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

// fakeWS is the local WebSocket fake of this test; the shared contract package
// stays free of protocol servers.
type fakeWS struct {
	srv     *httptest.Server
	mu      sync.Mutex
	conns   int
	handler func(ctx context.Context, conn *websocket.Conn) error
}

func newFakeWS(handler func(ctx context.Context, conn *websocket.Conn) error) *fakeWS {
	w := &fakeWS{handler: handler}
	w.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		w.mu.Lock()
		w.conns++
		w.mu.Unlock()
		defer func() {
			if cerr := conn.CloseNow(); cerr != nil {
				return
			}
		}()
		if err := w.handler(r.Context(), conn); err != nil {
			if cerr := conn.Close(websocket.StatusInternalError, ""); cerr != nil {
				return
			}
		}
	}))
	return w
}

func (w *fakeWS) URL() string { return "ws" + strings.TrimPrefix(w.srv.URL, "http") }
func (w *fakeWS) Close()      { w.srv.Close() }
func (w *fakeWS) Conns() int {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.conns
}

type fakeRelay struct {
	srv    *fakeWS
	conns  chan *websocket.Conn
	reqs   chan string
	events chan string
}

func newFakeRelay() *fakeRelay {
	fr := &fakeRelay{
		conns:  make(chan *websocket.Conn, 8),
		reqs:   make(chan string, 8),
		events: make(chan string, 8),
	}
	fr.srv = newFakeWS(func(ctx context.Context, conn *websocket.Conn) error {
		fr.conns <- conn
		for {
			_, data, err := conn.Read(ctx)
			if err != nil {
				return fmt.Errorf("fake relay read: %w", err)
			}
			var frame []json.RawMessage
			if err := json.Unmarshal(data, &frame); err != nil || len(frame) == 0 {
				continue
			}
			var kind string
			if err := json.Unmarshal(frame[0], &kind); err != nil {
				continue
			}
			switch kind {
			case "REQ":
				fr.reqs <- string(data)
			case "EVENT":
				fr.events <- string(data)
			}
		}
	})
	return fr
}

func waitChan[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(5 * time.Second):
		t.Fatalf("timeout waiting for %s", what)
	}
	var zero T
	return zero
}

func noDispatch(t *testing.T, ch <-chan *gateway.Inbound) {
	t.Helper()
	select {
	case in := <-ch:
		t.Fatalf("unexpected dispatch %+v", in)
	case <-time.After(100 * time.Millisecond):
	}
}

func descriptor(t *testing.T, name string) *gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup(name)
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func newOutbox(ctx context.Context, t *testing.T, d *gwplat.Descriptor) *gwplat.Outbox {
	t.Helper()
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}, Workers: 1})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		if runErr := sched.Run(ctx); runErr != nil && !errors.Is(runErr, context.Canceled) {
			t.Errorf("scheduler: %v", runErr)
		}
	}()
	out, err := gwplat.NewOutbox(d, sched)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sched.Close)
	return out
}

func mustHex(t *testing.T, s string) []byte {
	t.Helper()
	raw, err := hex.DecodeString(s)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func remoteEvent(t *testing.T, privHex, text, botPub string, tags [][]string) event {
	t.Helper()
	priv, err := parseKey(privHex)
	if err != nil {
		t.Fatal(err)
	}
	if tags == nil {
		tags = [][]string{{"p", botPub}}
	}
	ev, err := signEvent(priv, kindNote, text, tags, time.Unix(1_700_000_000, 0), make([]byte, 32))
	if err != nil {
		t.Fatal(err)
	}
	return ev
}

func pushEvent(ctx context.Context, t *testing.T, conn *websocket.Conn, ev *event) {
	t.Helper()
	frame, err := json.Marshal([]any{"EVENT", "damnbot", ev})
	if err != nil {
		t.Fatal(err)
	}
	if err := conn.Write(ctx, websocket.MessageText, frame); err != nil {
		t.Fatal(err)
	}
}

// TestF7PLAT003Buzz proves the Buzz relay adapter: subscription/publish over
// WS, event-id replay dedupe surviving a reconnect, no key in the log, and the
// outq/render outbound seam.
func TestF7PLAT003Buzz(t *testing.T) {
	relay := newFakeRelay()
	defer relay.srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	d := descriptor(t, "buzz")
	out := newOutbox(ctx, t, d)
	var logBuf syncBuffer
	logger := slog.New(slog.NewTextHandler(&logBuf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	key := strings.Repeat("11", 32)
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, err := New(ctx, &Config{
		Enabled:  true,
		RelayURL: relay.srv.URL(),
		Backoff:  gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond},
		Log:      logger,
	}, d, secrets{"buzz_private_key": key}, out, h)
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	conn := waitChan(t, relay.conns, "first relay connection")
	req := waitChan(t, relay.reqs, "subscription")
	if !strings.Contains(req, `"REQ"`) || !strings.Contains(req, adapter.pub) {
		t.Fatalf("subscription = %s", req)
	}
	remotePub := verifyBuzzInbound(ctx, t, relay, adapter, handled, conn)

	// Publish through outq: the relay receives one signed EVENT frame whose id
	// and signature are valid and whose content is plain-rendered markdown.
	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat:     remotePub,
		Markdown: "**bold** reply",
		Send:     func(ctx context.Context, text string) error { return adapter.Publish(ctx, remotePub, false, text) },
	})
	if replyErr != nil {
		t.Fatal(replyErr)
	}
	frame := waitChan(t, relay.events, "published event")
	var parts []json.RawMessage
	if err := json.Unmarshal([]byte(frame), &parts); err != nil || len(parts) != 2 {
		t.Fatalf("published frame = %s", frame)
	}
	var published event
	if err := json.Unmarshal(parts[1], &published); err != nil {
		t.Fatal(err)
	}
	id, idErr := eventID(&published)
	if idErr != nil || id != published.ID {
		t.Fatalf("published id = %s/%q", id, published.ID)
	}
	var pub [32]byte
	pubBytes := mustHex(t, adapter.pub)
	copy(pub[:], pubBytes)
	var sig [64]byte
	sigBytes := mustHex(t, published.Sig)
	copy(sig[:], sigBytes)
	msg := sha256.Sum256([]byte(published.ID))
	if !verifySchnorr(pub, msg, sig) {
		t.Fatal("published signature is invalid")
	}
	if strings.Contains(published.Content, "**") {
		t.Fatalf("plain renderer leaked markup: %q", published.Content)
	}
	if strings.Contains(logBuf.String(), key) {
		t.Fatal("private key leaked into the log")
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}

func verifyBuzzInbound(ctx context.Context, t *testing.T, relay *fakeRelay, adapter *Adapter, handled chan *gateway.Inbound, conn *websocket.Conn) string {
	t.Helper()
	remoteKey := strings.Repeat("22", 32)
	remotePriv, err := parseKey(remoteKey)
	if err != nil {
		t.Fatal(err)
	}
	remotePubKey := publicKey(remotePriv)
	remotePub := hex.EncodeToString(remotePubKey[:])
	ev := remoteEvent(t, remoteKey, "hello bot", adapter.pub, nil)
	pushEvent(ctx, t, conn, &ev)
	in := waitChan(t, handled, "first dispatch")
	if in.Platform != "buzz" || in.SenderID != remotePub || in.ChatID != remotePub || in.Text != "hello bot" {
		t.Fatalf("inbound = %+v", in)
	}
	if !in.Mentioned {
		t.Fatal("p-tag mention not recorded")
	}
	pushEvent(ctx, t, conn, &ev)
	noDispatch(t, handled)
	// Drop and reconnect: the replay window survives, so the same event after
	// reconnect never becomes a second turn.
	if err := conn.Close(websocket.StatusNormalClosure, "test"); err != nil {
		t.Fatal(err)
	}
	conn2 := waitChan(t, relay.conns, "reconnect")
	waitChan(t, relay.reqs, "resubscription")
	pushEvent(ctx, t, conn2, &ev)
	noDispatch(t, handled)
	ev2 := remoteEvent(t, remoteKey, "second", adapter.pub, nil)
	pushEvent(ctx, t, conn2, &ev2)
	second := waitChan(t, handled, "second dispatch")
	if second.Text != "second" {
		t.Fatalf("second inbound = %+v", second)
	}
	return remotePub
}

// TestBuzzActivationGate is the negative control: disabled config or a missing
// secret never dials, and an invalid key never echoes the secret.
func TestBuzzActivationGate(t *testing.T) {
	relay := newFakeRelay()
	defer relay.srv.Close()
	ctx := t.Context()
	d := descriptor(t, "buzz")
	out := newOutbox(ctx, t, d)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	key := strings.Repeat("33", 32)
	if _, err := New(ctx, &Config{RelayURL: relay.srv.URL()}, d, secrets{"buzz_private_key": key}, out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, RelayURL: relay.srv.URL()}, d, nil, out, h); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v, want ErrMissingSecret", err)
	}
	if _, err := New(ctx, &Config{Enabled: true, RelayURL: relay.srv.URL()}, d, secrets{"buzz_private_key": key + "zz"}, out, h); !errors.Is(err, ErrKey) {
		t.Fatalf("bad key = %v, want ErrKey", err)
	}
	if got := relay.srv.Conns(); got != 0 {
		t.Fatalf("activation gate dialed %d times", got)
	}
	if strings.Contains(errText(t, func() error {
		_, err := New(ctx, &Config{Enabled: true, RelayURL: relay.srv.URL()}, d, secrets{"buzz_private_key": key + "zz"}, out, h)
		return err
	}), key) {
		t.Fatal("invalid key error leaked the secret")
	}
	if _, err := New(ctx, &Config{Enabled: true, RelayURL: "ws://example.com/relay"}, d, secrets{"buzz_private_key": key}, out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("public ws relay = %v, want ErrConfig", err)
	}
}

func errText(t *testing.T, fn func() error) string {
	t.Helper()
	err := fn()
	if err == nil {
		t.Fatal("expected an error")
	}
	return err.Error()
}

// TestBuzzPublishWithoutConnection proves the outbox send path fails typed
// before the relay is connected instead of writing nowhere.
func TestBuzzPublishWithoutConnection(t *testing.T) {
	ctx := t.Context()
	d := descriptor(t, "buzz")
	out := newOutbox(ctx, t, d)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	key := strings.Repeat("44", 32)
	adapter, err := New(ctx, &Config{Enabled: true, RelayURL: "ws://127.0.0.1:1", Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond}}, d, secrets{"buzz_private_key": key}, out, h)
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.Publish(ctx, "peer", false, "hello"); !errors.Is(err, ErrNoConn) {
		t.Fatalf("publish without connection = %v, want ErrNoConn", err)
	}
}
