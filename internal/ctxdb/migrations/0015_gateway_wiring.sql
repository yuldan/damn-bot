-- 0015_gateway_wiring.sql -- topik di baris inbound, message id edit-stream, dan
-- klaim callback sekali (ADR-249). Murni tambahan: kolom baru + dua tabel baru;
-- tabel/guard 0003 tidak diedit, guard inbound dibuat ulang agar kolom baru
-- immutable seperti kolom lain. DDL SPEC §11.4 diperbarui pada blok yang sama.

ALTER TABLE gateway_inbound ADD COLUMN thread_id TEXT NOT NULL DEFAULT '';  -- topik platform; routing pasca-restart (ADR-249)

DROP TRIGGER gwin_update_guard;
CREATE TRIGGER gwin_update_guard BEFORE UPDATE ON gateway_inbound
WHEN OLD.event_id IS NOT NULL OR NEW.event_id IS NULL OR NEW.payload IS NOT NULL
  OR NEW.platform IS NOT OLD.platform OR NEW.external_id IS NOT OLD.external_id
  OR NEW.chat_id IS NOT OLD.chat_id OR NEW.sender_id IS NOT OLD.sender_id
  OR NEW.received_at IS NOT OLD.received_at OR NEW.thread_id IS NOT OLD.thread_id
BEGIN SELECT RAISE(ABORT, 'gateway_inbound: only (event_id NULL->value, payload->NULL)'); END;

CREATE TABLE gateway_message_ids (      -- message id outbound per idem key (edit-stream idempoten, ADR-249)
    key        TEXT    PRIMARY KEY,     -- `gateway_deliveries.idem_key`
    message_id INTEGER NOT NULL,        -- id pesan platform; 0 = uncertain (kirim timeout, id tidak diketahui)
    updated_at INTEGER NOT NULL
) STRICT;

CREATE TABLE gateway_callbacks (        -- klaim callback query sekali (ADR-249)
    callback_id TEXT    PRIMARY KEY,
    claimed_at  INTEGER NOT NULL
) STRICT;
