# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/id/1.1.0/). Bagian `Unreleased` wajib berubah bila PR menyentuh `.go` non-test di `cmd/` atau `internal/` (ENGINEERING E-81, job `pr-meta`).

## Unreleased

### Fixed
- `ctxdb` (Windows): lock file dilepas eksplisit dengan `UnlockFileEx` sebelum handle ditutup (ADR-092 NEW3-01).

### Removed
- `ctxdb.BackupPath`: fungsi tak terjangkau (gate `deadcode`, E-53).
