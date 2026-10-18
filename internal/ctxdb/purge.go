package ctxdb

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// PurgeRequest selects P (SPEC §11.6 "Himpunan purge P").
type PurgeRequest struct {
	IDs       []string
	WithForks bool
	OlderThan time.Duration // > 0: by last activity instead of IDs
}

// Excluded records a phase-0 exclusion decided under the lock (never re-evaluated).
type Excluded struct {
	IDs    []string `json:"ids"`
	Reason string   `json:"reason"`
}

// PurgeReport is the outcome visible to the user.
type PurgeReport struct {
	TxID     string
	P        []string
	Excluded []Excluded
	Skipped  []string // --older-than without --with-forks: sessions with descendants
}

// purgeValue is module_state('purge', txid).value — fields defined only in §11.11.6.
type purgeValue struct {
	liveRec
	Phase    int        `json:"phase"`
	Excluded []Excluded `json:"excluded"`
	Cursor   int        `json:"cursor"`
	Audit    int64      `json:"audit_event"`
}

const (
	phase0Batch   = 2000
	sessionBatch  = 50
	eventBatch    = 2000
	firstChunk    = 16                    // initial events per unit before any measurement
	chunkTarget   = 25 * time.Millisecond // target cost of one event unit
	phaseDone     = 4
	phaseAborted  = 5
)

// Purge runs `damnbot sessions purge` in the foreground: P, ledger intent,
// then phases 0–3 in ≤ 200 ms transactions with a ≤ 20 % lock duty cycle.
func (s *Store) Purge(ctx context.Context, req PurgeRequest) (PurgeReport, error) {
	rep, err := s.purgeSet(ctx, req)
	if err != nil || len(rep.P) == 0 {
		return rep, err // nothing selected: no purge is started
	}
	rep.TxID = newID()
	if err := s.ledger(map[string]any{"type": "intent", "txid": rep.TxID, "P": rep.P}); err != nil {
		return rep, fmt.Errorf("ctxdb: purge ledger: %w: %w", ErrDiskFull, err)
	}
	rep.Excluded, err = s.runPurge(ctx, rep.TxID, rep.P, true)
	return rep, err
}

// runPurge loops batches until done, a barrier is requested, or ctx ends.
// Stopping normally finishes the batch then releases (expires_at = 0).
func (s *Store) runPurge(ctx context.Context, txid string, p []string, create bool) ([]Excluded, error) {
	var v purgeValue
	for i := 0; ; i++ {
		if markerPresent(s.dir) || ctx.Err() != nil {
			return v.Excluded, errors.Join(s.releasePurge(txid), ctx.Err(), fmt.Errorf("ctxdb: purge paused: %w", ErrDBBusy))
		}
		start := time.Now()
		err := s.tx(ctx, modePurge, func(t *Tx) (err error) { v, err = t.purgeBatch(txid, p, create && i == 0); return err })
		switch {
		case err != nil:
			return v.Excluded, err
		case v.Phase == phaseDone:
			return v.Excluded, s.ledger(map[string]any{"type": "committed", "txid": txid})
		case v.Phase == phaseAborted:
			return v.Excluded, abortReason(v.Excluded)
		}
		if err := s.clk.Sleep(ctx, dutyFactor*time.Since(start)); err != nil {
			return v.Excluded, errors.Join(s.releasePurge(txid), err)
		}
	}
}

// purgeBatch: fencing of the purge lease at the start of every batch, one
// unit of the current phase, then the value is saved in the same tx.
func (t *Tx) purgeBatch(txid string, p []string, create bool) (purgeValue, error) {
	v, err := t.purgeLease(txid, create)
	if err == nil {
		err = t.run("purge.clear")
	}
	if err != nil {
		return v, err
	}
	now := t.s.clk.BootMs()
	v.HeartbeatAt, v.ExpiresAt = now, now+PurgeTTL
	start := time.Now()
	switch v.Phase {
	case 0:
		err = t.phase0(txid, p, &v, start)
	case 1:
		err = t.phaseSessions(txid, &v, start, false)
	case 2:
		err = t.phaseEvents(txid, &v, start)
	default:
		err = t.phaseSessions(txid, &v, start, true)
	}
	if err == nil {
		err = t.freeBlobs()
	}
	if err != nil || v.Phase > 3 {
		return v, err
	}
	if _, err := t.Exec("UPDATE module_state SET value = ?, updated_at = ? WHERE module_id = 'purge' AND key = ?", jsonOf(v), t.s.clk.WallMs(), txid); err != nil {
		return v, err
	}
	ph, cur := v.Phase, v.Cursor
	t.OnCommit(func() { _ = t.s.ledger(map[string]any{"type": "progress", "txid": txid, "phase": ph, "cursor": cur}) })
	return v, nil
}

func abortReason(ex []Excluded) error {
	for _, e := range ex {
		if e.Reason == "session_busy" {
			return fmt.Errorf("ctxdb: purge aborted: %w", ErrSessionBusy)
		}
	}
	return fmt.Errorf("ctxdb: purge aborted: %w", ErrHasForks)
}

// purgeLease creates (first tx, re-checking that no purge exists) or fences
// the purge lease: the owner must still be this process.
func (t *Tx) purgeLease(txid string, create bool) (purgeValue, error) {
	var v purgeValue
	if create {
		var n int
		if err := t.Get("SELECT count(*) FROM module_state WHERE module_id = 'purge'", nil, &n); err != nil || n > 0 {
			return v, errors.Join(err, fmt.Errorf("ctxdb: another purge: %w", ErrPurgeInProgress))
		}
		ev, err := t.AppendEvent(SystemSession, Event{Kind: "system_notice", Payload: jsonOf(map[string]any{"kind": "purge", "txid": txid})})
		if err != nil {
			return v, err
		}
		v = purgeValue{liveRec: liveRec{Ident: t.s.id}, Audit: ev}
		_, err = t.Exec("INSERT INTO module_state(module_id, key, value, updated_at) VALUES ('purge', ?, ?, ?)", txid, jsonOf(v), t.s.clk.WallMs())
		return v, err
	}
	var raw string
	if err := t.Get("SELECT value FROM module_state WHERE module_id = 'purge' AND key = ?", []any{txid}, &raw); err != nil {
		return v, err
	}
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		return v, fmt.Errorf("ctxdb: purge value: %w", err)
	}
	if v.Ident != t.s.id {
		return v, fmt.Errorf("ctxdb: purge lease taken by pid %d: %w", v.PID, ErrPurgeInProgress)
	}
	return v, nil
}

// phase0 labels P children-first; X with a child outside P or a lease that
// is not takeover_ok (case (a), one observation) is excluded with all its
// ancestors in P, recorded in value.excluded[] in the same transaction.
func (t *Tx) phase0(txid string, p []string, v *purgeValue, start time.Time) error {
	inP := map[string]bool{}
	for _, id := range p {
		inP[id] = true
	}
	out := map[string]bool{}
	for _, e := range v.Excluded {
		for _, id := range e.IDs {
			out[id] = true
		}
	}
	labelled, err := t.Strings("SELECT session_id FROM session_labels WHERE key = 'purge_state' AND value = ?", txid)
	if err != nil {
		return err
	}
	done := 0
	for _, x := range p {
		if out[x] || slices.Contains(labelled, x) {
			continue
		}
		if done >= phase0Batch || time.Since(start) >= batchDeadline {
			return nil // continue in the next transaction
		}
		reason, err := t.phase0Reason(x, inP)
		if err != nil {
			return err
		}
		if reason != "" {
			anc, err := t.ancestorsIn(x, inP)
			if err != nil {
				return err
			}
			v.Excluded = append(v.Excluded, Excluded{IDs: anc, Reason: reason})
			t.OnCommit(func() { _ = t.s.ledger(map[string]any{"type": "excluded", "txid": txid, "ids": anc, "reason": reason}) })
			for _, a := range anc {
				out[a] = true
			}
			continue
		}
		if _, err := t.Exec("INSERT INTO session_labels(session_id, key, value, created_at) VALUES (?, 'purge_state', ?, ?)", x, txid, t.s.clk.WallMs()); err != nil {
			return err
		}
		labelled = append(labelled, x)
		done++
	}
	if len(labelled) == 0 {
		return t.abortPurge(txid, v)
	}
	v.Phase = 1 // I2: destructive phases only after all of P is labelled
	n := len(labelled)
	t.OnCommit(func() { _ = t.s.ledger(map[string]any{"type": "hidden", "txid": txid, "count": n}) })
	return nil
}

func (t *Tx) phase0Reason(x string, inP map[string]bool) (string, error) {
	kids, err := t.Strings("SELECT id FROM sessions WHERE parent_id = ?", x)
	if err != nil {
		return "", err
	}
	for _, k := range kids {
		if !inP[k] {
			return "has_forks", nil
		}
	}
	var lr liveRec
	err = t.Get("SELECT owner_pid, owner_start, owner_host, owner_boot_id, heartbeat_at, expires_at FROM session_leases WHERE session_id = ?",
		[]any{x}, &lr.PID, &lr.Start, &lr.Host, &lr.BootID, &lr.HeartbeatAt, &lr.ExpiresAt)
	switch {
	case errors.Is(err, ErrNotFound):
		return "", nil
	case err != nil:
		return "", err
	case !t.s.takeoverOK("lease:"+x, lr, false):
		return "session_busy", nil
	}
	return "", nil
}

// ancestorsIn returns x and its ancestors that are members of P.
func (t *Tx) ancestorsIn(x string, inP map[string]bool) ([]string, error) {
	out := []string{x}
	for cur := x; ; {
		var parent *string
		if err := t.Get("SELECT parent_id FROM sessions WHERE id = ?", []any{cur}, &parent); err != nil {
			return out, err
		}
		if parent == nil || !inP[*parent] {
			return out, nil
		}
		cur = *parent
		out = append(out, cur)
	}
}

func (t *Tx) abortPurge(txid string, v *purgeValue) error {
	if _, err := t.AppendEvent(SystemSession, Event{Kind: "system_notice", Payload: jsonOf(map[string]any{"kind": "purge_aborted", "txid": txid})}); err != nil {
		return err
	}
	v.Phase = phaseAborted
	t.OnCommit(func() { _ = t.s.ledger(map[string]any{"type": "aborted", "txid": txid}) })
	_, err := t.Exec("DELETE FROM module_state WHERE module_id = 'purge' AND key = ?", txid)
	return err
}

// labelledOrdered returns sessions whose latest purge_state label is txid,
// descendants first. The order is computed once per run and cached (the
// labelled set only shrinks, by phase-3 commits, which pop the cache).
func (t *Tx) labelledOrdered(txid string) ([]string, error) {
	if ids, ok := t.s.purgeOrder[txid]; ok {
		return ids, nil
	}
	ids, err := t.Strings(t.s.q["purge.labelled"], txid)
	if err == nil {
		ids, err = t.Strings(t.s.q["desc_first"], jsonOf(ids))
	}
	if err == nil {
		t.s.purgeOrder = map[string][]string{txid: ids}
	}
	return ids, err
}

// phaseSessions runs phase 1 (steps 1–8) or phase 3 (cleanup + 27–30) for
// ≤ 50 sessions within the deadline; the final phase-3 batch runs step 31.
func (t *Tx) phaseSessions(txid string, v *purgeValue, start time.Time, final bool) error {
	ids, err := t.labelledOrdered(txid)
	if err != nil {
		return err
	}
	if !final {
		ids = ids[min(v.Cursor, len(ids)):]
	} else if len(ids) == 0 { // cache drained: confirm against the DB (authoritative)
		delete(t.s.purgeOrder, txid)
		if ids, err = t.labelledOrdered(txid); err != nil {
			return err
		}
	}
	if len(ids) == 0 {
		if final {
			return t.purgeDone(txid, v)
		}
		v.Phase, v.Cursor = 2, 0
		return nil
	}
	n := 0
	for n < len(ids) && n < sessionBatch && (n == 0 || time.Since(start) < batchDeadline) {
		sb := jsonOf(ids[n : n+1]) // unit = one session, in descendant-first order
		if final {
			err = t.purgeFinal(sb, v.Audit)
		} else {
			err = t.run("purge.release", sb)
		}
		if err != nil {
			return err
		}
		n++
	}
	if final {
		t.OnCommit(func() { t.s.purgeOrder[txid] = t.s.purgeOrder[txid][n:] })
	} else {
		v.Cursor += n
	}
	return nil
}

// phaseEvents runs phase 2 (steps 10–25) on the remaining events of P.
func (t *Tx) phaseEvents(txid string, v *purgeValue, start time.Time) error {
	ids, err := t.labelledOrdered(txid)
	if err != nil {
		return err
	}
	ev, err := t.Ints("SELECT id FROM events WHERE session_id IN (SELECT value FROM json_each(?1)) ORDER BY id DESC LIMIT ?2", jsonOf(ids), eventBatch)
	if err != nil {
		return err
	}
	if len(ev) == 0 {
		v.Phase, v.Cursor = 3, 0
		return nil
	}
	for i := 0; i < len(ev); {
		n := min(max(t.s.chunk, 1), len(ev)-i)
		c0 := time.Now()
		if err := t.run("purge.events", jsonOf(ev[i:i+n]), v.Audit); err != nil {
			return err
		}
		d := max(time.Since(c0), time.Microsecond)
		i += n
		// Time-bounded batching (I11): the unit size follows the measured cost so
		// a batch ends before the deadline on slow machines too.
		t.s.chunk = min(max(int(int64(n)*int64(chunkTarget)/int64(d)), 1), eventBatch)
		if time.Since(start)+d*time.Duration(t.s.chunk)/time.Duration(n) >= batchDeadline {
			break
		}
	}
	return nil
}

// purgeDone = step 31: audit, notice dedup keys and the purge lease removed.
func (t *Tx) purgeDone(txid string, v *purgeValue) error {
	if _, err := t.AppendEvent(SystemSession, Event{Kind: "system_notice", Payload: jsonOf(map[string]any{"kind": "purge_done", "txid": txid})}); err != nil {
		return err
	}
	if _, err := t.Exec(`DELETE FROM module_state WHERE module_id = 'gateway' AND key LIKE 'purge_notice:%:' || ?1;
		DELETE FROM module_state WHERE module_id = 'purge' AND key = ?1`, txid); err != nil {
		return err
	}
	v.Phase = phaseDone
	return nil
}

// releasePurge sets value.expires_at = 0 (release form of the purge lease).
// It may run while a maintenance marker is present (finish-then-release).
func (s *Store) releasePurge(txid string) error {
	prev := s.barrierOwner.Swap(true)
	defer s.barrierOwner.Store(prev)
	return s.Write(context.Background(), func(t *Tx) error {
		_, err := t.Exec(`UPDATE module_state SET value = json_set(value, '$.expires_at', 0) WHERE module_id = 'purge' AND key = ?
			AND json_extract(value, '$.owner_pid') = ? AND json_extract(value, '$.owner_start') = ?`, txid, s.id.PID, s.id.Start)
		return err
	})
}

// PendingPurge returns the txid of an unfinished purge, or ErrNotFound.
func (s *Store) PendingPurge(ctx context.Context) (string, error) {
	var txid string
	err := s.get(ctx, "SELECT key FROM module_state WHERE module_id = 'purge' LIMIT 1", nil, &txid)
	return txid, err
}

// ResumePurge continues a pending purge after a CAS takeover (§11.11.6).
// Only long-lived processes resume; short ones get purge_pending.
func (s *Store) ResumePurge(ctx context.Context, allowB bool) error {
	txid, err := s.PendingPurge(ctx)
	if errors.Is(err, ErrNotFound) {
		return nil
	} else if err != nil {
		return err
	}
	if !slices.Contains([]string{"serve", "tui", "acp", "doctor"}, s.opt.Kind) {
		return fmt.Errorf("ctxdb: purge %s pending: %w", txid, ErrPurgePending)
	}
	err = s.Write(ctx, func(t *Tx) error {
		var raw string
		var v purgeValue
		if err := t.Get("SELECT value FROM module_state WHERE module_id = 'purge' AND key = ?", []any{txid}, &raw); err != nil {
			return err
		}
		if err := json.Unmarshal([]byte(raw), &v); err != nil {
			return fmt.Errorf("ctxdb: purge value: %w", err)
		}
		if v.Ident == s.id {
			return nil
		}
		if !s.takeoverOK("purge:"+txid, v.liveRec, allowB) {
			return fmt.Errorf("ctxdb: purge owner pid %d alive: %w", v.PID, ErrPurgeInProgress)
		}
		v.Ident, v.HeartbeatAt, v.ExpiresAt = s.id, s.clk.BootMs(), s.clk.BootMs()+PurgeTTL
		n, err := t.Exec("UPDATE module_state SET value = ? WHERE module_id = 'purge' AND key = ? AND value = ?", jsonOf(v), txid, raw)
		if err == nil && n == 0 {
			err = fmt.Errorf("ctxdb: purge takeover lost CAS: %w", ErrPurgeInProgress)
		}
		return err
	})
	if err != nil {
		return err
	}
	_, err = s.runPurge(ctx, txid, s.intentP(txid), false)
	return err
}

// ledger appends one fsynced JSON line to <data_dir>/purge.log (0600).
func (s *Store) ledger(entry map[string]any) error {
	entry["at"] = s.clk.WallMs()
	f, err := os.OpenFile(filepath.Join(s.dir, "purge.log"), os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(append([]byte(jsonOf(entry)), '\n'))
	if err == nil {
		err = f.Sync()
	}
	return errors.Join(err, f.Close())
}

// intentP returns intent.P of txid from the ledger (nil when lost: P is then
// the labelled set and phase 0 is considered complete).
func (s *Store) intentP(txid string) []string {
	for _, e := range readLedger(s.dir) {
		if e.Type == "intent" && e.TxID == txid {
			return e.P
		}
	}
	return nil
}

type ledgerEntry struct {
	Type string   `json:"type"`
	TxID string   `json:"txid"`
	P    []string `json:"P"`
	IDs  []string `json:"ids"`
}

func readLedger(dir string) []ledgerEntry {
	f, err := os.Open(filepath.Join(dir, "purge.log"))
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []ledgerEntry
	sc := bufio.NewScanner(f)
	sc.Buffer(nil, 64<<20)
	for sc.Scan() {
		var e ledgerEntry
		if json.Unmarshal(sc.Bytes(), &e) == nil {
			out = append(out, e)
		}
	}
	return out
}
