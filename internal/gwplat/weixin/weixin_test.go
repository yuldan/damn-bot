package weixin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	in   []gateway.Inbound
	seen chan gateway.Inbound
}

func newRecorder() *recorder { return &recorder{seen: make(chan gateway.Inbound, 8)} }

func (r *recorder) Handle(_ context.Context, in *gateway.Inbound) error {
	r.mu.Lock()
	r.in = append(r.in, *in)
	r.mu.Unlock()
	r.seen <- *in
	return nil
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.in)
}

type memCursor struct {
	mu    sync.Mutex
	c     string
	found bool
	saves int
}

func (m *memCursor) Load(context.Context) (string, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c, m.found, nil
}

func (m *memCursor) Save(_ context.Context, cursor string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.c, m.found = cursor, true
	m.saves++
	return nil
}

func (m *memCursor) get() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.c
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

type fakeILink struct {
	srv      *httptest.Server
	mu       sync.Mutex
	cursors  []string
	requests int
	sent     []map[string]string
}

func newFakeILink(t *testing.T) *fakeILink {
	t.Helper()
	f := &fakeILink{}
	f.srv = httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.requests++
		f.mu.Unlock()
		rw.Header().Set("Content-Type", "application/json")
		if strings.Contains(r.URL.Path, "getupdates") {
			cursor := r.URL.Query().Get("cursor")
			f.mu.Lock()
			f.cursors = append(f.cursors, cursor)
			f.mu.Unlock()
			if cursor == "" {
				writeJSON(t, rw, `{"next_cursor":"c1","messages":[{"msg_id":"w1","chat_id":"chat1","from":"u1","text":"hi"},{"msg_id":"w1","chat_id":"chat1","from":"u1","text":"hi"}]}`)
				return
			}
			writeJSON(t, rw, `{"next_cursor":"c2","messages":[]}`)
			return
		}
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(rw, err.Error(), http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		f.sent = append(f.sent, payload)
		f.mu.Unlock()
		writeJSON(t, rw, `{"ok":true}`)
	}))
	t.Cleanup(f.srv.Close)
	return f
}

func writeJSON(t *testing.T, rw http.ResponseWriter, body string) {
	t.Helper()
	if _, err := rw.Write([]byte(body)); err != nil {
		t.Errorf("write: %v", err)
	}
}

func (f *fakeILink) snapshot() (cursors []string, requests int, sent []map[string]string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.cursors...), f.requests, append([]map[string]string(nil), f.sent...)
}

func buildAdapter(t *testing.T, cfg *Config, src gwplat.SecretSource, h gwplat.Handler, sched *outq.Scheduler) (*Adapter, error) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		return nil, fmt.Errorf("registry: %w", err)
	}
	desc, err := reg.Lookup("weixin")
	if err != nil {
		return nil, fmt.Errorf("lookup: %w", err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
	if err != nil {
		return nil, fmt.Errorf("outbox: %w", err)
	}
	return New(t.Context(), cfg, &desc, src, out, h)
}

// TestF7PLAT024Weixin covers PLAT-024: long-poll with a persisted cursor,
// replay dedupe and the authenticated send.
func TestF7PLAT024Weixin(t *testing.T) {
	t.Run("activation_gate", assertWeixinActivation)
	t.Run("poll_cursor_send", assertWeixinPoll)
	t.Run("cursor_restart", assertWeixinRestart)
}

func assertWeixinActivation(t *testing.T) {
	srv := newFakeILink(t)
	cfg := &Config{Enabled: true, BaseURL: srv.srv.URL, Account: "acct"}
	if _, err := buildAdapter(t, cfg, secrets{}, newRecorder(), newScheduler(t)); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing secret = %v", err)
	}
	if _, requests, _ := srv.snapshot(); requests != 0 {
		t.Fatalf("missing secret made %d requests", requests)
	}
}

func assertWeixinRestart(t *testing.T) {
	t.Helper()
	srv := newFakeILink(t)
	cursor := &memCursor{c: "c1", found: true}
	cfg := &Config{Enabled: true, BaseURL: srv.srv.URL, Account: "acct", Cursor: cursor}
	ad, err := buildAdapter(t, cfg, secrets{"weixin_token": "tok"}, newRecorder(), newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if ad.cursor != "c1" {
		t.Fatalf("restored cursor = %q", ad.cursor)
	}
	if err := ad.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	cursors, _, _ := srv.snapshot()
	if len(cursors) == 0 || cursors[0] != "c1" {
		t.Fatalf("restart cursors = %v", cursors)
	}
}

func assertWeixinPoll(t *testing.T) {
	t.Helper()
	srv := newFakeILink(t)
	cursor := &memCursor{}
	cfg := &Config{Enabled: true, BaseURL: srv.srv.URL, Account: "acct", Cursor: cursor}
	rec := newRecorder()
	ad, err := buildAdapter(t, cfg, secrets{"weixin_token": "tok"}, rec, newScheduler(t))
	if err != nil {
		t.Fatal(err)
	}
	if err := ad.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	select {
	case in := <-rec.seen:
		if in.ChatID != "chat1" || in.SenderID != "u1" || in.UpdateID != "weixin:w1" {
			t.Fatalf("inbound = %+v", in)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("no inbound")
	}
	if rec.count() != 1 {
		t.Fatalf("turns = %d, want 1 (replay dedupe)", rec.count())
	}
	if got := cursor.get(); got != "c1" {
		t.Fatalf("cursor = %q, want c1", got)
	}
	if err := ad.poll(t.Context()); err != nil {
		t.Fatal(err)
	}
	cursors, _, _ := srv.snapshot()
	if len(cursors) != 2 || cursors[0] != "" || cursors[1] != "c1" {
		t.Fatalf("cursors = %v", cursors)
	}
	if got := cursor.get(); got != "c2" {
		t.Fatalf("cursor after second poll = %q", got)
	}
	assertWeixinSend(t, ad, srv)
}

func assertWeixinSend(t *testing.T, ad *Adapter, srv *fakeILink) {
	t.Helper()
	if err := ad.Send(t.Context(), "chat1", "hello **world**"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for _, _, sent := srv.snapshot(); len(sent) == 0 && time.Now().Before(deadline); _, _, sent = srv.snapshot() {
		select {
		case <-t.Context().Done():
		case <-time.After(10 * time.Millisecond):
		}
	}
	if _, _, sent := srv.snapshot(); len(sent) != 1 || sent[0]["account"] != "acct" || !strings.Contains(sent[0]["text"], "world") {
		t.Fatalf("sent = %v", sent)
	}
}
