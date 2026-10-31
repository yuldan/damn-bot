-- 0016_usage_cache_creation.sql -- token cache-write Anthropic per origin (GAP-COST-03,
-- ADR-266). Murni tambahan: kolom baru dengan default 0 pada tabel append-only;
-- DDL SPEC §11.2 diperbarui di blok yang sama.

ALTER TABLE usage_ledger ADD COLUMN cache_creation_input_tokens INTEGER NOT NULL DEFAULT 0;  -- cache_control write (5m/1h)
