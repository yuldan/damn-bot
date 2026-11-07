package signal

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	d, err := reg.Lookup("signal")
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
		return n, fmtError(err)
	}
	return n, nil
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func fmtError(err error) error { return errors.Join(errors.New("buffer"), err) }

type fakeDaemon struct {
	srv    *httptest.Server
	events chan string
	mu     sync.Mutex
	sent   []rpcRequest
	subs   chan struct{}
}

func newFakeDaemon() *fakeDaemon {
	f := &fakeDaemon{events: make(chan string, 8), subs: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/events", f.stream)
	mux.HandleFunc("/api/v1/rpc", f.rpc)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeDaemon) stream(rw http.ResponseWriter, r *http.Request) {
	flusher, ok := rw.(http.Flusher)
	if !ok {
		rw.WriteHeader(http.StatusInternalServerError)
		return
	}
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
		case data := <-f.events:
			if _, err := io.WriteString(rw, "data: "+data+"\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

func (f *fakeDaemon) rpc(rw http.ResponseWriter, r *http.Request) {
	var req rpcRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		rw.WriteHeader(http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	f.sent = append(f.sent, req)
	f.mu.Unlock()
	if _, err := io.WriteString(rw, `{"jsonrpc":"2.0","id":"x","result":{}}`); err != nil {
		return
	}
}

func (f *fakeDaemon) push(data string) { f.events <- data }

func (f *fakeDaemon) Sent() []rpcRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]rpcRequest(nil), f.sent...)
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

func notification(source string, timestamp int64, text, group string) string {
	env := map[string]any{"source": source, "timestamp": timestamp}
	data := map[string]any{"message": text, "mentioned": group != ""}
	if group != "" {
		data["groupInfo"] = map[string]string{"groupId": group}
	}
	env["dataMessage"] = data
	raw, err := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "method": "receive",
		"params": map[string]any{"envelope": env},
	})
	if err != nil {
		return ""
	}
	return string(raw)
}

// TestF7PLAT017Signal proves SSE receive, JSON-RPC send, replay dedupe, group
// mapping, and that plaintext never reaches INFO logs.
func TestF7PLAT017Signal(t *testing.T) {
	daemon := newFakeDaemon()
	defer daemon.srv.Close()
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
		Enabled: true, BaseURL: daemon.srv.URL, Account: "+15550001",
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond}, Log: logger,
	}, descriptor(t), out, h)
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	waitChan(t, daemon.subs, "event stream")
	msg := notification("+15550002", 1_700_000_000_000, "secret-plaintext", "")
	daemon.push(msg)
	dm := waitChan(t, handled, "dm")
	checkSignalDM(t, dm)
	daemon.push(msg)
	noDispatch(t, handled)
	daemon.push(notification("+15550002", 1_700_000_000_001, "group-hi", "group-1"))
	group := waitChan(t, handled, "group")
	checkSignalGroup(t, group)
	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat: "+15550002", Markdown: "**pong**",
		Send: func(ctx context.Context, text string) error { return adapter.Publish(ctx, "+15550002", text) },
	})
	if replyErr != nil {
		t.Fatal(replyErr)
	}
	waitUntil(t, func() bool { return len(daemon.Sent()) > 0 }, "rpc send")
	sent := daemon.Sent()[0]
	checkSignalRPC(t, sent)
	if strings.Contains(logs.String(), "secret-plaintext") || strings.Contains(logs.String(), "group-hi") {
		t.Fatal("plaintext leaked into INFO log")
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}

func checkSignalDM(t *testing.T, dm *gateway.Inbound) {
	t.Helper()
	if dm.ChatID != "+15550002" || dm.SenderID != "+15550002" || dm.Group || dm.UpdateID != "signal:+15550002|1700000000000|+15550002" {
		t.Fatalf("dm = %+v", dm)
	}
}

func checkSignalGroup(t *testing.T, group *gateway.Inbound) {
	t.Helper()
	if group.ChatID != "group-1" || !group.Group {
		t.Fatalf("group = %+v", group)
	}
}

func checkSignalRPC(t *testing.T, sent rpcRequest) {
	t.Helper()
	params, ok := sent.Params.(map[string]any)
	if !ok || params["message"] != "pong" || params["account"] != "+15550001" {
		t.Fatalf("rpc params = %#v", sent.Params)
	}
}

// TestSignalActivationGate is the negative control: disabled or public daemon
// URLs never connect.
func TestSignalActivationGate(t *testing.T) {
	daemon := newFakeDaemon()
	defer daemon.srv.Close()
	ctx := t.Context()
	out := newOutbox(ctx, t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	if _, err := New(&Config{BaseURL: daemon.srv.URL, Account: "+1"}, descriptor(t), out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
	if _, err := New(&Config{Enabled: true, BaseURL: "http://example.com", Account: "+1"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("public URL = %v, want ErrConfig", err)
	}
	if _, err := New(&Config{Enabled: true, BaseURL: daemon.srv.URL}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("missing account = %v, want ErrConfig", err)
	}
}
