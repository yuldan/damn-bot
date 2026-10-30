package main

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
)

// TestInboundQueueRoundTrip: an allowlisted update is committed to
// gateway_inbound and represented by one module_state pointer; the text is
// loaded back from the sealed row (not a second copy), and completion removes
// only the pointer.
func TestInboundQueueRoundTrip(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	allowGatewayUser(t, gw, "7")
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7", SenderID: "7", UpdateID: "u:9", Text: "halo"}
	queued, drop, _, err := gw.enqueueInbound(t.Context(), in)
	if err != nil || !queued || drop {
		t.Fatalf("enqueue = %v/%v/%v, want queued", queued, drop, err)
	}
	if n, depthErr := gw.queueDepth(t.Context(), "telegram", "7"); depthErr != nil || n != 1 {
		t.Fatalf("depth = %d, %v; want 1", n, depthErr)
	}
	key, loaded, ok, nextErr := gw.nextQueued(t.Context(), "telegram", "7")
	if nextErr != nil || !ok || loaded.Text != "halo" || loaded.UpdateID != "u:9" {
		t.Fatalf("next = %q %+v %v %v; want the queued text", key, loaded, ok, nextErr)
	}
	if err := gw.completeQueued(t.Context(), key); err != nil {
		t.Fatal(err)
	}
	if n, depthErr := gw.queueDepth(t.Context(), "telegram", "7"); depthErr != nil || n != 0 {
		t.Fatalf("depth after complete = %d, %v; want 0", n, depthErr)
	}
}

// TestInboundQueueReplayDropped is the negative control: a replayed update is
// dropped by the primary-key dedupe and never queued twice.
func TestInboundQueueReplayDropped(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	allowGatewayUser(t, gw, "7")
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7", SenderID: "7", UpdateID: "u:replay", Text: "x"}
	if _, _, _, enqErr := gw.enqueueInbound(t.Context(), in); enqErr != nil {
		t.Fatal(enqErr)
	}
	if queued, drop, _, enqErr := gw.enqueueInbound(t.Context(), in); enqErr != nil || queued || !drop {
		t.Fatalf("replay enqueue = %v/%v/%v, want dropped duplicate", queued, drop, enqErr)
	}
}

// TestInboundQueueOverflowReplies: the 51st queued message is still committed
// to gateway_inbound, is not queued, and receives exactly one __system__ reply.
func TestInboundQueueOverflowReplies(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	allowGatewayUser(t, gw, "7")
	for i := range inboundQueueLimit {
		key := fmt.Sprintf("q:telegram:7:%06d", i)
		value, merr := json.Marshal(map[string]string{"update_id": fmt.Sprintf("u:%d", i)})
		if merr != nil {
			t.Fatal(merr)
		}
		if err := gw.store.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
			if _, xerr := tx.Exec(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('gateway', ?1, ?2, ?3)`,
				key, string(value), time.Now().UnixMilli()); xerr != nil {
				return fmt.Errorf("seed queue: %w", xerr)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7", SenderID: "7", UpdateID: "u:overflow", Text: "ke-51"}
	queued, drop, _, err := gw.enqueueInbound(t.Context(), in)
	if err != nil || queued || !drop {
		t.Fatalf("overflow enqueue = %v/%v/%v, want dropped with notice", queued, drop, err)
	}
	var body string
	if err := gw.store.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body FROM gateway_deliveries WHERE idem_key = ?1`,
			[]any{gateway.DeliveryKey("telegram", "7", "sys:busy:u:overflow")}, &body)
	}); err != nil {
		t.Fatalf("overflow notice missing: %v", err)
	}
	if body == "" {
		t.Fatal("overflow notice is empty")
	}
	// The inbound row is committed despite the overflow.
	var n int
	if err := gw.store.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM gateway_inbound WHERE external_id = 'u:overflow'`, nil, &n)
	}); err != nil || n != 1 {
		t.Fatalf("committed inbound rows = %d, %v; want 1", n, err)
	}
}

// TestChatLeaseBlocksLiveOwnerAndExpires: a live foreign lease refuses the
// drain, an expired one is taken over.
func TestChatLeaseBlocksLiveOwnerAndExpires(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	other := &serveGateway{store: gw.store, dataDir: gw.dataDir + "-other", stderr: gw.stderr}
	ok, err := gw.acquireChatLease(t.Context(), "telegram", "7")
	if err != nil || !ok {
		t.Fatalf("first lease = %v, %v", ok, err)
	}
	if ok, err := other.acquireChatLease(t.Context(), "telegram", "7"); err != nil || ok {
		t.Fatalf("foreign lease = %v, %v; want refused", ok, err)
	}
	if err := gw.store.DB.Write(t.Context(), func(tx *ctxdb.Tx) error {
		if _, xerr := tx.Exec(`UPDATE module_state SET value = '{"owner":"gone","until":1}' WHERE key = 'lease:telegram:7'`); xerr != nil {
			return fmt.Errorf("age lease: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if ok, err := other.acquireChatLease(t.Context(), "telegram", "7"); err != nil || !ok {
		t.Fatalf("expired lease takeover = %v, %v; want acquired", ok, err)
	}
}

// TestResumeDrainsQueuedUpdate: a pointer left by a previous process is
// dispatched on Start and removed only after the turn returns.
func TestResumeDrainsQueuedUpdate(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	allowGatewayUser(t, gw, "7")
	in := &telegram.Inbound{Platform: "telegram", ChatID: "7", SenderID: "7", UpdateID: "u:resume", Text: "lanjut"}
	if queued, _, _, err := gw.enqueueInbound(t.Context(), in); err != nil || !queued {
		t.Fatalf("enqueue = %v, %v", queued, err)
	}
	done := make(chan string, 1)
	gw.run = func(_ context.Context, got *telegram.Inbound, _ *gateway.Ticket) error {
		done <- got.Text
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gw.resumeInbound(ctx)
	select {
	case text := <-done:
		if text != "lanjut" {
			t.Fatalf("resumed text = %q", text)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("resume never drained the queued update")
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if n, depthErr := gw.queueDepth(t.Context(), "telegram", "7"); depthErr == nil && n == 0 {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("queued pointer was not removed after the turn")
		}
		<-time.After(time.Millisecond)
	}
}

// TestDispatchRunsQueuedText exercises the production dispatch path: a plain
// text update is enqueued and the drain lane runs the turn once.
func TestDispatchRunsQueuedText(t *testing.T) {
	gw := newTestGateway(t, "http://127.0.0.1:1")
	allowGatewayUser(t, gw, "7")
	done := make(chan string, 1)
	gw.run = func(_ context.Context, got *telegram.Inbound, _ *gateway.Ticket) error {
		done <- got.Text
		return nil
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	gw.dispatch(ctx, &telegram.Inbound{Platform: "telegram", ChatID: "7", SenderID: "7", UpdateID: "u:dispatch", Text: "halo"})
	select {
	case got := <-done:
		if got != "halo" {
			t.Fatalf("turn text = %q", got)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("dispatch never ran the queued turn")
	}
}
