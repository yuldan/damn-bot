package gwstore

import (
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

func TestSetIdentityIdempotent(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	for range 2 {
		if err := store.SetIdentity(ctx, "telegram", "42", "owner", "allowlisted", 10); err != nil {
			t.Fatal(err)
		}
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_identities`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}
}

func TestRevokePreservesRole(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	if err := store.SetIdentity(ctx, "telegram", "42", "owner", "allowlisted", 10); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, "telegram", "42", "blocked", 11); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListIdentities(ctx)
	if err != nil || len(list) != 1 {
		t.Fatalf("list = %+v err=%v", list, err)
	}
	if list[0].Role != "owner" || list[0].Status != "blocked" {
		t.Fatalf("revoke changed role or status: %+v", list[0])
	}
}

func TestRevokeUnknownCreatesBlocked(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	if err := store.SetStatus(ctx, "telegram", "99", "blocked", 12); err != nil {
		t.Fatal(err)
	}
	list, err := store.ListIdentities(ctx)
	if err != nil || len(list) != 1 || list[0].ExternalID != "99" || list[0].Role != "user" || list[0].Status != "blocked" {
		t.Fatalf("unknown revoke = %+v err=%v", list, err)
	}
	counts, err := store.Counts(ctx)
	if err != nil || counts.Total != 1 || counts.Blocked != 1 {
		t.Fatalf("counts = %+v err=%v", counts, err)
	}
}

func TestEnsureIdentityNeverOverwrites(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	inserted, err := store.EnsureIdentity(ctx, "telegram", "7", "user", "platform_authenticated", 1)
	if err != nil || !inserted {
		t.Fatalf("ensure: inserted=%v err=%v", inserted, err)
	}
	inserted, err = store.EnsureIdentity(ctx, "telegram", "7", "owner", "cryptographic", 2)
	if err != nil || inserted {
		t.Fatalf("second ensure: inserted=%v err=%v", inserted, err)
	}
	list, lerr := store.ListIdentities(ctx)
	if lerr != nil || len(list) != 1 || list[0].Role != "user" || list[0].Strength != "platform_authenticated" {
		t.Fatalf("existing row overwritten: %+v err=%v", list, lerr)
	}
}

func TestPairingLifecycle(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	now := time.UnixMilli(1_700_000_000_000)

	p, err := store.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5)
	if err != nil || p.Role != "owner" || len(p.Code) != 6 {
		t.Fatalf("bootstrap pair = %+v err=%v", p, err)
	}
	id, err := store.RedeemPairing(ctx, "telegram", p.Code, "42", "platform_authenticated", now.UnixMilli()+1)
	if err != nil || id.Role != "owner" || id.Status != "allowlisted" {
		t.Fatalf("redeem = %+v err=%v", id, err)
	}
	if c := count(t, db, `SELECT count(*) FROM gateway_identities WHERE role = 'owner' AND status = 'allowlisted'`); c != 1 {
		t.Fatalf("owner rows = %d", c)
	}
	if _, err := store.RedeemPairing(ctx, "telegram", p.Code, "42", "platform_authenticated", now.UnixMilli()+2); !errors.Is(err, ErrPairingUsed) {
		t.Fatalf("second redeem = %v, want ErrPairingUsed", err)
	}
}

func TestPairingExpired(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	now := time.UnixMilli(1_700_000_000_000)
	p, err := store.CreatePairing(t.Context(), "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemPairing(t.Context(), "telegram", p.Code, "42", "platform_authenticated", now.Add(10*time.Minute).UnixMilli()); !errors.Is(err, ErrPairingExpired) {
		t.Fatalf("expired redeem = %v, want ErrPairingExpired", err)
	}
}

func TestPairingQuotaAndOwnerRequired(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	now := time.UnixMilli(1_700_000_000_000)
	if _, err := store.EnsureIdentity(ctx, "telegram", "owner", "owner", "platform_authenticated", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	for i := range 5 {
		p, err := store.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5)
		if err != nil || p.Role != "user" {
			t.Fatalf("create %d: %+v err=%v", i, p, err)
		}
	}
	if _, err := store.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5); !errors.Is(err, ErrPairingQuota) {
		t.Fatalf("sixth = %v, want ErrPairingQuota", err)
	}
	if _, err := store.CreatePairing(ctx, "telegram", "cli", now.Add(time.Hour+time.Millisecond).UnixMilli(), 10*time.Minute, 5); err != nil {
		t.Fatalf("after window = %v", err)
	}

	// Only users, no owner: pairing is refused.
	db2 := open(t)
	users := Store{DB: db2}
	if err := users.SetIdentity(ctx, "telegram", "onlyuser", "user", "allowlisted", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if _, err := users.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5); !errors.Is(err, ErrOwnerRequired) {
		t.Fatalf("no owner = %v, want ErrOwnerRequired", err)
	}
}

func TestPairingRefusesWeakAndBlocked(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	now := time.UnixMilli(1_700_000_000_000)
	if _, err := store.EnsureIdentity(ctx, "telegram", "owner", "owner", "platform_authenticated", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	p, err := store.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, rerr := store.RedeemPairing(ctx, "telegram", p.Code, "weak", "weak", now.UnixMilli()+1); !errors.Is(rerr, ErrInvalidIdentity) {
		t.Fatalf("weak = %v", rerr)
	}
	if serr := store.SetIdentity(ctx, "telegram", "blocked", "user", "blocked", now.UnixMilli()); serr != nil {
		t.Fatal(serr)
	}
	p2, err := store.CreatePairing(ctx, "telegram", "cli", now.UnixMilli(), 10*time.Minute, 5)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.RedeemPairing(ctx, "telegram", p2.Code, "blocked", "platform_authenticated", now.UnixMilli()+1); !errors.Is(err, ErrIdentityBlocked) {
		t.Fatalf("blocked = %v", err)
	}
}

func TestAdminValidation(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	for _, c := range []struct{ platform, id, role, status string }{
		{"telegram", "-bad", "user", "allowlisted"},
		{"telegram", "ok", "admin", "allowlisted"},
		{"telegram", "ok", "user", "pending"},
		{"", "ok", "user", "allowlisted"},
	} {
		if err := store.SetIdentity(ctx, c.platform, c.id, c.role, c.status, 1); !errors.Is(err, ErrInvalidIdentity) {
			t.Errorf("SetIdentity(%+v) = %v", c, err)
		}
	}
	if err := store.SetStatus(ctx, "telegram", "ok", "pending", 1); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("SetStatus pending = %v", err)
	}
	if _, err := store.CreatePairing(ctx, "telegram", "", 1, time.Minute, 5); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("empty source = %v", err)
	}
	if _, err := store.RedeemPairing(ctx, "telegram", "abc", "42", "platform_authenticated", 1); !errors.Is(err, ErrInvalidIdentity) {
		t.Fatalf("short code = %v", err)
	}
}

// TestStatusSummarisesOneDatabase proves the read-only summary counts every
// identity class plus inbound/outbox/lease state (E-53 keeps Status reachable).
func TestStatusSummarisesOneDatabase(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	seedStatus(t, db, store)
	st, err := store.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if st.Identities.Total != 4 || st.Identities.Owners != 1 || st.Identities.Allowlisted != 2 ||
		st.Identities.Blocked != 1 || st.Identities.Pending != 1 {
		t.Fatalf("identities = %+v", st.Identities)
	}
	if st.Inbound != 1 || st.Deliveries["queued"] != 1 || st.Leases != 1 {
		t.Fatalf("status = %+v", st)
	}
}

func seedStatus(t *testing.T, db *ctxdb.Store, store Store) {
	t.Helper()
	ctx := t.Context()
	now := time.UnixMilli(1_700_000_000_000).UnixMilli()
	if _, err := store.EnsureIdentity(ctx, "telegram", "owner", "owner", "platform_authenticated", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetIdentity(ctx, "telegram", "user", "user", "allowlisted", now); err != nil {
		t.Fatal(err)
	}
	if err := store.SetStatus(ctx, "telegram", "blocked", "blocked", now); err != nil {
		t.Fatal(err)
	}
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO gateway_identities(platform, external_id, role, status, strength, updated_at)
			VALUES ('telegram', 'pending', 'user', 'pending', 'cryptographic', 1)`)
		if xerr != nil {
			return fmt.Errorf("pending identity: %w", xerr)
		}
		_, xerr = tx.Exec(`INSERT INTO gateway_deliveries(idem_key, platform, chat_id, body, status, attempts, next_at, updated_at)
			VALUES ('telegram:c:k', 'telegram', 'c', 'hi', 'queued', 0, 1, 1)`)
		if xerr != nil {
			return fmt.Errorf("queued delivery: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Record(ctx, &Inbound{Platform: "telegram", ChatID: "c", SenderID: "user", UpdateID: "u:1", Payload: `{"text":"hi"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateSession(ctx, ctxdb.Session{ID: "S", Origin: "gateway", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
}
