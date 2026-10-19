# Changelog

Format: [Keep a Changelog](https://keepachangelog.com/id/1.1.0/). Bagian `Unreleased` wajib berubah bila PR menyentuh `.go` non-test di `cmd/` atau `internal/` (ENGINEERING E-81, job `pr-meta`).

## Unreleased

### Fixed
- `provider`/`netx`: endpoint jahat tidak lagi bisa memicu CPU/memori tak sebanding — nama tool di-`append` dan dibatasi 256 B, ≤ 256 entri `tool_calls` per chunk dan setiap fragmen dihitung ke cap respons, reasoning dihitung termasuk salinan replay ter-quote; egress Inference tidak pernah mengikuti redirect (3xx = `bad_request`), purpose lain membuang header kredensial lintas host dan menolak https → http; fallback dilaporkan sebelum dan sesudah setiap percobaan termasuk yang gagal (REVIEW-F1-SEC3 PROV-M1..M5, ADR-100, ADR-102).
- `provider`: decoder delta tidak lagi merekursi tanpa batas saat melewati nilai JSON bersarang — `skip` iteratif dan nesting dibatasi 10.000 seperti encoding/json; frame SSE 4 MiB berisi `[` kini `errJSON`, bukan stack overflow fatal (REVIEW-F1-SEC3 PROV-B1, ADR-102).
- `budget`: biaya ledger tidak lagi bisa negatif atau mendekati 0 — token > 10¹², biaya provider > 10⁶ USD (termasuk +Inf), harga negatif, dan overflow tokens × harga ditolak `ErrInvalidUsage` (aritmetika tercek); test append-only `usage_ledger` kini non-vakum untuk UPDATE dan DELETE (REVIEW-F1-SEC3 LED-1..3, ADR-101).

### Changed
- `redact`: prefilter pasangan byte di `scan` — posisi yang dua byte pertamanya tidak bisa memulai pola mana pun dilewati tanpa `matchAt` (output identik, ≈ 3× lebih cepat pada prosa 8 KiB; dasar GAP-SEC-16 DLP ≤ 50 µs, REVIEW-F1-SEC1 NETX-16; ADR-096).

### Added
- `exec/classify`: tipe API (`Verdict`, `Class`, `Flag`, `Env`, `PathResolver`), label operand path (`deny_paths` leksikal + ter-resolve, `outside_read`, tulis luar root = `destructive`, glob, blob/`/dev` dikecualikan) dan kebijakan approval shell `Decide` (SPEC §14.1, ADR-079/088) + `OutputSource`; budget `exec` 3.400 / `exec/classify` 1.600 (ADR-106).
- `exec/classify`: `Classify` untuk bash/POSIX lewat AST `mvdan.cc/sh/v3/syntax` — setiap simple command (pipeline, compound, substitusi `$(…)`/`<(…)`, heredoc) dinilai, kata dievaluasi statis (hanya `~`/`$HOME`), redirect = operand, assignment `PATH`/`LD_*`… = `unknown`, referensi var secret = `unknown` + `references_secret`, fork bomb = `destructive` (ADR-106).
- `exec/classify`: tabel label (`destructive`, `irreversible`, `monetary`, `delegated_agent_bypass`, flag `network`), aturan opsi (`git push --force`/`reset --hard`/`clean -f`, `npm -g`, `kill -1`, …), git/find/pip/dd, wrapper (`sudo`/`doas`/`pkexec` = `privileged`, `env`/`timeout`/`xargs`/`npx`…), interpreter (`bash -c` literal direkursi, kode inline bahasa lain/stdin = `unknown`), `eval`, label `non_mutating` (ADR-106).
- `provider`: klien openai-chat `OpenAI` — retry P6 + circuit breaker per (provider, credential), fallback ≤ 2 model (mulai dari primer, lewati rute hilang/window kecil, event `provider_fallback`), parameter yang ditolak (`error.param`) dilepas per rute, batas respons 16 MiB/128 tool call; `providertest`: server palsu OpenAI-compatible (skrip, replay SSE, FakeClock) (ADR-102).
- `netx/retry`: `Policy.Wait` untuk waktu tunggu yang diinjeksi test (ADR-102).
- `provider`: tipe request/response/`Error` typed (SPEC §4.1), encoder request openai-chat deterministik (reasoning dikirim balik byte-identik), rakitan stream (tool call terfragmen, finish dinormalisasi, `stream_broken`, `empty_response`, strip token kontrol) (ADR-102).
- `provider`: decoder delta stream openai-chat tanpa reflect dan tanpa alokasi per chunk (content, reasoning, tool_calls, usage, finish; E-36, ADR-102).
- `budget`: estimator token COST-09 (rasio per script + kalibrasi EMA 0,5–1,5 dari ledger, `TestTokenEstimatorCalibration` en/id/ru/ja), perhitungan biaya (provider/tabel harga/unknown), entri ledger; `ctxdb/ledgerstore`: insert `usage_ledger`, sampel kalibrasi, total sesi (ADR-101).
- `provider`: decoder delta stream openai-chat tanpa reflect dan tanpa alokasi per chunk (content, reasoning, tool_calls, usage, finish; E-36, ADR-102).
- `codetools`: tool file eager CODE-TOOLS §2–§5 — `read_file` (halaman 16.000/4.000 char, tail mundur, indeks baris + async > 8 MiB, deteksi encoding/BOM/UTF-16, byte invalid `\xNN`, deteksi biner/magic, outline `go/ast` + heuristik, cache baca LRU + rilis idle), `grep` (walker paralel berurut deterministik, engine gitignore sendiri, literal fast-path + RE2, mode matches/files/count, context, jendela 500 char), `glob` (doublestar, sort path/mtime), `write_file` (temp + fsync file + rename + fsync dir, read-back sha256, overwrite dengan tag + `stale_read`, preservasi mode/owner/xattr, kunci per identitas file, pembersihan temp yatim) (ADR-103).
- `codetools`: tool file eager CODE-TOOLS §2–§5 — `read_file` (halaman 16.000/4.000 char, offset negatif dari akhir, byte invalid `\xNN`) (ADR-103).
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
- `redact`: penyaring stream `Redactor.NewStream` (`Push`/`Flush`, 0 alloc/Push, tanpa goroutine) dengan output identik whole-blob; menahan hanya sufiks yang bisa menjadi awal secret, batas tahanan 4 KiB fail-closed (ADR-096).
- `netx`: gerbang DLP egress tunggal untuk request `tool` — URL/Host/header/trailer/body, lapisan percent/form-decode ≤ 3, secret case-insensitive dalam bentuk raw/hex/escape URL/JSON/base64, batas kerja fail-closed (`dlp_limit`), ≤ 50 µs per 8 KiB (GAP-SEC-16); log DEBUG metadata tanpa body/query/header, host/path ter-redaksi, request yang diblok tidak pernah menampilkan URL-nya; `Purpose` nol ditolak; `SetSecrets` (ADR-071, ADR-100).
- `netx` (SSRF): IPv4 tertanam di NAT64/IPv4-compatible/6to4 dan rentang special-purpose IANA tidak lagi dianggap publik (ADR-100).
- `sandbox`: verdict path tool kode SPEC §14.2 (`Check`: `inside`/`outside_root`, `ErrDeniedPath`) atas path ter-resolve symlink; `deny_paths` dicocokkan leksikal dan ter-resolve, juga dalam bentuk kanonik prefix pola (direktori deny/Home bersymlink, S15-1), case-insensitive di semua OS (S15-3); junction Windows di-resolve seperti symlink (S15-2); normalisasi path Windows/MSYS/long-path (ADR-097).
- `sandbox`: `OpenFile`/`OpenApproved` anti-TOCTOU tanpa mengikuti symlink yang muncul setelah `Check`: openat2 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` (Linux ≥ 5.6), walk `openat(O_NOFOLLOW)` + verifikasi `/proc/self/fd` (Linux lama) atau device+inode (macOS/BSD), `os.Root` + `GetFinalPathNameByHandle` (Windows); escape/race → `ErrOutsideWorkspace`; setiap handle dicek ulang terhadap `deny_paths` lewat path nyata handle dan identitas objek deny literal (`ErrDeniedPath`, S16-2); junction di dalam root bisa dibuka (S16-1) (ADR-097).

### Fixed
- `sandbox`: file reguler ber-hard-link (jumlah link > 1) di dalam root tidak lagi dibuka otomatis: verdict baru `HardLinked` (`hardlinked_file`) dari `Check`/`OpenFile` butuh approval seperti `outside_root`; `O_TRUNC` diterapkan hanya setelah handle lolos cek deny dan hard link (ADR-097 butir 12, S16-7).
- `ctxdb` (Windows): lock file dilepas eksplisit dengan `UnlockFileEx` sebelum handle ditutup (ADR-092 NEW3-01).
- `ctxdb`: `MaintenancePending` menutup `os.Root` di semua jalur (sebelumnya 1 handle bocor per panggilan; di Windows data dir tak bisa dihapus) (ADR-094).
- `ctxdb` (Windows): fsync direktori tidak lagi gagal "Access is denied" (no-op di Windows, ADR-094).

### Removed
- `ctxdb.BackupPath`: fungsi tak terjangkau (gate `deadcode`, E-53).
