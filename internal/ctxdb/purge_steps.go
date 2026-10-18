package ctxdb

import (
	"os"
	"strconv"
)

// Purge FK-ordered steps (SPEC §11.6 table, children before parents). Sets
// are passed as JSON arrays and expanded with json_each(?1).
const (
	inSet = " IN (SELECT value FROM json_each(?1))"
	evOf  = " IN (SELECT id FROM events WHERE session_id" + inSet + ")"
)

// Ints returns the first integer column of every row.
func (t *Tx) Ints(q string, args ...any) ([]int64, error) {
	ss, err := t.Strings(q, args...)
	out := make([]int64, 0, len(ss))
	for _, s := range ss {
		n, _ := strconv.ParseInt(s, 10, 64)
		out = append(out, n)
	}
	return out, err
}

// execAll runs statements with the same arguments, stopping at the first error.
func (t *Tx) execAll(args []any, qs ...string) error {
	for _, q := range qs {
		if _, err := t.Exec(q, args...); err != nil {
			return err
		}
	}
	return nil
}

// delRefs deletes blob_refs matching cond and remembers their shas for step 25.
func (t *Tx) delRefs(cond string, args ...any) error {
	shas, err := t.Strings("SELECT sha256 FROM blob_refs WHERE "+cond, args...)
	if err != nil {
		return err
	}
	for _, s := range shas {
		t.freed[s] = true
	}
	_, err = t.Exec("DELETE FROM blob_refs WHERE "+cond, args...)
	return err
}

// purgeRelease = phase 1 steps 1–8 for sessions sb (also repeated in phase 3).
func (t *Tx) purgeRelease(sb string) error {
	dl, err := t.Ints(`SELECT id FROM gateway_deliveries WHERE status IN ('queued','sending','failed','uncertain')
		AND (event_id`+evOf+` OR CAST(id AS TEXT) IN (SELECT owner_id FROM blob_refs WHERE owner_kind = 'gateway_delivery' AND session_id`+inSet+`))`, sb)
	if err != nil {
		return err
	}
	if err := t.delDeliveries(jsonOf(dl)); err != nil {
		return err
	}
	if err := t.delRefs("owner_kind = 'turn_journal' AND owner_id IN (SELECT turn_id FROM turn_journal WHERE session_id"+inSet+")", sb); err != nil {
		return err
	}
	m, err := t.Ints("SELECT id FROM memory WHERE source_session"+inSet+" OR evidence_event_id"+evOf, sb)
	if err != nil {
		return err
	}
	if err := t.execAll([]any{sb},
		"DELETE FROM gateway_conversations WHERE session_id"+inSet, // step 2
		"DELETE FROM auto_session_refs WHERE session_id"+inSet,
		"DELETE FROM turn_journal WHERE session_id"+inSet, // step 3
		"DELETE FROM approval_responses WHERE session_id"+inSet,
		"UPDATE cron_runs SET session_id = NULL WHERE session_id"+inSet, // step 4
		"UPDATE kanban_tasks SET session_id = NULL WHERE session_id"+inSet,
	); err != nil {
		return err
	}
	return t.execAll([]any{jsonOf(m)}, // steps 5–8, M
		"UPDATE skills SET memory_id = NULL WHERE memory_id"+inSet,
		"UPDATE memory SET supersedes_id = NULL WHERE supersedes_id"+inSet+" AND id NOT"+inSet,
		"DELETE FROM memory_embeddings WHERE memory_id"+inSet,
		"DELETE FROM memory_pins WHERE memory_id"+inSet,
		"DELETE FROM memory_decisions WHERE memory_id"+inSet,
		"DELETE FROM memory_fts WHERE rowid"+inSet,
		"DELETE FROM memory WHERE id"+inSet,
	)
}

func (t *Tx) delDeliveries(ids string) error {
	if err := t.delRefs("owner_kind = 'gateway_delivery' AND owner_id IN (SELECT CAST(value AS TEXT) FROM json_each(?1))", ids); err != nil {
		return err
	}
	_, err := t.Exec("DELETE FROM gateway_deliveries WHERE id"+inSet, ids)
	return err
}

// purgeEvents = phase 2 steps 10–24 for event ids eb (JSON array).
func (t *Tx) purgeEvents(eb string, audit int64) error {
	dl, err := t.Ints("SELECT id FROM gateway_deliveries WHERE event_id"+inSet, eb)
	if err == nil {
		err = t.delDeliveries(jsonOf(dl)) // step 10
	}
	if err == nil { // step 11 (+ AF-05: refs of the processed inbound rows)
		err = t.delRefs("owner_kind = 'gateway_inbound' AND owner_id IN (SELECT platform || ':' || external_id FROM gateway_inbound WHERE event_id"+inSet+")", eb)
	}
	if err == nil {
		err = t.execAll([]any{eb},
			"DELETE FROM gateway_inbound WHERE event_id"+inSet,
			"DELETE FROM action_undos WHERE event_id"+inSet+" OR action_id IN (SELECT id FROM actions WHERE event_id"+inSet+")") // 12
	}
	for _, k := range [][2]string{{"action", "actions"}, {"file_snapshot", "file_snapshots"}} { // 13, 14
		if err == nil {
			err = t.delRefs("owner_kind = '"+k[0]+"' AND owner_id IN (SELECT CAST(id AS TEXT) FROM "+k[1]+" WHERE event_id"+inSet+")", eb)
		}
	}
	if err == nil {
		err = t.delRefs("owner_kind = 'event_attachment' AND owner_id IN (SELECT event_id || ':' || ord FROM event_attachments WHERE event_id"+inSet+")", eb)
	}
	if err == nil {
		err = t.delRefs("owner_kind = 'event' AND owner_id IN (SELECT CAST(value AS TEXT) FROM json_each(?1))", eb) // 22
	}
	if err != nil {
		return err
	}
	if _, err := t.Exec("UPDATE usage_ledger SET session_id = '__system__', event_id = ?2, turn_id = NULL WHERE event_id"+inSet, eb, audit); err != nil {
		return err // step 19: keep cost totals (P27) without content
	}
	return t.execAll([]any{eb},
		"DELETE FROM actions WHERE event_id"+inSet,
		"DELETE FROM file_snapshots WHERE event_id"+inSet,
		"DELETE FROM approvals WHERE event_id"+inSet, // 15
		"DELETE FROM plans WHERE event_id"+inSet,     // 16
		"DELETE FROM reasoning_carrier_rejections WHERE carrier_id IN (SELECT id FROM reasoning_carriers WHERE event_id"+inSet+")",
		"DELETE FROM reasoning_carriers WHERE event_id"+inSet,
		"DELETE FROM summaries_fts WHERE rowid IN (SELECT id FROM summaries WHERE event_id"+inSet+")",
		"DELETE FROM summaries WHERE event_id"+inSet,
		"DELETE FROM event_attachments WHERE event_id"+inSet, // 21
		"DELETE FROM events_fts WHERE rowid"+inSet,           // 23
		"DELETE FROM blob_fts WHERE rowid"+inSet,
		"DELETE FROM events WHERE id"+inSet, // 24
	)
}

// purgeFinal = phase 3 for sessions sb: defensive repeat of 10–25 and 1–3,
// leases, then steps 27–30 so step 30 never sticks on an FK (I10).
func (t *Tx) purgeFinal(sb string, audit int64) error {
	ev, err := t.Ints("SELECT id FROM events WHERE session_id"+inSet+" ORDER BY id DESC", sb)
	if err == nil {
		err = t.purgeEvents(jsonOf(ev), audit)
	}
	if err == nil {
		err = t.purgeRelease(sb)
	}
	if err == nil { // 27: actions of S_b with event outside P
		err = t.delRefs("owner_kind = 'action' AND owner_id IN (SELECT CAST(id AS TEXT) FROM actions WHERE session_id"+inSet+")", sb)
	}
	var dl []int64
	if err == nil { // 28 (a)
		dl, err = t.Ints("SELECT CAST(owner_id AS INTEGER) FROM blob_refs WHERE owner_kind = 'gateway_delivery' AND session_id"+inSet, sb)
	}
	if err == nil {
		err = t.delDeliveries(jsonOf(dl))
	}
	if err == nil { // 28 (b)
		err = t.delRefs("session_id"+inSet, sb)
	}
	if err != nil {
		return err
	}
	if _, err := t.Exec("UPDATE usage_ledger SET session_id = '__system__', event_id = ?2, turn_id = NULL WHERE session_id"+inSet, sb, audit); err != nil {
		return err
	}
	return t.execAll([]any{sb},
		"DELETE FROM reasoning_carrier_rejections WHERE session_id"+inSet, // 27
		"DELETE FROM approvals WHERE session_id"+inSet,
		"DELETE FROM plans WHERE session_id"+inSet,
		"DELETE FROM summaries_fts WHERE rowid IN (SELECT id FROM summaries WHERE session_id"+inSet+")",
		"DELETE FROM summaries WHERE session_id"+inSet,
		"DELETE FROM action_undos WHERE action_id IN (SELECT id FROM actions WHERE session_id"+inSet+")",
		"DELETE FROM actions WHERE session_id"+inSet,
		"DELETE FROM session_labels WHERE session_id"+inSet, // 29
		"DELETE FROM session_leases WHERE session_id"+inSet,
		"DELETE FROM sessions WHERE id"+inSet, // 30
	)
}

// freeBlobs = step 25: shas whose refs were deleted in this transaction and
// now have none → file to .trash/<txid>/, rows deleted.
func (t *Tx) freeBlobs() error {
	for sha := range t.freed {
		var n int
		if err := t.Get("SELECT count(*) FROM blob_refs WHERE sha256 = ?", []any{sha}, &n); err != nil || n > 0 {
			if err != nil {
				return err
			}
			continue
		}
		if _, err := os.Stat(t.s.blobPath(sha)); err == nil {
			if err := t.trashFile(sha); err != nil {
				return err
			}
			t.trashed = true
		}
		if err := t.execAll([]any{sha}, "DELETE FROM blob_expirations WHERE sha256 = ?1", "DELETE FROM blobs WHERE sha256 = ?1"); err != nil {
			return err
		}
	}
	return nil
}
