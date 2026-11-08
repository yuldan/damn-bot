package gwwa_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwwa"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// TestEdgeCases catalogs the F7 bridge row owned by this package (ADR-326).
func TestEdgeCases(t *testing.T) {
	g := phase.Current(t)
	t.Run("EC-PLAT-024", func(t *testing.T) {
		if !g.Item(t, "EC-PLAT-024", "F7", "covered") {
			return
		}
		TestF7ECPLAT024BridgeOwnership(t)
	})
}

// TestF7ECPLAT024BridgeOwnership pins EC-PLAT-024: a timeout while reading a
// bridge RPC body does not release the session that owns the chat. The slow
// request holds the session's ownership token (client.go: Call takes it before
// it writes a frame and releases it only when the call returns); a second
// request for the same session cannot become a second owner, and ownership
// transfers only after the slow call finishes.
func TestF7ECPLAT024BridgeOwnership(t *testing.T) {
	t.Run("timeout_keeps_owner", ecPLAT024TimeoutKeepsOwner)
	t.Run("slow_request_keeps_ownership", ecPLAT024SlowRequestKeepsOwnership)
	t.Run("two_slow_requests_one_owner", ecPLAT024Serialized)
}

// ownershipBridge is a `damnbot.bridge/v1` peer whose /rpc handler writes the
// first half of the response body and then stalls, so the client's body read
// runs into its context deadline.
type ownershipBridge struct {
	mu        sync.Mutex
	events    int
	sends     int
	inFlight  int
	maxFlight int

	gate      chan struct{}
	stalled   chan struct{}
	eventLine chan string
}

func newOwnershipBridge() *ownershipBridge {
	return &ownershipBridge{
		gate:      make(chan struct{}),
		stalled:   make(chan struct{}, 16),
		eventLine: make(chan string, 8),
	}
}

func (b *ownershipBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no bearer", http.StatusUnauthorized)
		return
	}
	switch r.URL.Path {
	case "/events":
		b.stream(w, r)
	case "/rpc":
		b.rpc(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (b *ownershipBridge) stream(w http.ResponseWriter, r *http.Request) {
	b.mu.Lock()
	b.events++
	primary := b.events == 1
	b.mu.Unlock()
	if _, err := fmt.Fprintln(w, `{"jsonrpc":"2.0","method":"bridge.hello","params":{"protocol":"damnbot.bridge/v1","bridge_version":"0.1.0","capabilities":{"wa_version":"2.3000.101","wa_version_source":"build"}}}`); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	if !primary {
		// A second connection is a second session: it never inherits the
		// first session's inbound history.
		<-r.Context().Done()
		return
	}
	for {
		select {
		case line := <-b.eventLine:
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
			if flusher, ok := w.(http.Flusher); ok {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (b *ownershipBridge) rpc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID     uint64 `json:"id"`
		Method string `json:"method"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	b.mu.Lock()
	b.inFlight++
	if b.inFlight > b.maxFlight {
		b.maxFlight = b.inFlight
	}
	gate := b.gate
	slow := req.Method == "send"
	if slow {
		b.sends++
	}
	b.mu.Unlock()
	defer func() {
		b.mu.Lock()
		b.inFlight--
		b.mu.Unlock()
	}()
	if !slow {
		if _, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID); err != nil {
			return
		}
		return
	}
	// First half of the body, then a stall: the client is inside the read when
	// its deadline expires.
	if _, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":`, req.ID); err != nil {
		return
	}
	if flusher, ok := w.(http.Flusher); ok {
		flusher.Flush()
	}
	select {
	case b.stalled <- struct{}{}:
	default:
	}
	select {
	case <-gate:
	case <-r.Context().Done():
		return
	}
	if _, err := fmt.Fprint(w, "{}}"); err != nil {
		return
	}
}

func (b *ownershipBridge) newGate() chan struct{} {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.gate = make(chan struct{})
	return b.gate
}

func (b *ownershipBridge) counts() (events, sends, maxFlight int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.events, b.sends, b.maxFlight
}

func (b *ownershipBridge) inbound(text string) {
	b.eventLine <- fmt.Sprintf(`{"jsonrpc":"2.0","method":"inbound","params":{"external_id":"e-%s","chat_id":"6281","sender_id":"6281","text":%q}}`, text, text)
}

func connectOwnershipBridge(t *testing.T, srv *httptest.Server) *gwwa.Client {
	t.Helper()
	tr, err := gwwa.NewHTTP(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	c, err := gwwa.Connect(t.Context(), tr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := c.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	return c
}

func ecPLAT024TimeoutKeepsOwner(t *testing.T) {
	bridge := newOwnershipBridge()
	srv := httptest.NewServer(http.HandlerFunc(bridge.handle))
	t.Cleanup(srv.Close)
	c := connectOwnershipBridge(t, srv)
	bridge.inbound("hi")
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil })

	gate := bridge.newGate()
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	if err := c.Send(ctx, "6281", "slow", "k1"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("slow send = %v, want context deadline", err)
	}
	// Release the stalled handler; the session that owns the chat is still the
	// same one and needs no fresh inbound message to send again.
	close(gate)
	if err := c.Send(t.Context(), "6281", "after", "k2"); err != nil {
		t.Fatalf("send after body timeout: %v", err)
	}
	events, sends, _ := bridge.counts()
	if events != 1 {
		t.Fatalf("events connections = %d, want 1 (timeout reconnected)", events)
	}
	if sends != 2 {
		t.Fatalf("bridge sends = %d, want 2", sends)
	}
}

func ecPLAT024Serialized(t *testing.T) {
	bridge := newOwnershipBridge()
	srv := httptest.NewServer(http.HandlerFunc(bridge.handle))
	t.Cleanup(srv.Close)
	c := connectOwnershipBridge(t, srv)
	bridge.inbound("hi")
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil })

	gate := bridge.newGate()
	errs := make(chan error, 2)
	go func() { errs <- c.Send(t.Context(), "6281", "one", "k1") }()
	select {
	case <-bridge.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("first slow send never reached the bridge")
	}
	go func() { errs <- c.Send(t.Context(), "6281", "two", "k2") }()
	// The second call waits on the session's serialization: the bridge sees one
	// in-flight request, not a second owner.
	select {
	case <-bridge.stalled:
		t.Fatal("second slow send reached the bridge while the first was in flight")
	case <-time.After(200 * time.Millisecond):
	}
	close(gate)
	for range 2 {
		if err := <-errs; err != nil {
			t.Fatalf("serialized send: %v", err)
		}
	}
	events, sends, maxFlight := bridge.counts()
	if events != 1 || sends != 2 {
		t.Fatalf("events/sends = %d/%d, want 1/2", events, sends)
	}
	if maxFlight != 1 {
		t.Fatalf("max in-flight = %d, want 1 (two owners)", maxFlight)
	}
}

// ecPLAT024SlowRequestKeepsOwnership is the ownership negative control: while
// the slow body read still holds the session, a second request for the same
// session is refused with ErrOwned (no frame written) instead of becoming a
// second owner; ownership only moves after the slow call returns.
func ecPLAT024SlowRequestKeepsOwnership(t *testing.T) {
	bridge := newOwnershipBridge()
	srv := httptest.NewServer(http.HandlerFunc(bridge.handle))
	t.Cleanup(srv.Close)
	c := connectOwnershipBridge(t, srv)
	bridge.inbound("hi")
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil })

	gate := bridge.newGate()
	first := make(chan error, 1)
	go func() { first <- c.Send(t.Context(), "6281", "owner", "k1") }()
	select {
	case <-bridge.stalled:
	case <-time.After(5 * time.Second):
		t.Fatal("slow owner never reached the bridge")
	}
	busy, cancel := context.WithTimeout(t.Context(), 150*time.Millisecond)
	defer cancel()
	err := c.Send(busy, "6281", "second", "k2")
	if !errors.Is(err, gwwa.ErrOwned) || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended send = %v, want ErrOwned + deadline", err)
	}
	events, sends, maxFlight := bridge.counts()
	if events != 1 || sends != 1 || maxFlight != 1 {
		t.Fatalf("while owned: events/sends/max = %d/%d/%d, want 1/1/1", events, sends, maxFlight)
	}
	close(gate)
	if err := <-first; err != nil {
		t.Fatalf("slow owner: %v", err)
	}
	if err := c.Send(t.Context(), "6281", "after", "k3"); err != nil {
		t.Fatalf("ownership did not transfer after completion: %v", err)
	}
	events, sends, maxFlight = bridge.counts()
	if events != 1 || sends != 2 || maxFlight != 1 {
		t.Fatalf("after transfer: events/sends/max = %d/%d/%d, want 1/2/1", events, sends, maxFlight)
	}
}
