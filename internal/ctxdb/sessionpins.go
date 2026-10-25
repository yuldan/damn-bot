package ctxdb

import (
	"context"
	"fmt"
)

// pinSeqsQ lists the pinned transcript positions of one session: the newest row
// per (session_id, seq) decides, so a later pinned=0 releases the pin (MEM-025).
const pinSeqsQ = `SELECT p.seq FROM session_pins p
	 WHERE p.session_id = ? AND p.pinned = 1
	   AND p.id = (SELECT max(x.id) FROM session_pins x WHERE x.session_id = p.session_id AND x.seq = p.seq)
	 ORDER BY p.seq`

// PinSeqs returns the transcript sequences pinned for the session (MEM-025),
// ascending, inside tx.
func PinSeqs(tx *Tx, sid string) ([]int64, error) {
	out, err := tx.Ints(pinSeqsQ, sid)
	if err != nil {
		return nil, wrap(err)
	}
	return out, nil
}

// SessionPinSeqs is PinSeqs outside a transaction; a missing or purging
// session is ErrNotFound.
func (s *Store) SessionPinSeqs(ctx context.Context, sid string) ([]int64, error) {
	if err := s.Visible(ctx, sid); err != nil {
		return nil, err
	}
	out, err := s.ints(ctx, pinSeqsQ, sid)
	if err != nil {
		return nil, err
	}
	return out, nil
}

// SetSessionPin appends one pin state: pinned=true pins a transcript position,
// false releases it (last value wins). Rows are append-only, so the history of
// pin/unpin stays and the purge owns the deletion.
func (s *Store) SetSessionPin(ctx context.Context, sid string, seq int64, pinned bool) error {
	if seq <= 0 {
		return fmt.Errorf("ctxdb: pin seq %d: want a positive transcript sequence", seq)
	}
	value := 0
	if pinned {
		value = 1
	}
	return s.Write(ctx, func(t *Tx) error {
		var n int
		if err := t.Get(`SELECT count(*) FROM sessions s WHERE s.id = ? AND `+notPurging("s.id"), []any{sid}, &n); err != nil {
			return err
		}
		if n == 0 {
			return fmt.Errorf("ctxdb: session %s: %w", sid, ErrNotFound)
		}
		_, err := t.Exec(`INSERT INTO session_pins(session_id, seq, pinned, created_at) VALUES (?,?,?,?)`,
			sid, seq, value, t.s.clk.WallMs())
		return wrap(err)
	})
}
