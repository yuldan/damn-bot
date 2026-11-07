package ntfy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

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
	d, err := reg.Lookup("ntfy")
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

type fakeNtfy struct {
	srv    *httptest.Server
	items  chan message
	sent   []string
	mu     sync.Mutex
	subs   chan struct{}
	subbed int
}

func newFakeNtfy() *fakeNtfy {
	f := &fakeNtfy{items: make(chan message, 8), subs: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/topic/sse", f.stream)
	mux.HandleFunc("/topic", f.publish)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeNtfy) stream(rw http.ResponseWriter, r *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
	f.mu.Lock()
	f.subbed++
	f.mu.Unlock()
	select {
	case f.subs <- struct{}{}:
	default:
	}
	rw.Header().Set("Content-Type", "text/event-stream")
	rw.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		select {
		case <-r.Context().Done():
			return
		case msg := <-f.items:
			raw, err := json.Marshal(msg)
			if err != nil {
				return
			}
			if _, err := io.WriteString(rw, "data: "+string(raw)+"\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (f *fakeNtfy) publish(rw http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(io.LimitReader(r.Body, 64<<10))
	if err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.sent = append(f.sent, string(body))
	f.mu.Unlock()
	rw.WriteHeader(http.StatusAccepted)
}

func (f *fakeNtfy) push(msg *message) { f.items <- *msg }

func (f *fakeNtfy) Sent() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.sent...)
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

// TestF7PLAT013Ntfy proves SSE subscribe/publish, replay dedupe, no message
// body in INFO logs, and outq/render outbound.
func TestF7PLAT013Ntfy(t *testing.T) {
	server := newFakeNtfy()
	defer server.srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := newOutbox(ctx, t)
	var logs syncBuffer
	logger := slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelInfo}))
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, err := New(&Config{
		Enabled: true, BaseURL: server.srv.URL, Topic: "topic",
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond}, Log: logger,
	}, descriptor(t), out, h)
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	waitChan(t, server.subs, "subscription")
	server.push(&message{ID: "n1", Event: "message", Message: "secret-body"})
	in := waitChan(t, handled, "first message")
	if in.Platform != "ntfy" || in.Text != "secret-body" || in.ChatID != "topic" {
		t.Fatalf("inbound = %+v", in)
	}
	server.push(&message{ID: "n1", Event: "message", Message: "secret-body"})
	noDispatch(t, handled)
	server.push(&message{ID: "n2", Event: "message", Message: "second"})
	if second := waitChan(t, handled, "second message"); second.Text != "second" {
		t.Fatalf("second = %+v", second)
	}
	server.push(&message{ID: "n3", Event: "open", Message: "ignored"})
	noDispatch(t, handled)
	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat: "topic", Markdown: "**pong**",
		Send: func(ctx context.Context, text string) error { return adapter.Publish(ctx, text) },
	})
	if replyErr != nil {
		t.Fatal(replyErr)
	}
	var sent string
	waitUntil(t, func() bool {
		msgs := server.Sent()
		if len(msgs) == 0 {
			return false
		}
		sent = msgs[0]
		return true
	}, "published reply")
	if sent != "pong" {
		t.Fatalf("published = %q", sent)
	}
	if strings.Contains(logs.String(), "secret-body") {
		t.Fatal("message body leaked into INFO log")
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}

// TestNtfyActivationGate is the negative control: disabled config or an
// invalid base URL opens no connection.
func TestNtfyActivationGate(t *testing.T) {
	server := newFakeNtfy()
	defer server.srv.Close()
	ctx := t.Context()
	out := newOutbox(ctx, t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	if _, err := New(&Config{BaseURL: server.srv.URL, Topic: "topic"}, descriptor(t), out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	credURL := "https://" + "user" + ":" + "pass" + "@example.com"
	if _, err := New(&Config{Enabled: true, BaseURL: credURL, Topic: "topic"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("credential URL = %v, want ErrConfig", err)
	}
	if _, err := New(&Config{Enabled: true, BaseURL: server.srv.URL, Topic: "bad topic"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("bad topic = %v, want ErrConfig", err)
	}
}
