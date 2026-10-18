-- 0007_gaps.sql — carrier reasoning (PROV-01), pin tool MCP (SEC-04), sesi web (SEC-09),
--                 cursor gateway non-Telegram (GW-08), batas model (COST-02/PROV-02), circuit lintas proses (COST-06),
--                 token mcp serve (SEC-21)

CREATE TABLE reasoning_carriers (              -- append-only (P9)
    id              INTEGER PRIMARY KEY,
    event_id        INTEGER NOT NULL REFERENCES events(id),   -- assistant_message
    ord             INTEGER NOT NULL,
    provider_family TEXT    NOT NULL,                         -- anthropic|gemini|openai_responses|copilot|...
    model           TEXT    NOT NULL,
    kind            TEXT    NOT NULL CHECK (kind IN ('thought_signature','thinking','redacted_thinking','encrypted_reasoning','reasoning_opaque')),
    anchor          TEXT    NOT NULL DEFAULT '',              -- tool_call_id / indeks part
    payload         BLOB    NOT NULL,                         -- byte-identik dari wire; dikecualikan dari redaksi (§14.3); terenkripsi bila storage.encrypt
    payload_sha     TEXT    NOT NULL,                         -- juga dicatat di events.payload.carriers[] → masuk hash-chain
    replayable      INTEGER NOT NULL DEFAULT 1 CHECK (replayable IN (0,1)),
    created_at      INTEGER NOT NULL,
    UNIQUE (event_id, ord)
) STRICT;
CREATE TABLE reasoning_carrier_rejections (    -- append-only; signature ditolak provider, tahan restart
    carrier_id  INTEGER NOT NULL REFERENCES reasoning_carriers(id),
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    error_code  TEXT    NOT NULL,
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (carrier_id, session_id)
) STRICT;
CREATE INDEX idx_carrier_rej_session ON reasoning_carrier_rejections(session_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE mcp_tool_pins (                   -- mutable: status pin per tool MCP
    server_id    TEXT    NOT NULL,
    tool_name    TEXT    NOT NULL,
    pin_sha      TEXT    NOT NULL,             -- sha256(nama ‖ deskripsi ‖ inputSchema kanonik ‖ annotations) saat disetujui
    pending_sha  TEXT,                         -- definisi baru yang belum disetujui
    status       TEXT    NOT NULL CHECK (status IN ('approved','quarantined','removed')),
    approved_at  INTEGER,
    updated_at   INTEGER NOT NULL,
    PRIMARY KEY (server_id, tool_name)
) STRICT;

CREATE TABLE web_sessions (                    -- mutable: sesi web UI server-side
    id_hash          TEXT PRIMARY KEY,         -- sha256 token cookie
    principal        TEXT    NOT NULL,
    created_at       INTEGER NOT NULL,
    last_seen_at     INTEGER NOT NULL,
    absolute_expiry  INTEGER NOT NULL,         -- maks 7 hari, tanpa perpanjangan
    revoked_at       INTEGER
) STRICT;

CREATE TABLE gateway_cursors (                 -- mutable: cursor/offset per adapter (IMAP UID, stream offset, dsb.)
    platform    TEXT    NOT NULL,
    account     TEXT    NOT NULL,
    cursor      TEXT    NOT NULL,
    updated_at  INTEGER NOT NULL,
    PRIMARY KEY (platform, account)
) STRICT;

CREATE TABLE model_limits (                   -- mutable: context window per model (COST-02, PROV-02)
    provider        TEXT    NOT NULL,
    model           TEXT    NOT NULL,
    context_window  INTEGER NOT NULL CHECK (context_window > 0),
    source          TEXT    NOT NULL CHECK (source IN ('config','registry','discovery','learned','fallback')),
    observed_at     INTEGER NOT NULL,
    PRIMARY KEY (provider, model)
) STRICT;

CREATE TABLE provider_circuits (               -- mutable: circuit breaker & pacing lintas proses (COST-06)
    provider       TEXT    NOT NULL,
    credential_id  TEXT    NOT NULL DEFAULT '',
    state          TEXT    NOT NULL CHECK (state IN ('closed','open','half_open')),
    open_until     INTEGER,
    remaining_req  INTEGER,
    remaining_tok  INTEGER,
    reset_at       INTEGER,
    updated_at     INTEGER NOT NULL,
    PRIMARY KEY (provider, credential_id)
) STRICT;

CREATE TABLE mcp_serve_tokens (                -- mutable: token klien `damnbot mcp serve` (SEC-21)
    id_hash     TEXT PRIMARY KEY,              -- sha256 token
    profile     TEXT    NOT NULL,
    identity    TEXT    NOT NULL,
    scope       TEXT    NOT NULL CHECK (scope IN ('read','send','approve')),  -- approve: hanya list/respond approval (§11.11.10)
    created_at  INTEGER NOT NULL,
    revoked_at  INTEGER
) STRICT;

CREATE TRIGGER carriers_no_update   BEFORE UPDATE ON reasoning_carriers           WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'reasoning_carriers is append-only'); END;
CREATE TRIGGER carriers_no_delete   BEFORE DELETE ON reasoning_carriers           WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'reasoning_carriers is append-only'); END;
CREATE TRIGGER carrierrej_no_update BEFORE UPDATE ON reasoning_carrier_rejections WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'reasoning_carrier_rejections is append-only'); END;
CREATE TRIGGER carrierrej_no_delete BEFORE DELETE ON reasoning_carrier_rejections WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'reasoning_carrier_rejections is append-only'); END;
