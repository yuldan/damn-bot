package gwstore

import (
	"errors"
	"testing"
	"time"
)

func delivery(chat, key, body string) *Delivery {
	return &Delivery{IdemKey: "telegram\x1f" + chat + "\x1f" + key, Platform: "telegram", ChatID: chat, Body: body}
}

func TestEnqueueDeliveryIdempotent(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	d := delivery("c1", "k1", "hello")

	id, inserted, err := store.EnqueueDelivery(t.Context(), d)
	if err != nil || !inserted || id == 0 {
		t.Fatalf("first: id=%d inserted=%v err=%v", id, inserted, err)
	}
	again, inserted, rerr := store.EnqueueDelivery(t.Context(), d)
	if rerr != nil || inserted || again != id {
		t.Fatalf("replay: id=%d inserted=%v err=%v", again, inserted, rerr)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 1 {
		t.Fatalf("rows = %d, want 1", n)
	}

	changed := delivery("c1", "k1", "different")
	if _, _, cerr := store.EnqueueDelivery(t.Context(), changed); !errors.Is(cerr, ErrDeliveryConflict) {
		t.Fatalf("conflict = %v, want ErrDeliveryConflict", cerr)
	}
	other := delivery("c2", "k1", "hello")
	otherID, inserted, oerr := store.EnqueueDelivery(t.Context(), other)
	if oerr != nil || !inserted || otherID == id {
		t.Fatalf("other chat: id=%d inserted=%v err=%v", otherID, inserted, oerr)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

// TestDeliveryClaimAndSent: claim moves one due row to sending with one attempt;
// sent closes it and later transitions refuse it.
func TestDeliveryClaimAndSent(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	now := time.Now().UnixMilli()

	first, _, err := store.EnqueueDelivery(ctx, delivery("c1", "k1", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	second, _, err := store.EnqueueDelivery(ctx, delivery("c1", "k2", "world"))
	if err != nil {
		t.Fatal(err)
	}
	claimed, err := store.ClaimDeliveries(ctx, now+60_000, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim = %+v err=%v", claimed, err)
	}
	if claimed[0].ID != first || claimed[0].Status != "sending" || claimed[0].Attempts != 1 {
		t.Fatalf("claimed = %+v", claimed[0])
	}
	if again, cerr := store.ClaimDeliveries(ctx, now+60_000, 10); cerr != nil || len(again) != 1 || again[0].ID != second {
		t.Fatalf("second claim = %+v err=%v", again, cerr)
	}
	if err := store.SentDelivery(ctx, first, "msg-1", now+2); err != nil {
		t.Fatal(err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries WHERE id = ? AND status = 'sent'`, first); n != 1 {
		t.Fatal("first not sent")
	}
	if err := store.RetryDelivery(ctx, first, "late", now+3); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("retry of sent = %v, want ErrDeliveryNotFound", err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 2 {
		t.Fatalf("rows = %d, want 2", n)
	}
}

// TestDeliveryRetryAndFail: retry re-queues at next_at without a new row, and
// fail is terminal.
func TestDeliveryRetryAndFail(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	ctx := t.Context()
	now := time.Now().UnixMilli()

	id, _, err := store.EnqueueDelivery(ctx, delivery("c1", "k1", "hello"))
	if err != nil {
		t.Fatal(err)
	}
	if _, cerr := store.ClaimDeliveries(ctx, now+60_000, 1); cerr != nil {
		t.Fatal(cerr)
	}
	if rerr := store.RetryDelivery(ctx, id, "429", now+10_000); rerr != nil {
		t.Fatal(rerr)
	}
	if due, derr := store.ClaimDeliveries(ctx, now+5_000, 10); derr != nil || len(due) != 0 {
		t.Fatalf("claimed before next_at: %+v err=%v", due, derr)
	}
	claimed, err := store.ClaimDeliveries(ctx, now+10_000, 10)
	if err != nil || len(claimed) != 1 || claimed[0].ID != id || claimed[0].Attempts != 2 {
		t.Fatalf("reclaim = %+v err=%v", claimed, err)
	}
	if err := store.FailDelivery(ctx, id, "banned", now+10_001); err != nil {
		t.Fatal(err)
	}
	if err := store.RetryDelivery(ctx, id, "late", now+10_002); !errors.Is(err, ErrDeliveryNotFound) {
		t.Fatalf("retry of failed = %v, want ErrDeliveryNotFound", err)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries`); n != 1 {
		t.Fatalf("rows = %d, want 1 (retry must not duplicate)", n)
	}
	if n := count(t, db, `SELECT count(*) FROM gateway_deliveries WHERE status = 'failed' AND last_error = 'banned'`); n != 1 {
		t.Fatal("failed row missing")
	}
}

func TestEnqueueDeliveryValidation(t *testing.T) {
	db := open(t)
	store := Store{DB: db}
	for _, d := range []Delivery{
		{Platform: "telegram", ChatID: "c", Body: "b"},
		{IdemKey: "k", ChatID: "c", Body: "b"},
		{IdemKey: "k", Platform: "telegram", Body: "b"},
		{IdemKey: "k", Platform: "telegram", ChatID: "c"},
	} {
		if _, _, err := store.EnqueueDelivery(t.Context(), &d); !errors.Is(err, ErrInvalidDelivery) {
			t.Errorf("EnqueueDelivery(%+v) = %v", d, err)
		}
	}
	if _, err := store.ClaimDeliveries(t.Context(), time.Now().UnixMilli(), 0); !errors.Is(err, ErrInvalidDelivery) {
		t.Fatalf("claim limit 0 = %v", err)
	}
}
