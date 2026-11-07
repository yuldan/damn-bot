package webhook

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

func testSender(client HTTPDoer) *Sender {
	return &Sender{
		Client: client, MaxAttempts: DefaultAttempts,
		Backoff: gwplat.Backoff{Base: time.Millisecond, Max: time.Millisecond},
	}
}

// TestF7GW011Subscriptions (GW-011): a stored subscription exists once, is
// listed and removed, and a test delivery signs exactly one event without ever
// exposing the secret value.
func TestF7GW011Subscriptions(t *testing.T) {
	t.Parallel()
	store, _ := testStore(t)
	sub, created, err := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	})
	if err != nil || !created {
		t.Fatalf("Add = %+v created=%t err=%v", sub, created, err)
	}
	if _, again, aerr := store.Add(&Subscription{
		Provider: "generic", URL: "https://hooks.example.test/alert",
		Events: []string{"message.created"}, SecretRef: "webhook_secret",
	}); aerr != nil || again {
		t.Fatalf("idempotent Add created a row: %t %v", again, aerr)
	}
	if rows := store.List(); len(rows) != 1 || rows[0].ID != sub.ID {
		t.Fatalf("List = %+v", rows)
	}

	checkSignedTestDelivery(t, &sub)
	if err := store.Remove(sub.ID); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(sub.ID); ok {
		t.Fatal("row survived Remove")
	}
}

func checkSignedTestDelivery(t *testing.T, sub *Subscription) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("read body: %v", rerr)
		}
		if r.Header.Get(SignatureHeader) != Sign([]byte(testSecret), body) {
			t.Errorf("signature mismatch")
		}
		var env Envelope
		if uerr := json.Unmarshal(body, &env); uerr != nil {
			t.Errorf("envelope: %v", uerr)
		}
		if env.Event != "test" || env.ID == "" {
			t.Errorf("test envelope = %+v", env)
		}
		calls.Add(1)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sub.URL = srv.URL
	receipt, err := testSender(srv.Client()).Send(t.Context(), sub, []byte(testSecret), "test")
	if err != nil {
		t.Fatalf("send: %v", err)
	}
	if receipt.Attempts != 1 || calls.Load() != 1 || receipt.Status != http.StatusOK || receipt.ID == "" {
		t.Fatalf("receipt = %+v calls=%d", receipt, calls.Load())
	}
}

// TestF7GW011SubscriptionRetry (GW-011): a failing target is retried a bounded
// number of times with the same event id, and a permanent failure is typed.
func TestF7GW011SubscriptionRetry(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	var ids []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("read body: %v", rerr)
		}
		var env Envelope
		if err := json.Unmarshal(body, &env); err != nil {
			t.Errorf("envelope: %v", err)
		}
		ids = append(ids, env.ID)
		if calls.Add(1) < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()
	sub := &Subscription{Provider: "generic", URL: srv.URL, Events: []string{"test"}, SecretRef: "webhook_secret"}
	receipt, err := testSender(srv.Client()).Send(t.Context(), sub, []byte(testSecret), "test")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if receipt.Attempts != 3 || calls.Load() != 3 {
		t.Fatalf("receipt = %+v calls=%d", receipt, calls.Load())
	}
	for _, id := range ids {
		if id != receipt.ID {
			t.Fatalf("retry changed the event id: %v want %s", ids, receipt.ID)
		}
	}

	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer failing.Close()
	before := calls.Load()
	_, err = testSender(failing.Client()).Send(context.Background(),
		&Subscription{URL: failing.URL, Events: []string{"test"}, SecretRef: "webhook_secret"},
		[]byte(testSecret), "test")
	if !errors.Is(err, ErrDelivery) || calls.Load()-before != DefaultAttempts {
		t.Fatalf("bounded retry = %v, attempts=%d", err, calls.Load()-before)
	}
}

// TestF7GW011GraphTestEvent: an msgraph target receives a Graph-shaped
// notification carrying the stored subscription id and clientState.
func TestF7GW011GraphTestEvent(t *testing.T) {
	t.Parallel()
	var got map[string][]map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, rerr := io.ReadAll(r.Body)
		if rerr != nil {
			t.Errorf("read body: %v", rerr)
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Errorf("graph envelope: %v", err)
		}
		w.WriteHeader(http.StatusAccepted)
	}))
	defer srv.Close()
	sub := &Subscription{
		ID: "gsub-1", Provider: "msgraph", URL: srv.URL,
		Events: []string{"message.created"}, SecretRef: "msgraph_client_state",
	} // #nosec G101 -- secret name, not a value
	if _, err := testSender(srv.Client()).Send(t.Context(), sub, []byte(testSecret), "test"); err != nil {
		t.Fatal(err)
	}
	value := got["value"]
	if len(value) != 1 || value[0]["subscriptionId"] != "gsub-1" || value[0]["clientState"] != testSecret {
		t.Fatalf("graph test event = %+v", got)
	}
}
