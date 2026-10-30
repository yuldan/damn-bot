package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/gateway/telegram"
	"github.com/damnbot/damnbot/internal/obs"
)

// TestNextPollInterval pins the idle backoff table: 200 → 500 → 1000 ms,
// and any claimed row resets to 200 ms.
func TestNextPollInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cur     time.Duration
		claimed int
		want    time.Duration
	}{
		{pollFast, 0, pollMid},
		{pollMid, 0, pollIdle},
		{pollIdle, 0, pollIdle},
		{pollIdle, 3, pollFast},
		{pollFast, 1, pollFast},
	}
	for _, c := range cases {
		if got := nextPollInterval(c.cur, c.claimed); got != c.want {
			t.Errorf("nextPollInterval(%s, %d) = %s, want %s", c.cur, c.claimed, got, c.want)
		}
	}
}

// TestRunDeliveryLoopIdleBackoff proves the loop backs off while idle and
// claims an enqueued row within 1 s: the queue latency ceiling of the table.
// testing/synctest drives the timers on a fake clock (E-30).
func TestRunDeliveryLoopIdleBackoff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		calls := make(chan time.Time, 8)
		release := make(chan struct{})
		go runDeliveryLoop(ctx, func(context.Context, time.Time) int {
			calls <- time.Now()
			select {
			case <-release:
				return 1
			default:
				return 0
			}
		})
		next := func() time.Time {
			t.Helper()
			select {
			case at := <-calls:
				return at
			case <-time.After(10 * time.Second):
				t.Fatal("delivery loop stopped polling")
				return time.Time{}
			}
		}

		first := next()
		second := next()
		if gap := second.Sub(first); gap != pollMid {
			t.Fatalf("second poll after %s, want the %s step", gap, pollMid)
		}
		third := next()
		if gap := third.Sub(second); gap != pollIdle {
			t.Fatalf("third poll after %s, want the %s step", gap, pollIdle)
		}

		close(release)
		claimed := next()
		if wait := claimed.Sub(third); wait > pollIdle {
			t.Fatalf("claimed a row %s after enqueue, want <= %s", wait, pollIdle)
		}
		reset := next()
		if gap := reset.Sub(claimed); gap != pollFast {
			t.Fatalf("next poll after a claim = %s, want the %s fast interval", gap, pollFast)
		}
		cancel()
	})
}

// TestClaimDeliveriesIdleNoWriteTx: an empty (or not-yet-due) outbox opens no
// write transaction, only the read-only EXISTS probe (SCORECARD §1.2).
func TestClaimDeliveriesIdleNoWriteTx(t *testing.T) {
	db, writes := watchedGatewayStore(t)
	g := &serveGateway{store: gwstore.Store{DB: db}, metrics: obs.NewMetrics(), stderr: io.Discard}
	ctx := t.Context()

	writes.Store(0)
	for range 20 {
		if n := g.claimDeliveries(ctx, time.Now()); n != 0 {
			t.Fatalf("idle claim returned %d deliveries", n)
		}
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("idle outbox opened %d write transactions, want 0", n)
	}

	now := time.Now().UnixMilli()
	id, _, err := g.store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: "telegram\x1fc1\x1fk1", Platform: "telegram", ChatID: "c1", Body: "hello",
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`UPDATE gateway_deliveries SET next_at = ?2 WHERE id = ?1`, id, now+3_600_000)
		if xerr != nil {
			return fmt.Errorf("age delivery: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatalf("age delivery: %v", err)
	}
	writes.Store(0)
	if n := g.claimDeliveries(ctx, time.Now()); n != 0 {
		t.Fatalf("not-yet-due claim returned %d deliveries", n)
	}
	if n := writes.Load(); n != 0 {
		t.Fatalf("not-yet-due outbox opened %d write transactions, want 0", n)
	}
}

// watchedGatewayStore opens a store whose write transactions are counted.
func watchedGatewayStore(t *testing.T) (*ctxdb.Store, *atomic.Int64) {
	t.Helper()
	var writes atomic.Int64
	db, err := ctxdb.Open(t.Context(), &ctxdb.Options{
		Dir: t.TempDir(), Kind: "run",
		OnTx: func(string, time.Duration) { writes.Add(1) },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close ctxdb: %v", cerr)
		}
	})
	return db, &writes
}

// TestThreadNotFoundFailsAndNotifiesOwner proves GAP-GW-06: a lost forum topic
// fails the delivery (no retry, no parent-chat fallback) and enqueues exactly
// one owner notice. The negative control is the second call: the same notice
// key must not add a row.
func TestThreadNotFoundFailsAndNotifiesOwner(t *testing.T) {
	db, _ := watchedGatewayStore(t)
	store := gwstore.Store{DB: db}
	g := &serveGateway{store: store, owner: "telegram:777", metrics: obs.NewMetrics(), stderr: io.Discard}
	ctx := t.Context()
	id, _, err := store.EnqueueDelivery(ctx, &gwstore.Delivery{
		IdemKey: gateway.DeliveryKey("telegram", "-100", "k1"), Platform: "telegram",
		ChatID: "-100", ThreadID: "9", Body: "rahasia",
	})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := store.ClaimDeliveries(ctx, time.Now().UnixMilli(), 8)
	if err != nil || len(rows) != 1 {
		t.Fatalf("claim = %d rows, %v", len(rows), err)
	}
	g.failThreadDelivery(ctx, &rows[0], telegram.ErrThreadNotFound, time.Now())
	g.failThreadDelivery(ctx, &rows[0], telegram.ErrThreadNotFound, time.Now())

	var status, lastErr string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT status, last_error FROM gateway_deliveries WHERE id = ?1`, []any{id}, &status, &lastErr)
	}); err != nil {
		t.Fatal(err)
	}
	if status != gateway.StatusFailed || !strings.Contains(lastErr, "thread not found") {
		t.Fatalf("delivery status=%q last_error=%q, want failed thread_not_found", status, lastErr)
	}
	var notices int
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM gateway_deliveries WHERE chat_id = '777' AND idem_key LIKE ?1`,
			[]any{"%sys:thread-not-found:" + fmt.Sprint(id)}, &notices)
	}); err != nil {
		t.Fatal(err)
	}
	if notices != 1 {
		t.Fatalf("owner notices = %d, want exactly 1", notices)
	}
	var text string
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT body FROM gateway_deliveries WHERE chat_id = '777'`, nil, &text)
	}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "tidak dialihkan") || !strings.Contains(text, "-100") {
		t.Fatalf("owner notice = %q, want thread id and no-fallback wording", text)
	}
	// Negative control: nothing was routed to the parent chat.
	var parent int
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM gateway_deliveries WHERE chat_id = '-100' AND thread_id = ''`, nil, &parent)
	}); err != nil {
		t.Fatal(err)
	}
	if parent != 0 {
		t.Fatalf("parent-chat deliveries = %d, want 0", parent)
	}
}
