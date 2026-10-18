-- 0002_runtime.sql — lease, state modul, katalog tool, cache MCP, impor (tabel mutable eksplisit)

CREATE TABLE session_leases (
    session_id     TEXT PRIMARY KEY REFERENCES sessions(id),
    fence_token    INTEGER NOT NULL UNIQUE,      -- dari module_state('ctxdb','fence_seq'), naik monoton tiap akuisisi (ADR-042)
    owner_pid      INTEGER NOT NULL,
    owner_start    TEXT    NOT NULL,             -- cap start proses opaque dari OS, dibandingkan persis (§11.11.1; anti PID reuse)
    owner_host     TEXT    NOT NULL,             -- '<hostname>#<pid-namespace>' (Linux: inode /proc/self/ns/pid); identitas proses satu kernel (§11.11.1)
    owner_boot_id  TEXT    NOT NULL,             -- identitas boot kernel opaque ('' bila tidak tersedia), §11.11.1
    owner_kind     TEXT    NOT NULL,             -- tui|acp|serve|headless|cli|mcp_serve
    heartbeat_at   INTEGER NOT NULL,             -- jam boot (ms), bukan jam dinding (§3.3)
    expires_at     INTEGER NOT NULL              -- jam boot (ms)
) STRICT;

CREATE TABLE module_state (                    -- KV modul (offset long-poll, cursor, rate limit window)
    module_id   TEXT    NOT NULL,
    key         TEXT    NOT NULL,
    value       TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (module_id, key)
) STRICT;

CREATE TABLE tool_catalog (                    -- indeks tool deferred; dibangun ulang saat versi/config berubah
    name           TEXT PRIMARY KEY,
    grp            TEXT    NOT NULL,
    module_id      TEXT    NOT NULL,
    description    TEXT    NOT NULL,
    schema         TEXT    NOT NULL CHECK (json_valid(schema)),
    effect         TEXT    NOT NULL CHECK (effect IN ('read','idempotent','non_idempotent','dynamic')),
    schema_partial INTEGER NOT NULL DEFAULT 0,
    catalog_sha    TEXT    NOT NULL
) STRICT;
CREATE VIRTUAL TABLE tool_catalog_fts USING fts5(
    name, description, params, content = '', tokenize = "unicode61 remove_diacritics 2 tokenchars '_'"
);

CREATE TABLE mcp_tool_cache (
    server_id   TEXT    NOT NULL,
    config_sha  TEXT    NOT NULL,
    tools_json  TEXT    NOT NULL CHECK (json_valid(tools_json)),
    server_info TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(server_info)),
    scan_result TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(scan_result)),
    fetched_at  INTEGER NOT NULL,
    PRIMARY KEY (server_id, config_sha)
) STRICT;

CREATE TABLE import_runs (
    id          INTEGER PRIMARY KEY,
    source      TEXT    NOT NULL CHECK (source IN ('hermes','claude_code','codex','openclaw')),
    source_path TEXT    NOT NULL,
    stats       TEXT    NOT NULL CHECK (json_valid(stats)),
    created_at  INTEGER NOT NULL
) STRICT;

CREATE TABLE imported_items (
    source      TEXT    NOT NULL,
    source_key  TEXT    NOT NULL,              -- mis. "session:<id>", "skill:<name>@<sha>"
    target_kind TEXT    NOT NULL,
    target_id   TEXT    NOT NULL,
    run_id      INTEGER NOT NULL REFERENCES import_runs(id),
    PRIMARY KEY (source, source_key)
) STRICT;
CREATE INDEX idx_imported_items_run ON imported_items(run_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
