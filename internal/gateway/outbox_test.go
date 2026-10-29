package gateway_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

// fakeOutbox is the consumer-side OutboxStore with primary-key semantics.
type fakeOutbox struct {
	rows  map[int64]gateway.Delivery
	byKey map[string]int64
	next  int64
	err   error
}

func newFakeOutbox() *fakeOutbox {
	return &fakeOutbox{rows: map[int64]gateway.Delivery{}, byKey: map[string]int64{}}
}

func (f *fakeOutbox) Enqueue(_ context.Context, d *gateway.Delivery) (int64, bool, error) {
	if f.err != nil {
		return 0, false, f.err
	}
	if id, ok := f.byKey[d.IdemKey]; ok {
		return id, false, nil
	}
	f.next++
	d.ID, d.Status = f.next, gateway.StatusQueued
	f.rows[d.ID], f.byKey[d.IdemKey] = *d, d.ID
	return d.ID, true, nil
}

func (f *fakeOutbox) Claim(_ context.Context, now time.Time, limit int) ([]gateway.Delivery, error) {
	out := make([]gateway.Delivery, 0, limit)
	for id := int64(1); id <= f.next && len(out) < limit; id++ {
		d := f.rows[id]
		if d.Status != gateway.StatusQueued || d.NextAt.After(now) {
			continue
		}
		d.Status = gateway.StatusSending
		d.Attempts++
		f.rows[id] = d
		out = append(out, d)
	}
	return out, nil
}

func (f *fakeOutbox) Sent(_ context.Context, id int64, ref string, _ time.Time) error {
	d := f.rows[id]
	d.Status, d.ExternalRef = gateway.StatusSent, ref
	f.rows[id] = d
	return nil
}

func (f *fakeOutbox) Retry(_ context.Context, id int64, _ string, at time.Time) error {
	d := f.rows[id]
	d.Status, d.NextAt = gateway.StatusQueued, at
	f.rows[id] = d
	return nil
}

// errNotSending mirrors gwstore: a terminal transition only applies to a row
// that is still sending.
var errNotSending = errors.New("fake outbox: delivery not sending")

func (f *fakeOutbox) Fail(_ context.Context, id int64, _ string) error {
	d, ok := f.rows[id]
	if !ok || d.Status != gateway.StatusSending {
		return errNotSending
	}
	d.Status = gateway.StatusFailed
	f.rows[id] = d
	return nil
}

func (f *fakeOutbox) len() int { return len(f.rows) }

func message(chat string) *gateway.Message {
	return &gateway.Message{Platform: "telegram", ChatID: chat, Key: "k1", Body: "hello"}
}

func TestOutboxEnqueueIdempotent(t *testing.T) {
	store := newFakeOutbox()
	box, err := gateway.NewOutbox(store)
	if err != nil {
		t.Fatal(err)
	}
	first, err := box.Enqueue(t.Context(), message("c1"))
	if err != nil || first.Duplicate || first.ID == 0 {
		t.Fatalf("first: %+v err=%v", first, err)
	}
	for range 2 {
		again, aerr := box.Enqueue(t.Context(), message("c1"))
		if aerr != nil || !again.Duplicate || again.ID != first.ID {
			t.Fatalf("replay: %+v err=%v", again, aerr)
		}
	}
	if store.len() != 1 {
		t.Fatalf("outbox length = %d, want 1", store.len())
	}

	// The same caller key in another chat is a different delivery.
	other, err := box.Enqueue(t.Context(), message("c2"))
	if err != nil || other.Duplicate || other.ID == first.ID {
		t.Fatalf("other chat: %+v err=%v", other, err)
	}
	if store.len() != 2 {
		t.Fatalf("outbox length = %d, want 2", store.len())
	}
}

// TestOutboxRetryUpdatesRow: retries update next_at and attempts; they never
// append a new row.
func TestOutboxRetryUpdatesRow(t *testing.T) {
	store := newFakeOutbox()
	box, err := gateway.NewOutbox(store)
	if err != nil {
		t.Fatal(err)
	}
	q, err := box.Enqueue(t.Context(), message("c1"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	claimed, err := box.Claim(t.Context(), t0, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 1 {
		t.Fatalf("claim: %+v err=%v", claimed, err)
	}
	next, rerr := box.Retry(t.Context(), q.ID, 1, "rate limited", 0, t0)
	if rerr != nil || !next.Equal(t0.Add(time.Second)) {
		t.Fatalf("retry next=%v err=%v", next, rerr)
	}
	if store.len() != 1 {
		t.Fatalf("retry duplicated the row: %d", store.len())
	}
	if due, derr := box.Claim(t.Context(), t0.Add(500*time.Millisecond), 10); derr != nil || len(due) != 0 {
		t.Fatalf("early claim: %+v err=%v", due, derr)
	}
	claimed, err = box.Claim(t.Context(), next, 10)
	if err != nil || len(claimed) != 1 || claimed[0].Attempts != 2 {
		t.Fatalf("second claim: %+v err=%v", claimed, err)
	}
	if store.len() != 1 {
		t.Fatalf("outbox length = %d, want 1", store.len())
	}
}

// TestOutboxRetryAfterAndSent: a platform retry_after wins over the
// exponential delay and a successful send records the platform message id.
func TestOutboxRetryAfterAndSent(t *testing.T) {
	store := newFakeOutbox()
	box, err := gateway.NewOutbox(store)
	if err != nil {
		t.Fatal(err)
	}
	q, err := box.Enqueue(t.Context(), message("c1"))
	if err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1_700_000_000, 0)
	if _, cerr := box.Claim(t.Context(), t0, 1); cerr != nil {
		t.Fatal(cerr)
	}
	next, err := box.Retry(t.Context(), q.ID, 2, "429", 30*time.Second, t0)
	if err != nil || !next.Equal(t0.Add(30*time.Second)) {
		t.Fatalf("retry_after next=%v err=%v", next, err)
	}
	if _, err := box.Claim(t.Context(), next, 1); err != nil {
		t.Fatal(err)
	}
	if err := box.Sent(t.Context(), q.ID, "msg-1", t0); err != nil {
		t.Fatal(err)
	}
	if d := store.rows[q.ID]; d.Status != gateway.StatusSent || d.ExternalRef != "msg-1" {
		t.Fatalf("sent row = %+v", d)
	}
}

func TestBackoff(t *testing.T) {
	at := time.Unix(1_700_000_000, 0)
	cases := []struct {
		attempt int
		after   time.Duration
		want    time.Duration
	}{
		{1, 0, time.Second},
		{2, 0, 2 * time.Second},
		{3, 0, 4 * time.Second},
		{7, 0, 60 * time.Second},
		{20, 0, 60 * time.Second},
		{1, 30 * time.Second, 30 * time.Second},
		{1, 5 * time.Minute, 5 * time.Minute},
	}
	for _, c := range cases {
		if got := gateway.Backoff(at, c.attempt, c.after).Sub(at); got != c.want {
			t.Errorf("Backoff(attempt=%d, after=%v) = %v, want %v", c.attempt, c.after, got, c.want)
		}
	}
}

func TestOutboxValidation(t *testing.T) {
	if _, err := gateway.NewOutbox(nil); !errors.Is(err, gateway.ErrNoOutboxStore) {
		t.Fatalf("NewOutbox(nil) = %v", err)
	}
	box, err := gateway.NewOutbox(newFakeOutbox())
	if err != nil {
		t.Fatal(err)
	}
	bad := []gateway.Message{
		{Platform: "", ChatID: "c", Key: "k", Body: "b"},
		{Platform: "telegram", ChatID: "", Key: "k", Body: "b"},
		{Platform: "telegram", ChatID: "c", Key: "", Body: "b"},
		{Platform: "telegram", ChatID: "c", Key: "k"},
		{Platform: "telegram", ChatID: "c\n", Key: "k", Body: "b"},
	}
	for i := range bad {
		if _, err := box.Enqueue(t.Context(), &bad[i]); !errors.Is(err, gateway.ErrInvalidDelivery) {
			t.Errorf("Enqueue(%+v) = %v, want ErrInvalidDelivery", bad[i], err)
		}
	}
	if _, err := box.Claim(t.Context(), time.Now(), 0); !errors.Is(err, gateway.ErrInvalidDelivery) {
		t.Errorf("Claim(limit=0) = %v", err)
	}
	if got := gateway.DeliveryKey("telegram", "c1", "k1"); got != "telegram\x1fc1\x1fk1" {
		t.Errorf("DeliveryKey = %q", got)
	}
}

func TestLedgerEditsOneMessage(t *testing.T) {
	l := gateway.NewLedger()
	if _, ok := l.RecordEdit("telegram", "c1", "k1"); ok {
		t.Fatal("edited before sending")
	}
	r, err := l.RecordSent("telegram", "c1", "k1", "msg-1")
	if err != nil || r.MessageID != "msg-1" || r.Edits != 0 {
		t.Fatalf("send: %+v err=%v", r, err)
	}
	if _, err := l.RecordSent("telegram", "c1", "k1", "msg-1"); err != nil {
		t.Fatalf("idempotent send: %v", err)
	}
	for i := 1; i <= 2; i++ {
		r, ok := l.RecordEdit("telegram", "c1", "k1")
		if !ok || r.MessageID != "msg-1" || r.Edits != i {
			t.Fatalf("edit %d: %+v ok=%v", i, r, ok)
		}
	}
	if _, err := l.RecordSent("telegram", "c1", "k1", "msg-2"); !errors.Is(err, gateway.ErrLedgerConflict) {
		t.Fatalf("second message = %v, want ErrLedgerConflict", err)
	}
}

func TestLedgerIsolatesChats(t *testing.T) {
	l := gateway.NewLedger()
	if _, err := l.RecordSent("telegram", "c1", "k1", "msg-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := l.RecordEdit("telegram", "c1", "k1"); !ok {
		t.Fatal("edit after send was not applied")
	}
	if _, ok := l.Receipt("telegram", "c2", "k1"); ok {
		t.Fatal("chat c2 sees chat c1 delivery")
	}
	if r, err := l.RecordSent("telegram", "c2", "k1", "msg-3"); err != nil || r.MessageID != "msg-3" {
		t.Fatalf("c2 send: %+v err=%v", r, err)
	}
	if r, _ := l.Receipt("telegram", "c1", "k1"); r.MessageID != "msg-1" || r.Edits != 1 {
		t.Fatalf("c1 receipt overwritten: %+v", r)
	}
}

// newTestOutbox wires a fake store and an outbox for the failure tests.
func newTestOutbox(t *testing.T) (*fakeOutbox, *gateway.Outbox) {
	t.Helper()
	store := newFakeOutbox()
	box, err := gateway.NewOutbox(store)
	if err != nil {
		t.Fatal(err)
	}
	return store, box
}

// enqueueOne queues message("c1") and returns its handle.
func enqueueOne(t *testing.T, box *gateway.Outbox) gateway.Queued {
	t.Helper()
	q, err := box.Enqueue(t.Context(), message("c1"))
	if err != nil {
		t.Fatal(err)
	}
	return q
}

// singleClaim claims exactly one delivery at now and checks its attempt count.
func singleClaim(t *testing.T, box *gateway.Outbox, now time.Time, wantAttempts int) gateway.Delivery {
	t.Helper()
	claimed, err := box.Claim(t.Context(), now, 1)
	if err != nil || len(claimed) != 1 {
		t.Fatalf("claim at %v = %+v err=%v, want one", now, claimed, err)
	}
	if claimed[0].Attempts != wantAttempts {
		t.Fatalf("attempts = %d, want %d", claimed[0].Attempts, wantAttempts)
	}
	return claimed[0]
}

// requireNoClaim asserts the queue has nothing due at now.
func requireNoClaim(t *testing.T, box *gateway.Outbox, now time.Time) {
	t.Helper()
	if due, err := box.Claim(t.Context(), now, 10); err != nil || len(due) != 0 {
		t.Fatalf("claim at %v = %+v err=%v, want none", now, due, err)
	}
}

// requireStatus asserts one row's status and returns it.
func requireStatus(t *testing.T, store *fakeOutbox, id int64, want string) gateway.Delivery {
	t.Helper()
	d := store.rows[id]
	if d.Status != want {
		t.Fatalf("delivery %d status = %q, want %q", id, d.Status, want)
	}
	return d
}

// requireOneRow asserts the outbox holds exactly one row.
func requireOneRow(t *testing.T, store *fakeOutbox) {
	t.Helper()
	if n := store.len(); n != 1 {
		t.Fatalf("outbox length = %d, want 1", n)
	}
}

// TestOutboxRetryKeepsQueued: a transient send error is a retry, not a
// permanent failure; the row stays queued and the next Claim takes it again.
func TestOutboxRetryKeepsQueued(t *testing.T) {
	store, box := newTestOutbox(t)
	q := enqueueOne(t, box)
	t0 := time.Unix(1_700_000_000, 0)
	singleClaim(t, box, t0, 1)
	next, err := box.Retry(t.Context(), q.ID, 1, "connection reset", 0, t0)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, store, q.ID, gateway.StatusQueued)
	requireOneRow(t, store)
	if reclaimed := singleClaim(t, box, next, 2); reclaimed.ID != q.ID {
		t.Fatalf("reclaimed id = %d, want %d", reclaimed.ID, q.ID)
	}
}

// TestOutboxFailTerminal: a delivery that keeps failing is retried (queued,
// never failed) until the last allowed attempt, then Fail closes it as failed.
// Fail keeps the attempt count of the last Claim and no later Claim returns
// the message again.
func TestOutboxFailTerminal(t *testing.T) {
	store, box := newTestOutbox(t)
	q := enqueueOne(t, box)
	t0 := time.Unix(1_700_000_000, 0)
	singleClaim(t, box, t0, 1)

	// Attempt 1 fails transiently: the row goes back to the queue, not failed.
	next, err := box.Retry(t.Context(), q.ID, 1, "503 unavailable", 0, t0)
	if err != nil {
		t.Fatal(err)
	}
	requireStatus(t, store, q.ID, gateway.StatusQueued)

	// Attempt 2 exhausts the limit and is non-retryable: Fail is terminal.
	singleClaim(t, box, next, 2)
	if err := box.Fail(t.Context(), q.ID, "banned: non-retryable"); err != nil {
		t.Fatal(err)
	}
	d := requireStatus(t, store, q.ID, gateway.StatusFailed)
	if d.Attempts != 2 {
		t.Fatalf("attempts = %d, want 2 (Fail must not rewrite the claim count)", d.Attempts)
	}
	requireNoClaim(t, box, next.Add(24*time.Hour))
	requireOneRow(t, store)
}

// TestOutboxFailIdempotent: a repeated Fail cannot duplicate the row, reopen it
// or add attempts. The store refuses the second transition because the row is
// no longer sending, and replaying the original message stays a duplicate.
func TestOutboxFailIdempotent(t *testing.T) {
	store, box := newTestOutbox(t)
	q := enqueueOne(t, box)
	t0 := time.Unix(1_700_000_000, 0)
	singleClaim(t, box, t0, 1)
	if err := box.Fail(t.Context(), q.ID, "blocked by user"); err != nil {
		t.Fatal(err)
	}
	before := store.rows[q.ID]
	if ferr := box.Fail(t.Context(), q.ID, "blocked by user"); !errors.Is(ferr, errNotSending) {
		t.Fatalf("second Fail = %v, want not-sending", ferr)
	}
	if after := store.rows[q.ID]; after != before {
		t.Fatalf("second Fail changed the row: %+v -> %+v", before, after)
	}
	requireOneRow(t, store)
	replay, err := box.Enqueue(t.Context(), message("c1"))
	if err != nil || !replay.Duplicate || replay.ID != q.ID {
		t.Fatalf("replay after fail: %+v err=%v", replay, err)
	}
	requireOneRow(t, store)
	requireNoClaim(t, box, t0.Add(24*time.Hour))
}
