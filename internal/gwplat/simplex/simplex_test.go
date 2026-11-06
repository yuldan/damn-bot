package simplex

import (
	"bytes"
	"context"
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

func descriptor(t *testing.T) *gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("simplex")
	if err != nil {
		t.Fatal(err)
	}
	return &d
}

func newOutbox(ctx context.Context, t *testing.T) *gwplat.Outbox {
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
	t.Cleanup(sched.Close)
	out, err := gwplat.NewOutbox(descriptor(t), sched)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

type fakeSimplex struct {
	srv     *httptest.Server
	mu      sync.Mutex
	conns   int
	conn    *websocket.Conn
	cmds    []string
	ready   chan struct{}
	pending chan string
}

func newFakeSimplex() *fakeSimplex {
	f := &fakeSimplex{ready: make(chan struct{}, 8), pending: make(chan string, 8)}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(rw, r, nil)
		if err != nil {
			return
		}
		defer func() {
			if cerr := conn.CloseNow(); cerr != nil {
				return
			}
		}()
		f.mu.Lock()
		f.conns++
		f.conn = conn
		f.mu.Unlock()
		for {
			_, data, err := conn.Read(r.Context())
			if err != nil {
				return
			}
			f.mu.Lock()
			f.cmds = append(f.cmds, string(data))
			f.mu.Unlock()
			select {
			case f.ready <- struct{}{}:
			default:
			}
		}
	}))
	return f
}

func (f *fakeSimplex) URL() string { return "ws" + strings.TrimPrefix(f.srv.URL, "http") }

func (f *fakeSimplex) push(item map[string]any) error {
	f.mu.Lock()
	conn := f.conn
	f.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("no connection")
	}
	raw, err := json.Marshal(map[string]any{"corrId": "x", "resp": map[string]any{"type": "newChatItem", "chatItem": item}})
	if err != nil {
		return fmt.Errorf("frame: %w", err)
	}
	if err := conn.Write(context.Background(), websocket.MessageText, raw); err != nil {
		return fmt.Errorf("write frame: %w", err)
	}
	return nil
}

func (f *fakeSimplex) Cmds() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cmds...)
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

func slogNew(b *syncBuffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(b, &slog.HandlerOptions{Level: slog.LevelInfo}))
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

func waitUntil(t *testing.T, cond func() bool, what string) {
	t.Helper()
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-ticker.C:
		case <-deadline.C:
			t.Fatalf("timeout waiting for %s", what)
		}
	}
}

func noDispatch(t *testing.T, ch <-chan *gateway.Inbound) {
	t.Helper()
	select {
	case in := <-ch:
		t.Fatalf("unexpected dispatch %+v", in)
	case <-time.After(100 * time.Millisecond):
	}
}

func chatItem(id, text, contact, group string, mentioned bool) map[string]any {
	chatInfo := map[string]any{}
	if group != "" {
		chatInfo["groupInfo"] = map[string]string{"groupId": group}
	} else {
		chatInfo["contact"] = map[string]string{"contactId": contact}
	}
	return map[string]any{
		"chatInfo": chatInfo,
		"meta":     map[string]string{"itemId": id},
		"content":  map[string]any{"msgContent": map[string]any{"text": text, "mentioned": mentioned}},
	}
}

// TestF7PLAT018Simplex proves WS receive, command send, replay dedupe, and no
// plaintext in INFO logs.
func TestF7PLAT018Simplex(t *testing.T) {
	daemon := newFakeSimplex()
	defer daemon.srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := newOutbox(ctx, t)
	var logs syncBuffer
	logger := slogNew(&logs)
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, err := New(&Config{
		Enabled: true, URL: daemon.URL(),
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond}, Log: logger,
	}, descriptor(t), out, h)
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	waitUntil(t, func() bool { return len(daemon.Cmds()) > 0 }, "subscribe")
	if !strings.Contains(daemon.Cmds()[0], "/subscribe") {
		t.Fatalf("first command = %q", daemon.Cmds()[0])
	}
	if err := daemon.push(chatItem("s1", "secret-plaintext", "alice", "", false)); err != nil {
		t.Fatal(err)
	}
	dm := waitChan(t, handled, "dm")
	checkSimplexDM(t, dm)
	if err := daemon.push(chatItem("s1", "secret-plaintext", "alice", "", false)); err != nil {
		t.Fatal(err)
	}
	noDispatch(t, handled)
	if err := daemon.push(chatItem("s2", "group", "alice", "group-1", true)); err != nil {
		t.Fatal(err)
	}
	checkSimplexGroup(t, waitChan(t, handled, "group"))
	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat: "alice", Markdown: "**pong**",
		Send: func(ctx context.Context, text string) error { return adapter.Publish(ctx, "alice", text) },
	})
	if replyErr != nil {
		t.Fatal(replyErr)
	}
	waitUntil(t, func() bool {
		for _, cmd := range daemon.Cmds() {
			if strings.Contains(cmd, "@alice pong") {
				return true
			}
		}
		return false
	}, "send command")
	if strings.Contains(logs.String(), "secret-plaintext") || strings.Contains(logs.String(), "group") {
		t.Fatal("plaintext leaked into INFO log")
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}

func checkSimplexDM(t *testing.T, dm *gateway.Inbound) {
	t.Helper()
	if dm.ChatID != "alice" || dm.UpdateID != "simplex:s1" || dm.Group {
		t.Fatalf("dm = %+v", dm)
	}
}

func checkSimplexGroup(t *testing.T, group *gateway.Inbound) {
	t.Helper()
	if group.ChatID != "group-1" || !group.Group {
		t.Fatalf("group = %+v", group)
	}
}

// TestSimplexActivationGate is the negative control: disabled config and
// non-loopback daemon URLs open no connection.
func TestSimplexActivationGate(t *testing.T) {
	daemon := newFakeSimplex()
	defer daemon.srv.Close()
	ctx := t.Context()
	out := newOutbox(ctx, t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	if _, err := New(&Config{URL: daemon.URL()}, descriptor(t), out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	if _, err := New(&Config{Enabled: true, URL: "ws://example.com/socket"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("public URL = %v, want ErrConfig", err)
	}
	if _, err := New(&Config{Enabled: true, URL: "wss://127.0.0.1:5225"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("wss URL = %v, want ErrConfig", err)
	}
}
