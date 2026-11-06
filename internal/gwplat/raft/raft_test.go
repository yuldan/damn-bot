package raft

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
	d, err := reg.Lookup("raft")
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

type fakeRaft struct {
	srv    *httptest.Server
	mu     sync.Mutex
	items  []wakeItem
	sent   []string
	wake   chan struct{}
	starts chan struct{}
	reqs   int
}

func newFakeRaft() *fakeRaft {
	f := &fakeRaft{wake: make(chan struct{}, 8), starts: make(chan struct{}, 8)}
	mux := http.NewServeMux()
	mux.HandleFunc("/wake", f.wakeHandler)
	mux.HandleFunc("/send", f.send)
	f.srv = httptest.NewServer(mux)
	return f
}

func (f *fakeRaft) wakeHandler(rw http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.reqs++
	after, err := strconv.Atoi(r.URL.Query().Get("after"))
	if err != nil || after < 0 {
		after = 0
	}
	f.mu.Unlock()
	select {
	case f.starts <- struct{}{}:
	default:
	}
	deadline := time.NewTimer(50 * time.Millisecond)
	defer deadline.Stop()
	for {
		f.mu.Lock()
		if after < len(f.items) {
			items := append([]wakeItem(nil), f.items[after:]...)
			next := len(f.items)
			f.mu.Unlock()
			rw.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(rw).Encode(wakeBatch{Items: items, Next: next}); err != nil {
				return
			}
			return
		}
		f.mu.Unlock()
		select {
		case <-r.Context().Done():
			return
		case <-f.wake:
		case <-deadline.C:
			rw.WriteHeader(http.StatusNoContent)
			return
		}
	}
}

func (f *fakeRaft) send(rw http.ResponseWriter, r *http.Request) {
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

func (f *fakeRaft) push(item wakeItem) {
	f.mu.Lock()
	f.items = append(f.items, item)
	f.mu.Unlock()
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *fakeRaft) Sent() []string {
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

// TestF7PLAT016Raft proves long-poll receive, replay dedupe, outbound publish,
// and the render/outq seam.
func TestF7PLAT016Raft(t *testing.T) {
	server := newFakeRaft()
	defer server.srv.Close()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	out := newOutbox(ctx, t)
	handled := make(chan *gateway.Inbound, 8)
	h := gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		handled <- in
		return nil
	})
	adapter, err := New(&Config{
		Enabled: true, BaseURL: server.srv.URL, Profile: "workspace",
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond},
	}, descriptor(t), out, h)
	if err != nil {
		t.Fatal(err)
	}
	runErr := make(chan error, 1)
	go func() { runErr <- adapter.Run(ctx) }()
	waitChan(t, server.starts, "first wake")
	server.push(wakeItem{ID: "r1", Chat: "chan", Sender: "alice", Text: "hello", Group: true, Mentioned: true})
	if in := waitChan(t, handled, "first item"); in.UpdateID != "raft:r1" || in.ChatID != "chan" || !in.Group {
		t.Fatalf("inbound = %+v", in)
	}
	server.push(wakeItem{ID: "r1", Chat: "chan", Sender: "alice", Text: "hello", Group: true, Mentioned: true})
	noDispatch(t, handled)
	server.push(wakeItem{ID: "r2", Chat: "chan", Sender: "alice", Text: "second"})
	if in := waitChan(t, handled, "second item"); in.UpdateID != "raft:r2" {
		t.Fatalf("second = %+v", in)
	}
	replyErr := out.Reply(ctx, gwplat.Reply{
		Chat: "chan", Markdown: "**pong**",
		Send: func(ctx context.Context, text string) error { return adapter.Publish(ctx, "chan", text) },
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
	if !strings.Contains(sent, `"text":"pong"`) || !strings.Contains(sent, `"profile":"workspace"`) {
		t.Fatalf("published = %q", sent)
	}
	cancel()
	if stopErr := waitChan(t, runErr, "adapter stop"); stopErr != nil && !errors.Is(stopErr, context.Canceled) {
		t.Fatalf("run: %v", stopErr)
	}
}

// TestRaftBridgeAbsent is the negative control: a missing local bridge yields
// module_unavailable with installation guidance and a non-loopback URL is
// refused.
func TestRaftBridgeAbsent(t *testing.T) {
	ctx := t.Context()
	out := newOutbox(ctx, t)
	h := gwplat.HandlerFunc(func(context.Context, *gateway.Inbound) error { return nil })
	adapter, err := New(&Config{
		Enabled: true, BaseURL: "http://127.0.0.1:1", Profile: "workspace",
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: 10 * time.Millisecond},
	}, descriptor(t), out, h)
	if err != nil {
		t.Fatal(err)
	}
	err = adapter.Run(ctx)
	if !errors.Is(err, ErrBridgeAbsent) || !errors.Is(err, gwplat.ErrUnavailable) || !strings.Contains(err.Error(), "install") {
		t.Fatalf("absent bridge = %v, want ErrBridgeAbsent module_unavailable with install message", err)
	}
	if _, err := New(&Config{Enabled: true, BaseURL: "http://example.com", Profile: "p"}, descriptor(t), out, h); !errors.Is(err, ErrConfig) {
		t.Fatalf("public URL = %v, want ErrConfig", err)
	}
	if _, err := New(&Config{BaseURL: "http://127.0.0.1:1", Profile: "p"}, descriptor(t), out, h); !errors.Is(err, gwplat.ErrUnavailable) {
		t.Fatalf("disabled = %v, want ErrUnavailable", err)
	}
}
