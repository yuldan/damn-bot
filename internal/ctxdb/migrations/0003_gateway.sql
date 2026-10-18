-- 0003_gateway.sql — identitas & peran, percakapan, dedupe inbound, outbox

CREATE TABLE gateway_identities (
    platform      TEXT    NOT NULL,
    external_id   TEXT    NOT NULL,
    display_name  TEXT    NOT NULL DEFAULT '',
    role          TEXT    NOT NULL DEFAULT 'user' CHECK (role IN ('owner','user')),
    status        TEXT    NOT NULL CHECK (status IN ('pending','allowlisted','blocked')),
    pairing_hash  TEXT,                        -- sha256 kode pairing, NULL setelah dipakai
    pairing_until INTEGER,
    workspace     TEXT,                        -- batas workspace per user (opsional)
    tz            TEXT,                        -- zona IANA per identitas; NULL = zona host (PROV-03)
    strength      TEXT    NOT NULL CHECK (strength IN ('cryptographic','platform_authenticated','weak')),  -- = Caps.IdentityStrength, wajib diisi adapter (tanpa DEFAULT: fail-closed, V-13/ADR-054); weak → tidak bisa di-pair (SEC-10)
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (platform, external_id)
) STRICT;

CREATE TABLE gateway_conversations (
    platform    TEXT    NOT NULL,
    chat_id     TEXT    NOT NULL,
    thread_id   TEXT    NOT NULL DEFAULT '',
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    created_at  INTEGER NOT NULL,
    PRIMARY KEY (platform, chat_id, thread_id, session_id)
) STRICT;
CREATE INDEX idx_gw_conv_latest ON gateway_conversations(platform, chat_id, thread_id, created_at DESC);
CREATE INDEX idx_gw_conv_session ON gateway_conversations(session_id);  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)

CREATE TABLE gateway_inbound (
    platform     TEXT    NOT NULL,
    external_id  TEXT    NOT NULL,             -- update_id / message id platform
    chat_id      TEXT    NOT NULL,
    sender_id    TEXT    NOT NULL,
    event_id     INTEGER REFERENCES events(id), -- NULL = antre; diisi tepat sekali oleh worker (SELF-HEALING §5.1)
    payload      TEXT    CHECK (payload IS NULL OR json_valid(payload)),  -- isi pesan durable selama antre (ADR-046): {text, caption?, reply_to?, attachments[].blob_sha, kind};
                                               -- setelah redaksi §14.3, dienkripsi bila storage.encrypt; NULL-kan bersamaan dengan pengisian event_id
    received_at  INTEGER NOT NULL,
    PRIMARY KEY (platform, external_id),
    CHECK (event_id IS NOT NULL OR payload IS NOT NULL)   -- baris antre selalu membawa isi
) STRICT;
CREATE INDEX idx_gw_inbound_event ON gateway_inbound(event_id) WHERE event_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
CREATE INDEX idx_gw_inbound_queue ON gateway_inbound(platform, chat_id, received_at, external_id) WHERE event_id IS NULL;  -- antrean kerja per chat (ADR-046)
-- Mutable terbatas: satu-satunya UPDATE yang sah = (event_id NULL → nilai, payload → NULL) dalam satu UPDATE; kolom lain dan DELETE ditolak
-- (kecuali `sessions purge`: trigger ber-`WHEN dmb_maint() IS NOT 'purge'` seperti tabel append-only, ADR-042).
CREATE TRIGGER gwin_update_guard BEFORE UPDATE ON gateway_inbound
WHEN OLD.event_id IS NOT NULL OR NEW.event_id IS NULL OR NEW.payload IS NOT NULL
  OR NEW.platform IS NOT OLD.platform OR NEW.external_id IS NOT OLD.external_id
  OR NEW.chat_id IS NOT OLD.chat_id OR NEW.sender_id IS NOT OLD.sender_id OR NEW.received_at IS NOT OLD.received_at
BEGIN SELECT RAISE(ABORT, 'gateway_inbound: only (event_id NULL->value, payload->NULL)'); END;
CREATE TRIGGER gwin_no_delete BEFORE DELETE ON gateway_inbound WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'gateway_inbound is insert-only'); END;

CREATE TABLE gateway_deliveries (              -- outbox; status mutable
    id            INTEGER PRIMARY KEY,
    idem_key      TEXT    NOT NULL UNIQUE,
    platform      TEXT    NOT NULL,
    chat_id       TEXT    NOT NULL,
    thread_id     TEXT    NOT NULL DEFAULT '',
    event_id      INTEGER REFERENCES events(id),
    body          TEXT    NOT NULL,
    media_blob_sha TEXT   REFERENCES blobs(sha256),  -- media outbound (TTS/gambar/dokumen); ref blob owner `gateway_delivery` (ADR-039)
    status        TEXT    NOT NULL CHECK (status IN ('queued','sending','sent','failed','dead','uncertain')),
    attempts      INTEGER NOT NULL DEFAULT 0,
    last_error    TEXT    NOT NULL DEFAULT '',
    external_ref  TEXT,
    next_at       INTEGER NOT NULL,
    updated_at    INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_gw_deliveries_due ON gateway_deliveries(status, next_at);
CREATE INDEX idx_gw_deliveries_media ON gateway_deliveries(media_blob_sha) WHERE media_blob_sha IS NOT NULL;  -- indeks anak FK `blobs`
CREATE INDEX idx_gw_deliveries_event ON gateway_deliveries(event_id) WHERE event_id IS NOT NULL;  -- indeks anak FK (ADR-040, `TestFKChildIndexes`)
