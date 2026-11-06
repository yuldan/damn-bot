package line

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
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

func newAdapter(t *testing.T, api string, in gwplat.Handler) *Adapter {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	desc, err := reg.Lookup("line")
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
	a, err := New(ctx, &Config{Enabled: true, API: api}, &desc,
		secrets{"line_channel_secret": "channel-secret", "line_access_token": "line-token"}, out, in)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func payload(id, token string) []byte {
	return []byte(`{"events":[{"type":"message","replyToken":"` + token + `","source":{"userId":"U1"},"message":{"id":"` + id + `","type":"text","text":"halo"}}]}`)
}

func TestF7PLAT010LINE(t *testing.T) {
	sent := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer line-token" {
			http.Error(w, "no", http.StatusUnauthorized)
			return
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		sent <- body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	var got []gateway.Inbound
	a := newAdapter(t, srv.URL, gwplat.HandlerFunc(func(_ context.Context, in *gateway.Inbound) error {
		got = append(got, *in)
		return nil
	}))
	h := a.Handler()
	body := payload("m-1", "rt-1")
	sig := gwplat.HMACSHA256Base64("channel-secret", body)
	for range 2 {
		req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/line", strings.NewReader(string(body)))
		req.Header.Set("X-Line-Signature", sig)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("valid = %d", rec.Code)
		}
	}
	if len(got) != 1 {
		t.Fatalf("turns = %d, want 1", len(got))
	}
	next := payload("m-2", "rt-1")
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/line", strings.NewReader(string(next)))
	req.Header.Set("X-Line-Signature", gwplat.HMACSHA256Base64("channel-secret", next))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if len(got) != 1 {
		t.Fatal("reused reply token ran a turn")
	}
	if err := a.Send(t.Context(), "U1", "**halo**"); err != nil {
		t.Fatal(err)
	}
	sentBody := awaitValue(t, sent)
	if !strings.Contains(string(mustJSON(t, sentBody)), "halo") {
		t.Fatalf("sent = %v", sentBody)
	}
}

func TestF7LINEHMAC(t *testing.T) {
	body := []byte(`{"events":[]}`)
	good := gwplat.HMACSHA256Base64("secret", body)
	if err := gwplat.VerifyHMACSHA256("secret", body, good); err != nil {
		t.Fatal(err)
	}
	if err := gwplat.VerifyHMACSHA256("other", body, good); err == nil {
		t.Fatal("wrong secret accepted")
	}
	if err := gwplat.VerifyHMACSHA256("secret", append(body, ' '), good); err == nil {
		t.Fatal("tampered body accepted")
	}
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
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
