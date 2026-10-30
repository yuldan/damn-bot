package main

import (
	"context"
	"fmt"
	"io"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/obs"
)

// TestNextDeliveryInterval pins the idle backoff table: 200 → 500 → 1000 ms,
// and any claimed row resets to 200 ms.
func TestNextDeliveryInterval(t *testing.T) {
	t.Parallel()
	cases := []struct {
		cur     time.Duration
		claimed int
		want    time.Duration
	}{
		{deliveryPollFast, 0, deliveryPollMid},
		{deliveryPollMid, 0, deliveryPollIdle},
		{deliveryPollIdle, 0, deliveryPollIdle},
		{deliveryPollIdle, 3, deliveryPollFast},
		{deliveryPollFast, 1, deliveryPollFast},
	}
	for _, c := range cases {
		if got := nextDeliveryInterval(c.cur, c.claimed); got != c.want {
			t.Errorf("nextDeliveryInterval(%s, %d) = %s, want %s", c.cur, c.claimed, got, c.want)
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
		if gap := second.Sub(first); gap != deliveryPollMid {
			t.Fatalf("second poll after %s, want the %s step", gap, deliveryPollMid)
		}
		third := next()
		if gap := third.Sub(second); gap != deliveryPollIdle {
			t.Fatalf("third poll after %s, want the %s step", gap, deliveryPollIdle)
		}

		close(release)
		claimed := next()
		if wait := claimed.Sub(third); wait > deliveryPollIdle {
			t.Fatalf("claimed a row %s after enqueue, want <= %s", wait, deliveryPollIdle)
		}
		reset := next()
		if gap := reset.Sub(claimed); gap != deliveryPollFast {
			t.Fatalf("next poll after a claim = %s, want the %s fast interval", gap, deliveryPollFast)
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
