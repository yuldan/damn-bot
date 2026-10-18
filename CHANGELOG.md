# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/id/1.1.0/). Bagian `Unreleased` wajib berubah bila PR menyentuh `.go` non-test di `cmd/` atau `internal/` (ENGINEERING E-81, job `pr-meta`).

## Unreleased

### Added
- `codetools`: tool file eager CODE-TOOLS §2–§5 — `read_file` (halaman 16.000/4.000 char, tail mundur, indeks baris + async > 8 MiB, deteksi encoding/BOM/UTF-16, byte invalid `\xNN`, deteksi biner/magic, outline `go/ast` + heuristik), `grep` (walker paralel berurut deterministik, engine gitignore sendiri, literal fast-path + RE2, mode matches/files/count, context, jendela 500 char), `glob` (doublestar), `edit_file` (exact-only, multi-edit atomik, kandidat `no_match` informatif, `stale_read` + rentang berubah, line-range termasuk > 64 MiB streaming), `write_file` (temp + fsync file + rename + fsync dir, read-back sha256, preservasi mode/owner/xattr, hardlink in-place) (ADR-103).
- `sandbox`: `Denied` dan `OpenResolved` untuk walker grep/glob (satu `openat2` `RESOLVE_NO_SYMLINKS` + cek identitas deny pasca-open) (ADR-103).
- Atribusi: sidecar `ORIGIN.md` (sumber, commit, lisensi, SHA-256) untuk setiap direktori file turunan pihak ketiga, diverifikasi `TestAttributionHeaders` (ADR-018 (b), ADR-099).
- `netx/retry`: satu kebijakan retry P6 (kelas typed, backoff 1 s/4 s ±20 %, `Retry-After` ≤ 30 s, maks 3 attempt) + circuit breaker in-process (ADR-100).
- `netx`: parser SSE WHATWG tanpa alokasi per event (E-35, ADR-100).
- `netx`: satu-satunya pembuat klien HTTP — transport ber-pool bersama, dial non-loopback ditolak di binary test (E-69) dan mode offline, egress tool hanya ke IP publik (anti SSRF), egress `background` dapat dimatikan, watchdog stream TTFB/idle (GAP-REL-07), error fase koneksi vs putus di tengah request (EC-PROV-030) (ADR-100).
- `schema`: tokenizer JSON ketat tanpa reflect (RFC 8259, UTF-8 valid, tanpa surrogate tunggal, kunci duplikat ditolak, nesting ≤ 64) dengan error ber-offset untuk model (ADR-095).
- `schema`: compiler `Compile(raw, ModeInternal|ModeMCP)` untuk subset keyword SPEC §4.4; mode internal menolak keyword di luar subset (`CompileError`, `ErrSchema`), mode MCP mengabaikannya dan melaporkan `Schema.Partial()` (ADR-095).
- `schema`: `Schema.Validate` — validasi argumen tool satu lintasan tanpa reflect (0 alloc/op untuk 1 KiB, E-37), semantik draft 2020-12 (`enum`/`const` dengan kesamaan JSON), error pertama deterministik ber-JSON Pointer; diuji JSON-Schema-Test-Suite dan `FuzzToolArgs` (ADR-095).
- `boundary`: satu-satunya peluncur goroutine dan `recover` (E-24, E-26): `Go` (Task milik pemanggil, `Wait`/`Done`), `Run` (panggilan sinkron terjaga), `Group` (errgroup + `SetLimit`); panic menjadi `*PanicError` (`ErrPanic`) dan dicatat ERROR tanpa nilai panic (P29).
- `redact`: redaksi secret whole-blob SPEC §14.3 tanpa `regexp` (biaya linear) untuk pola token, header Authorization, userinfo URL (user boleh kosong, password sampai `@` terakhir sebelum whitespace, bentuk JSON `:\/\/`; R-02), blok PEM, dan nilai secret yang dikenal (`SecretsFromEnv`, `Options.Secrets` tanpa panjang minimum); sentinel `[REDACTED:<tipe>]` + `ContainsSentinel` (ADR-096).
- `sandbox`: verdict path tool kode SPEC §14.2 (`Check`: `inside`/`outside_root`, `ErrDeniedPath`) atas path ter-resolve symlink; `deny_paths` dicocokkan leksikal dan ter-resolve, juga dalam bentuk kanonik prefix pola (direktori deny/Home bersymlink, S15-1), case-insensitive di semua OS (S15-3); junction Windows di-resolve seperti symlink (S15-2); normalisasi path Windows/MSYS/long-path (ADR-097).
- `sandbox`: `OpenFile`/`OpenApproved` anti-TOCTOU tanpa mengikuti symlink yang muncul setelah `Check`: openat2 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` (Linux ≥ 5.6), walk `openat(O_NOFOLLOW)` + verifikasi `/proc/self/fd` (Linux lama) atau device+inode (macOS/BSD), `os.Root` + `GetFinalPathNameByHandle` (Windows); escape/race → `ErrOutsideWorkspace`; setiap handle dicek ulang terhadap `deny_paths` lewat path nyata handle dan identitas objek deny literal (`ErrDeniedPath`, S16-2); junction di dalam root bisa dibuka (S16-1) (ADR-097).

### Fixed
- `ctxdb` (Windows): lock file dilepas eksplisit dengan `UnlockFileEx` sebelum handle ditutup (ADR-092 NEW3-01).
- `ctxdb`: `MaintenancePending` menutup `os.Root` di semua jalur (sebelumnya 1 handle bocor per panggilan; di Windows data dir tak bisa dihapus) (ADR-094).
- `ctxdb` (Windows): fsync direktori tidak lagi gagal "Access is denied" (no-op di Windows, ADR-094).

### Removed
- `ctxdb.BackupPath`: fungsi tak terjangkau (gate `deadcode`, E-53).
