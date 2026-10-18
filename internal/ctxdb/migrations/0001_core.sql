-- 0001_core.sql — sesi, transcript, ledger, memory, skills

CREATE TABLE schema_migrations (
    version     INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    checksum    TEXT    NOT NULL,
    applied_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE sessions (
    id           TEXT PRIMARY KEY,             -- ULID
    parent_id    TEXT REFERENCES sessions(id),
    fork_seq     INTEGER,
    origin       TEXT    NOT NULL CHECK (origin IN ('tui','acp','headless','web','gateway','cron','kanban','delegate','batch','import','scorecard','system')),
    cwd          TEXT    NOT NULL,
    model        TEXT    NOT NULL,             -- provider/model
    config_sha   TEXT    NOT NULL,
    context_tier TEXT    NOT NULL,             -- teks context tier beku (P15)
    created_at   INTEGER NOT NULL,
    CHECK ((parent_id IS NULL) = (fork_seq IS NULL) OR origin = 'delegate')
) STRICT;
CREATE INDEX idx_sessions_cwd_created ON sessions(cwd, created_at DESC);
CREATE INDEX idx_sessions_parent ON sessions(parent_id);

CREATE TABLE session_labels (                  -- metadata mutable dipisah dari sessions (append-only, nilai terakhir menang)
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    key         TEXT    NOT NULL CHECK (key IN ('title','archived','pinned','gateway_chat','purge_state')),  -- purge_state = txid purge fase 1 (§11.6, ADR-040)
    value       TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_session_labels ON session_labels(session_id, key, id DESC);

CREATE TABLE blobs (
    sha256      TEXT PRIMARY KEY,
    size        INTEGER NOT NULL,
    mime        TEXT    NOT NULL,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE events (
    id            INTEGER PRIMARY KEY,
    session_id    TEXT    NOT NULL REFERENCES sessions(id),
    seq           INTEGER NOT NULL,
    turn_id       TEXT,
    kind          TEXT    NOT NULL CHECK (kind IN (
                    'user_message','assistant_message','tool_dispatch','tool_rejected','tool_result',
                    'llm_call','stream_aborted','provider_fallback','credential_rotated','compaction','summary_ref',
                    'approval','preview','hook_error','error','budget_warning','budget_exhausted',
                    'turn_start','turn_end','todo_update','plan','plan_step','plan_deviation',
                    'verify_run','undo','system_notice','config_reload',
                    'module_start','module_error','delegate_start','delegate_end',
                    'crash_recovered','imported','context_exclude','dlp_blocked','acp_fs_fallback')),
    role          TEXT    CHECK (role IN ('user','assistant','tool','system')),
    content       TEXT    NOT NULL DEFAULT '', -- persis yang dilihat model (sesudah redaksi) atau preview
    tool_call_id  TEXT,
    tool_name     TEXT,
    payload       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(payload)),
    blob_sha      TEXT    REFERENCES blobs(sha256),
    prev_hash     TEXT    NOT NULL,
    hash          TEXT    NOT NULL,            -- sha256(prev_hash|session_id|seq|kind|content|payload|blob_sha)
    created_at    INTEGER NOT NULL,
    UNIQUE (session_id, seq)
) STRICT;
CREATE INDEX idx_events_turn ON events(session_id, turn_id);
CREATE INDEX idx_events_kind ON events(session_id, kind, seq);
CREATE INDEX idx_events_tool_call ON events(tool_call_id) WHERE tool_call_id IS NOT NULL;
CREATE INDEX idx_events_blob ON events(blob_sha) WHERE blob_sha IS NOT NULL;  -- indeks anak FK `blobs` (DELETE blobs saat purge tanpa full scan, ADR-039)

CREATE TABLE event_attachments (
    event_id    INTEGER NOT NULL REFERENCES events(id),
    ord         INTEGER NOT NULL,
    blob_sha    TEXT    NOT NULL REFERENCES blobs(sha256),
    PRIMARY KEY (event_id, ord)
) STRICT;
CREATE INDEX idx_evatt_blob ON event_attachments(blob_sha);  -- indeks anak FK `blobs` (ADR-039)

CREATE TABLE summaries (
    id            INTEGER PRIMARY KEY,
    session_id    TEXT    NOT NULL REFERENCES sessions(id),
    from_seq      INTEGER NOT NULL,
    to_seq        INTEGER NOT NULL,
    content       TEXT    NOT NULL CHECK (json_valid(content)),
    model         TEXT    NOT NULL,
    input_tokens  INTEGER NOT NULL,
    output_tokens INTEGER NOT NULL,
    event_id      INTEGER NOT NULL REFERENCES events(id),
    created_at    INTEGER NOT NULL,
    CHECK (from_seq <= to_seq)
) STRICT;
CREATE INDEX idx_summaries_session ON summaries(session_id, to_seq);
CREATE INDEX idx_summaries_event ON summaries(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE usage_ledger (
    id                  INTEGER PRIMARY KEY,
    session_id          TEXT    NOT NULL REFERENCES sessions(id),
    turn_id             TEXT,
    event_id            INTEGER NOT NULL REFERENCES events(id),
    purpose             TEXT    NOT NULL CHECK (purpose IN ('turn','compaction','delegate','auto','media','tool')),  -- tool = metering non-LLM (COST-11)
    feature             TEXT    NOT NULL DEFAULT '',   -- auto: review|curator|title|...; media: vision|image|video|stt|tts|embedding; tool: tool:<backend>
    provider            TEXT    NOT NULL,              -- id registry
    model               TEXT    NOT NULL,
    credential_id       TEXT    NOT NULL DEFAULT '',
    gateway_identity    TEXT    NOT NULL DEFAULT '',   -- budget per user gateway
    input_tokens        INTEGER NOT NULL,
    est_input_tokens    INTEGER NOT NULL DEFAULT 0,  -- estimasi lokal sebelum call; sumber kalibrasi EMA (COST-09)
    cached_input_tokens INTEGER NOT NULL DEFAULT 0,
    output_tokens       INTEGER NOT NULL,
    cost_usd_micros     INTEGER,
    cost_source         TEXT    NOT NULL CHECK (cost_source IN ('provider','pricing_table','unknown')),
    pricing_version     TEXT    NOT NULL,      -- versi tabel harga embedded atau 'config' (COST-01)
    attempts            INTEGER NOT NULL,
    latency_ms          INTEGER NOT NULL,
    ttft_ms             INTEGER,
    created_at          INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_usage_turn ON usage_ledger(session_id, turn_id);
CREATE INDEX idx_usage_time ON usage_ledger(created_at, provider);
CREATE INDEX idx_usage_feature ON usage_ledger(purpose, feature, created_at);
CREATE INDEX idx_usage_event ON usage_ledger(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE file_snapshots (
    id          INTEGER PRIMARY KEY,
    event_id    INTEGER NOT NULL REFERENCES events(id),
    path        TEXT    NOT NULL,
    before_sha  TEXT    REFERENCES blobs(sha256),
    after_sha   TEXT    REFERENCES blobs(sha256), -- NULL bila file dihapus
    mode        INTEGER,                          -- permission bits sebelum
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_file_snapshots_path ON file_snapshots(path, id);
CREATE INDEX idx_fsnap_before ON file_snapshots(before_sha) WHERE before_sha IS NOT NULL;  -- indeks anak FK `blobs` (ADR-039)
CREATE INDEX idx_fsnap_after  ON file_snapshots(after_sha)  WHERE after_sha IS NOT NULL;
CREATE INDEX idx_fsnap_event ON file_snapshots(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE approvals (
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    event_id    INTEGER NOT NULL REFERENCES events(id),
    tool_name   TEXT    NOT NULL,
    subject     TEXT    NOT NULL,
    decision    TEXT    NOT NULL CHECK (decision IN ('allow_once','allow_session','deny','edit','auto_allowlist','auto_mode','auto_class_readonly','auto_smart','timeout')),
    source      TEXT    NOT NULL CHECK (source IN ('tui','acp','web','headless','gateway','config','hook','smart_approval','taint','recovery','mcp_serve')),  -- mcp_serve: approval atas call dari klien `damnbot mcp serve` (SEC-21)
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_approvals_event ON approvals(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
CREATE INDEX idx_approvals_session ON approvals(session_id);

CREATE TABLE memory (
    id                INTEGER PRIMARY KEY,
    kind              TEXT    NOT NULL CHECK (kind IN ('memory','skill')),
    category          TEXT    NOT NULL DEFAULT 'fact' CHECK (category IN ('fact','preference','project','procedure')),
    scope             TEXT    NOT NULL,              -- 'user' | 'project:<abs>' | 'gateway:<platform>:<identity>'
    name              TEXT,
    content           TEXT    NOT NULL,
    origin            TEXT    NOT NULL CHECK (origin IN ('agent','user','auto','import','hub')),
    source_session    TEXT    REFERENCES sessions(id),
    evidence_event_id INTEGER REFERENCES events(id),
    supersedes_id     INTEGER REFERENCES memory(id),
    expires_at        INTEGER,
    scan_result       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scan_result)),
    created_at        INTEGER NOT NULL,
    CHECK (kind = 'memory' OR name IS NOT NULL),
    CHECK (kind = 'skill' OR length(content) <= 500),
    CHECK (kind = 'memory' OR length(CAST(content AS BLOB)) <= 8192),
    CHECK (origin NOT IN ('agent','auto') OR evidence_event_id IS NOT NULL OR kind = 'skill')
) STRICT;
CREATE INDEX idx_memory_scope ON memory(scope, kind, category);
CREATE INDEX idx_memory_evidence ON memory(evidence_event_id) WHERE evidence_event_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
CREATE INDEX idx_memory_source ON memory(source_session) WHERE source_session IS NOT NULL;
CREATE INDEX idx_memory_supersedes ON memory(supersedes_id) WHERE supersedes_id IS NOT NULL;

CREATE TABLE memory_decisions (
    id          INTEGER PRIMARY KEY,
    memory_id   INTEGER NOT NULL REFERENCES memory(id),
    status      TEXT    NOT NULL CHECK (status IN ('pending','scanned','approved','rejected','retired','expired')),
    actor       TEXT    NOT NULL CHECK (actor IN ('system','user')),
    reason      TEXT    NOT NULL DEFAULT '',   -- 'confirmed', 'superseded:<id>', 'duplicate', ...
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_memory_decisions ON memory_decisions(memory_id, id DESC);

CREATE TABLE memory_pins (                     -- append-only; status pin efektif = baris terakhir (M-19, §4.8)
    id          INTEGER PRIMARY KEY,
    memory_id   INTEGER NOT NULL REFERENCES memory(id),
    pinned      INTEGER NOT NULL CHECK (pinned IN (0,1)),
    actor       TEXT    NOT NULL CHECK (actor IN ('user','system')),
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_memory_pins ON memory_pins(memory_id, id DESC);

CREATE VIEW v_memory_status AS
SELECT m.*, d.status AS status,
       coalesce((SELECT pinned FROM memory_pins p WHERE p.memory_id = m.id ORDER BY p.id DESC LIMIT 1), 0) AS pinned,
       (SELECT max(created_at) FROM memory_decisions x WHERE x.memory_id = m.id AND x.status = 'approved') AS last_confirmed_at
FROM memory m
JOIN memory_decisions d ON d.id = (
    SELECT id FROM memory_decisions WHERE memory_id = m.id ORDER BY id DESC LIMIT 1);

CREATE TABLE memory_embeddings (               -- opsional (ADR-024)
    memory_id   INTEGER PRIMARY KEY REFERENCES memory(id),
    model       TEXT    NOT NULL,
    dim         INTEGER NOT NULL,
    vec         BLOB    NOT NULL,              -- float32 little-endian
    created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE skills (
    id          INTEGER PRIMARY KEY,
    name        TEXT    NOT NULL,
    path        TEXT    NOT NULL,
    sha256      TEXT    NOT NULL,
    description TEXT    NOT NULL,              -- utuh, tidak dipotong
    body        TEXT    NOT NULL,
    source      TEXT    NOT NULL CHECK (source IN ('user','builtin','hub','agent','import')),
    memory_id   INTEGER REFERENCES memory(id),
    indexed_at  INTEGER NOT NULL,
    UNIQUE (name, sha256)
) STRICT;
CREATE INDEX idx_skills_memory ON skills(memory_id) WHERE memory_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE VIEW v_turn_usage AS
SELECT session_id, turn_id,
       COUNT(*) FILTER (WHERE purpose = 'turn') AS iterations,
       SUM(input_tokens) AS input_tokens,
       SUM(cached_input_tokens) AS cached_input_tokens,
       SUM(output_tokens) AS output_tokens,
       SUM(cost_usd_micros) AS cost_usd_micros,
       SUM(cost_usd_micros IS NULL) AS unknown_cost_calls
FROM usage_ledger GROUP BY session_id, turn_id;

-- FTS5 contentless (INF-01/INF-02, ADR-033 Q50): teks dinormalisasi oleh fungsi Go `dmb_fts_norm(text)`
-- (bigram CJK, didaftarkan ke driver), query memakai normalisasi yang sama; snippet diambil dari tabel sumber.
-- Butuh SQLite ≥ 3.43 (`contentless_delete`), diverifikasi gate REL-04.
-- Perintah FTS5 'rebuild' TIDAK didukung pada tabel contentless: re-index (INF-02 dan recovery korupsi FTS,
-- SELF-HEALING §3.5) = INSERT INTO <fts>(<fts>) VALUES('delete-all') lalu insert ulang dari tabel sumber
-- lewat dmb_fts_norm (blob_fts: dari file blob), batch 5.000 baris/transaksi.
CREATE TABLE fts_meta (                        -- mutable: versi normalisasi/layout per index (re-index otomatis, INF-02)
    name            TEXT PRIMARY KEY,
    norm_version    INTEGER NOT NULL,
    layout_version  INTEGER NOT NULL,
    reindexed_to    INTEGER NOT NULL DEFAULT 0, -- rowid terakhir yang sudah di-index ulang
    updated_at      INTEGER NOT NULL
) STRICT;

CREATE VIRTUAL TABLE events_fts USING fts5(
    content, tool_name,
    content = '', contentless_delete = 1,
    tokenize = "unicode61 remove_diacritics 2 tokenchars '_-./'"
);
CREATE TRIGGER events_ai AFTER INSERT ON events
WHEN NEW.kind IN ('user_message','assistant_message','tool_result','imported')
BEGIN
    INSERT INTO events_fts(rowid, content, tool_name) VALUES (NEW.id, dmb_fts_norm(NEW.content), coalesce(NEW.tool_name, ''));
END;

CREATE VIRTUAL TABLE summaries_fts USING fts5(
    content, content = '', contentless_delete = 1,
    tokenize = "unicode61 remove_diacritics 2"
);
CREATE TRIGGER summaries_ai AFTER INSERT ON summaries BEGIN
    INSERT INTO summaries_fts(rowid, content) VALUES (NEW.id, dmb_fts_norm(NEW.content));
END;

CREATE VIRTUAL TABLE memory_fts USING fts5(
    content, content = '', contentless_delete = 1,
    tokenize = "unicode61 remove_diacritics 2"
);
CREATE TRIGGER memory_ai AFTER INSERT ON memory BEGIN
    INSERT INTO memory_fts(rowid, content) VALUES (NEW.id, dmb_fts_norm(NEW.content));
END;

CREATE VIRTUAL TABLE skills_fts USING fts5(
    name, description, body, content = '', contentless_delete = 1,
    tokenize = "unicode61 remove_diacritics 2"
);
CREATE TRIGGER skills_ai AFTER INSERT ON skills BEGIN
    INSERT INTO skills_fts(rowid, name, description, body) VALUES (NEW.id, NEW.name, dmb_fts_norm(NEW.description), dmb_fts_norm(NEW.body));
END;
-- `skills` mutable (re-index saat file skill berubah): contentless_delete mengizinkan DELETE per rowid, lalu insert ulang.
CREATE TRIGGER skills_au AFTER UPDATE ON skills BEGIN
    DELETE FROM skills_fts WHERE rowid = OLD.id;
    INSERT INTO skills_fts(rowid, name, description, body) VALUES (NEW.id, NEW.name, dmb_fts_norm(NEW.description), dmb_fts_norm(NEW.body));
END;
CREATE TRIGGER skills_ad AFTER DELETE ON skills BEGIN
    DELETE FROM skills_fts WHERE rowid = OLD.id;
END;

-- Output tool penuh dari blob spill (text/*, JSON, ≤ 16 MiB) di-index terpisah; rowid = events.id pemilik blob (INF-02).
CREATE VIRTUAL TABLE blob_fts USING fts5(
    content, content = '', contentless_delete = 1,
    tokenize = "unicode61 remove_diacritics 2 tokenchars '_-./'"
);

-- Immutability (P9). Satu-satunya jalur hapus/ubah: damnbot sessions purge (§11.6). Trigger tidak pernah di-DROP/CREATE (skema tetap,
-- 0 re-prepare statement di proses lain, ADR-042); pengecualian lewat WHEN dmb_maint() IS NOT 'purge', yang hanya bernilai 'purge'
-- di koneksi purge selama transaksinya (§11). sessions/blobs no-UPDATE dan kanban_task_events tidak punya pengecualian.
CREATE TRIGGER events_no_update    BEFORE UPDATE ON events            WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'events is append-only'); END;
CREATE TRIGGER events_no_delete    BEFORE DELETE ON events            WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'events is append-only'); END;
CREATE TRIGGER evatt_no_update     BEFORE UPDATE ON event_attachments WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'event_attachments is append-only'); END;
CREATE TRIGGER evatt_no_delete     BEFORE DELETE ON event_attachments WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'event_attachments is append-only'); END;
CREATE TRIGGER labels_no_update    BEFORE UPDATE ON session_labels    WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'session_labels is append-only'); END;
CREATE TRIGGER labels_no_delete    BEFORE DELETE ON session_labels    WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'session_labels is append-only'); END;
CREATE TRIGGER summaries_no_update BEFORE UPDATE ON summaries         WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'summaries is append-only'); END;
CREATE TRIGGER summaries_no_delete BEFORE DELETE ON summaries         WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'summaries is append-only'); END;
CREATE TRIGGER usage_no_update     BEFORE UPDATE ON usage_ledger      WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'usage_ledger is append-only'); END;
CREATE TRIGGER usage_no_delete     BEFORE DELETE ON usage_ledger      WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'usage_ledger is append-only'); END;
CREATE TRIGGER sessions_no_update  BEFORE UPDATE ON sessions          BEGIN SELECT RAISE(ABORT, 'sessions is immutable; use session_labels'); END;
CREATE TRIGGER blobs_no_update     BEFORE UPDATE ON blobs             BEGIN SELECT RAISE(ABORT, 'blobs is immutable'); END;
CREATE TRIGGER sessions_no_delete  BEFORE DELETE ON sessions          WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'sessions are deleted only by purge'); END;
CREATE TRIGGER blobs_no_delete     BEFORE DELETE ON blobs             WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'blobs rows are deleted only by purge'); END;
CREATE TRIGGER memory_no_update    BEFORE UPDATE ON memory            WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory is immutable; use memory_decisions'); END;
CREATE TRIGGER memory_no_delete    BEFORE DELETE ON memory            WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory is append-only'); END;
CREATE TRIGGER memdec_no_update    BEFORE UPDATE ON memory_decisions  WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory_decisions is append-only'); END;
CREATE TRIGGER memdec_no_delete    BEFORE DELETE ON memory_decisions  WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory_decisions is append-only'); END;
CREATE TRIGGER mempin_no_update    BEFORE UPDATE ON memory_pins       WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory_pins is append-only'); END;
CREATE TRIGGER mempin_no_delete    BEFORE DELETE ON memory_pins       WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'memory_pins is append-only'); END;
CREATE TRIGGER approvals_no_update BEFORE UPDATE ON approvals         WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'approvals is append-only'); END;
CREATE TRIGGER approvals_no_delete BEFORE DELETE ON approvals         WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'approvals is append-only'); END;
CREATE TRIGGER fsnap_no_update     BEFORE UPDATE ON file_snapshots    WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'file_snapshots is append-only'); END;
CREATE TRIGGER fsnap_no_delete     BEFORE DELETE ON file_snapshots    WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'file_snapshots is append-only'); END;
