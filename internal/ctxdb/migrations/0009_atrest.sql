-- ADR-124: optional field-level at-rest encryption. Additive columns keep the
-- existing CHECK constraints valid (the legacy column holds a placeholder
-- while *_enc carries the envelope). Plaintext databases ignore these columns.
ALTER TABLE events ADD COLUMN content_enc BLOB;
ALTER TABLE events ADD COLUMN payload_enc BLOB;
ALTER TABLE summaries ADD COLUMN content_enc BLOB;
ALTER TABLE memory ADD COLUMN content_enc BLOB;
ALTER TABLE memory ADD COLUMN content_len INTEGER;
ALTER TABLE reasoning_carriers ADD COLUMN payload_enc BLOB;
ALTER TABLE gateway_inbound ADD COLUMN payload_enc BLOB;
