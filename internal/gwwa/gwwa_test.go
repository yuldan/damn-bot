package gwwa_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gwwa"
)

// fakeBridge is the cheapest conformant `damnbot.bridge/v1` peer: a net.Pipe
// that speaks hello, answers requests, and lets the test push notifications.
type fakeBridge struct {
	conn net.Conn
	mu   sync.Mutex

	hello    gwwa.Hello
	sends    int
	requests []string
	cold     bool
	// failMethod makes the bridge answer that method with an RPCError.
	failMethod string
}

func newPipeBridge(t *testing.T, hello gwwa.Hello) (*fakeBridge, net.Conn) {
	t.Helper()
	client, server := net.Pipe()
	b := &fakeBridge{conn: server, hello: hello}
	t.Cleanup(func() {
		if err := server.Close(); err != nil {
			return
		}
	})
	go b.run(t)
	return b, client
}

func (b *fakeBridge) run(t *testing.T) {
	t.Helper()
	b.write(t, map[string]any{"jsonrpc": "2.0", "method": "bridge.hello", "params": b.hello})
	scanner := bufio.NewScanner(b.conn)
	scanner.Buffer(make([]byte, 64<<10), gwwa.MaxFrame)
	for scanner.Scan() {
		var req struct {
			ID     uint64          `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &req); err != nil {
			t.Errorf("bridge decode: %v", err)
			return
		}
		b.mu.Lock()
		b.requests = append(b.requests, req.Method)
		cold := b.cold
		fail := b.failMethod
		if req.Method == "send" {
			b.sends++
		}
		b.mu.Unlock()
		resp := map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": map[string]any{}}
		if req.Method == fail && fail != "" {
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{
				"code": -32000, "message": "bridge_error",
			}}
		}
		if req.Method == "send" && cold {
			resp = map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": map[string]any{
				"code": -32000, "message": "cold_outbound_denied", "data": map[string]any{"code": "cold_outbound_denied"},
			}}
		}
		if req.Method == "status" {
			resp["result"] = map[string]any{"state": "connected", "reason": "none"}
		}
		b.write(t, resp)
	}
}

func (b *fakeBridge) write(t *testing.T, frame any) {
	t.Helper()
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Errorf("bridge marshal: %v", err)
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, err := b.conn.Write(append(raw, '\n')); err != nil {
		t.Errorf("bridge write: %v", err)
	}
}

func (b *fakeBridge) notify(t *testing.T, method string, params any) {
	t.Helper()
	b.write(t, map[string]any{"jsonrpc": "2.0", "method": method, "params": params})
}

func (b *fakeBridge) methods() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.requests...)
}

func (b *fakeBridge) sendCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sends
}

func validHello() gwwa.Hello {
	return gwwa.Hello{
		Protocol:      gwwa.Protocol,
		BridgeVersion: "0.1.0",
		Capabilities:  gwwa.Capabilities{WaVersion: "2.3000.101", WaVersionSource: "build"},
	}
}

// TestBridgeProtocolConformance is the ADR-034/SPEC §9.7.1 gate: the
// handshake accepts only the pinned protocol, a wrong major or an unpinned
// wa_version is bridge_incompatible, the version is never fetched at runtime,
// and a cold send never reaches the bridge.
func TestBridgeProtocolConformance(t *testing.T) {
	t.Run("handshake", conformanceHandshake)
	t.Run("incompatible", conformanceIncompatible)
	t.Run("cold_outbound", conformanceCold)
	t.Run("bridge_rejects_cold", conformanceBridgeCold)
}

func conformanceHandshake(t *testing.T) {
	_, conn := newPipeBridge(t, validHello())
	c, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn))
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	if got := c.Hello(); got.Capabilities.WaVersion != "2.3000.101" || got.Capabilities.WaVersionSource != "build" {
		t.Fatalf("hello = %+v", got)
	}
	h, err := c.Status(t.Context())
	if err != nil || h.State != gwwa.StateConnected {
		t.Fatalf("status = %+v/%v", h, err)
	}
}

func conformanceIncompatible(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*gwwa.Hello)
	}{
		{"wrong_major", func(h *gwwa.Hello) { h.Protocol = "damnbot.bridge/v2" }},
		{"missing_version", func(h *gwwa.Hello) { h.Capabilities.WaVersion = "" }},
		{"runtime_version", func(h *gwwa.Hello) { h.Capabilities.WaVersionSource = "fetch" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h := validHello()
			tc.mutate(&h)
			_, conn := newPipeBridge(t, h)
			if _, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn)); !errors.Is(err, gwwa.ErrProtocol) {
				t.Fatalf("%s = %v, want ErrProtocol", tc.name, err)
			}
		})
	}
}

func conformanceCold(t *testing.T) {
	b, conn := newPipeBridge(t, validHello())
	c, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn))
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	if err := c.Send(t.Context(), "628999", "hello", "idem-1"); !errors.Is(err, gwwa.ErrColdOutbound) {
		t.Fatalf("cold send = %v, want ErrColdOutbound", err)
	}
	if b.sendCount() != 0 {
		t.Fatalf("cold send wrote %d frames", b.sendCount())
	}
	b.notify(t, "inbound", map[string]any{"external_id": "e1", "chat_id": "628999", "sender_id": "628999", "text": "hi"})
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil })
	if err := c.Send(t.Context(), "628999", "hello", "idem-2"); err != nil {
		t.Fatalf("warm send: %v", err)
	}
	if b.sendCount() != 1 {
		t.Fatalf("warm send wrote %d frames", b.sendCount())
	}
}

func conformanceBridgeCold(t *testing.T) {
	b, conn := newPipeBridge(t, validHello())
	c, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn))
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	b.mu.Lock()
	b.cold = true
	b.mu.Unlock()
	b.notify(t, "inbound", map[string]any{"external_id": "e2", "chat_id": "628777", "sender_id": "628777", "text": "hi"})
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil })
	if err := c.Send(t.Context(), "628777", "hello", "idem-3"); !errors.Is(err, gwwa.ErrColdOutbound) {
		t.Fatalf("bridge cold refusal = %v, want ErrColdOutbound", err)
	}
}

func closeClient(t *testing.T, c *gwwa.Client) {
	t.Helper()
	if err := c.Close(); err != nil {
		t.Errorf("close: %v", err)
	}
}

// TestF7GAPGW11Breaker locks the health.reason → breaker table: the ban and
// logout classes stop reconnect for operator action, only network/unknown
// reconnect, and a temporary ban retries once after 24 hours.
func TestF7GAPGW11Breaker(t *testing.T) {
	for _, tc := range []struct {
		reason gwwa.HealthReason
		action gwwa.Action
		retry  time.Duration
	}{
		{gwwa.ReasonLoggedOut, gwwa.ActionNeedsAttention, 0},
		{gwwa.ReasonBanned, gwwa.ActionNeedsAttention, 0},
		{gwwa.ReasonTemporaryBan, gwwa.ActionNeedsAttention, 24 * time.Hour},
		{gwwa.ReasonProtocolOutdated, gwwa.ActionNeedsAttention, 0},
		{gwwa.ReasonNetwork, gwwa.ActionReconnect, 0},
		{gwwa.ReasonUnknown, gwwa.ActionReconnect, 0},
		{gwwa.ReasonNone, gwwa.ActionNone, 0},
	} {
		v := gwwa.Evaluate(gwwa.Health{State: gwwa.StateDisconnected, Reason: tc.reason})
		if v.Action != tc.action || v.RetryAfter != tc.retry {
			t.Fatalf("%s = %+v, want %s/%s", tc.reason, v, tc.action, tc.retry)
		}
		if v.Action == gwwa.ActionNeedsAttention && v.Instruction == "" {
			t.Fatalf("%s has no operator instruction", tc.reason)
		}
	}
	// A ten-minute outage at the 1 s → 60 s schedule stays ≤ 15 attempts.
	total, delay, attempts := time.Duration(0), time.Second, 0
	for total+delay <= 10*time.Minute {
		total += delay
		attempts++
		if delay < 30*time.Second {
			delay *= 2
		} else {
			delay = time.Minute
		}
	}
	if attempts > 15 {
		t.Fatalf("10-minute outage = %d attempts, want ≤ 15", attempts)
	}
}

func waitEvent(t *testing.T, c *gwwa.Client, match func(gwwa.Event) bool) gwwa.Event {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case ev, ok := <-c.Events():
			if !ok {
				t.Fatal("event stream closed")
			}
			if match(ev) {
				return ev
			}
		case <-deadline:
			t.Fatal("notification not delivered")
		}
	}
}

// TestBridgeHTTPTransport proves the loopback HTTP transport: hello and
// inbound arrive over /events and send is a POST /rpc with the bearer token;
// no request fetches a WhatsApp Web protocol version at runtime.
func TestBridgeHTTPTransport(t *testing.T) {
	bridge := newHTTPBridge()
	srv := httptest.NewServer(http.HandlerFunc(bridge.handle))
	defer srv.Close()

	tr, err := gwwa.NewHTTP(srv.URL, "tok", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	c, err := gwwa.Connect(ctx, tr)
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	bridge.events <- `{"jsonrpc":"2.0","method":"inbound","params":{"external_id":"h1","chat_id":"6281","sender_id":"6281","text":"yo"}}`
	waitEvent(t, c, func(ev gwwa.Event) bool { return ev.Inbound != nil && ev.Inbound.ExternalID == "h1" })
	if err := c.Send(ctx, "6281", "reply", "h1"); err != nil {
		t.Fatal(err)
	}
	for _, p := range bridge.pathsSeen() {
		if strings.Contains(p, "version") || strings.Contains(p, "fetch") {
			t.Fatalf("runtime protocol-version request: %s", p)
		}
	}
	if !strings.Contains(strings.Join(bridge.pathsSeen(), ","), "/rpc") {
		t.Fatalf("no rpc request: %v", bridge.pathsSeen())
	}
}

type httpBridge struct {
	mu     sync.Mutex
	paths  []string
	events chan string
}

func newHTTPBridge() *httpBridge {
	return &httpBridge{events: make(chan string, 8)}
}

func (b *httpBridge) pathsSeen() []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]string(nil), b.paths...)
}

func (b *httpBridge) handle(w http.ResponseWriter, r *http.Request) {
	if r.Header.Get("Authorization") != "Bearer tok" {
		http.Error(w, "no bearer", http.StatusUnauthorized)
		return
	}
	b.mu.Lock()
	b.paths = append(b.paths, r.URL.Path)
	b.mu.Unlock()
	switch r.URL.Path {
	case "/events":
		b.stream(w, r)
	case "/rpc":
		b.rpc(w, r)
	default:
		http.NotFound(w, r)
	}
}

func (b *httpBridge) stream(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/x-ndjson")
	flusher, flushable := w.(http.Flusher)
	if !flushable {
		flusher = nil
	}
	if _, err := fmt.Fprintln(w, `{"jsonrpc":"2.0","method":"bridge.hello","params":{"protocol":"damnbot.bridge/v1","bridge_version":"0.1.0","capabilities":{"wa_version":"2.3000.101","wa_version_source":"build"}}}`); err != nil {
		return
	}
	if flusher != nil {
		flusher.Flush()
	}
	for {
		select {
		case line := <-b.events:
			if _, err := fmt.Fprintln(w, line); err != nil {
				return
			}
			if flusher != nil {
				flusher.Flush()
			}
		case <-r.Context().Done():
			return
		}
	}
}

func (b *httpBridge) rpc(w http.ResponseWriter, r *http.Request) {
	var req struct {
		ID uint64 `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if _, err := fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%d,"result":{}}`, req.ID); err != nil {
		return
	}
}

// TestBridgeAckAndLogout walks the acknowledgement and logout calls of the
// client surface, including the case where the bridge answers with an RPCError.
func TestBridgeAckAndLogout(t *testing.T) {
	t.Run("ok", ackAndLogoutOK)
	t.Run("bridge_error", ackAndLogoutError)
}

func ackAndLogoutOK(t *testing.T) {
	b, conn := newPipeBridge(t, validHello())
	c, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn))
	if err != nil {
		t.Fatal(err)
	}
	defer closeClient(t, c)
	if err := c.Ack(t.Context(), "wamid.x"); err != nil {
		t.Fatalf("ack: %v", err)
	}
	if err := c.Logout(t.Context()); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if got := b.methods(); !containsMethod(got, "ack") || !containsMethod(got, "logout") {
		t.Fatalf("bridge saw %v, want ack and logout", got)
	}
}

func ackAndLogoutError(t *testing.T) {
	for _, method := range []string{"ack", "logout"} {
		t.Run(method, func(t *testing.T) {
			b, conn := newPipeBridge(t, validHello())
			b.mu.Lock()
			b.failMethod = method
			b.mu.Unlock()
			c, err := gwwa.Connect(t.Context(), gwwa.Stdio(conn))
			if err != nil {
				t.Fatal(err)
			}
			defer closeClient(t, c)
			var rpc *gwwa.RPCError
			if method == "ack" {
				err = c.Ack(t.Context(), "wamid.y")
			} else {
				err = c.Logout(t.Context())
			}
			if !errors.As(err, &rpc) || rpc.Code != -32000 {
				t.Fatalf("%s = %v, want *RPCError", method, err)
			}
		})
	}
}

func containsMethod(methods []string, want string) bool {
	return slices.Contains(methods, want)
}
