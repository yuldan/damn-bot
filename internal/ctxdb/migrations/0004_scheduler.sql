-- 0004_scheduler.sql — cron (catch-up, lease) dan kanban

CREATE TABLE cron_jobs (
    id                  TEXT PRIMARY KEY,      -- ULID
    name                TEXT    NOT NULL UNIQUE,
    owner_identity      TEXT    NOT NULL DEFAULT 'local',  -- ≤ 20 job aktif per identitas (COST-14)
    schedule            TEXT    NOT NULL,
    tz                  TEXT,                  -- IANA; NULL = zona config (PROV-03)
    prompt              TEXT    NOT NULL,
    prompt_sha          TEXT    NOT NULL,      -- sha256 prompt ternormalisasi (dedup COST-14)
    model               TEXT,                  -- NULL = cron.default_model (Q50)
    cwd                 TEXT    NOT NULL,
    deliver_to          TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(deliver_to)),
    max_cost_usd        REAL    NOT NULL DEFAULT 0.10,
    approval_mode       TEXT    NOT NULL DEFAULT 'ask' CHECK (approval_mode IN ('ask','auto-edit','auto')),
    catch_up            TEXT    NOT NULL DEFAULT 'run_once' CHECK (catch_up IN ('skip','run_once','run_all')),
    max_catch_up        INTEGER NOT NULL DEFAULT 3 CHECK (max_catch_up BETWEEN 1 AND 10),
    overlap             TEXT    NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip','queue_one','parallel')),  -- REL-08
    idempotent          INTEGER NOT NULL DEFAULT 0 CHECK (idempotent IN (0,1)),                         -- REL-09
    retry_on_crash      INTEGER NOT NULL DEFAULT 0 CHECK (retry_on_crash IN (0,1)),
    pause_after_crashes INTEGER NOT NULL DEFAULT 3 CHECK (pause_after_crashes BETWEEN 1 AND 20),          -- REL-09
    paused_at           INTEGER,               -- NULL = tidak di-pause
    pause_reason        TEXT,
    gate                TEXT    CHECK (gate IS NULL OR json_valid(gate)),                                 -- COST-13
    after               TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(after)),                          -- REL-16 DAG
    after_timeout_s     INTEGER NOT NULL DEFAULT 3600 CHECK (after_timeout_s BETWEEN 1 AND 86400),
    enabled             INTEGER NOT NULL DEFAULT 0,
    scan_result         TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scan_result)),
    created_at          INTEGER NOT NULL,
    updated_at          INTEGER NOT NULL,
    UNIQUE (prompt_sha, schedule, deliver_to)  -- COST-14
) STRICT;

CREATE TABLE cron_runs (
    id           INTEGER PRIMARY KEY,
    job_id       TEXT    NOT NULL REFERENCES cron_jobs(id),
    session_id   TEXT    REFERENCES sessions(id),
    scheduled_at INTEGER NOT NULL,
    claimed_by   TEXT,                         -- pid@host pemegang lease
    started_at   INTEGER,
    finished_at  INTEGER,
    stop_reason  TEXT,
    outcome      TEXT    CHECK (outcome IN ('ok','failed','crashed','missed','skipped_safe_mode','skipped_overlap','gated')),  -- REL-08, COST-13
    error        TEXT    NOT NULL DEFAULT '',
    UNIQUE (job_id, scheduled_at)              -- satu run per jadwal, lintas proses & restart
) STRICT;
CREATE INDEX idx_cron_runs_session ON cron_runs(session_id) WHERE session_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE kanban_boards (
    id          TEXT PRIMARY KEY,
    name        TEXT    NOT NULL UNIQUE,
    created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE kanban_tasks (
    id          TEXT PRIMARY KEY,
    board_id    TEXT    NOT NULL REFERENCES kanban_boards(id),
    title       TEXT    NOT NULL,
    body        TEXT    NOT NULL DEFAULT '',
    status      TEXT    NOT NULL CHECK (status IN ('backlog','ready','in_progress','review','done','blocked')),
    assignee    TEXT    NOT NULL DEFAULT '',
    priority    INTEGER NOT NULL DEFAULT 2 CHECK (priority BETWEEN 0 AND 4),
    labels      TEXT    NOT NULL DEFAULT '[]' CHECK (json_valid(labels)),  -- label 'agent' → watcher menjalankan agent (M-19)
    due_at      INTEGER,
    session_id  TEXT    REFERENCES sessions(id),
    created_at  INTEGER NOT NULL,
    updated_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_kanban_status ON kanban_tasks(board_id, status, priority);
CREATE INDEX idx_kanban_session ON kanban_tasks(session_id) WHERE session_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE kanban_task_events (
    id          INTEGER PRIMARY KEY,
    task_id     TEXT    NOT NULL REFERENCES kanban_tasks(id),
    actor       TEXT    NOT NULL,
    kind        TEXT    NOT NULL CHECK (kind IN ('created','status','comment','assign','edit','schedule')),
    payload     TEXT    NOT NULL CHECK (json_valid(payload)),
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_kanban_events_task ON kanban_task_events(task_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
CREATE TRIGGER kanban_events_no_update BEFORE UPDATE ON kanban_task_events BEGIN SELECT RAISE(ABORT, 'kanban_task_events is append-only'); END;
CREATE TRIGGER kanban_events_no_delete BEFORE DELETE ON kanban_task_events BEGIN SELECT RAISE(ABORT, 'kanban_task_events is append-only'); END;
