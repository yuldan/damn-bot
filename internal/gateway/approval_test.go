package gateway_test

import (
	"errors"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/gateway"
)

func approval(t *testing.T, id string, now time.Time) *gateway.ApprovalRequest {
	t.Helper()
	return &gateway.ApprovalRequest{ID: id, Platform: "telegram", ChatID: "c1", UserID: "owner", Summary: "run shell", ExpiresAt: now.Add(10 * time.Minute)}
}

// TestApprovalCallbackIdempotent: the same callback twice applies once; a
// different answer is a conflict, never a second application.
func TestApprovalCallbackIdempotent(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	if err := a.Open(approval(t, "req-1", now)); err != nil {
		t.Fatal(err)
	}
	if d, ok := a.Decision("req-1"); ok || d != gateway.Deny {
		t.Fatalf("unresolved decision = %v, ok=%v", d, ok)
	}
	applied, err := a.Callback("req-1", gateway.AllowOnce)
	if err != nil || !applied {
		t.Fatalf("first allow: applied=%v err=%v", applied, err)
	}
	applied, err = a.Callback("req-1", gateway.AllowOnce)
	if err != nil || applied {
		t.Fatalf("duplicate allow: applied=%v err=%v", applied, err)
	}
	if d, ok := a.Decision("req-1"); !ok || d != gateway.AllowOnce {
		t.Fatalf("decision = %v, ok=%v", d, ok)
	}
	if applied, err = a.Callback("req-1", gateway.Deny); !errors.Is(err, gateway.ErrApprovalConflict) || applied {
		t.Fatalf("conflicting callback: applied=%v err=%v", applied, err)
	}
	if d, _ := a.Decision("req-1"); d != gateway.AllowOnce {
		t.Fatalf("conflict changed the decision to %v", d)
	}
}

// TestApprovalTimeoutDenies: at expiry the request resolves Deny and a late
// allow callback is not applied.
func TestApprovalTimeoutDenies(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	req := approval(t, "req-2", now)
	if err := a.Open(req); err != nil {
		t.Fatal(err)
	}
	now = req.ExpiresAt
	if d, ok := a.Decision("req-2"); !ok || d != gateway.Deny {
		t.Fatalf("expired decision = %v, ok=%v", d, ok)
	}
	if applied, err := a.Callback("req-2", gateway.AllowOnce); err != nil || applied {
		t.Fatalf("late allow: applied=%v err=%v", applied, err)
	}
	if d, _ := a.Decision("req-2"); d != gateway.Deny {
		t.Fatalf("late allow changed decision to %v", d)
	}
	if applied, err := a.Callback("req-2", gateway.Deny); err != nil || applied {
		t.Fatalf("duplicate timeout deny: applied=%v err=%v", applied, err)
	}
}

func TestApprovalRejectsInvalid(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	a := gateway.NewApprovals(func() time.Time { return now })
	if err := a.Open(approval(t, "req", now)); err != nil {
		t.Fatal(err)
	}
	if err := a.Open(approval(t, "req", now)); !errors.Is(err, gateway.ErrApprovalExists) {
		t.Fatalf("duplicate open = %v", err)
	}
	bad := []gateway.ApprovalRequest{
		{Platform: "telegram", ChatID: "c", UserID: "u", ExpiresAt: now},
		{ID: "x", Platform: "telegram", ChatID: "c", UserID: "u"},
	}
	for i := range bad {
		if err := a.Open(&bad[i]); !errors.Is(err, gateway.ErrInvalidApproval) {
			t.Errorf("Open(%+v) = %v", bad[i], err)
		}
	}
	if _, err := a.Callback("missing", gateway.AllowOnce); !errors.Is(err, gateway.ErrApprovalNotFound) {
		t.Fatalf("unknown callback = %v", err)
	}
	if _, err := a.Callback("req", gateway.Decision(9)); !errors.Is(err, gateway.ErrInvalidDecision) {
		t.Fatalf("invalid decision = %v", err)
	}
	if _, ok := a.Decision("missing"); ok {
		t.Fatal("unknown id reported a decision")
	}
}
