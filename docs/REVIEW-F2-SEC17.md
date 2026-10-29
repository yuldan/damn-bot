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
