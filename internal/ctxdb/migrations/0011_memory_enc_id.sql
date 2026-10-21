-- ADR-124: per-row identity for sealed memory values; no UPDATE of memory.
ALTER TABLE memory ADD COLUMN enc_id TEXT NOT NULL DEFAULT '';
