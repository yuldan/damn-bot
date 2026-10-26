# REVIEW-F2-SEC2 — Review keamanan memory & skills stack (#140, #142, #149, #153, #152)

**Status: lengkap — Putaran 1: 0 BLOCKER, 6 MAJOR, 5 MINOR (kelima PR `changes required`). Putaran 2: JN3-1..11 tertutup; JN3-12/JN3-13 baru. Putaran 3: JN3-12/JN3-13 tertutup, JN3-14 (paging OFFSET) terbuka. Putaran 4 (`#152` `e64148b`): OFFSET dihapus, visibility menjadi predikat SQL sebelum ORDER BY/LIMIT; JN3-14 tertutup (100k purge k=10 26,6 ms; p95 25,7 ms). Putaran 5: #140 `changes required` karena E-84 625 > 600. Putaran 6: split #155/#140 bersih tetapi CODEOWNERS kehilangan dua baris (E-79). Putaran 7 (`#155` `be2d4ae` … `#152` `19c37c1`): hanya restore CODEOWNERS; archtest hijau, ukuran tetap, #152 delta identik. Verdict akhir: **keenam PR `approve`**.** Review independen atas `internal/memory`, `internal/ctxdb/memstore`, `internal/skills`, `internal/ctxdb/skillstore`, dan `ctxdb.Recall` (SPEC §4.8, §11.2, §11.6, P10, P25; ADR-120). Reviewer tidak menulis kode yang direview; kode produk tidak diubah. Tanggal: 2026-10-21. Pemeriksaan runtime di Linux; Windows/macOS dibaca statis dan di-cross-compile.

Head yang direview (stacked, belum di-merge): `#140` `1fb8476` (memory domain), `#142` `b6844a4` (memstore), `#149` `2294812` (skills), `#153` `c9c0e50` (skillstore), `#152` `38a97bb` (recall). Worktree terpisah `~/.cache/damnbot/wt-f2-sec2` pada head teratas `38a97bb`; semua probe dijalankan di worktree itu dan dihapus sebelum dokumen ini di-commit. Tidak ada wiring produksi di stack ini (hanya `cmd/damnbot/memory_compat_test.go`), sehingga seluruh temuan belum *reachable* sampai F2 disambungkan; severity dinilai untuk saat wiring dan untuk invarian paket yang akan dipakai gateway multi-user.

Batas ukuran (E-84, baris Go non-test +/− per PR): #140 = 567, #142 = 518, #149 = 337, #153 = 329, #152 = 74 — semuanya ≤ 600. LOC non-komentar: `memory` 411 + `skills` 253 = 664/1200, `ctxdb/memstore` 437/600, `ctxdb/skillstore` 283/400.

## Verdict per PR

| PR | Isi | Verdict |
|---|---|---|
| #140 | `internal/memory`: tipe, state machine karantina, threat scan, redaksi, recall | **changes required** — JN3-1 (Recall/Pending tanpa scope fail-open), JN3-7 |
| #142 | `ctxdb/memstore`: transaksi, cap, scope, purge, FTS | **changes required** — JN3-1, JN3-2, JN3-3 (MAJOR), JN3-8 |
| #149 | `internal/skills`: parser SKILL.md, karantina, retrieval/prompt | **changes required** — JN3-4 (MAJOR), JN3-10, JN3-11 |
| #153 | `ctxdb/skillstore`: upsert idempoten, tautan karantina, filter retrieval | **changes required** — JN3-5 (MAJOR), JN3-4, JN3-9 |
| #152 | `ctxdb.Recall` + harness 1M | **changes required** — JN3-6 (MAJOR) |

Ringkasan: enam MAJOR adalah isolasi scope memori (dua arah: baca dan mutasi), kebocoran baris `kind='skill'` ke recall memori, skill body tanpa redaksi, karantina skill yang bisa di-*hide* oleh sumber tidak tepercaya, dan `Recall` tanpa `LIMIT` yang meledak pada kueri umum. Tidak ada BLOCKER dalam arti gate/merge yang merah: seluruh gate arsitektur, test, race, coverage, dan ukuran lulus. Verdict tetap `changes required` karena temuan MAJOR di atas harus diperbaiki sebelum wiring.

## Temuan

`confirmed` = dibuktikan dengan probe; `static` = dibuktikan dari kode/dokumen.

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| JN3-1 | #140+#142 | **MAJOR** | confirmed | `Query.Scopes` kosong berarti **semua scope**: `memstore.Search` mengembalikan memori `approved` milik identitas gateway lain, dan `memory.Service.Pending` tidak punya parameter scope sehingga daftar review menampilkan `scanned` lintas tenant. Isolasi `gateway:<platform>:<identity>` (SPEC §4.8/P25) menjadi opt-in pemanggil; default fail-open. | probe `TestZZProbeEmptyScopesLeakAll`: `no scopes: 2 hits` (`gateway:telegram:alice`, `gateway:telegram:bob`); `TestZZProbeScannedAcrossScopes`: 2 hits; `memstore.go:238-300` (klausa scope hanya bila `len>0`), `memory/service.go:129-166` (Pending tanpa scope) |
| JN3-2 | #142 | **MAJOR** | confirmed | `SupersedesID` tidak divalidasi terhadap scope baris baru. Penulisan `approved` di scope Alice dengan `SupersedesID` milik Bob langsung me-`retire` memori Bob (dan dapat menargetkan baris `kind='skill'`). `Decide`/`Pin` juga menerima id arbitrer tanpa cek scope/tenant. | probe `TestZZProbeCrossScopeSupersede`: `victim status after cross-scope supersede: retired`; `memstore.go:95-150`, `:159-206`, `:387-401` |
| JN3-3 | #142+#140 | **MAJOR** | confirmed | `searchIDs` tidak memfilter `kind='memory'`; baris karantina skill (`kind='skill'`, body hingga 8.192 B, dikecualikan dari cap 500 char) masuk ke `memory.Service.Recall` setelah di-approve. Body skill (instruksi) bocor ke prompt memori dan melewati sub-cap memori/skill. | probe `TestZZProbeSkillRowInMemoryRecall`: `memory recall hits for approved skill row: 1`, `content="skillbody instruction payload"`; `memstore.go:238-300`; skema `memory CHECK (kind='memory' OR length(content) <= 500)` |
| JN3-4 | #149+#153 | **MAJOR** | confirmed | Body skill **tidak diredaksi** sebelum disimpan: `skillstore.quarantineMemory` menulis `r.Body` apa adanya ke `memory.content` dan `skills.body`/`skills_fts`; `skills.Service` tidak punya redactor (Config hanya `Scan`). SKILL.md dari `hub`/`agent`/`import` yang memuat token/kredensial menyimpannya ke DB dan indeks FTS, bahkan ketika statusnya `rejected`. Bertentangan dengan GAP-SEC-26 (0 `gh[pousr]_` di DB/log) dan defense-in-depth memori. | probe `TestZZProbeSecretBodyStored`: `memory.content contains secret=true; skills.body contains secret=true`; `skillstore.go:296-329`, `skills/service.go:28-35` |
| JN3-5 | #153 | **MAJOR** | confirmed | Instalasi tidak tepercaya dapat menghapus/menyembunyikan skill `approved` yang tepercaya: `indexTx` menghapus baris `name` dengan sha berbeda, dan upsert `(name,sha256)` menimpa `memory_id` menjadi id karantina baru. `agent`/`hub`/`import` tidak menjadi `approved`, tetapi dapat mematikan skill keamanan tanpa keputusan user (integrity/availability). | probe `TestZZProbeUntrustedHidesTrusted`: `after untrusted quarantine: 0 visible, rows=1`; `TestZZProbeSameContentHidesApproved`: `0 visible`; `skillstore.go:195-222` |
| JN3-6 | #152 | **MAJOR** | confirmed | `Recall` tidak memakai `LIMIT` di SQL; seluruh rowid yang cocok dimuat dan diurutkan `bm25` sebelum kandidat purge difilter. Kueri umum pada 100.000 event memakan **252,7 ms** (vs 3,6 ms untuk term sempit), jadi target 50 ms @1.000.000 event tidak mungkin terpenuhi untuk kueri umum; ini DoS/anggaran latensi. Harness `TestRecallP95` hanya memakai `w%d` (≈1/97 korpus) sehingga tidak menangkap kasus ini. | probe `TestZZRecallBroadQuery`: `broad('event')=252.679402ms narrow('w5')=3.56042ms` @100k; `visible.go:53-77` |
| JN3-7 | #140 | MINOR | confirmed | `Propose` memindai teks yang **sudah diredaksi**, sehingga temuan `secret` severity high tidak pernah muncul (`scan={"severity":"low"}`). Redaksi tetap bekerja (tidak ada kebocoran), tetapi kontrol "secret → auto-reject" mati dan `scan_result` menyesatkan reviewer. `ScanText(nil, secret)` juga low, tetapi `memory.New` menolak redactor nil dan `skills.Install` menolak scanner nil untuk sumber tidak tepercaya. | probe `TestZZProbeSecretScanOrder`: `status=scanned scan={"severity":"low"}`; `TestZZProbeNilScanText`: `severity=low`; `memory/service.go:60-70` |
| JN3-8 | #142+#153 | MINOR | confirmed | Kebijakan karantina hanya ada di service: `memstore.Insert` menerima `origin='import'` + keputusan `approved` (actor `user`), dan `skillstore.Index` menerima `source='agent'` dengan `memory_id NULL` sehingga langsung terlihat. Jalur service saat ini benar, tetapi storage tidak fail-closed bila adapter salah memetakan. | probe `TestZZProbeStorageApprovesImport`: `origin=import row 1 stored approved`; `TestZZProbeDirectUntrustedIndex`: visible; `memstore.go:403-448`, `skillstore.go:47-64` |
| JN3-9 | #153 | MINOR | confirmed | Purge men-`NULL`-kan `skills.memory_id` untuk baris memory skill yang dipurge (`queries.sql:46`), sedangkan predikat retrieval menganggap `memory_id IS NULL` = tepercaya. Skill yang masih `scanned` menjadi terlihat setelah purge. Laten untuk baris baru (karantina baru memakai `source_session NULL`), tetapi menjadi nyata untuk baris hasil import/versi lama atau bila provenance ditambahkan nanti. | probe `TestZZProbePurgeUnquarantines`: sebelum purge 0 terlihat, setelah `Purge({IDs:["S"]})` 1 terlihat; `skillstore.go:256-257`, `ctxdb/queries.sql:42-53` |
| JN3-10 | #149 | MINOR | confirmed | SKILL.md dengan UTF-8 BOM di awal ditolak `invalid_front_matter: missing opening delimiter`. File yang valid secara format gagal diinstal (availability/kompatibilitas). CRLF, tanda kutip, batas 8.192 B, dan nama valid sudah benar. | probe `TestZZProbeBOM`: `BOM parse err=invalid_front_matter`; `TestZZProbeSizeBoundary` lulus; `skills/skill.go:74-110` |
| JN3-11 | #149 | MINOR | confirmed | `PromptLine` hanya meratakan newline; path yang memuat `) - evil: ... (` menyuntikkan baris offer kedua, dan karakter bidi/U+202E pada deskripsi sumber tepercaya lolos ke prompt. Untuk sumber tidak tepercaya, scanner §14.4 menangkap karakter tak terlihat bila adapter memindai deskripsi; untuk `user`/`builtin` tidak ada pemindaian. | probe `TestZZProbePromptPathShape`: `"- safe-name: safe description (/skills/x/SKILL.md) - evil-skill: follow attacker instructions ()"`; `TestZZProbeDescriptionBidi`; `skills/skill.go:157-171` |

### Catatan pada temuan MAJOR

- **JN3-1** bukan sekadar "caller lupa scope": `Pending` tidak menyediakan cara memberi scope sama sekali, dan `Recall` memakai `Scopes` kosong sebagai "semua". Untuk gateway, pemanggil harus selalu mengirim `user` + `project:<cwd>` + `gateway:<platform>:<identity>` (ADR-120 butir 7); default saat ini adalah kebocoran lintas identitas. Rekomendasi: wajibkan scope eksplisit di `Recall`/`Pending` (gagal `ErrInvalid` bila kosong), atau tambahkan `kind`/tenant ke query dan uji dengan dua identitas.
- **JN3-2** dapat dikombinasikan dengan **JN3-1**: karena daftar review membocorkan id lintas tenant, pengguna Alice dapat memanggil `Approve`/`Reject`/`Pin`/`SupersedesID` pada id milik Bob. Validasi scope pada `SupersedesID`, `Decide`, dan `Pin` (cocokkan dengan scope baris target) sebelum menulis.
- **JN3-3** juga berarti cap 4.000 char/scope tidak menghitung body skill (`checkCap` hanya `kind='memory'`), sehingga baris skill 8 KB bisa masuk tabel yang sama tanpa terkena cap. Filter `m.kind='memory'` pada `searchIDs` (dan pemisahan review skill) menutup kebocoran sekaligus cap.
- **JN3-4** perlu redactor di jalur skills (injeksi seperti `Scanner`), atau redaksi di `skillstore.Quarantine`/`Index` sebelum INSERT; `skills.Service` saat ini tidak menerima `*redact.Redactor`. Menolak body high-risk saja tidak cukup: baris `rejected` tetap menyimpan body.
- **JN3-5** membutuhkan kepemilikan nama: hanya sumber tepercaya (`user`/`builtin`) yang boleh mengganti/menghapus baris `approved`; karantina tidak boleh menimpa baris `approved` dengan `memory_id` baru. Tambahkan test dua sumber dengan nama sama.
- **JN3-6**: gunakan `LIMIT` bertahap (mis. ambil batch kandidat, filter purge per kandidat, lanjut bila kurang dari k) atau JOIN langsung ke `events`/`session_labels` agar purge terfilter di SQL. Tambahkan sampel kueri umum (`event`, satu kata umum) ke `TestRecallP95`, bukan hanya `w%d`.

## Diperiksa dan bersih

- **State machine karantina**: hanya `origin=user` yang langsung `approved`; `agent`/`auto`/`import`/`hub` selalu diskrining, `high` → `rejected`, selain itu `scanned`. `agent`/`auto` wajib `evidence_event_id` (Go + CHECK skema). Approval actor `user`, transisi tertutup (termasuk `rejected`/`retired` tidak bisa kembali); `rejected` tidak pernah masuk retrieval.
- **Fail-closed nil**: `memory.New` menolak `Store`/`Redactor` nil; `skills.Install` menolak scanner nil untuk sumber tidak tepercaya; `skillstore.Quarantine` menolak sumber tepercaya.
- **Scope eksplisit bekerja**: probe `Scopes:["gateway:telegram:alice"]` hanya mengembalikan Alice; filter `status`, `expires_at`, `pinned`, dan purge per kandidat berfungsi saat diberikan.
- **Purge memori**: filter `source_session` dan sesi `evidence_event_id` ada; label `purge_state` dilindungi trigger (`purge_state label is reserved for purge`), jadi pemanggil biasa tidak bisa memalsukan label. Baris memori dengan provenance sesi yang dipurge ikut dihapus oleh `queries.sql:42-53`.
- **FTS tidak bisa disuntik sintaks**: `ftsQuery` hanya mempertahankan huruf/angka; `dmb_fts_norm` men-*lowercase* query dan indeks. Probe `AND`, `OR`, `NOT`, `NEAR`, `alpha - ("beta`, `"alpha`, `alpha*`, `!!!`, CJK: semua `err=nil` (bukan syntax error), hanya hasil yang sesuai. Semua nilai query/status/scope memakai placeholder parameter.
- **Cap dan supersede transaksional**: 500 char/entri dan 4.000 char/scope dihitung dalam transaksi tulis yang sama; `TooLarge` dipetakan ke `memory.ErrTooLarge`; supersede me-retire baris lama di transaksi yang sama; dua penulis diserialkan writer lock. `TestScopeCap`, `TestTransitions`, `TestCrossProcess` lulus.
- **Parser SKILL.md**: batas 8.192 B tepat (8192 diterima, 8193 `ErrTooLarge`), CRLF dinormalisasi, tanda kutip tunggal/ganda, front matter tanpa pasangan/body kosong ditolak dengan error tertutup, SHA-256 dihitung dari byte asli, nama `[a-z0-9_-]` ≤ 64, deskripsi dipotong 120 char di prompt (utuh di index), ≤ 3 skill per offer.
- **Retrieval skill**: hanya baris tanpa `memory_id` atau `memory_id` berstatus `approved`; `scanned`/`rejected` tidak muncul; `Approve` hanya dari `scanned`; `Search` memakai prefix pada term terakhir dan tetap melalui normalisasi FTS.
- **Redaksi memori**: `memory.Service` dan `memstore.Insert` sama-sama menerapkan redactor; probe `TestRedactionAtStorage`/`TestRedactionOnWrite` menunjukkan secret tidak tersimpan dan tidak terindeks FTS.
- **Gate**: `go test ./internal/memory/ ./internal/skills/ ./internal/ctxdb/memstore/ ./internal/ctxdb/skillstore/` hijau; `go test ./internal/archtest/ -count=1` hijau (E-01/E-04/E-09/E-10/E-14); `go vet` bersih; cross-build Windows/darwin keempat paket berhasil; `-race` keempat paket hijau (1,0–9,4 dtk); coverage scoped: memory 83,3 %, skills 83,7 %, memstore 77,1 %, skillstore 81,1 % (min 70 %).
- **Batas arsitektur**: hanya `ctxdb/memstore` yang mengimpor `database/sql` (di bawah `internal/ctxdb/...`, E-04); `memory.Store` dan `skills.Store` masing-masing 5 method (E-09); tidak ada global mutable baru (hanya sentinel error); `memstore`/`skillstore` tidak mengimpor paket konsumen; `skills` tidak mengimpor `memory` (scanner diinjeksi).
- **Dokumen/ukuran**: ADR-120 ada, `CODEOWNERS` menambahkan `/internal/memory/` dan `/internal/skills/`, baris `loc.budget` sudah sesuai (`memory+skills` 1.200, `ctxdb/memstore` 600, `ctxdb/skillstore` 400), E-84 kelima PR ≤ 600.

## Residual risk

- **Isolasi tenant adalah tanggung jawab pemanggil** sampai wiring: `Scopes` kosong = semua scope (JN3-1). Gateway F4 harus selalu mengirim scope identitas; belum ada test lintas-identitas di stack ini.
- **Skill bersifat global**: tabel `skills`/`memory(kind='skill')` tidak punya scope per `gateway:<platform>:<identity>`; skill yang di-approve satu pengguna dapat ditawarkan ke sesi gateway lain. SPEC tidak mendefinisikan skill per-tenant; perlu keputusan ADR sebelum gateway.
- **Redaksi skill** bergantung pada redactor F1 dan pola yang tersedia; body kode/skill bisa memuat rahasia yang tidak cocok pola (JN3-4).
- **Performa `Recall`** sensitif terhadap term umum dan ukuran korpus (JN3-6); angka 1M pada Q14 belum diukur untuk kueri umum, dan `Store.Search` lama masih meneruskan query mentah ke `MATCH` (pre-existing, di luar PR ini).
- **FTS/query panjang**: tidak ada batas panjang teks query; string sangat panjang dapat memperlambat parsing/tokenisasi walau tidak menjadi syntax error.
- **Purge/GC**: baris skill yang tautannya di-`NULL`-kan purge menjadi terlihat (JN3-9); interaksi dengan `doctor gc`, backup/restore, dan re-index FTS belum diuji di stack ini.
- **Windows/macOS**: hanya cross-compile/statis; `filepath` scope dan `FTS5` normalisasi tidak diuji runtime.
- **Belum ada wiring produksi**: `cmd/damnbot` hanya punya adapter test (`memory_compat_test.go`); temuan dinilai untuk saat tools/CLI/gateway menyambungkan paket ini.

## Tidak dicakup

- Tool/CLI memory (`propose_memory`, `recall`, `/memory review`), TUI/web/gateway, dan otorisasi tenant F4.
- Memory provider eksternal, embedding/cosine, compaction, dan asembler context tier.
- Fuzz, benchmark penuh (1M event) di Q14, stress konkurensi lintas proses lebih dari dua, dan purge/GC end-to-end.
- Windows/macOS runtime dan filesystem case-insensitive.
- Perbaikan setelah head yang direview; verdict berlaku untuk `1fb8476`, `b6844a4`, `2294812`, `c9c0e50`, `38a97bb`.


## Putaran 2 — re-review perbaikan (2026-10-21)

Head baru (stacked): `#140` `8b9247a`, `#142` `25ad231`, `#149` `b78b8df`, `#153` `b12ddd9`, `#152` `7fcb724`. Metode: diff hanya pada hunk yang berubah vs head Putaran 1, probe `TestZZR2*` sementara di worktree terpisah (dihapus sebelum commit), lalu gate penuh. `memory.Store` berubah (`Decide`/`Pin` menerima `scope`); adapter composition-root `cmd/damnbot/memory_compat_test.go` sudah ikut berubah dan lulus `go test -tags goolm ./cmd/damnbot -run TestMemory`.

### Verdict per PR (Putaran 2)

| PR | Head | Verdict |
|---|---|---|
| #140 | `8b9247a` | **approve** — JN3-1/7 tertutup; interface scope + `Pending`/`Recall` wajib scope |
| #142 | `25ad231` | **approve** — JN3-1/2/3/8 tertutup |
| #149 | `b78b8df` | **approve** — JN3-4/7/10/11 tertutup (redaksi + parser) |
| #153 | `b12ddd9` | **approve** — JN3-4/5/8/9 tertutup (presedensi trusted, purge, redaksi storage) |
| #152 | `7fcb724` | **changes required** — JN3-6 (perf) tertutup, tetapi JN3-12 (MAJOR, purge starvation) dan JN3-13 (MINOR, broad ordering) baru |

### Status JN3-1..11

| ID | Status | Bukti probe Putaran 2 |
|---|---|---|
| JN3-1 | **fixed** | `TestZZR2EmptyScopesDeny`: `Search` tanpa scope = 0 hits; `memory.Recall`/`Pending` tanpa scope = `ErrInvalid` (`validateScopes`); scope eksplisit hanya mengembalikan Alice. |
| JN3-2 | **fixed** | `TestZZR2CrossScopeMutations`: supersede = `ErrScope` (`... 1 is in "gateway:telegram:bob"`), `Decide`/`Pin` lintas scope = `ErrScope`; korban tetap `approved`. `memory.Propose` juga memeriksa `SupersedesID` lewat `Get` + scope. |
| JN3-3 | **fixed** | `TestZZR2SkillRowNotInMemoryRecall`: baris `kind='skill'` approved → 0 hits di memory recall (`m.kind='memory'`). |
| JN3-4 | **fixed** | `TestZZR2SecretRedacted`: secret tidak ada di `memory.content`, `skills.body`, `description`, `memory_fts`, `skills_fts`; sentinel redaksi ada. Service memindai **raw** lalu meredaksi sebelum storage. |
| JN3-5 | **fixed** | `TestZZR2TrustedThenQuarantine` dan `TestZZR2QuarantineThenTrusted`: kedua urutan tetap menampilkan baris trusted; karantina dengan nama sama tidak menghapus/menyembunyikan trusted (`NOT EXISTS trusted same name`, delete hanya baris karantina). |
| JN3-6 | **fixed (perf)** | 1M: selective p95 **33,63 ms**, broad p95 **7,37 ms**; 100k broad p95 **9,99 ms** — semua < 50 ms. Lihat JN3-12 untuk regresi cakupan kandidat. |
| JN3-7 | **fixed** | `TestZZR2SecretScannedBeforeRedaction`: agent + secret → `status=rejected`, `scan={"severity":"high","findings":[{"category":"secret"...}]}`, konten tersimpan diredaksi. |
| JN3-8 | **fixed** | `TestZZR2StoragePolicy`: `Put` origin import approved → `ErrPolicy`; `Index` source agent → `ErrPolicy`. |
| JN3-9 | **fixed** | `TestZZR2PurgeDoesNotUnquarantine`: setelah purge, `memory_id` NULL tetapi source `hub` tidak lolos predikat trusted → 0 terlihat. |
| JN3-10 | **fixed** | `TestZZR2BOM`: SKILL.md dengan UTF-8 BOM diterima, `name="sdlc-review"`, SHA 64 hex. |
| JN3-11 | **fixed** | `TestZZR2BidiAndPath`: bidi di deskripsi ditolak `ErrInvalid`; path dengan `)` di-escape (`%29`/`%28`) sehingga tidak membentuk baris offer kedua; `PromptLine` menyanitasi kontrol/bidi. |

### Temuan baru

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| JN3-12 | #152 | **MAJOR** | confirmed | `candidateCap(k)` membatasi kandidat ke max(4k,64)…256 sebelum filter purge. Bila kandidat teratas berasal dari sesi yang sedang dipurge, `Recall` berhenti sebelum mencapai hasil valid: 300 event sesi S (purge label) + 10 event sesi B → `Recall("purgeword", 10)` dan `k=50` mengembalikan **0**, padahal 10 hasil valid ada. Batas 256 juga membuat `k>256` tidak pernah terpenuhi. Spec §11.6 meminta filter purge per kandidat sampai k hasil. | probe `TestZZR2PurgeStarvation`: `k=10: hits=0`, `k=50: hits=0`; `visible.go:53-104`, `candidateCap` |
| JN3-13 | #152 | MINOR | confirmed | Fallback broad tidak memakai `ORDER BY`; hasilnya bukan "newest-first" seperti komentar kode. Pada 100k event, `Recall("event",10)` mengembalikan id **1..10** (event tertua), bukan yang terbaru. Untuk term umum, recall menjadi tidak berguna secara temporal. | probe `TestZZR2BroadOrdering`: `broad @100000: elapsed=6.901702ms ids=1..10`; komentar `visible.go:48-50` |

### Verifikasi gate Putaran 2

- `go test ./internal/memory/ ./internal/skills/ ./internal/ctxdb/memstore/ ./internal/ctxdb/skillstore/ -count=1` → hijau.
- `go test ./internal/ctxdb/ -run 'TestRecall|TestPurgeFTSIntegrity'` → hijau.
- `go test -tags goolm ./cmd/damnbot -run TestMemory` → hijau (adapter `Store` scope baru cocok).
- `go test ./internal/archtest/ -count=1` → hijau; `go vet -tags goolm` paket terkait + cmd → bersih.
- Cross-build Windows/darwin keempat paket → ok.
- `go test -race` keempat paket → hijau (1,0–12,1 dtk).
- Coverage scoped: memory 85,6 %, skills 86,7 %, memstore 77,6 %, skillstore 83,3 % (min 70 %).
- E-84: #140 600, #142 563, #149 397, #153 370, #152 99 — semua ≤ 600. LOC: memory+skills 741/1200, memstore 476/600, skillstore 319/400.
- Perf 1M (`DAMNBOT_RECALL_EVENTS=1000000`, `-tags goolm,perf -run TestRecallP95`): selective p50 31,22 / p95 33,63 ms; broad p50 6,76 / p95 7,37 ms; 100k broad p95 9,99 ms.

### Rekomendasi JN3-12/JN3-13

- JN3-12: jangan memotong kandidat sebelum yakin ada cukup hasil non-purge. Iterasi batch (`LIMIT`/`OFFSET` atau keyset per batch 64–256) sampai k hasil terkumpul atau kandidat habis; atau JOIN `events`/`session_labels` dengan `NOT EXISTS purge_state` di SQL agar purge terfilter sebelum `LIMIT`. Tambahkan regression: 300 event purged + 10 valid untuk k=10/50.
- JN3-13: gunakan `ORDER BY rowid DESC` pada fallback broad agar benar-benar newest-first, atau ubah komentar/dokumentasi dan terima degradasi; sertakan test urutan.
- JN3-12 menahan #152; JN3-13 MINOR dapat menyusul. #140/#142/#149/#153 disetujui untuk head ini.


## Putaran 3 — verifikasi #152 setelah paging (`9d6c8d1`)

Head `#152` `9d6c8d1` (103 baris Go non-test inkremental; total PR 108 baris ≤ 600). Perubahan: kandidat diambil per halaman 256, loop sampai k hasil terlihat atau kandidat habis; `candidateCap` dihapus; fallback broad memakai `ORDER BY rowid DESC`; test `TestRecallPurgedStarvation`, `TestRecallHonorsLargeK`, `TestRecallBroadNewestFirst`.

### Verdict

| PR | Head | Verdict |
|---|---|---|
| #152 | `9d6c8d1` | **changes required** — JN3-12/JN3-13 tertutup, tetapi JN3-14 (MAJOR, worst-case paging) baru |

#140/#142/#149/#153 tetap **approve** untuk head Putaran 2.

### Bukti probe Putaran 3

- **Purge starvation (JN3-12) fixed**: 300 event sesi S berlabel purge + 10 event valid B → `Recall("purgeword",10)` dan `k=50` masing-masing **10 hits**, semuanya dari B.
- **k > 256 fixed**: 600 event → `Recall("bigk",500)` = **500 hits, 500 id unik** (tidak ada duplikat/skip dari paging).
- **Broad newest-first (JN3-13) fixed**: 100k event → `Recall("broadword",10)` = id **100000..99991**, 9,26 ms.
- **Perf 1M**: `TestRecallP95` (1.000.000 event) → selective p50 31,28 / p95 **34,14 ms**; broad p50 7,21 / p95 **8,05 ms** — sesuai klaim (38,7/7,9) dan < 50 ms.
- **Gate**: `TestRecall|TestPurgeFTSIntegrity` hijau; `-race` paket ctxdb untuk test tersebut hijau (125,0 dtk); `go test ./internal/archtest/ -count=1` hijau; `go vet ./internal/ctxdb/` bersih; `ctxdb` LOC 2.955/3.100.

### Temuan baru

| ID | PR | Severity | Status | Temuan | Bukti |
|---|---|---|---|---|---|
| JN3-14 | #152 | **MAJOR** | confirmed | Paging memakai `LIMIT 256 OFFSET n` pada `ORDER BY rowid DESC` (broad) atau `bm25` (selective). Setiap halaman memindai/mengurutkan ulang seluruh prefix offset, sehingga biaya menjadi O(matches²/256) ketika hasil valid berada di belakang banyak baris yang dipurge. 100k event purge yang lebih baru dari 10 hasil valid membuat `Recall("pageword",10)` memakan **6,83 detik** (target 50 ms); 1M baris purge diskalakan ~100× (≈10 menit). Ini juga jalur DoS saat purge sesi besar berjalan dan recall dipanggil bersamaan. | probe `TestZZR3PurgedPagesCost`: `100k purged pages: elapsed=6.830508339s hits=10 err=<nil>`; `visible.go` `recallBatch` (`LIMIT ? OFFSET ?`), loop `Recall` |

### Rekomendasi JN3-14

- Ganti OFFSET dengan **keyset pagination**: broad `WHERE rowid < lastRowid ORDER BY rowid DESC LIMIT 256`; selective gunakan kursor `(bm25, rowid)` atau filter purge di SQL (`JOIN events e ... NOT EXISTS session_labels purge_state`) sebelum `ORDER BY bm25 LIMIT k`.
- Alternatif: batas halaman eksplisit (mis. 16 halaman) dengan hasil parsial + penanda degradasi, bukan loop tanpa batas.
- Tambahkan regression: 100k baris purge lebih baru dari 10 valid, k=10, p95 < 50 ms; dan 1M purged jika feasible.
- JN3-14 menahan #152; JN3-12/JN3-13 sudah tertutup dan tidak perlu dibuka kembali.


## Putaran 4 — verifikasi final #152 (`e64148b`)

Head `#152` `e64148b` (67 baris Go non-test inkremental; total PR 108 ≤ 600). `recallBatch`/`OFFSET` dihapus: `Recall` menjalankan **satu** query FTS; bila ada label purge, visibility menjadi predikat SQL `f.rowid IN (SELECT e2.id FROM events e2 WHERE e2.session_id NOT IN (SELECT session_id FROM session_labels WHERE key='purge_state'))` **sebelum** `ORDER BY`/`LIMIT k`. Broad tetap `ORDER BY f.rowid DESC`; selective `bm25`. Pemeriksaan `notPurging` per baris dipertahankan sebagai lapisan kedua.

### Verdict

| PR | Head | Verdict |
|---|---|---|
| #152 | `e64148b` | **approve** — JN3-12/13/14 tertutup; tidak ada jalur OFFSET/kuadratik |
| #140/#142/#149/#153 | Putaran 2 | **approve** (tidak berubah) |

### Bukti probe Putaran 4

- **100k purge + 10 valid** (valid lebih tua dari baris purge): `Recall("pageword",10)` = **26,61 ms**, `k=50` = **26,23 ms**, 10 hit semuanya dari sesi valid. Test paket `TestRecallP95Purged100k` (50 sampel): p95 **25,74 ms** (k=10) dan **33,79 ms** (k=50) — sebelumnya 6,83 dtk; tidak ada OFFSET/kuadratik.
- **Starvation**: 300 purge + 10 valid → k=10 dan k=50 masing-masing **10 hit**, tanpa hit purge.
- **k besar**: 600 event → `k=500` = **500 hit, 500 id unik** (tidak ada duplikat/skip).
- **Broad newest-first**: 100k event → id **100000..99991**, 7,19 ms.
- **Perf 1M**: selective p50 31,33 / p95 **37,97 ms**; broad p50 7,16 / p95 **7,75 ms** — di bawah 50 ms (klaim 34,7/7,6).
- **Statis**: tidak ada `OFFSET`, `recallPage`, atau `candidateCap` di `visible.go`; satu query + satu lookup per hasil terpilih.
- **Gate**: `TestRecall|TestPurgeFTSIntegrity` hijau; `-race` paket ctxdb untuk test tersebut hijau (126,2 dtk); `go test ./internal/archtest/ -count=1` hijau; `go vet ./internal/ctxdb/` bersih; LOC `ctxdb` 2.955/3.100.

### Residual

- Saat ada label purge, setiap recall membangun subquery himpunan id event yang terlihat (O(event terlihat), bukan O(hasil purge)); pada 100k event biayanya ~26–34 ms dan sudah memenuhi target. Label purge yang tertinggal tanpa event dapat menambah biaya kecil.
- Race label yang masuk setelah subquery tetapi sebelum lookup baris ditutup oleh `notPurging` per baris (hasil bisa < k hanya pada jendela transien).
- Broad fallback tetap menurunkan ranking bm25 menjadi newest-first untuk term >15.000 match (didokumentasikan; bukan regresi).
- Tidak ada temuan terbuka untuk #152; kelima PR memory/skills disetujui pada head masing-masing.


## Putaran 5 — rebase scope portability (verifikasi singkat)

Head setelah rebase: `#140` `715e415`, `#142` `38f3280`, `#149` `579b55f`, `#153` `55f6b3f`, `#152` `bf71072`. Fix hanya normalisasi scope portabel: `project:` di-`Clean` + `ToSlash`, path absolut diterima lewat `path.IsAbs` atau `filepath.IsAbs(FromSlash(...))`; service menormalkan sebelum store, memstore memvalidasi bentuk yang sama.

| PR | Verdict | Catatan |
|---|---|---|
| #140 | **changes required** | E-84 **625 baris Go non-test** (> 600, tanpa label `large-change` + 2 approver); sebelumnya tepat 600, normalisasi menambah 25. Perilaku scope sendiri lulus. |
| #142 | **approve** | `validScope` portabel; `Decide`/`Pin`/supersede lintas scope tetap `ErrScope`; scope kosong tetap deny. |
| #149 | **approve** | Tidak ada perubahan perilaku terkait scope; delta hanya warisan rebase. |
| #153 | **approve** | Tidak ada perubahan perilaku terkait scope; delta hanya warisan rebase. |
| #152 | **approve** | `git diff e64148b..bf71072 -- internal/ctxdb` **tidak kosong** hanya karena baris `memstore.validScope` warisan #142 (+2/−1); diff milik #152 (`pr153..pr152`) **identik byte-per-byte** dengan `b12ddd9..e64148b` — recall/tests tidak berubah. |

Bukti verifikasi:
- `go test -race ./internal/memory ./internal/ctxdb/memstore` → hijau (1,03 dtk / 12,02 dtk).
- `TestScopeIsolation`, `TestScopeNormalization`, `TestScopeBoundaries`, `TestRecallQueryAndLimit` → PASS. Cross-scope approve/pin/supersede = `ErrScope`; `Recall{Text:"q"}` dan `Pending(nil)` = `ErrInvalid`; `Query` tanpa scope di memstore = 0 hit (deny).
- `GOOS=windows` dan `GOOS=darwin` build `memory`, `memstore`, `skills`, `skillstore`, `ctxdb` → OK; `path.IsAbs` menerima `/portable/repo` di semua OS, dan `C:/repo` diterima di Windows lewat `filepath.FromSlash` (statis; Windows runtime tidak dijalankan di sini).
- `go test ./internal/archtest/ -count=1` → hijau.
- LOC pasca-rebase: memory 458 + skills 303 = 761/1200, memstore 477/600, skillstore 319/400.

Residual kecil: perbandingan scope tetap case-sensitive (drive letter `C:` vs `c:` di Windows dianggap beda — isolasi terlalu ketat, bukan kebocoran); pemanggil `memstore` langsung harus mengirim bentuk yang sudah dinormalkan; normalisasi UNC (`\\server\share`) berbeda antar-OS (edge, tidak diuji runtime).


## Putaran 6 — graph #155→#140 (split E-84)

Graph baru: `#155` `ab0812c` (base main `4c9c207`) → `#140` `0b451f1` → `#142` `7afa916` → `#149` `23cc4a9` → `#153` `fcb16cc` → `#152` `6112f9a`.

### Ukuran (E-84, Go non-test per PR)

| PR | Baris | Status |
|---|---:|---|
| #155 | 64 | ok |
| #140 | 567 | ok (turun dari 625) |
| #142 | 564 | ok |
| #149 | 397 | ok |
| #153 | 370 | ok |
| #152 | 99 | ok |

### Verdict

| PR | Verdict | Alasan |
|---|---|---|
| #155 | **changes required** | Gate E-79: commit `ab0812c` menghapus baris `/internal/compaction/ @yuldan` dan `/internal/journal/ @yuldan` dari `.github/CODEOWNERS`; `TestCodeownersComplete` gagal di semua head graph (`origin/main` lulus). Ukuran/scope sendiri bersih (64 baris). |
| #140 | **changes required (warisan)** | Delta #140 sendiri bersih: 567 baris, tidak menyentuh `scope.go`/`scope_test.go`, `normalizeScope`/`validateScope` hanya didefinisikan di #155, `TestScopeNormalization` hanya di #155 dan `TestScopeIsolation` hanya di #140 (tidak ada duplikasi). Namun head-nya mewarisi E-79 #155 sampai diperbaiki/di-rebase. |

### Bukti

- Ukuran E-84 di atas; `git diff pr155..pr140 -- internal/memory` hanya `doc.go`, `memory_test.go`, `scan.go`, `service.go`, `types.go` — tidak ada `scope.go`/`scope_test.go` yang diduplikasi.
- `go test -race ./internal/memory ./internal/ctxdb/memstore` → hijau (1,02 dtk / 12,14 dtk); `TestScopeNormalization`, `TestScopeIsolation`, `TestScopeBoundaries`, `TestRecallQueryAndLimit` PASS; cross-scope deny/empty deny/portable `project:/...` tetap benar.
- `GOOS=windows`/`GOOS=darwin` build paket terkait → ok.
- **#152**: delta milik #152 (`pr153..pr152`) identik dengan `b12ddd9..e64148b` untuk `visible.go`, `recall_test.go`, `recall_perf_test.go`, `store.go`, `tx.go`; `git diff --stat` 6 file/488 baris sama. Perbedaan CHANGELOG hanya posisi baris warisan base. Semantik recall tidak berubah.
- **Gate gagal**: `go test ./internal/archtest/ -count=1` → `FAIL TestCodeownersComplete` untuk `internal/compaction` dan `internal/journal`; `origin/main` lulus. Perbaikan: kembalikan dua baris CODEOWNERS di #155 dan rebase graph.

### Rekomendasi

Kembalikan dua baris `/internal/compaction/ @yuldan` dan `/internal/journal/ @yuldan` di `.github/CODEOWNERS` pada #155, lalu rebase #140..#152. Setelah itu graph memenuhi E-79/E-84 dan semua temuan Putaran 4 tetap berlaku (tidak ada perubahan kode recall).


## Putaran 7 — CODEOWNERS restore (verifikasi singkat)

Head: `#155` `be2d4ae`, `#140` `7c37137`, `#142` `d8bf26b`, `#149` `2528266`, `#153` `5233eea`, `#152` `19c37c1`.

- Delta vs head Putaran 6 (`ab0812c`/`0b451f1`/`7afa916`/`23cc4a9`/`fcb16cc`/`6112f9a`) untuk **setiap** PR hanya `.github/CODEOWNERS | 2 ++` — tidak ada perubahan kode/test.
- Baris `/internal/compaction/`, `/internal/journal/`, `/internal/memory/` ada; diff vs `origin/main` hanya menambahkan baris memory. Duplikat `/internal/codetools/` sudah ada di `origin/main` (bukan regresi PR ini).
- Ukuran E-84 tetap: #155 64, #140 567, #142 564, #149 397, #153 370, #152 99 — semua ≤ 600.
- `go test ./internal/archtest/ -count=1` di head `19c37c1` → **ok (exit 0)**; `origin/main` juga ok.
- #152 delta kode/test (`pr153..pr152`) tetap identik dengan `b12ddd9..e64148b`; recall tidak berubah.

### Verdict

| PR | Verdict |
|---|---|
| #155 | **approve** — 64 baris, scope normalization tunggal, tanpa duplikasi; gate E-79 pulih |
| #140 | **approve** — 567 baris, delta hanya memakai scope dari #155; gate warisan pulih |
| #142/#149/#153/#152 | **approve** (Putaran 4/6 tetap berlaku) |

Tidak perlu mengulang probe keamanan penuh: satu-satunya perubahan sejak Putaran 6 adalah restore dua baris CODEOWNERS, dan seluruh perilaku scope/recall terverifikasi tidak berubah.
