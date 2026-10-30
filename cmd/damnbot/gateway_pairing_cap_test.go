package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/gwstore"
	"github.com/damnbot/damnbot/internal/gateway"
)

func pairHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

// TestPairingPlatformCap is SEC-27: the eleventh pending code evicts the oldest
// and raises one owner notice; a second prune inside ten minutes is silent.
func TestPairingPlatformCap(t *testing.T) {
	db := recoveryDB(t)
	store := gwstore.Store{DB: db}
	now := time.UnixMilli(1_700_000_000_000)
	for i := range pairCapPlatform {
		if err := recordPairingPending(t.Context(), store, "telegram", fmt.Sprintf("src%d", i), fmt.Sprintf("00000%d", i), now.Add(time.Duration(i)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	evicted, notice, err := prunePairingPending(t.Context(), store, "telegram", "fresh", now.Add(time.Minute))
	if err != nil || evicted != 1 || !notice {
		t.Fatalf("full prune = %d/%v/%v, want one eviction and one notice", evicted, notice, err)
	}
	var pending int
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM module_state WHERE module_id = 'gateway' AND key LIKE 'pairsrc:telegram:%'`, nil, &pending)
	}); qerr != nil || pending != pairCapPlatform-1 {
		t.Fatalf("pending = %d, %v; want %d", pending, qerr, pairCapPlatform-1)
	}
	var old int
	if qerr := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM module_state WHERE module_id = 'gateway' AND key = ?1`,
			[]any{pairCodeKey("telegram", pairHash("000000"))}, &old)
	}); qerr != nil || old != 0 {
		t.Fatalf("oldest code row = %d, %v; want evicted", old, qerr)
	}
	if _, again, qerr := prunePairingPending(t.Context(), store, "telegram", "fresh", now.Add(2*time.Minute)); qerr != nil || again {
		t.Fatalf("second prune notice = %v, %v; want suppressed", again, qerr)
	}
}

// TestPairingSourceCap: one pending code of the same source is evicted before
// a second code of that source is issued, independent of the platform cap.
func TestPairingSourceCap(t *testing.T) {
	db := recoveryDB(t)
	store := gwstore.Store{DB: db}
	now := time.UnixMilli(1_700_000_000_000)
	if err := recordPairingPending(t.Context(), store, "discord", "cli", "123456", now); err != nil {
		t.Fatal(err)
	}
	evicted, notice, err := prunePairingPending(t.Context(), store, "discord", "cli", now.Add(time.Minute))
	if err != nil || evicted != 1 || notice {
		t.Fatalf("per-source prune = %d/%v/%v, want the source's old code evicted", evicted, notice, err)
	}
	// Negative control: a different source is not charged for another one.
	evicted, _, err = prunePairingPending(t.Context(), store, "discord", "web", now.Add(2*time.Minute))
	if err != nil || evicted != 0 {
		t.Fatalf("other source prune = %d, %v; want no-op", evicted, err)
	}
}

// TestPairingOwnerNoticeOnce: the owner delivery is one outbox row for the same
// notice window, even if two CLI runs race.
func TestPairingOwnerNoticeOnce(t *testing.T) {
	db := recoveryDB(t)
	store := gwstore.Store{DB: db}
	t.Setenv("DAMNBOT_GATEWAY_OWNER", "telegram:42")
	now := time.UnixMilli(1_700_000_000_000)
	notifyPairingOwner(t.Context(), store, "telegram", now)
	notifyPairingOwner(t.Context(), store, "telegram", now.Add(time.Minute))
	var n int
	if err := db.Write(t.Context(), func(tx *ctxdb.Tx) error {
		return tx.Get(`SELECT count(*) FROM gateway_deliveries WHERE idem_key = ?1`,
			[]any{gateway.DeliveryKey("telegram", "42", fmt.Sprintf("sys:pending-pairing-full:telegram:%d", now.Truncate(pairNoticeEvery).Unix()))}, &n)
	}); err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Fatalf("owner notices = %d, want 1", n)
	}
}
