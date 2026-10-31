-- 0017_mcp_tool_pins.sql — kontrak pin tool MCP (SEC-04, GAP-SEC-04, ADR-272).
--
-- Tabel `mcp_tool_pins` dari 0007 belum pernah ditulis (tidak ada kode pemakai
-- sebelum F5-10) dan kolomnya tidak memuat hash schema/state yang dipakai
-- deteksi rug-pull. Migrasi ini membangun ulang bentuk finalnya; tidak ada
-- baris yang hilang karena tabel belum pernah diisi pada rilis pra-1.0.
-- Schema mentah tidak disimpan: hanya sha256 definisi ternormalisasi dan
-- ringkasan diff (JSON, tanpa isi schema).

DROP TABLE mcp_tool_pins;
CREATE TABLE mcp_tool_pins (                   -- mutable: pin per tool MCP (SEC-04)
    server_id     TEXT    NOT NULL,
    tool_name     TEXT    NOT NULL,
    schema_sha256 TEXT    NOT NULL CHECK (length(schema_sha256) = 64),  -- sha256 definisi kanonik (nama‖deskripsi‖schema‖annotations)
    first_seen    INTEGER NOT NULL,
    approved_at   INTEGER,                     -- NULL bila belum pernah disetujui (karantina baru)
    approved_by   TEXT    NOT NULL DEFAULT '',
    state         TEXT    NOT NULL CHECK (state IN ('pinned','changed','quarantined')),
    diff_summary  TEXT    NOT NULL DEFAULT '{}' CHECK (json_valid(diff_summary)),
    updated_at    INTEGER NOT NULL,
    PRIMARY KEY (server_id, tool_name)
) STRICT;
