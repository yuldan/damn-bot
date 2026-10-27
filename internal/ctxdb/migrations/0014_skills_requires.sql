-- 0014_skills_requires.sql -- requirement host skill (GAP-TOOL-10, ADR-208):
-- JSON `requires {bins,os,arch,gpu}` disimpan bersama baris index supaya jalur
-- baca (katalog/prompt) memakai requirement tersimpan, bukan membaca ulang
-- SKILL.md setiap query. '' = tidak ada requirement host. Kolom baru, bukan
-- tabel baru; DDL SPEC §11.2 diperbarui pada baris yang sama.

ALTER TABLE skills ADD COLUMN requires_host TEXT NOT NULL DEFAULT '';
