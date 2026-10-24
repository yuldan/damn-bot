package ctxdb

import (
	"context"
	"slices"
	"testing"
)

// TestSessionPinsRoundTrip: pins are append-only with last-value-wins, the
// transaction helper and the store helper agree, and bad input fails closed
// (MEM-025, ADR-186).
func TestSessionPinsRoundTrip(t *testing.T) {
	t.Parallel()
	e := newEnv(t)
	s := e.open(1, "tui")
	t.Cleanup(func() {
		if err := s.Close(); err != nil {
			t.Errorf("close: %v", err)
		}
	})
	ctx := context.Background()
	if _, err := s.CreateSession(ctx, Session{ID: "S1", Origin: "headless", Cwd: "/w"}); err != nil {
		t.Fatal(err)
	}
	for _, seq := range []int64{2, 9, 9} { // 9 pinned then released
		if err := s.SetSessionPin(ctx, "S1", seq, seq == 2); err != nil {
			t.Fatal(err)
		}
	}
	var inTx []int64
	if err := s.Write(ctx, func(tx *Tx) error {
		var werr error
		inTx, werr = PinSeqs(tx, "S1")
		return werr
	}); err != nil {
		t.Fatal(err)
	}
	out, err := s.SessionPinSeqs(ctx, "S1")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(out, []int64{2}) || !slices.Equal(inTx, []int64{2}) {
		t.Fatalf("pins = %v (tx %v), want [2]: a released pin must stay out", out, inTx)
	}
	if err := s.SetSessionPin(ctx, "S1", 0, true); err == nil {
		t.Error("seq 0 was accepted")
	}
	if err := s.SetSessionPin(ctx, "missing", 1, true); err == nil {
		t.Error("a missing session accepted a pin")
	}
	if _, err := s.SessionPinSeqs(ctx, "missing"); err == nil {
		t.Error("a missing session returned pins")
	}
	// The append-only trigger rejects an update outside the purge path.
	if err := s.Write(ctx, func(tx *Tx) error {
		_, uerr := tx.Exec("UPDATE session_pins SET pinned = 0 WHERE session_id = 'S1'")
		return uerr
	}); err == nil {
		t.Error("an update of session_pins was allowed")
	}
}
