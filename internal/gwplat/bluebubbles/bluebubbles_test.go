package bluebubbles

import (
	"context"
	"encoding/json"
	"errors"
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

type secrets map[string]string

func (s secrets) Get(_ context.Context, name string) (string, error) {
	v, ok := s[name]
	if !ok {
		return "", errors.New("missing")
	}
	return v, nil
}

type allowPacer struct{}

func (allowPacer) TakeChat(string, string, bool, time.Time) (bool, time.Duration) { return true, 0 }
func (allowPacer) Penalize(string, time.Time)                                     {}

func newAdapter(t *testing.T, base string, in func(context.Context, *gateway.Inbound) error) (*Adapter, *gwplat.Outbox) {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("bluebubbles")
	if err != nil {
		t.Fatal(err)
	}
	sched, err := outq.New(&outq.Options{Pacer: allowPacer{}})
	if err != nil {
		t.Fatal(err)
	}
	out, err := gwplat.NewOutbox(&desc, sched)
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
	a, err := New(ctx, &Config{Enabled: true, BaseURL: base}, &desc,
		secrets{"bluebubbles_password": "s3cret"}, out, gwplat.HandlerFunc(in))
	if err != nil {
		t.Fatal(err)
	}
	return a, out
}

func TestF7PLAT002BlueBubbles(t *testing.T) {
	var mu sync.Mutex
	sent := make(chan map[string]string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		body["password"] = r.URL.Query().Get("password")
		mu.Lock()
		sent <- body
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var mu2 sync.Mutex
	var got []gateway.Inbound
	a, _ := newAdapter(t, srv.URL, func(_ context.Context, in *gateway.Inbound) error {
		mu2.Lock()
		got = append(got, *in)
		mu2.Unlock()
		return nil
	})
	h := a.Handler()
	payload := `{"type":"new-message","data":{"guid":"m-1","text":"halo","isFromMe":false,"handle":{"address":"u-1"},"chats":[{"guid":"iMessage;-;u-1"}]}}`
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hook?password=s3cret", strings.NewReader(payload))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("webhook = %d", rec.Code)
		}
	}
	if len(got) != 1 {
		t.Fatalf("turns = %d, want 1", len(got))
	}
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/hook?password=wrong", strings.NewReader(payload))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized || len(got) != 1 {
		t.Fatalf("wrong password status=%d turns=%d", rec.Code, len(got))
	}
	if err := a.Send(t.Context(), "iMessage;-;u-1", "**halo** dunia"); err != nil {
		t.Fatal(err)
	}
	body := awaitValue(t, sent)
	if body["password"] != "s3cret" || !strings.Contains(body["message"], "halo") {
		t.Fatalf("sent = %v", body)
	}
}

func awaitValue[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(5 * time.Second):
		t.Fatal("no value")
		var zero T
		return zero
	}
}
