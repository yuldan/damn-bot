-- 0005_recovery.sql — journal turn mutable (bukan transcript; P9 tidak berlaku)
CREATE TABLE turn_journal (
    turn_id           TEXT    PRIMARY KEY,
    session_id        TEXT    NOT NULL REFERENCES sessions(id),
    state             TEXT    NOT NULL CHECK (state IN (
                          'running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval','crashed','resumed')),
    current_iteration INTEGER NOT NULL DEFAULT 0 CHECK (current_iteration BETWEEN 0 AND 40),
    last_tool_call_id TEXT,
    last_tool_name    TEXT,
    tool_class        TEXT    CHECK (tool_class IN ('read','idempotent','non_idempotent')),
    idempotency_key   TEXT,
    args_sha          TEXT,
    target_path       TEXT,
    before_sha        TEXT,                    -- hash pembanding isi file, bukan ref blob (before-image dirujuk `actions`, ADR-039)
    after_sha         TEXT,                    -- hash pembanding isi file, bukan ref blob
    resume_turn_id    TEXT,
    tool_pgid         INTEGER,                 -- process group tool `exec` yang sedang berjalan (ADR-046); dibunuh pengambil alih di host sama
    tool_pgid_start   TEXT,                    -- cap start opaque leader group (§11.11.1); verifikasi sebelum SIGKILL
    approval_request  TEXT    CHECK (approval_request IS NULL OR json_valid(approval_request)),  -- SEC-13: request_id, tool, subject ter-redaksi, `preview_blob_sha` (ref blob, §11.6), preview_sha, channels[], expires_at (§11.11.10)
    started_at        INTEGER NOT NULL,
    updated_at        INTEGER NOT NULL,
    CHECK ((last_tool_call_id IS NULL) = (last_tool_name IS NULL)),
    CHECK (last_tool_call_id IS NULL OR (tool_class IS NOT NULL AND args_sha IS NOT NULL)),
    CHECK (state <> 'tool_pending' OR last_tool_call_id IS NOT NULL),
    CHECK (state <> 'awaiting_approval' OR approval_request IS NOT NULL),
    CHECK (tool_class IS NOT 'idempotent' OR last_tool_name NOT IN ('write_file','edit_file')
           OR (target_path IS NOT NULL AND after_sha IS NOT NULL))
) STRICT;
CREATE INDEX idx_turn_journal_open ON turn_journal(state) WHERE state IN ('running','tool_pending','tool_done','awaiting_confirmation','awaiting_approval');
CREATE INDEX idx_turn_journal_session ON turn_journal(session_id, started_at);
