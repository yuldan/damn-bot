# REVIEW-F2-SEC17 — Tool `memory`/`session_search` (#266) dan referensi konteks `@` (#267)

Review independen untuk #266 `74faf3f` (basis `f2-e2e`, ADR-147) dan #267 `7fd2165` (basis `f2-tools-mem`, ADR-148; stacked di atas #266). Kode produk tidak diubah. Aturan owner berlaku (e2e + kontrol negatif untuk batas keamanan). **Round ini PARTIAL**: owner menghentikan sesi saat review berjalan; semua temuan di bawah sudah diverifikasi dengan kode + probe, tetapi tidak ada bagian yang disimpulkan tanpa bukti. Tanggal: 2026-10-22.

## Round 1 — 2026-10-22 — **PARTIAL**

**Verdict:**

| PR | Head | Verdict |
|---|---|---|
| #266 memory + session_search | `74faf3f` | **REQUEST CHANGES (provisional)**: SEC17-1 (session_search lintas project), SEC17-2 (#211 exclusion tidak dihormati), SEC17-3 (agent me-retire memori user tanpa approval, audit `actor=user`), SEC17-4 (replace me-retire sebelum validasi) |
| #267 referensi `@` | `7fd2165` | **REQUEST CHANGES (provisional)**: SEC17-5 (konten ref tidak men-taint turn) ; SEC17-6/7/8 MINOR/INFO |

Gate lokal yang dijalankan: `go test -tags goolm -count=1 -run 'TestMemoryTool|TestSessionSearch|TestNewToolsSchema' ./cmd/damnbot ./internal/tools ./internal/e2e` → **ok**; `go test -run TestContextRef -v ./internal/e2e` → `TestContextRefFile` dan `TestContextRefOutsideWorkspace` **PASS**. Probe `TestZZSec266Probe` dan `TestZZSec267Refs` (`cmd/damnbot`, dihapus setelah dijalankan). CI kedua head tidak diperiksa (sesi dihentikan).

## #266 — `memory` dan `session_search`

### Yang benar

- **Karantina tulis agent:** `memoryToolBackend.Add/Replace` membangun `memory.Proposal{Origin: agent, EvidenceEventID: event tool_dispatch untuk CallID}`; `memory.Service.Propose` memaksa `StatusScanned` untuk origin agent dan `StatusRejected` bila scan `high`. E2E `TestMemoryToolEndToEnd` memverifikasi baris agent berstatus `scanned` (tidak pernah approved otomatis) ✔.
- **Bukti tidak dapat dipalsukan lewat argumen:** `CallID` berasal dari `Env.CallID` dispatcher, bukan dari argumen tool; event `tool_dispatch` dicari dengan `tool_call_id` itu ✔.
- **`read` hanya approved:** `Service.Recall` memakai `Statuses: [approved]`, sehingga tulisan agent yang masih `scanned` tidak terlihat pada run yang sama ✔.
- **Redaksi & scope:** konten diredaksi sebelum disimpan; scope `user` atau `project:<cwd>` (cwd dari dispatcher, bukan `cd` shell) ✔.
- **session_search:** hasil `UntrustedSource: "session"` (dispatcher membungkus `<untrusted source="session">` dan men-taint turn) ✔; `k` dibatasi 10 dan query 200 rune; kueri dinormalisasi/di-quote oleh `ctxdb` (tidak ada injeksi operator) ✔; sesi ber-`purge_state` difilter oleh `notPurging` ✔.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| **SEC17-1** | MEDIUM | confirmed (statis) | **`session_search` lintas project.** `ctxSearch.Search` memanggil `ctxdb.Search{Text, Limit}` tanpa filter sesi/cwd; `Search` tidak punya filter project. Model di workspace A dapat menarik snippet transkrip workspace B (termasuk isi file/secret yang pernah muncul), melewati batas sandbox untuk konten lama. ADR-147 mendokumentasikannya sebagai keterbatasan F2 ("belum difilter per workspace"), tetapi instruksi review owner meminta scope project dihormati. | Tambah filter project/cwd pada `ctxdb.Query` (mis. `sessions.cwd`), atau batasi tool ke sesi saat ini secara default; e2e dua workspace: kueri dari A tidak mengembalikan hit dari B. Bila owner menerima keterbatasan ADR-147, catat sebagai accepted risk eksplisit. |
| **SEC17-2** | MEDIUM | confirmed (probe) | **Exclusion #211 tidak dihormati.** `searchFTS` menghitung kolom `excluded` (turn dengan event `context_exclude`), tetapi `ctxdb.Search` tidak memfilternya dan `ctxSearch` membuang flag itu (`tools.SessionHit` bahkan tidak punya field `Excluded`). Probe: event `assistant_message` di turn `T1` + marker `context_exclude` → `ctxdb.Search` mengembalikan hit `excluded=true`, dan `ctxSearch.Search` mengembalikan snippet `SECRET-EXCLUDED-NEEDLE` apa adanya. Jadi turn yang sengaja dikeluarkan #211 dari request berikutnya dapat masuk kembali lewat tool. | Lewati hit `Excluded` (atau tandai eksplisit dan jangan kirim snippet-nya); tambah test: turn ber-marker `context_exclude` tidak muncul di `session_search`. |
| **SEC17-3** | MEDIUM | confirmed (probe) | **Agent dapat me-retire memori user yang sudah approved tanpa approval, dan audit mencatat `actor=user`.** `memoryToolBackend.Remove` (dan `Replace`) memanggil `Service.Retire` → `decide` dengan `ActorUser`; `memstore.decide` hanya memeriksa scope dan transisi (`approved→retired` diizinkan), tanpa memeriksa origin/pinned. Tool `memory` ber-efek `EffectNonIdempotent` tanpa Inspector → **auto** di `auto`/`auto-edit`. Probe: `Propose` user-origin → `approved`; `memoryToolBackend.Remove` → `status=retired`, keputusan terakhir `actor=user`. Model dapat menghapus memori global user (termasuk pinned) tanpa persetujuan dan menghapus jejak siapa pelakunya. | Batasi Retire untuk memori origin agent dan/atau wajibkan approval untuk origin user/pinned; catat `ActorAgent`; tambah kontrol negatif: `remove` atas memori user di mode `auto` harus ditolak/ask dan status tetap `approved`. |
| SEC17-4 | MINOR | confirmed (statis) | `Replace` me-retire id lama **sebelum** proposal baru divalidasi/discan. Bila proposal baru ditolak (scan `high`) atau gagal (mis. scope/ukuran), memori lama sudah hilang. Pola yang sama dengan ACC-56 pada skill. | Validasi/scan proposal baru lebih dulu (atau satu transaksi: insert baru lalu retire lama), sehingga kegagalan tidak menghapus yang lama. |

## #267 — referensi konteks `@`

### Yang benar (probe)

Sandbox yang dipakai adalah sandbox run yang sama dengan tool (roots + `deny_paths`), dan `readRefPath` memakai `Check` (deny pada path dan hasil resolve, prefix root, deteksi hardlink) lalu `OpenResolved` (menolak symlink pada path dan memverifikasi handle). Probe dengan `Roots=[workspace]`, `Home`, `DenyPaths=["**/.env","~/.ssh"]`:

| Ref | Hasil |
|---|---|
| `@notes.txt`, `@link-in` (symlink ke file di dalam root) | dibaca, dibungkus `<untrusted source="ref:…">` ✔ |
| `@link-out` (symlink ke luar root) | ditolak "path is outside the workspace" ✔ |
| `@hard.txt` (hardlink, nlink>1), `@envlink.txt` (hardlink ke `.env`) | ditolak ✔ |
| `@/etc/passwd`, `@link-out/x` | ditolak ✔ |
| `@~/secret.txt`, `@~/.ssh/id_rsa` | `~` tidak diekspansi; dicari sebagai direktori literal di cwd → ENOENT; tidak ada pembacaan home ✔ |
| `@.env`, `@sshdir/id_rsa` (symlink ke `~/.ssh`) | `denied_path` ✔ |
| `@.` (direktori) | listing dibungkus `<untrusted>` ✔ |

`maxRefs=8`, `maxRefTotal=64 KiB`, satu file ≤32 KiB; `@url` memakai `netx` purpose `Tool` (DLP + anti-SSRF: hanya alamat publik) dan `--offline` menolak. E2E `TestContextRefFile` (positif, biner nyata) dan `TestContextRefOutsideWorkspace` (`@../` → exit 2, **0 request provider**) ada.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan |
|---|---|---|---|---|
| **SEC17-5** | MEDIUM | confirmed (statis) | **Konten ref tidak men-taint turn.** `expandRefs` menyisipkan konten `<untrusted source="ref:…">` ke prompt setelah `disp.BeginTurn()`, tetapi `Dispatcher.MarkTainted()` hanya dipanggil di test — `d.tainted` tetap `false`. Akibatnya di `--unsafe-auto-shell` tool non-read tetap auto-approve meskipun prompt memuat konten file/URL yang oleh kode sendiri ditandai untrusted (ADR-070). Untuk `@url` ini konten jaringan yang tidak dipercaya. | Panggil `disp.MarkTainted()` bila ada ref yang diekspansi (minimal untuk `@url`); e2e: `@file` berisi instruksi + panggilan shell mutating di unsafe → harus ditolak karena tainted. |
| SEC17-6 | MINOR | open | Test refs hanya menutup `@../`; tidak ada test untuk symlink keluar, `deny_paths`, hardlink, path absolut, `~`. Perilakunya benar (probe di atas), tetapi properti keamanan ini tidak terkunci. | Tambahkan kasus probe di atas ke `refs_test.go` (dan satu ke e2e) sebagai kontrol negatif. |
| SEC17-7 | MINOR | open | `strings.Replace(prompt, f, expanded, 1)` mengganti kemunculan **pertama**; bila isi ref memuat token yang sama dengan field berikutnya, penggantian kedua dapat masuk ke dalam blok yang baru disisipkan dan token user yang asli tidak diekspansi. Semua blok tetap dibungkus (bukan breakout), tetapi asosiasi sumber menyesatkan. | Bangun output dengan menelusuri field prompt, bukan `strings.Replace`. |
| SEC17-8 | INFO | — | `maxRefBytes` (dinamai bytes) diterapkan pada `[]rune`; `io.LimitReader` sudah membatasi 32 KiB sehingga tidak ada overread. `refSource` memotong atribut pada 120 byte dan dapat memotong rune. | Kosmetik. |
| — | INFO | — | Ref di luar workspace **tidak** dapat di-approve seperti `read_file` (langsung gagal). Ini lebih ketat dan sesuai tujuan keamanan; catat sebagai perbedaan perilaku yang disengaja. | — |

**Ringkasan Round 1 (PARTIAL):** mekanisme #267 benar untuk root/deny_paths/symlink/hardlink (probe tidak menemukan kebocoran), tetapi konten ref tidak men-taint turn sehingga nilai keamanan pembungkusan `<untrusted>` tidak ditegakkan oleh kebijakan approval (SEC17-5). Untuk #266, karantina tulis agent benar, tetapi `session_search` melintasi project (SEC17-1) dan mengembalikan turn yang di-exclude #211 (SEC17-2), dan jalur `remove`/`replace` me-retire memori user tanpa approval dengan audit `actor=user` (SEC17-3). Kedua PR **REQUEST CHANGES (provisional)** sampai owner memutuskan SEC17-1 (keterbatasan ADR-147) dan memperbaiki SEC17-2/3/5.

## Round 2 — 2026-10-22 — fixes verified (SEC17-1..8, ACC-62..74)

Round ini reviewer juga mengerjakan perbaikan (owner meminta fix, bukan hanya review). Setiap push memakai `--force-with-lease`; tidak ada gate yang dilonggarkan dan tidak ada tes yang di-skip.

**Verdict.**

| PR | Head round 2 | Verdict |
|---|---|---|
| #264 checkpoints CLI | `211f626` → **`f4f6d6e`** (push; rebase ke `f2-sessions-prune` `a967057`) | **ACC-62/ACC-63 tertutup**; SEC15-9 tertutup (e2e biner: busy → exit 4, marker runner → exit 2, dengan kontrol negatif); SEC15-10 dicatat sebagai pekerjaan integrasi (ADR-146 butir 13). **E-84: 668 baris Go non-test > 600** (`pr-meta` GAGAL; bahkan head pra-fix sudah 604) → butuh label `large-change` + 2 approver atau split ctxdb/CLI. |
| #266 memory + session_search | `74faf3f` → **`b3a529b`** (rebase `--onto main`; base PR diubah ke `main`) | **SEC17-1..4 tertutup** dengan kontrol negatif (unit + e2e biner); ACC-67..69 tidak ada defect terbukti. |
| #267 `@`-references | `7fd2165` → **`2648f81`** (push; rebase ke `f2-tools-mem` baru belum dilakukan) | **SEC17-5 tertutup** (diverifikasi ulang reviewer di bawah); SEC17-6/7/8 tertutup dan dikunci tes; ACC-70/71 terkunci. |
| #268 nudge + cache breakpoint | `84aefec` → **`7a5b807`** (push) | **ACC-72 defect nyata** (nudge turn ke-11) dan **ACC-73 defect nyata** (MEM-034 inert) diperbaiki; ACC-74 terkunci byte-exact. |

### Bukti per item (file:baris)

| ID | Bukti | Status |
|---|---|---|
| SEC17-1 | `internal/ctxdb/read.go:19,363,382-385` (`Query.Cwd` → `session_id IN (SELECT id FROM sessions WHERE cwd = ?)` pada query events **dan** summaries); `internal/tools/session_search.go:20-24,58-60` (seam `cwd`, `Env.Cwd == ""` → `invalid_args`); `cmd/damnbot/memory_wire.go:237-241` (adapter menolak cwd kosong) | tertutup |
| SEC17-2 | `cmd/damnbot/memory_wire.go:247` (`if hits[i].Excluded { continue }`); `ctxdb.Search` tetap mengembalikan hit ter-flag (kontrak `SEC11-5`/`Recall` dipertahankan, didokumentasikan) | tertutup |
| SEC17-3 | `internal/memory/service.go:122-131` (`RetireByAgent`, aktor `system`, reason `retired:agent`); `internal/ctxdb/memstore/memstore.go:45-47,193-197` (`ErrNotAgent`: retire ber-aktor `system` hanya untuk baris `origin=agent`); `internal/tools/memory.go:16,181-182` (`ErrMemoryDenied` → kode `denied`) | tertutup |
| SEC17-4 | `cmd/damnbot/memory_wire.go:165-199` (`Propose` dulu; `rejected` → tidak retire; pra-cek baca-saja `Service.Get`) | tertutup |
| SEC17-5 | `cmd/damnbot/refs.go:73-90` (`expandPromptRefs` → `disp.MarkTainted()` bila `refs`), dipanggil `cmd/damnbot/run.go:238` sebelum `loop.Prompt` | tertutup |
| SEC17-6 | `cmd/damnbot/refs_test.go` `TestExpandRefsSandboxBoundaries`; `internal/e2e/refs_test.go` `TestContextRefSandboxBoundaries` (symlink keluar, `deny_paths`, hardlink, absolut, `~` → exit 2, 0 request provider; kontrol positif file/symlink dalam root) | tertutup |
| SEC17-7 | `cmd/damnbot/refs.go:90-120` (`refTokens` menelusuri field prompt, bukan `strings.Replace`); `refs_test.go` `TestExpandRefsTokenAssociation` | tertutup |
| SEC17-8 | `cmd/damnbot/refs.go:16-24` (`maxRefRunes`), `:122-140` (`refSource` memotong pada batas rune); `TestRefSourceBounded` | tertutup |
| ACC-62/63 | lihat verifikasi reviewer di bawah | tertutup |

### Verifikasi independen reviewer (bukan klaim subagen)

```
# ACC-63/ACC-62 pada biner PRA-fix 211f626 (seed 3 aksi / 2 sesi, ~/.cache/damnbot/negdata)
$ old-damnbot checkpoints status --session S1 --data-dir $D   -> actions: 3      # filter diabaikan
$ old-damnbot checkpoints clear --dry-run --yes --data-dir $D -> cleared 3 checkpoints
$ old-damnbot checkpoints status --data-dir $D                -> actions: 0      # MENGHAPUS
# biner sesudah fix (f4f6d6e), dir yang sama:
$ new-damnbot checkpoints status --session S1 --data-dir $D   -> actions: 2
$ new-damnbot checkpoints clear --dry-run --yes --data-dir $D -> would clear 3 checkpoints (exit 0)
$ new-damnbot checkpoints status --data-dir $D                -> actions: 3      # utuh
$ new-damnbot checkpoints clear --session S1 --yes --data-dir $D -> exit 2 "--session is not supported by this subcommand"
# dir read-only (0500): status/list/dry-run jalan; `clear --yes` exit 1 (fail closed), `status` tetap actions: 3

# SEC17-5 kontrol negatif (menonaktifkan taint di ps-267, lalu dipulihkan):
$ sed -i 's/^\tif refs {$/\tif false && refs {/' cmd/damnbot/refs.go
$ go test -tags goolm -count=1 -run 'TestContextRefTaintsShell|TestContextRefControlShellRuns' -v ./internal/e2e
--- PASS: TestContextRefControlShellRuns
    refs_test.go:175: a tainted turn auto-approved a mutating shell command
--- FAIL: TestContextRefTaintsShell
```
Gate terfokus (tail as-is):
```
#264  go test -tags goolm -count=1 ./internal/ctxdb ./cmd/damnbot ./internal/e2e ./internal/archtest
      ok internal/ctxdb 46.866s | ok cmd/damnbot 5.177s | ok internal/e2e 3.353s | ok internal/archtest 0.906s
      scripts/loc-check.sh budget -> LOC ok ctxdb 4216/4250 (tanpa baris GAGAL); scripts/pr-meta.sh size -> 668 (GAGAL)
#266  go test -tags goolm -count=1 ./internal/tools ./cmd/damnbot ./internal/e2e   -> ok 0.243s / 3.040s / 3.456s
      pr-meta size 584 ok; loc: tools 2403/2500, ctxdb 3794/3800
#267  go test -tags goolm -count=1 -run TestContextRef -v ./internal/e2e  -> 10 tes PASS (termasuk taint + kontrol)
#268  go test -tags goolm -count=1 ./cmd/damnbot ./internal/provider ./internal/loop ./internal/e2e -> ok; bench/budget ok
      loc: provider 2097/2100
```

### Pemetaan ACC (turunan — **tidak ada definisi formal** ACC-62..74 di repo, seluruh ref, komentar/review GitHub, maupun arsip sesi)

| id | perilaku yang diasumsikan | sumber turunan | hasil |
|---|---|---|---|
| ACC-62 | `checkpoints --session` benar-benar menyaring | deskripsi tiket + kode (`status` mem-parse flag tanpa memakai) | defect → diperbaiki |
| ACC-63 | `clear --dry-run` tidak pernah menghapus | deskripsi tiket + kontrak `prune --dry-run` | defect → diperbaiki |
| ACC-64..66 | perilaku CLI `checkpoints` lain (limit, kolom list, read-only, exit code, `clear-legacy`) | body PR #264 | tidak ada defect terbukti pada probe; satu cacat kosmetik diperbaiki (prefiks error `sessions checkpoints …` → `checkpoints …`) |
| ACC-67..69 | clamp k/query, wrapper untrusted, error map, `read` hanya approved, fail-closed CallID, schema | body PR #266 | tidak ada defect terbukti (bukti per item di laporan #266) |
| ACC-70/71 | batas 8 ref/32 KiB/64 KiB, exit 2 pada escape/offline, netralisasi tag, listing `@.` | body PR #267 | terverifikasi & terkunci tes |
| ACC-72 | nudge pada turn ke-10 (paritas hermes `config_defaults.py:1321`, interval 10) | body PR #268 + paritas MEM-003 | **defect** (turn ke-11) → diperbaiki `543a165` |
| ACC-73 | cache breakpoint sampai ke wire untuk route Anthropic | body PR #268 (`Family == "anthropic"`) | **defect** (`Route.Family` = base URL ternormalisasi, SEC8-9 → gerbang tidak pernah benar) → diperbaiki `7a5b807` (vendor dibaca dari host) |
| ACC-74 | famili lain dan `CacheBreakAfter=0` byte-identik | body PR #268 | terverifikasi & terkunci (`TestCacheBreakStableBody`, `TestCacheBreakpointGenericWire`) |

Item yang **tidak** dapat diverifikasi tanpa definisi formal: ACC-64..66, ACC-67..69 (dinilai lulus terhadap perilaku yang didokumentasikan di body PR, bukan terhadap teks kriteria owner).

### Budget `internal/tools` (#266 vs #263)

`scripts/loc-check.sh budget`: `main` `affe2d8` 2168/2200 · #266 `b3a529b` 2403/2500 · #263 `50816f9` 2315/2500 → delta aditif ≈ **2550 > 2500**: PR yang **merge terakhir** di antara keduanya wajib menaikkan `tools` 2500 → **2600** (margin ≈50) plus komentar `loc.budget` + amandemen ADR-nya; tanpa itu gate `loc-budget` gagal setelah keduanya mendarat. `ctxdb` #266 3794/3800 (margin 6 LOC — diawasi).

### Sisa pekerjaan / risiko

1. **#264 melebihi E-84** (668 baris Go non-test) — keputusan owner: label `large-change` + 2 approver, atau split (API maintenance `ctxdb` sebagai PR stacked, CLI di atasnya).
2. **#267 belum di-rebase** ke `f2-tools-mem` baru: `git rebase --onto b3a529b 74faf3f f2-refs-trim` (konflik `docs/DECISIONS.md`: pertahankan amandemen ADR-147 dari #266 **dan** bagian ADR-148 dari #267 — jangan buang salah satu).
3. **SEC15-10** (codec data dir terenkripsi) menunggu stack kripto mendarat di `main`.

### Catatan batasan harness MEM-034 (#268) — **bukan klaim tertutup**

Positif end-to-end jalur Anthropic untuk MEM-034 **tidak dapat** dibuktikan lewat biner asli dengan harness `internal/e2e` saat ini: harness hanya dapat menyetel `--base-url` ke server fake `127.0.0.1`, sementara `Route.Family` selalu merupakan hasil `provider.NormalizeBaseURL(base_url)` (`cmd/damnbot/run.go:515`) — jadi satu-satunya keluarga yang bisa menyentuh wire adalah origin fake itu. Akibatnya bukti positif ACC-73 ada di level encoder dengan bentuk `Family` persis seperti produksi (`NormalizeBaseURL("https://api.anthropic.com/v1")` → tepat satu blok `cache_control` pada pesan batas, `internal/provider/cache_test.go:TestCacheBreakpointProductionFamily`), sedangkan wire nyata hanya memberi kontrol negatif (`internal/e2e/cache_test.go:TestCacheBreakpointGenericWire`: tanpa `cache_control`, `content` tetap string). Untuk menutup jalur ini sebagai positif end-to-end, harness butuh seam famili eksplisit (mis. env/flag uji yang mengganti `Family` terhitung) — pekerjaan terpisah, belum ada di PR #268.

## Round 3 — 2026-10-22 — recheck wiring compaction (#219 `b13133c`) dan recall/frozen context (#220 `6a53ecc`) (W6/W7)

Ringkas: jalur summarizer → budget guard **benar** pada dua sumbu yang paling mudah salah (estimasi guard memakai payload pasca-compaction, dan usage panggilan summary dihitung ke turn), redaksi/validasi/escaping ringkasan kuat, dan retrieval #220 sudah dibatasi lineage + membuang turn ter-exclude. Satu temuan kelas SEC17-5 tersisa: **ringkasan ber-taint tidak men-taint turn** sehingga nilai `<untrusted source="summary">` tidak ditegakkan oleh kebijakan approval. Tidak ada PR yang di-merge dan tidak ada cabang yang di-push.

### Temuan

| ID | Severity | Status | Temuan | Perbaikan yang disarankan |
|---|---|---|---|---|
| **SEC18-1** | MEDIUM | confirmed (statis: grep + diff #219) | **Ringkasan tainted tidak men-taint turn.** `internal/compaction/summary.go:169-180` (`Blocks`) membungkus `<untrusted source="summary">` hanya bila `Summary.Tainted`, dan `internal/compaction/compactor.go:203` menyimpan flag itu, tetapi tidak ada jalur produksi yang memanggil `tools.Dispatcher.MarkTainted()` dari `SessionView.Summaries` (grep non-test `MarkTainted`: hanya definisi `internal/tools/dispatch.go:171-173`). `internal/assemble/request.go:100,111` menaruh `summaries` ke tier tanpa efek pada policy taint, dan diff #219 (`internal/loop/turn.go`, `types.go`) hanya menambah `Compactor`/`UsageCompactor`. Akibat: pada `--unsafe-auto-shell` (atau `shell_allow`), turn yang konteksnya memuat ringkasan hasil tool untrusted tetap meng-auto-approve tool mutating — kelas yang sama dengan SEC17-5 (@refs) yang sudah ditutup di #267. | Panggil `disp.MarkTainted()` ketika view compaction membawa ≥1 summary ber-`Tainted`: teruskan flag (mis. `compaction.View.Tainted` → `assemblerAdapter` → `loop.Prompt`) lalu taint sebelum tool dievaluasi; tambah e2e biner: sesi dengan tool output untrusted → compaction → turn berikutnya menjalankan perintah mutating di `--unsafe-auto-shell` (harus ditolak), kontrol negatif tanpa summary (perintah jalan). |
| SEC18-2 | LOW-MEDIUM | confirmed (statis) | **Ringkasan non-tainted masuk tier konteks tanpa wrapper.** `summary.go:171-179`: wrapper hanya untuk `Tainted`, sehingga ringkasan yang disusun dari teks user/assistant (tidak tainted) dirender polos dan `internal/assemble/recall.go:31-38` menempatkannya di tier konteks (pesan `system`). Ini desain resmi ADR-121/ADR-122 (bukan regresi #219), tetapi artinya teks yang dikendalikan user/agent (mis. isi skill/AGENTS.md yang ikut percakapan) dapat muncul di pesan istimewa setelah diringkas. Mitigasi yang sudah ada: `validate` JSON ketat + `DisallowUnknownFields` + redaksi + cap (≤600 tok atau `< inputTokens`) di `internal/compaction/summary.go:65-95`, escape `<` → `\u003c` di `summary.go:173`, `validWrappers` di `recall.go:35`, header `[summary of events …: earlier context, not a new request]` + `[end of summary]`. | Bila owner menganggap teks user/agent juga data: teruskan flag "derived from transcript" ke wrapper yang sama, atau batasi field ringkasan (mis. larang `decisions/open_items` memuat frasa imperatif) — keputusan desain, bukan perbaikan mekanis. |
| SEC18-3 | INFO (terverifikasi benar) | confirmed (statis) | **Guard/budget memakai payload pasca-compaction dan menghitung panggilan summary.** `cmd/damnbot/compaction_wire.go:58-75` (`runMeter.Measure`) membangun lewat assembler yang sama termasuk `Summaries` (`compaction.Blocks(v.Summaries)`), jadi estimasi guard = payload yang benar-benar dikirim; `internal/loop/turn.go` (diff #219) memakai `UsageCompactor.CompactUsage` dan menambahkan usage panggilan compaction ke total turn (SEC12-4). Ringkasan sendiri dibatasi `summaryMaxTokens=600` dan ditolak bila `n >= inputTokens` (`summary.go:92`). | — |
| SEC18-4 | INFO (terkunci) | confirmed (statis + LOC/test) | **Konsistensi redaksi/spill dan determinisme.** Input summary dibangun dari event transkrip yang sama (`internal/compaction/history.go:61-106`) dengan `Content`/`BlobSHA`/`Tainted` per hasil tool dipertahankan, dan `renderInput` men-taint summary bila salah satu item tainted (`summary.go:118-137`); balasan provider diredaksi + divalidasi (`summary.go:85-95`); `compactionSummarizer.Summarize` meredaksi System+Input (`cmd/damnbot/compaction_wire.go:29-37`) dan compactor menolak `Redactor == nil` (`internal/compaction/compactor.go:92`, fail closed). #220: `retrieval` membatasi ke lineage sesi, membuang turn `context_exclude` (`hitVisible`), melewati hit turn berjalan, dan mengurutkan deterministik (`score, session_id, seq`) dengan ≤3 snippet. | — |

### Status PR dan rebase (lokal, tanpa push)

| PR | Head | Base | Mergeable | Checks (mentah) | Go non-test | Rebase ke `main` (`d47cdf3`) |
|---|---|---|---|---|---|---|
| #219 | `b13133c` | `main` | CONFLICTING / DIRTY | tidak ada baris `ci-required`/`meta-test`/`pr-meta` yang terdaftar (checks basi) | 321 (`pr-meta size: 321 … size ok`) | konflik: `CHANGELOG.md` (additif) + **`cmd/damnbot/run.go` (kode)** |
| #220 | `6a53ecc` | `f2-w67` (=#219) | MERGEABLE / CLEAN | `meta-test pass 7s`, `pr-meta pass 9s` (run lama) | 252 vs `f2-w67` (`pr-meta size: 252 … size ok`) | belum bisa dinilai: rebase berhenti di commit #219 dengan konflik yang sama; konflik milik #220 sendiri belum terlihat |

Konflik `cmd/damnbot/run.go` (2 hunk, **tidak saya putuskan**):
1. literal `loop.Deps`: sisi `main` punya `Carriers: &ctxCarrierStore{db: model.db},` sedangkan #219 menambah `Compactor: comp,`.
2. `runModel`: sisi `main` `provider: prov,` vs #219 `provider: &providerAdapter{client: openai, model: modelRef, provider: provName},`.
Usulan aditif (untuk diputuskan penulis PR, bukan reviewer): pertahankan `Carriers` **dan** tambahkan `Compactor` pada hunk 1; pada hunk 2 pilih satu bentuk `provider:` setelah memastikan `prov` adalah adapter yang sama.

### Budget

Tidak ada PR ini yang mengubah `loc.budget`/`docs/ENGINEERING.md` (`git diff --stat` kosong untuk keduanya) → tidak ada baris ganda, tidak perlu aturan penjumlahan, dan reservasi ADR-177 untuk tabrakan `tools` tidak tersentuh di sini. Terukur di head masing-masing (`scripts/loc-check.sh budget`, tanpa baris `GAGAL`): #219 `compaction` 796/900, `assemble` 1264/1300, `loop` 1774/1800 (margin 26); #220 menambahkan `internal/ctxdb` (lihat lampiran laporan).

### Tes terfokus (head apa adanya, loadavg < 6)

```
# #219 b13133c
go test -tags goolm -count=1 ./internal/compaction ./internal/assemble ./internal/loop ./cmd/damnbot
ok  internal/compaction 0.099s | ok internal/assemble 0.039s | ok internal/loop 0.037s | ok cmd/damnbot 1.849s
# #220 6a53ecc
go test -tags goolm -count=1 ./internal/compaction ./internal/assemble ./internal/loop ./internal/ctxdb ./cmd/damnbot
ok  internal/compaction 0.107s | ok internal/assemble 0.039s | ok internal/loop 0.037s | ok internal/ctxdb 66.460s | ok cmd/damnbot 2.059s
# loc #220: compaction 796/900, loop 1774/1800, ctxdb 3490/3500 (margin 10), assemble 1264/1300, TOTAL 31741/190000
```
