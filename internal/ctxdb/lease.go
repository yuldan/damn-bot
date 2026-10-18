package ctxdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
)

// Lease timing (SPEC §11.11.2, §11.11.4, §11.11.6), boot-clock ms.
const (
	LeaseTTL       = 10_000
	LeaseHeartbeat = 2_000
	PurgeTTL       = 30_000
	observeGap     = 3_000 // two observations ≥ 3 s apart (monotonic, no suspend)
)

// liveRec is the shape alive() reads; identical for session leases, the
// purge lease and the poller lease (§11.11.2).
type liveRec struct {
	Ident
	HeartbeatAt int64 `json:"heartbeat_at"`
	ExpiresAt   int64 `json:"expires_at"`
}

type observation struct{ mono, hb int64 }

// alive is the single liveness function (§11.11.2 criteria 1–3).
func (s *Store) alive(x liveRec) bool {
	now := s.clk.BootMs()
	if x.BootID != "" && s.id.BootID != "" {
		if x.BootID != s.id.BootID {
			return false
		}
	} else if x.HeartbeatAt > now+1000 {
		return false // boot-clock stamp from the future ⇒ another boot
	}
	if now >= x.ExpiresAt {
		return false
	}
	return !s.ownerGone(x)
}

// ownerGone = criterion 3 false: same host and the owner provably absent.
func (s *Store) ownerGone(x liveRec) bool {
	return x.Host == s.id.Host && s.opt.Procs.Gone(x.Ident)
}

// takeoverOK (§11.11.2): case (a) owner provably gone → immediately; case (b)
// expired on two observations ≥ 3 s apart with unchanged heartbeat — only
// background scanners pass allowB (never synchronous paths, P12).
func (s *Store) takeoverOK(key string, x liveRec, allowB bool) bool {
	s.obsMu.Lock()
	defer s.obsMu.Unlock()
	if s.alive(x) {
		delete(s.obs, key)
		return false
	}
	if s.ownerGone(x) {
		return true
	}
	if !allowB {
		return false
	}
	now := s.clk.MonoMs()
	o, ok := s.obs[key]
	if !ok || o.hb != x.HeartbeatAt {
		s.obs[key] = observation{mono: now, hb: x.HeartbeatAt}
		return false
	}
	return now-o.mono >= observeGap
}

func (s *Store) token(sid string) (int64, bool) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	t, ok := s.tokens[sid]
	return t, ok
}

func (s *Store) setToken(sid string, t int64) {
	s.tokMu.Lock()
	defer s.tokMu.Unlock()
	if t == 0 {
		delete(s.tokens, sid)
		return
	}
	s.tokens[sid] = t
}

// nextFence increments module_state('ctxdb','fence_seq') in this transaction.
func (t *Tx) nextFence() (int64, error) {
	var v string
	err := t.Get(`INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('ctxdb','fence_seq','1',?)
		ON CONFLICT(module_id, key) DO UPDATE SET value = CAST(CAST(value AS INTEGER) + 1 AS TEXT), updated_at = excluded.updated_at
		RETURNING value`, []any{t.s.clk.WallMs()}, &v)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// AcquireLease takes the session lease inside t (no row → held(t); dead and
// takeover_ok → held(t'), t' > t). Guard P rejects labelled sessions.
func (t *Tx) AcquireLease(sid string, allowB bool) (int64, error) {
	var x liveRec
	var old int64
	err := t.Get(`SELECT fence_token, owner_pid, owner_start, owner_host, owner_boot_id, heartbeat_at, expires_at
		FROM session_leases WHERE session_id = ?`, []any{sid},
		&old, &x.PID, &x.Start, &x.Host, &x.BootID, &x.HeartbeatAt, &x.ExpiresAt)
	switch mine, _ := t.s.token(sid); {
	case errors.Is(err, ErrNotFound):
	case err != nil:
		return 0, err
	case mine == old:
		return old, nil
	case x.Ident != t.s.id && !t.s.takeoverOK("lease:"+sid, x, allowB): // own row after a drain: reissue
		return 0, fmt.Errorf("ctxdb: session %s held by pid %d: %w", sid, x.PID, ErrSessionBusy)
	default:
		if _, err := t.Exec("DELETE FROM session_leases WHERE session_id = ? AND fence_token = ?", sid, old); err != nil {
			return 0, err
		}
	}
	tok, err := t.nextFence()
	if err != nil {
		return 0, err
	}
	now := t.s.clk.BootMs()
	_, err = t.Exec(`INSERT INTO session_leases(session_id, fence_token, owner_pid, owner_start, owner_host, owner_boot_id, owner_kind, heartbeat_at, expires_at)
		VALUES (?,?,?,?,?,?,?,?,?)`, sid, tok, t.s.id.PID, t.s.id.Start, t.s.id.Host, t.s.id.BootID, t.s.opt.Kind, now, now+LeaseTTL)
	if err != nil {
		return 0, err
	}
	t.s.setToken(sid, tok) // visible to dmb_lease_token() for the rest of this tx
	return tok, nil
}

// AcquireLease acquires a session lease in its own transaction.
func (s *Store) AcquireLease(ctx context.Context, sid string) (tok int64, err error) {
	err = s.Write(ctx, func(t *Tx) error { tok, err = t.AcquireLease(sid, false); return err })
	return tok, err
}

// Heartbeat renews held(t); 0 rows → lost (lease_fenced), token dropped.
func (s *Store) Heartbeat(ctx context.Context, sid string) error {
	tok, ok := s.token(sid)
	if !ok {
		return fmt.Errorf("ctxdb: no lease on %s: %w", sid, ErrLeaseFenced)
	}
	err := s.Write(ctx, func(t *Tx) error {
		now := s.clk.BootMs()
		n, err := t.Exec("UPDATE session_leases SET heartbeat_at = ?, expires_at = ? WHERE session_id = ? AND fence_token = ?",
			now, now+LeaseTTL, sid, tok)
		if err == nil && n == 0 {
			err = fmt.Errorf("ctxdb: lease %s lost: %w", sid, ErrLeaseFenced)
		}
		return err
	})
	if errors.Is(err, ErrLeaseFenced) || errors.Is(err, ErrSessionPurging) {
		s.setToken(sid, 0)
	}
	return err
}

// ReleaseLease deletes held(t) — the only release form of a session lease.
func (s *Store) ReleaseLease(ctx context.Context, sid string) error {
	tok, ok := s.token(sid)
	if !ok {
		return nil
	}
	defer s.setToken(sid, 0)
	return s.Write(ctx, func(t *Tx) error {
		_, err := t.Exec("DELETE FROM session_leases WHERE session_id = ? AND fence_token = ?", sid, tok)
		return err
	})
}

// CheckLease is the read-only check point before every LLM call, tool
// dispatch and tool-result commit (§11.11.4): ≤ 1 lookup.
func (s *Store) CheckLease(ctx context.Context, sid string) error {
	tok, ok := s.token(sid)
	var cur, purging int64
	err := s.get(ctx, `SELECT coalesce((SELECT fence_token FROM session_leases WHERE session_id = ?1), 0),
		EXISTS (SELECT 1 FROM session_labels WHERE session_id = ?1 AND key = 'purge_state')`, []any{sid}, &cur, &purging)
	switch {
	case err != nil:
		return err
	case purging == 1:
		s.setToken(sid, 0)
		return fmt.Errorf("ctxdb: %s: %w", sid, ErrSessionPurging)
	case !ok || cur != tok:
		s.setToken(sid, 0)
		return fmt.Errorf("ctxdb: %s: %w", sid, ErrLeaseFenced)
	}
	return nil
}

// newID returns a random 128-bit id (ULID ordering is not needed here).
func newID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
