-- 0006_actions.sql — action journal (undo universal), status & rujukan blob, plan mode, scorecard mingguan

CREATE TABLE actions (                         -- append-only; satu baris per aksi mutating
    id            INTEGER PRIMARY KEY,
    session_id    TEXT    NOT NULL REFERENCES sessions(id),
    turn_id       TEXT,
    event_id      INTEGER NOT NULL REFERENCES events(id),     -- tool_dispatch atau event config/memory
    kind          TEXT    NOT NULL CHECK (kind IN ('file_write','file_edit','file_delete','file_move','shell','memory','skill','config','external')),
    reversibility TEXT    NOT NULL CHECK (reversibility IN ('reversible','partial','irreversible')),
    subject       TEXT    NOT NULL,                           -- path / command / key
    inverse       TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(inverse)), -- `$.before_blob_shas[]` before-image (ref blob, §11.6) / snapshot refs / row id / ref git
    bytes_held    INTEGER NOT NULL DEFAULT 0,                 -- blob yang ditahan untuk undo (retensi)
    created_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_actions_session ON actions(session_id, id DESC);
CREATE INDEX idx_actions_subject ON actions(subject, id DESC);
CREATE INDEX idx_actions_event ON actions(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE action_undos (                    -- append-only; hasil undo
    id          INTEGER PRIMARY KEY,
    action_id   INTEGER NOT NULL REFERENCES actions(id),
    status      TEXT    NOT NULL CHECK (status IN ('undone','conflict','expired','failed')),
    detail      TEXT    NOT NULL DEFAULT '',
    event_id    INTEGER NOT NULL REFERENCES events(id),
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_action_undos ON action_undos(action_id);
CREATE INDEX idx_action_undos_event ON action_undos(event_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE blob_expirations (                -- append-only event-log status file blob (ADR-037); baris `blobs` tetap, FK utuh
    id          INTEGER PRIMARY KEY,
    sha256      TEXT    NOT NULL REFERENCES blobs(sha256),
    kind        TEXT    NOT NULL CHECK (kind IN ('expired','restored')),  -- expired: file dihapus `doctor gc` (tanpa ref hidup) atau tidak ada di arsip restore; restored: isi sama ditulis ulang
    reason      TEXT    NOT NULL CHECK (reason IN ('journal_retention','orphan','quota','restore_missing','rewrite')),
    at          INTEGER NOT NULL,
    CHECK ((kind = 'expired') = (reason <> 'rewrite'))
) STRICT;
CREATE INDEX idx_blob_exp_latest ON blob_expirations(sha256, id DESC);
-- Status blob = baris terakhir per sha256 (MAX(id)): tidak ada baris atau `restored` → hidup; `expired` → pembaca mendapat `expired`.
-- `expired` hanya ditulis `doctor gc` untuk sha tanpa ref hidup di `blob_refs`, di bawah lock tulis (protokol §11.6, ADR-039), atau oleh restore (`restore_missing`).

CREATE TABLE blob_refs (                       -- append-only (P9); satu baris per rujukan blob dari sumber mana pun (ADR-039, §11.6)
    id          INTEGER PRIMARY KEY,
    sha256      TEXT    NOT NULL REFERENCES blobs(sha256),
    owner_kind  TEXT    NOT NULL CHECK (owner_kind IN ('event','event_attachment','file_snapshot','action','turn_journal','gateway_delivery','gateway_inbound')),
    owner_id    TEXT    NOT NULL,              -- events.id | '<event_id>:<ord>' | file_snapshots.id | actions.id | turn_journal.turn_id | gateway_deliveries.id | '<platform>:<external_id>'
    ref_key     TEXT    NOT NULL,              -- 'blob_sha' | path JSON kunci `*_blob_sha(s)` (mis. 'payload.raw_blob_sha') | 'before_sha' | 'after_sha' | 'media_blob_sha'
    session_id  TEXT    NOT NULL REFERENCES sessions(id),   -- sesi owner; owner tanpa sesi → '__system__' (purge per sesi)
    created_at  INTEGER NOT NULL,              -- = created_at owner (aturan hidup berbasis waktu tanpa join)
    UNIQUE (owner_kind, owner_id, ref_key, sha256)   -- sekaligus indeks (owner_kind, owner_id); replay idempoten
) STRICT;
CREATE INDEX idx_blob_refs_sha ON blob_refs(sha256);              -- re-check liveness gc + NOT EXISTS purge, O(log n)
CREATE INDEX idx_blob_refs_session ON blob_refs(session_id);      -- purge
-- Writer blob (§11.6) yang menulis ulang sha256 berstatus `expired` meng-INSERT baris `restored` dalam transaksi yang sama dengan `INSERT OR IGNORE INTO blobs`.

CREATE TABLE plans (                           -- append-only; versi plan baru = row baru
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    turn_id     TEXT    NOT NULL,
    version     INTEGER NOT NULL,
    content     TEXT    NOT NULL CHECK (json_valid(content)),  -- plan/v1
    decision    TEXT    NOT NULL CHECK (decision IN ('proposed','approved','edited','rejected')),
    event_id    INTEGER NOT NULL REFERENCES events(id),
    created_at  INTEGER NOT NULL,
    UNIQUE (turn_id, version)
) STRICT;
CREATE INDEX idx_plans_session ON plans(session_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
CREATE INDEX idx_plans_event ON plans(event_id);

CREATE TABLE scorecard_runs (
    id              INTEGER PRIMARY KEY,
    started_at      INTEGER NOT NULL,
    finished_at     INTEGER,
    damnbot_version TEXT    NOT NULL,
    hermes_commit   TEXT    NOT NULL,
    evalset         TEXT    NOT NULL,
    status          TEXT    NOT NULL CHECK (status IN ('running','pass','fail','error')),
    lost_dimensions TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(lost_dimensions)),
    artifact_path   TEXT    NOT NULL,
    cost_usd_micros INTEGER NOT NULL DEFAULT 0
) STRICT;

CREATE TRIGGER actions_no_update  BEFORE UPDATE ON actions      WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'actions is append-only'); END;
CREATE TRIGGER undos_no_update    BEFORE UPDATE ON action_undos WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'action_undos is append-only'); END;
CREATE TRIGGER plans_no_update    BEFORE UPDATE ON plans        WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'plans is append-only'); END;
CREATE TRIGGER actions_no_delete  BEFORE DELETE ON actions      WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'actions is append-only'); END;
CREATE TRIGGER undos_no_delete    BEFORE DELETE ON action_undos WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'action_undos is append-only'); END;
CREATE TRIGGER plans_no_delete    BEFORE DELETE ON plans        WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'plans is append-only'); END;
CREATE TRIGGER blobexp_no_update  BEFORE UPDATE ON blob_expirations WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'blob_expirations is append-only'); END;
CREATE TRIGGER blobexp_no_delete  BEFORE DELETE ON blob_expirations WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'blob_expirations is append-only'); END;
CREATE TRIGGER blobrefs_no_update BEFORE UPDATE ON blob_refs        WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'blob_refs is append-only'); END;
CREATE TRIGGER blobrefs_no_delete BEFORE DELETE ON blob_refs        WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'blob_refs is append-only'); END;
