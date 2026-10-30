package main

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/netx/retry"
)

// TestCircuitStoreShared (COST-06): provider_circuits really carries the
// opened circuit across two Shared values (two processes), and a non-primary
// provider row does not make /readyz fail.
func TestCircuitStoreShared(t *testing.T) {
	db := openCronDB(t)
	store := circuitStore{db: db}
	a := &retry.Shared{Provider: "openai", Credential: "sk-a", Store: store, Cache: time.Nanosecond}
	b := &retry.Shared{Provider: "openai", Credential: "sk-a", Store: store, Cache: time.Nanosecond}
	for i := range retry.BreakerFailures {
		g := a
		if i%2 == 1 {
			g = b
		}
		if err := g.Record(t.Context(), retry.Server); err != nil {
			t.Fatal(err)
		}
	}
	c, ok, err := store.Load(t.Context(), "openai", "sk-a")
	if err != nil || !ok || c.State != retry.Open {
		t.Fatalf("stored circuit = %+v ok=%v err=%v", c, ok, err)
	}
	if aerr := b.Allow(t.Context()); !errors.Is(aerr, retry.ErrCircuitOpen) {
		t.Fatalf("second process Allow = %v, want open", aerr)
	}
	open, err := circuitOpen(t.Context(), db, "openai")
	if err != nil || !open {
		t.Fatalf("circuitOpen(openai) = %v, %v", open, err)
	}
	if open, err := circuitOpen(t.Context(), db, "other"); err != nil || open {
		t.Fatalf("circuitOpen(other) = %v, %v", open, err)
	}
}

// TestOldestQueued (REL-17): the outbox readiness probe reads the oldest
// queued row and ignores sending/sent rows.
func TestOldestQueued(t *testing.T) {
	db := openCronDB(t)
	old := time.Now().Add(-10 * time.Minute).UnixMilli()
	err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_deliveries(idem_key, platform, chat_id, thread_id, body, status, updated_at, next_at)
			VALUES ('old', 'telegram', 'c1', '', '{}', 'queued', ?1, ?1), ('new', 'telegram', 'c1', '', '{}', 'sent', ?2, ?2)`,
			old, time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("insert deliveries: %w", xerr)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	at, ok, err := oldestQueued(t.Context(), db)
	if err != nil || !ok || at.UnixMilli() != old {
		t.Fatalf("oldestQueued = %s ok=%v err=%v, want %d", at, ok, err, old)
	}
}
