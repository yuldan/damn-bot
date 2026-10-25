-- 0012_session_pins.sql -- pin sisi prompt (MEM-025, ADR-186): posisi transkrip
-- yang tidak boleh dikompres. Append-only dengan semantik nilai terakhir menang:
-- baris pinned=1 mem-pin, baris pinned=0 melepas pin; seq aktif = baris terbaru
-- per (session_id, seq). Tidak menyentuh tabel/trigger lama (memperluas CHECK
-- session_labels akan menuntut rebuild tabel + ~52 baris trigger, ADR-186).

CREATE TABLE session_pins (                    -- append-only (P9)
    id          INTEGER PRIMARY KEY,
    session_id  TEXT    NOT NULL REFERENCES sessions(id),
    seq         INTEGER NOT NULL CHECK (seq > 0),
    pinned      INTEGER NOT NULL CHECK (pinned IN (0,1)),
    created_at  INTEGER NOT NULL
) STRICT;
CREATE INDEX idx_session_pins ON session_pins(session_id, seq, id DESC);
CREATE INDEX idx_session_pins_session ON session_pins(session_id);  -- indeks anak FK (ADR-040)

CREATE TRIGGER session_pins_no_update BEFORE UPDATE ON session_pins BEGIN SELECT RAISE(ABORT, 'session_pins is append-only'); END;
CREATE TRIGGER session_pins_no_delete BEFORE DELETE ON session_pins WHEN dmb_maint() IS NOT 'purge' BEGIN SELECT RAISE(ABORT, 'session_pins is append-only'); END;
CREATE TRIGGER guard_session_pins_ins BEFORE INSERT ON session_pins WHEN dmb_maint() IS NOT 'purge' BEGIN
    SELECT RAISE(ABORT, 'session_purging') WHERE EXISTS (SELECT 1 FROM session_labels WHERE session_id = NEW.session_id AND key = 'purge_state');
END;
