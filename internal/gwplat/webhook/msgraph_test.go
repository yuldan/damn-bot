package webhook

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwplat"
)

type graphCall struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

type fakeGraph struct {
	mu    sync.Mutex
	calls []graphCall
	fail  int
}

func (g *fakeGraph) handler(t *testing.T) http.HandlerFunc {
	t.Helper()
	return func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("graph body: %v", err)
		}
		call := graphCall{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		if len(raw) > 0 {
			if uerr := json.Unmarshal(raw, &call.body); uerr != nil {
				t.Errorf("graph body json: %v", uerr)
			}
		}
		g.mu.Lock()
		g.calls = append(g.calls, call)
		fail := g.fail
		g.mu.Unlock()
		if fail > 0 {
			g.fail--
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		switch r.Method {
		case http.MethodPost:
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusCreated)
			if _, werr := io.WriteString(w, `{"id":"gsub-1"}`); werr != nil {
				t.Errorf("graph write: %v", werr)
			}
		case http.MethodDelete:
			w.WriteHeader(http.StatusNoContent)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}
}

func (g *fakeGraph) snapshot() []graphCall {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]graphCall(nil), g.calls...)
}

func testGraphAdapter(t *testing.T, h gwplat.Handler, srv *httptest.Server) (*GraphAdapter, *Store) {
	t.Helper()
	store, _ := testStore(t)
	a, err := NewGraph(t.Context(), &MSGraphConfig{
		Enabled: true, BaseURL: srv.URL, HTTPClient: srv.Client(),
		Token:        func(context.Context) (string, error) { return "graph-token", nil },
		Lifetime:     time.Hour,
		RenewLead:    15 * time.Minute,
		MaxBodyBytes: MaxBodyBytes,
	}, testDescriptor(t, "msgraph_webhook"),
		secretMap{"msgraph_client_state": "cs-secret"}, store, h)
	if err != nil {
		t.Fatal(err)
	}
	return a, store
}

func addGraphSubscription(t *testing.T, store *Store) Subscription {
	t.Helper()
	sub, _, err := store.Add(Subscription{
		ID: "gsub-1", Provider: "msgraph",
		URL: "https://hooks.example.test/graph", Events: []string{"message.created"},
		SecretRef: "msgraph_client_state",
	})
	if err != nil {
		t.Fatal(err)
	}
	return sub
}

func notification(t *testing.T, clientState, resource, changeType string) []byte {
	t.Helper()
	body, err := json.Marshal(map[string]any{"value": []map[string]any{{
		"subscriptionId": "gsub-1", "clientState": clientState,
		"changeType": changeType, "resource": resource, "tenantId": "tenant",
	}}})
	if err != nil {
		t.Fatal(err)
	}
	return body
}

// TestF7PLAT030MSGraph (PLAT-030): a Graph subscription is created once,
// renewed before expiry through the HTTP seam, and deleted with its stored row.
func TestF7PLAT030MSGraph(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	rec := &recorder{}
	a, store := testGraphAdapter(t, rec, srv)

	sub, created, err := a.Subscribe(t.Context(), Subscription{
		Provider: "msgraph", URL: "https://hooks.example.test/graph",
		Events: []string{"message.created"}, ExpiresAt: time.Now().Add(10 * time.Minute),
	})
	if err != nil || !created || sub.ID != "gsub-1" {
		t.Fatalf("Subscribe = %+v created=%t err=%v", sub, created, err)
	}
	calls := graph.snapshot()
	if len(calls) != 1 || calls[0].method != http.MethodPost ||
		calls[0].path != "/v1.0/subscriptions" || calls[0].auth != "Bearer graph-token" {
		t.Fatalf("create calls = %+v", calls)
	}
	if got := fmt.Sprint(calls[0].body["notificationUrl"]); got != "https://hooks.example.test/graph" {
		t.Fatalf("notificationUrl = %q", got)
	}
	if got := fmt.Sprint(calls[0].body["clientState"]); got != "cs-secret" {
		t.Fatalf("clientState was not sent to Graph")
	}

	renewed, err := a.RenewDue(t.Context())
	if err != nil || renewed != 1 {
		t.Fatalf("RenewDue = %d, %v", renewed, err)
	}
	calls = graph.snapshot()
	last := calls[len(calls)-1]
	if last.method != http.MethodPatch || !strings.Contains(last.path, "/v1.0/subscriptions/gsub-1") {
		t.Fatalf("renew call = %+v", last)
	}
	stored, ok := store.Get("gsub-1")
	if !ok || !stored.ExpiresAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatalf("expiry not extended: %+v", stored)
	}
	if err := a.RemoveSubscription(t.Context(), "gsub-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get("gsub-1"); ok {
		t.Fatal("subscription row survived removal")
	}
}

// TestF7MSGraphValidationToken (GW-012): the handshake echoes the token
// verbatim as text/plain and refuses an empty token.
func TestF7MSGraphValidationToken(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	a, _ := testGraphAdapter(t, &recorder{}, srv)

	const token = "Validation: abc123"
	echo, err := a.HandleValidation(token)
	if err != nil || echo != token {
		t.Fatalf("HandleValidation = %q, %v", echo, err)
	}
	if _, err := a.HandleValidation(""); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty token = %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/graph?validationToken=abc123", nil)
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "abc123" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("handshake = %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
}

// TestF7MSGraphClientStateMismatch (GW-012): a foreign clientState, an unknown
// subscription or an unsupported changeType rejects the whole batch with zero
// dispatches.
func TestF7MSGraphClientStateMismatch(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	rec := &recorder{}
	a, store := testGraphAdapter(t, rec, srv)
	addGraphSubscription(t, store)

	for _, body := range [][]byte{
		notification(t, "wrong", "/chats('c1')/messages('m1')", "created"),
		notification(t, "cs-secret", "/chats('c1')/messages('m1')", "mystery"),
	} {
		if n, err := a.HandleNotification(t.Context(), body); n != 0 || err == nil {
			t.Fatalf("foreign notification = %d, %v", n, err)
		}
	}
	unknown := notification(t, "cs-secret", "/chats('c1')/messages('m1')", "created")
	var batch map[string][]map[string]any
	if err := json.Unmarshal(unknown, &batch); err != nil {
		t.Fatal(err)
	}
	batch["value"][0]["subscriptionId"] = "gsub-unknown"
	mutated, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	if n, err := a.HandleNotification(t.Context(), mutated); n != 0 || !errors.Is(err, ErrUnknownSubscription) {
		t.Fatalf("unknown subscription = %d, %v", n, err)
	}
	if rec.count() != 0 {
		t.Fatalf("dispatches = %d, want 0", rec.count())
	}
}

// TestF7GW012MSGraph (GW-012): the HTTP surface accepts a valid notification
// into the Teams pipeline once, dedupes the replay, and rejects an unregistered
// notification without running a turn.
func TestF7GW012MSGraph(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	rec := &recorder{}
	a, store := testGraphAdapter(t, rec, srv)
	addGraphSubscription(t, store)
	body := notification(t, "cs-secret", "/chats('19:team')/messages('m1')", "created")

	for range 2 {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequest(http.MethodPost, "/graph", bytes.NewReader(body)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("notification = %d %q", w.Code, w.Body.String())
		}
	}
	if rec.count() != 1 {
		t.Fatalf("dispatches = %d, want 1 (replay deduped)", rec.count())
	}
	in := rec.ins[0]
	if in.Platform != "msgraph_webhook" || in.ChatID != "19:team" || !in.Mentioned {
		t.Fatalf("inbound = %+v", in)
	}
	foreign := httptest.NewRecorder()
	a.ServeHTTP(foreign, httptest.NewRequest(http.MethodPost, "/graph",
		bytes.NewReader(notification(t, "evil", "/chats('x')/messages('y')", "created"))))
	if foreign.Code != http.StatusBadRequest || rec.count() != 1 {
		t.Fatalf("foreign = %d, dispatches=%d", foreign.Code, rec.count())
	}
	big := httptest.NewRecorder()
	a.ServeHTTP(big, httptest.NewRequest(http.MethodPost, "/graph",
		bytes.NewReader(bytes.Repeat([]byte("x"), MaxBodyBytes+1))))
	if big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d, want 413", big.Code)
	}
	if _, err := NewGraph(t.Context(), &MSGraphConfig{Enabled: true}, testDescriptor(t, "msgraph_webhook"),
		nil, store, rec); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing clientState = %v, want ErrMissingSecret", err)
	}
}
