package webhook

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

// TestF7GW011Webhook (GW-011): a stored subscription delivers exactly one
// signed test event and retries a failing target a bounded number of times.
func TestF7GW011Webhook(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	sub, _, err := store.Add(Subscription{
		Provider: "generic",
		URL:      "https://hooks.example.test/alert", Events: []string{"message.created"},
		SecretRef: "webhook_secret",
	})
	if err != nil {
		t.Fatal(err)
	}

	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("read body: %v", rerr)
		}
		if r.Header.Get(SignatureHeader) != Sign([]byte("s3cr3t"), body) {
			t.Errorf("signature mismatch")
		}
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sender := &Sender{
		Client: srv.Client(), MaxAttempts: 3,
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	}
	receipt, err := sender.Send(t.Context(), Subscription{
		URL: srv.URL, Provider: "generic",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	}, []byte("s3cr3t"), "test")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if receipt.Attempts != 3 || calls.Load() != 3 || receipt.Status != http.StatusOK || receipt.ID == "" {
		t.Fatalf("receipt = %+v calls=%d", receipt, calls.Load())
	}
	if _, ok := store.Get(sub.ID); !ok {
		t.Fatal("store row disappeared after a delivery")
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	before := calls.Load()
	_, err = (&Sender{
		Client: failing.Client(), MaxAttempts: 3,
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	}).
		Send(context.Background(), Subscription{
			URL: failing.URL, Events: []string{"message.created"},
			SecretRef: "webhook_secret",
		}, []byte("s3cr3t"), "test")
	if !errors.Is(err, ErrDelivery) || calls.Load()-before != 3 {
		t.Fatalf("bounded retry = %v, attempts=%d", err, calls.Load()-before)
	}
}
