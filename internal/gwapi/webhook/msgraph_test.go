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

const graphClientState = "cs-secret"

type graphCall struct {
	method string
	path   string
	auth   string
	body   map[string]any
}

type fakeGraph struct {
	mu    sync.Mutex
	calls []graphCall
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
		g.mu.Unlock()
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

func graphDescriptor(t *testing.T) gwplat.Descriptor {
	t.Helper()
	reg, err := gwplat.NewDefaultRegistry()
	if err != nil {
		t.Fatal(err)
	}
	d, err := reg.Lookup("msgraph_webhook")
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func testGraphAdapter(t *testing.T, h gwplat.Handler, srv *httptest.Server) (*GraphAdapter, *Store) {
	t.Helper()
	store, _ := testStore(t)
	desc := graphDescriptor(t)
	a, err := NewGraph(t.Context(), &MSGraphConfig{
		Enabled: true, BaseURL: srv.URL, Doer: srv.Client(),
		Token:        func(context.Context) (string, error) { return "graph-token", nil },
		Lifetime:     time.Hour,
		RenewLead:    15 * time.Minute,
		MaxBodyBytes: MaxBodyBytes,
	}, &desc, secretMap{"msgraph_client_state": graphClientState}, store, h)
	if err != nil {
		t.Fatal(err)
	}
	return a, store
}

func addGraphSubscription(t *testing.T, store *Store) Subscription {
	t.Helper()
	sub, _, err := store.Add(&Subscription{
		ID: "gsub-1", Provider: "msgraph",
		URL: "https://hooks.example.test/graph", Events: []string{"message.created"},
		SecretRef: "msgraph_client_state", // #nosec G101 -- secret name, not a value
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
	a, store := testGraphAdapter(t, &captureHandler{}, srv)
	sub := graphSubscribe(t, a, graph)
	graphRenew(t, a, graph, store)
	if err := a.RemoveSubscription(t.Context(), "gsub-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := store.Get(sub.ID); ok {
		t.Fatal("subscription row survived removal")
	}
	calls := graph.snapshot()
	last := calls[len(calls)-1]
	if last.method != http.MethodDelete || last.path != "/v1.0/subscriptions/gsub-1" {
		t.Fatalf("delete call = %+v", last)
	}
}

// TestF7PLAT030MSGraphRenew is the ledger name for renewal-before-expiry.
func TestF7PLAT030MSGraphRenew(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	a, store := testGraphAdapter(t, &captureHandler{}, srv)
	graphSubscribe(t, a, graph)
	graphRenew(t, a, graph, store)
}

func graphSubscribe(t *testing.T, a *GraphAdapter, graph *fakeGraph) Subscription {
	t.Helper()
	sub, created, err := a.Subscribe(t.Context(), &Subscription{
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
	if got := fmt.Sprint(calls[0].body["clientState"]); got != graphClientState {
		t.Fatalf("clientState was not sent to Graph")
	}
	if _, created, err := a.Subscribe(t.Context(), &Subscription{
		Provider: "msgraph", URL: "https://hooks.example.test/graph",
		Events: []string{"message.created"}, SecretRef: "msgraph_client_state", // #nosec G101 -- secret name, not a value // #nosec G101 -- secret name, not a value
	}); err != nil || created || len(graph.snapshot()) != 1 {
		t.Fatalf("idempotent Subscribe created a second row: %t %v", created, err)
	}
	return sub
}

func graphRenew(t *testing.T, a *GraphAdapter, graph *fakeGraph, store *Store) {
	t.Helper()
	renewed, err := a.RenewDue(t.Context())
	if err != nil || renewed != 1 {
		t.Fatalf("RenewDue = %d, %v", renewed, err)
	}
	calls := graph.snapshot()
	last := calls[len(calls)-1]
	if last.method != http.MethodPatch || !strings.Contains(last.path, "/v1.0/subscriptions/gsub-1") {
		t.Fatalf("renew call = %+v", last)
	}
	stored, ok := store.Get("gsub-1")
	if !ok || !stored.ExpiresAt.After(time.Now().Add(50*time.Minute)) {
		t.Fatalf("expiry not extended: %+v", stored)
	}
}

// TestF7MSGraphValidationToken (GW-012): the handshake echoes the token
// verbatim as text/plain and refuses an empty token.
func TestF7MSGraphValidationToken(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	a, _ := testGraphAdapter(t, &captureHandler{}, srv)

	const token = "Validation: abc123"
	echo, err := a.HandleValidation(token)
	if err != nil || echo != token {
		t.Fatalf("HandleValidation = %q, %v", echo, err)
	}
	if _, err := a.HandleValidation(""); !errors.Is(err, ErrValidation) {
		t.Fatalf("empty token = %v", err)
	}
	w := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graph?validationToken=abc123", nil)
	a.ServeHTTP(w, req)
	if w.Code != http.StatusOK || w.Body.String() != "abc123" ||
		!strings.HasPrefix(w.Header().Get("Content-Type"), "text/plain") {
		t.Fatalf("handshake = %d %q %q", w.Code, w.Body.String(), w.Header().Get("Content-Type"))
	}
	bad := httptest.NewRecorder()
	a.ServeHTTP(bad, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graph?validationToken=", nil))
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("empty handshake = %d, want 400", bad.Code)
	}
}

// TestF7MSGraphClientState (GW-012): a foreign clientState, an unknown
// subscription or an unsupported changeType rejects the whole batch with zero
// dispatches.
func TestF7MSGraphClientState(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	rec := &captureHandler{}
	a, store := testGraphAdapter(t, rec, srv)
	addGraphSubscription(t, store)

	for _, tc := range []struct {
		name string
		body []byte
	}{
		{"clientState", notification(t, "wrong", "/chats('c1')/messages('m1')", "created")},
		{"changeType", notification(t, graphClientState, "/chats('c1')/messages('m1')", "mystery")},
		{"unknown", unknownSubscriptionNotification(t)},
	} {
		if n, err := a.HandleNotification(t.Context(), tc.body); n != 0 || err == nil {
			t.Fatalf("%s notification = %d, %v", tc.name, n, err)
		}
	}
	if len(rec.got) != 0 {
		t.Fatalf("dispatches = %d, want 0", len(rec.got))
	}
}

func unknownSubscriptionNotification(t *testing.T) []byte {
	t.Helper()
	var batch map[string][]map[string]any
	if err := json.Unmarshal(notification(t, graphClientState, "/chats('c1')/messages('m1')", "created"), &batch); err != nil {
		t.Fatal(err)
	}
	batch["value"][0]["subscriptionId"] = "gsub-unknown"
	mutated, err := json.Marshal(batch)
	if err != nil {
		t.Fatal(err)
	}
	return mutated
}

// TestF7GW012MSGraph (GW-012): the HTTP surface accepts a valid notification
// into the Teams pipeline once, dedupes the replay, and rejects an unregistered
// notification without running a turn.
func TestF7GW012MSGraph(t *testing.T) {
	t.Parallel()
	graph := &fakeGraph{}
	srv := httptest.NewServer(graph.handler(t))
	defer srv.Close()
	rec := &captureHandler{}
	a, store := testGraphAdapter(t, rec, srv)
	addGraphSubscription(t, store)
	body := notification(t, graphClientState, "/chats('19:team')/messages('m1')", "created")

	for range 2 {
		w := httptest.NewRecorder()
		a.ServeHTTP(w, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graph", bytes.NewReader(body)))
		if w.Code != http.StatusAccepted {
			t.Fatalf("notification = %d %q", w.Code, w.Body.String())
		}
	}
	if len(rec.got) != 1 {
		t.Fatalf("dispatches = %d, want 1 (replay deduped)", len(rec.got))
	}
	in := rec.got[0]
	if in.Platform != "msgraph_webhook" || in.ChatID != "19:team" || !in.Mentioned {
		t.Fatalf("inbound = %+v", in)
	}
	foreign := httptest.NewRecorder()
	a.ServeHTTP(foreign, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graph",
		bytes.NewReader(notification(t, "evil", "/chats('x')/messages('y')", "created"))))
	if foreign.Code != http.StatusBadRequest || len(rec.got) != 1 {
		t.Fatalf("foreign = %d, dispatches=%d", foreign.Code, len(rec.got))
	}
	big := httptest.NewRecorder()
	a.ServeHTTP(big, httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/graph",
		bytes.NewReader(bytes.Repeat([]byte("x"), MaxBodyBytes+1))))
	if big.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized = %d, want 413", big.Code)
	}
	desc := graphDescriptor(t)
	if _, err := NewGraph(t.Context(), &MSGraphConfig{Enabled: true}, &desc,
		nil, store, rec); !errors.Is(err, gwplat.ErrMissingSecret) {
		t.Fatalf("missing clientState = %v, want ErrMissingSecret", err)
	}
}
