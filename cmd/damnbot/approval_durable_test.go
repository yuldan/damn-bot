package main

import (
	"context"
	"sync"
	"testing"
	"time"
)

type memLedger struct {
	mu sync.Mutex
	m  map[string]approvalRecord
}

func newMemLedger() *memLedger { return &memLedger{m: map[string]approvalRecord{}} }

func (l *memLedger) Load(_ context.Context, id string) (approvalRecord, bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	rec, ok := l.m[id]
	return rec, ok, nil
}

func (l *memLedger) Put(_ context.Context, id string, rec approvalRecord) (bool, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, ok := l.m[id]; ok {
		return false, nil
	}
	l.m[id] = rec
	return true, nil
}

func newDurable(t *testing.T, now *time.Time) (*durableApprovals, *int) {
	t.Helper()
	asks := 0
	d := &durableApprovals{
		ledger: newMemLedger(), now: func() time.Time { return *now }, ttl: time.Minute,
		newNonce: func() string { return "nonce-1" },
	}
	return d, &asks
}

// TestDurableApproveReplayNoSecondPrompt: approve is recorded and marked
// executed, so a resumed request neither prompts nor allows a second run.
func TestDurableApproveReplayNoSecondPrompt(t *testing.T) {
	now := time.Now()
	d, asks := newDurable(t, &now)
	ask := func(context.Context, string) (bool, error) { *asks++; return true, nil }
	if allow, err := d.decide(t.Context(), "r1", ask); err != nil || !allow {
		t.Fatalf("first = %v, %v; want allow", allow, err)
	}
	if allow, err := d.decide(t.Context(), "r1", ask); err != nil || allow {
		t.Fatalf("replay = %v, %v; want deny without prompt", allow, err)
	}
	if *asks != 1 {
		t.Fatalf("prompts = %d, want 1", *asks)
	}
}

// TestDurableDenyReplayStaysDenied: deny survives and is never re-asked.
func TestDurableDenyReplayStaysDenied(t *testing.T) {
	now := time.Now()
	d, asks := newDurable(t, &now)
	ask := func(context.Context, string) (bool, error) { *asks++; return false, nil }
	allow, err := d.decide(t.Context(), "r1", ask)
	if err != nil || allow {
		t.Fatalf("first = %v, %v; want deny", allow, err)
	}
	allow, err = d.decide(t.Context(), "r1", ask)
	if err != nil || allow || *asks != 1 {
		t.Fatalf("replay allow=%v err=%v prompts=%d", allow, err, *asks)
	}
}

// TestDurableExpiredCannotBeApproved: after the TTL the record denies and the
// old decision can never be turned into an approval.
func TestDurableExpiredCannotBeApproved(t *testing.T) {
	now := time.Now()
	d, asks := newDurable(t, &now)
	ask := func(context.Context, string) (bool, error) { *asks++; return false, nil }
	if _, err := d.decide(t.Context(), "r1", ask); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2 * time.Minute)
	if allow, err := d.decide(t.Context(), "r1", ask); err != nil || allow {
		t.Fatalf("expired = %v, %v; want deny", allow, err)
	}
	if *asks != 1 {
		t.Fatalf("expired record prompted again: %d", *asks)
	}
}

// TestDurableParallelSessionsIndependent: equal request ids in two sessions
// are distinct keys and do not share decisions.
func TestDurableParallelSessionsIndependent(t *testing.T) {
	now := time.Now()
	d, _ := newDurable(t, &now)
	askAllow := func(context.Context, string) (bool, error) { return true, nil }
	askDeny := func(context.Context, string) (bool, error) { return false, nil }
	idA := approvalRequestID("sess-a", "same prompt", "shell", "cmd")
	idB := approvalRequestID("sess-b", "same prompt", "shell", "cmd")
	if idA == idB {
		t.Fatal("session not part of request id")
	}
	allowA, err := d.decide(t.Context(), idA, askAllow)
	if err != nil || !allowA {
		t.Fatalf("session A = %v, %v", allowA, err)
	}
	allowB, err := d.decide(t.Context(), idB, askDeny)
	if err != nil || allowB {
		t.Fatalf("session B = %v, %v; want deny", allowB, err)
	}
}
