package gwstore

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestF7ECPLAT008PairingLock is EC-PLAT-008: two processes (two independent
// Store handles, each with its own writer connection) redeeming the same code
// never both succeed, and no update is lost.
func TestF7ECPLAT008PairingLock(t *testing.T) {
	dir := t.TempDir()
	a, b := openDir(t, dir, 0), openDir(t, dir, 0)
	t.Cleanup(func() { closeStore(t, b) })
	t.Cleanup(func() { closeStore(t, a) })
	now := time.Now().UnixMilli()
	code, err := (Store{DB: a}).CreatePairing(t.Context(), "telegram", "cli", now, 10*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	errs := make([]error, 2)
	for i, db := range []*ctxdb.Store{a, b} {
		wg.Go(func() {
			_, errs[i] = (Store{DB: db}).RedeemPairing(t.Context(), "telegram", code.Code, "42", "platform_authenticated", now+1)
		})
	}
	wg.Wait()
	ok, used := 0, 0
	for _, err := range errs {
		switch {
		case err == nil:
			ok++
		case errors.Is(err, ErrPairingUsed):
			used++
		default:
			t.Fatalf("redeem error = %v, want nil or ErrPairingUsed", err)
		}
	}
	if ok != 1 || used != 1 {
		t.Fatalf("redeem outcomes ok=%d used=%d, want exactly one winner", ok, used)
	}
}
