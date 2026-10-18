package ctxdb

import "context"

// startupRecovery runs in every writer process after the barrier open order
// (SELF-HEALING §3.4): `__system__` row, trash recovery (trigger a), then
// synchronous journal recovery limited to takeover_ok case (a) (P12).
// Pending purges are left to long-lived processes (§11.11.6).
func (s *Store) startupRecovery(ctx context.Context) error {
	// The `__system__` session row the FKs require is created by every
	// writer, idempotently (ADR-093, TS-P02; SELF-HEALING §3.4 step 2).
	err := s.Write(ctx, func(t *Tx) error {
		if _, err := t.Exec(`INSERT INTO sessions(id, origin, cwd, model, config_sha, context_tier, created_at)
			VALUES (?, 'system', '', '', '', '', 0) ON CONFLICT DO NOTHING`, SystemSession); err != nil {
			return err
		}
		return t.recoverTrash("", "")
	})
	if err != nil {
		return err
	}
	_, err = s.RecoverJournals(ctx, false)
	return err
}
