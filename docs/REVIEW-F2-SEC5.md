# REVIEW-F2-SEC5 — Review keamanan wiring user-facing (#170, #171, #173, #174)

**Status: lengkap — 2 BLOCKER (SEC5-1 undo sandbox #170; SEC5-8 binary #171), 2 HIGH, 3 MEDIUM, 4 MINOR. Verdict: #170 `changes required`, #171 `changes required`, #173 `approve`, #174 `approve` dengan 1 MINOR.** Review independen atas `damnbot undo`, hook jurnal codetools, `damnbot review`, budget guard di `run`, dan wiring registry modul. Reviewer tidak menulis kode yang direview; kode produk tidak diubah. Tanggal: 2026-10-21.

Head yang direview: `#170` `d0a0683` (`feat/f2-wire-journal`), `#171` `d6c7ffb` (`feat/f2-wire-memory`), `#173` `5bc2176` (`feat/f2-wire-budget`), `#174` `d19481a` (`feat/f2-wire-modules`). Basis stack `d901fb1`; `origin/main` saat review `67c6a80` (#151). Pemeriksaan di Linux amd64; Windows/macOS hanya statis. Probe `TestZZ*` dijalankan di worktree terpisah dan dihapus sebelum commit dokumen ini. Runtime: `GOMAXPROCS=1`, `GOFLAGS=-p=1 -tags=goolm`, `nice -n 19`. Tanpa `-tags=goolm`, `cmd/damnbot` sengaja gagal compile (`goolm_guard.go`, E-39).

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #170 `d0a0683` | journal hook sebelum rename; `damnbot undo` + sandbox sesi + lease | **changes required — 1 BLOCKER (SEC5-1)**, 2 HIGH, 2 MEDIUM, 2 MINOR |
| #171 `d6c7ffb` | adaptor memory, `damnbot review`, scope/quarantine | **changes required — 1 BLOCKER (SEC5-8 binary)**, 1 MEDIUM, 1 MINOR |
| #173 `5bc2176` | `budget.Guard` di `run`, denial/warning, event | **approve** — tidak ada temuan |
| #174 `d19481a` | `modules.Freeze()` first, katalog deferred, zero-host gate, shutdown | **approve dengan MINOR** (SEC5-11); stack tetap tertahan #170/#171 |

Ringkasan: **2 BLOCKER, 2 HIGH, 3 MEDIUM, 4 MINOR.** BLOCKER pertama adalah sandbox undo yang dibangun dari `sessions.cwd` mutable + config saat ini (bukan sandbox asli sesi); probe menunjukkan `cwd="/"` membuat `/etc/passwd` menjadi `Inside`. BLOCKER kedua adalah binary `damnbot` 20.471.419 byte yang ikut ter-commit di #171.

## Temuan

`confirmed` = dijalankan/dibuktikan dengan probe; `static` = dibuktikan dari kode/dokumen.

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| SEC5-1 | #170 | **BLOCKER** | confirmed | `undoSandbox` membangun root dari `sessions.cwd` (nilai DB) + `cfg.Approval.AddDirs` + `cfg.DenyPaths` **saat undo**, bukan sandbox sesi saat aksi dicatat. Session hanya menyimpan `cwd` (bukan add_dirs/deny_paths), dan `cwd` tidak divalidasi. Session row yang diubah menjadi `/` membuat path luar (`/etc/passwd`) menjadi `Inside`, sehingga undo dapat memulihkan di luar root asli. | `undo.go:149-163`; `ctxdb/append.go:17-23`; probe `TestZZUndoSandboxCwdTamper`: `cwd=/`, `/etc/passwd` → `verdict=inside`, `resolved="/etc/passwd"` |
| SEC5-2 | #170 | HIGH | confirmed | `a<id>` mengambil aksi langsung lewat `store.Get` dan mengembalikan session aksi, mengabaikan `--session`/scope direktori. Aksi dari sesi lain dapat di-undo secara eksplisit lintas direktori; `--session=B` tetap memakai session A. | `undo.go:112-120`; probe `TestZZUndoActionCrossSession`: `a1 --session=B` → session `"A"` |
| SEC5-3 | #170 | HIGH | confirmed | `--dry-run` memakai `os.ReadFile(a.Path)` langsung, bukan sandbox/`FileReader`; path journal dapat berada di luar root dan symlink diikuti. Dry-run memang tidak menulis target, tetapi melanggar batas baca sandbox dan dapat membocorkan isi di luar root lewat diff. | `undo.go:254-264`; probe `TestZZUndoDryRunReadsOutsideSandbox`: action path di luar root dibaca dan menampilkan diff `-new/+old` |
| SEC5-4 | #170 | MEDIUM | static | Output `undo ls` dan `--dry-run` mencetak `a.Path`/`MoveTo` dan diff mentah tanpa sanitasi ANSI/bidi/newline. Nama file/diff dari workspace dapat mengendalikan terminal atau memalsukan baris output. | `undo.go:220-235`; `lineDiff` `:310-323`; tidak ada `oneLine`/strip Cf/Cc |
| SEC5-5 | #170 | MEDIUM | confirmed | Kegagalan awal `startRunModel` setelah `CreateSession` (mis. model tidak valid) menutup DB tanpa `ReleaseLease`; baris lease sesi tertinggal sampai TTL, sehingga `damnbot undo` bisa mendapat `session_busy`. Jalur sukses `runModel.close` melepas lease, jalur `fail` tidak. | `run.go:292-301` (`fail`), `:253` (normal release); probe `TestZZRunFailureLease`: exit 2, `session_leases=1` |
| SEC5-6 | #171 | MEDIUM | confirmed | Sanitizer `oneLine` membuang `unicode.IsControl` tetapi **tidak** membuang format/bidi (`Cf`, U+202E/U+2066/U+2069); `writeReview` juga mencetak `Scope`, `Category`, `Origin`, severity tanpa `oneLine`. Scope `project:` dapat memuat newline/control; output dapat memalsukan baris/tab atau mengubah tampilan keputusan manusia. | `review.go:158-168`, `:170-184`; probe `TestZZReviewBidiAndRawScope`: `oneLine` mempertahankan U+202E/U+2066/U+2069; output memuat `user\u202e\nFAKE\tROW` |
| SEC5-7 | #171 | MINOR | static | `review` dan `--bulk` memakai `memory.MaxRecall` = 50 sebagai batas antrean tanpa pagination. `--bulk` hanya memutuskan 50 entri pertama lalu exit 0; ID pending di luar 50 ditolak sebagai "not pending". Dengan beberapa `--scope`, antrean dapat melebihi 50. | `review.go:107`; `memory/types.go:15`; `memory/service.go:182-185` |
| SEC5-8 | #171 | **BLOCKER** | static | Binary hasil build `damnbot` (20.471.419 byte, mode 100755) ikut di-commit di root. `.gitignore` hanya mengabaikan `/.bin/`, bukan `/damnbot`; blob ini masuk diff PR #171 (dan terbawa #174). Bukan artefak sumber, tidak dapat direview, dan memperbesar repo/PR secara masif. | `git ls-tree -l pr171 damnbot`: blob `6a82575…`, 20.471.419 byte; `git diff --numstat d0a0683..d6c7ffb` menampilkan `damnbot` |
| SEC5-9 | #170 | MINOR | confirmed | Satu aksi file tercatat sekali (`actions=1`), tetapi `tool_dispatch` bertambah dari tiga lapisan: loop, dispatcher tools, dan `journalstore.Record`. Satu turn baca+edit menghasilkan **5** event `tool_dispatch` (bukan 3/4), sehingga transkrip/consumer dapat melihat dispatch ganda. | `journalstore.go:158`; `tools/dispatch.go:342`; `loop/dispatch.go:195`; probe `TestZZJournalDispatchEvents`: `actions=1`, `tool_dispatch=5` |
| SEC5-10 | #170 | MINOR | static | Hook jurnal menyalin seluruh before/after image ke memori (`make([]byte, fi.Size())`) dan menulis blob/DB sebelum rename; tidak ada batas ukuran per file. Benchmark CT-09 (`Edit1MB`) memakai `Opts{}` tanpa `Journal`, jadi gate perf tidak mengukur overhead snapshot/record produksi. | `commit.go:75-79`; `journal_wire.go:71-81`; `internal/codetools/bench_test.go:158` (`Opts{}`); benchmark no-op hook tidak mewakili blob DB |
| SEC5-11 | #174 | MINOR | static | `startRunModel` membuat `mods.runtime` lalu dapat gagal di `tools.NewRegistry`/provider. Closure `fail` hanya menutup engine/sessions/runner/DB; `modules.Runtime.Shutdown` hanya dipanggil di `runModel.close` (jalur sukses). Runtime modul dapat tertinggal pada jalur start gagal bila host/mods aktif. | `run.go:292-301`, `:319-327`; `run.go:233-237` (hanya close normal) |

### SEC5-1 (BLOCKER) — undo memakai `sessions.cwd` mutable + config sekarang

`undoSandbox` membaca satu kolom:

```go
err := db.Write(ctx, func(tx *ctxdb.Tx) error {
    return tx.Get("SELECT cwd FROM sessions WHERE id = ?", []any{sid}, &cwd)
})
...
sandbox.New(&sandbox.Config{
    Roots: append([]string{cwd}, cfg.Approval.AddDirs...),
    DenyPaths: cfg.DenyPaths(configDir),
})
```

Session table hanya menyimpan `Cwd`; tidak ada `add_dirs`/`deny_paths` asli (`ctxdb.Session` = ID/Parent/Fork/Origin/Cwd/Model/ConfigSHA). Dengan session row yang diubah menjadi `Cwd="/"`, root menjadi `/`; probe `TestZZUndoSandboxCwdTamper` mendapat `/etc/passwd` verdict `Inside`. Aksi yang tercatat di luar root asli lalu dapat dipulihkan dengan `undo`, dan `--force` tidak perlu karena sandbox sudah melebar. Ini persis threat “session record … widen the sandbox for undo”.

**Rekomendasi:** simpan snapshot sandbox sesi (cwd + add_dirs + deny_paths, atau `ConfigSHA` yang dapat direkonstruksi) saat `CreateSession`, validasi `cwd` terhadap root/config asli, tolak `cwd` yang bukan root yang dikenal, dan gunakan snapshot itu di `undoSandbox`; jangan fallback ke config saat ini untuk path yang tidak ada di snapshot. Tambah test session row `cwd=/` dan `add_dirs=/` harus ditolak.

### SEC5-2 (HIGH) — `a<id>` lintas session dan `--session` diabaikan

`undoTargets` untuk `a<id>`:

```go
a, err := store.Get(ctx, o.action)
return []journal.Action{a}, a.SessionID, nil
```

`o.session` tidak dipakai. `--session=B` dengan `a1` milik session A tetap memakai A; default `a<id>` dapat meng-undo direktori lain bila ID diketahui. `--session` juga tidak membatasi scope. **Rekomendasi:** bila `--session` diisi, tolak `a<id>` yang session-nya berbeda; untuk `a<id>` tanpa `--session`, minta konfirmasi eksplisit atau tampilkan session/cwd asal; jangan gunakan ID global sebagai otorisasi lintas direktori.

### SEC5-3 (HIGH) — dry-run membaca di luar sandbox

`dryRun` memakai `os.ReadFile(a.Path)`, bukan `journal.Undoer`/sandbox. Probe menulis file di luar cwd lalu `--dry-run` menampilkan diff normal. Dry-run memang tidak menulis, tetapi symlink/aksi luar root dapat membaca file yang seharusnya di luar batas dan menampilkannya ke terminal. **Rekomendasi:** gunakan `FileReader`/sandbox sesi untuk preview; bila path di luar root atau symlink, tampilkan verdict “would refuse/outside root” tanpa isi; jangan membaca dengan `os.ReadFile`.

### SEC5-4 (MEDIUM) — output undo tidak disanitasi

`previewActions` mencetak `a.Path`, `a.MoveTo`, dan diff `lineDiff` mentah. File name dapat mengandung newline/ESC/bidi; diff dapat mengandung escape. `undo ls` saja sudah cukup untuk memalsukan baris terminal. **Rekomendasi:** satu `oneLine`/`lineRune` yang membuang Cc/Cf (termasuk bidi) untuk path, verdict, dan setiap baris diff; pertahankan newline diff hanya setelah sanitasi.

### SEC5-5 (MEDIUM) — lease bocor saat start gagal

`CreateSession` mengambil lease; `startRunModel` kemudian dapat gagal (provider endpoint, registry, journalstore). `fail` hanya `closeDB`, tidak `ReleaseLease`. Probe model invalid: exit 2, `session_leases=1`. Lease baru hilang setelah TTL; `undo` dapat `session_busy`/exit 4. **Rekomendasi:** `fail` harus `ReleaseLease(ctx,sid)` sebelum `CloseContext` (best-effort + join error), atau gunakan satu cleanup yang selalu melepas lease.

### SEC5-6 (MEDIUM) — review output masih dapat dikendalikan bidi/scope

`oneLine` hanya membuang `unicode.IsControl`; bidi/format (`Cf`) tetap. `writeReview` mencetak `r.Scope` dan metadata lain tanpa `oneLine`. Probe `writeReview` menghasilkan `user\u202e\nFAKE\tROW` di output. Quarantined content/scope berasal dari proposal; reviewer manusia dapat melihat baris palsu atau urutan terbalik. **Rekomendasi:** pakai `lineRune`/strip `unicode.Cf` untuk **semua** kolom, termasuk scope, category, origin, severity; pertahankan test ANSI + bidi + newline.

### SEC5-7 (MINOR) — antrean review dibatasi 50 tanpa pagination

`reviewMain` memanggil `Pending/Rejected(..., memory.MaxRecall)` dan `--bulk` memutuskan hanya hasil itu. Dengan beberapa `--scope`, antrean dapat >50; entri sesudahnya tidak terlihat, `--bulk` exit 0 tanpa memproses semuanya, dan ID-nya ditolak “not pending”. **Rekomendasi:** batas review terpisah + pagination (`--limit/--after`) dan `--bulk` loop sampai kosong atau laporkan sisa.

### SEC5-8 (BLOCKER) — binary 20 MB ter-commit

`git ls-tree -l pr171 damnbot`:

```text
100755 blob 6a82575a5b40ed2fa1e04faef9be149316ef0153 20471419 damnbot
```

`.gitignore` hanya mengabaikan `/.bin/`, bukan `/damnbot`. Binary ini terbawa ke #174. **Rekomendasi:** hapus dari branch, tambahkan `/damnbot` ke `.gitignore` bila build lokal menghasilkan di root, dan rebase #174 setelahnya. Ini juga membuat PR #171 tidak lolos review sebagai perubahan sumber.

### SEC5-9 (MINOR) — event dispatch berlapis

Satu edit menghasilkan `actions=1` (baik), tetapi `tool_dispatch=5` untuk satu read + satu edit: loop `dispatch.go:195`, tools `dispatch.go:342`, dan journalstore `recordTx:158`. Tiga penulis event dengan `tool_call_id` yang sama dapat membingungkan transcript/consumer yang menganggap satu dispatch per call. **Rekomendasi:** tetapkan satu pemilik event transcript; gunakan event dispatcher yang sudah ada sebagai anchor `actions.event_id` atau dokumentasikan idempotensi consumer.

### SEC5-10 (MINOR) — gate performa tidak mengukur hook jurnal

`BenchmarkCodetools/Edit1MB` memakai `Opts{}`; hook `Journal` tidak diisi, sehingga snapshot before/after dan blob/DB record produksi tidak masuk CT-09. Benchmark no-op hook yang saya jalankan ≈28,99 ms vs 29,47 ms, tetapi itu hanya overhead callback, bukan `journalstore.Record`. Selain itu `snapshot` mengalokasikan seluruh file. **Rekomendasi:** tambah benchmark dengan recorder nyata/representatif dan batas ukuran image; dokumentasikan biaya snapshot pada file besar.

### SEC5-11 (MINOR) — runtime modul tidak di-Shutdown pada start gagal

`runModel.close` memanggil `m.modules.runtime.Shutdown` (jalur normal), tetapi `fail` tidak. Bila `mods` sudah dibuat (host + tool modul) lalu `tools.NewRegistry`/provider gagal, runtime dapat tertinggal. Saat ini `run` memanggil `newRunModules(modules.Default(), nil)` sehingga default tanpa host tidak membuat runtime; defect ini laten untuk wiring host berikutnya. **Rekomendasi:** pindahkan `mods` ke cleanup `fail` (atau satu `closeAll`) dan Shutdown sebelum sessions/runner.

## Diperiksa dan bersih

- **Journal ordering:** hook dipanggil setelah `tmp.Sync()` dan sebelum `renameReplace`/`copyInPlace` (`commit.go:75-83`); target masih byte-identik saat hook (`TestJournalHookSeesBothImages`).
- **Journal failure:** error hook membatalkan write, target tetap, temp dibuang; create gagal tidak meninggalkan file/temp (`TestJournalHookErrorAborts`; `entries=1`).
- **Action count:** satu edit = satu baris `actions`; tidak ada double action. (Event dispatch berlapis = SEC5-9.)
- **Undo normal:** byte/mode/BOM/CRLF kembali, `--dry-run` tidak menulis, conflict exit 1, `--force` memulihkan, `undo N` dan second undo no-op; `TestUndoRestoresEditByteIdentical`/`TestUndoConflictRefuses`, lease normal dilepas (`leases=0`).
- **Undo executor:** sandbox/verdict/undo belum diubah di stack ini; pemulihan tetap lewat `journal.Undoer` — tetapi sandbox yang dibangun SEC5-1 membatalkan jaminan root asli.
- **Exit codes:** parse error 2; busy/fenced/db_busy 4; refusal/conflict 1; sukses 0 (`undoExit`); test parse.
- **Budget:** cap 0 = tanpa cap, satu request, perilaku F1; denial daily/monthly/provider = `max_tokens` + detail, **0 request**; `budget_exhausted`/`budget_warning` events; unknown cost fail-closed; warning once per cap; loop tidak retry `ClassBudget` (`TestRunBudgetGuardDenies`, `TestStreamErrorBudgetClass`, `TestBudgetDenialStops`, `TestBudgetWarningsRecorded`; probe zero caps `completed`, `requests=1`).
- **Review scope/quarantine:** `Pending`/`Rejected` hanya scope yang diminta; ID luar scope/status ditolak; approve hanya `scanned`; rejected tidak recall-able; ANSI ESC di content dibuang; test `TestReviewApproveReject`, `TestReviewScopes`, `TestReviewSanitizesContent`. (Bidi/Scope = SEC5-6.)
- **Modules:** `modules.Freeze()` dievaluasi pertama di `main`; error registry exit 1 sebelum command (`TestStartRegistryError`); zero module/zero tool → exactly 8 F1 tools, `tool_search`/`tool_invoke` absent, katalog nil (`TestRunAdvertisesF1Tools`, `TestRunModulesWiring`); tool modul tanpa host ditolak; dengan `testHost` catalog/runtime/tool_search/tool_invoke berjalan; normal shutdown (`runModel.close:233`).
- **P1/f1-core:** tidak ada perubahan `internal/assemble`; `TestFixedOverheadGolden` lulus; `f1-core` byte-identik dan daftar 8 tool pada zero-module run.
- **Ukuran:** #170 **542**, #171 **443**, #173 **124**, #174 **93** baris Go non-test touched; semua ≤600 (E-84). Binary tidak dihitung sebagai Go tetapi BLOCKER SEC5-8.
- **LOC/core:** tidak ada baris `loc.budget` yang berubah; Total core tetap 36.420 sesuai main; `TestBudgetTableSum` lulus. `cmd/damnbot` bukan baris core `loc.budget`; 1.202 baris wiring tidak masuk baris budget paket.
- **Gate uji:** `go test -tags goolm ./cmd/damnbot ./internal/codetools ./internal/tools ./internal/memory ./internal/loop ./internal/modules ./internal/archtest` lulus; `-race` paket tersebut lulus; `go vet` bersih; `TestImportsAllowed` lulus. Tanpa `-tags goolm`, `cmd/damnbot` sengaja gagal compile (E-39; bukan regresi PR).
- **Benchmark:** `BenchmarkCodetools/Edit1MB` dan no-op journal ≈29 ms/op pada host ini; gate CT-09 tidak memasang Journal (SEC5-10).
- **Rebase/merge:** PR #170/#171/#173/#174 hanya menyentuh file tujuannya; stack berbasis `d901fb1`, sementara `origin/main` `67c6a80`; `git merge-tree origin/main pr174` konflik hanya `CHANGELOG.md` dan `docs/DECISIONS.md` (tidak ada konflik kode). Rebase ke main saat ini diperlukan sebelum merge.

## Residual risk

- **Undo sandbox (SEC5-1)** adalah risiko utama; sampai snapshot sandbox sesi disimpan/diverifikasi, undo tidak dapat menjamin root asli.
- **a<id>/scope (SEC5-2)** dan **dry-run (SEC5-3)** memperbesar permukaan bila DB/journal dapat disentuh user/agent; `--force` tidak memperbaiki root.
- **Output sanitization (SEC5-4/6)** penting untuk terminal manusia; bidi/newline pada scope/path/diff belum ditutup.
- **Lease (SEC5-5)** membuat undo gagal sementara sampai TTL setelah start failure.
- **Binary (SEC5-8)** harus hilang sebelum merge; #174 mewarisinya.
- **Journal event layers (SEC5-9)**, **perf/snapshot (SEC5-10)**, dan **module fail cleanup (SEC5-11)** bersifat latent/operational.
- **Host modul produksi** belum ada; `run` selalu memanggil `newRunModules(..., nil)`, jadi modul dengan tool saat ini menolak start. Hanya `testHost` yang membuktikan jalur host; implementasi Emit/I18n/Spawn/LLM/Storage dan modul nyata di luar review.
- **Memory provider/scanner** dan `memstore` di luar perubahan; `review` melihat hasil store, bukan memverifikasi ulang scan severity.
- **Sandbox `openat2`/root**, `exec`, dan provider/model live tidak diuji ulang di sini; Linux-only.
- **`cmd/damnbot` tidak masuk baris core `loc.budget`**; 1.202 baris wiring hanya dibatasi E-84 per PR, bukan budget paket. Bila kebijakan menghendaki baris `cmd`, itu keputusan ADR terpisah.

## Tidak dicakup

- Implementasi host modul produksi, descriptor modul nyata, dan MCP/F5.
- Internal memory provider, threat scanner, `memstore`/`skillstore`, dan FTS5.
- Implementasi `internal/sandbox` (openat2/root/symlink/TOCTOU) — hanya pemakaian sandbox di undo yang direview.
- Live provider/model, TUI, gateway, Windows/macOS, fuzz, dan benchmark CT-10 penuh.
- Perbaikan PR setelah head yang direview; verdict berlaku untuk head di atas.

---

## Round 2 — 2026-10-21

**Verdict keseluruhan: REQUEST CHANGES.** 9 dari 11 temuan round-1 FIXED, SEC5-3 PARTIAL, SEC5-1 FIXED sebagaimana dilaporkan tetapi jaminan barunya ("file hanya dijangkau lewat sandbox yang dijurnal") dipatahkan oleh race ancestor-directory di executor undo (**SEC5-12, HIGH, baru**). Temuan terbuka: **SEC5-3 (PARTIAL, HIGH), SEC5-12 (HIGH), SEC5-13 (MINOR), SEC5-14 (MINOR)**.

Head yang direview (stack berbasis `origin/main` `67c6a80`; `origin/main` saat review `b9164c8`, tanpa konflik di file stack):

| PR | Branch | Head | Verdict round 2 |
|---|---|---|---|
| #170 (1/5) | `feat/f2-wire-journal` | `39e56cb` | **approve** — SEC5-5/8/9/10/11 FIXED |
| #177 (2/5) | `feat/f2-wire-undo` | `593c343` | **changes required** — SEC5-3 PARTIAL, SEC5-12 HIGH, SEC5-13/14 MINOR |
| #171 (3/5) | `feat/f2-wire-memory` | `f7c2cf0` | **approve** — SEC5-6/7 FIXED (sanitizer bersama di #177: SEC5-13) |
| #173 (4/5) | `feat/f2-wire-budget` | `27bb33f` | **approve** — tidak ada temuan |
| #174 (5/5) | `feat/f2-wire-modules` | `d919aff` | **approve** — SEC5-11 FIXED; stack tetap tertahan #177 |

Metode: worktree terpisah, Linux amd64, `GOMAXPROCS=2 GOFLAGS=-p=1 nice -n 19`, `ulimit -v 4 GiB`. Gate: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/ctxdb/journalstore/... ./internal/archtest/...` di `d919aff` → **semua ok** (`cmd/damnbot` 34,9 s, `internal/journal` 1,0 s, `journalstore` 63,2 s, `archtest` 4,1 s). Probe `TestZZ*` (file `cmd/damnbot/zz_sec5r2_probe_test.go`) dijalankan lalu dihapus; tidak ada kode produk yang diubah.

### Verdict per temuan round 1

| ID | Verdict | Bukti |
|---|---|---|
| SEC5-1 | **FIXED** (lihat SEC5-12) | `journal_wire.go:26-53` `journalScope`: roots `Abs`+`EvalSymlinks`, deny `~/` di-expand saat record; disimpan di `actions.inverse.scope` (`journalstore.go:85-86,109`, `decode:437`). `undo.go:167-186` `undoSandbox`: hanya roots/deny jurnal + deny config **saat ini**; aksi tanpa scope, scope invalid/kosong, atau beberapa scope berbeda → ditolak. `sessions.cwd` hanya dipakai untuk **memilih** sesi default (`undo.go:141-160`), tidak lagi untuk sandbox. Probe `TestZZActionsScopeImmutable`: `UPDATE actions SET inverse=…` → `actions is append-only (1811)`; `UPDATE sessions SET cwd='/'` → `sessions is immutable`. Test `TestUndoJournaledSandbox` (cwd `/` + `add_dirs=["/"]` tidak memulihkan file luar; aksi tanpa scope ditolak; deny_paths sekarang tetap menolak). |
| SEC5-2 | **FIXED** | `undo.go:115-129`: sesi ditentukan dulu (`--session` atau sesi default direktori), lalu `a<id>` wajib `a.SessionID == sid`, else exit 1 `is not in session`. `undoApply` memanggil `Undoer.Undo(ctx, sid, id)` dengan id yang sama. Test `TestUndoActionIDScoped` (`a<id>` tanpa `--session` dari dir lain dan dengan `--session B` → refuse; file tidak tersentuh). |
| SEC5-3 | **PARTIAL** | `undo.go:277-361`: dry-run memakai sandbox jurnal per aksi, `Check(OpWrite)` harus `Inside/HardLinked`, komponen akhir via `os.Root.Lstat` (regular saja, symlink/special ditolak), `os.SameFile` antara Lstat dan handle, cap 1 GiB. `TestUndoDryRunReadsThroughSandbox` lulus (symlink final ke luar/dalam root ditolak tanpa isi). **Tetapi** `readInSandbox` membuka `os.OpenRoot(dir)` dengan path direktori **tidak ter-resolve** setelah `Check`; `OpenRoot` mengikuti symlink pada komponen ancestor. Probe `TestZZDryRunParentSwapRace` (goroutine menukar `ws/sub` ↔ symlink ke dir luar): **1.435 pembacaan `TOPSECRET` dari luar sandbox** dari ±78k panggilan dalam 4 s (`refused=12.818`). Perbaikan: baca dengan `sb.OpenFile(ctx, p, os.O_RDONLY, 0)` (openat2 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` relatif ke root, `internal/sandbox/open.go:35-41`) lalu cek regular/size pada handle; tambah test race ancestor-swap. |
| SEC5-4 | **FIXED** (residual SEC5-13) | `cli.go:86-115` `unsafeRune` = Cc∪Cf∪Zl∪Zp; `cleanLine` untuk path/MoveTo/verdict/error, `cleanBlock` per baris diff (`undo.go:247-249,284,288,392`); stderr `runSub` juga `cleanLine` (`cli.go:56,64`). Detail hasil `undoApply` (`undo.go:218`) adalah konstanta `internal/journal` (diperiksa `undo.go:57-293`), bukan data target. Test `TestUndoOutputSanitized`. Probe `TestZZSanitizerEdges`: ESC, NEL U+0085, ZWSP/ZWJ/LRM/ALM, BOM, soft hyphen, tag U+E0041, U+180E, LS U+2028, U+FFF9 dibuang; **byte UTF-8 invalid (C1 `0x9B`) lolos di `cleanLine`** → SEC5-13. |
| SEC5-5 | **FIXED** | `run.go` (`d919aff`): semua jalur gagal setelah `newSessionID` memanggil `closeDB(ctx, db, sid, …)` yang `ReleaseLease` sebelum close; setelah engine/runner dibuat, `fail` = `started.close(ctx)` (modules→engine→sessions→runner→lease→DB); jalur sukses `runModel.close` juga lewat `closeDB`. Error di `runTurn` ditutup oleh `defer model.close`. Test `TestRunStartFailureReleasesLease` (`nobase/m` → exit 2, `sessions=1`, `leases=0`). |
| SEC5-6 | **FIXED** (residual SEC5-13) | `review.go:218-237`: setiap kolom (status, scope, category, origin, severity, content) lewat `cleanLine`; Cf/bidi kini dibuang. Test `TestReviewSanitizesContent`. |
| SEC5-7 | **FIXED** | `review.go:111-116` listing tetap 50 tetapi menulis catatan "first N shown"; `--bulk` = `drainPending` (`:166-180`) halaman demi halaman sampai kosong, entri gagal tidak diulang (tanpa loop tak hingga); id eksplisit via `pendingByID` (`:184-212`) tanpa cap, wajib `scanned` dan scope ter-normalisasi (`memory.NormalizeScope`). Test `TestReviewBeyondOnePage`. |
| SEC5-8 | **FIXED** | Untuk setiap branch, `git rev-list origin/main..<branch>` + `git diff-tree --diff-filter=AM`: **tidak ada** path `damnbot`/`*/damnbot` dan **tidak ada** blob > 1 MiB; `git rev-list --objects origin/main..origin/feat/f2-wire-modules \| git cat-file --batch-check` → nol blob > 1 MiB. Ref remote `refs/pull/171/head` = `f7c2cf0` (bersih); `d6c7ffb` hanya tersisa di ref lokal reviewer lama, tidak di origin. `.gitignore` mengabaikan `/damnbot`, `/damnbot.exe`, `/cmd/damnbot/damnbot{,.exe}`. Guard `internal/archtest/binaries_test.go` `TestNoLargeOrBinaryFiles` (>1 MiB di luar allowlist + ELF/Mach-O/PE) lulus. Nit: `/damnbot` tercantum dua kali di `.gitignore`; magic Mach-O universal `0xcafebabe` tidak dideteksi (ambigu dengan Java class) — tertutup sebagian oleh cap 1 MiB. |
| SEC5-9 | **FIXED** | `tools_wire.go:15-28` `loopTranscript` no-op: loop satu-satunya penulis `tool_dispatch`/`tool_result` (`loop/dispatch.go:193-199` membawa `args_sha`+`effect`, jadi audit tidak hilang); `journalstore.go:179-199` `anchor` menggantung baris `actions` pada event dispatch loop (`ORDER BY seq DESC`, turn id dipertahankan) dan hanya menulis event sendiri untuk record mandiri. Test `TestOneDispatchPerCall` (read+edit: `tool_dispatch=2`, `tool_result=2`, aksi ter-anchor 1). |
| SEC5-10 | **FIXED** | Cap: `journal_wire.go:22-24,133-135` menolak image > 1 GiB sebelum alokasi; `readRegular` dry-run juga (`undo.go:345`). Benchmark: `BenchmarkJournalRecordEdit1MB` (recorder produksi + journalstore + ctxdb nyata) **24,8 ms/op, 2,08 MB/op, 520 allocs/op**; `BenchmarkCodetools/Edit1MB` 34,0 ms vs `Edit1MBJournal` 39,5 ms (host ini, 30x). Catatan: biaya edit+record end-to-end ≈ 60 ms dan tidak di-gate terhadap budget CT-09 40 ms; memori puncak hingga 2× image (before+after) per edit, dibatasi cap 1 GiB/image. Operasional, bukan keamanan. |
| SEC5-11 | **FIXED** | `run.go`: `fail` = `started.close`, yang memanggil `modules.runtime.Shutdown` bila ada; `started.modules` diisi hanya setelah `newRunModules` sukses (yang tidak membuat runtime saat error, `modules_wire.go:44-73`). Test `TestRunModulesWiring`/`TestStartRegistryError`. |

### Temuan baru round 2

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| SEC5-12 | #177 (kode `internal/journal`, pra-ada) | **HIGH** | confirmed | Executor undo memeriksa path dengan `sb.Check` lalu membuka direktori induk dengan `os.OpenRoot(filepath.Dir(p))` pada path **asli, tidak ter-resolve** (`internal/journal/undo.go:225-241` `current`, `:420-431` `parentRoot`, dipakai `write/remove/rename/chmod`). `os.OpenRoot` mengikuti symlink di komponen ancestor, sehingga direktori yang ditukar ke symlink di antara Check dan open mengarahkan **tulis/hapus/rename ke luar sandbox jurnal**. Hanya komponen akhir yang `O_NOFOLLOW`. Kode ini pra-ada, tetapi #177 menjadikannya permukaan CLI (`damnbot undo`) dan klaim SEC5-1 "every file is reached only through the sandbox journaled with the action" (`undo.go:3-4`) tidak berlaku. Penyerang: proses lokal yang dapat me-rename direktori di workspace saat user menjalankan undo (mis. run/agent lain di workspace yang sama atau proses latar dari shell agent). | Probe `TestZZUndoExecutorParentSwapRace`: aksi `ws/sub/f` (before=`PWNED`), sandbox jurnal roots=[ws], goroutine menukar `ws/sub` ↔ symlink ke dir luar; **`out/f` ditimpa `PWNED` setelah 18 undo** (`Undoer.Undo` persis seperti `undoApply`). |
| SEC5-13 | #177 (dipakai #171) | MINOR | confirmed | `cleanLine` memakai `strings.FieldsFunc`, yang memotong string asli: byte UTF-8 invalid (didecode `RuneError`, bukan Cc) diteruskan **mentah**, termasuk C1 8-bit `0x9B` (CSI), `0x9D` (OSC). Terminal UTF-8 modern umumnya mengabaikannya, tetapi terminal/locale 8-bit menafsirkannya sebagai escape. Nama file Linux boleh berisi byte arbitrer, jadi `undo ls`/`--dry-run`/stderr terpapar. `cleanBlock` aman (`strings.Map` mengganti dengan U+FFFD). Invisible filler (U+3164, U+FFA0, U+2800, CGJ, VS16) juga lolos — spoofing kosmetik saja. | Probe `TestZZSanitizerEdges`: `cleanLine("a\x9b2Jb") == "a\x9b2Jb"`; `cleanBlock` → `"a\uFFFD2Jb"`. Perbaikan: `strings.ToValidUTF8(s, "\uFFFD")` sebelum `FieldsFunc`, atau perlakukan `utf8.RuneError` sebagai unsafe; tambah kasus byte `0x9B` di `TestUndoOutputSanitized`. |
| SEC5-14 | #177 | MINOR | static | `undo N`: kumpulan aksi yang dicek scope-nya (`undoTargets` → `store.List`, `undo.go:130-138`) diambil **sebelum** lease; `Undoer.UndoLast` me-`List` ulang **sesudah** lease (`internal/journal/undo.go:71-77`). Aksi yang ditambahkan di antara keduanya (run yang selesai tepat di jendela itu) dieksekusi dengan sandbox milik aksi lain tanpa dicek. Dampak praktis kecil (`run` selalu membuat sesi baru → satu scope per sesi; deny saat ini tetap berlaku), tetapi pemeriksaan dan eksekusi harus memakai himpunan yang sama. | Perbaikan: ambil lease dulu, lalu `List` + `undoSandbox` + undo per id yang sudah dicek (`Undo(ctx,sid,id)` per aksi atau `UndoLast` yang menerima daftar). |

### Rekomendasi SEC5-3 + SEC5-12 (satu perbaikan)

Semua akses file undo (preview dan eksekusi) harus beranjak dari **root sandbox**, bukan dari path direktori absolut:

- baca: `sb.OpenFile(ctx, p, O_RDONLY, 0)` (openat2 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` / walk `O_NOFOLLOW`), cek regular + size pada handle;
- tulis/hapus/rename: buka `os.OpenRoot(root)` untuk root yang memuat path ter-resolve dari `Check`, lalu operasi `root.OpenFile/Remove/Rename/Chmod` dengan path relatif (os.Root menolak escape via symlink ancestor), atau API sandbox setara `openBeneath` untuk direktori induk;
- test regresi: race ancestor-swap untuk dry-run (tidak ada byte luar) dan undo (file luar tidak berubah), dengan deadline/iterasi tetap.

### Diperiksa dan bersih (round 2)

- **Tamper jurnal/sesi:** `actions` dan `sessions` dijaga trigger (`0006_actions.sql:83,86`; guard sessions) — UPDATE ditolak (probe). Scope jurnal tidak bisa dilebarkan tanpa melewati trigger; mengubah DB di luar proses = di luar model ancaman.
- **Roots jurnal dengan `..`/relatif/symlink:** `journalScope` → `filepath.Abs` + `EvalSymlinks`; `sandbox.New` di undo melakukan `canonicalRoot` lagi; roots kosong ditolak.
- **Tulisan approved di luar root:** dijurnal dengan scope run, sehingga undo menolaknya (`outside the action's sandbox`) — fail-closed, tidak dapat di-undo; perilaku konsisten dengan SEC5-1.
- **Hook jurnal:** dipanggil setelah `tmp.Sync()` sebelum replace (`codetools/commit.go:75-79`), juga jalur create (`edittool.go:363`); hanya tool `write` dengan recorder (`builtins_file.go:132-135`). `TestJournalHookSeesBothImages`, `TestJournalHookErrorAborts`.
- **Review/quarantine:** `pendingByID` membandingkan scope ter-normalisasi; `Service.decide` menormalkan scope lagi dan store memutuskan per (id, scope). `--rejected` hanya listing. `Service.Get` tidak di-scope, tetapi hanya dipakai `pendingByID` yang memfilter.
- **Budget (#173) / modules (#174):** range-diff terhadap round 1 hanya rebase + `memory.NormalizeScope`; tidak ada perubahan perilaku guard/registry. Dispatcher journal no-op tidak menghapus gate approval (approval tetap di loop sebelum `dispatchEvent`, `loop/dispatch.go:170-189`).
- **Redaksi:** `tool_result` kini hanya ditulis loop dari hasil dispatcher yang sudah melalui `Redactor`/spill; tidak ada jalur transcript kedua yang tidak teredaksi.

### Residual risk round 2

- SEC5-3/SEC5-12 membutuhkan penyerang lokal yang berlomba saat undo; tanpa perbaikan, sandbox undo tidak memberi jaminan terhadap proses di workspace. `TRUST-MODEL §3` sudah mencatat swap ancestor sebagai residual untuk `OpenApproved`, tetapi undo memakai jalur `Inside` yang seharusnya bebas race.
- Undo tetap Linux-teruji; Windows/macOS hanya statis.

**Overall: REQUEST CHANGES** — blocker merge: SEC5-3 (PARTIAL) dan SEC5-12 di #177; SEC5-13/SEC5-14 MINOR dapat diperbaiki di commit yang sama.

---

## Round 3 — 2026-10-21

**Verdict keseluruhan: REQUEST CHANGES.** Vektor round 2 tertutup: SEC5-3 dan SEC5-12 tidak lagi dapat mencapai **luar root**, SEC5-13 dan SEC5-14 FIXED. Namun `os.Root` hanya membatasi ke root, bukan ke `deny_paths`. Swap ancestor menjadi symlink **relatif ke direktori ter-deny di dalam root** masih membelokkan tulisan undo dan pembacaan dry-run ke path ter-deny (**SEC5-15, HIGH, baru**). Temuan terbuka: **SEC5-15 (HIGH)**, **SEC5-16 (MINOR)**, **SEC5-17 (nit)**.

| PR | Branch | Head | Verdict round 3 |
|---|---|---|---|
| #170 (1/5) | `feat/f2-wire-journal` | `77a3c9e` | **changes required**: SEC5-15 di `internal/journal/undo.go` (commit `77a3c9e`), SEC5-16 |
| #177 (2/5) | `feat/f2-wire-undo` | `d732a4c` | **changes required**: SEC5-15 di `readInSandbox`; SEC5-13/14 FIXED |
| #171 (3/5) | `feat/f2-wire-memory` | `9e72599` | **approve**: rebase saja |
| #173 (4/5) | `feat/f2-wire-budget` | `a13c40a` | **approve**: rebase saja |
| #174 (5/5) | `feat/f2-wire-modules` | `da83250` | **approve**: rebase saja; stack tertahan #170/#177 |

Gate di `da83250`: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/ctxdb/journalstore/... ./internal/archtest/... ./internal/sandbox/...` → **semua ok** (33,7 s / 3,1 s / 54,0 s / 3,5 s / 1,3 s). Probe `TestZZR3*` (`cmd/damnbot/zz_sec5r3_probe_test.go`) dijalankan lalu dihapus. Kode produk tidak diubah.

### Range-diff dan SEC5-8

- `git range-diff origin/main..d919aff origin/main..da83250`: `39e56cb = 39e56cb`, `593c343 = e4b67c8`, `595edae = 15f84a7`, `f7c2cf0 = 9e72599`, `27bb33f = a13c40a`, `d919aff = da83250`. Semuanya `=` (patch identik), ditambah dua commit baru `77a3c9e` dan `d732a4c`. **#171/#173/#174 hanya rebase.**
- SEC5-8 **tetap FIXED**. Untuk kelima branch, `git rev-list origin/main..<branch>` + `diff-tree --diff-filter=AMR` tidak menghasilkan path `damnbot`/`damnbot.exe`, dan `rev-list --objects | cat-file --batch-check` tidak menemukan blob > 1 MiB (0/0/0/0/0). `TestNoLargeOrBinaryFiles` lulus.

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC5-1 | **FIXED** (tidak berubah) | Scope jurnal append-only; lihat round 2. |
| SEC5-2 | **FIXED** | `undoApply` (`undo.go:189`) memakai `undoTargets` yang sama sesudah lease, dan pemeriksaan sesi `a<id>` tetap berlaku. |
| SEC5-3 | **FIXED untuk luar root; lihat SEC5-15** | `readInSandbox` (`undo.go:333-350`): hasil `Check` di-resolve, `filepath.Clean(p) == resolved` diwajibkan (symlink yang muncul belakangan → refuse), dan file dibuka lewat `os.OpenRoot(root kanonik)` + `rel` dari `RootOf`. Probe `TestZZR3DryRun` "outside": **0 bocor dari 65.261 pembacaan** (round 2: 1.435). Swap ke symlink relatif ke dir ter-deny di dalam root: **86 bocor dari 64.986** → SEC5-15. |
| SEC5-4/5/6/7/9/10/11 | **FIXED** (tidak berubah) | Patch identik (range-diff `=`). Test tetap lulus. |
| SEC5-12 | **FIXED untuk luar root; lihat SEC5-15/16** | `internal/journal/undo.go`: `check` mengembalikan path ter-resolve. `current:231` menolak `Clean(p) != resolved`, lalu melakukan `Lstat` → regular → `OpenFile(O_NOFOLLOW)` → `SameFile`. `write/remove/chmod/rename` memakai `parentRoot:443` (`sandbox.RootOf` → `os.OpenRoot(root)`; `rel == "."` ditolak, sehingga path yang sama dengan root ditolak). Rename lintas root ditolak (`:415`). Probe `TestZZR3ExecutorOutside` (mengulang probe round 2): **0 tulisan ke luar dalam 5.948 undo** (round 2: tembus setelah 18). |
| SEC5-13 | **FIXED** | `cli.go` `cleanLine` memanggil `strings.ToValidUTF8(s, "\uFFFD")` sebelum `FieldsFunc`. `TestCleanLineInvalidUTF8` (`0x9B`, `0x9D`, `\xff\xfe\x80`, UTF-8 terpotong; `cleanBlock` juga). |
| SEC5-14 | **FIXED** | `undoApply`: `undoSession` → `AcquireLease` → `undoTargets` (list) → `undoSandbox` (cek scope) → `UndoActions(acts)` (`internal/journal/undo.go:85`) tanpa list ulang. `a<id>` memakai id yang sama. `TestUndoActionsExecutesCheckedSet`. |

### Temuan baru round 3

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| SEC5-15 | #170 (`77a3c9e`), #177 (`d732a4c`) | **HIGH** | confirmed | `os.Root` **mengikuti symlink selama tetap di dalam root**. `parentRoot`/`readInSandbox` membuka root kanonik lalu memakai `rel` tekstual, sehingga ancestor yang ditukar dengan symlink **relatif** ke subdirektori lain di dalam root, sesudah `Check` dan sebelum operasi, dapat membelokkan write/remove/chmod/rename dan read ke path yang dilarang **`deny_paths`**. `Check` hanya menilai path pada saat pemeriksaan. Contoh: cwd = `$HOME` (root = home) dengan deny bawaan `~/.ssh`. Aksi `~/sub/authorized_keys` dengan before-image yang dipilih penyerang, `~/sub` → `.ssh`, sehingga undo menulis `~/.ssh/authorized_keys`. Pola deny di dalam repo (`**/.env`, `**/*.pem`) juga terdampak. Pada run, codetools memakai `sandbox.OpenFile` (`RESOLVE_NO_SYMLINKS`) sehingga tidak terdampak; masalah ini khusus undo. | Probe `TestZZR3ExecutorDeniedInRoot` (root=ws, `deny_paths=[ws/denied]`, swap `ws/sub` ↔ symlink `denied`): **`ws/denied/f` ditimpa `PWNED` setelah 465 undo**. `TestZZR3DryRun` "denied in-root": **86 pembacaan isi ter-deny**. |
| SEC5-16 | #170 (`77a3c9e`) | MINOR | confirmed | Direktori root itu sendiri dibuka ulang berdasarkan path (`os.OpenRoot(dir)`) pada setiap operasi. Root yang diganti symlink sesudah `Check` membelokkan operasi ke target symlink tersebut. Penyerang membutuhkan izin tulis di **parent root**, yang berada di luar sandbox, sehingga dampaknya lebih kecil daripada SEC5-15. | Probe `TestZZR3ExecutorRootSwap` (swap `parent/ws` ↔ symlink ke dir luar): **file luar ditimpa setelah 5 undo**. |
| SEC5-17 | #170 | nit | static | Pada `undoMove`, `u.remove(srcResolved)` (`undo.go:190`, hanya dengan `--force` dan src ada) dijalankan **sebelum** penolakan rename lintas root (`:415`). Akibatnya src terhapus walaupun rename gagal. Saat ini tidak terjangkau karena `run` hanya menjurnal `file_write`/`file_edit`. | Statis. Perbaikan: validasi bahwa src dan dst berada di root yang sama sebelum remove. |

### Rekomendasi SEC5-15/16

- Untuk operasi mutasi, buka **direktori induk** dari fd root sandbox tanpa mengikuti symlink apa pun (`openat2(RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)` atau walk `O_NOFOLLOW|O_DIRECTORY`, implementasi yang sama dengan `sandbox.openBeneath`). Lalu lakukan `openat/unlinkat/renameat/fchmodat` relatif ke fd tersebut. Jangan memakai `os.Root` dengan `rel` multi-komponen.
- Untuk dry-run, pakai `sb.OpenResolved(ctx, resolved, false)` (`internal/sandbox/resolved.go:48`, `RESOLVE_NO_SYMLINKS`) atau `sb.OpenFile(ctx, p, O_RDONLY, 0)`, lalu periksa regular file + ukuran pada handle.
- Untuk root: buka root dengan identitas yang dicatat saat `sandbox.New` (`os.SameFile` pada `Stat(".")`), atau dari fd root milik sandbox.
- Test regresi: tambahkan pada kedua race test kasus symlink **relatif ke dir ter-deny di dalam root** dengan target yang tidak boleh berubah atau terbaca.

### Determinisme test baru (risiko CI)

- `TestUndoAncestorSwapStaysInSandbox` (`internal/journal/undo_race_test.go`) dan `TestUndoDryRunAncestorSwapNeverReadsOutside` (`cmd/damnbot/undo_race_test.go`) dibatasi deadline 2 s dengan satu goroutine swapper berurutan. Error swapper dilaporkan lewat `errc` dan menghentikan test. Reset file dilakukan lewat handle `os.Root` yang tahan rename. Tidak ada assert berbasis waktu.
- Test ini **tidak dapat gagal palsu** terhadap kode yang benar: assert hanya "byte luar tidak berubah/terbaca". Sensitivitasnya terhadap regresi bersifat probabilistik, tetapi memadai: kode lama tembus dalam 18 iterasi (executor) dan sekitar 360 kebocoran/detik (dry-run).
- Stress: `-race -count=10` untuk keempat test baru → **ok** (journal 21,2 s; cmd 21,1 s). Biaya CI sekitar 2 × 2 s per run, ditambah satu core sibuk selama test. **Risiko flake: rendah.**
- `TestUndoActionsExecutesCheckedSet` dan `TestCleanLineInvalidUTF8` deterministik.

### Bypass `RootOf` lain yang diperiksa

- **Path sama dengan root:** `RootOf` → `rel == "."` → `parentRoot` menolak (`ErrUnsafePath`); `readRegular(".")` → bukan regular → refuse.
- **Root di-symlink saat record:** `journalScope` dan `canonicalRoot` me-resolve root, sehingga root jurnal kanonik. Root yang diganti **sesudahnya** dibahas di SEC5-16.
- **Symlink persisten pada path aksi:** `Check` me-resolve ke path lain, sehingga `Clean(p) != resolved` → conflict/refuse. Untuk target luar maupun dalam root, tidak ada operasi yang dijalankan.
- **Hard link:** verdict `HardLinked` tanpa `--force` → conflict. Dengan `--force`, `writeInPlace` menulis ke inode bersama, yang bisa berada di luar root. Ini desain pra-ada (JN2-5/JN2-12): pengguna menerima pesan eksplisit dan perlu memutuskannya. Dicatat sebagai residual, bukan temuan baru.
- **Rename lintas root:** ditolak berdasarkan `root.Name()`; lihat SEC5-17 untuk urutan remove.
- **Root bersarang:** `RootOf` mengembalikan root pertama yang memuat path. Root luar tetap berada di sandbox, dan deny diperiksa pada path ter-resolve.

**Overall: REQUEST CHANGES.** Blocker merge: SEC5-15 (HIGH) di #170 dan #177. SEC5-16 (MINOR) dan SEC5-17 (nit) sebaiknya diperbaiki bersama, karena satu perbaikan berbasis fd tanpa symlink menutup SEC5-15 dan SEC5-16 sekaligus.

---

## Round 4 — 2026-10-21

**Verdict keseluruhan: APPROVE.** SEC5-15 (HIGH) dan SEC5-17 sudah FIXED. SEC5-16 FIXED untuk vektor yang dilaporkan (root diganti symlink). Varian sisanya, yaitu root diganti **direktori nyata** lewat rename, membutuhkan izin tulis di parent root yang berada di luar sandbox, sehingga tetap terbuka sebagai MINOR (SEC5-16b). Hardening baru SEC5-18 berstatus nit. Tidak ada temuan BLOCKER, HIGH, atau MEDIUM yang terbuka.

Stack berbasis `origin/main` `58e286b`:

| PR | Branch | Head | Isi | Verdict round 4 |
|---|---|---|---|---|
| #180 (0/6) | `feat/f2-wire-sandbox` | `822163e` | `RootOf`, `RootIdentity`, `PinRoot` | **approve** (catatan SEC5-16b) |
| #181 (1/6) | `feat/f2-wire-journal-fix` | `b181eef` | executor undo via `OpenResolved`/`OpenFile`/`OpenDir`, `unlinkat`/`renameat` pada handle induk, fchmod, root dipin, validasi move lintas root | **approve**: SEC5-12/15/17 FIXED; SEC5-16 FIXED untuk symlink (16b MINOR); SEC5-18 nit |
| #170 (2/6) | `feat/f2-wire-journal` | `a60b7b8` | wiring jurnal | **approve**: rebase saja |
| #177 (3/6) | `feat/f2-wire-undo` | `632d763` | dry-run via `OpenResolved`, `Undoer.Close` | **approve**: SEC5-3/15 dry-run FIXED |
| #171 (4/6) | `feat/f2-wire-memory` | `7ecb211` | review | **approve**: rebase saja |
| #173 (5/6) | `feat/f2-wire-budget` | `11221de` | budget | **approve**: rebase saja |
| #174 (6/6) | `feat/f2-wire-modules` | `4220f05` | modules | **approve**: rebase saja |

Gate di `4220f05`: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/ctxdb/journalstore/... ./internal/archtest/... ./internal/sandbox/...` → **semua ok** (37,4 s / 7,1 s / 54,5 s / 3,7 s / 1,4 s). Probe `TestZZR4*` (`cmd/damnbot/zz_sec5r4_probe_test.go`) dijalankan lalu dihapus. Kode produk tidak diubah.

### Range-diff dan SEC5-8

- `git range-diff 67c6a80..da83250 58e286b..4220f05`: `39e56cb = a60b7b8`, `593c343/e4b67c8 = 81b6cb4`, `15f84a7 = b35093f`, `9e72599 = 7ecb211`, `a13c40a = 11221de`, `da83250 = 4220f05`. Semuanya patch identik. Untuk `d732a4c ! 5625362`, isi kode sama persis (diff patch tanpa `CHANGELOG.md`/`docs/DECISIONS.md` kosong); yang berbeda hanya teks CHANGELOG karena rebase ke `58e286b`. Commit `77a3c9e` diganti oleh **`822163e` + `b181eef`**, dan ada commit baru `632d763`. Dengan demikian **#170/#171/#173/#174 hanya rebase**.
- SEC5-8 **tetap FIXED** pada ketujuh branch: tidak ada path `damnbot`/`damnbot.exe` di commit `origin/main..<branch>`, dan tidak ada blob > 1 MiB (0 di setiap branch).

### Probe ulang (head `4220f05`, Linux amd64 dengan openat2)

| Vektor | Round 3 | Round 4 |
|---|---|---|
| A. Executor: ancestor → symlink absolut ke luar root | tidak tembus | **tidak tembus**, 4.422 undo |
| B. Executor: ancestor → symlink **relatif** ke dir ter-deny di dalam root (deny literal) | tembus setelah 465 undo | **tidak tembus**, 4.457 undo |
| B'. Seperti B, deny glob `**/denied` | (tidak diuji) | **tidak tembus**, 4.180 undo |
| C. Root diganti symlink ke dir luar | tembus setelah 5 undo | **tidak tembus**, 4.548 undo |
| D. Dry-run: symlink ke luar / ke dir ter-deny | 0 bocor / 86 bocor | **0 / 0** (54.254 dan 60.447 pembacaan) |
| G. Move lintas root dengan `--force` | src terhapus (statis) | **ditolak sebelum remove**: `failed "move crosses sandbox roots"`, src `SRC-NOW` utuh, dst utuh |
| F. Root diganti **direktori nyata** lewat rename (butuh izin tulis di parent root) | — | **tembus setelah 1.004 undo** → SEC5-16b |
| H. Dir ter-deny di-*rename* (tanpa symlink) ke path parent aksi, deny literal | — | **tidak tembus**, 4.356 undo (identitas dir ter-deny dicek pada handle `OpenDir`) |
| H'. Seperti H, deny glob `**/denied` | — | **tembus setelah 3 undo** → pra-ada, lihat Residual |

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC5-1…11 | **FIXED** (tidak berubah) | Lihat round 2/3; patch identik. |
| SEC5-3 | **FIXED** | `readInSandbox` (`cmd/damnbot/undo.go:339`): `Check` → `Clean(p) == resolved` → `sb.OpenResolved(resolved)` (openat2 `RESOLVE_NO_SYMLINKS` dari `/`, deny dicek ulang, identitas deny pada handle) → regular + cap dibaca lewat `LimitReader(cap+1)`. Probe D 0 bocor. `TestUndoDryRunAncestorSwapNeverReadsOutside`, `TestUndoDryRunRelativeSymlinkToDeniedDir`. |
| SEC5-12 | **FIXED** | `internal/journal/undo.go`: `current:286` memakai `OpenResolved`; `write:365` memakai `parentDir` (`OpenDir`, tanpa symlink) + `renameat`/`unlinkat` pada handle induk; chmod adalah `f.Chmod` (fchmod) pada handle `OpenResolved` (`chmodFile:402`). Probe A. `TestUndoAncestorSwapStaysInSandbox`. |
| SEC5-13/14 | **FIXED** | Tidak berubah sejak round 3. |
| SEC5-15 | **FIXED** | Tidak ada lagi `os.Root` atau path relatif multi-komponen. Setiap open memakai `RESOLVE_NO_SYMLINKS`, sehingga symlink relatif di dalam root juga ditolak, dan `deny_paths` dicek ulang pada path serta identitas handle. Probe B/B'/D. `TestUndoRelativeSymlinkToDeniedDir`. |
| SEC5-16 | **FIXED untuk symlink; residual 16b** | `PinRoot` (`internal/sandbox/resolved.go:51`) membuka root lewat `OpenDir` (tanpa symlink) lalu memeriksa `os.SameFile` pada **handle** terhadap identitas yang dicatat `New` (`RootIdentity:39`; `canonicalRoot` sekarang menyimpan `fi`). Pemeriksaan ini tidak punya TOCTOU untuk handle itu sendiri. Probe C. `TestPinRoot`, `TestUndoRootReplacedBySymlink`. Lihat SEC5-16b. |
| SEC5-17 | **FIXED** | `undoMove` (`undo.go:202`) memeriksa `RootOf(src) == RootOf(dst)` **sebelum** `remove`. Probe G. `TestUndoMoveCrossRootKeepsSource`. |

### Primitif sandbox baru (#180) sebagai batas keamanan

- **`RootOf`/`RootIdentity`:** pencocokan prefix komponen (`hasPathPrefix`, fold di Windows) pada root kanonik. Root pertama yang memuat path dipakai; untuk root bersarang, hasilnya tetap root sandbox. `rel == "."` ditolak oleh `parentDir` (base kosong, `.` atau `..` → `ErrUnsafePath`).
- **`PinRoot`:** `OpenDir` → `OpenResolved` → openat2 dari `AT_FDCWD` dengan `RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS`. Root yang diganti symlink gagal dengan `ELOOP`. Root yang diganti direktori nyata tertangkap oleh `SameFile` pada handle (`TestPinRoot`). Nilai identitas diambil dari `os.Stat` saat `New` pada path yang sudah di-resolve.
- **Fallback tanpa openat2** (`ENOSYS`/`EPERM`, kernel < 5.6 atau seccomp; dianalisis statis, `open_linux.go:16-29`, `walk_unix.go:21-44`): `walkOpen` membuka root lewat path (bisa mengikuti symlink root), lalu berjalan per komponen dengan `openat(O_NOFOLLOW|O_DIRECTORY)`. Linux menutup celah lewat `verifyProcFd` (`readlink /proc/self/fd/N == want`, fail-closed tanpa `/proc`). macOS/BSD memakai `verifyInode` (dev/ino handle == `Lstat(want)`; rename yang dibatalkan sebelum lookup adalah residual ADR-097 yang sudah terdokumentasi). Komponen akhir dibuka `O_NOFOLLOW|O_NONBLOCK`, dan `newFile` menolak FIFO/device. Saya tidak menemukan cara agar fallback menerima path yang ditolak jalur openat2, kecuali residual ADR-097 tersebut.
- **`Dir.Open`** (pra-ada): `openat(O_NOFOLLOW)` satu komponen, `dirChild` menolak separator, `.` dan `..`, identitas deny dicek, dan `HardLinked` dilaporkan.

### Temuan terbuka round 4

| ID | PR | Severity | Status | Temuan | Bukti / perbaikan |
|---|---|---|---|---|---|
| SEC5-16b | #181 | MINOR | confirmed | Root **dipin hanya sebagai pemeriksaan**. `rootDir` dipanggil di `parentDir`, tetapi operasi selanjutnya (`OpenDir(dir)`, `OpenFile(tmp)`, `OpenResolved` untuk chmod) kembali me-resolve path absolut dari `/`, **bukan relatif terhadap handle root yang dipin**. Root yang diganti direktori nyata lain (rename, tanpa symlink) **setelah** pin menerima operasi undo. Prasyaratnya izin tulis di parent root, yaitu di luar sandbox, sehingga penyerang dengan kemampuan itu sudah dapat menulis langsung ke direktori tersebut. | Probe F: `other/sub/f` ditimpa setelah 1.004 undo. Perbaikan: buka parent dan file relatif terhadap fd root yang dipin (openat2 `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` dari dfd pin), atau pastikan ulang `SameFile` antara root dan handle sesudah operasi. |
| SEC5-18 | #181 | nit | static | Dalam `write`, temp dibuat lewat `sb.OpenFile(filepath.Join(dir, tmp))` (berbasis path), sedangkan rename memakai handle `h`. Hal yang sama berlaku untuk `chmodFile` sesudah rename. Jika `dir` di-rename-swap di antara keduanya, temp berisi before-image dapat tertinggal di direktori lain (nama `*.damnbot-undo-*` acak, `renameat` gagal, sehingga target tidak berubah), dan chmod dapat mengenai file lain pada path yang sama. Kerusakan tetap di dalam sandbox, dan deny dicek ulang. | Perbaikan: buat temp dengan `openat(h, tmp, O_CREAT|O_EXCL|O_NOFOLLOW)` dan lakukan chmod lewat handle temp sebelum `renameat` (fchmod sebelum rename). |

### Residual (pra-ada, bukan regresi stack)

- **Deny glob dan rename direktori (H').** `deny_paths` dicocokkan berdasarkan path. Hanya pola literal absolut yang mencatat identitas (`compileDeny`/`appendID`), sehingga direktori yang cocok dengan pola **glob** dan di-*rename* ke nama yang tidak ter-deny keluar dari namespace deny. Ini berlaku untuk **semua** tool file (write_file dapat menulis ke `x/…` sesudah `mv denied x`, tanpa race), bukan hanya undo. Masalah ini termasuk model kepercayaan sandbox (path-based deny) dan di luar cakupan stack ini. Pola literal (mis. `~/.ssh`) tertangkap pada handle direktori induk (probe H).
- **Hard link + `--force`:** `writeInPlace` menulis ke inode bersama (desain JN2-5/12), dengan pesan eksplisit ke pengguna.
- Windows (`fsys_windows.go`, `dir_other.go`) hanya dianalisis statis.

### Determinisme test baru (risiko CI)

- `TestUndoRelativeSymlinkToDeniedDir`, `TestUndoRootReplacedBySymlink`, dan `TestUndoAncestorSwapStaysInSandbox` (journal), serta `TestUndoDryRun*` (cmd): masing-masing dibatasi deadline 2 s dengan satu swapper berurutan. Error swapper menghentikan test dan reset dilakukan lewat handle `os.Root`. Assert hanya berbentuk "victim tidak berubah/terbaca", sehingga test **tidak dapat gagal palsu** pada kode yang benar. `TestUndoMoveCrossRootKeepsSource`, `TestPinRoot`, `TestPinRootMissing`, dan `TestRootOf` deterministik.
- Stress `-race -count=10`: journal (4 test race/move) ok dalam 61,2 s, `cmd` `TestUndoDryRun.*` ok dalam 60,1 s, sandbox `TestPinRoot|TestRootOf` ok dalam 1,1 s. **Risiko flake: rendah.** Biaya CI sekitar 10 s ditambah satu core sibuk.

**Overall: APPROVE** untuk ketujuh PR (#180, #181, #170, #177, #171, #173, #174). SEC5-16b (MINOR) dan SEC5-18 (nit) dapat diperbaiki sebagai follow-up hardening.

---

## Round 5 — 2026-10-21 (follow-up #183)

**Verdict #183: REQUEST CHANGES.** SEC5-16b dan SEC5-18 FIXED, dan realignment LOC benar. Namun berjalan dari fd root yang dipin dengan `openat` mentah **menghilangkan pemeriksaan ulang `deny_paths`** yang sebelumnya dilakukan `OpenResolved`/`OpenDir`/`OpenApproved` (identitas handle dan path). Dua regresi terkonfirmasi: undo `--force` lewat hard link menulis ke **file ter-deny tanpa race**, dan rename direktori ter-deny literal ke path parent sekarang tembus (**SEC5-19, HIGH**). Stack #180…#174 tetap **APPROVE** (round 4). #183 jangan di-merge sampai SEC5-19 diperbaiki.

Head yang direview: #183 `feat/f2-undo-pinned-root` `18e510a` (basis `feat/f2-wire-modules` `4220f05`, satu commit). Gate: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/sandbox/... ./internal/archtest/...` → **ok** (36,8 s / 9,1 s / 1,4 s / 3,6 s). `TestUndoRootReplacedByRealDir`, `TestUndoRootReplacedBySymlink`, dan `TestUndoRelativeSymlinkToDeniedDir` lulus 5× dalam `-race -count=5` (masing-masing 2,00 s, deadline tetap).

Cross-build: `GOOS=windows|darwin|freebsd go vet` + `go build -tags goolm ./cmd/damnbot` + `go test -c ./internal/journal` → **ok**. `make loc`: `journal 607 / 610`. Probe `TestZZR5*` dijalankan pada `18e510a` dan pembanding `4220f05`, lalu dihapus.

### Probe (Linux, openat2)

| Vektor | `4220f05` (round 4) | `18e510a` (#183) |
|---|---|---|
| A. Ancestor → symlink ke luar root | tidak tembus (4.554) | **tidak tembus** (4.201) |
| B. Ancestor → symlink relatif ke dir ter-deny (literal / glob) | tidak tembus | **tidak tembus** (4.271 / 4.039) |
| C. Root → symlink | tidak tembus (4.597) | **tidak tembus** (3.416) |
| **F. Root diganti direktori nyata (rename)** | **tembus setelah 333 undo** | **tidak tembus** (3.350) → SEC5-16b FIXED |
| G. Move lintas root `--force` | ditolak, src utuh | **ditolak** ("move crosses sandbox roots"), src `SRC-NOW` utuh |
| **H. Rename dir ter-deny literal ke path parent** | **tidak tembus** (4.148, identitas dir dicek `OpenDir`) | **TEMBUS setelah 4 undo** → SEC5-19 |
| H'. Seperti H, deny glob | tembus setelah 709 (residual pra-ada) | tembus setelah 1 (residual yang sama) |
| **I. Hard link di workspace → file ter-deny literal, `undo --force`** | **ditolak** (`conflict … sandbox: denied_path`), file utuh | **`undone`, file ter-deny berisi `PWNED`** (deterministik, tanpa race) → SEC5-19 |
| J. FIFO di path aksi | ditolak oleh `Check` | ditolak oleh `Check` (tidak hang) |
| K. Kebocoran fd (300 undo, kedalaman 3) | — | **16 → 16** fd |

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC5-16b | **FIXED** | `parentDir` (`internal/journal/undo.go:264`) berjalan dari fd `PinRoot` dengan `Dir.OpenChild` (`openat(O_RDONLY|O_DIRECTORY|O_NOFOLLOW|O_CLOEXEC)`, `internal/sandbox/dir_unix.go:61`) per komponen. Handle antara ditutup, dan untuk `"."` dibuka descriptor baru sehingga root yang dipin tidak ikut tertutup. Operasi `openChild`/`unlinkat`/`renameat`/`fsync`/`fchmod` semuanya relatif ke fd induk. Probe F. `TestUndoRootReplacedByRealDir`. |
| SEC5-18 | **FIXED** | Temp dibuat dengan `openChild(h, tmp, O_RDWR|O_CREATE|O_EXCL)`, lalu `f.Chmod(mode)` + `Sync` **sebelum** `renameat`. `makeReplaceable`/`chmodChild` memakai handle induk, dan tidak ada lagi `chmodFile` berbasis path sesudah rename. |
| SEC5-15 | **REGRESI (lihat SEC5-19)** | Untuk symlink (probe B) tetap aman, tetapi pemeriksaan `deny_paths` via identitas handle hilang. |

### Temuan baru

| ID | Severity | Status | Temuan | Bukti / perbaikan |
|---|---|---|---|---|
| SEC5-19 | **HIGH** | confirmed | **`deny_paths` tidak lagi dicek ulang pada handle.** `current` (`undo.go:321`) dan `writeInPlace` (`:437`) membuka file lewat `openChild` (`fsys_unix.go:22`, `openat` mentah), dan `Dir.OpenChild` membuka direktori tanpa `vet`/`sameDenyID`, tanpa cek `denied(path)`, dan tanpa cek hard link. Sebelumnya `OpenResolved` (current/chmod), `OpenApproved` (writeInPlace), dan `OpenDir` (parent) membandingkan identitas handle dengan `denyIDs` (S16-2) dan path handle dengan pola deny. (1) **Hard link ke file ter-deny literal** (mis. `<config_dir>/secrets.enc`, atau file apa pun yang dideny secara literal): `Check` menilai path link sebagai `HardLinked` (bukan denied), `current` membaca dan meng-hash isi file ter-deny, dan dengan `--force` (yang disarankan oleh pesan "hard link; use --force") `writeInPlace` **menimpa file ter-deny secara deterministik**. (2) **Rename direktori ter-deny literal** ke path parent aksi: identitas direktori yang sebelumnya ditolak `OpenDir` kini lolos lewat `OpenChild`. | Probe I: `4220f05` → `conflict … denied_path`, sedangkan `18e510a` → `undone`, file ter-deny = `PWNED`. Probe H: tidak tembus (4.148) → tembus setelah 4 undo. Perbaikan: `Dir.OpenChild` dan `openChild` harus menjalankan vetting yang sama dengan `Dir.Open`/`vet`: `sameDenyID` pada `fstat` setiap handle (direktori dan file), `denied(d.child(name))` pada path yang dibangun, dan `regularLinks`/`Nlink > 1` → `HardLinked` untuk file kecuali disetujui. `writeInPlace` hanya boleh menulis bila pemeriksaan deny tersebut lolos. Tambahkan test regresi I dan H (literal) di `internal/journal`. |

### Review fd-walk

- **Kebocoran fd:** jalur sukses dan error di `parentDir` menutup handle antara. `current`/`write`/`chmod`/`remove`/`rename` menutup `h` lewat `defer`. `openChild` menutup fd saat `SetNonblock` gagal. Probe K: 16 → 16. Tidak ada kebocoran.
- **EINTR:** `unix.Openat`/`Unlinkat`/`Renameat` tidak di-retry. Pada direktori dan file reguler yang dibuka `O_NONBLOCK`, EINTR praktis tidak terjadi; FIFO ditolak lebih dulu oleh `Check`. Nit: bila terjadi, hasilnya error, bukan operasi yang salah.
- **Tipe file:** `openChild` tidak memvalidasi tipe (berbeda dengan `newFile`). `current` tetap memeriksa `IsRegular` sesudah `Stat`, dan FIFO/device ditolak oleh `Check` (probe J).
- **Windows (`fsys_windows.go`):** `openChild` = `os.OpenRoot(h.File().Name())` + `OpenFile`, sehingga berbasis path, tidak relatif fd, dan tanpa deny re-check. `Dir.OpenChild` (`dir_other.go`) membuka ulang lewat `OpenDir` (dengan deny). Build dan vet Windows ok. Statusnya residual terdokumentasi (TRUST-MODEL), tetapi perbaikan SEC5-19 juga harus menambahkan deny re-check di jalur Windows.

### LOC

- `loc.budget`: `journal 560 → 610` dengan komentar amandemen ADR-117 (round 4, terukur 607). ADR-117 di `DECISIONS.md` memuat amandemen yang sama.
- ENGINEERING §2.4: baris `tools` 1.800 → 2.200 adalah **realignment yang benar**. Nilai 2.200 sudah ada di `loc.budget` `main` (komentar `ADR-119: tools 1800 -> 2200`), dan ADR-119 butir 9 menetapkannya. Baris `journal` 500 → 610 juga menyelaraskan nilai lama (main: tabel 500 vs `loc.budget` 560).
- Jumlah baris core `loc.budget` = **36.570** (basis 36.520). Kedua angka Total core di ENGINEERING (sel tabel dan prosa) = **36.570**. Jumlah sel tabel setelah realignment = **36.570** (sebelumnya 36.060, selisih 460 dengan Total pada `main`). `TestBudgetTableSum` lulus.

**Overall:** stack #180–#174 **APPROVE** (tidak berubah). #183 **REQUEST CHANGES** (SEC5-19 HIGH). SEC5-16b/18 baru dianggap tertutup setelah #183 di-merge bersama perbaikan SEC5-19.

---

## Round 6 — 2026-10-21 (#183 head `d98d964`)

**Verdict #183: APPROVE.** SEC5-19 FIXED: kedua regresi round 5 tertutup (hard link ke file ter-deny dan rename direktori ter-deny literal). SEC5-16b dan SEC5-18 tetap FIXED, dan semua probe sebelumnya kembali ke hasil aman. Terbuka: dua MINOR (SEC5-20 pemeriksaan path saat `/proc` tidak tersedia, SEC5-21 biaya readlink pada `Dir.Open`) dan residual pra-ada H' (deny glob + rename direktori).

Head yang direview: #183 `feat/f2-undo-pinned-root` `d98d964` (satu commit di atas `4220f05`, menggantikan `18e510a`). Gate: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/sandbox/... ./internal/codetools/... ./internal/tools/... ./internal/archtest/...` → **semua ok**. `TestUndoHardlinkToDeniedFileRefused`, `TestUndoDeniedDirRenamedIntoParentRefused`, dan `TestUndoRootReplacedByRealDir` lulus 5× dalam `-race -count=5`.

Cross-build: `GOOS=windows|darwin|freebsd go vet ./internal/sandbox ./internal/journal` + `go build -tags goolm ./cmd/damnbot` → **ok**. `make loc`: `journal 609 / 610`, `sandbox+redact 1684 / 1700` (komentar `loc.budget` diperbarui menjadi terukur 609; Σ core tetap 36.570). Probe `TestZZR6*` dijalankan lalu dihapus.

### Perubahan yang direview

- `sandbox.Dir.openVetted` (`internal/sandbox/dir_unix.go:56`) memanggil `openatEINTR(dfd, name, flag|O_NOFOLLOW|O_CLOEXEC|O_NONBLOCK)` → `newFile` (tipe: regular/dir saja, FIFO/device ditolak) → **`vet(f, nil, Inside, byPath=true, single=false)`**. Artinya deny diperiksa pada path handle (`readlink /proc/self/fd/N`) dan pada identitas (`os.SameFile` terhadap `denyIDs`).
- `Dir.OpenFile` (`:35`) dan `Dir.OpenChild` (`:45`, `O_DIRECTORY`) memakai `openVetted`. `Dir.Open` (`:16`) = `OpenFile` + `linkCountFile` (`HardLinked` untuk approval). Helper `sameDenyID` dihapus.
- `internal/journal`: `current`/`write`/`chmodChild`/`writeInPlace` memakai `h.OpenFile`, sedangkan `unlinkat`/`renameat`/`fsync` dibungkus retry EINTR. `openChild` di `fsys_unix.go`/`fsys_windows.go` dihapus.
- Windows (`dir_other.go:26`): `OpenFile` = `os.OpenRoot(d.path)` + `OpenFile` + `vet(byPath=true)`. Path handle diambil dari `GetFinalPathNameByHandle` (`open_windows.go` `finalPath` → `normalizeWindows`), dan identitas dicek lewat `os.SameFile`. `OpenChild` = `OpenDir` (deny dan symlink dicek). Symlink yang tetap di dalam root masih diikuti di Windows (residual statis yang terdokumentasi).

### Probe (Linux, openat2)

| Vektor | `18e510a` (round 5) | `d98d964` |
|---|---|---|
| A. Ancestor → symlink ke luar root | tidak tembus | **tidak tembus** (4.196) |
| B. Ancestor → symlink relatif ke dir ter-deny (literal / glob) | tidak tembus | **tidak tembus** (4.420 / 4.159) |
| C. Root → symlink | tidak tembus | **tidak tembus** (3.448) |
| F. Root diganti direktori nyata | tidak tembus | **tidak tembus** (3.433) |
| **H. Rename dir ter-deny literal ke path parent** | **tembus setelah 4** | **tidak tembus** (3.569) ✔ |
| H'. Seperti H, deny glob | tembus (residual) | tembus setelah 405 (residual pra-ada yang sama; lihat bawah) |
| **I. Hard link → file ter-deny literal, `--force`** | **`undone`, file = `PWNED`** | **`conflict … sandbox: denied_path`**, file tetap `new` ✔ |
| J. FIFO di path aksi | ditolak `Check` | ditolak `Check` (tidak hang) |
| G. Move lintas root `--force` | ditolak, src utuh | **ditolak**, src `SRC-NOW` utuh |
| K. fd (300 undo, kedalaman 3) | 16 → 16 | **16 → 16** |
| Happy path (undo biasa di `a/b/f`, deny `**/.env`) | — | `undone`, isi dipulihkan ✔ |

### Verdict per temuan

| ID | Verdict | Bukti |
|---|---|---|
| SEC5-19 | **FIXED** | Probe H dan I di atas. `TestUndoHardlinkToDeniedFileRefused`, `TestUndoDeniedDirRenamedIntoParentRefused`. Identitas dicek pada **setiap** handle walk (direktori dan file), sehingga direktori ter-deny literal yang di-rename ke path parent ditolak di `OpenChild`, dan hard link ke file ter-deny ditolak di `OpenFile`. |
| SEC5-16b / SEC5-18 | **FIXED** (tidak berubah) | Probe F; temp dan chmod dilakukan lewat handle induk. |
| EINTR | **FIXED** | `openatEINTR` (`dir_unix.go:75`), `unlinkatEINTR`, `renameatEINTR`, dan `fsyncEINTR` me-retry hanya pada `EINTR`. |

### `/proc` tidak tersedia dan Windows

- **Linux tanpa `/proc` (container tanpa mount `/proc`, chroot):** `vet` dengan `byPath=true` memanggil `handlePath`. Bila `readlink` gagal, `ok = false` dan **pemeriksaan path dilewati tanpa error**, sehingga hanya cek identitas (`denyIDs`, pola literal) yang tersisa. Ini bukan regresi terhadap `4220f05`: `OpenResolved` untuk path `Inside` memakai `byPath=false` dan mengandalkan cek leksikal; walk tanpa symlink membuat path handle sama dengan path leksikal kecuali ada rename (H'). Namun perilakunya juga **tidak gagal-tertutup**, berbeda dengan `verifyProcFd` di jalur fallback openat2 yang gagal-tertutup tanpa `/proc`. → SEC5-20 (MINOR). `hidepid` tidak relevan karena `/proc/self` selalu terlihat oleh proses sendiri. Simulasi `/proc` tanpa root tidak memungkinkan di host review (`unshare -rm`: `uid_map` EPERM), sehingga bagian ini dianalisis statis.
- **Windows:** `handlePath` = `GetFinalPathNameByHandle`. Bila gagal, `ok = false` dan perilakunya sama (path dilewati, identitas tetap dicek). `finalPath` menangani buffer yang terlalu kecil (`size = n + 1`). Build dan vet Windows ok, tetapi tidak diuji di runtime Windows.

### Temuan terbuka

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| SEC5-20 | MINOR | static | `vet(byPath=true)` melewati cek deny berbasis path bila path handle tidak dapat dibaca (`/proc` tidak ada, atau `GetFinalPathNameByHandle` gagal), tanpa fallback dan tanpa error. | Dalam `openVetted`, gunakan path leksikal `d.child(name)` sebagai fallback untuk `s.denied(...)` saat `handlePath` gagal, atau gagal-tertutup seperti `verifyProcFd`. Tambahkan test dengan hook `handlePath` yang gagal. |
| SEC5-21 | MINOR (perf) | measured | `Dir.Open` (dipakai walker grep/glob codetools, CT-16) kini memanggil `vet(byPath=true)`, yaitu satu `readlink /proc/self/fd/N` + alokasi string per file, padahal sebelumnya hanya `fstat` + identitas tanpa syscall tambahan. `BenchmarkCodetools/GrepToolC3` (10×3, host berisik): `18e510a` 17,6 / 16,1 / 14,6 ms → `d98d964` 19,3 / 18,4 / 17,3 ms (≈ +14 %, budget CT-04 p95 ≤ 15 ms). | Ukur dengan `make perf` (≥ 100 sampel). Untuk walker yang sudah berada di dalam direktori yang tervalidasi, cek path leksikal `d.child(name)` cukup, dan `readlink` hanya diperlukan pada jalur undo. |

### Residual (tidak berubah)

- **H' (deny glob + rename direktori):** pola glob tidak punya identitas, dan path handle (`readlink`) menunjukkan nama *saat ini* (`ws/sub/…`) yang tidak cocok dengan glob. Ini sifat pra-ada dari `deny_paths` berbasis path, dan berlaku untuk semua tool file tanpa race (round 4).
- **Windows:** symlink yang tetap di dalam root diikuti oleh `os.Root` (sudah terdokumentasi).

**Overall:** stack #180–#174 **APPROVE**; #183 (`d98d964`) **APPROVE** dengan SEC5-20/21 MINOR sebagai follow-up.

---

## Round 7 — 2026-10-21 (#183 head `58d5dc8`, konfirmasi singkat)

**Verdict #183: APPROVE.** SEC5-20 dan SEC5-21 FIXED. Semua probe sebelumnya tetap aman, dan vetting `Dir.Open` untuk walker tidak lebih lemah daripada `main` (bahkan lebih ketat). Tidak ada temuan baru.

Head yang direview: #183 `58d5dc8` (satu commit di atas `4220f05`, menggantikan `d98d964`). Gate: `go test -race -tags goolm -count=1 ./cmd/damnbot/... ./internal/journal/... ./internal/sandbox/... ./internal/codetools/... ./internal/tools/... ./internal/archtest/...` → **semua ok**. `TestOpenFileDenyPathFallback` lulus 3×. Cross-build `windows|darwin|freebsd` (vet `sandbox`/`journal` + build `cmd/damnbot`) → ok. `make loc`: `sandbox+redact 1700 / 1700`, `journal 609 / 610`. Probe `TestZZR7*` dan `TestZZWalkerOpen` dijalankan lalu dihapus.

### Perubahan

- `Dir.vetHandle` (`internal/sandbox/dir.go`): cek identitas (`vet`, `byPath=false`), lalu cek path. Dengan `byPath`, yang dipakai adalah path handle (`handlePath`, atau seam test `Sandbox.handlePath`), dengan **fallback ke path leksikal `d.child(name)`** bila OS tidak dapat melaporkannya. Tanpa `byPath`, yang dicek hanya path leksikal. → **SEC5-20 FIXED**: `/proc` yang tidak ada, atau `GetFinalPathNameByHandle` yang gagal, kini tetap memeriksa path leksikal (`TestOpenFileDenyPathFallback`).
- `Dir.Open` (walker codetools) → `openVetted(..., byPath=false)`: identitas + leksikal, tanpa `readlink` per file. `OpenFile`/`OpenChild` (executor undo) tetap `byPath=true`. → **SEC5-21 FIXED** secara konstruksi. Benchmark `GrepToolC3` (10×3) terlalu berisik untuk membedakan (`d98d964` 18,6/15,3/14,2 ms vs `58d5dc8` 18,1/16,9/13,5 ms); verifikasi angka diserahkan ke `make perf`.
- `RootIdentity` di-inline ke `PinRoot` (pencarian root yang sama, lalu `os.SameFile` pada handle).

### Walker `Dir.Open`: `main` (`0128e6e`) vs `58d5dc8`

| Entri (root, deny = `<root>/secret.enc` literal + `**/*.pem`) | `main` | `58d5dc8` |
|---|---|---|
| `plain` | dibuka | dibuka |
| `secret.enc` (literal ter-deny) | `denied_path` | `denied_path` |
| `hl-denied` (hard link ke `secret.enc`) | `denied_path` (identitas) | `denied_path` (identitas) |
| `hl-plain` (hard link biasa) | `hardlinked_file` | `hardlinked_file` |
| `a.pem` (glob ter-deny) | **dibuka** (di `main`, `Dir.Open` mengandalkan cek path oleh pemanggil) | `denied_path` (cek leksikal baru) |
| `sym` (symlink) | `outside_workspace` (ELOOP) | `outside_workspace` |
| `fifo` | `not a regular file` | `not a regular file` |

Tidak ada kasus yang diblok `main` tetapi lolos di `58d5dc8`.

### Probe executor (ulang round 6)

| Vektor | `58d5dc8` |
|---|---|
| A. Ancestor → symlink ke luar root | tidak tembus (4.129) |
| B. Ancestor → symlink relatif ke dir ter-deny (literal / glob) | tidak tembus (4.351 / 4.155) |
| C. Root → symlink | tidak tembus (3.466) |
| F. Root diganti direktori nyata | tidak tembus (3.287) |
| H. Rename dir ter-deny literal ke path parent | tidak tembus (3.679) |
| H'. Seperti H, deny glob | tembus setelah 275 (residual pra-ada yang sama: path handle menunjukkan nama baru yang tidak cocok dengan glob) |
| I. Hard link → file ter-deny, `--force` | `conflict … denied_path`, file utuh |
| J. FIFO | ditolak (`failed`, tidak hang) |
| G. Move lintas root `--force` | `failed`, src utuh |
| K. fd (300 undo) / happy path | 37 → 37; `undone` |

**Overall:** stack #180–#174 **APPROVE**; #183 (`58d5dc8`) **APPROVE**. Temuan terbuka SEC5: hanya residual H' (deny glob + rename) dan Windows (symlink di dalam root diikuti `os.Root`), keduanya pra-ada dan terdokumentasi.
