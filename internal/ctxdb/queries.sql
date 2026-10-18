-- queries.sql — named statements of ctxdb (SPEC §11.6 purge order, gc, recovery).
-- Blocks start with "-- name: <id>"; a block may hold several statements, executed
-- in order with the same arguments (?1, ?2). /*LIVE_REF*/ is replaced by gc.live_ref.
-- Purge sets are JSON arrays expanded with json_each(?1). Temp tables live on the
-- writer connection only (no change to the main schema, TestPurgeNoSchemaChange).

-- name: temp.tables
CREATE TEMP TABLE IF NOT EXISTS purge_refs (id INTEGER PRIMARY KEY, sha TEXT NOT NULL);
CREATE TEMP TABLE IF NOT EXISTS purge_ids (id INTEGER PRIMARY KEY);

-- name: purge.clear
DELETE FROM temp.purge_refs;
DELETE FROM temp.purge_ids;

-- name: purge.release
-- Phase 1 steps 1–8 for S_b = ?1 (also repeated by phase 3).
-- 1: undelivered outbox rows of S_b (by event or by blob_refs owner) + their refs.
DELETE FROM temp.purge_ids;
INSERT INTO temp.purge_ids SELECT id FROM gateway_deliveries
 WHERE status IN ('queued','sending','failed','uncertain')
   AND (event_id IN (SELECT id FROM events WHERE session_id IN (SELECT value FROM json_each(?1)))
        OR CAST(id AS TEXT) IN (SELECT owner_id FROM blob_refs WHERE owner_kind = 'gateway_delivery'
                                AND session_id IN (SELECT value FROM json_each(?1))));
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'gateway_delivery' AND owner_id IN (SELECT CAST(id AS TEXT) FROM temp.purge_ids) ON CONFLICT DO NOTHING;
-- 3 (refs first): approval previews of open journals.
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'turn_journal'
   AND owner_id IN (SELECT turn_id FROM turn_journal WHERE session_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
DELETE FROM blob_refs WHERE id IN (SELECT id FROM temp.purge_refs);
DELETE FROM gateway_deliveries WHERE id IN (SELECT id FROM temp.purge_ids);
-- 2
DELETE FROM gateway_conversations WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM auto_session_refs WHERE session_id IN (SELECT value FROM json_each(?1));
-- 3
DELETE FROM turn_journal WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM approval_responses WHERE session_id IN (SELECT value FROM json_each(?1));
-- 4
UPDATE cron_runs SET session_id = NULL WHERE session_id IN (SELECT value FROM json_each(?1));
UPDATE kanban_tasks SET session_id = NULL WHERE session_id IN (SELECT value FROM json_each(?1));
-- 5–8: M = source_session ∈ S_b ∪ evidence_event_id ∈ E(S_b)
DELETE FROM temp.purge_ids;
INSERT INTO temp.purge_ids SELECT id FROM memory
 WHERE source_session IN (SELECT value FROM json_each(?1))
    OR evidence_event_id IN (SELECT id FROM events WHERE session_id IN (SELECT value FROM json_each(?1)));
UPDATE skills SET memory_id = NULL WHERE memory_id IN (SELECT id FROM temp.purge_ids);
UPDATE memory SET supersedes_id = NULL
 WHERE supersedes_id IN (SELECT id FROM temp.purge_ids) AND id NOT IN (SELECT id FROM temp.purge_ids);
DELETE FROM memory_embeddings WHERE memory_id IN (SELECT id FROM temp.purge_ids);
DELETE FROM memory_pins WHERE memory_id IN (SELECT id FROM temp.purge_ids);
DELETE FROM memory_decisions WHERE memory_id IN (SELECT id FROM temp.purge_ids);
DELETE FROM memory_fts WHERE rowid IN (SELECT id FROM temp.purge_ids);
DELETE FROM memory WHERE id IN (SELECT id FROM temp.purge_ids);

-- name: purge.events
-- Phase 2 steps 10–24 for E_b = ?1; ?2 = audit event of the purge (step 19).
DELETE FROM temp.purge_ids;
INSERT INTO temp.purge_ids SELECT id FROM gateway_deliveries WHERE event_id IN (SELECT value FROM json_each(?1));
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'gateway_delivery' AND owner_id IN (SELECT CAST(id AS TEXT) FROM temp.purge_ids) ON CONFLICT DO NOTHING;
-- 11 + AF-05: refs of processed inbound rows go with them.
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'gateway_inbound' AND owner_id IN (SELECT platform || ':' || external_id FROM gateway_inbound
                                                       WHERE event_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'action' AND owner_id IN (SELECT CAST(id AS TEXT) FROM actions
                                              WHERE event_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'file_snapshot' AND owner_id IN (SELECT CAST(id AS TEXT) FROM file_snapshots
                                                     WHERE event_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'event_attachment' AND owner_id IN (SELECT event_id || ':' || ord FROM event_attachments
                                                        WHERE event_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'event' AND owner_id IN (SELECT CAST(value AS TEXT) FROM json_each(?1)) ON CONFLICT DO NOTHING;
DELETE FROM blob_refs WHERE id IN (SELECT id FROM temp.purge_refs);                                       -- 10, 13, 14, 21, 22
DELETE FROM gateway_deliveries WHERE id IN (SELECT id FROM temp.purge_ids);                               -- 10
DELETE FROM gateway_inbound WHERE event_id IN (SELECT value FROM json_each(?1));                          -- 11
DELETE FROM action_undos WHERE event_id IN (SELECT value FROM json_each(?1))
    OR action_id IN (SELECT id FROM actions WHERE event_id IN (SELECT value FROM json_each(?1)));        -- 12
DELETE FROM actions WHERE event_id IN (SELECT value FROM json_each(?1));                                  -- 13
DELETE FROM file_snapshots WHERE event_id IN (SELECT value FROM json_each(?1));                           -- 14
DELETE FROM approvals WHERE event_id IN (SELECT value FROM json_each(?1));                                -- 15
DELETE FROM plans WHERE event_id IN (SELECT value FROM json_each(?1));                                    -- 16
DELETE FROM reasoning_carrier_rejections
 WHERE carrier_id IN (SELECT id FROM reasoning_carriers WHERE event_id IN (SELECT value FROM json_each(?1))); -- 17
DELETE FROM reasoning_carriers WHERE event_id IN (SELECT value FROM json_each(?1));                       -- 18
UPDATE usage_ledger SET session_id = '__system__', event_id = ?2, turn_id = NULL
 WHERE event_id IN (SELECT value FROM json_each(?1));                                                     -- 19 (P27 totals kept)
DELETE FROM summaries_fts WHERE rowid IN (SELECT id FROM summaries WHERE event_id IN (SELECT value FROM json_each(?1)));
DELETE FROM summaries WHERE event_id IN (SELECT value FROM json_each(?1));                                -- 20
DELETE FROM event_attachments WHERE event_id IN (SELECT value FROM json_each(?1));                        -- 21
DELETE FROM events_fts WHERE rowid IN (SELECT value FROM json_each(?1));                                  -- 23
DELETE FROM blob_fts WHERE rowid IN (SELECT value FROM json_each(?1));
DELETE FROM events WHERE id IN (SELECT value FROM json_each(?1));                                         -- 24

-- name: purge.final
-- Phase 3 steps 27–30 for S_b = ?1 (after a defensive repeat of 10–24 and 1–3); ?2 = audit event.
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE owner_kind = 'action' AND owner_id IN (SELECT CAST(id AS TEXT) FROM actions
                                              WHERE session_id IN (SELECT value FROM json_each(?1))) ON CONFLICT DO NOTHING;
DELETE FROM temp.purge_ids;
INSERT INTO temp.purge_ids SELECT CAST(owner_id AS INTEGER) FROM blob_refs
 WHERE owner_kind = 'gateway_delivery' AND session_id IN (SELECT value FROM json_each(?1)) ON CONFLICT DO NOTHING; -- 28 (a)
INSERT INTO temp.purge_refs SELECT id, sha256 FROM blob_refs
 WHERE session_id IN (SELECT value FROM json_each(?1)) ON CONFLICT DO NOTHING;                           -- 28 (b)
DELETE FROM blob_refs WHERE id IN (SELECT id FROM temp.purge_refs);
DELETE FROM gateway_deliveries WHERE id IN (SELECT id FROM temp.purge_ids);
DELETE FROM reasoning_carrier_rejections WHERE session_id IN (SELECT value FROM json_each(?1));            -- 27
DELETE FROM approvals WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM plans WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM summaries_fts WHERE rowid IN (SELECT id FROM summaries WHERE session_id IN (SELECT value FROM json_each(?1)));
DELETE FROM summaries WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM action_undos WHERE action_id IN (SELECT id FROM actions WHERE session_id IN (SELECT value FROM json_each(?1)));
DELETE FROM actions WHERE session_id IN (SELECT value FROM json_each(?1));
UPDATE usage_ledger SET session_id = '__system__', event_id = ?2, turn_id = NULL
 WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM session_labels WHERE session_id IN (SELECT value FROM json_each(?1));                         -- 29
DELETE FROM session_leases WHERE session_id IN (SELECT value FROM json_each(?1));
DELETE FROM sessions WHERE id IN (SELECT value FROM json_each(?1));                                       -- 30

-- name: purge.freed
-- Step 25: shas whose refs were deleted in this transaction and now have none.
SELECT DISTINCT r.sha FROM temp.purge_refs r WHERE NOT EXISTS (SELECT 1 FROM blob_refs b WHERE b.sha256 = r.sha)

-- name: purge.drop_blob
DELETE FROM blob_expirations WHERE sha256 = ?1;
DELETE FROM blobs WHERE sha256 = ?1;

-- name: purge.labelled
-- Sessions whose latest purge_state label is ?1.
SELECT l.session_id FROM session_labels l WHERE l.key = 'purge_state' AND l.value = ?1
   AND l.id = (SELECT max(id) FROM session_labels m WHERE m.session_id = l.session_id AND m.key = 'purge_state')

-- name: desc_first
-- Members of the JSON set ?1 ordered descendants first (depth inside the set, descending).
WITH RECURSIVE
  m(id) AS (SELECT value FROM json_each(?1)),
  d(id, depth) AS (
    SELECT m.id, 0 FROM m JOIN sessions s ON s.id = m.id WHERE s.parent_id IS NULL OR s.parent_id NOT IN (SELECT id FROM m)
    UNION ALL
    SELECT s.id, d.depth + 1 FROM d JOIN sessions s ON s.parent_id = d.id WHERE s.id IN (SELECT id FROM m))
SELECT id FROM d ORDER BY depth DESC, id

-- name: purge.nodes
-- purgeSet input: id, parent, last activity (max-seq event or created_at), labelled.
SELECT s.id, coalesce(s.parent_id, ''),
       coalesce((SELECT created_at FROM events e WHERE e.session_id = s.id ORDER BY seq DESC LIMIT 1), s.created_at),
       EXISTS (SELECT 1 FROM session_labels l WHERE l.session_id = s.id AND l.key = 'purge_state')
  FROM sessions s WHERE s.id <> '__system__'

-- name: gc.live_ref
-- blob_refs row r has a live owner (§11.6 table); ?1 = T_gc (wall ms). PK lookups only.
(r.owner_kind IN ('event','event_attachment')
 OR (r.owner_kind IN ('file_snapshot','action') AND r.created_at >= ?1)
 OR (r.owner_kind = 'turn_journal' AND EXISTS (SELECT 1 FROM turn_journal j WHERE j.turn_id = r.owner_id
     AND j.state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval')))
 OR (r.owner_kind = 'gateway_delivery' AND EXISTS (SELECT 1 FROM gateway_deliveries d WHERE d.id = CAST(r.owner_id AS INTEGER)
     AND d.status IN ('queued','sending','failed','uncertain')))
 OR (r.owner_kind = 'gateway_inbound' AND EXISTS (SELECT 1 FROM gateway_inbound g
     WHERE g.platform = substr(r.owner_id, 1, instr(r.owner_id, ':') - 1)
       AND g.external_id = substr(r.owner_id, instr(r.owner_id, ':') + 1) AND g.event_id IS NULL)))

-- name: gc.candidates
-- Step 2 (outside the lock): live-status shas without a live ref; ref count; retention reason.
SELECT b.sha256, (SELECT count(*) FROM blob_refs r WHERE r.sha256 = b.sha256),
       EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND r.owner_kind IN ('file_snapshot','action'))
  FROM blobs b
 WHERE coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'
   AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND /*LIVE_REF*/)

-- name: gc.recheck
-- Step 3 (under the lock): ?2 is still a candidate.
SELECT count(*) FROM blobs b WHERE b.sha256 = ?2
   AND coalesce((SELECT kind FROM blob_expirations e WHERE e.sha256 = b.sha256 ORDER BY e.id DESC LIMIT 1), '') <> 'expired'
   AND NOT EXISTS (SELECT 1 FROM blob_refs r WHERE r.sha256 = b.sha256 AND /*LIVE_REF*/)

-- name: gc.idle_leases
-- Leases of sessions with a running journal (§11.11.8 "active" when alive()).
SELECT l.owner_pid, l.owner_start, l.owner_host, l.owner_boot_id, l.heartbeat_at, l.expires_at
  FROM turn_journal j JOIN session_leases l ON l.session_id = j.session_id
 WHERE j.state IN ('running','tool_pending','tool_done')

-- name: journal.open
-- Open journals of unlabelled sessions with their lease (pid 0 = no lease row).
SELECT j.turn_id, j.session_id, j.state, s.origin, coalesce(j.last_tool_call_id, ''), coalesce(j.last_tool_name, ''),
       coalesce(j.tool_class, ''), coalesce(j.target_path, ''), coalesce(j.before_sha, ''), coalesce(j.after_sha, ''),
       coalesce(j.tool_pgid_start, ''), coalesce(j.tool_pgid, 0), coalesce(l.owner_pid, 0), coalesce(l.owner_start, ''),
       coalesce(l.owner_host, ''), coalesce(l.owner_boot_id, ''), coalesce(l.heartbeat_at, 0), coalesce(l.expires_at, 0)
  FROM turn_journal j JOIN sessions s ON s.id = j.session_id LEFT JOIN session_leases l ON l.session_id = j.session_id
 WHERE j.state IN ('running','tool_pending','tool_done')
   AND NOT EXISTS (SELECT 1 FROM session_labels x WHERE x.session_id = j.session_id AND x.key = 'purge_state')

-- name: lease.get
SELECT fence_token, owner_pid, owner_start, owner_host, owner_boot_id, heartbeat_at, expires_at
  FROM session_leases WHERE session_id = ?1
