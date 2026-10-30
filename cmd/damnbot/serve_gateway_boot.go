package main

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// reclaimDeliveries returns every delivery a previous process left in
// 'sending' to the queue. The serve flock guarantees no other worker shares
// the data dir, so each such row is orphaned by a crash; the adapter checks
// its idempotency key before a resend, so a row whose message did reach the
// platform is recorded again without sending a duplicate (SELF-HEALING §3.4).
func reclaimDeliveries(ctx context.Context, db *ctxdb.Store) (int64, error) {
	var n int64
	err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var xerr error
		n, xerr = tx.Exec(`UPDATE gateway_deliveries SET status = 'queued', next_at = ?1, updated_at = ?1
			WHERE status = 'sending'`, time.Now().UnixMilli())
		if xerr != nil {
			return fmt.Errorf("reclaim update: %w", xerr)
		}
		return nil
	})
	if err != nil {
		return 0, fmt.Errorf("gateway: reclaim deliveries: %w", err)
	}
	return n, nil
}

// reclaimOnStart logs the reclaim; a write failure keeps the daemon down
// rather than letting the outbox silently skip the orphaned rows.
func reclaimOnStart(ctx context.Context, db *ctxdb.Store, stderr io.Writer) error {
	n, err := reclaimDeliveries(ctx, db)
	if err != nil {
		return err
	}
	if n > 0 {
		sessionsPrintf(stderr, "gateway: reclaimed %d in-flight deliveries\n", n)
	}
	return nil
}
