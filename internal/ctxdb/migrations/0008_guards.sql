-- 0008_guards.sql — guard sesi + tabel pendukung (ADR-042, ADR-043; model & invarian §11.11):
--   (P) tidak ada baris baru/berubah yang merujuk sesi berlabel purge_state  → RAISE 'session_purging';
--   (F) transcript/journal/outbox sesi hanya ditulis pemegang lease aktif   → RAISE 'lease_fenced'
--       (token dmb_lease_token(s) = session_leases.fence_token; '__system__' dikecualikan).
-- Fail-closed (§11.11.3): seluruh guard aktif kecuali dmb_maint() = 'purge'; guard F juga dilewati di mode 'restore'.
-- NULL atau nilai tak dikenal → semua guard dan P9 aktif.
-- Rujukan NULL lolos (subquery NULL → kondisi tidak terpenuhi).
-- Tabel pendukung (ADR-043, §11.11.10):
CREATE TABLE approval_responses (              -- mutable (insert/delete): kotak masuk jawaban approval dari proses tanpa lease sesi
    id           INTEGER PRIMARY KEY,          -- (jembatan `mcp serve`, web/gateway untuk turn di proses lain), §11.11.10
    session_id   TEXT    NOT NULL REFERENCES sessions(id),
    turn_id      TEXT    NOT NULL,
    request_id   TEXT    NOT NULL UNIQUE,      -- = turn_journal.approval_request.request_id (128-bit acak per permintaan); jawaban kedua untuk permintaan sama ditolak
    preview_sha  TEXT    NOT NULL,             -- sha256 preview yang ditampilkan ke penjawab; harus sama dengan permintaan
    decision     TEXT    NOT NULL CHECK (decision IN ('allow_once','deny','edit')),
    edited_args  TEXT    CHECK (edited_args IS NULL OR json_valid(edited_args)),  -- hanya untuk `edit`; divalidasi ulang P3 oleh konsumen
    source       TEXT    NOT NULL CHECK (source IN ('web','gateway','mcp_serve','acp','tui')),
    responder    TEXT    NOT NULL,             -- identitas penjawab (HMAC id platform / identitas token mcp serve / 'local')
    responder_token TEXT,                      -- mcp_serve_tokens.id_hash; diperiksa ulang konsumen (scope 'approve', belum dicabut)
    created_at   INTEGER NOT NULL,
    CHECK ((decision = 'edit') = (edited_args IS NOT NULL)),
    CHECK ((source = 'mcp_serve') = (responder_token IS NOT NULL)),
    CHECK (source <> 'mcp_serve' OR decision <> 'edit')   -- klien MCP: approve/deny saja (§11.11.10)
) STRICT;
CREATE INDEX idx_approval_responses_session ON approval_responses(session_id, turn_id);  -- indeks anak FK + konsumsi

CREATE TABLE auto_session_refs (               -- append-only (P9): event `__system__` fitur auto → sesi yang dirujuk (pengganti payload.session_ref)
    event_id    INTEGER NOT NULL REFERENCES events(id),
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    PRIMARY KEY (event_id, session_id)
) STRICT;
CREATE INDEX idx_auto_session_refs_session ON auto_session_refs(session_id);  -- indeks anak FK; purge fase 1
CREATE TRIGGER autorefs_no_update BEFORE UPDATE ON auto_session_refs WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'auto_session_refs is append-only'); END;
CREATE TRIGGER autorefs_no_delete BEFORE DELETE ON auto_session_refs WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'auto_session_refs is append-only'); END;
CREATE TRIGGER guard_approval_responses_ins BEFORE INSERT ON approval_responses WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'approval_not_pending') WHERE NOT EXISTS (SELECT 1 FROM turn_journal WHERE turn_id = NEW.turn_id AND session_id = NEW.session_id
        AND state = 'awaiting_approval' AND json_extract(approval_request, '$.request_id') = NEW.request_id);
END;
CREATE TRIGGER guard_autorefs_ins BEFORE INSERT ON auto_session_refs WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'internal: auto_session_refs.event_id must be a __system__ event') WHERE (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT '__system__';
END;
CREATE TRIGGER approvalresp_no_update BEFORE UPDATE ON approval_responses BEGIN SELECT RAISE(ABORT, 'approval_responses: insert/delete only'); END;

CREATE TRIGGER guard_events_ins BEFORE INSERT ON events WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
END;
CREATE TRIGGER guard_evatt_ins BEFORE INSERT ON event_attachments WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND (SELECT session_id FROM events WHERE id = NEW.event_id) <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND fence_token = dmb_lease_token((SELECT session_id FROM events WHERE id = NEW.event_id)));
END;
CREATE TRIGGER guard_summaries_ins BEFORE INSERT ON summaries WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'internal: cross-session event_id') WHERE NEW.event_id IS NOT NULL AND (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id;
END;
CREATE TRIGGER guard_usage_ins BEFORE INSERT ON usage_ledger WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'internal: cross-session event_id') WHERE NEW.event_id IS NOT NULL AND (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id;
END;
CREATE TRIGGER guard_approvals_ins BEFORE INSERT ON approvals WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'internal: cross-session event_id') WHERE NEW.event_id IS NOT NULL AND (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id;
END;
CREATE TRIGGER guard_plans_ins BEFORE INSERT ON plans WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'internal: cross-session event_id') WHERE NEW.event_id IS NOT NULL AND (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id;
END;
CREATE TRIGGER guard_actions_ins BEFORE INSERT ON actions WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'internal: cross-session event_id') WHERE NEW.event_id IS NOT NULL AND (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id;
END;
CREATE TRIGGER guard_undos_ins BEFORE INSERT ON action_undos WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND (SELECT session_id FROM events WHERE id = NEW.event_id) <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND fence_token = dmb_lease_token((SELECT session_id FROM events WHERE id = NEW.event_id)));
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM actions WHERE id = NEW.action_id) AND key = 'purge_state');
END;
CREATE TRIGGER guard_fsnap_ins BEFORE INSERT ON file_snapshots WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND (SELECT session_id FROM events WHERE id = NEW.event_id) <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND fence_token = dmb_lease_token((SELECT session_id FROM events WHERE id = NEW.event_id)));
END;
CREATE TRIGGER guard_carriers_ins BEFORE INSERT ON reasoning_carriers WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND (SELECT session_id FROM events WHERE id = NEW.event_id) <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND fence_token = dmb_lease_token((SELECT session_id FROM events WHERE id = NEW.event_id)));
END;
CREATE TRIGGER guard_carrierrej_ins BEFORE INSERT ON reasoning_carrier_rejections WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT event_id FROM reasoning_carriers WHERE id = NEW.carrier_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_blobrefs_ins BEFORE INSERT ON blob_refs WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
END;
CREATE TRIGGER guard_journal_ins BEFORE INSERT ON turn_journal WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
END;
CREATE TRIGGER guard_journal_upd BEFORE UPDATE ON turn_journal WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND NEW.session_id <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = NEW.session_id AND fence_token = dmb_lease_token(NEW.session_id));
END;
CREATE TRIGGER guard_gwdel_ins BEFORE INSERT ON gateway_deliveries WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'lease_fenced') WHERE dmb_maint() IS NOT 'restore' AND (SELECT session_id FROM events WHERE id = NEW.event_id) <> '__system__' AND NOT EXISTS (SELECT 1 FROM session_leases WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND fence_token = dmb_lease_token((SELECT session_id FROM events WHERE id = NEW.event_id)));
END;
CREATE TRIGGER guard_gwdel_upd BEFORE UPDATE ON gateway_deliveries WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
END;
CREATE TRIGGER guard_gwin_ins BEFORE INSERT ON gateway_inbound WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
END;
CREATE TRIGGER guard_gwin_upd BEFORE UPDATE ON gateway_inbound WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.event_id) AND key = 'purge_state');
END;
CREATE TRIGGER gwconv_no_update BEFORE UPDATE ON gateway_conversations BEGIN SELECT RAISE(ABORT, 'gateway_conversations: ganti sesi = baris baru'); END;
CREATE TRIGGER guard_gwconv_ins BEFORE INSERT ON gateway_conversations WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_labels_ins BEFORE INSERT ON session_labels WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
    SELECT RAISE(ABORT, 'internal: purge_state label is reserved for purge') WHERE NEW.key = 'purge_state';
END;
CREATE TRIGGER guard_leases_ins BEFORE INSERT ON session_leases WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_leases_upd BEFORE UPDATE ON session_leases WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_sessions_ins BEFORE INSERT ON sessions WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.parent_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_memory_ins BEFORE INSERT ON memory WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.source_session AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = NEW.evidence_event_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.supersedes_id) AND key = 'purge_state');
END;
CREATE TRIGGER guard_memdec_ins BEFORE INSERT ON memory_decisions WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_mempin_ins BEFORE INSERT ON memory_pins WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_memembed_ins BEFORE INSERT ON memory_embeddings WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_memembed_upd BEFORE UPDATE ON memory_embeddings WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_skills_ins BEFORE INSERT ON skills WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_skills_upd BEFORE UPDATE ON skills WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT source_session FROM memory WHERE id = NEW.memory_id) AND key = 'purge_state');
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = (SELECT session_id FROM events WHERE id = (SELECT evidence_event_id FROM memory WHERE id = NEW.memory_id)) AND key = 'purge_state');
END;
CREATE TRIGGER guard_cronruns_ins BEFORE INSERT ON cron_runs WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_cronruns_upd BEFORE UPDATE ON cron_runs WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_kanban_ins BEFORE INSERT ON kanban_tasks WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
CREATE TRIGGER guard_kanban_upd BEFORE UPDATE ON kanban_tasks WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
