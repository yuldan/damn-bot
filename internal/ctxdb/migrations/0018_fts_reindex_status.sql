-- 0018_fts_reindex_status.sql — progress/status re-index FTS (GAP-INF-02, F9).
--
-- fts_meta sudah menyimpan versi normalisasi/layout dan cursor reindexed_to
-- (0001). Upgrade background butuh dua kolom tambahan agar status dapat
-- dilaporkan ke `doctor` dan re-index yang terputus dapat dilanjutkan:
-- `status` = 'done' | 'partial' dan `total` = jumlah baris sumber saat
-- re-index tabel itu dimulai. Baris lama default 'done' karena index lama
-- memang lengkap untuk versi yang tercatat.
ALTER TABLE fts_meta ADD COLUMN status TEXT NOT NULL DEFAULT 'done'
    CHECK (status IN ('done','partial'));
ALTER TABLE fts_meta ADD COLUMN total INTEGER NOT NULL DEFAULT 0;
