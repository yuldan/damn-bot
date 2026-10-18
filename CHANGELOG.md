# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/id/1.1.0/). Bagian `Unreleased` wajib berubah bila PR menyentuh `.go` non-test di `cmd/` atau `internal/` (ENGINEERING E-81, job `pr-meta`).

## Unreleased

### Added
- `budget`: estimator token COST-09 (rasio per script + kalibrasi EMA 0,5–1,5 dari ledger, `TestTokenEstimatorCalibration` en/id/ru/ja), perhitungan biaya (provider/tabel harga/unknown), entri ledger; `ctxdb/ledgerstore`: insert `usage_ledger`, sampel kalibrasi, total sesi (ADR-101).
- `provider`: decoder delta stream openai-chat tanpa reflect dan tanpa alokasi per chunk (content, reasoning, tool_calls, usage, finish; E-36, ADR-102).
- Atribusi: sidecar `ORIGIN.md` (sumber, commit, lisensi, SHA-256) untuk setiap direktori file turunan pihak ketiga, diverifikasi `TestAttributionHeaders` (ADR-018 (b), ADR-099).
- `netx/retry`: satu kebijakan retry P6 (kelas typed, backoff 1 s/4 s ±20 %, `Retry-After` ≤ 30 s, maks 3 attempt) + circuit breaker in-process (ADR-100).
- `netx`: parser SSE WHATWG tanpa alokasi per event (E-35, ADR-100).
- `netx`: satu-satunya pembuat klien HTTP — transport ber-pool bersama, dial non-loopback ditolak di binary test (E-69) dan mode offline, egress tool hanya ke IP publik (anti SSRF), egress `background` dapat dimatikan, watchdog stream TTFB/idle (GAP-REL-07), error fase koneksi vs putus di tengah request (EC-PROV-030) (ADR-100).
- `schema`: tokenizer JSON ketat tanpa reflect (RFC 8259, UTF-8 valid, tanpa surrogate tunggal, kunci duplikat ditolak, nesting ≤ 64) dengan error ber-offset untuk model (ADR-095).
- `schema`: compiler `Compile(raw, ModeInternal|ModeMCP)` untuk subset keyword SPEC §4.4; mode internal menolak keyword di luar subset (`CompileError`, `ErrSchema`), mode MCP mengabaikannya dan melaporkan `Schema.Partial()` (ADR-095).
- `schema`: `Schema.Validate` — validasi argumen tool satu lintasan tanpa reflect (0 alloc/op untuk 1 KiB, E-37), semantik draft 2020-12 (`enum`/`const` dengan kesamaan JSON), error pertama deterministik ber-JSON Pointer; diuji JSON-Schema-Test-Suite dan `FuzzToolArgs` (ADR-095).
- `boundary`: satu-satunya peluncur goroutine dan `recover` (E-24, E-26): `Go` (Task milik pemanggil, `Wait`/`Done`), `Run` (panggilan sinkron terjaga), `Group` (errgroup + `SetLimit`); panic menjadi `*PanicError` (`ErrPanic`) dan dicatat ERROR tanpa nilai panic (P29).
- `redact`: redaksi secret whole-blob SPEC §14.3 tanpa `regexp` (biaya linear) untuk pola token, header Authorization, userinfo URL (user boleh kosong, password sampai `@` terakhir sebelum whitespace, bentuk JSON `:\/\/`; R-02), blok PEM, dan nilai secret yang dikenal (`SecretsFromEnv`, `Options.Secrets` tanpa panjang minimum); sentinel `[REDACTED:<tipe>]` + `ContainsSentinel` (ADR-096).
- `redact`: penyaring stream `Redactor.NewStream` (`Push`/`Flush`, 0 alloc/Push, tanpa goroutine) dengan output identik whole-blob; menahan hanya sufiks yang bisa menjadi awal secret, batas tahanan 4 KiB fail-closed (ADR-096).
- `netx`: gerbang DLP egress tunggal untuk request `tool` — URL/Host/header/trailer/body, lapisan percent/form-decode ≤ 3, secret case-insensitive dalam bentuk raw/hex/escape URL/JSON/base64, batas kerja fail-closed (`dlp_limit`), ≤ 50 µs per 8 KiB (GAP-SEC-16); log DEBUG metadata tanpa body/query/header, host/path ter-redaksi, request yang diblok tidak pernah menampilkan URL-nya; `Purpose` nol ditolak; `SetSecrets` (ADR-071, ADR-100).
- `netx` (SSRF): IPv4 tertanam di NAT64/IPv4-compatible/6to4 dan rentang special-purpose IANA tidak lagi dianggap publik (ADR-100).
- `sandbox`: verdict path tool kode SPEC §14.2 (`Check`: `inside`/`outside_root`, `ErrDeniedPath`) atas path ter-resolve symlink; `deny_paths` dicocokkan leksikal dan ter-resolve, juga dalam bentuk kanonik prefix pola (direktori deny/Home bersymlink, S15-1), case-insensitive di semua OS (S15-3); junction Windows di-resolve seperti symlink (S15-2); normalisasi path Windows/MSYS/long-path (ADR-097).

### Fixed
- `ctxdb` (Windows): lock file dilepas eksplisit dengan `UnlockFileEx` sebelum handle ditutup (ADR-092 NEW3-01).
- `ctxdb`: `MaintenancePending` menutup `os.Root` di semua jalur (sebelumnya 1 handle bocor per panggilan; di Windows data dir tak bisa dihapus) (ADR-094).
- `ctxdb` (Windows): fsync direktori tidak lagi gagal "Access is denied" (no-op di Windows, ADR-094).

### Removed
- `ctxdb.BackupPath`: fungsi tak terjangkau (gate `deadcode`, E-53).
