# REVIEW-1.0 — review silang independen Spec Damn-Bot

Objek review: `SPEC.md`, `LICENSE`, dan semua `docs/*.md` di `/home/jelangkung/Dokumen/Damn-Bot` per 2026-10-17 (17 file, 8.034 baris). Read-only: tidak ada file selain dokumen ini yang diubah.

## Ringkasan

| Severity | Jumlah |
|---|---:|
| BLOCKER | 11 |
| MAJOR | 26 |
| MINOR | 28 |
| **Total** | **65** |

**Verdict: siap freeze setelah perbaikan BLOCKER + MAJOR — YA**, dengan dua syarat:

1. **B-01** (lisensi WhatsApp Web) butuh keputusan user lewat ADR baru. Opsi yang ada: drop dari 1.0, atau pisahkan sebagai binary GPL terpisah. Keputusan ini sekaligus menentukan M-01 (Σ budget binary).
2. **M-24** dikerjakan lebih dulu: terapkan semua keputusan §22.2/§22.3 ke bagian target (§2, §3.5, §9, §10, §11, §12, §17.1). Sebagian besar BLOCKER skema (B-04..B-09) dan CLI (B-10) muncul karena keputusan yang sudah Accepted belum disalin ke DDL/tabel normatif.

Tidak ada temuan yang mengubah arsitektur (Go single binary, SQLite append-only, modul lazy, tool surface tetap). Setiap temuan punya perbaikan konkret dan lokal.

## Metode

- **Lima lane review paralel** (read-only), masing-masing dengan kontrak bukti `file:line` di kedua sisi: (1) angka/gate, (2) skema SQLite §11 vs semua rujukan tabel/kolom/enum, (3) status ADR + ID invariant P1–P29 + nama tool/perintah, (4) keterlacakan FEATURE-PARITY/GAP-SWEEP/BASELINE/SCORECARD/EDGE-CASES, (5) kelayakan fisik, lisensi, CGO.
- **Dicek langsung oleh reviewer:**
  - Placeholder (grep `TBD|TODO|FIXME|XXX|menunggu persetujuan|menunggu keputusan|belum diputuskan|placeholder`, di luar OPEN-QUESTIONS dan GAP-SWEEP yang bersifat audit).
  - Link relatif: 161 link markdown.
  - Diagram mermaid: `/tmp/mmd/check.mjs`.
  - Verifikasi ulang bukti kunci; ditandai ✔ di kolom Lokasi.
- **Duplikat antar-lane digabung.** Contoh: Σ binary, angka health/HEALTHCHECK, nama test P9, dan status Q19/Q20.
- **Klaim eksternal yang diverifikasi lane di sumber upstream, tidak dari repo ini:** lisensi `go.mau.fi/libsignal` = GPL-3.0 (pkg.go.dev) dan build tag `goolm` mautrix-go (docs.mau.fi). Klaim lain tanpa bukti di repo ditandai `[INFERENCE]`.

### Hasil pemeriksaan mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal parse (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 161 link, 0 rusak. Rujukan ke file yang belum ada (`THIRD_PARTY_NOTICES.md`, `CHANGELOG.md`, `SECURITY.md`, `docs/TRUST-MODEL.md`) adalah deliverable implementasi dan bukan link, jadi tidak dihitung rusak |
| Placeholder di luar OPEN-QUESTIONS | 4 sisa "menunggu persetujuan" yang sudah diputuskan: SPEC.md:86, SPEC.md:806, ROADMAP.md:50, plus SCORECARD.md:254 merujuk OPEN-QUESTIONS Q19/Q20 yang sudah tidak ada (m-11). 0 TBD/TODO/FIXME. `SELF-HEALING.md:303` = komentar enum, bukan placeholder |
| OPEN-QUESTIONS | 6 pertanyaan terbuka, semua bertanda "tidak memblokir freeze". Satu salah klaim cakupan (Q15, m-14) |
| LICENSE | Teks MIT standar lengkap, "Copyright (c) 2026 Damn-Bot contributors", konsisten dengan SPEC.md:3, DECISIONS.md:237, CROSS-CUTTING.md:128 |

### Angka cakupan / keterlacakan

| Cek | Hasil |
|---|---|
| Prefix FEATURE-PARITY (18 prefix, 854 baris) punya modul di SPEC §8 | 18/18 |
| Prefix FEATURE-PARITY punya fase di ROADMAP | 18/18 |
| Kolom per baris `modul/fase/status/test/bench` di FEATURE-PARITY | 0/854 (B-11) |
| ID GAP-SWEEP ada di SPEC §22.1 | 118/118, prioritas cocok 118/118 (P0 16 / P1 65 / P2 37), 0 duplikat, 0 ID asing |
| ID GAP-SWEEP ada di MITIGATION §10 (`GAP-*`) | 118/118, 0 duplikat |
| ID GAP-SWEEP ada di tabel fase ROADMAP | 118/118. Fase dicocokkan baris per baris hanya untuk F1 dan F9 (identik) |
| Gap P0 terukur yang punya baris SCORECARD §1.9 | 12/16; hilang SEC-05, SEC-10, SEC-22, GW-01 (M-26) |
| Kelemahan BASELINE §4–§8 yang punya baris MITIGATION | semua kecuali "aux model = model utama" (m-21) |
| Dimensi SCORECARD dengan metode ukur + ambang | semua; 2 baseline §1.8 salah label (m-22) |
| Invariant P1–P29 punya nama test CI | 29/29 bernama; 2 invariant punya nama/angka test ganda antar dokumen (m-08 P9, m-09 P19) |
| Kategori EDGE-CASES yang dirujuk nama di SPEC/MITIGATION | 3/10 (m-24) |
| ADR | 33 (ADR-001..033), tanpa nomor ganda/celah, semua Accepted. Teks basi ada di m-11, m-12, m-13 |
| Ladder migrasi SQLite | 0001→0007 berurutan tanpa celah; semua FK menunjuk tabel yang dibuat lebih dulu; DDL §11.7 identik dengan SELF-HEALING.md:295-325 |

## Temuan

Lokasi ditulis `fileA:line ↔ fileB:line`. ✔ berarti baris itu dibaca ulang oleh reviewer.

### BLOCKER

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| B-01 | BLOCKER | SPEC.md:291 ✔ ↔ docs/ENGINEERING.md:417 (E-40) ↔ docs/DECISIONS.md:237-239 (ADR-021) ↔ docs/SCORECARD.md:90 | `gateway.whatsapp` memakai `go.mau.fi/whatsmeow` (MPL-2.0), yang wajib bergantung pada `go.mau.fi/libsignal` berlisensi **GPL-3.0** (lane memverifikasi lewat pkg.go.dev v0.2.2; fork RadicalApp/libsignal-protocol-go, juga GPL-3.0). Binary full yang didistribusikan menjadi karya gabungan GPL-3.0. Ini bertentangan dengan ADR-021 (MIT, GPL dilarang) dan gate lisensi E-40 pasti merah. | Butuh keputusan user lewat ADR-034. Opsi A (disarankan): WhatsApp Web `dropped-with-approval` untuk 1.0, karena Cloud API sudah menjadi default wizard (GW-01). Hapus SPEC.md:291, baris whatsmeow di ENGINEERING.md:474/E-40, dan Q6 di ADR-032. Opsi B: `damnbot-wa-bridge` sebagai binary terpisah ber-GPL-3.0 dengan `go.mod` sendiri, berbicara lewat stdio JSON seperti daemon Q24. Kedua opsi juga membereskan M-01 (−8,0 MB). |
| B-02 | BLOCKER | SPEC.md:97 (P1: katalog ≤ 600 tok, fixture "65 MCP server tercache") ↔ SPEC.md:702 ✔ (satu baris per grup, `mcp:<server>` ≤ 12 kata) ↔ SPEC.md:263 ✔ (≤ 15 tok/grup) | Hitungan katalog tidak mungkin muat. 600 / 15 = maksimum 40 baris, padahal fixture P1 sendiri memuat 65 grup MCP: 65 × 15 = 975 tok. Dengan batas bawah realistis 7 tok/baris: 65 × 7 + ±18 grup modul × 12 ≈ 671 tok, tetap > 600. Spec tidak punya aturan collapse/overflow, sementara §9.6 mengklaim P1 "tidak terpengaruh jumlah server". Test P1 akan gagal by design. | Di SPEC.md:702, semua server MCP digabung jadi satu baris ≤ 60 tok (`- mcp: 65 servers (github, slack, … +N more); tool_search group="mcp:<name>"`), atau dibatasi K server teratas + `+N more`. Tambahkan `TestCatalogOverflow` (65 dan 200 server → katalog ≤ 600) ke kolom verifikasi P1. |
| B-03 | BLOCKER | SPEC.md:97 (context tier ≤ 1.100 tok, fixture "memory penuh") ↔ SPEC.md:574 ✔, :580 ✔ (memori `approved` ≤ 4.000 char/scope **di prompt**; `pinned` selalu masuk) ↔ SPEC.md:714 (snapshot ≤ 150 tok) ↔ SPEC.md:592 (skill ≤ 200 tok) | Cap context tier lebih kecil dari isi wajibnya. Satu scope penuh = 4.000 / 4 ≈ 1.000 tok, ditambah snapshot 150 dan skill 200 = 1.350 > 1.100, itu pun tanpa AGENTS.md. Sesi TUI biasa punya dua scope (`user` + `project`) ≈ 2.350 tok, lebih dari 2× cap. Aturan pemotongan hanya ada untuk AGENTS.md. | Ubah SPEC.md:574/:580 menjadi cap **penyimpanan**, lalu tambahkan sub-cap memori di prompt ≤ 600 tok lintas scope (urutan: pinned → skor BM25, penutup `[+N memories: recall]`). Tetapkan urutan potong context tier di SPEC.md:714: AGENTS.md → memori non-pinned → skill. Perbarui fixture P1. |
| B-04 | BLOCKER | SPEC.md:2041 (SEC-13), :2049 (SEC-21) ↔ SPEC.md:1457-1458 ✔ (CHECK `turn_journal.state`), :1477 (`idx_turn_journal_open`), :1599 (ALTER 0007) ↔ docs/SELF-HEALING.md:299-305,324 | SEC-13/SEC-21 menulis `turn_journal.state = 'awaiting_approval'`, tetapi CHECK di 0005 hanya mengizinkan `running, tool_pending, tool_done, awaiting_confirmation, crashed, resumed`. SQLite tidak bisa mengubah CHECK lewat ALTER, jadi INSERT approval tertunda pasti gagal. Index open-turn juga tidak mencakup state ini. SEC-13 menyebut kolom `approval_request` ada di 0005, padahal DDL-nya di 0007. | Tambahkan `'awaiting_approval'` ke CHECK dan ke WHERE index di SPEC.md:1458/:1477 dan salin ke SELF-HEALING.md:299-305/:324 (aman, belum ada rilis). Pindahkan `approval_request` ke DDL 0005 dan hapus ALTER di :1599, atau perbaiki rujukan SEC-13 menjadi 0007. |
| B-05 | BLOCKER | SPEC.md:2097 (COST-11), :2083 (GW-09) ↔ SPEC.md:1082 ✔ (CHECK `usage_ledger.purpose`), :388 (`type Purpose`) | COST-11 menyatakan CHECK `purpose` "diperluas" dengan `'tool'`, tetapi DDL masih `('turn','compaction','delegate','auto','media')` dan tipe `Purpose` belum punya `tool`. Metering tool berbayar dan SMS akan ditolak DB. | Tambahkan `'tool'` ke CHECK di SPEC.md:1082 dan ke komentar `type Purpose` (:388); dokumentasikan `feature = tool:<backend>` (:1083); tegaskan di P11 (:107) bahwa `tool` bukan LLM call. |
| B-06 | BLOCKER | SPEC.md:2088 (COST-02 `model_limits`), :2092 (COST-06 `provider_circuits`), :2104 (PROV-02) ↔ SPEC.md:1244-1306 (0002), :1541-1604 (0007); docs/MITIGATION-MATRIX.md:214 | Dua tabel mutable dinyatakan ada di "§11.3", tetapi tidak ada di 0002 maupun 0007 (grep ✔: hanya muncul di baris §22.1). SPEC.md:1543 justru mengklaim semua tabel §22 sudah didefinisikan di 0007. | Tambahkan DDL ke 0007: `model_limits(provider, model, context_window, source CHECK IN ('config','registry','discovery','learned','fallback'), observed_at, PRIMARY KEY(provider, model))` dan `provider_circuits(provider, credential_id, state CHECK IN ('closed','open','half_open'), open_until, remaining_req, remaining_tok, reset_at, updated_at, PRIMARY KEY(provider, credential_id))`. Ubah rujukan §11.3 menjadi §11.9 (SPEC.md:2088/:2092/:2104, MITIGATION:214). |
| B-07 | BLOCKER | SPEC.md:2064 (REL-08 `skipped_overlap`), :2099 (COST-13 `gated`) ↔ SPEC.md:1396 ✔ (CHECK `cron_runs.outcome`) | Dua outcome yang wajib di-INSERT (test REL-08 menghitung barisnya) tidak ada di CHECK `ok\|failed\|crashed\|missed\|skipped_safe_mode`. | Perluas CHECK di SPEC.md:1396 dengan `'skipped_overlap','gated'`. |
| B-08 | BLOCKER | SPEC.md:2064, :2065, :2072, :2099, :2100, :2105 ↔ SPEC.md:1369-1385 (`cron_jobs`) | Delapan kolom/constraint `cron_jobs` yang diwajibkan §22 tidak ada di DDL: `overlap`, `idempotent`, `pause_after_crashes`/status pause, `after`, `after_timeout_s`, `gate`, `prompt_sha` + `UNIQUE(prompt_sha, schedule, deliver_to)`, `model`, `tz`. DDL hanya punya `enabled`. | Lengkapi DDL 0004 (pra-rilis): `tz TEXT`, `model TEXT`, `prompt_sha TEXT NOT NULL`, `overlap TEXT NOT NULL DEFAULT 'skip' CHECK (overlap IN ('skip','queue_one','parallel'))`, `idempotent INTEGER NOT NULL DEFAULT 0`, `pause_after_crashes INTEGER NOT NULL DEFAULT 3`, `paused_at INTEGER`, `gate TEXT CHECK (gate IS NULL OR json_valid(gate))`, `after TEXT NOT NULL DEFAULT '[]'`, `after_timeout_s INTEGER NOT NULL DEFAULT 3600`, `UNIQUE(prompt_sha, schedule, deliver_to)`. |
| B-09 | BLOCKER | SPEC.md:2087 (COST-01 `pricing_version`) ↔ SPEC.md:1077-1096 (`usage_ledger`); SPEC.md:2105 (PROV-03 `gateway_identities.tz`) ↔ SPEC.md:1313-1324 | Kolom `usage_ledger.pricing_version TEXT NOT NULL` dan `gateway_identities.tz` disebut sebagai bagian §11.2/§11.4, tetapi tidak ada di DDL. Keduanya ada di jalur budget guard (P27) dan volatile tier. | Tambahkan kedua kolom ke DDL 0001/0003 (pra-rilis). `tz` bernilai NULL = zona host. |
| B-10 | BLOCKER | SPEC.md:937-966 (§10, 28 perintah top-level) ↔ SPEC.md:2135 (OPS-08: ≤ 20 + pemetaan) ↔ docs/DECISIONS.md:397 (ADR-033 Q45) ↔ SPEC.md:2150, :2177 | Tabel CLI normatif masih 28 perintah (`actions`, `rollback`, `modules`, `budget`, `insights`, `why`, `scorecard`, `gc`, `purge`, `debug`, …), padahal OPS-08 yang sudah Accepted menetapkan ≤ 20 perintah dengan pemetaan (`actions`→`undo ls`, `why`→`sessions why`, `gc`→`doctor gc`, …). Implementer yang mengikuti §10 pasti gagal `TestCLITopLevelCount`. | Tulis ulang §10 sesuai daftar final OPS-08 (gabung M-20 dan m-18). Selaraskan rujukan lama: CROSS-CUTTING.md:79,82,88; EXEC-AND-CODING.md:421-422; SELF-HEALING.md:338; MITIGATION-MATRIX.md:70; DECISIONS.md:51,323; SPEC.md:376 (`damnbot why`). |
| B-11 | BLOCKER | docs/FEATURE-PARITY.md:73 ✔ (header 18 tabel: `ID \| Fitur \| Deskripsi \| Sumber \| Default \| Dependensi \| Catatan`) ↔ SPEC.md:787 ✔, docs/ROADMAP.md:31, docs/SCORECARD.md:75,254, docs/DECISIONS.md:243-269 | SPEC §8 menyatakan setiap baris parity punya `modul`, `fase`, `status`, `test`, `bench`, tetapi 854/854 baris tidak punya kolom itu. Yang kritis untuk freeze: baris `dropped-with-approval` (ADR-022 NP-01..19, NP-21) tidak bisa diidentifikasi karena NP tidak dipetakan ke ID baris (mis. NP-02 → CLI-019, NP-19 → OPS-014/…, NP-21 → MEM-006/MEM-007/PLUG-022). Akibatnya scope 1.0 dan penyebut gate [I] "854 − dropped" tidak terdefinisi. | Minimal sebelum freeze: tambahkan kolom `status` di 18 tabel dan kolom "Baris parity" di tabel ADR-022 (DECISIONS.md:245). `modul`/`fase` bisa diturunkan dari tabel prefix ROADMAP (nyatakan eksplisit). `test`/`bench` boleh diisi per fase; tulis aturan itu di SPEC.md:787. |

### MAJOR

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| M-01 | MAJOR | SPEC.md:271-299 (Σ kolom "Binary maks") ↔ SPEC.md:300, :109 (P13 ≤ 52 MB) ↔ docs/DECISIONS.md:404 (ADR-033 "tetap ≤ 52 MB") | Jumlah budget binary 29 baris modul = **56,2 MB** > 52 MB. Setiap modul bisa lolos budgetnya sendiri sementara P13 gagal. Tidak ada teks yang menyatakan batas per modul adalah plafon individual. | Bila B-01 opsi A/B dipilih: Σ = 48,2 MB; perbarui catatan ADR-033. Bila tidak: turunkan baris (mis. 26 adapter 5,0 → 3,0, core 14,0 → 13,4) atau naikkan P13 lewat ADR. Tambahkan assert `Σ budget ≤ P13` di `make size-check`. |
| M-02 | MAJOR | SPEC.md:271-299 (Σ cap LOC = 185.500) ↔ docs/ROADMAP.md:22, :196 (rencana 186.500) ↔ SPEC.md:300, docs/DECISIONS.md:404 (≈ 186.000) | Rencana LOC melebihi jumlah cap per modul sebanyak 1.000, jadi menurut rencana sendiri minimal satu gate P21 per modul gagal. Ada tiga angka "rencana": 185.500, 186.000, 186.500. | Pilih satu angka. Naikkan cap modul penyerap GAP (mis. core 30.000 → 31.000) atau pangkas rencana ke ≤ 185.500. Selaraskan SPEC.md:300, ROADMAP.md:22/:196, DECISIONS.md:404. |
| M-03 | MAJOR | docs/ENGINEERING.md:474 ("24 awal … 21 runtime … = 27") ↔ docs/CODE-TOOLS.md:273 (`golang.org/x/text/unicode/norm`), :355 (`.tar.zst`) ↔ SPEC.md:97, :1845 (tokenizer o200k test-only) | Daftar runtime hanya berisi 20 modul, bukan 21 (20 + 3 test + 3 ADR-033 = 26). Ada dependency langsung yang tidak terdaftar: `golang.org/x/text` (impor langsung), decoder zstd (stdlib tidak punya; kandidat `klauspost/compress`) [INFERENCE], dan tokenizer o200k (E-45 ikut menghitung dependency test). 26 + 2 = 28 (penuh), tambah tokenizer = 29 > 28. | Koreksi hitungan di ENGINEERING.md:474 dan daftarkan `x/text`. Ganti `.tar.zst` → `.tar.gz` (stdlib) di CODE-TOOLS.md:355/ADR-015. Pindahkan `bench/budget` + tokenizer ke `go.mod` terpisah (pola `interop/go.mod`) dan tulis eksplisit bahwa itu di luar E-45. |
| M-04 | MAJOR | SPEC.md:292 ✔, :879 (Matrix E2EE `goolm`) ↔ docs/ENGINEERING.md:416 (E-39 `CGO_ENABLED=0 go build ./...`), SPEC.md:109 | mautrix-go memakai libolm via cgo secara default; implementasi pure-Go baru aktif dengan build tag `goolm` (lane memverifikasi di docs.mau.fi). Tidak ada perintah build/test/size di spec yang memasang tag itu. Dengan `CGO_ENABLED=0` tanpa tag, backend Olm tidak terdaftar: build gagal atau E2EE error saat runtime. Kepatuhan ADR-001 jadi ambigu. | Tetapkan `GOFLAGS=-tags=goolm` di Makefile dan semua job (E-39, E-65, size-check, test, lint, deadcode). Tambahkan file guard `//go:build !goolm` yang sengaja gagal compile. Catat di baris Matrix ENGINEERING §4.2. |
| M-05 | MAJOR | docs/ENGINEERING.md:472 (E-50: `THIRD_PARTY_NOTICES.md` = output `go-licenses report`, diff = 0) ↔ docs/DECISIONS.md:207, docs/CROSS-CUTTING.md:129 (atribusi hermes di file yang sama) ↔ docs/CODE-TOOLS.md:355-359 (grammar WASM di-embed) | (1) File hasil generate dengan diff = 0 tidak bisa memuat atribusi hermes (bukan modul Go), jadi kedua aturan saling meniadakan. (2) `go-licenses report` hanya mengeluarkan CSV, tanpa teks MIT/BSD dan tanpa NOTICE Apache-2.0 (wazero, yaml). (3) Grammar WASM non-Go tidak tertangkap E-40/E-50. (4) Tidak ada aturan pengiriman notice dalam arsip/binary maupun lokasi source untuk file MPL (MPL-2.0 §3.2). | Ganti E-50: `go-licenses save`/`report --template` berisi teks lengkap + NOTICE, digabung `third_party/manual/*.txt` (hermes, grammar, runtime tree-sitter). Embed di binary (`damnbot licenses` atau subperintah `doctor` sesuai OPS-08) dan sertakan di arsip rilis. Tambahkan paragraf source MPL. Test baru `TestNoticesComplete`. |
| M-06 | MAJOR | SPEC.md:837, :288 (voice mode TUI, "backend eksternal") ↔ docs/DECISIONS.md:11 (ADR-001) ↔ docs/FEATURE-PARITY.md:828 | Mekanisme capture mikrofon dan playback audio di TUI tidak dispesifikasikan. Library audio Go yang lazim (portaudio, malgo, oto/ALSA) butuh cgo [INFERENCE], sementara target latensi ≤ 200 ms tetap mengikat. | Tetapkan di SPEC.md:837: capture dan playback lewat executable eksternal yang dideteksi `voice doctor` (`pw-record`/`arecord`/`sox`/`ffmpeg -f avfoundation\|dshow`; `pw-play`/`aplay`/`afplay`/`ffplay`), selain itu `module_unavailable`. Ukur waktu spawn di `TestVoicePipelineLatency`. |
| M-07 | MAJOR | SPEC.md:2096 (COST-10: Starlark "≤ 256 MiB … via `Thread.SetMaxExecutionSteps`") ↔ SPEC.md:820 ↔ SPEC.md:114 (P18 `serve` ≤ 90 MB, peak ≤ 300 MB) | `SetMaxExecutionSteps` membatasi jumlah langkah, bukan byte. Go tidak punya batas heap per goroutine (`debug.SetMemoryLimit` bersifat lunak dan berlaku untuk seluruh proses). Kuota 256 MiB in-process tidak bisa ditegakkan, dan satu skrip bisa menjebol P18. | Jalankan Starlark di proses anak (`damnbot __starlark`) dengan RLIMIT_AS / Job Object 256 MiB seperti §B.7, atau hapus klaim MiB dan ganti dengan "≤ N langkah + cap ukuran nilai" + test RSS skrip agresif. |
| M-08 | MAJOR | docs/ENGINEERING.md:416 (E-39), :518 (E-65): 4 target ↔ docs/CROSS-CUTTING.md:96-99, docs/SCORECARD.md:79 (6 target), SPEC.md:2060 (REL-04 "6 target") | Ada empat matriks build berbeda. Build pure-Go (ADR-001) hanya di-gate di linux/amd64, linux/arm64, darwin/arm64, windows/amd64. darwin/amd64, windows/arm64, linux/arm, android/arm64 tidak pernah dibuktikan, padahal SCORECARD "Dukungan OS" menuntut CI hijau di sana. | Satu daftar target rilis kanonik (mis. 8: linux/{amd64,arm64,arm}, darwin/{amd64,arm64}, windows/{amd64,arm64}, android/arm64) di ENGINEERING E-39/E-65; SCORECARD.md:79 dan REL-04 merujuk ke daftar itu. |
| M-09 | MAJOR | docs/MITIGATION-MATRIX.md:71 (B-03 "Image ≤ 60 MB (RC)") ↔ SPEC.md:2139 (INF-03), docs/DECISIONS.md:401 (Q49 "≤ binary + 2 MB (≤ 54 MB), terkompresi ≤ 23 MB"), docs/SCORECARD.md:169 | Target ukuran image Docker berbeda, tanpa penjelasan. | Ganti target B-03 di MITIGATION-MATRIX.md:71 dengan angka Q49. |
| M-10 | MAJOR | docs/ENGINEERING.md:449 ✔ (acp 1.800 LOC), docs/DECISIONS.md:135 ✔ (9.000 LOC JS/CSS), SPEC.md:2203-2225 (§22.4: P21 160.000, heal 4.000, media 3.000/0,6 MB, gateway 7.000, core 22.000) ↔ SPEC.md:117, :271-300 (190.000; 10.000 JS/CSS; acp 2.500; heal 5.500; media 9.500/1,2 MB; gateway 10.500; core 30.000) | Angka pra-ADR-033 masih tampil normatif tanpa penanda. Catatan "angka lama" di SPEC.md:2150 hanya mencakup §22.2. §22.4 juga diawali backtick yatim ("- ` (estimasi…"). | Perbarui ENGINEERING.md:449 (2.500, ADR-033) dan DECISIONS.md:135 (10.000, ADR-033). Beri judul §22.4 "analisis pra-ADR-033 (historis)" atau perbarui angkanya; perbaiki backtick. |
| M-11 | MAJOR | docs/SELF-HEALING.md:249, :577 (P22-5 "tick 1 s") ↔ docs/DECISIONS.md:402 (Q50), SPEC.md:2102 (COST-16: adaptif 1 s / 10 s, wake-up ≤ 60/menit), SPEC.md:2172 | Test watchdog P22-5 dengan tick tetap 1 s = 60 wake-up/menit dari watchdog saja, bertentangan dengan gate COST-16. | Ubah SELF-HEALING.md:249/:577 menjadi "tick adaptif 1 s (op in-flight) / 10 s (idle), ADR-033 Q50" dan tambahkan assert wake-up ≤ 60/menit di P22-5. |
| M-12 | MAJOR | SPEC.md:1833 (§15) ↔ SPEC.md:2073 (REL-17) ↔ SPEC.md:2050 (SEC-22 `HEALTHCHECK … health --ready`) ↔ SPEC.md:2139 (INF-03 `doctor --quick`) | Dua bagian normatif memberi angka health berbeda. `/healthz`: "≤ 5 ms" vs "sampler ≤ 3 s". `/readyz`: lag poll Telegram < 90 s vs ≤ 120 s; outbox "backlog < 1.000" vs "queued tertua ≤ 300 s"; "≥ 1 provider tidak open" vs "provider primer tidak open". Perintah HEALTHCHECK ada tiga versi (`/healthz`, `health --ready`, `doctor --quick`). Nama test juga dua: `TestReadyzFaultInjection` vs `TestReadyz`. | REL-17 (SPEC.md:2073) jadi satu-satunya sumber angka; §15 (:1833) cukup merujuk. Satu perintah HEALTHCHECK (nama hasil pemetaan OPS-08 untuk `health --ready`) di SEC-22 dan INF-03. Satu nama test. |
| M-13 | MAJOR | SPEC.md:1612-1621 (§12 "contoh lengkap = default") ↔ SPEC.md:2063 (REL-07 `turn.max_wall_s`), docs/DECISIONS.md:392 (Q40: 900 s) | Timeout turn 900 s (angka gate) tidak ada di §12, dan section `[turn]` tidak dikenal di §12 sehingga validasi config akan menolaknya. Hubungan `provider.timeout_s = 120` dengan TTFB REL-07 (25 s + 5 s/32K, maksimum 90 s) dan idle 60 s tidak dijelaskan. | Tambahkan `max_wall_s = 900` di `[loop]` §12 dan ganti `turn.max_wall_s` di SPEC.md:2063 menjadi `loop.max_wall_s`. Beri komentar arti `provider.timeout_s` atau hapus key itu bila sudah digantikan REL-07. |
| M-14 | MAJOR | SPEC.md:2137-2138 (INF-01/02: FTS contentless + `fts_meta`, `blob_fts`), :1606 ↔ SPEC.md:1193-1226 (FTS `content='events'` + trigger mentah), :1446 (purge), :981 (checksum migrasi) | Catatan :1606 menyatakan FTS contentless + `dmb_fts_norm`, tetapi DDL 0001 masih external-content. Tabel `fts_meta`/`blob_fts` tidak ada, dan purge tidak menghapus FTS. Migrasi dikunci checksum setelah rilis, jadi DDL harus final sekarang. | Tulis ulang blok FTS SPEC.md:1193-1226 (`content=''`, `contentless_delete=1`, trigger `dmb_fts_norm(...)`); tambahkan `fts_meta` + `blob_fts`; perbarui purge :1446; hapus catatan :1606. |
| M-15 | MAJOR | docs/ROADMAP.md:102 (F2 "migrasi 0001–0006") ↔ SPEC.md:1541-1604 (0007), :2103 (PROV-01 tabel dibuat F2) | Deliverable F2 berhenti di 0006, padahal 0007 berisi kolom core recovery dan tabel PROV-01 yang wajib ada di F2. | Ubah ROADMAP.md:102 menjadi "0001–0007". |
| M-16 | MAJOR | SPEC.md:105 (P9 "0 UPDATE/DELETE pada tabel append-only") ↔ SPEC.md:1011 (`session_labels` tanpa trigger), :1228-1241, :1536-1538, :1603; SPEC.md:544 (gc menghapus action journal) | `session_labels` ditandai append-only tanpa trigger. Sembilan tabel append-only lain (`event_attachments`, `file_snapshots`, `approvals`, `memory`, `memory_decisions`, `actions`, `action_undos`, `plans`, `reasoning_carrier_rejections`) hanya punya trigger no-UPDATE, tanpa no-DELETE, sehingga `TestImmutableTables` tidak bisa lulus untuk DELETE. Selain itu gc di :544 menghapus baris `actions` yang append-only. | Tambahkan trigger `*_no_delete` untuk semua tabel itu, plus no-update/no-delete untuk `session_labels` (purge sudah DROP/CREATE trigger, :1446). Ubah :544 menjadi "gc hanya blob yang ditahan aksi kedaluwarsa". |
| M-17 | MAJOR | SPEC.md:364-377 ✔ (§3.5) ↔ SPEC.md:2093 (COST-07 `no_progress`), :780 (`invalid_answer`), :2194 (P2), :371 (`empty_response` "setelah 1 nudge") vs :2101 (COST-15 "tanpa nudge") | `no_progress` dan `invalid_answer` dipakai tetapi tidak ada di tabel stop reason, sehingga tidak punya pemetaan ACP dan assert P2 "tepat satu stop_reason" ambigu. Definisi `empty_response` (nudge) bertentangan dengan COST-15. | Tambahkan baris `no_progress` (→ `end_turn` + `_meta`) dan `invalid_answer` di §3.5; ganti :371 menjadi "setelah 1 retry tanpa nudge (COST-15)". |
| M-18 | MAJOR | SPEC.md:125 (P29 event `dlp_blocked`), :2118 (UX-01 `acp_fs_fallback`), :2090 (COST-04 "ledger `payload`") ↔ SPEC.md:1032-1039 (CHECK `events.kind`), :1077-1096 | Dua event kind yang diwajibkan test (`TestEgressDLP`, UX-01) tidak ada di CHECK `events.kind`, sehingga append gagal. Kolom `usage_ledger.payload` (COST-04) tidak ada. | Tambahkan `'dlp_blocked','acp_fs_fallback'` ke CHECK, atau nyatakan keduanya sebagai `system_notice{kind}`. COST-04: pindahkan ke payload event `llm_call` atau tambahkan kolom `payload` JSON. |
| M-19 | MAJOR | SPEC.md:580 (memori `pinned`), :884 (kanban "berlabel `agent`"), :2049 (token `mcp serve` per klien) ↔ SPEC.md:1124-1142, :1407-1419, §11 | Tiga state persisten tidak punya kolom/tabel: pin memori (tabel `memory` immutable, tanpa `pinned`), label kanban, dan token klien `mcp serve` beserta scope. | Tambahkan status `pinned`/`unpinned` di `memory_decisions` (atau tabel `memory_pins`); `kanban_tasks.labels` atau definisikan "berlabel agent" = `assignee='agent'`; tabel `mcp_serve_tokens(id_hash PK, profile, identity, scope CHECK IN ('read','send'), created_at, revoked_at)` di 0007. |
| M-20 | MAJOR | SPEC.md:2135 (OPS-08: 20 perintah) ↔ SPEC.md:2029 (`backup`), :2113 (`tools fetch`), :2050/:2073 (`health`), :2046 (`auth logout`, `uninstall --purge`), :2045 (`debug bundle`), :2035/:2037/:2127 (`web open`, `web sessions revoke`), :2130 (OPS-03 `setup --uninstall`) | Baris §22.1 yang sudah disetujui menambah top-level `backup`, `tools`, `health`, `uninstall`, `auth` tanpa pemetaan OPS-08. `uninstall --purge` dan `setup --uninstall` adalah pasangan duplikat, padahal OPS-08 mensyaratkan 0 duplikat. | Lengkapi pemetaan OPS-08 (mis. `backup` → `sessions backup`, `tools` → `doctor tools`, `health` → `doctor health`, `uninstall` → `setup --uninstall [--purge-data]`, `auth logout` → `setup auth logout`, `debug bundle` → `doctor bundle`, `web …` → `serve open\|sessions`), lalu ubah SEC-01/07/09/17/18/22, TOOL-06, REL-17, UX-10 ke nama final. |
| M-21 | MAJOR | SPEC.md:78, docs/ROADMAP.md:176, docs/ENGINEERING.md:550 ("P1–P28") ↔ SPEC.md:93, :125 (P29) | Gate merge per modul, gate rilis, dan aturan perubahan invariant masih menulis P1–P28, jadi P29 (privasi log & egress DLP) tidak ikut gate. | Ganti menjadi "P1–P29" di ketiga lokasi. |
| M-22 | MAJOR | SPEC.md:2127 (UX-10 "+ QR terminal") ↔ docs/DECISIONS.md:400 (Q48 "Tanpa QR di 1.0"), docs/MITIGATION-MATRIX.md:237 | Requirement 1.0 masih mewajibkan fitur yang diputuskan tidak ada di 1.0. | Hapus "+ QR terminal" di SPEC.md:2127 dan "QR" di MITIGATION-MATRIX.md:237. |
| M-23 | MAJOR | SPEC.md:2169 (§22.2 GW-01: `allow_cold_outbound = true` dengan ack kedua, "Butuh persetujuan user") + :2150 ("setiap Usulan = final") ↔ docs/DECISIONS.md:386 (Q34: cold outbound dilarang total), SPEC.md:879, :2075 | Catatan :2150 membuat usulan GW-01 normatif, padahal isinya masih mengizinkan cold outbound, bertentangan dengan Q34 dan §9.7. | Ganti klausa di SPEC.md:2169 dengan "Final: ADR-033 Q34 (cold outbound dilarang total)"; di :2075 ganti "ADR baru yang mengamandemen ADR-032 Q6" dengan "ADR-033 Q34". |
| M-24 | MAJOR | SPEC.md:2184 ✔ (§22.3 "diterapkan saat implementasi fase"), :2150 ↔ SPEC.md:91-127 (§2), :1868-1895 (§17.1), :362-377 (§3.5) | Perubahan invariant yang sudah disetujui (P2 stop reason baru, P5 agregat `min(24.000, 0,30 × window)`, P14, P19, P22, P24, P25 admission control, P26, §17.1 CPU idle & keystroke→echo) sengaja tidak dimasukkan ke §2/§17.1. Akibatnya ada dua sumber normatif dan teks §2 yang tercetak tidak final. Ini akar masalah B-04..B-10 dan banyak MAJOR di atas: §22.1/§22.2 menetapkan perubahan yang tidak masuk ke §3.5/§9/§10/§11/§12. | Sebelum freeze, terapkan seluruh §22.2/§22.3 ke bagian targetnya (§2, §3.5, §9, §10, §11, §12, §17.1), lalu ubah §22.2/§22.3 menjadi log keputusan historis. Tambahkan test dokumen `TestSpecNoPendingAmendments` (grep "diterapkan saat implementasi", "Usulan:" di luar bagian historis = 0). |
| M-25 | MAJOR | SPEC.md:832 (§9.4 daftar tool browser) ↔ SPEC.md:2168 (SEC-05: "§9.4 harus ditambah `browser_cdp`"), :2033 | Resolusi untuk gap P0 SEC-05 belum diterapkan: `browser_cdp` (parity TOOL-017) tidak ada di daftar tool §9.4, padahal allowlist method & approval-nya ditetapkan untuk tool itu. | Tambahkan `browser_cdp` (deferred, approval wajib untuk method sensitif) di SPEC.md:832. |
| M-26 | MAJOR | docs/SCORECARD.md:132-170 (§1.9) ↔ SPEC.md:2021 ("yang terukur punya baris di SCORECARD §1.9"), :2033, :2038, :2050, :2075 | Empat gap P0 dengan metrik numerik di §22.1 tidak punya baris SCORECARD: SEC-05, SEC-10, SEC-22, GW-01. Tabel §1.9 juga tidak punya kolom "Cara ukur"/"Baseline hermes" seperti §1.1–§1.8. | Tambahkan 4 baris tersebut dan kolom "Cara ukur" (nama test dari kolom Verifikasi §22.1, mis. `TestIRCAccountAuth`). |

### MINOR

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| m-01 | MINOR | docs/SELF-HEALING.md:354 ↔ SPEC.md:1664 | Enum `recovery.continue` berbeda: `safe_auto \| ask \| never` vs `off \| ask \| safe_auto`. | Ganti `never` menjadi `off` di SELF-HEALING.md:354. |
| m-02 | MINOR | docs/ROADMAP.md:23 ↔ docs/ROADMAP.md:21, :196 | Estimasi LOC test ≈ 120.000–180.000 dihitung dari 149.000, padahal total source sekarang ≈ 186.500. | Ubah menjadi ≈ 150.000–225.000. |
| m-03 | MINOR | SPEC.md:2032 (SEC-04 pin mencakup `annotations`) ↔ SPEC.md:1574; SPEC.md:2037 (SEC-09 kolom `user`) ↔ SPEC.md:1584 (`principal`) | Komentar `pin_sha` tidak memuat `annotations`; nama kolom berbeda. | Tambahkan `‖ annotations` di :1574; ganti `user` menjadi `principal` di :2037. |
| m-04 | MINOR | SPEC.md:2041 ("→ `denied`") ↔ SPEC.md:~1117 (CHECK `approvals.decision`); docs/SELF-HEALING.md:355 ("→ `aborted`") ↔ SPEC.md:1458, §3.5 | `denied` dan `aborted` bukan nilai enum yang ada. | SEC-13: `decision='timeout'`. SELF-HEALING:355: `turn_journal.state='crashed'` + `approvals.decision='timeout'`. |
| m-05 | MINOR | docs/SELF-HEALING.md:408 ↔ SPEC.md:1339-1347; SPEC.md:868 ↔ SPEC.md:1346 | Pseudo-SQL inbound Telegram tidak mengisi `sender_id` (NOT NULL). §9.7 menyebut PK `(platform, update_id)`, padahal kolomnya `external_id = 'u:'‖update_id`. | Tambahkan `sender_id` di SELF-HEALING:408; ubah SPEC.md:868. |
| m-06 | MINOR | SPEC.md:1272 (`tool_catalog.effect` memuat `'dynamic'`) ↔ SPEC.md:464 (`Effect` hanya read/idempotent/non_idempotent) | Nilai `dynamic` tidak didefinisikan di kontrak §4.3. | Definisikan di §4.3 (ditentukan per call lewat classifier; journal mencatat kelas hasil resolusi) atau hapus dari CHECK. |
| m-07 | MINOR | docs/CROSS-CUTTING.md:22 ↔ SPEC.md:2103, :2170 (PROV-01) | Kolom `reasoning_carriers.payload` tidak masuk daftar kolom terenkripsi at-rest. | Tambahkan di CROSS-CUTTING.md:22. |
| m-08 | MINOR | docs/MITIGATION-MATRIX.md:35 (`TestEventsImmutable`) ↔ SPEC.md:105 (P9 `TestImmutableTables`) | Dua nama test untuk invariant P9 yang sama. | Pakai `TestImmutableTables` di MITIGATION-MATRIX.md:35. |
| m-09 | MINOR | SPEC.md:115 (P19 `TestKill9Chaos`, 200 kill), docs/EXEC-AND-CODING.md:321, docs/MITIGATION-MATRIX.md:55 ↔ docs/SELF-HEALING.md:556 (`TestChaosKill9Turns`, 100×) | Dua nama test dan dua jumlah iterasi untuk properti "0 korupsi setelah kill -9". | Satu test (mis. `TestKill9Chaos`, 200 kill) didefinisikan di SELF-HEALING §9 dan dirujuk di semua lokasi. |
| m-10 | MINOR | SPEC.md:2171, :2078 (GW-04 "atau goldmark"), :2108 (TOOL-01 "bila slot tersedia") ↔ docs/DECISIONS.md:385 (Q33), docs/ENGINEERING.md:474 | Pilihan library masih bersyarat, padahal Q33 sudah final menambah `ledongthuc/pdf` dan `goldmark`. | Tetapkan kedua library di GW-04/TOOL-01; tandai bullet :2171 sebagai digantikan Q33. |
| m-11 | MINOR | SPEC.md:86 ✔, :806 ✔, docs/ROADMAP.md:50 ✔, docs/SCORECARD.md:254 ✔, :271, docs/DECISIONS.md:241 (judul NP-01..NP-20), docs/FEATURE-PARITY.md:1007-1030 ↔ docs/DECISIONS.md:243 (ADR-022 Accepted), :267 (NP-21), :361-362 | Placeholder sisa: "menunggu persetujuan drop (Q19)", "drop berlaku setelah persetujuan Q19/Q20", "menunggu persetujuan Q19/Q20", dan SCORECARD menyatakan persetujuan tercatat di OPEN-QUESTIONS (yang sekarang tidak memuat Q19/Q20). Judul ADR-022 dan tabel NP di FEATURE-PARITY belum memuat NP-21. | Ganti dengan "`dropped-with-approval` (ADR-022, Accepted)" dan rujuk DECISIONS.md:361-362; judul ADR-022 menjadi NP-01..NP-21; tambahkan NP-21 + kolom keputusan di FEATURE-PARITY. |
| m-12 | MINOR | SPEC.md:20 ↔ docs/DECISIONS.md:345, :377 | Indeks dokumen menulis "ADR-001..031", padahal ADR-032/033 ada dan normatif. | Ubah menjadi "ADR-001..033". |
| m-13 | MINOR | SPEC.md:2160 ↔ docs/DECISIONS.md:327-333 (ADR-030 = freeze), :377-388 (ADR-033 Q36) | SEC-02 merujuk "ADR integrasi GAP-SWEEP (ADR-030)" yang salah nomor. | Ganti menjadi "ADR-033 Q36". |
| m-14 | MINOR | docs/OPEN-QUESTIONS.md:5 ↔ docs/OPEN-QUESTIONS.md:15, docs/DECISIONS.md:347 | "Q6–Q30 diputuskan" padahal Q15 masih terbuka. | Ubah menjadi "Q6–Q14, Q16–Q30". |
| m-15 | MINOR | docs/CODE-TOOLS.md:550 ("CT-01…CT-28") ↔ SPEC.md:119 (P23 CT-01..36), docs/CODE-TOOLS.md:574-580 | Gate P23 di CODE-TOOLS tidak mencakup CT-30..36. | Ubah menjadi CT-01…CT-36. |
| m-16 | MINOR | docs/CODE-TOOLS.md:20 ("Exact only (P4)") ↔ SPEC.md:100 (P4 = tool tak dikenal/fuzzy nama/hasil sintetis) | Makna P-ID salah rujuk. | Ganti menjadi "(ADR-005, P23)". |
| m-17 | MINOR | SPEC.md:653 (`todo` efek `read`) ↔ docs/SELF-HEALING.md:287 (`todo` `idempotent`) | Kelas efek tool yang sama berbeda, padahal kelas ini menentukan paralelisme dan recovery. | Pakai `idempotent` (tool menimpa state daftar) di SPEC.md:653. |
| m-18 | MINOR | docs/DECISIONS.md:366, SPEC.md:2075, :2032, :2049, :2117, :2060, docs/SELF-HEALING.md:210, docs/EXEC-AND-CODING.md:501, SPEC.md:2051 ↔ SPEC.md:952-966 (§10) | Subcommand/flag dirujuk tetapi tidak ada di §10: `gateway doctor`, `gateway whatsapp-web enable`, `mcp review`, `mcp serve token create`, `skills list --unavailable`, `version --verbose`, `service install --dry-run`, hidden `__exec-shim`, `exec ssh add`. | Masukkan ke §10 baru (sub-baris per perintah). Tambahkan `__exec-shim` ke daftar hidden OPS-08. `exec ssh add` → `setup ssh add`. |
| m-19 | MINOR | SPEC.md:793, :798, :802 ↔ SPEC.md:787 | Nilai status `core` bukan anggota enum status (`core-eager` / …). | Ganti menjadi `core-eager`. |
| m-20 | MINOR | SPEC.md:2029-2146 (§22.1), docs/ROADMAP.md:186-194, docs/SCORECARD.md:132 ↔ docs/MITIGATION-MATRIX.md:139 (`GAP-`), docs/FEATURE-PARITY.md:953 (GW-001) | ID gap tanpa prefiks (GW-01, TOOL-01, PROV-01, OPS-01) hampir sama dengan ID parity (GW-001, TOOL-001, …), hanya beda jumlah digit. | Pakai prefiks `GAP-` di SPEC §22.1, ROADMAP, dan SCORECARD §1.9, konsisten dengan MITIGATION §10. |
| m-21 | MINOR | docs/BASELINE-hermes.md:74, :65-67 ↔ docs/MITIGATION-MATRIX.md:16 (T-04) | Kelemahan baseline "aux model = model utama" tidak punya baris mitigasi. Nilai ambang hermes (threshold 0,50, `protect_last_n` 20, `target_ratio` 0,20) hanya tersirat. | Tambahkan T-14 (routing `Purpose` → model murah, SPEC §4.1) dan lengkapi T-04 dengan nilai ambang hermes vs `compact_at` 0,75. |
| m-22 | MINOR | docs/SCORECARD.md:122, :124 ↔ docs/SCORECARD.md:117 | Bagian §1.8 berjudul "tidak ada di hermes", tetapi baseline Preview dan Budget guard ditulis "diukur di F0" dengan test Go yang tidak bisa dijalankan terhadap hermes. | Ganti baseline menjadi "tidak ada (target absolut)". |
| m-23 | MINOR | docs/ENGINEERING.md:538 (E-76 gate F8) ↔ SPEC.md:1856 (gate F9) | Fase gate regresi edge-case berbeda. | Ubah ENGINEERING.md:538 menjadi F9. |
| m-24 | MINOR | docs/EDGE-CASES-hermes.md:9-18, :52 ↔ SPEC.md:1846, :1856, docs/MITIGATION-MATRIX.md | Hanya 3/10 kategori edge-case (EC-PROV, EC-STATE, EC-PLAT) yang dirujuk nama di SPEC; MITIGATION tidak merujuk satu pun. Tabel tidak punya kolom status/test, jadi "166/166 covered/n/a" tidak bisa dilacak. | Tambahkan kolom `status \| test` di setiap tabel EDGE-CASES; pecah baris SPEC §16 per kategori. |
| m-25 | MINOR | docs/DECISIONS.md:122, SPEC.md:1736 (`damnbot-desktop` webview CGO) ↔ docs/ENGINEERING.md:416 (E-39 `go build ./...`) | Lokasi source wrapper ber-CGO tidak ditetapkan. Bila berada di modul utama, E-39 gagal dan dependency webview terhitung E-45. | Tetapkan `desktop/go.mod` terpisah, di luar E-39/E-45/size-check (tulis di ADR-011). |
| m-26 | MINOR | docs/FEATURE-PARITY.md:252, :693 (`/paste` gambar) ↔ SPEC.md:2123 (UX-06 OSC 52 teks) ↔ docs/DECISIONS.md:11 | Cara membaca gambar dari clipboard tidak dispesifikasikan. Library lazim butuh cgo [INFERENCE]. | Tetapkan executable eksternal (`wl-paste`, `xclip -t image/png -o`, `osascript`, PowerShell `Get-Clipboard -Format Image`), selain itu `module_unavailable`. |
| m-27 | MINOR | SPEC.md:2113 (TOOL-06 "daftar mirror") ↔ docs/ENGINEERING.md:417 (E-40) | Mirror milik proyek untuk ffmpeg (sering GPL/LGPL) dan Lightpanda (AGPL-3.0 [INFERENCE]) memicu kewajiban source-offer yang tidak diatur. | `tools.lock` hanya memuat URL rilis resmi upstream; mirror proyek hanya untuk artefak permisif atau wajib meng-host source; tampilkan lisensi saat `fetch`. |
| m-28 | MINOR | docs/ENGINEERING.md:417 (`TestMPLIsolation`: MPL hanya whatsmeow, mautrix) | Modul transitif `go.mau.fi/util` (MPL-2.0 [INFERENCE]) tidak ada di allowlist MPL. | Daftarkan semua modul MPL transitif secara eksplisit di E-40. |

## Dicek dan konsisten

- **P1 token:** total ≤ 5.000 = stable 1.200 + katalog 600 + context 1.100 + volatile 100 + schema eager 2.000. Angkanya konsisten di SPEC §2, §3.2.3, §4.5, §5.1, §5.2, §6, §17.1, MITIGATION T-01/T-08, ADR-012, ROADMAP, SCORECARD, EXEC-AND-CODING:331/400. Schema 10 tool × ≤ 200 tok = 2.000 (estimasi char/4 ≈ 1.250 tok). Yang tidak muat adalah *isi* katalog dan context tier, bukan alokasinya (B-02, B-03).
- **P21:** ≤ 190.000 LOC Go + ≤ 10.000 JS/CSS, dependency ≤ 28, 0 CGO. Konsisten di SPEC P21/§17.1, CROSS-CUTTING:27, ENGINEERING E-45/:592, MITIGATION:124, SCORECARD:90/110, ROADMAP:196. Pengecualian: angka basi di M-10, Σ cap di M-02, hitungan dependency di M-03.
- **Binary/arsip/install:** P13 ≤ 52 MB, arsip ≤ 22 MB, install ≤ 30 s pada 10 Mbit/s (22 MB × 8 / 10 ≈ 17,6 s, sisa ≈ 11 s). Image Docker ≤ 54 MB ≥ binary, layer ≤ 23 MB ≥ arsip, jadi tidak mustahil. Hanya B-03 MITIGATION yang berbeda (M-09).
- **Startup ≤ 100 ms p95, `version` ≤ 20 ms / `init()` ≤ 10 ms, RSS idle TUI ≤ 50 MB / `serve` ≤ 90 MB / peak ≤ 300 MB, overhead iterasi p50 ≤ 5 ms, `Store.Append` ≤ 5 ms, `recall` ≤ 50 ms @ 1 juta event, `tool_search` ≤ 5 ms:** konsisten antar dokumen.
- **Batas iterasi 40** (P2, §3.5, CHECK `turn_journal.current_iteration BETWEEN 0 AND 40`, delegate), **retry** 3 attempt per model + maks 2 fallback, **spill > 4K char**, **compact_at 0,75**, **budget subagent 25%**, **timeout hook 5 s**: konsisten. Timeout turn 900 s hanya hilang dari §12 (M-13).
- **Skema:** kolom dan index yang dipakai kueri §11.6 ada semua; status outbox `queued|sending|sent|failed|dead|uncertain` konsisten di SPEC §9.7, SELF-HEALING §5.3–5.4, dan DDL; tidak ada nomor migrasi yang dipakai ganda di dokumen lain.
- **Lisensi library utama:** modernc.org/sqlite (BSD-3), wazero (Apache-2.0), mvdan.cc/sh (BSD-3), Charm bubbletea/lipgloss (MIT), coder/websocket (ISC), goldmark (MIT), starlark-go (BSD-3), ledongthuc/pdf (BSD-3), x/* (BSD-3) kompatibel dengan MIT. Masalahnya hanya rantai GPL whatsmeow (B-01) dan mekanisme notice (M-05).
- **Status ADR:** 33/33 Accepted. Satu-satunya teks "menunggu" yang tersisa merujuk ADR-022 yang sudah Accepted (m-11).

## Risiko kelayakan yang perlu spike di F0 (bukan temuan)

Hitungan kasar [INFERENCE] yang belum cukup bukti untuk dijadikan temuan. Disarankan diukur di harness F0 sebelum fase terkait dimulai:

1. **Schema `edit_file`:** ≈ 720 char JSON. Pada o200k, JSON biasanya ≈ 3–3,5 char/tok, jadi ≈ 205–240 tok, mepet atau melewati cap CT-27 ≤ 200 tok/tool.
2. **Preflight gambar 12 MP ≤ 150 ms pure Go:** decode JPEG stdlib saja ≈ 100–200 ms, belum termasuk resize dan encode.
3. **Σ cap `init()` per paket:** ≤ 0,5 ms × ≥ 29 paket ≈ 14,5 ms, lebih besar dari total ≤ 10 ms. Masih bisa dipenuhi karena keduanya batas atas, tetapi polanya sama dengan M-01.
4. **Cache kompilasi wazero:** ≤ 8 MB/grammar (CODE-TOOLS.md:387). Sesi multi-bahasa bisa menekan RSS idle TUI ≤ 50 MB, dan skenario P18 belum memuat beberapa grammar sekaligus.

## Jumlah per severity

BLOCKER **11** · MAJOR **26** · MINOR **28** · total **65**.

Urutan perbaikan yang disarankan:

1. M-24: terapkan §22.2/§22.3 ke bagian target.
2. B-04..B-09 + M-14, M-16..M-19: lengkapi DDL §11.
3. B-10 + M-20 + m-18: tulis ulang §10 CLI.
4. B-02, B-03: angka katalog dan context tier.
5. B-01 (keputusan user) + M-01.
6. B-11: kolom status FEATURE-PARITY + pemetaan NP→baris.
7. Sisa MAJOR.
8. MINOR boleh dikerjakan bersamaan atau setelah freeze lewat ADR ringan (§20).

## §Verifikasi perbaikan (2026-10-18)

Verifikasi independen atas klaim penulis spec bahwa 65/65 temuan sudah tertutup (63 diperbaiki langsung; B-01 dan M-01 lewat ADR-034) dan bahwa SPEC.md kini berstatus "Spec 1.0 — FROZEN" (SPEC.md:3). Objek: SPEC.md (2.342 baris) dan semua docs/*.md per 2026-10-18. Read-only kecuali bagian ini.

### Ringkasan

| Status | Jumlah |
|---|---:|
| OK | 54 |
| PARSIAL | 11 |
| GAGAL | 0 |
| **Total ID** | **65** |

| Temuan baru | Jumlah |
|---|---:|
| BLOCKER | 0 |
| MAJOR | 6 |
| MINOR | 30 |
| **Total** | **36** |

**Verdict: Freeze sah — TIDAK (belum).**

- Semua BLOCKER asli tertutup kecuali dua yang masih PARSIAL di dokumen pendamping (B-03, B-06).
- Tidak ada temuan baru berkategori BLOCKER. Desain tidak perlu dibuka ulang.
- Status FROZEN belum sah karena masih ada **6 MAJOR baru** (N-01..N-06) dan **7 PARSIAL dengan residu setingkat MAJOR**: M-02, M-03, M-07, M-08, M-10, M-14 (→ N-04), M-20 (→ N-06). Kriteria freeze di review ini adalah "BLOCKER + MAJOR tertutup".
- Semua perbaikan yang tersisa berupa edit teks atau DDL lokal.
- Rekomendasi: kerjakan residu PARSIAL dan N-01..N-06, catat sebagai ADR-036 (errata pra-implementasi; §20 mewajibkan ADR karena SPEC sudah berlabel FROZEN), lalu tetapkan ulang tanggal freeze. MINOR boleh menyusul lewat ADR ringan.

### Metode

- Lima lane verifikasi paralel (read-only) dijalankan dengan pembagian yang sama seperti review awal: kelayakan/lisensi, skema, CLI/ADR/invariant, keterlacakan, angka basi/integrasi §22.
- Untuk setiap ID, lane membaca temuan dan usulan asli, mencari ulang dengan grep (nomor baris lama sudah bergeser), lalu memeriksa apakah perbaikan memunculkan kontradiksi baru.
- Reviewer mengecek ulang bukti kunci secara langsung, yang ditandai ✔:
  - distribusi status FEATURE-PARITY;
  - jumlah baris ROADMAP per fase;
  - SPEC.md:126 vs :2156;
  - SPEC.md:2237 dan :2213;
  - SELF-HEALING.md:375;
  - SPEC.md:1144, :1383, :2202;
  - ENGINEERING.md:583.
- Residu PARSIAL yang merupakan sisa temuan lama dicatat di kolom Catatan baris ID-nya, tidak diberi nomor N baru, supaya tidak terhitung dua kali.

### Cek mekanis dan angka gate

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 162 link, 0 rusak (target file ada); 0 link anchor `#` |
| P1 ≤ 5.000 tok | 1.200 + 600 + 1.100 + 100 + 2.000 = 5.000, sama di SPEC.md:98, :521-529, :650, :708, :718-721 |
| Context tier (B-03) | sub-cap 60 + 150 + 400 + 300 + 150 + 40 = 1.100, sama di SPEC.md:98, :720, DECISIONS.md:428; urutan potong normatif |
| Katalog (B-02) | worst case 24 grup × 15 + MCP 60 + penutup 30 = 450 ≤ 600; `TestCatalogOverflow` 65/200 server. Efek samping: N-01 |
| Σ budget binary (M-01) | dijumlah ulang 29 baris §3.2.4 = 48,4 MB ≤ 52 MB; gate Σ `TestBudgetTableSum` |
| Σ LOC | 184.900 = Σ §3.2.4 = rencana tunggal (residu per fase: M-02) |
| Dependency langsung | 26/28 (23 runtime + 3 test) di ENGINEERING E-45; 0 modul GPL/AGPL/LGPL di binary utama; `TestNoGPLInBinary`; `whatsmeow`/`libsignal` hanya di bridge eksternal (residu: N-03, N-07) |
| Build CGO-free | tag `goolm` wajib + guard `//go:build !goolm` (ENGINEERING.md:416); risiko override tag: N-02 |
| CLI | §10 = 20 perintah top-level + 4 hidden (`__complete`, `__starlark`, `__exec-shim`, `supervise`); 0 nama top-level lama di dokumen normatif, kecuali MITIGATION.md:158. Bentuk subperintah/flag: N-06 |
| FEATURE-PARITY `status` (B-11) ✔ | 854 baris di 18 tabel: `planned` 836 / `dropped-with-approval (NP-xx)` 17 (NP-19 ×10, NP-15 ×2, NP-21 ×2, NP-02, NP-07, NP-16) / `external-bridge (ADR-034)` 1 (PLAT-025). Penyebut 854 − 17 = 837 konsisten di SPEC.md:793, SCORECARD, DECISIONS; pemetaan NP → baris ada |
| GAP-SWEEP | 118/118 di SPEC §22.1, MITIGATION §10, ROADMAP, semua berprefix `GAP-`; 16/16 P0 punya baris SCORECARD §1.9 |
| Invariant | P1–P29 di semua rujukan rentang; 0 P30+; P20/P21/P28 tanpa nama `Test*` (N-28) |
| ADR | ADR-001..035, berurutan, semua Accepted; teks status basi: N-27, N-35 |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| B-01 | OK | SPEC.md:292, SPEC.md:886, SPEC.md:894, SPEC.md:902, docs/ENGINEERING.md:417, docs/ENGINEERING.md:460, docs/ENGINEERING.md:479, docs/DECISIONS.md:413-418, docs/CROSS-CUTTING.md:28 | whatsmeow/libsignal tidak di-link ke binary mana pun; WhatsApp Web lewat bridge GPL terpisah; ada TestNoGPLInBinary. Sisa rujukan whatsmeow di sisi damnbot (SPEC:2202, MITIGATION:214) dicatat sebagai temuan baru. |
| B-02 | OK | SPEC.md:98, SPEC.md:264, SPEC.md:708, SPEC.md:849, docs/DECISIONS.md:428 | Ada baris MCP gabungan ≤ 60 tok dan aturan overflow untuk > 24 grup; worst case 24×15 + 60 + 30 = 450 ≤ 600; TestCatalogOverflow diuji dengan 65/200 server dan 40 grup. Pengurutan berdasarkan pemakaian 30 hari memicu konflik baru dengan P15 (lihat temuan baru). |
| B-03 | PARSIAL | OK: SPEC.md:98, :107, :580, :588, :597, :600, :720; masih lama: docs/MITIGATION-MATRIX.md:19 (T-07 'top-3, ≤ 200 tok'), docs/SCORECARD.md:27 ('Token skill per turn ≤ 200') | Residu: cap skill lama ≤ 200 tok masih di docs/MITIGATION-MATRIX.md:19 (T-07) dan docs/SCORECARD.md:27, padahal SPEC §4.8 kini 150. |
| B-04 | OK | SPEC.md:1534, :1551, :1555; docs/SELF-HEALING.md:304, :323, :327; SPEC.md:2158, :1911 | 'awaiting_approval' sudah ada di CHECK dan di WHERE index pada SPEC dan SELF-HEALING. Kolom approval_request ada di 0005, dan tidak ada ALTER. |
| B-05 | OK | SPEC.md:1105, :391, :1106, P11 :109 | 'tool' ada di CHECK, di tipe Purpose, dan di P11. Lihat temuan baru soal feature='sms' di GW-09. |
| B-06 | PARSIAL | SPEC.md:1681, :1690, :1624 (OK); docs/MITIGATION-MATRIX.md:216 masih '§11.3' | DDL ada di 0007 (SPEC.md:1681, :1690). Residu: docs/MITIGATION-MATRIX.md:216 masih '§11.3'; GW-08 `gateway_cursors` dirujuk '§11.4' di docs/MITIGATION-MATRIX.md:194 dan SPEC.md:2199 (harus §11.9). |
| B-07 | OK | SPEC.md:1471, :905 | 'skipped_overlap' dan 'gated' sudah ada di CHECK cron_runs.outcome. |
| B-08 | OK | SPEC.md:1436-1459 (tz, prompt_sha, model, overlap :1446, idempotent, pause_after_crashes, paused_at, gate, after, after_timeout_s, UNIQUE :1459) | Semua kolom cron_jobs dan constraint yang diwajibkan §22 sudah ada di 0004. |
| B-09 | OK | SPEC.md:1116, :1382 | Kolom usage_ledger.pricing_version dan gateway_identities.tz sudah ada di DDL asalnya. |
| B-10 | OK | SPEC.md:962-986, SPEC.md:988, docs/CROSS-CUTTING.md:79,82,88, docs/EXEC-AND-CODING.md:421-422, docs/SELF-HEALING.md:341, docs/MITIGATION-MATRIX.md:71, docs/DECISIONS.md:51,323, SPEC.md:382 | §10 kini berisi 20 perintah top-level (di luar root `damnbot`), 4 perintah hidden, dan tabel pemetaan nama lama. Semua rujukan lama yang disebut usulan sudah memakai nama final. Baris OPS-08 di §22.1 masih berbeda dari §10 (lihat temuan baru). |
| B-11 | OK | docs/FEATURE-PARITY.md:66 (konvensi + rekap 836/17/1, penyebut 837), header 18 tabel berkolom `status` (:74, :154, :262, :355, :422, :457, :541, :582, :610, :649, :685, :717, :758, :827, :862, :910, :952, :982), :1010-1031 (tabel NP berkolom 'Baris parity'); SPEC.md:793; docs/SCORECARD.md:75 | Kolom status ada di semua 854 baris dan pemetaan NP→baris eksplisit. Kolom test/bench sengaja ditambahkan per fase (SPEC.md:793), jadi gate tetap bisa dievaluasi. |
| M-01 | OK | SPEC.md:268, SPEC.md:301, SPEC.md:110 (TestBudgetTableSum), docs/DECISIONS.md:404, :417 | Σ 29 baris dijumlah ulang = 48,4 MB ≤ 52 MB. Tiap baris kini tertulis sebagai plafon individual dan ada gate Σ. |
| M-02 | PARSIAL | OK: SPEC.md:301, docs/ROADMAP.md:21, :196, docs/DECISIONS.md:404, :430; masih lama: docs/ENGINEERING.md:327 ('≈ 186.000'), docs/ROADMAP.md:12-20 (baris fase F1–F9 berjumlah 149.000) | Σ 184.900 kini rencana tunggal. Residu: docs/ENGINEERING.md:327 '≈ 186.000'; docs/ROADMAP.md:12-20 kolom per fase masih Σ 149.000 (F3 14.000 vs Σ §3.2.4 21.500; F6 16.000 vs 24.500; JS F8 9.000 vs total 9.900). |
| M-03 | PARSIAL | OK: docs/ENGINEERING.md:479 (23 runtime + 3 test = 26, x/text terdaftar, .tar.gz, bench/go.mod terpisah), docs/CODE-TOOLS.md:355; masih lama: SPEC.md:2009 ('≤ 28 (rencana 27)'), docs/ENGINEERING.md:597 ('rencana 27') | Daftar ENGINEERING E-45 = 26 (23 runtime + 3 test, x/text terdaftar, `.tar.gz`, bench/go.mod terpisah). Residu: SPEC.md:2009 dan docs/ENGINEERING.md:597 masih 'rencana 27'. |
| M-04 | OK | docs/ENGINEERING.md:416 (E-39 GOFLAGS=-tags=goolm + guard //go:build !goolm + TestGoolmGuard), :459, :523 (E-65), SPEC.md:110, SPEC.md:1977 | Tag goolm wajib di semua job dan ada guard compile. Ada risiko override -tags di size-check/minimal (lihat temuan baru). |
| M-05 | OK | docs/ENGINEERING.md:477 (E-50), SPEC.md:984 (doctor licenses), docs/DECISIONS.md:207 | Template teks lengkap + NOTICE Apache, bagian manual untuk aset non-Go (hermes, grammar WASM, i18n), lokasi source MPL, TestNoticesComplete. |
| M-06 | OK | SPEC.md:843 | Capture/playback lewat executable eksternal per OS, fallback module_unavailable; waktu spawn ≤ 50 ms dihitung dalam 200 ms. |
| M-07 | PARSIAL | OK: SPEC.md:826 (proses anak __starlark, RLIMIT_AS/Job Object 256 MiB, TestStarlarkMemoryLimit); masih lama: SPEC.md:2213 (GAP-COST-10 '≤ 256 MiB (alokasi diukur via Thread.SetMaxExecutionSteps …)', test TestStarlarkLimits), docs/SCORECARD.md:159 (TestStarlarkLimits) | §9.2 SPEC.md:826 benar (proses anak `__starlark` + RLIMIT_AS, `TestStarlarkMemoryLimit`). Residu MAJOR: baris normatif SPEC.md:2213 (GAP-COST-10) masih mengklaim kuota MiB lewat `SetMaxExecutionSteps` + `TestStarlarkLimits`, juga docs/SCORECARD.md:159; starlark belum ada di tabel bahasa EXEC-AND-CODING §B.7. |
| M-08 | PARSIAL | OK: docs/ENGINEERING.md:416, :523, docs/CROSS-CUTTING.md:94-101, docs/SCORECARD.md:79, SPEC.md:1977, SPEC.md:2177; masih lama: docs/MITIGATION-MATRIX.md:172 ('Job sqlite-engine 6 target') | 8 target kanonik konsisten (ENGINEERING E-39/E-65, CROSS-CUTTING, SCORECARD, SPEC). Residu: docs/MITIGATION-MATRIX.md:172 'Job sqlite-engine 6 target'. |
| M-09 | OK | docs/MITIGATION-MATRIX.md:72, SPEC.md:2256, docs/DECISIONS.md:401, docs/SCORECARD.md:173 | Target image ≤ binary + 2 MB (≤ 54 MB) dan ≤ 23 MB terkompresi kini sama di semua dokumen. |
| M-10 | PARSIAL | OK: docs/ENGINEERING.md:449 (acp 2.500), docs/DECISIONS.md:133 (10.000 JS/CSS), SPEC.md:2319 (judul §22.4 'analisis pra-ADR-033, historis; angka berlaku = §3.2.4', backtick yatim sudah hilang di :2321). MASIH SALAH: SPEC.md:2237 (GAP-UX-03 normatif: 'internal/acp, ≤ 1.800 LOC §3.2.4') ↔ SPEC.md:277 (acp 2.500); docs/ENGINEERING.md:327 ('Rencana total … ≈ 186.000') ↔ SPEC.md:301 (184.900) | ENGINEERING:449, DECISIONS:133, §22.4 historis OK. Residu MAJOR: SPEC.md:2237 (GAP-UX-03, normatif) masih 'internal/acp ≤ 1.800 LOC' vs SPEC.md:277 (2.500) — dua gate untuk modul sama. |
| M-11 | OK | docs/SELF-HEALING.md:249 (tick adaptif 1 s/10 s, ADR-033 Q50), docs/SELF-HEALING.md:580 (P22-5: wake-up idle ≤ 6/menit, total ≤ 60/menit, TestWatchdogAdaptiveTick), SPEC.md:1993 (§17.1 wake-up ≤ 60/menit, tick adaptif) | Di luar log historis §22.2 (SPEC.md:2289) tidak ada lagi 'tick 1 s' tetap. |
| M-12 | OK | SPEC.md:1948, SPEC.md:2190, SPEC.md:2167, SPEC.md:2256, docs/SCORECARD.md:150 | §15 menyebut REL-17 sebagai sumber angka tunggal dengan angka identik: healthz ≤ 3 s, getUpdates ≤ 120 s, queued ≤ 300 s, provider primer. HEALTHCHECK di SEC-22/INF-03 sama: `doctor health --ready`. Verifikasinya `TestReadyz` + `TestReadyAfterAdapters` + `promtool`, dan `TestReadyzFaultInjection` sudah hilang. |
| M-13 | OK | SPEC.md:1733-1735, SPEC.md:1732 (komentar TTFB/idle di [provider]), SPEC.md:2180, SPEC.md:99, SPEC.md:378 | `[loop] max_wall_s = 900` ada di §12. REL-07, P2 dan §3.5 memakai `loop.max_wall_s`. `provider.timeout_s` sudah diganti komentar TTFB 25 s + 5 s/32K (maks 90 s) dan idle 60 s. |
| M-14 | PARSIAL | SPEC.md:1227-1277 (contentless, fts_meta :1230, blob_fts :1274), :1522 (purge menghapus FTS); docs/SELF-HEALING.md:375, :546 | DDL contentless + `fts_meta` + `blob_fts` + purge FTS sudah benar (SPEC.md:1227-1277, :1522). Perbaikannya memunculkan N-04 (rebuild FTS di SELF-HEALING). |
| M-15 | OK | docs/ROADMAP.md:102 | Teks sekarang 'migrasi 0001–0007'. |
| M-16 | OK | SPEC.md:1279-1301, :1500-1501, :1614-1619, :1711-1714, :1717, :550 | 16/16 tabel append-only punya no-UPDATE dan no-DELETE. gc tidak lagi menghapus baris actions. |
| M-17 | OK | SPEC.md:372, :374, :375, :438, P2 :99 | no_progress dan invalid_answer ada beserta pemetaan ACP. empty_response kini 'tanpa nudge', sama dengan COST-15. Celah stop reason budget lain dilaporkan sebagai temuan baru. |
| M-18 | OK | SPEC.md:1062, :2207 | dlp_blocked dan acp_fs_fallback ada di CHECK. Metrik cache_break dipindah ke payload llm_call. |
| M-19 | OK | SPEC.md:1179-1190, :586, :1490, :907, :1702, :858 | Tabel memory_pins (append-only, dengan trigger), kolom kanban_tasks.labels, dan tabel mcp_serve_tokens sudah ada. |
| M-20 | PARSIAL | SPEC.md:971, :984, :969 (pemetaan OK); masih salah: docs/MITIGATION-MATRIX.md:158 (`uninstall --purge`), SPEC.md:2146 (`sessions backup …\|restore`), SPEC.md:2163 (`setup auth logout <provider\|--all>`) | Top-level lama sudah dipetakan (SPEC.md:969-988). Residu: docs/MITIGATION-MATRIX.md:158 `uninstall --purge`; bentuk subperintah SEC-01 (SPEC.md:2146) dan SEC-18 (:2163) tidak cocok §10 — bagian dari N-06. |
| M-21 | OK | SPEC.md:79, docs/ROADMAP.md:176, docs/ENGINEERING.md:555 | Ketiga lokasi sudah "P1–P29". Tidak ada lagi rujukan P1–P28 di luar REVIEW-1.0. |
| M-22 | OK | SPEC.md:2244, docs/MITIGATION-MATRIX.md:239, docs/DECISIONS.md:400 | UX-10 dan MITIGATION kini menulis "tanpa QR di 1.0 (ADR-033 Q48)". Flag `--remote` yang dipakai di sini tidak ada di §10 (lihat temuan baru). |
| M-23 | OK | SPEC.md:2267, SPEC.md:2286, SPEC.md:886, SPEC.md:898, docs/DECISIONS.md:386 | §22.2 ditandai historis, dan GW-01 ditutup dengan "Final: ADR-033 Q34 — cold outbound dilarang total". `allow_cold_outbound` dan "ADR baru yang mengamandemen" tidak lagi normatif. |
| M-24 | OK | SPEC.md:2265-2267 (§22.2 historis), :2301 (§22.3 historis, 'sudah diterapkan ke §2 dan §17.1'), :2319 (§22.4 historis); penerapan diperiksa satu per satu di checks[0] | Semua perubahan §22.3 sudah ada di §2, §3.5, dan §17.1. Sisa kecil: definisi CPU idle di SCORECARD belum ikut diperbarui (lihat new_findings). |
| M-25 | OK | SPEC.md:838 (`browser_cdp` deferred + allowlist), SPEC.md:2150 | `browser_cdp` sudah ada di daftar tool §9.4. |
| M-26 | OK | docs/SCORECARD.md:132-137 (GAP-SEC-05, GAP-SEC-10, GAP-SEC-22, GAP-GW-01 + kolom 'Cara ukur' & 'Baseline hermes'), :138-176 | Semua 16 gap P0 punya baris §1.9. Untuk GAP-SEC-11 (:146) dan GAP-TOOL-01 (:167), kolom 'Cara ukur' hanya menulis 'kolom Verifikasi baris GAP terkait SPEC §22.1' karena SPEC.md:2156/:2225 memang tidak menamai test. |
| m-01 | OK | docs/SELF-HEALING.md:357 (`safe_auto` default; `ask`; `off`) = SPEC.md:1776 (`off \| ask \| safe_auto`) | Nilai `never` sudah tidak ada. |
| m-02 | OK | docs/ROADMAP.md:23 (≈ 150.000–225.000 dari 184.900) | 0,8–1,2 × 184.900 = 147.920–221.880, jadi pembulatan ≈ 150.000–225.000 wajar. |
| m-03 | OK | SPEC.md:1656, :2149, :2154, :1666 | Pin sekarang mencakup annotations, dan nama kolomnya principal. |
| m-04 | OK | SPEC.md:2158, :1911; docs/SELF-HEALING.md:358 | Nilai yang dipakai sekarang decision='timeout' dan state='crashed'. 'denied' yang tersisa hanya kode error, bukan nilai enum. |
| m-05 | OK | docs/SELF-HEALING.md:411; SPEC.md:876 | sender_id sudah diisi, dan PK ditulis sebagai (platform, external_id) dengan external_id = 'u:'‖update_id. |
| m-06 | OK | SPEC.md:468-470, :1332 | Nilai 'dynamic' sudah didefinisikan di §4.3, dan journal mencatat kelas hasil resolusi. |
| m-07 | OK | docs/CROSS-CUTTING.md:22; SPEC.md:1639 | reasoning_carriers.payload sudah masuk daftar kolom terenkripsi. |
| m-08 | OK | docs/MITIGATION-MATRIX.md:36 (`TestImmutableTables`); grep `TestEventsImmutable` di SPEC+docs = 0 (di luar REVIEW-1.0) | Tinggal satu nama test. |
| m-09 | OK | docs/SELF-HEALING.md:559 (`TestKill9Chaos` definisi tunggal, 200×), SPEC.md:116, docs/EXEC-AND-CODING.md:321, docs/MITIGATION-MATRIX.md:56; grep `TestChaosKill9Turns` = 0 | Nama dan jumlah iterasi (200) sudah seragam. |
| m-10 | OK | SPEC.md:2195 (GW-04 goldmark), SPEC.md:2225 (TOOL-01 ledongthuc/pdf), docs/ENGINEERING.md:461-463 | Kedua library ditetapkan tanpa syarat; bullet lama ada di §22.2 yang sudah ditandai historis (SPEC.md:2267). |
| m-11 | OK | SPEC.md:87, docs/ROADMAP.md:50, docs/SCORECARD.md:258, :275, docs/DECISIONS.md:241 (judul NP-01..NP-21), :243, docs/FEATURE-PARITY.md:1031 (NP-21); grep 'menunggu persetujuan' / 'Q19/Q20' = 0 | Placeholder sudah hilang. DECISIONS.md:362 (Q20 'NP-01..NP-20') adalah log historis, wajar. |
| m-12 | OK | SPEC.md:21, docs/DECISIONS.md:7-424 | Indeks menulis "ADR-001..035", dan DECISIONS memang berisi 35 ADR berurutan. |
| m-13 | OK | SPEC.md:1923 (ADR-033 Q36), docs/DECISIONS.md:388 | SEC-02 tidak lagi merujuk ADR-030. ADR-030 hanya dirujuk di SPEC.md:2106 (freeze) dan itu benar. |
| m-14 | OK | docs/OPEN-QUESTIONS.md:5, docs/DECISIONS.md:347 | Kini tertulis "Q6–Q14 dan Q16–Q30 … Q15 masih terbuka". |
| m-15 | PARSIAL | docs/CODE-TOOLS.md:550, :574-580, SPEC.md:120 | Rentang sudah CT-01…CT-36, tetapi CT-29 tidak ada di dokumen mana pun (docs/CODE-TOOLS.md:573-574): 35 ID, bukan 36. |
| m-16 | OK | docs/CODE-TOOLS.md:20 | Kini "Exact only (ADR-005, P23)". |
| m-17 | OK | SPEC.md:663; docs/SELF-HEALING.md:287 | todo sekarang idempotent di kedua tempat. |
| m-18 | OK | SPEC.md:962, :970, :977, :978, :979, :984, :986, :971; docs/DECISIONS.md:366; docs/EXEC-AND-CODING.md:501; docs/SELF-HEALING.md:210 | Kesembilan item asal kini ada di §10, termasuk `__exec-shim` di daftar hidden dan `setup ssh add`. Rujukan subcommand/flag lain yang belum terdaftar dicatat sebagai temuan baru. |
| m-19 | OK | SPEC.md:797-808 (semua `core-eager`; nilai `core` polos = 0) | Enum mode sudah konsisten. Lihat temuan baru soal nama kolom 'Status dominan'. |
| m-20 | OK | SPEC.md:2140 (aturan prefix + alias singkat), :2146-2263 (semua baris `GAP-`), docs/MITIGATION-MATRIX.md:141-258, docs/ROADMAP.md:186-194, docs/SCORECARD.md:132-176; grep baris tabel tanpa prefix = 0 | Semua tabel memakai `GAP-`. Bentuk singkat `SEC-02` di prosa diizinkan secara eksplisit oleh SPEC.md:2140. |
| m-21 | OK | docs/MITIGATION-MATRIX.md:16 (T-04: threshold 0,50, protect_last_n 20, target_ratio 0,20 vs compact_at 0,75), :26 (T-14 routing `Purpose`) | Usulan review diterapkan penuh. |
| m-22 | OK | docs/SCORECARD.md:121 (Preview), :123 (Budget guard) = 'tidak ada (target absolut)' | Baseline tidak lagi 'diukur di F0'. |
| m-23 | OK | docs/ENGINEERING.md:543 (E-76 'gate F9'), SPEC.md:1971 ('gate F9') | Fase gate kini sama. |
| m-24 | PARSIAL | SPEC.md:1971 (10 kategori + `TestEdgeCases/<EC-ID>` + laporan `edgecases.json`), docs/MITIGATION-MATRIX.md:124 (10 kategori); masih salah: SPEC.md:1971 jumlah per kategori ↔ docs/EDGE-CASES-hermes.md:9-18; tabel EDGE-CASES (:52, :87, :104, :131, :152, :176, :191, :213, :242, :255) tanpa kolom status/test | Laporan `TestEdgeCases/<EC-ID>` + `edgecases.json` membuat 166/166 terlacak (SPEC.md:1971). Residu: jumlah per kategori di SPEC.md:1971 salah (EC-FIO 6 vs 19, EC-WIN 5 vs 17, EC-PLAT 23 vs 24, EC-SEC 7 vs 8; Σ 139 ≠ 166) vs docs/EDGE-CASES-hermes.md:9-18; tabel EDGE-CASES tetap tanpa kolom status/test. |
| m-25 | OK | docs/DECISIONS.md:122, docs/ENGINEERING.md:416, :479 | desktop/go.mod terpisah, di luar E-39, E-45 dan size-check. |
| m-26 | OK | SPEC.md:844 | Clipboard gambar lewat executable eksternal per OS, else module_unavailable. |
| m-27 | OK | SPEC.md:2230 | tools.lock hanya memuat URL upstream; mirror proyek hanya untuk artefak permisif atau bila source ikut di-host; lisensi ditampilkan dan dikonfirmasi saat fetch. |
| m-28 | OK | docs/ENGINEERING.md:417, docs/CROSS-CUTTING.md:28 | deps.mpl eksplisit: mautrix + go.mau.fi/util; MPL transitif baru = PR merah. |

### Temuan baru

Nomor `N-xx`. Lokasi ditulis `fileA:line ↔ fileB:line`.

#### MAJOR

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| N-01 | MAJOR | SPEC.md:708 (urutan katalog/MCP berdasar pemakaian 30 hari) ↔ SPEC.md:112 (P15: stable + katalog byte-identik per (versi, config hash), `TestStableTierGolden`) ↔ SPEC.md:719 (§6) | Perbaikan B-02 membuat isi katalog bergantung statistik pemakaian. §5.2 mengklaim deterministik per (versi, config hash, statistik beku per sesi), tetapi P15 dan §6 tetap per (versi, config hash). Dua sesi dengan config sama bisa menghasilkan katalog berbeda: golden test dan prefix cache lintas sesi pecah. | Pilih satu: (a) urutan alfabetis murni (deterministik per config, paling sederhana); atau (b) ubah P15 dan §6 menjadi per (versi, config hash, snapshot pemakaian) dan golden test memakai snapshot beku. |
| N-02 | MAJOR | docs/ENGINEERING.md:416 (E-39 `GOFLAGS=-tags=goolm` + guard `//go:build !goolm`) ↔ SPEC.md:110 (P13 build `-tags minimal`, `nomod_<id>`) ↔ docs/CODE-TOOLS.md:386 (`TAGS=grammars_embedded`) | Flag `-tags` di command line menimpa `-tags` dari GOFLAGS (bukan digabung) [INFERENCE dari `go help environment`]. Build minimal/size-check/grammar akan kehilangan `goolm`: guard gagal compile atau Matrix tanpa Olm. | Tetapkan di E-39 dan P13 bahwa setiap build ber-tag selalu menyertakan `goolm` (`-tags goolm,minimal`, `-tags goolm,nomod_<id>`, `goolm,grammars_embedded`); Makefile membangun daftar tag dari satu variabel. |
| N-03 | MAJOR | SPEC.md:2202 (GAP-GW-11 butir 2: 'versi protokol di-pin saat build (whatsmeow `store.SetWAVersion`)') ↔ SPEC.md:886-902 (§9.7.1), docs/DECISIONS.md:413 (ADR-034) | Requirement normatif `damnbot` masih mewajibkan pemanggilan API whatsmeow yang kini hanya ada di repo bridge, jadi CI `damnbot` tidak bisa memverifikasinya. Breaker `LoggedOut`/`TemporaryBan` GW-11 bergantung pada `health{state, reason}`, tetapi nilai `reason` tidak dienumerasi di kontrak `damnbot.bridge/v1`. | Pindahkan butir (2) menjadi kewajiban bridge di §9.7.1 (dilaporkan lewat `hello.capabilities`/`bridge_version`); enumerasikan `health.reason ∈ {logged_out, temporary_ban, banned, network, …}` + pemetaan ke breaker GW-11 dan uji di `TestBridgeProtocolConformance`. |
| N-04 | MAJOR | docs/SELF-HEALING.md:375 (`INSERT INTO <fts>(<fts>) VALUES('rebuild')`), :546 (`TestAutoRestore`), :563 ↔ SPEC.md:1240 (FTS `content = ''`, `contentless_delete = 1`) | Akibat langsung M-14: perintah FTS5 `rebuild` tidak didukung pada tabel contentless (dokumentasi resmi sqlite.org/fts5.html, dicek lane), sehingga jalur recovery 'korupsi FTS → rebuild tanpa restore' pasti gagal. | Ganti dengan `delete-all` + re-index dari tabel sumber lewat `dmb_fts_norm` (jalur yang sama dengan re-index `fts_meta` INF-02); `blob_fts` di-re-index dari file blob. Perbarui teks `TestAutoRestore` dan `TestChaosCorruptDBPage`. |
| N-05 | MAJOR | SPEC.md:126 ✔ (P29: isi pesan di log 'TTL ≤ 7 hari') ↔ SPEC.md:2156 ✔ (GAP-SEC-11: `log.message_content_ttl` default 24 jam, maks 72 jam) | Dua angka normatif untuk TTL yang sama. Tidak saling meniadakan (72 jam ≤ 7 hari), tetapi gate P29 akan lulus untuk config 7 hari yang ditolak validasi SEC-11. (Lane mengusulkan BLOCKER; diturunkan ke MAJOR karena tidak ada nilai yang memenuhi satu sisi sekaligus melanggar arah lain secara fatal.) | Samakan P29 dengan SEC-11: 'TTL `log.message_content_ttl` default 24 jam, maks 72 jam'. |
| N-06 | MAJOR | SPEC.md:2146, :2163, :2154, :2162, :2230, :2244, :2206, :935, :549, :1522 ↔ SPEC.md:969-986 (§10) | Requirement normatif memakai bentuk subperintah/flag yang tidak ada di §10, sehingga `TestCLIDocsMatchHelp` pasti merah: `sessions backup create\|verify\|restore` + `--include-secrets` (§10: `backup [create\|ls\|verify]`, `restore <file>`); `setup auth logout <provider\|--all>` (§10 `[--provider P]`); `serve sessions revoke <id>`; `doctor bundle [--out] [--upload]`; `doctor tools fetch\|verify\|list` (§10 `fetch\|ls`); `serve open --remote`; `setup --uninstall [--dry-run]`; `config secrets ls --refs`; `damnbot import <arsip>` (§10 `import archive <file>`); `setup --completion <shell>` (§10 `completion <shell>`). | Satu bentuk per perintah: perluas sel §10 lalu samakan teks §22.1 (mis. `sessions backup create\|ls\|verify`, `sessions restore <file> [--dry-run]`, `setup auth logout [--provider P\|--all]`, `serve sessions revoke [--all\|<id>]`, `doctor bundle [--out P] [--upload]`, `doctor tools fetch\|verify\|ls`, `serve open [--app\|--remote]`, `setup --uninstall [--dry-run] [--purge-data]`, `setup secrets ls --refs`, `import archive <file>`). |

#### MINOR

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| N-07 | MINOR | docs/MITIGATION-MATRIX.md:214 (GAP-COST-16) ↔ docs/DECISIONS.md:413 | Sisa risiko masih menyebut ticker internal whatsmeow sebagai library di proses `damnbot`. | Ganti dengan 'bridge WhatsApp = proses terpisah; wake-up-nya di luar anggaran `serve`'. |
| N-08 | MINOR | docs/MITIGATION-MATRIX.md:187 (GAP-GW-01) ↔ docs/DECISIONS.md:406-418, docs/SCORECARD.md:137 | Mitigasi masih 'ADR baru mengamandemen Q6', tanpa ADR-034/bridge dan tanpa `TestBridgeProtocolConformance`. | Rujuk ADR-033 Q34 + ADR-034 dan tambahkan test tersebut. |
| N-09 | MINOR | docs/SCORECARD.md:76 ↔ docs/DECISIONS.md:420 | ADR-034 menyatakan baris 'Platform chat' menghitung PLAT-025 lewat bridge; barisnya hanya '30'. | Tambahkan '(PLAT-025 via `damnbot-wa-bridge`, uji fake bridge)'. |
| N-10 | MINOR | docs/ENGINEERING.md:506 (E-55 'matrix 4 OS'), :525 ↔ SPEC.md:1977 (6 target berrunner) | Jumlah runner test PR berbeda dengan §16. | Samakan ke 6 target berrunner, atau nyatakan PR = 4, nightly/RC = 6. |
| N-11 | MINOR | SPEC.md:2221 (GAP-PROV-02 mode degradasi: katalog ≤ 200, context ≤ 600) ↔ SPEC.md:708, :720 | Mode degradasi tidak punya komposisi/sub-cap; aturan overflow hanya terpicu di 600. | Tetapkan sub-cap degradasi (mis. 8 grup × 15 + MCP 40 + penutup 20 ≤ 200; context 60/100/200/120/80/40 = 600) + `TestContextTierBudget/degraded`. |
| N-12 | MINOR | SPEC.md:1717 ('`gateway_inbound` insert-only') ↔ docs/SELF-HEALING.md:419 (`UPDATE gateway_inbound SET event_id`) | Tabel digolongkan insert-only padahal protokol exactly-once meng-UPDATE `event_id`. | Golongkan 'mutable terbatas (event_id NULL→nilai)' + trigger yang hanya mengizinkan update itu. |
| N-13 | MINOR | SPEC.md:1383 ✔ (`strength IN ('strong','weak')`) ↔ SPEC.md:2155 (`IdentityStrength ∈ {cryptographic, platform_authenticated, weak}`) | Enum kekuatan identitas berbeda antara DDL dan GAP-SEC-10. | Samakan CHECK dengan tiga nilai SEC-10 (aman pra-rilis) atau tulis pemetaan eksplisit. |
| N-14 | MINOR | SPEC.md:2200 (GAP-GW-09 `feature='sms'`) ↔ SPEC.md:1106, :2214 (`tool:<backend>`) | Konvensi `feature` untuk `purpose='tool'` tidak konsisten. | Ubah menjadi `feature='tool:sms'`. |
| N-15 | MINOR | SPEC.md:369-375 (§3.5) ↔ SPEC.md:2083-2085 (§18.4 cap USD per user gateway), :2214 (cap tool sesi/harian), P2 SPEC.md:99 | Cap gateway per user dan cap tool tidak punya stop_reason, padahal P2 meng-assert tepat satu stop_reason dari §3.5. | Tambah `budget_gateway_user` dan `budget_tools` (ACP `max_tokens`), atau nyatakan keduanya ditolak sebelum turn/dispatch. |
| N-16 | MINOR | SPEC.md:1144 ✔ (CHECK `approvals.source`) ↔ SPEC.md:2166 (GAP-SEC-21 approval via `mcp serve`) | Keputusan approval dari klien `mcp serve` tidak punya nilai `source`. | Tambah `'mcp_serve'` ke CHECK. |
| N-17 | MINOR | SPEC.md:1526 ('disalin verbatim') ↔ docs/SELF-HEALING.md:296-306, :318 | SQL identik tetapi komentar berbeda; migrasi dikunci checksum, jadi salinan kanonik harus jelas. | Salin komentar persis, atau tulis 'SQL identik; SPEC §11.7 kanonik'. |
| N-18 | MINOR | SPEC.md:1269-1271 (`skills_fts` hanya AFTER INSERT) ↔ SPEC.md:1717 (`skills` mutable) | UPDATE/DELETE skill meninggalkan entri FTS basi. | Tambah trigger AFTER UPDATE/DELETE yang menghapus (dan re-insert) baris FTS. |
| N-19 | MINOR | SPEC.md:639 (`note → hook_note`) ↔ SPEC.md:1056-1062 (CHECK `events.kind`) | `hook_note` dipakai seperti event kind tetapi tidak ada di CHECK. | Tambah ke CHECK atau tulis sebagai `system_notice{kind:"hook_note"}`. |
| N-20 | MINOR | SPEC.md:898 (`cold_outbound_forbidden`), :900 (ack disimpan 'di event') ↔ SPEC.md:2192 (`cold_outbound_denied`; ack di `modules.gateway.whatsapp_web.ack_ban_risk_at`) | Dua kode error dan dua lokasi penyimpanan ack untuk bridge WhatsApp. | Satu kode (mis. `cold_outbound_denied`) dan satu lokasi ack (config key + `system_notice` sebagai audit). |
| N-21 | MINOR | SPEC.md:2212 (GAP-COST-09 'EMA dipersist di state sesi') ↔ SPEC.md:1290 (`sessions` immutable), :1037 (CHECK key `session_labels`) | Tidak ada kolom/tabel untuk kalibrasi EMA per sesi. | Nyatakan EMA diturunkan dari `usage_ledger` saat load, atau tambah key `token_calib` di `session_labels`. |
| N-22 | MINOR | SPEC.md:2181, :2182, :2216 (REL-08/09, COST-13 → 'SELF-HEALING §6') ↔ docs/SELF-HEALING.md:460-470 | §6 SELF-HEALING tidak memuat overlap/`skipped_overlap`, gate, auto-pause, `idempotent`. | Tambahkan baris kebijakan tersebut ke §6. |
| N-23 | MINOR | SPEC.md:550 (`doctor gc` menghapus blob yang ditahan aksi kedaluwarsa) ↔ SPEC.md:1068, :1130-1131 (FK ke `blobs`), :994 (`foreign_keys=ON`) | [INFERENCE] Menghapus baris `blobs` yang masih dirujuk akan gagal FK. | Tegaskan gc hanya menghapus file blob; baris `blobs` tetap dengan penanda `expired`. |
| N-24 | MINOR | SPEC.md:2252 (sel GAP-OPS-08: hidden hanya `__complete`, `supervise`; '≤ 20'; `TestCLINoDuplicateIntent`, `TestDocsCLIHelpSync`) ↔ SPEC.md:962, :988, :2107 (4 hidden; '= 20'; `TestCLINoDuplicates`, `TestCLIDocsMatchHelp`); docs/MITIGATION-MATRIX.md:247, docs/SCORECARD.md:171, docs/ENGINEERING.md:574 | Dua sumber normatif CLI dengan daftar hidden, operator jumlah, dan nama test berbeda. | Sel OPS-08 cukup 'lihat §10'; pilih satu set nama test (`TestCLITopLevelCount`, `TestCLINoDuplicates`, `TestCLIDocsMatchHelp`) di semua dokumen. |
| N-25 | MINOR | docs/SELF-HEALING.md:38, :109, :138, :391, :506 ↔ SPEC.md:969-984 (§10) | Flag yang tidak ada di §10: `service install --system --user <name>`, `--no-linger-check`, `serve --supervisor=systemd`, `doctor --quick --health`, `doctor --fix --yes`. | Tambahkan flag ke §10 (boleh bertanda internal); `--quick --health` → `--quick`. |
| N-26 | MINOR | SPEC.md:962 ('terlihat di `damnbot help`') ↔ SPEC.md:2242 (`damnbot help <topik>`) | `help` dipakai sebagai perintah ber-argumen tetapi tidak ada di §10 dan tidak dinyatakan dikecualikan dari hitungan 20. | Tambahkan 'help [topik]: builtin parser, tidak dihitung'. |
| N-27 | MINOR | docs/DECISIONS.md:404 (ADR-033 Konsekuensi: 'Spec belum dibekukan: menunggu review silang independen') ↔ SPEC.md:3 (FROZEN), SPEC.md:2106-2107 | Kalimat status lama tanpa penanda historis di ADR Accepted; DECISIONS juga tidak masuk pengecualian `TestSpecNoPendingAmendments`. | Ganti dengan 'status saat ADR ditulis; freeze setelah ADR-034/035 (SPEC §20)' atau hapus. |
| N-28 | MINOR | SPEC.md:117 (P20), :118 (P21), :125 (P28) ↔ SPEC.md:94 ('semua invariant di-enforce test otomatis') | Tiga invariant hanya punya nama job/tool, tanpa nama `Test*`/`Benchmark*` yang bisa dicari. | Sebut nama gate: P20 job `scorecard` + `TestScorecardGate`; P21 `TestBudgetTableSum`/`TestDirectDeps`; P28 `BenchmarkSpeedParity*`. |
| N-29 | MINOR | SPEC.md:2152 (GAP-SEC-07 `serve open` TTL 60 s) ↔ SPEC.md:2244 (GAP-UX-10 `serve open --remote` TTL 10 menit) | TTL URL sekali pakai berbeda tanpa kalimat pembeda mode. | Nyatakan lokal 60 s, `--remote` 10 menit; daftarkan `--remote` di §10. |
| N-30 | MINOR | SPEC.md:812, docs/ROADMAP.md:50 ↔ docs/DECISIONS.md:251, :255, :262, :263, :264 | Teks 'NP-01..NP-21: `dropped-with-approval`' padahal ADR-022 mempertahankan NP-06, NP-10, NP-17, NP-20 dan sebagian NP-18. | 'NP-01..NP-21 diputuskan per ADR-022/033; yang berkeputusan drop dan punya baris parity = 17 baris `dropped-with-approval` (NP-02/07/15/16/19/21)'. |
| N-31 | MINOR | docs/DECISIONS.md:428 (ADR-035 '837 baris dalam scope') ↔ docs/FEATURE-PARITY.md:66, SPEC.md:793 | Istilah 'dalam scope' ambigu (837 = 836 `planned` + 1 `external-bridge`). Penyebut gate 837 sendiri konsisten. | Tulis '837 baris penyebut gate (836 `planned` + 1 `external-bridge`)'. |
| N-32 | MINOR | SPEC.md:795 (header 'Status dominan') ↔ SPEC.md:793 (`status` = scope; `mode` = core-eager/…) | Kolom berisi nilai `mode` tetapi dinamai 'Status', mudah tertukar dengan kolom `status` FEATURE-PARITY. | Ganti header menjadi 'Mode dominan'. |
| N-33 | MINOR | docs/ROADMAP.md:39 ('setiap baris FEATURE-PARITY.md diberi kolom `fase`'), docs/SCORECARD.md:250 ('kolom mapping parity') ↔ SPEC.md:793 (`fase` diturunkan dari prefix) | Kolom `fase`/'mapping parity' tidak ada; fase per baris tidak bisa diturunkan mekanis dari tabel per prefix + kata kunci. | Selaraskan ROADMAP:39 dengan SPEC; ganti 'kolom mapping parity' dengan rujukan konkret atau jadikan kolom `fase` deliverable F0. |
| N-34 | MINOR | SPEC.md:2267 ('frasa Usulan hanya di bagian historis ini') ↔ SPEC.md:2315-2316 (§22.3 'Usulan default'), SPEC.md:2107 | Klaim cakupan bagian historis terlalu sempit (§22.3 juga memuat 'Usulan'). | Ubah menjadi 'hanya di §22.2–§22.4 historis (lihat §20)'. |
| N-35 | MINOR | docs/DECISIONS.md:327-333 (ADR-030) ↔ SPEC.md:2106 | ADR-030 (freeze) tanpa baris Status/tanggal; konteksnya belum menyebut ADR-034/035. | Tambahkan 'Status: Accepted 2026-10-17; freeze berlaku setelah ADR-033..035'. |
| N-36 | MINOR | docs/ENGINEERING.md:583 ('÷ 874 baris', `grep -c -E '^\| [A-Z]+-[0-9]+'`) ↔ SPEC.md:793 (854 / 837) | Perintah grep menghitung 875 baris (ukur reviewer: 854 parity + 21 baris NP), dan angka yang ditulis 874 — dua-duanya tidak cocok dengan 854/837. | Pakai penyebut §8 (837) dengan pola `'^\| [A-Z]+-[0-9]{3} '` dan hitung ulang batas (190.000 ÷ 837 ≈ 227). |

### Jumlah akhir verifikasi

ID asli: OK **54** · PARSIAL **11** · GAGAL **0** (dari 65).

Temuan baru: BLOCKER **0** · MAJOR **6** · MINOR **30** · total **36**.

Freeze sah: **tidak**. Syarat freeze sah:

1. Tutup residu 11 PARSIAL. Yang berbobot MAJOR: M-02, M-03, M-07, M-08, M-10, M-14, M-20.
2. Tutup N-01..N-06.
3. Catat sebagai ADR-036 dan perbarui tanggal freeze di SPEC.md:3 dan §20.

## §Verifikasi errata ADR-036 (2026-10-18)

Verifikasi independen atas ADR-036 (docs/DECISIONS.md:434). ADR itu mengklaim menutup 11 ID PARSIAL dan 36 temuan N-01..N-36 dari §Verifikasi perbaikan. Objek pemeriksaan: SPEC.md dan semua docs/*.md (8.609 baris). Status SPEC saat ini: "kandidat freeze (menunggu verifikasi errata)" (SPEC.md:3). Dokumen dibaca saja; satu-satunya yang ditulis adalah bagian ini.

Kriteria freeze sah: 0 BLOCKER dan 0 MAJOR terbuka. MINOR boleh dicatat sebagai errata pasca-freeze lewat ADR.

### Ringkasan

| Status ID errata | Jumlah |
|---|---:|
| OK | 40 |
| PARSIAL | 7 |
| GAGAL | 0 |
| **Total** | **47** (11 PARSIAL lama + N-01..N-36) |

| Temuan baru (V-) | Jumlah |
|---|---:|
| BLOCKER | 0 |
| MAJOR | 4 |
| MINOR | 27 |
| **Total** | **31** |

MAJOR yang masih terbuka: **5**, terdiri dari V-01..V-04 ditambah residu N-06/M-20. BLOCKER terbuka: 0.

**Verdict: Freeze sah — TIDAK.** Kemajuannya besar:

- 40/47 ID errata tertutup.
- Semua angka gate utama kini satu sumber: P1, Σ binary, Σ LOC = Σ fase ROADMAP, dependency 26/28, 8 target, katalog alfabetis, kontrak bridge, dan TTL P29.
- Tidak ada BLOCKER.

Freeze masih terhalang 5 MAJOR. Semuanya berupa edit lokal kecil, bukan masalah desain:

- V-01: trigger `blob_expirations` belum ada.
- V-02: penanda expired pada blob content-addressed yang ditulis ulang.
- V-03: cakupan scan `TestCLIDocsMatchHelp` belum mengecualikan dokumen historis.
- V-04: jumlah kasus CT-20 ditulis dengan tiga angka.
- Residu N-06: satu sel `uninstall --purge`.

Bila kelima MAJOR itu ditutup (ADR-037) tanpa memunculkan MAJOR baru, freeze dapat dinyatakan sah. MINOR boleh dicatat sebagai errata pasca-freeze.

### Metode

- Empat lane verifikasi paralel, semuanya read-only: (1) angka/build/bridge, (2) skema & recovery, (3) CLI/ADR/gate bernama, (4) keterlacakan.
- Setiap lane membaca ADR-036, mencari ulang setiap lokasi dengan grep di seluruh SPEC.md + docs/*.md (kecuali GAP-SWEEP, REVIEW-1.0, dan bagian bertanda historis), lalu memeriksa kontradiksi yang muncul akibat perbaikan.
- Reviewer mengecek langsung, ditandai ✔ di bawah:
  - distribusi status FEATURE-PARITY;
  - Σ kolom fase ROADMAP;
  - sisa `rebuild`/`GOFLAGS`/angka lama;
  - DDL `blob_expirations` beserta trigger 0006;
  - CT-20 di SPEC.md:1993/:2257 vs CODE-TOOLS;
  - cakupan `TestSpecDocs` (SPEC.md:2137);
  - jumlah ID CT unik.
- Temuan lane yang menduplikasi residu PARSIAL tidak diberi nomor V. Contoh: SCORECARD.md:171 `≤ 20` sama dengan residu N-24.

### Cek mekanis dan angka gate

| Cek | Hasil |
|---|---|
| Diagram mermaid ✔ | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown ✔ | 163 link, 0 rusak |
| P1 ≤ 5.000 tok | 1.200 + 600 + 1.100 + 100 + 2.000 = 5.000. Angka sama di SPEC.md:98, §4.5, §5.1, §5.2, §6 |
| Context tier | 60 + 150 + 400 + 300 + 150 + 40 = 1.100. Skill ≤ 150 (+ ≤ 20 volatile) sama di SPEC §4.8/§6, MITIGATION T-07/T-08, SCORECARD:27 |
| Katalog | Urutan alfabetis murni di SPEC.md:98, :264, :710, :851, P15 :112, §6 :721. Ada `TestCatalogDeterministic`. Worst case 450 ≤ 600. Degradasi: 180 ≤ 200, context 600 |
| Σ LOC ✔ | Kolom fase ROADMAP F1..F9 = 29.500 + 12.000 + 21.500 + 20.000 + 30.500 + 24.500 + 25.100 + 21.800 + 0 = 184.900 = Σ §3.2.4. JS ≈ 9.900 |
| Σ binary | 48,4 MB ≤ 52 MB |
| Dependency | 23 runtime + 3 test = 26 ≤ 28. 'rencana 27' hanya muncul di Q33 dengan anotasi historis (DECISIONS.md:387 ✔) |
| Build tag ✔ | 0 `GOFLAGS=-tags` normatif. `TAGS ?= goolm` hanya ditambah (ENGINEERING.md:416). Sisa: tag `perf` (V-10) |
| FTS recovery ✔ | `rebuild` hanya tersisa sebagai kalimat larangan (SPEC.md:1235). Jalur yang dipakai: `delete-all` + `dmb_fts_norm` |
| CLI | §10 berisi 20 top-level + 4 hidden + builtin `help`. Semua bentuk normatif cocok, kecuali SPEC.md:2193 (residu N-06) |
| FEATURE-PARITY ✔ | 854 baris: `planned` 836, `dropped-with-approval` 17, `external-bridge` 1. Penyebut 837 konsisten, kecuali ROADMAP.md:170 (V-28) |
| EDGE-CASES | Σ per kategori = 166, sama dengan SPEC §16 |
| CODE-TOOLS ✔ | 31 ID CT unik, tetapi dokumen menulis "30 ID" (residu m-15) |
| GAP-SWEEP | 118/118 dengan prefix `GAP-` di SPEC §22.1, MITIGATION §10, ROADMAP. 16/16 P0 ada di SCORECARD §1.9 |

### Status per ID errata

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| B-03 | OK | SPEC.md:98, :600-601, :722; docs/MITIGATION-MATRIX.md:19-20; docs/SCORECARD.md:27 | Batas skill sekarang ≤ 150 tok di context tier ditambah ≤ 20 tok di volatile tier, dan angkanya sama di SPEC §4.8/§6, MITIGATION T-07, dan SCORECARD. Pencarian `≤ 200 tok` untuk skill tidak menemukan sisa; yang tersisa hanya batas schema tool CT-27/EXC-01, yang memang berbeda. |
| B-06 | OK | docs/MITIGATION-MATRIX.md:216 (PROV-02 → §11.9 `model_limits`), :194 (GW-08 → §11.9 `gateway_cursors`), SPEC.md:2229 (GW-08 §11.9), SPEC.md:2239 (COST-06 §11.9), SPEC.md:1654 (daftar tabel 0007) | Tidak ada lagi '§11.3' atau '§11.4' untuk tabel 0007. MITIGATION:217 (§11.4/§11.5 untuk `tz`) benar karena kolomnya memang ada di 0003/0004. |
| M-02 | OK | SPEC.md:268-301; docs/ROADMAP.md:11-23, :196; docs/ENGINEERING.md:327 | Kolom fase ROADMAP F1..F8 dijumlah ulang hasilnya 184.900, sama dengan Σ §3.2.4. ENGINEERING:327 sudah memakai 184.900. Angka 186.000/186.500/149.000 kini hanya muncul di bagian historis (SPEC §22.3/§22.4 :2301, :2323, :2372 dan delta di DECISIONS:432). |
| M-03 | OK | SPEC.md:2039; docs/ENGINEERING.md:479, :597; docs/ROADMAP.md:196; docs/DECISIONS.md:387, :406, :422 | Daftar E-45 dihitung ulang: 23 runtime + 3 test = 26 ≤ 28. Frasa 'rencana 27' tersisa hanya di DECISIONS Q33:387 dan sudah diberi anotasi historis ('saat ADR ditulis; dikoreksi menjadi 26'). |
| M-07 | OK | SPEC.md:828, :2243; docs/SCORECARD.md:159; docs/MITIGATION-MATRIX.md:208; docs/EXEC-AND-CODING.md:463, :475, :502; docs/ENGINEERING.md:463 | Semua lokasi memakai proses anak `__starlark` dengan RLIMIT_AS/Job Object 256 MiB. `SetMaxExecutionSteps` hanya dipakai untuk batas langkah. `TestStarlarkLimits` tidak ditemukan lagi; namanya kini `TestStarlarkMemoryLimit` dan `TestStarlarkStepLimit`. |
| M-08 | OK | docs/MITIGATION-MATRIX.md:172; SPEC.md:2007, :2207; docs/ENGINEERING.md:416, :523; docs/CROSS-CUTTING.md:94-101; docs/SCORECARD.md:79 | Job `sqlite-engine` berjalan di 8 target kanonik E-39 di semua dokumen. Tidak ada lagi '6 target' untuk build/engine. |
| M-10 | OK | SPEC.md:277, :2267; docs/ENGINEERING.md:327, :449; docs/DECISIONS.md:135, :386 | GAP-UX-03 kini menulis acp ≤ 2.500 LOC, sama dengan §3.2.4. Angka 1.800 tersisa hanya di §22.4 historis (SPEC.md:2372). |
| M-14 | OK | SPEC.md:1232-1293 (contentless + `fts_meta` + `blob_fts`), :1546 (purge menghapus 4 FTS) | DDL dan purge sudah benar. Residu N-04 sudah ditutup (lihat N-04). |
| M-20 | PARSIAL | SPEC.md:966-992 (§10 + pemetaan nama lama), docs/MITIGATION-MATRIX.md:158 (sudah `setup --uninstall --purge-data`), SPEC.md:2176 (SEC-01 sudah bentuk §10); sisa: SPEC.md:2193 kolom Verifikasi 'uninstall `--purge` tanpa jaringan' | Residu = residu N-06 (satu lokasi yang sama), dihitung sekali sebagai MAJOR terbuka.  |
| m-15 | PARSIAL | docs/CODE-TOOLS.md:515-528 (CT-01..14), :534-543 (CT-20..29, CT-29 ada di :543), :551, :552-582 (gate CT-01..14, 20..29, 30..36); SPEC.md:120; docs/DECISIONS.md:448 | Residu MINOR. CT-29 sudah ada, tetapi teksnya menyebut '30 ID' padahal 14 + 10 + 7 = 31 ID unik (CODE-TOOLS:551, SPEC:120, ADR-036 DECISIONS:448). Ganti ketiganya menjadi '31 ID'. |
| m-24 | OK | SPEC.md:2001; docs/EDGE-CASES-hermes.md:7-19, baris :54-83, :89-100, :106-127, :133-148, :154-172, :178-187, :193-209, :215-238, :244-251, :257-264; docs/MITIGATION-MATRIX.md:124; docs/ROADMAP.md:54-67 | Jumlah per kategori di SPEC §16 sama dengan katalog (Σ 166). Status/test dilacak lewat `edgecases.json` + `TestEdgeCaseCatalogSync`, tidak lagi sebagai kolom di katalog. |
| N-01 | OK | SPEC.md:98, :112, :264, :710, :721, :851; docs/DECISIONS.md:430, :441 | Katalog kini selalu alfabetis murni dan byte-identik per (versi, config hash) di P1, §3.2.3, §5.2, §6, P15, dan §9.6. `TestCatalogDeterministic` ada di SPEC.md:710 dan :851. 'Pemakaian 30 hari' dan 'statistik beku' tidak ditemukan lagi. |
| N-02 | OK | docs/ENGINEERING.md:416, :523; SPEC.md:110, :2007; docs/CODE-TOOLS.md:386; docs/CROSS-CUTTING.md:94 | Tidak ada lagi `GOFLAGS=-tags` di dokumen normatif. E-39 menetapkan `TAGS ?= goolm` yang hanya ditambah, P13 dan CODE-TOOLS memakai `goolm,minimal\|nomod_<id>\|grammars_embedded`. Sisa minor (tag `perf`, ADR-010/015 lama) dicatat di temuan baru. |
| N-03 | OK | SPEC.md:899, :901-903, :906, :2232; docs/DECISIONS.md:443 | Enum `health.reason` (7 nilai) dan `wa_version` di `hello.capabilities` sudah ada; pin versi kini kewajiban bridge. GW-11 tidak lagi mewajibkan whatsmeow di sisi damnbot; whatsmeow hanya disebut sebagai contoh untuk repo bridge (SPEC.md:902). |
| N-04 | OK | SPEC.md:1235-1237, docs/SELF-HEALING.md:375, :555 (TestAutoRestore), :572 (TestChaosCorruptDBPage), SPEC.md:2285 (INF-02 → SELF-HEALING §3.5), docs/DECISIONS.md:444 | `rebuild` FTS hanya tersisa sebagai kalimat larangan. Jalur `delete-all` + `dmb_fts_norm` + checkpoint `reindexed_to` sama di keempat lokasi. Celah kecil soal sumber re-index dicatat di temuan baru. |
| N-05 | OK | SPEC.md:126, :2186 | P29 dan GAP-SEC-11 sama-sama: default 24 jam, maks 72 jam, nilai > 72 jam ditolak. Pencarian 'TTL ≤ 7 hari' tidak menemukan sisa. |
| N-06 | PARSIAL | SPEC.md:973 (serve open/sessions revoke), :975 (setup auth logout/secrets/completion/--uninstall), :976 (import archive), :978 (sessions backup/restore), :988 (doctor bundle/tools fetch\|verify\|ls); §22.1 cocok di :2176, :2182, :2184, :2192, :2193 (teks), :2260, :2261, :2274, :2277, :2278, :2279, :2281; docs/CROSS-CUTTING.md:43, :47; docs/MITIGATION-MATRIX.md:158, :239, :243 | **Residu MAJOR terbuka**: SPEC.md:2193 (kolom Verifikasi GAP-SEC-18) masih `uninstall --purge` → `TestCLIDocsMatchHelp` merah. Fix: `setup --uninstall --purge-data`.  |
| N-07 | OK | docs/MITIGATION-MATRIX.md:214 | Sisa risiko kini berbunyi 'bridge = proses terpisah; wake-up di luar anggaran `serve`'. Ticker internal kini hanya merujuk goolm/mautrix. |
| N-08 | OK | docs/MITIGATION-MATRIX.md:187 | Baris sudah merujuk ADR-033 Q34 + ADR-034 + SPEC §9.7.1 dan mencantumkan `TestBridgeProtocolConformance`. |
| N-09 | OK | docs/SCORECARD.md:76 | Target kini '30 (PLAT-025 via `damnbot-wa-bridge`, uji fake bridge `TestBridgeProtocolConformance`)'. |
| N-10 | OK | docs/ENGINEERING.md:506, :525; SPEC.md:2007; docs/CROSS-CUTTING.md:96-101 | E-55, urutan job PR, dan §16 sama-sama: 6 target dengan runner + 2 QEMU. |
| N-11 | OK | SPEC.md:2251; docs/DECISIONS.md:449 | Sub-cap degradasi dihitung ulang: katalog 8×15 + 40 + 20 = 180 ≤ 200; context 60+100+200+120+80+40 = 600. `TestContextTierBudget/degraded` sudah disebut. |
| N-12 | OK | SPEC.md:1414-1430 (`gwin_update_guard`, `gwin_no_delete`), :1747 ('mutable terbatas', `TestGatewayInboundGuard`), docs/SELF-HEALING.md:419 | Klausa `WHERE event_id IS NULL` di §5.1 cocok dengan guard, sehingga `rows affected = 0` tetap menjadi sinyal 'sudah diproses'. |
| N-13 | OK | SPEC.md:1399 ↔ :2185 | Enum sudah 3 nilai dan identik. Default kolom yang fail-open dicatat sebagai temuan baru. |
| N-14 | OK | SPEC.md:2230 (`feature='tool:sms'`), :1110 komentar `tool:<backend>`, :2244 | Konvensinya sudah satu. |
| N-15 | PARSIAL | SPEC.md:371-372 (OK) ↔ SPEC.md:2113 (§18.4 daftar cap tanpa cap tool), :124 (P27 tanpa gateway user/tool), :99 (P2 tanpa gateway user) | Residu MINOR: daftar cap §18.4 (SPEC.md:2113), P27 (:124), P2 (:99) belum memuat `budget_gateway_user`/`budget_tools`; pemicu '200 turn/hari' ditolak pra-turn (P25 :122) sehingga tidak bisa jadi stop_reason — nyatakan eksplisit.  |
| N-16 | OK | SPEC.md:1149 | `mcp_serve` sudah ada di CHECK beserta komentar SEC-21. |
| N-17 | OK | SPEC.md:1550 (klaim 'SQL identik; SPEC §11.7 kanonik' + `TestJournalDDLMatchesDoc`), :1553-1580 ↔ docs/SELF-HEALING.md:294-328 | Perbandingan manual menunjukkan SQL identik setelah komentar dihapus. SELF-HEALING §3.3 sendiri tidak menyatakan bahwa SPEC kanonik (opsional). |
| N-18 | OK | SPEC.md:1277-1287 | `skills_au` dan `skills_ad` sudah ada. Normalisasi kolom `name` yang tidak konsisten dicatat sebagai temuan baru. |
| N-19 | OK | SPEC.md:641 ↔ CHECK events.kind SPEC.md:1061-1069 | `hook_note` = `system_notice{kind:"hook_note"}` dan tidak ada sebagai kind tersendiri di dokumen lain. |
| N-20 | OK | SPEC.md:509, :900, :904, :906, :2222; docs/MITIGATION-MATRIX.md:187 | Tinggal satu kode, `cold_outbound_denied`; `cold_outbound_forbidden` tidak ditemukan lagi. Ack disimpan di satu lokasi (config key `ack_ban_risk_at` + `system_notice{kind:"wa_ban_risk_ack"}`) dan sama di §9.7.1 dan GW-01. |
| N-21 | OK | SPEC.md:1116 (`est_input_tokens`), :2242 (EMA tidak dipersist, diturunkan dari ≤ 50 baris ledger) | Tidak tersisa teks 'dipersist di state sesi'. Konflik rumus estimator dicatat sebagai temuan baru. |
| N-22 | OK | docs/SELF-HEALING.md:472-479 (overlap, gate, idempotent, pause_after_crashes + test), SPEC.md:2211-2212, :2246 | Isi kebijakan konsisten dengan REL-08 ('parallel maks 2') dan DDL §11.5. |
| N-23 | PARSIAL | SPEC.md:552, :1613-1617, :1747, :2085 (OK) ↔ SPEC.md:1641-1649 (trigger 0006 hanya untuk actions/undos/plans) | Teks gc benar; residu = V-01 (trigger `blob_expirations` hilang), tidak dihitung ganda.  |
| N-24 | PARSIAL | SPEC.md:2282 (OPS-08 'lihat §10', 4 hidden, 3 nama test), SPEC.md:966, docs/MITIGATION-MATRIX.md:247 ('Tepat 20', `= 20`), docs/ENGINEERING.md:574; sisa: docs/SCORECARD.md:171 target '≤ 20 / 0' | Residu MINOR. Daftar hidden dan nama test sudah seragam. Operator di SCORECARD masih '≤ 20', padahal SPEC dan MITIGATION menulis `TestCLITopLevelCount (= 20)`. |
| N-25 | OK | SPEC.md:973 (`--supervisor=systemd\|launchd\|windows-service` internal; hidden `supervise --supervisor=task-scheduler`), :974 (`install [--system --user <name>] [--no-linger-check] [--dry-run]`), :988 (`--fix [--yes]`, `--quick`); docs/SELF-HEALING.md:38, :109, :138, :157, :199, :239, :515 | Semua flag SELF-HEALING kini ada di §10, dan `--quick --health` sudah hilang (grep = 0). |
| N-26 | OK | SPEC.md:966 ('`help [topik]` dan `--help`/`-h` adalah builtin parser (tidak dihitung dalam 20)'), SPEC.md:2272 (`damnbot help <topik>`) | `help [topik]` kini dinyatakan builtin dan tidak dihitung. |
| N-27 | OK | docs/DECISIONS.md:406 (ADR-033 Konsekuensi diberi '*(Historis — status saat ADR ditulis …)*'), SPEC.md:2137 (paragraf DECISIONS bertanda Historis dikecualikan) | Sudah diberi penanda historis. Kalimat serupa di ADR-032 belum ditandai; dilaporkan sebagai temuan baru. |
| N-28 | OK | SPEC.md:117 (P20 `TestScorecardGate`), :118 (P21 `TestBudgetTableSum`, `TestDirectDeps`, `TestFileLength`), :125 (P28 `BenchmarkSpeedParity*`); docs/DECISIONS.md:449 | Nama gate sudah ada di SPEC §2. Dokumen pendamping belum merujuk nama yang sama; dilaporkan sebagai temuan baru. |
| N-29 | OK | SPEC.md:973 (lokal TTL 60 s, `--remote` TTL 10 menit), :2182 (SEC-07), :2274 (UX-10 'beda dari lokal 60 s'), docs/MITIGATION-MATRIX.md:239 | Pembeda mode sudah eksplisit dan `--remote` terdaftar di §10. |
| N-30 | OK | SPEC.md:814; docs/ROADMAP.md:50; docs/DECISIONS.md:243-267; docs/FEATURE-PARITY.md:1008-1032 | Teks kini menyebut keep NP-06/10/17/20 + sebagian NP-18. 17 baris drop = NP-02(1)+07(1)+15(2)+16(1)+19(10)+21(2). |
| N-31 | OK | docs/DECISIONS.md:430; docs/FEATURE-PARITY.md:66; SPEC.md:795 | ADR-035 kini menulis '837 baris penyebut gate = 836 planned + 1 external-bridge'. |
| N-32 | OK | SPEC.md:797 | Header kolom sudah 'Mode dominan'. |
| N-33 | PARSIAL | docs/ROADMAP.md:31 (OK); grep 'mapping parity' di docs/ = 0 (OK); docs/FEATURE-PARITY.md:66 | Residu MINOR. FEATURE-PARITY:66 masih menulis 'Kolom `modul`/`fase` diturunkan dari prefix…'. Ini bertentangan dengan ROADMAP:31 dan SPEC:795, yang menyatakan `fase` tidak bisa diturunkan mekanis dan diisi sebagai deliverable F0 (`TestParityPhaseColumn`). |
| N-34 | OK | SPEC.md:2297 ('hanya muncul di §22.2–§22.4 historis (lihat §20)'), SPEC.md:2137 | Cakupan bagian historis sudah §22.2–§22.4 dan konsisten dengan §20. |
| N-35 | OK | docs/DECISIONS.md:329 ('Status: Accepted 2026-10-17. Freeze berlaku setelah ADR-033..036 terintegrasi dan errata ADR-036 diverifikasi auditor') | Baris Status dan syarat freeze sudah ada dan konsisten dengan SPEC.md:2136. |
| N-36 | OK | docs/ENGINEERING.md:583; docs/SCORECARD.md:99 | Penyebut sudah 837 dengan pola `[0-9]{3}`, batas 190.000 ÷ 837 = 227,0 dan baseline 972.645 ÷ 837 ≈ 1.162. Ada masalah render GFM pada perintahnya, dicatat di temuan baru. |

### Temuan baru

#### MAJOR

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| V-01 | MAJOR | SPEC.md:1613-1617, :1641-1649 ↔ SPEC.md:1747, docs/DECISIONS.md:447 ✔ | `blob_expirations` ada di daftar append-only untuk `TestImmutableTables` (no-UPDATE + no-DELETE), tetapi 0006 tidak punya `blobexp_no_update`/`blobexp_no_delete`. Hasilnya, triggernya ada untuk 16 dari 17 tabel append-only, jadi gate P9 pasti merah atau implementer menebak sendiri. | Tambahkan di 0006: `CREATE TRIGGER blobexp_no_update BEFORE UPDATE ON blob_expirations …` dan `blobexp_no_delete …` dengan pola pesan yang sama. |
| V-02 | MAJOR | SPEC.md:1614 (`sha256 PRIMARY KEY`, append-only), :1545 (`INSERT OR IGNORE` blob) ↔ SPEC.md:552 (pembaca blob ber-penanda → `expired`) | Blob bersifat content-addressed. Bila konten yang sama ditulis ulang setelah gc, file baru ada, tetapi penanda lama tidak bisa dihapus (append-only, PK). Pembaca lalu mendapat `expired` untuk blob yang hidup (mis. snapshot file baru dengan isi sama). Insert penanda kedua juga gagal PK. | Jadikan event-log `blob_expirations(id PK, sha256, kind IN ('expired','restored'), reason, at)` dengan status = baris terakhir, atau nyatakan bahwa writer blob memeriksa penanda dan menulis ke sha yang sama hanya lewat baris 'restored'. Tambahkan kasus ke `TestGCKeepsBlobRows`. |
| V-03 | MAJOR | SPEC.md:966, docs/ENGINEERING.md:574 (TestCLIDocsMatchHelp: 'setiap `damnbot …` di SPEC/docs ada di parser', tanpa pengecualian) ↔ docs/GAP-SWEEP.md:111 (`damnbot backup/restore`), :119 (`damnbot web sessions revoke --all`), :127 (`damnbot debug bundle`), SPEC.md:2345 (§22.3 historis `damnbot --version`), docs/REVIEW-1.0.md (banyak bentuk lama) ✔ | Gate CLI bernama ini memindai seluruh SPEC/docs tanpa daftar pengecualian. Padahal GAP-SWEEP (dinyatakan 'tidak diubah', SPEC.md:2168), REVIEW-1.0, dan bagian historis §22.2–§22.4 memuat bentuk lama, jadi `TestCLIDocsMatchHelp` pasti merah sejak awal. Daftar pengecualian hanya ada untuk `TestSpecNoPendingAmendments` (SPEC.md:2137). | Di SPEC §10, SPEC §20, dan ENGINEERING E-83: samakan cakupan `TestCLIDocsMatchHelp` dengan pengecualian `TestSpecNoPendingAmendments` (GAP-SWEEP, REVIEW-1.0, §22.2–§22.4, paragraf DECISIONS bertanda *(Historis …)*). |
| V-04 | MAJOR | SPEC.md:2257 (GAP-TOOL-03 'CT-20 (310 kasus…)') ↔ SPEC.md:120, docs/CODE-TOOLS.md:534, :545, :566, docs/MITIGATION-MATRIX.md:222 (325 kasus); SPEC.md:1993, docs/SCORECARD.md:56, docs/MITIGATION-MATRIX.md:128 ('suite edit ≥ 300') ✔ | Ukuran suite edit CT-20 yang di-test punya tiga angka: 310 (§22.1 normatif), ≥ 300 (§16, SCORECARD, MITIGATION) dan ≥ 325 (P23, CODE-TOOLS). | Pakai '≥ 325 kasus (CODE-TOOLS §10.3)' di SPEC:1993, :2257, SCORECARD:56 dan MITIGATION:128. |

#### MINOR (boleh errata pasca-freeze)

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| V-05 | MINOR | SPEC.md:903, :2232 (adapter `needs_attention`) ↔ SPEC.md:233 (`Status` = stopped\|starting\|ready\|degraded\|failed) | Breaker GW-11 dan §9.7.1 memindahkan adapter ke state `needs_attention`, padahal state ini tidak ada di enum `Module.Status`. Test breaker tidak punya nilai yang bisa di-assert. | Tambahkan `needs_attention` ke enum Status (dan transisinya di state diagram §3.2), atau petakan ke `failed` + `reason`. |
| V-06 | MINOR | docs/MITIGATION-MATRIX.md:197 (GAP-GW-11: 'versi WA yang di-pin bisa basi … perlu rilis Damn-Bot baru') ↔ SPEC.md:902-903, :2232 | Setelah ADR-034, pin versi WA adalah kewajiban bridge. Versi basi berarti update bridge (`reason=protocol_outdated`), bukan rilis Damn-Bot. Baris ini juga belum menyebut `health.reason` dan `TestBridgeProtocolConformance`. | Ganti sisa risiko menjadi 'perlu rilis `damnbot-wa-bridge` baru (`health.reason=protocol_outdated` → `needs_attention`)' dan tambahkan `TestBridgeProtocolConformance` di kolom verifikasi. |
| V-07 | MINOR | SPEC.md:828 (§9.2) ↔ SPEC.md:2243, docs/EXEC-AND-CODING.md:463, :489, :502 | Spesifikasi Starlark belum sepenuhnya sama dengan §B.7. (1) §9.2 mengikat 'tool deferred/inti yang diizinkan approval', sedangkan §B.7/COST-10 hanya mengizinkan 7 tool. (2) Batas waktu ≤ 30 s tetap, sedangkan schema `timeout_s` 1–300. (3) macOS: SPEC menulis `ulimit -v`, tabel §B.7 macOS menulis `RLIMIT_DATA`. (4) §9.2 menyebut 'batas OS yang sama dengan §B.7', padahal §B.7 default 512 MiB dan Starlark 256 MiB. | Di §9.2, rujuk allowlist 7 tool §B.7. Tetapkan `timeout_s` Starlark ≤ 30 (atau ikut schema). Samakan mekanisme macOS. Ganti 'sama dengan' menjadi 'mekanisme sama, cap 256 MiB'. |
| V-08 | MINOR | SPEC.md:710 (§5.2 slot volatile 'nama skill/tool relevan baru', ≤ 20 tok) ↔ SPEC.md:723 (§6 Volatile: 'nama skill relevan baru', tanpa sub-cap) | N-01 memindahkan petunjuk berbasis pemakaian ke volatile tier, tetapi baris §6 belum menyebut tool maupun sub-cap ≤ 20 tok. Hanya §4.8 dan SCORECARD yang memuat 20 tok untuk skill. | Di baris Volatile §6 tulis 'nama skill/tool relevan baru (≤ 20 tok)'. |
| V-09 | MINOR | SPEC.md:98 (`+N groups`), :710 (`- +<K> more groups: tool_search`; '(3) bila grup > 24 … grup diurutkan alfabetis'), :2251 (`+K more groups`), docs/DECISIONS.md:430 (`+N more groups`) | Teks baris overflow ditulis dalam tiga bentuk. Klausa (3) juga terbaca seolah pengurutan alfabetis hanya berlaku saat overflow, padahal kalimat berikutnya 'urutan murni alfabetis' selalu berlaku. Golden test butuh satu bentuk literal. | Tetapkan satu literal di §5.2 (mis. `- +<K> more groups: tool_search`), rujuk dari P1/PROV-02/ADR-035, dan pindahkan 'diurutkan alfabetis' keluar dari kondisi overflow. |
| V-10 | MINOR | SPEC.md:109, :115, :2002 (tag `perf`) + docs/DECISIONS.md:111 (ADR-010 `-tags minimal`, `nomod_<id>`), :176 (ADR-015 `-tags grammars_embedded`) ↔ docs/ENGINEERING.md:416 (E-39 daftar `TAGS +=`) | E-39 hanya menyebut `minimal`, `nomod_<id>`, dan `grammars_embedded`. Job perf (`go test -tags perf`) belum dinyatakan lewat `TAGS`, sehingga goolm bisa tertimpa dan guard `!goolm` gagal compile. Teks ADR-010/015 juga masih memakai bentuk tag tanpa `goolm`. | Tambahkan `TAGS += perf` (hasilnya `-tags goolm,perf`) di E-39 dan §16, dan beri anotasi 'bentuk tag final: E-39' di ADR-010/015. |
| V-11 | MINOR | SPEC.md:118 (P21 `TestDirectDeps` = `go list -m` + `deps.allow`) ↔ docs/ENGINEERING.md:422 (E-45 `TestDirectDeps` = parse `go.mod`) | Gate yang sama punya dua metode ukur. `go list -m all` ikut menghitung modul indirect (> 28), sedangkan parse `require` langsung memberi 26. | Samakan P21 ke 'parse `require` langsung (non-`// indirect`) `go.mod` utama', sesuai E-45. |
| V-12 | MINOR | SPEC.md:552 ('baris `blobs` tidak pernah dihapus') ↔ SPEC.md:1546 (purge: 'file + baris `blobs`/`blob_expirations` … dihapus setelah commit') | Ada dua aturan yang bertentangan untuk baris `blobs`. Selain itu, penghapusan purge terjadi setelah commit, yaitu setelah trigger dibuat ulang, sehingga akan ditolak bila trigger no_delete `blob_expirations` ditambahkan (temuan di atas). | SPEC.md:552 → 'tidak pernah dihapus kecuali oleh `sessions purge`'. Hapus baris `blobs`/`blob_expirations` yatim di dalam transaksi purge yang sama, sebelum trigger dibuat ulang. |
| V-13 | MINOR | SPEC.md:1399 (`DEFAULT 'platform_authenticated'`) ↔ SPEC.md:2185 (SEC-10 fail-closed: `weak` tidak bisa di-pair) | Default DDL fail-open. Bila adapter lupa mengisi `strength`, identitas IRC/nick lemah otomatis dianggap terautentikasi platform dan bisa di-pair. | Hapus DEFAULT (`NOT NULL` tanpa default, diisi wajib dari `Caps.IdentityStrength`) atau gunakan `DEFAULT 'weak'`. Tambahkan kasus ke `TestIdentityStrengthTable`. |
| V-14 | MINOR | docs/SELF-HEALING.md:378 (salvage 'dengan `id` > maksimum di backup' termasuk `gateway_inbound`) ↔ SPEC.md:1414-1422 (PK `(platform, external_id)`, tanpa `id`), :1425-1429 | Kriteria salvage tidak terdefinisi untuk `gateway_inbound`. Kasus baris backup `event_id NULL` vs DB rusak `event_id` terisi juga tidak diatur (INSERT gagal PK; satu-satunya UPDATE sah = NULL→nilai). Untuk `gateway_deliveries` yang mutable, kriteria `id >` melewatkan perubahan status setelah backup, sehingga baris `sent` bisa kembali `queued` dan dikirim ganda. | Tetapkan per tabel: `gateway_inbound` = INSERT OR IGNORE lalu UPDATE event_id NULL→nilai (sah menurut guard). `gateway_deliveries` = ambil status 'terjauh' (sent/uncertain menang atas queued), selaras dengan GAP-REL-10. |
| V-15 | MINOR | SPEC.md:1277-1283 (`skills_fts.name` = `NEW.name` mentah) ↔ SPEC.md:1235-1237, docs/SELF-HEALING.md:375 (re-index 'lewat dmb_fts_norm') | Kolom `name` tidak dinormalisasi oleh trigger, sementara query memakai `dmb_fts_norm`. Nama skill CJK tidak cocok dengan bigram, dan re-index yang menormalisasi semua kolom menghasilkan index berbeda dari trigger (bertentangan dengan klaim `TestAutoRestore` 'hasil query sama'). | Pakai `dmb_fts_norm(NEW.name)` di `skills_ai`/`skills_au`, atau tetapkan bahwa re-index memakai ekspresi kolom persis sama dengan trigger. |
| V-16 | MINOR | docs/SELF-HEALING.md:375 ('semua tabel FTS contentless', sumber: events/summaries/memory/skills + blob) ↔ SPEC.md:1352-1354 (`tool_catalog_fts` contentless tanpa `contentless_delete`) | Jalur recovery korupsi FTS tidak menyebut `tool_catalog_fts`. | Tambahkan: '`tool_catalog_fts`: `delete-all` + bangun ulang dari `tool_catalog` (sama dengan pembangunan katalog saat versi/config berubah)'. |
| V-17 | MINOR | docs/MITIGATION-MATRIX.md:250 (GAP-INF-02 → 'SPEC §11.1, §11.6') ↔ SPEC.md:1238-1293 (`fts_meta`, `blob_fts` di §11.2), :2285 | Rujukan seksi untuk DDL `fts_meta`/`blob_fts` salah. | Ubah menjadi 'SPEC §11.2, §11.6; SELF-HEALING §3.5'. |
| V-18 | MINOR | SPEC.md:535 (`ceil(bytes/3,6)`, rasio dari 1 call sebelumnya) ↔ SPEC.md:2242 (COST-09: rasio per script Latin 4/Kiril 2/CJK 1 char/tok, EMA ≤ 50 call), :2241 (COST-08 default 3,0 char/tok) | Ada tiga definisi normatif estimator token (unit byte vs char, default 3,6 vs 3,0 vs per-script, satu call vs EMA). | §4.5 merujuk COST-09 sebagai satu-satunya rumus. Default rasio sesi tanpa ledger = turunan per-script. Hapus `bytes/3,6`. |
| V-19 | MINOR | SPEC.md:427 (`system_notice{carrier_dropped}`), :1953 (`carrier_dropped{reason:"redacted"}`) ↔ SPEC.md:2250 (`system_notice{kind:"carrier_dropped", reason, count}`), :641 (pola `kind` untuk hook_note) | Bentuk payload `carrier_dropped` tidak konsisten (kelas masalah yang sama dengan N-19), dan `TestCarrierDroppedEvent` butuh satu bentuk. | Seragamkan ke `system_notice{kind:"carrier_dropped", reason, count}` di :427 dan :1953. |
| V-20 | MINOR | docs/SCORECARD.md:3, :225, :246-257, docs/ENGINEERING.md:308 (E-19), docs/EXEC-AND-CODING.md:545, docs/ROADMAP.md:78, :173 ↔ SPEC.md:117, :118, :125, docs/DECISIONS.md:449 | Gate P20/P21/P28 punya nama di SPEC tetapi tidak dirujuk di dokumen pendamping. `TestScorecardGate` = 0 kemunculan; `BenchmarkSpeedParity*` = 0, karena SCORECARD/ROADMAP memakai `bench/speed/<ID>`; `TestBudgetTableSum` tidak ada di ENGINEERING. Hanya `TestDirectDeps` (ENGINEERING.md:422, :475, :597) dan `TestFileLength` (:303, :538, :585, :586) yang konsisten. | SCORECARD §3.3 dan §4 menyebut `BenchmarkSpeedParity/<ID>` di `bench/speed` dan `TestScorecardGate`. ROADMAP F0 butir 5 dan F9 acceptance butir 1 merujuk nama yang sama. ENGINEERING E-19 menambah `TestBudgetTableSum` di samping `loc-budget`. |
| V-21 | MINOR | docs/DECISIONS.md:377 (ADR-032 Konsekuensi: 'Spec tetap **belum** dibekukan; freeze menunggu integrasi GAP-SWEEP') ↔ SPEC.md:2136, docs/DECISIONS.md:329 | Kalimat status freeze lama di ADR Accepted tanpa penanda historis. Ini kelas yang sama dengan N-27, tetapi di ADR-032. Syarat freeze yang disebut (hanya GAP-SWEEP) juga sudah usang. | Tambahkan '*(Historis — status saat ADR ditulis. Status terkini: SPEC §20 + ADR-030.)*' seperti DECISIONS.md:406. |
| V-22 | MINOR | docs/ROADMAP.md:25 ('Milestone Spec 1.0 freeze: sebelum F0 dimulai, setelah GAP-SWEEP diintegrasikan') ↔ SPEC.md:2136, docs/DECISIONS.md:329, :436 | Syarat freeze di ROADMAP hanya GAP-SWEEP, sedangkan SPEC dan ADR-030/036 mensyaratkan ADR-033..036 terintegrasi plus verifikasi auditor errata. | 'Milestone Spec 1.0 freeze: sebelum F0, setelah ADR-033..036 terintegrasi dan errata ADR-036 diverifikasi auditor (SPEC §20, ADR-030).' |
| V-23 | MINOR | SPEC.md:2137 (`spec_pending_phrases.txt` tanpa isi), SPEC.md:2297 (frasa 'Usulan' tanpa titik dua) ↔ docs/PROVIDERS.md:164 (kolom 'Usulan Damn-Bot'), docs/EDGE-CASES-hermes.md:52, :87, :104, :131, :152, :176, :191, :213, :242, :255 (kolom 'Usulan regression test'), SPEC.md:3 dan :2136 ('menunggu verifikasi'/'kandidat freeze') | Isi daftar frasa tidak didefinisikan. Jika 'Usulan' dicocokkan apa adanya (SPEC.md:2297), 11 header tabel di PROVIDERS dan EDGE-CASES (di luar pengecualian) membuat gate merah. [INFERENCE] Jika daftar memuat 'menunggu …', baris Status SPEC dan §20 juga merah, padahal keduanya tidak dikecualikan. | Tetapkan isi daftar secara literal dan case-sensitive di §20 (mis. 'Usulan:', 'Usulan resolusi:', 'diterapkan saat implementasi', 'menunggu persetujuan', 'TBD'). Nyatakan bahwa 'kandidat freeze (menunggu verifikasi …)' di baris Status dan §20 bukan frasa tertunda. |
| V-24 | MINOR | SPEC.md:988 (`doctor … bundle [--out P] [--upload]` 'support bundle terenkripsi'), docs/CROSS-CUTTING.md:47 ('menulis file lokal terenkripsi ke kunci publik maintainer') ↔ SPEC.md:2192 (SEC-17: 'zip lokal …; `--upload` hanya mengenkripsi bundle ke kunci publik maintainer') | Dua sumber berbeda soal enkripsi bundle. SEC-17 berarti tanpa `--upload` hasilnya zip lokal tidak terenkripsi; §10 dan CROSS-CUTTING menyatakan selalu terenkripsi. | Pilih satu. Contoh: 'default zip lokal ter-redaksi (0600); `--upload` = enkripsi ke kunci maintainer', lalu samakan §10 dan CROSS-CUTTING §1.7. Atau selalu terenkripsi, dan `--upload` dihapus/diganti nama. |
| V-25 | MINOR | docs/SELF-HEALING.md:515 ('Exit: 0 semua ok, 1 masih ada masalah, 3 ada yang diperbaiki') ↔ SPEC.md:2280 (GAP-OPS-06: '0 ok, 1 error/critical tersisa, 2 hanya warning, 3 diperbaiki & bersih') | Dua kontrak exit code `damnbot doctor`. SELF-HEALING §8 tidak punya exit 2 (hanya warning), jadi contoh di SELF-HEALING.md:518-534 (1 peringatan tersisa) ambigu antara exit 1 dan 2. | Salin tabel exit OPS-06 ke SELF-HEALING §8 (0/1/2/3) dan nyatakan OPS-06 sebagai sumber normatif. |
| V-26 | MINOR | docs/DECISIONS.md:266 (NP-20 `--version` vs `/version`: keep, 'keduanya ada') ↔ SPEC.md:966 (builtin parser hanya `help [topik]`, `--help`/`-h`), SPEC.md:990 (`version --verbose`) | `damnbot --version` dipertahankan oleh NP-20 tetapi tidak terdaftar di §10, baik sebagai builtin maupun flag. Setelah pengecualian historis diterapkan, `TestCLIDocsMatchHelp` tidak bisa memverifikasinya. | Tambahkan '`--version` (alias builtin untuk `version`, tidak dihitung)' ke kalimat builtin parser §10. |
| V-27 | MINOR | SPEC.md:13 (indeks: 'REVIEW-1.0 … 65 temuan ditutup (ADR-035)') ↔ docs/DECISIONS.md:428, :436 | Indeks dokumen masih menyatakan 65 temuan ditutup oleh ADR-035, padahal ADR-035/036 menyatakan 11 di antaranya parsial dan diselesaikan ADR-036 yang menunggu verifikasi. | 'Review silang independen pra-freeze; 65 temuan (ADR-035) + errata verifikasi N-01..N-36 (ADR-036, menunggu verifikasi auditor)'. |
| V-28 | MINOR | docs/ROADMAP.md:170 ('gate parity 854 baris') ↔ docs/ROADMAP.md:31, :174, SPEC.md:795, docs/SCORECARD.md:75 (837) | Deliverable F9 masih menyebut gate parity 854 baris, padahal penyebut gate 837. | Ganti menjadi 'gate parity 837/837 baris penyebut (SPEC §8)'. |
| V-29 | MINOR | docs/ENGINEERING.md:583 (`grep -E '^\| [A-Z]+-[0-9]{3} ' … \| grep -vc …` di dalam sel tabel) ↔ SPEC.md:795 | [INFERENCE: aturan escape pipe tabel GFM] Di dalam sel tabel GFM, `\|` dirender sebagai `\|` walaupun berada di code span. Pola yang tampil menjadi `'^\| …'`, alternasi ERE yang cocok dengan setiap baris, sehingga perintah yang disalin dari dokumen hasil render tidak menghasilkan 837. | Pakai pola tanpa backslash, `'^[\|] [A-Z]+-[0-9]{3} '`, atau pindahkan perintah ke code block di luar tabel. |
| V-30 | MINOR | docs/FEATURE-PARITY.md:740 (PLUG-022 'Provider memori bundled (byterover, retaindb)' = planned), :1032 (NP-21 'PLUG-022 tetap planned untuk Honcho') ↔ SPEC.md:2254 (GAP-PROV-05 'porsi byterover/retaindb di PLUG-022 menjadi dropped-with-approval') | Satu baris tidak bisa sebagian di-drop. Isi PLUG-022 hanya dua provider yang di-drop, tetapi statusnya planned, dan SPEC menyiratkan status campuran. | Ubah deskripsi PLUG-022 menjadi 'slot/plugin memory provider (Honcho)'. Di SPEC:2254 hapus frasa 'porsi PLUG-022 menjadi dropped-with-approval' dan ganti dengan 'PLUG-022 tetap planned (Honcho)'. |
| V-31 | MINOR | SPEC.md:886, docs/ROADMAP.md:65, :128 ('15 entri EC-PLAT Telegram') ↔ docs/EDGE-CASES-hermes.md:215-217, :221 (EC-PLAT-001/002/003/007 juga Telegram), :223-237 (009–023 bertanda '(Telegram)') | Himpunan entri Telegram yang menjadi gate F4 ambigu. Bila hanya yang bertanda '(Telegram)' yang dihitung (009–023), empat entri Telegram lain (001/002/003/007) jatuh ke gate F7. | Sebut ID secara eksplisit di ROADMAP F4 (mis. 'EC-PLAT-001..023 kecuali 008'), atau beri tanda '(Telegram)' di 001/002/003/007 dan ubah jumlahnya menjadi 19. |

### Jumlah akhir

ID errata: OK **40** · PARSIAL **7** · GAGAL **0** (dari 47).

Temuan baru: BLOCKER **0** · MAJOR **4** · MINOR **27** · total **31**.

Terbuka untuk kriteria freeze: BLOCKER 0 · MAJOR 5 (V-01..V-04 + residu N-06/M-20).

Freeze sah: **tidak**. Syarat yang tersisa:

1. Tutup V-01..V-04 dan residu SPEC.md:2193 lewat ADR-037. Residu MINOR (m-15, N-15, N-24, N-33) dan V MINOR boleh disertakan, atau dicatat sebagai errata pasca-freeze.
2. Verifikasi singkat atas lima lokasi MAJOR tersebut.
3. Bila lolos, tulis tanggal freeze di SPEC.md:3 dan §20 (ADR-030).

## §Verifikasi ADR-037 (2026-10-18)

Bagian ini memverifikasi ADR-037 (docs/DECISIONS.md:455), yang mengklaim menutup V-01..V-04 dan residu N-06/M-20, sekaligus menyentuh V-12. Semua lokasi yang disebut ADR dan semua rujukan `blob_expirations`/`blobexp`, CT-20, `TestCLIDocsMatchHelp`, dan `uninstall` dicek langsung oleh reviewer di SPEC.md + docs/*.md, tanpa subagen. Dokumen lain hanya dibaca. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka.

### Cek mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 164 link, 0 rusak |
| Permukaan CLI | 193 code span `` `damnbot …` `` di dokumen yang tidak dikecualikan; argumen pertama semuanya perintah §10 / hidden / builtin `help`. Satu-satunya pengecualian adalah `--version` (backlog V-26, MINOR). Subperintah `doctor voice\|gateway\|grammars\|index\|licenses\|tools\|health\|gc\|pprof\|bundle`, `sessions backup\|restore\|why\|purge`, `setup auth\|secrets\|ssh\|completion\|--uninstall`, `serve open\|sessions`, `import archive` semuanya ada di SPEC.md:966-990 |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| V-01 | OK | SPEC.md:1656-1657 (`blobexp_no_update`, `blobexp_no_delete`), SPEC.md:1755 (daftar append-only 17 tabel termasuk `blob_expirations`) | Trigger sudah ada di 0006 dengan pola pesan yang sama dengan tabel lain. |
| V-02 | OK, dengan catatan W-01 | SPEC.md:1613-1623 (event-log `id PK`, `kind ∈ {expired, restored}`, CHECK pasangan kind/reason, indeks `(sha256, id DESC)`, status = baris terakhir), SPEC.md:1545 (writer meng-INSERT `restored/rewrite` dalam transaksi yang sama), SPEC.md:552, :2093 | Bentrok PK dan kasus "isi sama ditulis ulang setelah gc" sudah tertutup; `TestBlobRewriteAfterGC` ada. Ada kasus lain yang belum tertutup: status expired berlaku untuk satu sha256, padahal keputusan retensi dibuat per aksi (W-01). |
| V-03 | OK | SPEC.md:2146 (satu daftar pengecualian `testdata/docs_exclusions.txt`, 5 entri, dipakai kedua test), SPEC.md:2145, SPEC.md:966 (cakupan scan: code span + baris code block, pengecualian = daftar §20), docs/ENGINEERING.md:574 (E-83 merujuk daftar yang sama) | Cakupan scan dan daftar pengecualian `TestCLIDocsMatchHelp` kini identik dengan `TestSpecNoPendingAmendments`. |
| V-04 | OK | SPEC.md:120, :2001, :2266; docs/CODE-TOOLS.md:534, :566; docs/SCORECARD.md:56; docs/MITIGATION-MATRIX.md:39, :128, :222; docs/ROADMAP.md:95 | Semua lokasi kini "≥ 325 kasus". Grep `310 kasus` / `≥ 300 kasus` / `suite edit ≥ 300` di dokumen normatif = 0. |
| N-06 / M-20 (residu) | OK | SPEC.md:2202 (kolom Verifikasi GAP-SEC-18: `damnbot setup --uninstall --purge-data`); grep `uninstall --purge` tanpa `-data` = 0 | Bentuk perintah sesuai §10 (SPEC.md:975). |
| V-12 | OK | SPEC.md:552 ("satu-satunya penghapus baris = `sessions purge` untuk blob yatim"), SPEC.md:1546 (purge menghapus `blob_expirations` lalu `blobs` **di dalam** transaksi, sebelum trigger `blobexp_no_delete` dibuat ulang; file dihapus setelah commit) | Urutan FK (`blob_expirations` → `blobs`) benar. `blobs` hanya punya trigger no-UPDATE (SPEC.md:1755), sehingga DELETE baris yatim di purge tidak bentrok. Tidak ada kontradiksi lagi antara aturan retensi dan purge. |

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| W-01 | MAJOR | SPEC.md:552 (gc menghapus file blob "yatim, **atau ditahan aksi journal yang sudah kedaluwarsa**"), SPEC.md:2093 (retensi undo 30 hari/2 GiB → file dihapus + `blob_expirations{kind:'expired'}`) ↔ SPEC.md:1072 (`events.blob_sha`), :1085 (`event_attachments.blob_sha`), :1135-1136 (`file_snapshots.before_sha/after_sha`), :1622 (status = baris terakhir **per sha256**), SPEC.md:552 ("tidak ada penghapusan otomatis transcript") | Blob disimpan per isi (content-addressed), jadi satu sha256 bisa sekaligus dirujuk before-image aksi yang sudah kedaluwarsa **dan** spill/attachment transcript yang masih hidup, mis. isi file yang sama hasil `read_file` yang di-spill. Aturan gc hanya melihat "ditahan aksi kedaluwarsa" dan status expired berlaku untuk seluruh sha. Akibatnya gc menghapus file yang masih dirujuk transcript, dan pembaca transcript mendapat `expired`. Ini melanggar "tidak ada penghapusan otomatis transcript". | Di SPEC.md:552 dan :2093, `doctor gc` hanya menghapus file blob yang **tidak punya referensi hidup**: tidak ada di `events.blob_sha`/`event_attachments`/`summaries`/memori, dan semua `file_snapshots` perujuknya milik aksi yang sudah kedaluwarsa. Blob yang masih dirujuk tetap disimpan dan tidak diberi baris `expired`; undo aksi lama tetap bisa memakai blob itu. Tambahkan `TestGCSharedBlobKept` (sha sama dirujuk snapshot aksi kedaluwarsa + spill event hidup → file tetap ada, status hidup). |

Dicek dan tidak ditemukan masalah:

- Konsistensi trigger append-only vs purge: purge me-DROP lalu membuat ulang trigger dalam satu transaksi.
- CHECK `(kind = 'expired') = (reason <> 'rewrite')`: `restored` hanya dengan `rewrite`, `expired` tidak pernah dengan `rewrite`.
- Lookup status lewat `idx_blob_exp_latest`.
- Bentuk tabel lama (`sha256 PRIMARY KEY`) tidak dirujuk lagi di dokumen mana pun: grep `blob_expirations` di luar SPEC/DECISIONS = 0.
- Race writer vs gc tidak dilaporkan. SPEC §3.3 menetapkan satu writer per data dir, sehingga INSERT `restored` dan `expired` terserialisasi [INFERENCE: gc berjalan di proses writer yang sama].

Backlog MINOR ADR-037 (DECISIONS.md:472-502: m-15, N-15, N-24, N-33, V-05..V-31 kecuali V-12) dicatat sebagai errata pasca-freeze F0, sesuai kriteria. Tidak diverifikasi di putaran ini.

### Jumlah dan verdict

ID yang diverifikasi: OK **6/6** (V-01, V-02, V-03, V-04, residu N-06/M-20, V-12) · PARSIAL 0 · GAGAL 0.

Temuan baru: BLOCKER **0** · MAJOR **1** (W-01) · MINOR **0**.

**Freeze sah: TIDAK.** W-01 masih terbuka sebagai MAJOR. Perbaikannya satu klausa aturan gc di SPEC.md:552 dan :2093 plus satu test, tanpa perubahan DDL, token, LOC, atau ukuran. Begitu W-01 ditutup lewat ADR singkat (atau amandemen ADR-037) dan lolos verifikasi titik, kriteria freeze terpenuhi: 0 BLOCKER, 0 MAJOR terbuka, dan seluruh MINOR tercatat di backlog ADR-037.

## §Verifikasi ADR-038 (2026-10-18)

Bagian ini memverifikasi ADR-038 (docs/DECISIONS.md:508-541), yang mengklaim menutup W-01 lewat gc berbasis perujuk hidup dan penguncian lintas proses. Yang dicek langsung oleh reviewer, tanpa subagen:

- semua lokasi yang disebut ADR: SPEC.md:552, :1545-1554, :1574-1575, :1624, :1631, :2102, :2153;
- seluruh DDL §11 untuk kolom/JSON yang bisa menyimpan sha256 blob;
- kontrak `Meta` tool di SPEC §4.3/§5.1 dan EXEC-AND-CODING;
- outbox gateway dan bridge;
- index §11;
- budget `Store.Append` di SPEC/SELF-HEALING/ROADMAP.

Dokumen lain hanya dibaca. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka.

### Cek mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 165 link, 0 rusak |
| Kolom DDL `REFERENCES blobs` | Ada 6: `events.blob_sha` (SPEC.md:1072), `event_attachments.blob_sha` (:1085), `file_snapshots.before_sha`/`after_sha` (:1135-1136), `blob_expirations.sha256` (:1623, log status). Ditambah 2 komentar `blob ref`: `turn_journal.before_sha`/`after_sha` (:1574-1575) dan `actions.inverse $.blobs[]` (:1604). Semuanya masuk daftar tertutup SPEC.md:1546-1552 |
| Budget `Store.Append` | ≤ 10 ms p99 Linux/Windows, ≤ 30 ms macOS. Angka sama di SPEC.md:553, :2043, SELF-HEALING.md:276, :593, ROADMAP.md:107, DECISIONS.md:533 |
| Trigger append-only (P9) vs protokol baru | Tidak ada bentrok. gc hanya INSERT `blob_expirations` dan tidak menyentuh baris `blobs`. Purge menghapus `blob_expirations` lalu `blobs` di dalam transaksi setelah DROP trigger, dan `blobs` hanya punya trigger no-UPDATE (SPEC.md:1307, :1764) |

### Status

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| W-01 | PARSIAL | SPEC.md:552, :1546-1552 (daftar tertutup + aturan "minimal satu perujuk hidup"), :2102 (`T_cut` tunggal, kedaluwarsa hanya melepas perujuk aksi/snapshot), :1631, `TestGCSharedBlobKept` (:1552) | Kasus yang dilaporkan sudah tertutup: sha yang dirujuk before-image aksi kedaluwarsa sekaligus `events.blob_sha` hidup tetap disimpan. Tetapi daftar perujuk belum lengkap terhadap kontrak tool (X-01), sehingga blob yang dirujuk transcript masih bisa terhapus. Residu dihitung sebagai X-01, tidak ganda. |

Yang sudah benar:

- Publikasi writer berjalan seperti ini: temp + `fsync` di luar lock, lalu `rename` + `fsync` dir + `INSERT blobs`/`restored` + baris perujuk dalam satu `BEGIN IMMEDIATE`. Kombinasi ini dengan langkah gc (`rename` → `.trash/` + `expired` dalam satu `BEGIN IMMEDIATE`) menghasilkan urutan total yang benar untuk pasangan **writer ↔ gc tunggal**: writer lebih dulu → perujuk ter-commit; gc lebih dulu → writer melihat `expired` → `restored`.
- Crash writer sebelum COMMIT → file final tanpa baris `blobs` dibersihkan gc di bawah lock.
- `T_cut` didefinisikan satu kali.
- Mode `--allow-network-fs` konsisten.

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| X-01 | MAJOR | SPEC.md:1547 (perujuk #1 = hanya kunci bernama `blob_sha` di `events.payload`) ↔ docs/EXEC-AND-CODING.md:111 ("log mentah ter-redaksi disimpan sebagai blob kedua, `Meta.raw_blob`"), :126 (`Meta` shell memuat `blob_sha` **dan** `raw_blob`), :298 (payload `tool_result` hanya `{…, blob_sha}`), SPEC.md:663 (`Meta` shell di §5.1 tanpa `raw_blob`), SPEC.md:485 (`Result.Meta map[string]any`) | Blob log mentah shell dirujuk lewat kunci `raw_blob`, bukan `blob_sha`. Kunci itu juga tidak tercantum di payload `tool_result` EXEC:298 maupun di §5.1. Akibatnya sha tersebut tidak punya perujuk hidup menurut daftar tertutup, sehingga `doctor gc` pertama menandainya `orphan` lalu menghapusnya. Hasilnya, setiap log mentah shell yang dispill hilang. Ini bertentangan dengan klaim ADR-038 "blob transcript tidak pernah hilang". `TestBlobRefColumnsListed` hanya memindai DDL dan komentar `blob ref`, jadi kunci JSON di `Meta` tidak terdeteksi. | (a) Satu konvensi: setiap ref blob di JSON memakai kunci `blob_sha` atau berakhiran `_blob_sha` (ganti nama `raw_blob` → `raw_blob_sha` di EXEC-AND-CODING:111/:126, tambahkan ke SPEC.md:663 dan payload EXEC:298). Perujuk #1 mencakup pola kunci itu. (b) Lebih kuat, dan sekaligus menutup X-03: setiap ref blob di payload/`Meta` disalin ke tabel append-only terindeks `event_blob_refs(event_id, sha)` di transaksi append yang sama. Perujuk #1 = `events.blob_sha` ∪ `event_blob_refs`. `TestBlobRefColumnsListed` diperluas ke validator payload, yang menolak kunci `*_sha` berisi sha blob di luar konvensi. Tambahkan `TestGCKeepsRawShellLog`. |
| X-02 | MAJOR | SPEC.md:1553 ("(3) COMMIT; (4) `unlink` isi `.trash/`"; "Recovery crash gc sebelum COMMIT: file di `.trash/` yang status terakhirnya bukan `expired` dikembalikan ke path final saat start/gc berikutnya"), SPEC.md:1554 (purge memakai protokol trash yang sama, lalu `unlink` isi `.trash/`), docs/DECISIONS.md:526-527 | Langkah `unlink` berlaku untuk **seluruh** isi `.trash/` dan dijalankan di luar lock setelah COMMIT. Proses B (gc) bisa menghapus file yang baru saja dipindahkan proses C (gc/purge lain, misalnya CLI `sessions purge` saat `serve` menjalankan gc) ke `.trash/` di dalam transaksi C yang **belum** commit. Bila C lalu rollback (kill -9, error, watchdog; skenario yang memang diuji P19/`TestKill9Chaos`), file hilang padahal statusnya hidup. Pada purge yang rollback, baris sesi dan perujuknya tetap ada tetapi file-nya sudah hilang. Kasus terbalik: recovery tidak dinyatakan berjalan di bawah lock, dan aturan "status terakhir bukan `expired`" juga cocok untuk file sisa purge yang sudah commit (baris `blobs`/`blob_expirations` sudah dihapus), sehingga file yang sudah di-purge dikembalikan ke path final (privasi). Properti "aman bersamaan" di DECISIONS.md:539 tidak terpenuhi. | Satu direktori trash per transaksi: `blobs/.trash/<txid>/` (txid dicatat di transaksi yang sama, mis. baris `module_state` atau nama unik pid+seq). Setelah COMMIT, hanya direktori milik transaksi itu yang di-`unlink`. Recovery berjalan di bawah `BEGIN IMMEDIATE`: file di `.trash/<txid>/` dikembalikan ke path final **hanya bila** baris `blobs` ada dan status terakhir bukan `expired`; selain itu dihapus. Gunakan rename yang menimpa (replace) agar aman di Windows bila file final sudah dipublikasi ulang. Tambahkan langkah ini ke urutan startup recovery SELF-HEALING §3. Test: `TestGCPurgeConcurrentRollback` (gc proses A + purge proses B yang di-kill sebelum COMMIT → 0 file hilang berstatus hidup, 0 file purge yang kembali). |
| X-03 | MAJOR | SPEC.md:1553 (gc: "satu transaksi `BEGIN IMMEDIATE` per batch (≤ 200 ms…)" dengan langkah "(1) pilih sha berstatus hidup tanpa perujuk hidup (kueri di atas)"), SPEC.md:1547 (`json_tree` atas `events.payload` "di kedalaman mana pun") ↔ DDL §11: tanpa index pada `events.blob_sha`, `event_attachments.blob_sha` (PK `(event_id, ord)`), `file_snapshots.before_sha`/`after_sha` (index satu-satunya `(path, id)`, SPEC.md:1140) ↔ SPEC.md:553, :2043 (`Store.Append` ≤ 10 ms p99), SPEC.md:2044 (`recall` @ 1.000.000 event), docs/SELF-HEALING.md:253 (watchdog store writer: transaksi > 10 s → exit 70) | Pemeriksaan "tanpa perujuk hidup" untuk setiap sha kandidat butuh full scan `events` dan `event_attachments`, ditambah `json_tree` atas **semua** `events.payload` (skala target 1.000.000 event), karena tidak ada index pada kolom perujuk dan ref di payload tidak termaterialisasi. Semua itu terjadi **di dalam** lock tulis global lintas proses. [INFERENCE: parse JSON + scan 1 juta baris = orde detik.] Akibatnya batas ≤ 200 ms per batch tidak realistis, semua writer (TUI/ACP/`serve`/gateway) terblokir selama gc sehingga `Store.Append` p99 dan latensi gateway jebol, dan watchdog store writer 10 s bisa terpicu (exit 70). | Tambahkan partial index `events(blob_sha) WHERE blob_sha IS NOT NULL`, `event_attachments(blob_sha)`, `file_snapshots(before_sha)`, `file_snapshots(after_sha)`, dan tabel `event_blob_refs(sha, event_id)` terindeks dari X-01(b), sehingga pemeriksaan per sha = beberapa index lookup tanpa `json_tree`. Pemilihan kandidat dilakukan di luar lock (snapshot baca); di bawah `BEGIN IMMEDIATE` cukup re-check per sha lewat index. Tetapkan batch kecil (mis. ≤ 100 sha, ≤ 20 ms lock). Tambahkan `BenchmarkGCBatch` (1.000.000 event, lock ≤ 20 ms p99) dan `BenchmarkAppendDuringGC` (`Store.Append` ≤ 10 ms p99 selama gc berjalan). |
| X-04 | MINOR | SPEC.md:1577 (`turn_journal.approval_request`: "preview sha") ↔ SPEC.md:1546-1552, SPEC.md §18.2 (diff panjang dikirim "sebagai dokumen") | Tidak jelas apakah "preview sha" adalah ref blob. Bila ya, preview untuk approval yang tertunda (hingga 24 jam) tidak punya perujuk hidup. | Nyatakan eksplisit. Bila blob, gunakan kunci `preview_blob_sha` dan masukkan ke perujuk #5 (journal terbuka). |
| X-05 | MINOR | SPEC.md:1432-1446 (`gateway_deliveries.body`, `event_id` nullable) ↔ SPEC.md §9.7.1 (`send{…, media_ref?}`), SPEC.md:844 (media hasil = blob) | Media outbound di outbox (TTS, gambar) yang diretry setelah gc tidak dijamin punya perujuk, terutama bila `event_id` NULL. | Tetapkan bahwa media outbound selalu berupa `event_attachments` dari `event_id` non-NULL, atau tambahkan perujuk `gateway_deliveries` (ref media di `body`) selama `status ∉ {sent, dead}`. |

### Jumlah dan verdict

ID diverifikasi: W-01 **PARSIAL** (residu = X-01).

Temuan baru: BLOCKER **0** · MAJOR **3** (X-01, X-02, X-03) · MINOR **2** (X-04, X-05).

**Freeze sah: TIDAK.** ADR-038 menutup kasus W-01 yang dilaporkan dan tidak bentrok dengan P9 maupun angka budget yang tertulis. Namun ADR itu memperkenalkan tiga MAJOR:

- daftar perujuk tidak lengkap terhadap `Meta.raw_blob`;
- `unlink` `.trash/` tidak dibatasi per transaksi dan recovery tidak dikunci;
- kueri liveness tanpa index dijalankan di bawah lock tulis global.

Usulan perbaikan yang paling ringkas mencakup ketiganya sekaligus:

1. Tabel append-only `event_blob_refs` + konvensi kunci `*_blob_sha` (X-01, X-03).
2. Index pada kolom perujuk + re-check di bawah lock lewat index saja (X-03).
3. Trash per transaksi + recovery di bawah lock yang mensyaratkan baris `blobs` (X-02).

Dampak: DDL bertambah (1 tabel, 4 index), `Store.Append` mendapat ≤ 1 INSERT tambahan per ref blob. Setelah dicatat lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; X-04/X-05 boleh masuk backlog pasca-freeze.

## §Verifikasi ADR-039 (2026-10-18)

Bagian ini memverifikasi ADR-039 (docs/DECISIONS.md:543-605), yang mengklaim menutup residu W-01 dan X-01..X-05 lewat `blob_refs` terindeks, gc dua fase, trash per transaksi, dan recovery di bawah lock. Reviewer mengecek langsung, tanpa subagen:

- semua lokasi yang disebut: SPEC.md:486, :552-553, :663, :1081, :1089, :1143-1144, :1444, :1453, :1551-1570, :1590-1593, :1620, :1641-1660, :1696-1697, :1795, :2074, :2133, :2237; EXEC-AND-CODING.md:111, :126, :298; SELF-HEALING.md:378;
- DDL yang memegang FK ke `blobs`, `events`, dan `sessions`;
- urutan purge terhadap FK;
- urutan recovery startup di SELF-HEALING §3;
- budget `Store.Append` di semua dokumen.

Dokumen lain hanya dibaca. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka; MINOR masuk backlog.

### Cek mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 166 link, 0 rusak |
| Sisa istilah lama | `json_tree`, `$.blobs[]`, `raw_blob` tanpa `_sha`, "preview sha", `TestGCTrashRecovery`, dan `.trash/<sha>` global hanya tersisa di ADR-038, yang baris Status-nya sudah menandai "digantikan ADR-039" (DECISIONS.md:510), serta di konteks ADR-039 |
| P9 | `blob_refs` memiliki `blobrefs_no_update`/`blobrefs_no_delete` (SPEC.md:1696-1697). Daftar append-only berisi 18 tabel (SPEC.md:1795), cocok dengan ADR. Purge me-DROP lalu membuat ulang trigger di transaksi yang sama |
| Indeks anak FK `blobs` | `idx_events_blob`, `idx_evatt_blob`, `idx_fsnap_before`/`after`, `idx_gw_deliveries_media`, `idx_blob_refs_sha`, `idx_blob_exp_latest`. Semua kolom `REFERENCES blobs` kini terindeks |
| Budget `Store.Append` | ≤ 10 ms p99 termasuk INSERT `blob_refs` (SPEC.md:553, :2074). Pengecualian "diukur tanpa gc" hanya tertulis di SPEC.md:1567 (Y-06) |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| W-01 (residu) / X-01 | OK | SPEC.md:1552-1563 (`blob_refs` + tabel owner; konvensi `blob_sha`/`*_blob_sha`/`*_blob_shas`; validator menolak hex 64 char dengan kunci lain), SPEC.md:663 (`raw_blob_sha` di `Meta` shell), EXEC-AND-CODING.md:111, :126, :298, SPEC.md:1649-1660 (DDL + indeks `blob_refs`) | Log mentah shell kini punya ref owner `event`. `TestGCRawBlobKept`, `TestBlobRefColumnsListed` (DDL + registri skema payload/`Meta`/`inverse`/`approval_request`), dan `TestBlobRefsComplete` (dinamis) menutup jalur "kunci tak dikenal". |
| X-02 | PARSIAL | SPEC.md:1565 (recovery di bawah lock, syarat baris `blobs` ada + status bukan `expired` + path final belum ada), :1567 (`.trash/<txid>/`, unlink hanya milik sendiri), :1568, :1570 (purge memakai protokol yang sama) | Race unlink lintas transaksi dan pemulihan file purge yang sudah COMMIT sudah tertutup. **Residu MAJOR (dihitung sekali):** pemicu recovery tidak menjamin file **hidup** kembali tepat waktu. Rinciannya di bawah tabel. |
| X-03 | OK | SPEC.md:1081, :1089, :1143-1144, :1453, :1659-1660 (indeks), :1566-1567 (kandidat di luar lock lewat snapshot WAL; re-check O(b × log n) di bawah `BEGIN IMMEDIATE`; ≤ 500 sha / ≤ 20.000 baris ref; deadline 150 ms, lock ≤ 200 ms; duty cycle ≤ 20 %; `TestGCLockBudget` 1.000.000 event) | Tidak ada lagi `json_tree` di jalur gc. Batas lock dipilih 200 ms, bukan 20 ms; ini trade-off yang terdokumentasi. Konsistensi angkanya lintas dokumen dicatat di Y-06. |
| X-04 | OK | SPEC.md:1593, :1560, :2237 | `preview_blob_sha` = ref owner `turn_journal`, hidup selama state terbuka. |
| X-05 | OK | SPEC.md:1444, :1453, :1561, :900 | `media_blob_sha` + owner `gateway_delivery`, hidup selama `status ∈ {queued, sending, failed, uncertain}`. Bridge `send.media_ref` diturunkan dari kolom ini. |

**Residu X-02.** Recovery hanya berjalan "saat start `serve`/TUI" dan "sebelum setiap gc" (SPEC.md:1565). Rollback atau kill sebelum COMMIT pada **purge** membuat file blob kembali berstatus hidup, karena baris sesi dan ref-nya dipulihkan oleh rollback, padahal file-nya masih di `.trash/<txid>/`.

- Proses pemilik tidak memulihkan trash-nya sendiri setelah rollback non-crash.
- ACP, `run`, dan CLI tidak menjalankan recovery saat open DB.
- SELF-HEALING §3 tidak mencantumkan langkah ini sama sekali: grep `trash` = 0.
- Pembaca yang mendapat ENOENT dengan status hidup langsung mengembalikan `blob_missing` (SPEC.md:1551).

Akibatnya, setelah purge CLI yang gagal atau di-kill, spill/lampiran transcript sesi yang **tidak jadi** di-purge tidak bisa dibaca dan dilaporkan sebagai error integritas. Kondisi ini bertahan sampai TUI/`serve` restart atau gc berikutnya, yang bisa berjam-jam pada `serve` yang terus berjalan. Ini melanggar properti "0 ref hidup ke file hilang" (`TestGCWriterRace`, SPEC.md:1568).

Perbaikan:

1. Pada rollback/error, proses pemilik segera menjalankan langkah 1 untuk `txid`-nya dalam `BEGIN IMMEDIATE` baru.
2. Setiap proses writer (TUI, ACP, `run`, `serve`, CLI) menjalankan langkah 1 saat open DB. Tulis langkah ini di urutan recovery SELF-HEALING §3.4.
3. Pembaca yang mendapat ENOENT dengan status hidup mencari `blobs/.trash/*/<sha>`; bila ditemukan, ia menjalankan langkah 1 di bawah lock lalu mencoba sekali lagi sebelum mengembalikan `blob_missing`.

Test: perluas `TestGCTrashPerTx` dengan kasus "purge di-kill/rollback sementara sesi dibaca proses lain → pembacaan berhasil ≤ 1 retry".

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| Y-01 | MAJOR | SPEC.md:1570 (purge: "`DELETE` baris sumber … → `DELETE FROM blob_refs WHERE session_id = ?` → baris `gateway_deliveries` milik sesi itu … yang belum terkirim → `status='dead'`, `last_error='purged'` → … `DELETE` baris `blob_expirations` dan `blobs`"), DECISIONS.md:585 ("Purge vs outbox") ↔ SPEC.md:1442 (`gateway_deliveries.event_id INTEGER REFERENCES events(id)`), :1444 (`media_blob_sha REFERENCES blobs(sha256)`), :1000 (`foreign_keys=ON`), :1443 (`body TEXT NOT NULL`) | Purge **mempertahankan** baris `gateway_deliveries` milik sesi: yang belum terkirim diubah jadi `dead`, yang `sent` dibiarkan. Padahal baris itu memegang FK ke `events` dan `blobs` yang dihapus di transaksi yang sama. (1) `DELETE` events sesi gagal pada FK `gateway_deliveries.event_id`; purge `gateway_inbound` sudah ditangani, deliveries belum. (2) `blob_refs` owner `gateway_delivery` sesi itu ikut terhapus, sehingga sha media tampak "tanpa ref sama sekali", lalu `DELETE blobs` gagal pada FK `media_blob_sha`. Dengan FK immediate, setiap purge sesi gateway yang punya outbox gagal deterministik dan rollback. Rollback itu juga memicu residu X-02 untuk file yang sudah dipindah ke trash. (3) Kolom `body` (teks pesan) sesi yang di-purge tetap tersimpan, sehingga tujuan privasi purge tidak tercapai. | Purge **menghapus** baris `gateway_deliveries` milik sesi (lewat `event_id` sesi atau `blob_refs` ber-owner `gateway_delivery` sesi itu) **sebelum** `DELETE` events, di transaksi yang sama. Sender yang sedang memegang baris `sending` akan mendapat 0 rows affected saat UPDATE dan memperlakukannya sebagai `purged`. Alternatif: UPDATE `event_id = NULL, media_blob_sha = NULL, body = '', status = 'dead', last_error = 'purged'` (tabel mutable), dengan tetap menghapus `blob_refs`-nya. Ubah juga DECISIONS.md:585. Test: `TestPurgeGatewaySession` (sesi dengan delivery `queued` + `sent` + media → purge sukses, 0 baris delivery berisi body sesi, 0 pelanggaran FK). |
| Y-02 | MAJOR | SPEC.md:1570 (purge per sesi: `purge <id>\|--older-than 90d`, SPEC.md:978), SPEC.md:551 ("Fork: sesi baru `parent_id`, `fork_seq`; event parent tidak disalin. Rewind = fork di seq lebih awal") ↔ SPEC.md:1025 (`parent_id TEXT REFERENCES sessions(id)`), :1544 (history fork-aware = rekursi `parent_id`), `blob_refs.session_id` = sesi owner (SPEC.md:1655) | Ditemukan saat memverifikasi purge; masalah ini sudah ada sebelum ADR-039, bukan efeknya. Riwayat sesi anak (fork, rewind, delegate) memakai event induk sampai `fork_seq` tanpa disalin, dan ref blob prefix itu ber-`session_id` induk. Purge induk: (a) bila baris `sessions` induk dihapus, FK `parent_id` anak gagal, sehingga purge sesi yang pernah di-rewind selalu gagal; (b) bila baris induk dipertahankan, event prefix dan blob-nya (`blob_refs WHERE session_id = induk`) terhapus, sehingga sesi anak yang **tidak** dipurge diam-diam kehilangan seluruh riwayat sebelum titik fork. Contohnya, sesi aktif hasil rewind kehilangan prefixnya saat `--older-than 90d` memilih induk yang lebih tua. Spec tidak memilih salah satu. | Tetapkan semantik purge di §11.6/§10: purge beroperasi pada **pohon** sesi. `purge <id>` ditolak dengan daftar turunan kecuali `--with-forks`; `--older-than` memakai aktivitas terakhir seluruh pohon (purge hanya bila semua anggota lebih tua). Alternatifnya, materialisasi prefix ke anak sebelum purge sebagai event `imported` baru dengan hash-chain baru dan ref blob ber-`session_id` anak. Test: `TestPurgeForkTree` (induk + 2 rewind → `purge` induk tanpa flag ditolak; dengan `--with-forks` 0 baris tersisa; riwayat sesi lain utuh). |
| Y-03 | MINOR | SPEC.md:1566-1567 (kandidat "file final tanpa baris `blobs`" diproses langkah 3: "rename … lalu INSERT `blob_expirations{kind:'expired', reason}`") ↔ SPEC.md:1639 (`blob_expirations.sha256 … REFERENCES blobs(sha256)`) | Untuk file sisa crash writer yang tidak punya baris `blobs`, INSERT `expired` melanggar FK, sehingga batch gagal atau implementer harus menebak. | Untuk sha tanpa baris `blobs`: re-check di bawah lock bahwa baris `blobs` **masih** tidak ada, lalu pindahkan file ke `.trash/<txid>/` **tanpa** baris `expired`. |
| Y-04 | MINOR | SELF-HEALING.md:378 ("baris `blobs` dibuat bila file blob ada, bila tidak ada ditulis `blob_expirations{…restore_missing}`") ↔ SPEC.md:1047-1052 (`blobs.size`, `mime` NOT NULL), :1072 (`events.blob_sha REFERENCES blobs`), :1639 | Salvage atas event yang file blob-nya hilang tidak bisa memenuhi FK `events.blob_sha`/`blob_refs`/`blob_expirations`, karena baris `blobs` tidak dibuat, dan `size`/`mime` tidak diketahui. Event itu gagal di-salvage. | Salvage membaca baris `blobs` dari DB rusak secara best-effort. Bila tidak terbaca, sisipkan baris placeholder (`size = 0`, `mime = 'application/octet-stream'`) lalu tulis `expired/restore_missing`. |
| Y-05 | MINOR | SELF-HEALING.md:377-378 (auto-restore `LAST_GOOD` + salvage), :369 (backup harian, simpan 3) ↔ SPEC.md:1569 (restore pengguna: pass `restore_missing` untuk semua baris `blobs` hidup), :1570 (purge), SPEC.md:552 ("`sessions purge` satu-satunya penghapusan") | (1) Auto-restore tidak menjalankan pass `restore_missing` global. Baris `expired` yang ditulis setelah backup hilang, sehingga blob yang sudah di-gc tampil hidup dan menghasilkan `blob_missing` palsu. (2) Auto-restore dari backup yang lebih tua dari purge **menghidupkan kembali** sesi yang sudah di-purge (baris DB), dan recovery trash bisa ikut mengembalikan file-nya karena baris `blobs` muncul lagi. Backup harian juga menyimpan data purge ≤ 3 hari tanpa dokumentasi. | Auto-restore memakai pass `restore_missing` yang sama dengan restore pengguna. Purge menulis tombstone (id sesi + waktu) ke log terpisah ber-`fsync` di luar DB, yang diterapkan ulang setelah auto-restore. Dokumentasikan retensi data purge di backup, atau sediakan opsi `--include-backups`. |
| Y-06 | MINOR | SPEC.md:1567 ("Target `Store.Append` ≤ 10 ms p99 diukur tanpa gc; selama gc tunggu tambahan ≤ 200 ms"; "gc terjadwal `serve` berjalan saat idle (tanpa turn aktif)") ↔ SPEC.md:553, :2074, :2082 (gateway inbound → `turn_start` ≤ 50 ms p95), SELF-HEALING.md:276, :593 (`BenchmarkAppendFull`), ROADMAP.md:107 | Pengecualian gc pada budget Append hanya tertulis di satu tempat. Nama benchmark-nya juga dua (`BenchmarkAppend` vs `BenchmarkAppendFull`). "Idle" hanya dilihat dari proses `serve`, padahal TUI/ACP di proses lain dan inbound gateway bisa sedang aktif, dan budget gateway p95 tidak diberi pengecualian. | Tulis pengecualian yang sama di SPEC.md:553/:2074, SELF-HEALING P22-9, dan ROADMAP; pakai satu nama benchmark. Definisikan idle = 0 lease sesi aktif di **semua** proses (`session_leases`) dan 0 inbound dalam 60 s. Tambahkan `BenchmarkAppendDuringGC` (p99 ≤ 210 ms) sebagai batas eksplisit. |
| Y-07 | MINOR | SPEC.md:1551 (publikasi hanya untuk ref tertunda dari `PutBlob`) ↔ SPEC.md:1561 (`media_blob_sha`), :1620 (`inverse.before_blob_shas`) | Owner baru yang merujuk sha **yang sudah ada** tanpa `PutBlob` belum diatur. Contohnya delivery yang mengirim ulang lampiran event, atau aksi yang merujuk sha snapshot. Bila sha itu sudah `expired`, terbentuk ref hidup ke file yang tidak ada. | Setiap INSERT ref ke sha yang sudah ada memeriksa status di transaksi yang sama. Bila `expired`, operasi gagal dengan `blob_expired` (atau pemanggil wajib memakai `PutBlob` dengan bytes). Tambahkan kasus ini ke `TestBlobRefsComplete`. |

Dicek dan tidak ditemukan masalah:

- **Urutan writer ↔ gc di bawah lock.** Writer lebih dulu: ref ter-commit, sehingga re-check melewati sha itu. gc lebih dulu: writer melihat `expired`, menulis ulang file, dan menambah `restored`.
- **Recovery vs `.trash/<txid>/` milik transaksi lain yang sudah COMMIT.** File gc berstatus `expired` dan file purge tidak punya baris `blobs`, jadi keduanya dihapus. Tidak ada pemulihan file purge pasca-COMMIT.
- **Kecukupan `blob_refs` sebagai pengganti FK.** Setiap kolom `REFERENCES blobs` punya baris `blob_refs`, sehingga "tanpa `blob_refs`" berarti tanpa anak FK. Pengecualiannya adalah baris yang dipertahankan purge (Y-01).
- **Margin `T_gc = T_cut − 3.600 s` terhadap undo.** Bila kuota menggeser `T_cut` lebih dari 1 jam di tengah undo, pembaca yang membuka file tetap aman (POSIX / `FILE_SHARE_DELETE`), dan ENOENT dijawab `expired`.
- **Restore pengguna.** Memakai jalur publikasi + `restore_missing`.
- **Salvage sebelum gc.** Ref diturunkan ulang lewat `BlobRefsOf`.

### Jumlah dan verdict

ID diverifikasi (6): OK **5** (residu W-01, X-01, X-03, X-04, X-05; residu W-01 dan X-01 satu baris tabel) · PARSIAL **1** (X-02) · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **2** (Y-01, Y-02) · MINOR **5** (Y-03..Y-07).

MAJOR terbuka untuk kriteria freeze: **3**, yaitu residu X-02, Y-01, dan Y-02. BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** Desain `blob_refs` + gc dua fase + trash per transaksi benar dan menutup X-01/X-03/X-04/X-05. Sisa masalah ada di jalur **purge**:

- outbox yang dipertahankan melanggar FK dan menyimpan body pesan (Y-01);
- pohon fork/rewind tidak terdefinisi (Y-02);
- file hidup tertahan di trash setelah purge gagal karena pemicu recovery tidak lengkap (residu X-02).

Ketiganya edit teks/protokol lokal di SPEC §11.6/§10 dan SELF-HEALING §3.4, tanpa perubahan budget token/LOC/ukuran. Y-02 butuh satu keputusan semantik: tolak tanpa `--with-forks`, atau purge seluruh pohon. Setelah dicatat lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; Y-03..Y-07 masuk backlog pasca-freeze.

## §Verifikasi ADR-040 (2026-10-18)

Bagian ini memverifikasi ADR-040 (docs/DECISIONS.md:607-678), yang mengklaim menutup residu X-02 dan Y-01, Y-02 (MAJOR) serta Y-03..Y-07 (MINOR). Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen, mencakup:

- protokol purge tiga fase (SPEC.md:1588-1595) dan tabel urutan 31 langkah (SPEC.md:1597-1631);
- **graf FK lengkap** yang diekstrak mekanis dari DDL §11.2-§11.9: 46 kolom `REFERENCES` dari 33 tabel, dipetakan satu per satu ke langkah purge;
- ketersediaan indeks berawalan kolom FK untuk setiap kolom `REFERENCES`;
- urutan startup writer (docs/SELF-HEALING.md:345-350), backup/restore, salvage, dan disk-full;
- konsistensi lintas dokumen (P9, P19, P22, P27, §4.6, §16, §17.1, §20) dan dokumen pendamping.

Dokumen lain read-only. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka; MINOR masuk backlog.

### Cek mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 167 link, 0 rusak |
| Kolom `REFERENCES` di DDL | 46 kolom / 33 tabel (sessions 13, events 13, blobs 6, memory 4, actions 1, reasoning_carriers 1, cron_jobs 1, kanban_boards 1, kanban_tasks 1, import_runs 1) |
| Setiap kolom FK punya langkah purge | 46/46 tercakup (peta di bawah) |
| Setiap kolom FK punya indeks berawalan kolom itu | 46/46, sehingga `TestFKChildIndexes` dapat dipenuhi; 17 di antaranya berkomentar eksplisit ADR-040, sisanya memakai indeks/`UNIQUE`/PK yang sudah ada |
| Nomor langkah tabel purge | 1..31, tanpa celah/duplikat |
| `session_labels.key` CHECK | memuat `purge_state` (SPEC.md:1041) |
| P9 | pengecualian purge tertulis di SPEC.md:106; trigger `blobrefs_no_update`/`no_delete` ada; setiap transaksi purge me-DROP lalu membuat ulang trigger, dan DDL SQLite transaksional sehingga rollback tidak meninggalkan trigger hilang |
| Nama test ADR-040 | 16/16 ada di SPEC/DECISIONS/SELF-HEALING |
| Nama benchmark | tunggal: `BenchmarkAppend` + `BenchmarkAppendDuringGC` (SELF-HEALING.md:276 tidak lagi memakai `BenchmarkAppendFull`) |
| Jendela pemeliharaan gc/purge | konsisten di SPEC.md:553 (§4.6), :1591 (§11.6), :2141/:2149 (§17.1), SELF-HEALING.md:276/:601, ROADMAP.md:107 |

### Peta FK → langkah purge

| Induk | Kolom anak | Langkah |
|---|---|---|
| `sessions` | `parent_id` (self) | 30 (satu statement; P tertutup terhadap turunan) |
| | `session_labels.session_id`, `session_leases.session_id` | 29 |
| | `events.session_id` | 24 |
| | `summaries.session_id` | 20, 27 |
| | `usage_ledger.session_id` | 19 (pindah ke `__system__`), 27 |
| | `approvals.session_id` | 15, 27 |
| | `memory.source_session` | 8 |
| | `gateway_conversations.session_id` | 2 |
| | `cron_runs.session_id`, `kanban_tasks.session_id` | 4 (NULL) |
| | `actions.session_id` | 13, 27 |
| | `plans.session_id` | 16, 27 |
| | `reasoning_carrier_rejections.session_id` | 17, 27 |
| | `turn_journal.session_id` | 3 |
| | `blob_refs.session_id` | 28 |
| `events` | `event_attachments.event_id` | 21 |
| | `file_snapshots.event_id` | 14 |
| | `approvals.event_id` | 15 |
| | `memory.evidence_event_id` | 8 |
| | `usage_ledger.event_id` (NOT NULL) | 19 (UPDATE ke event audit) |
| | `summaries.event_id` | 20 |
| | `gateway_deliveries.event_id` | 1, 10 |
| | `gateway_inbound.event_id` | 11 |
| | `actions.event_id` | 13 |
| | `action_undos.event_id` | 12 |
| | `plans.event_id` | 16 |
| | `reasoning_carriers.event_id` | 18 |
| `memory` | `memory_pins.memory_id`, `memory_embeddings.memory_id`, `memory_decisions.memory_id` | 7 |
| | `skills.memory_id` | 5 (NULL) |
| | `memory.supersedes_id` (self) | 6 (NULL) |
| `blobs` | `events.blob_sha`, `event_attachments.blob_sha`, `file_snapshots.before_sha`/`after_sha`, `gateway_deliveries.media_blob_sha`, `blob_expirations.sha256`, `blob_refs.sha256` | 1, 10, 13, 14, 21, 22, 25 |
| `actions` | `action_undos.action_id` | 12 |
| `reasoning_carriers` | `reasoning_carrier_rejections.carrier_id` | 17 |
| `cron_jobs` | `cron_runs.job_id` | tidak dihapus (job tetap) |
| `kanban_boards` | `kanban_tasks.board_id` | tidak dihapus (board tetap) |
| `kanban_tasks` | `kanban_task_events.task_id` | tidak dihapus (tugas tetap, hanya `session_id` di-NULL) |
| `import_runs` | `imported_items.run_id` | tidak dihapus (riwayat impor tetap) |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| Y-01 | OK | SPEC.md:1597-1631 langkah 1 dan 10 (`gateway_deliveries` dihapus di fase 1 untuk status belum final dan di fase 2 untuk sisa, termasuk `body` dan `media_blob_sha`), `TestPurgeGatewaySession`, `TestDeliveryHasEvent` | Delivery sesi ter-purge benar-benar dihapus, bukan dipertahankan sebagai `dead`, sehingga FK `event_id`/`media_blob_sha` tidak lagi memblok `DELETE events`/`blobs` dan `body` tidak tersisa. Residu kecil di Z-02. |
| Y-02 | OK | SPEC.md:1588 (`has_forks` + turunan), :1592 (hitung ulang turunan di bawah lock), :978 (`--with-forks`), langkah 30, `TestPurgeRefusesForks`, `TestPurgeWithForks` | Purge beroperasi pada pohon: ditolak tanpa flag bila ada turunan; `--older-than` memakai keanggotaan subtree. Karena event induk tidak pernah disalin dan blob prefix ber-`session_id` induk, purge anak tidak menghapus prefix milik induk yang tetap hidup. |
| Y-03 | OK | SPEC.md:1584 (sha tanpa baris `blobs` → `rename` ke `.trash/<txid>/`, tanpa `expired`), `TestGCOrphanFileNoRow` | File tanpa baris `blobs` tidak lagi menulis `expired`, jadi FK aman. |
| Y-04 | OK | docs/SELF-HEALING.md:378 (urutan induk-dulu: `sessions` → `blobs` (dibaca dari DB rusak atau placeholder `size`/`mime`) → owner → `blob_refs`), `TestSalvageMissingBlob` | FK `events.blob_sha`/`blob_refs`/`blob_expirations` terpenuhi walau file blob hilang. |
| Y-05 | OK | SPEC.md:1569 (restore pengguna **dan** auto-restore menjalankan pass `restore_missing` → terapkan ulang ledger → recovery trash), SPEC.md:1590 (`purge.log`, `intent`/`committed`/`aborted`), docs/SELF-HEALING.md:386, `TestAutoRestoreReappliesPurge`, `TestAutoRestoreRestoreMissing`, `TestPurgeLedgerReapply` | Urutan (3) sebelum (4) menjamin sesi ter-purge tidak dihidupkan lagi dan file-nya tidak dipulihkan dari trash. |
| Y-06 | OK | SPEC.md:553, :1591, :2141, :2149; docs/SELF-HEALING.md:276, :601; docs/ROADMAP.md:107 | Satu aturan jendela pemeliharaan di empat dokumen, satu nama benchmark, definisi idle eksplisit (0 lease belum kedaluwarsa di semua proses dan 0 inbound dalam 60 s), dan gateway inbound diberi pengecualian eksplisit dengan batas ≤ 200 ms. |
| Y-07 | OK | SPEC.md:1568 (INSERT `blob_refs` wajib + cek baris `blobs` ada dan status bukan `expired`; gagal → `blob_expired`; cek dan INSERT di bawah lock yang sama dengan re-check gc), `TestBlobRefsComplete` | Tidak ada ref hidup baru ke file yang sedang atau sudah dipindah gc. |
| Residu X-02 | OK | SPEC.md:1583 (empat pemicu recovery: (a) setiap proses writer saat open DB, (b) sebelum gc, (c) proses pemilik segera setelah rollback, (d) pembaca yang mendapat ENOENT untuk sha hidup, ≤ 50 ms), docs/SELF-HEALING.md:345-350 (langkah 2 sebelum langkah 3 dan 4) | Empat pemicu menutup celah waktu; transaksi diulang setelah recovery. |

Kasus yang saya periksa khusus dan **tidak** menemukan masalah:

- **Urutan writer ↔ gc/purge/recovery** tetap total di bawah `BEGIN IMMEDIATE`; `.trash/<txid>/` tidak pernah disentuh proses lain kecuali recovery yang dikunci.
- **Resume purge vs journal recovery.** Fase 1 sudah COMMIT (label + hapus `turn_journal` P) sebelum crash; saat resume, langkah startup 3 (purge) berjalan sebelum langkah 4 (journal), dan `resume` atas sesi ber-`purge_state` ditolak `session_purging`. Jadi journal recovery tidak menulis event baru ke sesi yang sedang dipurge, dan tidak ada event di luar `E_P` yang menggantung di langkah 30.
- **Kecukupan `blob_refs` sebagai pengganti FK.** Setiap kolom `REFERENCES blobs` punya baris `blob_refs`, dan langkah 28 menghapus ref milik sesi; ref milik sesi lain (dedup content-addressed) tetap, sehingga blob bersama tidak terhapus.
- **P27.** `usage_ledger` dipindah ke `__system__` dengan `event_id` = event audit purge, sehingga total cap tidak berubah (`TestPurgeKeepsBudgetTotals`).
- **P19/P22.** Purge berjalan di goroutine latar dengan batas transaksi yang sama; recovery ke layanan ≤ 10 s tidak dihalangi purge panjang.
- **P9.** Hanya purge yang boleh UPDATE/DELETE tabel append-only (SPEC.md:106), dan trigger dibuat ulang dalam transaksi yang sama.
- **`DELETE FROM sessions WHERE id IN P` dengan self-FK `parent_id`.** SQLite memeriksa FK immediate di akhir statement dan P tertutup terhadap turunan, jadi tidak ada pelanggaran [INFERENCE: perilaku SQLite yang didokumentasikan, tetap diverifikasi `TestPurgeFKOrder` + `PRAGMA foreign_key_check`].

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| Z-01 | MAJOR | SPEC.md:1591 ("Setiap transaksi `BEGIN IMMEDIATE` ≤ 200 ms … jadi P19 (0 `SQLITE_BUSY`) berlaku untuk sesi sebesar apa pun"; `TestPurgeLockBudget`: subtree 100.000 event) ↔ SPEC.md:1592 ("1. **Tandai & lepas** (satu transaksi)"), SPEC.md:1594 ("3. **Final** (satu transaksi): langkah 27–31"), SPEC.md:1593 (hanya fase 2 yang dibatch ≤ 2.000 event) | Batas ≤ 200 ms per transaksi dijamin untuk **ukuran event**, tetapi tidak untuk **jumlah sesi**. Fase 1 dan fase 3 dinyatakan "satu transaksi" dan keduanya berskala \|P\|: fase 1 menulis satu `session_labels` per sesi plus langkah 1–3 untuk seluruh P sekaligus; fase 3 menjalankan langkah 27–31 untuk seluruh P sekaligus, termasuk `DELETE session_labels`/`leases` dan `DELETE FROM sessions WHERE id IN P`. Pada `purge --older-than 90d --with-forks` dengan puluhan ribu sesi kecil (banyak sesi, sedikit event), kedua fase melewati 200 ms, sehingga P19 tidak terjamin dan writer lain bisa menabrak `busy_timeout` 5 s. `TestPurgeLockBudget` hanya memuat 100.000 **event** di sedikit sesi, jadi tidak menangkap kasus ini. Klausa generik "Deadline tercapai di tengah batch → COMMIT langkah yang sudah selesai" tidak dapat diterapkan pada fase 1/3 sebagaimana ditulis, karena keduanya disebut eksplisit "satu transaksi". | Batch fase 1 dan fase 3 per **sesi** (mis. ≤ 2.000 sesi atau ≤ 2.000 baris yang akan dihapus per transaksi): `BEGIN IMMEDIATE` → subset langkah untuk sesi batch itu → COMMIT → lanjut. Pertahankan invariant yang ada: label `purge_state` ditulis di transaksi pertama sebelum fase 2 (agar sesi tidak terlihat lebih awal), ledger `intent` sebelum COMMIT pertama, `committed` setelah batch terakhir fase 3, dan audit `purge_done` di transaksi terakhir. Tambahkan `TestPurgeLockBudgetManySessions` (≥ 20.000 sesi × 1 event, setiap transaksi ≤ 200 ms) dan sebutkan kasus ini di SPEC.md:1591. |
| Z-02 | MINOR | SPEC.md:1606 (langkah 28: "DELETE `blob_refs` `session_id ∈ P` (sisa) lalu langkah 25") ↔ SPEC.md:1608 (langkah 30), SPEC.md:1449 (`gateway_deliveries.media_blob_sha REFERENCES blobs(sha256)`), :1441 (`event_id INTEGER REFERENCES events(id)`, nullable) ↔ SPEC.md:1597-1605 (langkah 1 dan 10 hanya menghapus delivery belum final / `event_id ∈ E_b`) | Tidak ada langkah yang menangani baris `gateway_deliveries` yang **tetap hidup** padahal `session_id ∈ P`: status `sent`/`dead` dengan `event_id` NULL atau di luar `E_P`. Langkah 28 menghapus `blob_refs`-nya, lalu langkah 25 mencoba `DELETE blobs` → FK `media_blob_sha` menolak → transaksi fase 3 rollback, ledger tetap `intent`, dan resume berikutnya gagal dengan cara yang sama, sehingga purge tidak pernah selesai. Kasus ini hanya tertutup bila `TestDeliveryHasEvent` benar-benar melarang delivery ber-`session_id` non-`__system__` tanpa `event_id`; teksnya tidak menyatakan hal itu secara eksplisit. | Tambahkan langkah eksplisit di fase 3 **sebelum** langkah 28: hapus baris `gateway_deliveries` dengan `session_id ∈ P` yang belum terhapus, atau set `media_blob_sha = NULL`, `body = ''`, `status = 'dead'`, `last_error = 'purged'`. Perjelas di SPEC.md:1600 bahwa `TestDeliveryHasEvent` memaksa `event_id` non-NULL untuk setiap delivery milik sesi. |
| Z-03 | MINOR | SPEC.md:1592 (wajib: dikecualikan dari `sessions ls/show/search/export`, `recall`, web UI, gateway, `doctor --verify`) ↔ SPEC.md:1544-1553 (tabel "Kueri utama" tidak memuat filter itu), SPEC.md:1546 (`recall` = `events_fts`/`summaries_fts MATCH ? ORDER BY bm25) | Filter `purge_state` tidak tercermin di tabel kueri §11.6 padahal itu jalur baca utama; implementer harus menebak bentuknya, dan `recall` menyentuh `events_fts` contentless yang butuh join ke `sessions`/`session_labels`. Risiko nyata: sesi yang sedang dipurge muncul di `ls`/`recall` antara fase 1 dan fase 3. | Tambahkan baris eksplisit di tabel §11.6 untuk setiap kueri yang menyentuh sesi (`… AND NOT EXISTS (SELECT 1 FROM session_labels WHERE session_id=… AND key='purge_state')`; indeks `idx_session_labels` sudah ada) dan nama test `TestPurgeHiddenFromQueries` (ls/show/search/recall/export/web/gateway/verify). |
| Z-04 | MINOR | SPEC.md:1588 ("`--older-than D`: aktivitas terakhir sesi = `created_at` event terakhirnya") ↔ SPEC.md:1029-1037 (`sessions` memuat `created_at`) | Sesi tanpa event (dibuat lalu ditinggalkan, atau wizard yang batal) tidak punya "event terakhir", sehingga tidak pernah memenuhi `--older-than` dan tidak pernah dipurge. | Tetapkan fallback `COALESCE(MAX(events.created_at), sessions.created_at)`; untuk subtree, aktivitas terakhir = maksimum seluruh anggota. Tambahkan kasus sesi 0-event ke `TestPurgeWithForks`. |
| Z-05 | MINOR | SPEC.md:509 (daftar kode error §4.3) ↔ SPEC.md:116 (`db_busy`), :1588 (`has_forks`), :1592 (`session_purging`), :1568 (`blob_expired`), :1567 dan :1586 (`blob_missing`) | Lima kode error yang diwajibkan teks normatif tidak ada di satu daftar tertutup mana pun; daftar §4.3 hanya menerima `cold_outbound_denied` dari gelombang sebelumnya. Implementer bisa memetakannya diam-diam ke `internal` sehingga test dan klien tidak bisa membedakan penyebabnya. | Tambahkan ke §4.3 atau ke tabel kecil "kode error store/CLI/maintenance" di §3.5: `db_busy`, `blob_missing`, `blob_expired`, plus kode CLI `has_forks` dan `session_purging` beserta exit code-nya. |
| Z-06 | MINOR | SPEC.md:1590 (resume saat open DB), :1592 (fase 1 "ambil lease semua sesi P … gagal → `ErrSessionBusy`"), :1595 (resume di goroutine latar), docs/SELF-HEALING.md:347 (resume pada setiap proses writer) | Resume tidak mengatur kepemilikan lease: setelah crash, baris `session_leases` P tetap ada sampai kedaluwarsa (heartbeat 2 s, TTL 10 s). Tidak dinyatakan apakah resume mengambil alih atau memperpanjang lease itu, apa yang terjadi bila dua proses membuka DB bersamaan dan keduanya memulai resume, dan apakah lease di-heartbeat selama fase 2/3 yang bisa berlangsung lebih dari 10 s pada purge besar. | Nyatakan eksplisit: resume berjalan di bawah `BEGIN IMMEDIATE` dan hanya dimulai bila tidak ada pemegang lease P yang masih hidup, atau ambil alih lease kedaluwarsa di transaksi yang sama; purge me-heartbeat lease P setiap ≤ 5 s selama fase 2–3; `has_forks` dan lease dihitung ulang saat resume. Sebutkan di `TestPurgeResumeAfterKill`. |
| Z-07 | MINOR | SPEC.md:1595 (resume di goroutine latar pada **setiap** proses writer, termasuk `run`/CLI) ↔ SPEC.md:1591 (batas ≤ 200 ms/transaksi) | Proses berumur pendek (`damnbot run` satu turn, hook, CLI lain) akan menjalankan purge besar di latar sambil berebut lock tulis dengan pekerjaannya sendiri, lalu keluar: pengguna bisa melihat sesi "menghilang" (label sudah ditulis, fase 1 ter-commit) padahal perintah yang dijalankannya bukan purge. Fungsional tetap benar karena label otoritatif dan resume berlanjut di proses berikutnya, tetapi efek samping pada proses non-daemon tidak dinyatakan. | Batasi resume otomatis ke proses panjang (`serve`, TUI) atau beri kuota: mulai resume hanya bila proses berumur > N menit atau dipicu eksplisit `doctor gc`/`sessions purge`; proses pendek cukup mencatat dan menyerahkan ke proses berikutnya. Sebutkan di SELF-HEALING §3.4. |

### Jumlah dan verdict

ID diverifikasi: OK **8** (Y-01, Y-02, Y-03, Y-04, Y-05, Y-06, Y-07, residu X-02) · PARSIAL **0** · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **1** (Z-01) · MINOR **6** (Z-02..Z-07).

MAJOR terbuka untuk kriteria freeze: **1** (Z-01). BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-040 menutup seluruh item yang dilaporkan dan desainnya benar: graf FK lengkap 46/46 kolom punya langkah purge, 46/46 punya indeks berawalan FK, nomor langkah 1..31 tanpa celah, P9/P27/P22/P19 tetap konsisten, dan urutan startup writer menutup residu X-02. Sisa satu MAJOR: batas ≤ 200 ms/transaksi tidak dapat dipenuhi fase 1 dan fase 3 untuk purge dengan banyak sesi, dan test yang ditetapkan tidak menangkapnya. Perbaikannya batching per sesi pada kedua fase, tanpa perubahan DDL, token, LOC, atau ukuran binary. Setelah Z-01 ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; Z-02..Z-07 masuk backlog pasca-freeze.

### Perintah pengecekan

```bash
# Graf FK dari DDL §11 (46 kolom)
awk '/^CREATE TABLE /{t=$3; sub(/\\(.*/,"",t); cur=t}
     cur && /REFERENCES/{n=split($0,a,"REFERENCES");
       pre=a[1]; gsub(/^[ \\t]+/,"",pre); split(pre,b,"[ \\t]+");
       post=a[2]; gsub(/^[ \\t]+/,"",post); match(post,/^[a-z_]+/);
       printf "%s.%s -> %s\\n", cur, b[1], substr(post,RSTART,RLENGTH)}
     /^\\) STRICT;/{cur=""}' SPEC.md | sort -u | wc -l

# Indeks berawalan kolom FK (setiap kolom REFERENCES harus punya)
grep -E 'CREATE (UNIQUE )?INDEX' SPEC.md | grep -c 'indeks anak FK (ADR-040'   # 17 eksplisit; sisanya indeks/PK/UNIQUE lama

# Nomor langkah purge
awk '/^\\| # \\| Fase \\| Tabel/,/^$/' SPEC.md | grep -oE '^\\| [0-9]+ \\|' | tr -d '| ' | tr '\\n' ' '
```

## §Verifikasi ADR-041 (2026-10-18)

Bagian ini memverifikasi ADR-041 (docs/DECISIONS.md:680-740), yang mengklaim menutup Z-01 (MAJOR) dan Z-02..Z-07 (MINOR). Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen, dan mencakup:

- lokasi yang disebut: SPEC.md:314, :509-519, :1569-1582, :1605-1615, :1617-1651; docs/SELF-HEALING.md:349;
- jalur tulis yang menyentuh sesi: §3.3 lease, §4.6 `Store`, §3.4 diagram turn, recovery journal SELF-HEALING §3.4;
- kontrak exit code (§3.3 `serve`, §7.2 `run`, OPS-06 `doctor`, §4.3 tabel baru);
- sisa istilah protokol tiga fase ADR-040 di semua dokumen;
- konsistensi dengan P9, P19, P22, P27, §17.1, ROADMAP.

Dokumen lain read-only. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka; MINOR masuk backlog lewat ADR.

### Cek mekanis

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 168 link, 0 rusak |
| Sisa protokol lama | "tiga fase" / "Tandai & lepas" / "ambil lease semua sesi P" hanya tersisa di ADR-040, yang baris Status-nya sudah menandai "protokol fase … digantikan ADR-041" (DECISIONS.md:609) |
| Nama test ADR-041 | `TestPurgeLockBudget` (kasus 50.000 sesi), `TestPurgeResumeIdempotent`, `TestPurgeNewForkExcludes`, `TestPurgeHiddenFromQueries`, `TestErrorCodesClosed`, `TestPurgeLeaseTakeover`, `TestPurgeResumeOnlyLongLived`: semuanya ada di SPEC + DECISIONS |
| Kode error | 6 kode baru masuk daftar tertutup §4.3 (SPEC.md:509) + tabel sumber/arti/exit (SPEC.md:513-519); `session_busy` juga dirujuk di §3.3 (SPEC.md:314) |
| P9 | tetap: purge satu-satunya pengecualian (SPEC.md:1604); setiap transaksi me-DROP/CREATE trigger tabel yang disentuh (SPEC.md:1610) |
| P27 | `usage_ledger` dipindah ke `__system__` (langkah 19/27); kolom `gateway_identity` tidak disentuh, jadi cap USD per user tetap terhitung |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| Z-01 | OK | SPEC.md:1610 (setiap transaksi ≤ 200 ms, batas jumlah **dan** deadline 150 ms, duty cycle ≤ 20%; `TestPurgeLockBudget` (b) 50.000 sesi × 2 event), :1611 (fase 0 label-only, ≤ 2.000 sesi/batch), :1612 dan :1614 (fase 1/3 ≤ 50 sesi/batch, turunan-dulu), :1613 (fase 2 ≤ 2.000 event), :1650 (langkah 30 per batch, turunan-dulu) | Fase yang berskala jumlah sesi kini dibatch, dan unit COMMIT parsial didefinisikan per sesi/per event. I1 (himpunan berlabel tertutup-turunan) + urutan post-order menjamin langkah 30 tidak melanggar FK `parent_id`. |
| Z-02 | OK | SPEC.md:1648 (langkah 28(a): DELETE delivery status apa pun yang ber-`blob_refs` owner `gateway_delivery` sesi batch, **sebelum** (b)/(c)), :1621 (aturan outbox: delivery sesi selain `__system__` wajib `event_id`, `TestDeliveryHasEvent` memeriksa writer) | Delivery tanpa `event_id` dan tanpa media tidak memegang FK, dan tidak bisa milik sesi karena invarian writer. Fase 3 tidak lagi bisa gagal permanen karena outbox. |
| Z-03 | OK | SPEC.md:1571-1580 (filter `purging` di history, `recall`, `ls`/web, `show/export/why`/resume/gateway, `search`, `doctor --verify`), :1582 (definisi `purging(s)` + helper wajib `ctxdb.Visible`, `TestPurgeHiddenFromQueries`) | Jalur baca **sesi** tertutup. Jalur baca turunan (memori, outbox) belum; dicatat sebagai AA-02, bukan residu, karena di luar cakupan Z-03. |
| Z-04 | OK | SPEC.md:1605 (aktivitas = `created_at` event `seq` maksimum, atau `sessions.created_at` untuk sesi tanpa event; subtree = maksimum anggota; kasus 0 event di `TestPurgeWithForks`) | Kalimatnya rusak secara tata bahasa ("bila sesi tidak punya event sesi tanpa event (…) dengan begitu tetap bisa dipurge"), tetapi maknanya jelas. Dicatat sebagai bagian AA-08. |
| Z-05 | OK | SPEC.md:509 (daftar tertutup), :511 (`TestErrorCodesClosed`), :513-519 (tabel kode → sumber, arti, exit CLI) | Kelima kode + `session_busy` kini tertutup. Semantik exit dicatat di AA-04. |
| Z-06 | OK | SPEC.md:1608 (lease purge per `txid` di `module_state`; heartbeat ≤ 5 s, TTL 30 s; fencing di awal setiap batch; lepas normal `heartbeat_at = 0`; takeover CAS hanya bila dilepas/kedaluwarsa/pemilik mati di `host`+`boot_id` sama; `TestPurgeLeaseTakeover`), docs/SELF-HEALING.md:349 | Dua proses yang membuka DB bersamaan → tepat satu pemilik; proses yang terhenti > TTL ditolak fencing. PID reuse tidak berbahaya karena fallback ke TTL. |
| Z-07 | OK | SPEC.md:1609 (foreground oleh proses `sessions purge`; resume otomatis hanya `serve`/TUI saat open DB + tiap 60 s, atau `doctor --fix`; proses pendek hanya recovery trash + `purge_pending`, `TestPurgeResumeOnlyLongLived`), docs/SELF-HEALING.md:349 | Proses pendek tidak lagi menanggung purge. Dampak bagi pengguna yang hanya memakai ACP dicatat di AA-07. |

Kasus yang saya periksa khusus dan **tidak** menemukan masalah:

- **Race fork di tengah fase 0.** Fork baru dari leluhur yang belum berlabel memicu pengeluaran leluhur itu saat diproses (post-order). Fork dari sesi yang sudah berlabel ditolak `session_purging`, asalkan cek dilakukan di transaksi tulis fork (lihat AA-01). Cucu baru dari turunan yang belum diproses tertangkap secara transitif, karena turunan itu dikeluarkan bersama leluhurnya.
- **Pengeluaran X dengan turunan yang sudah berlabel.** Turunan tetap di P dan dipurge; himpunan berlabel tetap tertutup-turunan; X dan leluhurnya hidup tanpa leluhur berlabel. Konsisten dengan I1 dan dilaporkan ke user (preview menandai sesi ber-lease).
- **Crash di setiap batas fase.**
  - Fase 0 sebelum label pertama → tidak ada label/audit → `aborted`.
  - Fase 3 setelah semua sesi terhapus tetapi sebelum `purge_done` → baris `module_state` masih ada → batch final dijalankan ulang tanpa sesi → `purge_done`.
  - Ledger tertinggal → DB otoritatif.
- **P19.** Fase 0 satu transaksi untuk |P| ≤ 2.000 tetap dibatasi deadline 150 ms. Unit per sesi di fase 1/3 kecil, karena memori/delivery non-final/label per sesi kecil dan langkah 27 "normalnya 0".
- **Konflik lease dengan gc.** gc tidak menyentuh baris yang dihapus purge kecuali `blob_expirations` lewat protokol trash yang sama.

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AA-01 | MAJOR | SPEC.md:1608 ("Purge **tidak** memegang `session_leases` sesi P: label menolak lease baru"), :1623 (langkah 3: "sesi ber-lease hidup sudah dikeluarkan di fase 0 dan label menolak lease baru, jadi tidak ada turn aktif"), :1611 (fase 0 hanya mengeluarkan sesi "ber-lease **hidup**") ↔ SPEC.md:314 + :1349-1356 (`session_leases`: `owner_pid`, `owner_host`, `expires_at`, tanpa token fencing), SPEC.md:550-557 (`Store.Append(ctx, e Event)` tanpa lease/fencing), docs/SELF-HEALING.md:350-364 (langkah 4 recovery journal + auto-continue `safe_auto` untuk **setiap** baris journal terbuka) ↔ SPEC.md:1650 (langkah 30 `DELETE FROM sessions`), :1644 (langkah 24 hanya event `E_P` di fase 2) | ADR-041 melepas lease per sesi dari purge dan bergantung pada "label menolak lease **baru**". Tidak ada dokumen yang mewajibkan jalur tulis memeriksa `purging(s)` atau kepemilikan lease yang masih berlaku di transaksinya. Ada dua pemicu nyata yang menulis ke sesi berlabel. (1) **Pemegang lease basi:** proses yang terhenti > 10 s (Ctrl-Z/SIGSTOP, laptop tidur, lompatan jam dinding karena NTP; `expires_at` berbasis wall clock lintas proses) dianggap tidak ber-lease hidup, sehingga sesinya dilabel. Saat bangun, heartbeat-nya meng-UPDATE baris lease yang masih ada, lalu turn-nya lanjut `Append`. (2) **Recovery journal setelah purge terputus:** fase 0 sudah COMMIT, tetapi baris `turn_journal` sesi P baru dihapus di fase 1 (langkah 3). Proses writer berikutnya (TUI/`run`/ACP) menjalankan langkah 4 SELF-HEALING §3.4 untuk semua journal terbuka: menulis `turn_end{crashed}` + `crash_recovered`, lalu dengan default `safe_auto` membuat turn baru yang **mengirim riwayat sesi yang sedang dipurge ke provider LLM** (biaya + privasi). Event yang tertulis setelah fase 2 selesai tidak termasuk `E_P` yang diproses fase 2, sehingga langkah 30 gagal FK `events.session_id`. Batch fase 3 lalu rollback dan diulang terus (purge tidak pernah selesai, lock diambil tiap siklus). Klaim "tidak ada turn aktif" di langkah 3 tidak didukung oleh mekanisme apa pun. | (a) Setiap transaksi tulis yang terikat ke sesi (`Store.Append`, UPDATE `turn_journal`, renewal/akuisisi `session_leases`, `Fork`/pembuatan sesi anak `delegate`, INSERT `actions`/`file_snapshots`/`gateway_deliveries`) memeriksa di transaksi yang sama `NOT purging(s)` **dan** bahwa baris `session_leases` masih milik pemanggil (tambah `owner_start_ns`/`boot_id` sebagai token fencing) dan belum kedaluwarsa. Gagal → `session_purging`/`session_busy`, turn dibatalkan tanpa LLM call. Renewal atas lease kedaluwarsa = akuisisi baru (cek label). (b) Langkah 4 SELF-HEALING §3.4 melewati sesi berlabel (journal-nya dihapus fase 1). (c) Pertahanan berlapis: fase 3 per sesi menjalankan ulang langkah 10–25 untuk event `S_b` yang tersisa sebelum langkah 27, sehingga langkah 30 tidak bisa macet. Test: `TestPurgeStaleLeaseHolder` (TUI di-SIGSTOP > 10 s, purge, SIGCONT → `session_purging`, 0 event baru, purge selesai) dan `TestPurgeSkipsJournalRecovery` (crash turn + purge terputus setelah fase 0 → proses berikutnya 0 LLM call ke sesi P). |
| AA-02 | MINOR | SPEC.md:1573 (Memori relevan: `memory_fts` + `v_memory_status`, tanpa filter), :1576 (Outbox due: `idx_gw_deliveries_due`, tanpa filter) ↔ SPEC.md:1611 ("Sesi berlabel dikecualikan dari semua kueri baca"), :1628 (memori `M` baru dihapus di fase 1), :1621 (delivery non-final baru dihapus di fase 1) | Antara fase 0 dan fase 1, yang bisa berlangsung tanpa batas bila purge terputus dan tidak ada `serve`/TUI, memori ber-`source_session`/`evidence` sesi P tetap diinjeksikan ke context tier sesi lain, dan sender outbox tetap bisa mengirim delivery `queued`/`failed` milik sesi P. Data sesi yang sudah "tidak terlihat" masih dipakai dan dikirim. | Tambahkan filter `NOT purging(source_session)` (dan sesi event bukti) pada baris Memori relevan / `v_memory_status`, dan `NOT purging(sesi event_id)` pada Outbox due. Masukkan keduanya ke `TestPurgeHiddenFromQueries`. |
| AA-03 | MINOR | SPEC.md:1578 (gateway `gateway_conversations` → sesi + `purging(id)` → `session_purging` untuk operasi tulis) ↔ SPEC.md:1622 (langkah 2: "pesan berikutnya di chat itu membuka sesi baru", baru setelah fase 1), SPEC.md:119 (P22: 0 pesan masuk dibuang diam) | Pesan masuk ke chat yang percakapannya menunjuk sesi yang sedang dipurge mendapat `session_purging` di jendela fase 0 → fase 1. Perilaku gateway (balas error, tahan, atau buang) tidak ditetapkan. | Nyatakan bahwa resolusi percakapan gateway memperlakukan sesi `purging` sebagai tidak ada, sehingga sesi baru dibuka seketika (sama dengan perilaku setelah langkah 2). Tambahkan kasus ke `TestPurgeGatewaySession`. |
| AA-04 | MINOR | SPEC.md:513-519 (exit 4 = "sementara, coba lagi" untuk `session_purging`) ↔ SPEC.md:1611 (sesi berlabel tidak bisa dibatalkan, pasti dihapus), SPEC.md:797 (`run`: exit 0 / else 2), SPEC.md:312 (kontrak exit `serve`), SPEC.md:2416 / docs/SELF-HEALING.md:523 (`doctor` 0/1/2/3) | `session_purging` bersifat permanen, sehingga exit 4 "coba lagi" mengundang retry loop skrip yang tidak akan pernah berhasil. Urutan prioritas antara tabel §4.3 dan kontrak exit per perintah (`run` 2, `doctor` 0–3, `serve` 0/2/70/75/78) juga tidak dinyatakan; contohnya `run -r <id>` yang kena `session_busy` bisa exit 2 atau 4. | `session_purging` → exit 1 (ditolak). Tambahkan satu kalimat prioritas: error sebelum logika perintah memakai tabel §4.3; selebihnya kontrak perintah. |
| AA-05 | MINOR | SPEC.md:1605 (himpunan P: `purge <id>`, `--with-forks`, `--older-than`) ↔ SPEC.md:1611 (fase 0: label baru di-INSERT; label append-only "nilai terakhir menang", SPEC.md:1049-1055) ↔ SPEC.md:1607 ("label `purge_state` (nilai = `txid`) menentukan anggota purge yang tersisa") | Purge kedua yang overlap dengan purge tertunda tidak didefinisikan. Bila P baru dihitung tanpa `ctxdb.Visible`, fase 0 bisa me-relabel sesi milik `txid` lama, dan purge lama lalu mencoba menghapus induk yang anaknya sudah "dipindah" ke `txid` baru → langkah 30 gagal FK berulang sampai purge lain selesai. | Tetapkan: P dihitung lewat `ctxdb.Visible`; `purge <id>` atas sesi berlabel → `session_purging`; di fase 0, anak berlabel milik `txid` lain dihitung "di luar P" (pengeluaran aman). Alternatif: maksimum satu purge aktif (baris `module_state(module_id='purge')` ada → tolak dengan exit 4). |
| AA-06 | MINOR | SPEC.md:1610 ("me-`DROP` trigger no-UPDATE/no-DELETE tabel yang disentuhnya lalu membuatnya ulang sebelum COMMIT") ↔ SPEC.md:2186 (prepared statements wajib untuk hot path), SPEC.md:564 / :2161 (`BenchmarkAppendDuringGC`) | Setiap DROP/CREATE TRIGGER menaikkan schema cookie, sehingga **semua** koneksi di semua proses harus me-re-prepare statement pada pemakaian berikutnya (`SQLITE_SCHEMA`). Dengan batching ADR-041, satu purge besar bisa menghasilkan ribuan perubahan skema. Hal ini tidak tercermin di budget jendela pemeliharaan. | Ganti DROP/CREATE dengan klausa `WHEN NOT dmb_purge_active()` pada trigger (fungsi Go terdaftar per koneksi; true hanya di koneksi purge selama transaksinya, pola yang sama dengan `dmb_fts_norm`). Alternatif: ukur re-prepare di `BenchmarkAppendDuringGC` varian purge. |
| AA-07 | MINOR | SPEC.md:1609 (resume otomatis hanya `serve`/TUI; ACP termasuk "proses berumur pendek") ↔ SPEC.md:308 (ACP = satu proses per client, hidup selama sesi editor) | Pengguna yang hanya memakai ACP (editor) atau `run` tidak pernah me-resume purge yang terputus. Data tetap tersembunyi tetapi tidak terhapus tanpa batas waktu, dan WARN hanya terlihat di `doctor`. | Izinkan ACP me-resume (proses panjang), atau tampilkan `purge_pending` sebagai notice di TUI/ACP/`run` (sekali per proses) dengan saran `damnbot doctor --fix`. |
| AA-08 | MINOR | SPEC.md:1572 (`recall` "di luar purge berjalan") ↔ SPEC.md:2162 (§17.1 `recall` tanpa pengecualian), docs/ROADMAP.md:107; SPEC.md:1605 (kalimat Z-04 rusak) | Pengecualian budget `recall` selama purge hanya ada di §11.6. Kalimat aturan sesi tanpa event juga tidak gramatikal. | Tambahkan pengecualian yang sama di §17.1 dan ROADMAP F2. Perbaiki kalimat menjadi: "…atau `sessions.created_at` bila sesi tidak punya event (dibuat lalu ditinggalkan, wizard batal), sehingga sesi itu tetap bisa dipurge". |
| AA-09 | MINOR | SPEC.md:1607 (`TestPurgeResumeIdempotent`: kill di setiap batas batch dan di tengah setiap fase → hasil akhir **byte-identik** dengan purge tanpa gangguan) ↔ SPEC.md:1611 (pengeluaran fase 0 bergantung pada lease hidup saat batch dievaluasi), :1607 (`excluded` ditulis ke ledger setelah COMMIT, boleh tertinggal) | Crash di fase 0 setelah sebuah batch mengeluarkan X tetapi sebelum `excluded` tercatat membuat X dievaluasi ulang saat resume. Bila lease X sudah kedaluwarsa, X dan leluhurnya ikut dipurge, sehingga hasilnya berbeda dari run tanpa gangguan. Klaim byte-identik hanya berlaku dengan jam/lease beku. | Persempit klaim test menjadi "dengan fake clock dan lease beku". Lebih baik: catat pengeluaran di DB dalam transaksi yang sama (mis. `module_state(module_id='purge', key=<txid>).value.excluded[]`), sehingga resume tidak mengevaluasi ulang sesi yang sudah dikeluarkan. |

### Jumlah dan verdict

ID diverifikasi: OK **7** (Z-01..Z-07) · PARSIAL **0** · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **1** (AA-01) · MINOR **8** (AA-02..AA-09).

MAJOR terbuka untuk kriteria freeze: **1** (AA-01). BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-041 menutup Z-01..Z-07 dengan benar: batching empat fase berbatas jumlah sesi dan event, I1/I2, lease purge dengan fencing dan takeover CAS, resume hanya oleh proses panjang, filter baca sesi, kode error tertutup, dan langkah 28 untuk outbox.

Sisa satu MAJOR: dengan purge tidak lagi memegang `session_leases`, tidak ada mekanisme yang mencegah **penulis yang sudah ada** menulis ke sesi berlabel. Dua penulis seperti itu adalah pemegang lease basi dan recovery journal + auto-continue. Akibatnya purge bisa macet permanen di langkah 30, dan riwayat sesi yang dipurge bisa terkirim ke provider LLM.

Perbaikannya adalah satu aturan fencing di jalur tulis (`NOT purging(s)` + token lease di transaksi yang sama), pengecualian sesi berlabel dari recovery journal, dan pembersihan sisa event di fase 3. Tidak perlu perubahan token, LOC, ukuran, atau dependency; DDL bertambah 1–2 kolom token di `session_leases`. Setelah AA-01 ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; AA-02..AA-09 masuk backlog ADR.

## §Verifikasi ADR-042 (2026-10-18)

Bagian ini memverifikasi ADR-042 (docs/DECISIONS.md:742-824), yang mengklaim menutup AA-01 (MAJOR) dan AA-02..AA-09 (MINOR) serta memuat tabel 25 skenario adversarial. Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen, dan mencakup:

- lokasi yang disebut: SPEC.md:314, :509-524, :1015, :1323-1347, :1355-1366, :1582-1592, :1611-1625, :1631-1661, :1894-2030, :2310, :2366; docs/SELF-HEALING.md:59, :349-352, :382-386; docs/ROADMAP.md:107;
- reproduksi DDL secara read-only di `/tmp/adr042.db`;
- penulis lintas proses: ACP, gateway, cron, delegate, undo, `mcp serve`, import, restore/salvage.

Dokumen lain read-only.

### Cek mekanis dan reproduksi DDL

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal (`node /tmp/mmd/check.mjs SPEC.md docs/*.md`) |
| Link relatif markdown | 170 link, 0 rusak |
| Eksekusi DDL | 8 blok ```` ```sql ```` §11 (0001..0008, SPEC.md:1028-2027) dieksekusi berurutan di SQLite **3.53.4** (Python `sqlite3`), dengan `dmb_fts_norm`/`dmb_maint`/`dmb_lease_token` didaftarkan dan `foreign_keys=ON`: **0 error**. Klaim penulis terbukti. |
| Jumlah trigger | 79 total = **33** `guard_*` + **35** P9 ber-`WHEN dmb_maint() <> 'purge'` + 11 lain (FTS ai/au/ad, `sessions_no_update`, `blobs_no_update`, `gwin_update_guard`, `kanban_events_no_*`). Cocok dengan DECISIONS.md:757 dan :783. |
| Uji fungsional guard (18 kasus) | event tanpa lease → `lease_fenced`; token benar → OK; token basi → `lease_fenced`; `__system__` tanpa lease → OK; label `purge_state` dari koneksi biasa → `internal: … reserved`; mode `purge` → OK; setelah label: event (token benar), heartbeat lease, fork → semuanya `session_purging`; mode `restore`: UPDATE `events` → `events is append-only` (P9 tetap aktif) tetapi INSERT event ke sesi berlabel tanpa lease → **lolos**; mode `purge`: UPDATE `events` lolos, UPDATE `sessions` tetap ditolak; purge mini (journal → FTS → events → labels → leases → sessions) → `foreign_key_check` = 0, `schema_version` tetap |
| Uji fail-open | `dmb_maint()` = NULL → UPDATE `events` **lolos** (P9 terlewati) dan INSERT event ke sesi berlabel tanpa lease **lolos** (guard terlewati). `dmb_maint()` = `'gc'` (nilai tak dikenal) → guard terlewati, P9 aktif. Lihat AB-03. |
| Cakupan guard vs graf FK | 27 tabel menjangkau `sessions` lewat FK. Kolom tanpa pemeriksaan `NEW.<kolom>` di guard: `actions.event_id`, `approvals.event_id`, `plans.event_id`, `summaries.event_id`, `usage_ledger.event_id`, INSERT `gateway_inbound.event_id`, `kanban_task_events.task_id`, dan UPDATE pada `gateway_conversations` (mutable). Lihat AB-04. |
| Sisa mekanisme lama | "DROP/CREATE TRIGGER" hanya muncul sebagai larangan (SPEC.md:1323) dan di riwayat ADR |
| Konsistensi P9 dengan `WHEN` | Pengecualian P9 hanya `'purge'`. `sessions`/`blobs` no-UPDATE dan `kanban_task_events` tanpa pengecualian. Tidak ada tabel yang disentuh purge (langkah 1–31) yang terhalang trigger tanpa `WHEN`: purge tidak meng-UPDATE `sessions`/`blobs`, dan `gateway_inbound` hanya di-DELETE. |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| AA-01 | OK | SPEC.md:314 (token fencing, heartbeat bersyarat token, `CheckLease` sebelum setiap LLM call), :1355-1366 (`fence_token UNIQUE`, `owner_start_ns`, `owner_boot_id`, jam boot), :1900-2027 (33 guard P/F), :1621 (I3), :1624 (fase 3 mengulang 10–25 dan 1–3 + hapus lease sebelum 27–30), docs/SELF-HEALING.md:350 (recovery melewati sesi berlabel, ambil lease dulu) | Kedua pemicu AA-01 tertutup di DB (direproduksi). Tidak ada jalur macet: baris yang masuk lewat kolom yang tidak dijaga (AB-04) terhapus oleh langkah DELETE-per-`event_id` di batch yang sama, atau insert-nya gagal FK setelah event terhapus; fase 3 mengulang 1–3. Cacat liveness pada `boot_id` ada di AB-02 (bukan cacat fencing: token tetap benar). |
| AA-02 | OK | SPEC.md:1582 (memori: filter `source_session` + sesi `evidence_event_id`), :1585 (outbox due + guard UPDATE), :1969-1971 (`guard_gwdel_upd`), :1592 (`TestPurgeHiddenFromQueries` memuat keduanya) | — |
| AA-03 | OK | SPEC.md:1588 (percakapan yang sesinya `purging` = tidak ada; sesi baru + baris `gateway_conversations` baru di transaksi `user_message` yang sama; tepat 1 balasan per chat per `txid`; P22) | Guard F mewajibkan akuisisi lease sesi baru di transaksi yang sama sebelum INSERT event. Ini tersirat, tidak dinyatakan. Kunci dedup `module_state('gateway','purge_notice:…')` tidak pernah dihapus (bagian AB-09). |
| AA-04 | OK | SPEC.md:517 (`session_purging` → 1, permanen), :518-519 (`lease_fenced`/`purge_in_progress` → 4), :524 (prioritas + `TestExitCodePrecedence`) | — |
| AA-05 | OK | SPEC.md:1615 (maksimal satu purge aktif, P lewat `ctxdb.Visible`, `TestPurgeSingleActive`), :1621 (cek ulang di transaksi pertama fase 0), :1617 (reapply berurutan) | Reapply berurutan di bawah barrier membuat barrier tertahan tanpa batas (AB-06). |
| AA-06 | OK | SPEC.md:1015 (`dmb_maint`, tanpa setter SQL, `SQLITE_INNOCUOUS`, tidak `DIRECTONLY`), :1323-1347, :1460-1466, :1784-1793, :1886-1889 (35 `WHEN`), :1620 (`TestPurgeNoSchemaChange`) | Direproduksi: `schema_version` tetap, mode `restore` tidak melewati P9. Ekspresi `WHEN` fail-open terhadap NULL/nilai asing (AB-03). |
| AA-07 | OK | SPEC.md:1619 (ACP ≥ 5 menit me-resume; notice sekali per proses di TUI/ACP/`run`), docs/SELF-HEALING.md:349 | — |
| AA-08 | OK | SPEC.md:2310 (§17.1 `recall` dengan pengecualian purge), docs/ROADMAP.md:107, SPEC.md:1615 (kalimat Z-04 sudah gramatikal) | — |
| AA-09 | OK | SPEC.md:1617 (`value.excluded[]` + `value.phase` di DB pada transaksi yang sama, tidak dievaluasi ulang; asumsi *fake clock + lease beku* eksplisit), :1621 (I2 bergantung pada `value.phase`) | — |

Kasus yang saya periksa khusus dan **tidak** menemukan masalah:

- **Lease kedaluwarsa tetapi belum diambil alih.** Guard F hanya membandingkan token, tidak memeriksa `expires_at`. Penulis yang terlambat tetap diterima selama belum ada proses lain yang mengambil alih. Ini aman: pengambil alih selalu mengganti token, dan purge menolak lewat guard P.
- **Delegate.** Anak dibuat lewat `guard_sessions_ins` (P pada `parent_id`). Event anak memerlukan lease anak oleh proses induk. Anak ber-lease hidup mengeluarkan leluhurnya di fase 0 (SPEC.md:1621).
- **Cron dan kanban.** Hanya guard P (SPEC.md:2016-2027). Langkah 4 meng-NULL-kan `session_id`.
- **Undo lintas sesi.** SPEC.md:2366: guard P pada sesi aksi, F pada sesi pemanggil, dan daftar file dilaporkan bila pencatatan ditolak.
- **Gateway outbox.** UPDATE `queued → sending` ditolak untuk sesi berlabel. Delivery `sending` yang tertahan dihapus di langkah 1/28.
- **ACP memegang sesi saat purge.** Sesi dikeluarkan lewat `session_busy` (skenario #13).
- **Purge ganda.** `purge_in_progress` dicek ulang di bawah lock.
- **Granularitas token per proses.** Token dipegang per proses, bukan per turn, sehingga goroutine basi di proses yang sama lolos guard F. [INFERENCE] Hal ini ditangani oleh serialisasi turn per sesi di dalam proses dan berada di luar tujuan fencing lintas proses.
- **Alat SQLite eksternal.** Alat eksternal bisa me-`.load` fungsi `dmb_maint` palsu. Ini di luar model ancaman, karena akses tulis ke file DB sudah setara dengan bisa me-DROP trigger.

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AB-01 | MAJOR | SPEC.md:1613 (barrier: proses yang melihat marker "tutup semua koneksi … **tunggu marker hilang**"; "Lock eksklusif tidak didapat ≤ 30 s → restore ditolak `db_busy`"; marker hanya dihapus di langkah (6) jalur sukses), docs/SELF-HEALING.md:382 (auto-restore: gagal → SAFE MODE), DECISIONS.md:773, :815-816 ↔ SPEC.md:119 (P22: recovery ke layanan ≤ 10 s) | `run/maint.request` adalah kunci tanpa pemilik dan tanpa liveness. (1) **Restorer mati** (kill -9, OOM, atau power loss, yang justru umum saat auto-restore karena korupsi) setelah menulis marker: marker tertinggal, dan setiap proses, termasuk `serve` setelah reboot, menunggu tanpa batas sebelum `BEGIN IMMEDIATE` berikutnya. Inbound gateway tidak tercatat, watchdog store writer (10 s) memicu restart, dan restart berulang berakhir di SAFE MODE. Tidak ada jalur self-heal. (2) **Restore ditolak setelah 30 s**, misalnya karena satu TUI di-Ctrl-Z masih memegang lock bersama: spesifikasi tidak mewajibkan restorer menghapus marker, sehingga semua proses yang sudah patuh menutup koneksi menunggu selamanya. Skenario #16/#17 hanya menguji jalur sukses dan jalur timeout dari sisi restorer. | Marker berisi `{pid, start_ns, boot_id, ts}`. Restorer memegang `flock`/`LockFileEx` **eksklusif** pada `run/maint.owner` sejak sebelum menulis marker sampai marker dihapus; lock ini lepas otomatis saat proses mati. Penunggu mencoba lock bersama non-blocking pada `maint.owner` setiap 1 s: bila berhasil, pemilik sudah mati, jadi marker basi dihapus dan penunggu lanjut. Setiap jalur gagal restore (timeout, error, Ctrl-C) menghapus marker sebelum return. Waktu tunggu penunggu dibatasi (≤ 30 s + durasi restore yang diumumkan di marker), lalu `db_busy`. Notifikasi SAFE MODE menyebut PID pemegang lock bersama (daftar `run/maint.holders/<pid>`). Test: `TestMaintMarkerStaleAfterKill` (kill -9 restorer setelah marker → proses lain lanjut ≤ 10 s) dan `TestRestoreTimeoutClearsMarker`. |
| AB-02 | MAJOR | SPEC.md:314 ("`boot_id` beda → lease mati"; "Lompatan jam dinding (NTP, user) tidak memengaruhi lease"; "host lain, mis. kontainer lain di volume yang sama → hanya kedaluwarsa"), :1361, :1621 (liveness fase 0), docs/SELF-HEALING.md:59 (`boot_id`: macOS `sysctl kern.boottime`; Windows "waktu boot dari `GetTickCount64`"), :349-350 ↔ DECISIONS.md:802 (skenario #3: "jam dinding tidak dipakai untuk lease") | ADR-042 menjadikan `boot_id` diskriminator liveness ("beda → mati"). Definisinya di Windows/macOS justru turunan jam dinding. Windows: waktu boot = jam dinding − `GetTickCount64`, dengan resolusi tick 10–16 ms ([Microsoft: Windows Time](https://learn.microsoft.com/en-us/windows/win32/sysinfo/windows-time)). Nilainya bergeser setiap kali jam dinding di-step atau di-slew NTP, dan dua proses yang menghitungnya pada waktu berbeda mendapat nilai berbeda, terlepas dari pembulatan, di batas pembulatan atau setelah drift. macOS: `kern.boottime` ikut disesuaikan saat jam kalender di-set [INFERENCE: perilaku XNU]. Akibatnya, proses baru menilai lease **hidup** milik TUI/ACP/serve sebagai "mati". Recovery journal di proses baru lalu mengambil alih lease, sehingga turn yang berjalan dibatalkan `lease_fenced`, process group tool (mis. build panjang) dibunuh, dan turn di-auto-continue di proses lain. Fase 0 purge memperlakukan sesi yang sedang dipakai sebagai tidak ber-lease. Takeover lease purge oleh "`boot_id` beda" (SELF-HEALING.md:349) juga terdampak. Klaim skenario #3 tidak berlaku di 2 dari 3 OS target. Teks "host lain → hanya kedaluwarsa" juga bertentangan dengan "`boot_id` beda → mati", karena mesin lain selalu punya `boot_id` berbeda. Kontainer dengan time namespace (`CLONE_NEWTIME`) menggeser `CLOCK_BOOTTIME` dengan `boot_id` yang sama, sehingga perbandingan kedaluwarsa antar-kontainer salah. | `boot_id` = identitas opaque dari kernel, bukan waktu: Linux `/proc/sys/kernel/random/boot_id`; macOS `sysctl kern.bootsessionuuid`; Windows nilai `BootId` (registry `…\Memory Management\PrefetchParameters`) [INFERENCE: ketersediaan]. Bila identitas kernel tidak tersedia, "`boot_id` beda" **tidak** boleh berarti mati; pakai aturan bebas jam: `expires_at − now_boot > TTL` ⇒ lease dari boot lain ⇒ mati. Nyatakan bahwa DB hanya didukung untuk proses di satu kernel (WAL membutuhkan shared memory satu host), lalu hapus frasa "host lain". Kontainer dengan time namespace tidak didukung, dan dideteksi `doctor` bila `CLOCK_BOOTTIME` antar-proses tidak konsisten dengan `heartbeat_at`. Test: `TestLeaseLivenessClockStep` (Windows/macOS: step jam ±1 jam + drift 24 jam simulasi → 0 takeover lease hidup). |
| AB-03 | MINOR | SPEC.md:1905-1907 dan semua guard (`WHEN dmb_maint() = ''`), SPEC.md:1324-1347 dan trigger P9 lain (`WHEN dmb_maint() <> 'purge'`), SPEC.md:1015 (mode di-set per koneksi, "dikembalikan ke `''` sebelum COMMIT/ROLLBACK") | Kedua ekspresi **fail-open**. NULL (fungsi mengembalikan `nil` karena bug, atau state koneksi tidak ada) mematikan guard **dan** P9 sekaligus, karena `NULL = ''` dan `NULL <> 'purge'` sama-sama bernilai NULL. Nilai asing (typo, mode baru) mematikan semua guard. Keduanya direproduksi di `/tmp`. Reset mode bergantung pada disiplin kode: panic atau COMMIT gagal di antara set dan reset meninggalkan koneksi writer pool dalam mode `purge`, dan transaksi biasa berikutnya melewati P9 dan guard tanpa jejak. | P9: `WHEN dmb_maint() IS NOT 'purge'`. Guard: `WHEN dmb_maint() IS NOT 'purge' AND dmb_maint() IS NOT 'restore'` (diuji: NULL/`''`/`'gc'` → guard aktif). Fungsi Go mengembalikan `''`, tidak pernah NULL. Mode diikat ke transaksi: di-set setelah `BEGIN` dan direset paksa oleh `sqlite3_commit_hook`/`rollback_hook`, sehingga mode tidak mungkin bocor ke transaksi berikutnya. Test: `TestMaintFailClosed` dan `TestMaintResetOnPanic`. |
| AB-04 | MINOR | SPEC.md:1896 ("Cakupan = setiap kolom FK langsung/tidak langsung ke `sessions` … `TestSessionGuardsCoverFK` … gagal bila ada kolom tanpa guard yang sesuai") ↔ SPEC.md:1708, :1167, :1764, :1115, :1126 (`event_id` di `actions`/`approvals`/`plans`/`summaries`/`usage_ledger`; guard hanya memeriksa `NEW.session_id`), :1454 + :1972 (`gateway_inbound.event_id`: hanya guard UPDATE), :1438 (`gateway_conversations` mutable; hanya guard INSERT), :1563 (`kanban_task_events.task_id → kanban_tasks.session_id`), :2004-2015 (`skills`/`memory_embeddings` hanya memeriksa `source_session`, sedangkan `memory_decisions`/`memory_pins` :1996-2003 juga memeriksa sesi `evidence_event_id`) | `TestSessionGuardsCoverFK` sesuai definisinya sendiri akan **gagal** terhadap DDL yang ditetapkan. Ada 8 jalur kolom tanpa guard (hasil ekstraksi `pragma foreign_key_list`). I3 secara formal bisa dilanggar, mis. baris di sesi B yang `event_id`-nya event sesi A yang berlabel. Tidak ada CHECK yang menegakkan kesamaan sesi. Tidak ditemukan jalur macet (lihat AA-01). | (a) Tambahkan pada guard 5 tabel itu: `SELECT RAISE(ABORT,'internal: cross-session event_id') WHERE (SELECT session_id FROM events WHERE id = NEW.event_id) IS NOT NEW.session_id`. Ini menegakkan kesamaan sesi, sehingga guard P pada `session_id` mencakup `event_id`. (b) Tambahkan `guard_gwin_ins` dan `gwconv_no_update` (PK sudah memuat `session_id`; ganti sesi = baris baru). (c) Samakan `skills`/`memory_embeddings` dengan `memory_decisions`. (d) Nyatakan di SPEC bahwa `kanban_task_events` dikecualikan dari test karena purge meng-NULL-kan `kanban_tasks.session_id`. |
| AB-05 | MINOR | SPEC.md:1896 (daftar penulis tanpa lease: label, kurator, kanban, cron, gateway, akuisisi lease) ↔ SPEC.md:2481 (`mcp serve` `permissions_respond` "terhubung ke `turn_journal awaiting_approval` nyata"; `messages_send`), :1171 (`approvals.source` `'mcp_serve'`/`'web'`/`'gateway'`), :2365 (`damnbot undo` CLI "aksi terakhir sesi"), :991 (`import hermes`/`archive`), :993 (`sessions fork`/`rewind` CLI), :1355-1366 (`owner_kind` tanpa `mcp_serve`) | Guard F mewajibkan lease pemegang turn untuk INSERT `approvals`/UPDATE `turn_journal`. Akibatnya, jembatan approval `mcp serve` (proses terpisah) tidak bisa diimplementasikan lewat DB, dan mekanisme alternatifnya (IPC) tidak ditetapkan. `damnbot undo` dari shell tidak punya "sesi pemanggil". Bila sesi sedang dipegang TUI, hasilnya `session_busy` atau pencatatan di sesi yang tidak jelas. Import massal harus mengambil lease per sesi impor (`fence_seq` naik per sesi), tetapi hal ini tidak dinyatakan. Tabel 25 skenario tidak memuat penulis-penulis ini. | Tambahkan inventaris penulis di §11.10, satu baris per jalur: lease apa yang dipegang, atau `__system__`. Jawaban approval dari proses lain ditulis ke kotak masuk tanpa guard F, mis. `approval_responses(turn_id, decision, source, created_at)` dengan guard P saja. Pemegang lease mengonsumsinya, lalu menulis `approvals` (≤ 1 s, sesuai `TestMCPServeScope`). Undo CLI tanpa sesi dicatat di `__system__` dengan rujukan aksi. Import mengambil dan melepas lease per sesi per batch. Test: `TestMCPServeApprovalRespond`, `TestImportUnderGuards`. |
| AB-06 | MINOR | SPEC.md:1613 (setelah barrier proses "buka ulang DB dan ambil ulang lease (token baru)"; (3) reapply purge; (5) journal ditutup `crashed` dengan `dmb_maint() = 'restore'`), :1617 (reapply "masing-masing sebagai purge baru … berurutan"), :1621 (I3: "tidak ada transaksi **non-purge** yang berhasil …"), docs/SELF-HEALING.md:385-386 (salvage + pasca-restore dalam mode `restore`) | (1) Turn yang sedang berjalan di proses lain **dilanjutkan** dengan state memori yang tidak cocok dengan DB hasil restore. Event setelah snapshot hilang, `prev_hash`/`seq` di memori tidak cocok [INFERENCE: bila di-cache], dan journal-nya sudah ditutup `crashed` oleh langkah (5). Hasilnya event turn muncul setelah `turn_end{crashed}`. (2) Langkah (5) dan salvage berjalan dengan guard nonaktif dan bisa menulis event ke sesi yang baru dilabel oleh reapply. Ini melanggar I3 sebagaimana ditulis, meski fase 3 membersihkannya. (3) Reapply semua purge **penuh dan berurutan** sebelum marker dihapus menahan barrier tanpa batas. Satu purge 100.000 event dengan duty cycle ≤ 20% berlangsung puluhan detik [INFERENCE], melampaui P22 ≤ 10 s untuk auto-restore. (4) Urutan proses baru tidak ditetapkan (ambil lock bersama sebelum membuka DB, lalu cek marker setelah lock didapat), sehingga arus proses pendek bisa membuat restore lapar lock. | (1) Setelah barrier, setiap proses membatalkan turn berjalan (tanpa menulis), memuat ulang sesi dari DB, dan menampilkan notice "DB dipulihkan dari backup <ts>". (2) Langkah (5) melewati sesi berlabel seperti recovery journal, dan I3 ditulis "non-pemeliharaan" dengan daftar penulis mode `restore`. (3) Di bawah barrier hanya fase 0 yang dijalankan, sebagai **satu** purge dengan P = ∪(`intent.P − excluded`) (gabungan himpunan tertutup-turunan tetap tertutup, I1). Fase 1–3 dilanjutkan oleh resume biasa. (4) Tetapkan urutan lock → cek marker → open, dengan preferensi untuk restorer. |
| AB-07 | MINOR | SPEC.md:1611 (gc idle = 0 baris `turn_journal` `state ∈ {running, tool_pending, tool_done}` di **semua** proses) ↔ docs/SELF-HEALING.md:352 (recovery journal hanya saat start / pengambilalihan), :350 (sesi berlabel dilewati, journal-nya baru dihapus purge fase 1/3), DECISIONS.md:775, :821 | Baris journal yatim tetap `running` sampai ada proses writer baru yang start. Contohnya TUI yang di-kill -9 saat `serve` sudah berjalan, atau journal sesi yang purge-nya tertunda di fase 0. Selama itu gc terjadwal `serve` tidak pernah idle. Ini kelas masalah yang sama dengan yang diperbaiki ADR-042 ("TUI menahan gc selamanya"), dengan pemicu lain. | Hitung hanya journal yang sesinya ber-lease hidup (join PK `session_leases` + aturan liveness), atau `serve` menjalankan recovery journal untuk lease kedaluwarsa setiap 60 s. Test: `TestGCIdleIgnoresOrphanJournal`. |
| AB-08 | MINOR | DECISIONS.md:794 dan SPEC.md:314 (residu "≤ 1 LLM call basi per turn"), DECISIONS.md:776 (process group dibunuh saat lease hilang) ↔ docs/SELF-HEALING.md:357-359 (pengambil alih menjalankan recovery `tool_pending` dan boleh re-run) | Residu yang sama berlaku untuk **eksekusi tool**. Contohnya proses yang terhenti antara COMMIT `tool_pending` (lolos guard F) dan mulai `exec`, atau tool file in-process yang menulis sebelum goroutine heartbeat (2 s) menyadari lease hilang. Bila pengambil alih sudah me-recover dan me-re-run tool, efek samping terjadi dua kali di workspace yang sama. Residu ini tidak dinyatakan. | `CheckLease` dijalankan juga tepat sebelum setiap start tool. Tool file in-process memeriksa ulang hash `before_sha` di bawah lock path sebelum menulis. Nyatakan residu sebagai "≤ 1 LLM call **atau** ≤ 1 start tool basi per turn". Tambahkan kasus `TestStaleWriterFenced` (SIGSTOP setelah `tool_pending`, takeover + recovery, lalu SIGCONT → tool tidak dieksekusi ulang oleh proses basi untuk tool file; `exec` dilaporkan). |
| AB-09 | MINOR | SPEC.md:1896 (fitur `auto` tanpa lease mencatat event di `__system__` dengan `payload.session_ref = X`), SPEC.md:948 (setiap call `auto` = event `llm_call`), DECISIONS.md:778 ↔ SPEC.md:1621 (audit purge hanya `count`; id sesi hanya di ledger), :1631-1661 (tidak ada langkah untuk `__system__`), :1588 (kunci `module_state('gateway','purge_notice:<chat>:<txid>')`) | Event `__system__` ber-`session_ref` lahir dari ADR-042 dan **tidak tersentuh purge**. Id sesi ter-purge tetap ada di DB dan ikut ter-backup, bertentangan dengan prinsip "id sesi hanya di ledger". Event itu juga tidak bisa dihapus tanpa merusak hash-chain `__system__`. Kunci dedup `purge_notice` menumpuk tanpa pembersihan. | Jangan menyimpan `session_ref` di payload event. Simpan rujukan di tabel samping ber-FK, mis. `auto_session_refs(event_id, session_id REFERENCES sessions)`, yang dihapus di fase 1 dan otomatis tercakup `TestSessionGuardsCoverFK`, atau cukup simpan di `usage_ledger` yang sudah ditangani langkah 19. Batch final purge menghapus `purge_notice:*:<txid>`. |

### Skenario adversarial yang terlewat dari tabel 25 skenario (DECISIONS.md:797-824)

| # | Skenario | Status | Temuan |
|---:|---|---|---|
| 26 | Restorer mati setelah menulis `maint.request` / restore ditolak setelah 30 s | tidak aman: semua writer menunggu selamanya | AB-01 |
| 27 | Step jam dinding NTP di Windows/macOS saat TUI menjalankan turn, lalu proses kedua start | tidak aman: lease hidup dinilai mati, turn dan tool dibunuh | AB-02 |
| 28 | `dmb_maint()` mengembalikan NULL / panic di tengah transaksi purge | tidak aman: P9 dan guard terlewati | AB-03 |
| 29 | Baris sesi B dengan `event_id` sesi A yang berlabel | lolos guard, tidak macet | AB-04 |
| 30 | `mcp serve permissions_respond` / `damnbot undo` dari shell / import massal | tidak terspesifikasi di bawah guard F | AB-05 |
| 31 | Restore saat TUI sedang di tengah turn | turn lanjut di atas DB yang berbeda | AB-06 |
| 32 | TUI di-kill -9 saat `serve` berjalan, tanpa proses baru | gc tertahan | AB-07 |
| 33 | SIGSTOP setelah COMMIT `tool_pending` | eksekusi tool ganda | AB-08 |
| 34 | Fitur `auto` mencatat `session_ref`, lalu sesi di-purge | id sesi tersisa di DB/backup | AB-09 |

### Jumlah dan verdict

ID diverifikasi: OK **9** (AA-01..AA-09) · PARSIAL **0** · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **2** (AB-01, AB-02) · MINOR **7** (AB-03..AB-09).

MAJOR terbuka untuk kriteria freeze: **2**. BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-042 menutup AA-01..AA-09 dengan benar. DDL-nya dapat dieksekusi apa adanya (0 error di SQLite 3.53.4). Guard P/F bekerja sesuai klaim di 18 kasus fungsional. P9 berbasis `WHEN` konsisten: hanya `purge` yang melewati P9, `restore` tidak. Tidak ditemukan jalur yang membuat purge macet.

Dua MAJOR berasal dari mekanisme **baru** ADR-042:

- **AB-01:** marker barrier tanpa pemilik bisa membekukan semua writer secara permanen setelah restorer crash atau restore ditolak.
- **AB-02:** liveness lease bergantung pada `boot_id` yang di Windows/macOS diturunkan dari jam dinding. Hal ini membatalkan klaim "lompatan jam tidak memengaruhi lease" dan memicu pengambilalihan lease yang masih hidup.

Keduanya bisa diperbaiki secara lokal tanpa perubahan token, LOC, dependency, atau DDL tabel: satu lock pemilik + jalur gagal yang menghapus marker, serta definisi `boot_id` opaque + aturan `expires_at − now > TTL`. Setelah ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; AB-03..AB-09 masuk backlog ADR.

### Perintah reproduksi

```bash
# read-only: ekstrak 8 blok ```sql dari SPEC.md:1011-2033, eksekusi di /tmp
PYTHONDONTWRITEBYTECODE=1 python3 - <<'EOF'
import sqlite3, re
L = open("SPEC.md").read().split("\n"); blocks, cur = [], None
for i, l in enumerate(L, 1):
    if not 1011 <= i <= 2033: continue
    if l.strip() == "```sql": cur = []; continue
    if cur is not None and l.strip() == "```": blocks.append("\n".join(cur)); cur = None; continue
    if cur is not None: cur.append(l)
st = {"maint": "", "tok": {}}
c = sqlite3.connect("/tmp/adr042.db", isolation_level=None)
c.create_function("dmb_fts_norm", 1, lambda t: t, deterministic=True)
c.create_function("dmb_maint", 0, lambda: st["maint"])          # non-deterministik
c.create_function("dmb_lease_token", 1, lambda s: st["tok"].get(s))
c.execute("PRAGMA foreign_keys=ON")
for b in blocks: c.executescript(b)                              # 0 error
t = c.execute("select name, sql from sqlite_master where type='trigger'").fetchall()
print(len(blocks), len(t), sum(n.startswith("guard_") for n, _ in t),
      sum("dmb_maint() <> 'purge'" in s for _, s in t))          # 8 79 33 35
EOF
```

## §Verifikasi ADR-043 (2026-10-18)

Bagian ini memverifikasi ADR-043 (docs/DECISIONS.md:826-907), yang mengklaim menutup AB-01, AB-02 (MAJOR) dan AB-03..AB-09 (MINOR). Saya juga me-review SPEC §11.11 (SPEC.md:2074-2277) sebagai satu kesatuan, tidak hanya delta-nya, mencakup:

- asumsi dan `alive()`;
- tabel mode;
- state machine lease/sesi/purge/barrier/journal;
- tabel penulis;
- urutan lock;
- I1–I14;
- 42 skenario.

Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen. DDL dan uji fungsional direproduksi read-only di `/tmp/adr043.db`. Dokumen lain read-only. Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka. Residu yang diakui eksplisit, bermitigasi, dan tidak melanggar P1–P29 bukan MAJOR.

### Cek mekanis dan reproduksi

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal |
| Link relatif markdown | 170 link, 0 rusak |
| Eksekusi DDL | 8 blok `sql` §11 (SPEC.md:1028-2070, 0001..0008) di SQLite **3.53.4**, `dmb_*` terdaftar, `foreign_keys=ON`: **0 error**. Klaim DECISIONS.md:894 terbukti. |
| Jumlah trigger | **86** = **36** `guard_*` + **37** P9 `WHEN dmb_maint() IS NOT 'purge'` + **13** lain (6 FTS, `gwin_update_guard`, `sessions_no_update`, `blobs_no_update`, `kanban_events_no_update/no_delete`, `approvalresp_no_update`, `gwconv_no_update`). Tidak ada sisa `= ''` atau `<> 'purge'`. Cocok dengan klaim. |
| Graf FK | **49** kolom FK. 29 tabel menjangkau `sessions` (39 kolom). Satu-satunya kolom tanpa `NEW.<kolom>` di guard INSERT adalah `kanban_task_events.task_id`, yang dinyatakan sebagai pengecualian tertutup (SPEC.md:1895). Setiap tabel mutable punya guard UPDATE atau no-UPDATE. Klaim "tercakup transitif" terbukti. |
| Uji fungsional | **51/51** kasus sesuai ekspektasi saya; daftar 41 kasus penulis tidak tersedia, jadi saya menyusun kasus setara sendiri. Hasilnya: fencing (tanpa token / token basi → `lease_fenced`, `__system__` lolos); label `purge_state` ditolak di mode `''` **dan** `'restore'`; cek sesi-sama (`approvals`/`usage_ledger` lintas sesi → `internal: cross-session event_id`); kotak masuk (INSERT tanpa lease lolos, UPDATE ditolak, **duplikat untuk turn yang sama diterima**); `auto_session_refs` (non-`__system__` ditolak, DELETE ditolak P9); `gwconv_no_update`; 10 jalur tulis ke sesi berlabel → `session_purging`; mode NULL/`'gc'`/`'PURGE'`/`''` → P9, P, F semuanya aktif; `'restore'` → P9 dan P aktif, F dilewati; `'purge'` → P9 dilewati, `sessions` tetap immutable; **DELETE `sessions`/`blobs` tanpa rujukan di mode `''` lolos**; purge mini → `foreign_key_check` = 0, `schema_version` tetap. |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| AB-01 | OK | SPEC.md:2150-2166 (`maint.owner` lepas otomatis, marker informatif, setiap jalur gagal menghapus marker lewat `defer`, deteksi basi lewat lock bersama non-blocking ≤ 1 s, batas tunggu 30 s + `expected_s` lalu `db_busy` yang terus dicoba), :2219 (I7) | Penghapusan marker basi dilakukan sambil memegang lock bersama `maint.owner`, sehingga restorer baru tidak bisa menyelinap di antara cek dan hapus (tidak ada TOCTOU). |
| AB-02 | OK | SPEC.md:2081 (`boot_id` opaque per OS, `''` bila tidak tersedia), :2089 (aturan "heartbeat dari masa depan ⇒ boot lain"), :2080 (model satu kernel, time namespace tidak didukung), :2083 (`owner_host` + pid-namespace), :2237 (skenario #3 dikoreksi) | Cacat yang dilaporkan (`boot_id` turunan jam dinding, frasa "host lain") tertutup. Kriteria 3 `alive()` masih bergantung jam dinding secara tidak langsung di macOS/Windows: lihat AC-02. |
| AB-03 | OK | SPEC.md:2097-2106, DDL guard/P9 (`IS NOT 'purge'`, F + `IS NOT 'restore'`) | Direproduksi: NULL/`'gc'`/`'PURGE'` → semua aktif. Pengikatan mode ke transaksi lewat hook driver bertanda [INFERENCE] di SPEC, dengan cadangan `defer`. |
| AB-04 | OK | SPEC.md:1895 (definisi transitif + pengecualian tertutup), guard cek sesi-sama 5 tabel, `guard_gwin_ins`, `gwconv_no_update`, guard `skills`/`memory_embeddings` memeriksa sesi bukti | Direproduksi (graf FK di atas). |
| AB-05 | OK | SPEC.md:2186-2203 (tabel penulis × mode), :2195 (kotak masuk approval), :2196 (undo CLI lease pendek), :2197 (import per batch), :2727 | Inventaris ada. Desain kotak masuk cacat (AC-01) dan `messages_send` belum terinventaris (AC-08). |
| AB-06 | PARSIAL | SPEC.md:1613 ((0) mode `'restore'` P9 + P aktif; (3) fase 0 saja, satu purge gabungan, batas `expected_s`; (5) melewati sesi berlabel), :2156 dan :2164 (turn dibatalkan tanpa tulis, urutan buka DB) ↔ SPEC.md:1617 (+1.400: "Restore menerapkan ulang entri … **berurutan** …, masing-masing sebagai purge baru") | Bagian (1), (2), dan (4) beres. Langkah (3) tidak dapat dijalankan di mode `'restore'` seperti yang tertulis: DDL menolak label `purge_state` (direproduksi). Teks lama di :1617 masih bertentangan. Lihat AC-03. |
| AB-07 | OK | SPEC.md:2170-2178 (journal yatim tidak dihitung, pembersihan saat open DB + `serve` tiap 60 s, auto-continue hanya oleh frontend asal), docs/SELF-HEALING.md:350 | Ada race setelah laptop bangun (AC-04). |
| AB-08 | OK | SPEC.md:2120 (titik cek sebelum LLM call, dispatch tool, dan commit hasil; hash ulang di bawah path lock; residu ≤ 1 LLM call atau ≤ 1 start tool; `exec` → `outcome_unknown`), :314 | — |
| AB-09 | OK | SPEC.md:1919-1932 (`auto_session_refs` append-only, guard P, wajib event `__system__`), :1631 (langkah 2), :1660 (langkah 31 menghapus `purge_notice:*:<txid>` + housekeeping 7 hari) | Rujukan fitur `auto` sudah tertutup. Klaim I13 yang diperluas ("id tidak tersisa di DB") melebihi mekanismenya (AC-06). |

### Review §11.11 sebagai keseluruhan

Bagian yang konsisten dan tidak bermasalah:

- **Struktur §11.11.**
  - Satu fungsi liveness dipakai bersama oleh lease sesi, lease purge, dan barrier.
  - Mode koneksi cocok persis dengan DDL (direproduksi).
  - State machine lease cocok dengan guard: `dead(t) → held(t)` hanya bila token masih `t` dan sesi tidak berlabel, karena heartbeat bersyarat token + `guard_leases_upd`.
  - Urutan purge (phase0..3, `aborted`, ambil alih CAS, pelepasan saat barrier) cocok dengan §11.6.
  - Tabel penulis cocok dengan guard yang aktif.
- **Jalur sesi lain.**
  - Fork, delegate, dan gateway mengakuisisi lease di transaksi yang sama.
  - Pengirim outbox, cron, kanban, dan label hanya terkena guard P.
  - Fitur `auto` menulis ke `__system__` + `auto_session_refs`.
- **Invarian.**
  - I1, I2, I3, I4, I6, I7, I8, I9, I11, dan I14 didukung mekanisme yang bisa ditunjuk di DDL atau aturan.
  - I10 berlaku dengan syarat ada kemajuan (lihat residu #37/#38).
- **Fail-closed.** Tidak ada nilai mode selain `'purge'` yang melewati P9, dan tidak ada nilai selain `'purge'`/`'restore'` yang melewati guard. Rujukan NULL (mis. `gateway_deliveries.event_id` NULL) tetap lolos. Ini pilihan yang dinyatakan (SPEC.md:1906), dan writer wajib mengisi `event_id` untuk sesi non-`__system__` (`TestDeliveryHasEvent`).

**Residu yang diakui** (diminta khusus):

| # | Residu | Jujur & bermitigasi? | Melanggar P1–P29? | Penilaian |
|---:|---|---|---|---|
| 9 | Undo dipulihkan, tetapi pencatatan ditolak karena sesi aksi mulai dipurge | Ya: guard dicek sebelum file disentuh; jendela sisa dilaporkan beserta daftar file (`TestUndoFencedReportsFiles`) | Tidak. P26 menyangkut dapat di-undo-nya aksi asli, yang memang sudah di-undo; yang hilang hanya jejak undo itu sendiri | bukan MAJOR |
| 33 | ≤ 1 start tool basi setelah COMMIT `tool_pending` | Ya: titik cek sebelum dispatch; hash ulang untuk tool file; process group dibunuh ≤ 2 s setelah lease hilang; pengambil alih melaporkan `outcome_unknown` | Tidak. Eksekusi basi adalah eksekusi **pertama** yang terlambat. Pengulangan oleh pengambil alih tetap butuh konfirmasi (P22) | bukan MAJOR |
| 37 | SIGSTOP di dalam transaksi tulis menahan lock SQLite | Ya, dinyatakan tidak terselesaikan. Proses lain mendapat `db_busy` typed (P19), dan offset Telegram tidak di-commit sehingga tidak ada pesan hilang (P22) | Tidak | bukan MAJOR. Pemicu paling umum (Ctrl-Z/SIGTSTP) sebenarnya bisa dicegah, dan `db_write_stalled` belum terdefinisi (AC-09) |
| 40 | Time namespace berbeda pada kernel yang sama | Ya: tidak didukung; `doctor` WARN; fencing menjaga keselamatan | Tidak | bukan MAJOR. Deteksinya satu arah: hanya sisi yang jamnya **di belakang** yang melihat heartbeat "dari masa depan", sehingga `doctor` di sisi yang jamnya di depan tidak memberi WARN |

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AC-01 | MAJOR | SPEC.md:1908-1917 (`approval_responses`: `session_id`, `turn_id`, `decision`, `source`, `responder`; "dicocokkan ke `turn_journal` `awaiting_approval`; baris tanpa journal terbuka dihapus konsumen"), :2195, :2275 (#41), :2727 (SEC-21: cakupan token untuk `permissions_respond` tidak ditetapkan; default read-only), :1684 (`approval_request` tanpa id permintaan) ↔ SPEC.md:123 (P26: aksi `irreversible`/`monetary`/`delegated_agent_bypass` 100% melewati approval + preview) | Jawaban di kotak masuk hanya terikat pada **turn**, tidak pada **permintaan** approval. Satu turn bisa memuat banyak approval berurutan. Jawaban terlambat atau ganda untuk permintaan #1 bisa datang saat journal sudah kembali `running`, misalnya karena permintaan sudah dijawab lewat kanal lain, timeout, atau klien MCP melakukan retry. Baris itu tidak dihapus, karena journal masih "terbuka", lalu dikonsumsi sebagai jawaban permintaan #2 yang bisa berupa `rm -rf` atau `git push --force` yang preview-nya tidak pernah dilihat penjawab. Duplikat untuk turn yang sama diterima DB (direproduksi). Otorisasi penjawab (scope token, identitas pemilik sesi, kanal yang menampilkan prompt) juga tidak ditetapkan di konsumen maupun DB, padahal kotak masuk membuka jalur approval lintas kanal yang baru ("web/gateway untuk turn di proses lain"). Keputusan `edit` tidak punya kolom argumen hasil edit. | Tambahkan `request_id TEXT NOT NULL` ke `approval_responses` dan `approval_request` (mis. `sha256(turn_id, tool_call_id, subject, preview_blob_sha)` atau penghitung `approval_seq`). Konsumen hanya menerima baris dengan `request_id` = permintaan yang sedang menunggu, `source` ∈ kanal permintaan, dan penjawab yang berwenang (token scope baru `approve`, identitas = pemilik sesi). Setelah resolusi, semua baris untuk `request_id` itu dan baris yang lebih lama dihapus di transaksi yang sama dengan `approvals`. `edit` dikeluarkan dari kotak masuk, atau ditambah `edited_args` JSON + preview ulang. Baris berumur > `expires_at` dibersihkan `serve`. Test: `TestApprovalInboxStaleResponseIgnored` (jawaban ganda #1 tidak pernah menjawab #2) dan `TestApprovalInboxUnauthorizedResponder`. |
| AC-02 | MINOR | SPEC.md:2083 (`owner_start_ns` = `now_boot` saat proses start), :1359, :2091 (kriteria 3: proses (`owner_pid`, `owner_start_ns`) masih ada), :2093 (`TestLeaseLivenessClockStep` di Linux/macOS/Windows), :2217 (I5) | Pengamat tidak bisa mengetahui waktu start proses lain dalam jam boot di macOS/Windows. API yang tersedia (`GetProcessTimes` CreationTime, `proc_pidinfo` `pbi_start_tvsec`) mengembalikan **jam dinding** saat start [INFERENCE: perilaku API]. Konversi ke `now_boot` bergeser sebesar step jam sejak proses start. Akibatnya, setelah step jam ±1 jam, pemilik yang hidup dinilai "tidak ada", lalu lease-nya dianggap mati dan diambil alih. Ini kelas cacat yang sama dengan AB-02. Spesifikasi menyatakan test-nya sendiri di 3 OS, sehingga definisi ini akan gagal saat F1 dan tidak lolos diam-diam; karena itu MINOR. | Definisikan `owner_start_ns` sebagai cap start **opaque dari OS** yang dibandingkan secara persis lewat API yang sama: Linux `/proc/<pid>/stat` `starttime`; macOS `proc_pidinfo` `pbi_start_tvsec/usec` apa adanya; Windows `GetProcessTimes` CreationTime apa adanya. Pemilik membaca cap miliknya sendiri lewat API yang sama. Kriteria 3 hanya boleh menyimpulkan "mati" bila PID tidak ada atau cap berbeda. |
| AC-03 | MINOR | SPEC.md:1613 ((0) "restore berjalan dengan `dmb_maint() = 'restore'`"; (3) reapply fase 0 + "sesinya dilabel ulang ke txid baru"), :2101 (mode `'restore'`), :2128 dan :2131 (label hanya dari koneksi `'purge'`; pelabelan ulang oleh reapply), DDL `guard_labels_ins` ↔ SPEC.md:1617 (+1.400, teks reapply lama); SPEC.md:2093 ("Pelepasan normal menulis `expires_at = 0`") ↔ :2117 (pemilik selesai → DELETE); :1618 (nilai lease purge tanpa `expires_at`/`owner_*`; "pelepasan … = state machine §11.11.6") ↔ :2135-2144 (tidak ada baris pelepasan/Ctrl-C); SPEC.md:893 dan docs/SELF-HEALING.md:433 (`poller_lease`: heartbeat 2 s / expiry 10 s, di luar `alive()`) ↔ SPEC.md:2087; SPEC.md:2269, :2271, :2272, :2275, :2276 (#35/#37/#38/#41/#42 tanpa nama test) ↔ :2231 | Ada beberapa kontradiksi normatif yang akan menggagalkan `TestConcurrencyDocRefs`, atau membuat implementasi menebak. (1) Langkah (3) restore harus menulis label `purge_state`, juga ke sesi yang sudah berlabel, tetapi di mode `'restore'` guard menolaknya (`internal: … reserved` dan `session_purging`, direproduksi). Mode yang benar (`'purge'` + lease purge milik restorer) tidak dinyatakan. (2) :1617 masih menetapkan reapply berurutan per entri. (3) Semantik pelepasan lease sesi ganda (UPDATE vs DELETE). Lease purge tidak punya field yang dipakai `alive(x)`, dan baris pelepasannya tidak ada. (4) `poller_lease` adalah lease ketiga yang tidak memakai `alive()`/jam boot. (5) Lima skenario tidak punya test dan tidak dipetakan ke invarian, padahal :2231 mewajibkannya. | (1) Tambahkan di §11.11.3/§11.11.7 dan §11.6 (3): reapply fase 0 dijalankan di mode `'purge'` oleh pemilik barrier, yang menulis lease purge baru di transaksi pertama. (2) Hapus kalimat lama di :1617 dan ganti dengan rujukan ke §11.11.7. (3) Satu aturan: lease sesi dilepas dengan DELETE; lease purge dilepas dengan `value.expires_at = 0`. Nyatakan field `value` = `{owner_pid, owner_start_ns, owner_host, boot_id, heartbeat_at, expires_at, phase, excluded[]}`, dan tambahkan baris "Ctrl-C / berhenti normal → lepas" di §11.11.6. (4) `poller_lease` memakai `alive()`. (5) Beri nama test: `TestRestoreConcurrent`, `TestDbWriteStalledReport`, `TestPurgeDiskFull`, `TestApprovalResponseAfterTurnEnd`, `TestRestoreForeignDataDirPurgesLabeled`. |
| AC-04 | MINOR | SPEC.md:2177 (pembersihan yatim saat open DB dan `serve` tiap 60 s: lease tidak `alive()` → ambil lease), :2116 (pemilik lama bangun → `held(t)` bila belum diambil alih), :2082 (jam boot ikut berjalan saat sleep) ↔ :2237 (skenario #3 "Laptop sleep … aman") | Saat laptop bangun, jam boot melompat sebesar durasi sleep, sehingga **semua** lease kedaluwarsa bersamaan. Pemilik baru me-heartbeat pada tick berikutnya (≤ 2 s). Pemindai yatim `serve` (fase acak dalam 60 s), atau proses baru yang start tepat setelah bangun (mis. `run` dari cron), bisa lebih dulu mengambil alih. Akibatnya turn yang berjalan sebelum laptop ditutup dibatalkan, process group dibunuh, dan journal ditutup `deferred`. [INFERENCE] Peluangnya sekitar 2/60 per sesi aktif per kali bangun untuk `serve`, lebih besar untuk proses yang baru start. Keselamatan tetap terjaga (fencing); yang terganggu liveness, padahal #3 diberi status "aman". | Untuk pemilik di host yang sama dan proses yang masih ada (kriteria 3 benar), pengambil alih mensyaratkan lease terlihat kedaluwarsa pada dua pengamatan berjarak ≥ 3 s menurut jam monotonik pengamat, tanpa perubahan `heartbeat_at`. Pemilik yang benar-benar mati (kriteria 3 salah) tetap diambil alih seketika. Tambahkan kasus sleep/resume ke `TestLeaseLivenessClockStep`. |
| AC-05 | MINOR | SPEC.md:1336-1337 (`sessions`/`blobs`: hanya no-UPDATE), :1891 ("`doctor gc` hanya menghapus file blob, baris `blobs` tetap") ↔ SPEC.md:2129 (`deleted` hanya lewat langkah 30), :1614 ("satu-satunya penghapusan data sesi") | DB tidak menegakkan state `deleted` hanya-lewat-purge. Koneksi biasa bisa `DELETE FROM sessions` untuk sesi tanpa rujukan (sesi 0 event, wizard batal) dan `DELETE FROM blobs`; keduanya direproduksi di mode `''`. Untuk `blobs`, penghapusan ini meninggalkan file tanpa baris atau melanggar status `blob_expirations`, sementara gc sendiri tidak pernah menghapus baris. | Tambahkan `sessions_no_delete` dan `blobs_no_delete` `BEFORE DELETE … WHEN dmb_maint() IS NOT 'purge'`. Jumlah P9 menjadi 39, dan keduanya masuk `TestImmutableTables`. |
| AC-06 | MINOR | SPEC.md:2225 (I13: "id sesi ter-purge tidak tersisa di DB") ↔ SPEC.md:1563 (`kanban_task_events.payload` JSON bebas, append-only tanpa pengecualian; uji: payload `{"session":"C"}` diterima), docs/SELF-HEALING.md:385 dan :387 (salvage: "celah dilaporkan per sesi" di `system_notice{healing:{… lost_ranges}}` `__system__`), payload event `delegate_*` di sesi induk [INFERENCE: memuat id anak] | Mekanisme I13 hanya menutup rujukan **terstruktur** (FK, `auto_session_refs`). Id sesi di payload JSON bebas, terutama di tabel append-only tanpa pengecualian purge atau di event `__system__` yang di-hash-chain, tidak tersentuh purge. Klaimnya melebihi mekanismenya. | Pilih salah satu. (a) Persempit I13 menjadi "tidak ada **rujukan terstruktur** ke sesi ter-purge". (b) Atau tetapkan aturan skema: id sesi hanya boleh disimpan di kolom FK atau tabel rujukan (pola `auto_session_refs`, mis. `session_links(event_id, session_id)` untuk `delegate_*`/healing), lalu `TestPurgeLeavesNoSessionRef` memindai semua kolom TEXT/JSON untuk substring id ter-purge. |
| AC-07 | MINOR | SPEC.md:1017-1024 (§11.1: satu transaksi per migrasi; `user_version` > maksimum → tolak **saat start**), SPEC.md:2739 ↔ SPEC.md:2076 (§11.11 "satu-satunya sumber normatif" konkurensi), :2148-2166 (barrier hanya untuk restore/salvage) | Migrasi oleh binary baru, misalnya `serve` yang di-restart setelah `damnbot update` sementara TUI versi lama masih terbuka, tidak ada di model. Proses lama tetap menulis ke skema baru, dengan trigger yang mungkin memanggil fungsi `dmb_*` yang belum dikenalnya, atau dengan asumsi invarian lama. Cek `user_version` hanya dilakukan saat start. | Jadikan migrasi pemeliharaan barrier (`reason: "migrate"`, eksklusif). Setelah membuka ulang, proses memeriksa `user_version`; bila lebih besar dari maksimum binary, proses berpindah ke read-only + notice (semantik exit 78). Tambahkan baris ke §11.11.7/§11.11.10 dan `TestMigrationWhileOldBinaryRuns`. |
| AC-08 | MINOR | SPEC.md:2727 (`mcp serve` `messages_send` butuh approval §14.1), :1171 (`approvals.source` `'mcp_serve'`), :2186-2203 (tabel penulis) | `messages_send` dari `mcp serve` menulis `approvals` (wajib `session_id` + `event_id` + guard F) dan `gateway_deliveries`, tetapi tidak ada baris yang menetapkan sesi dan lease yang dipakai. Proses `mcp serve` bukan pemegang turn. | Tambahkan baris di tabel penulis: `mcp serve` mencatat event + `approvals` + delivery di `__system__` dengan `auto_session_refs` ke sesi percakapan (F dikecualikan, P aktif), atau lewat kotak masuk ke `serve`. Test: `TestMCPServeMessagesSendUnderGuards`. |
| AC-09 | MINOR | SPEC.md:2271 (#37: `db_write_stalled{pid?, since}` setelah 30 s), DECISIONS.md:892 | Residu #37 jujur, tetapi (1) `db_write_stalled` tidak didefinisikan di daftar notice/event mana pun, dan (2) pemicu paling umumnya, Ctrl-Z pada TUI/`run`, adalah SIGTSTP yang **bisa ditangkap**, tidak seperti SIGSTOP. | TUI/ACP/`run` menangani SIGTSTP dengan menyelesaikan atau me-rollback transaksi tulis yang sedang berjalan (≤ 200 ms), melepas lock bersama `maint.lock`, baru menghentikan diri. Residu #37 lalu menyempit ke SIGSTOP/debugger. Definisikan `db_write_stalled` di SELF-HEALING §8 (temuan `doctor` + notice). |

### Skenario yang terlewat dari tabel 42 skenario (SPEC.md:2233-2276)

| # | Skenario | Status | Temuan |
|---:|---|---|---|
| 43 | Jawaban approval ganda/terlambat untuk permintaan #1, lalu permintaan #2 di turn yang sama | tidak aman: jawaban dipakai untuk aksi yang tidak dilihat penjawab | AC-01 |
| 44 | Step jam dinding macOS/Windows, lalu kriteria 3 `alive()` memeriksa waktu start pemilik | lease hidup dinilai mati | AC-02 |
| 45 | Reapply fase 0 di mode `'restore'` | ditolak DDL | AC-03 |
| 46 | Laptop bangun; pemindai yatim atau proses baru mendahului heartbeat pemilik | turn hidup dibatalkan | AC-04 |
| 47 | DELETE `sessions`/`blobs` oleh koneksi non-purge | lolos | AC-05 |
| 48 | Id sesi di payload JSON bebas setelah purge | tersisa | AC-06 |
| 49 | Migrasi oleh binary baru saat binary lama masih menulis | tidak terdefinisi | AC-07 |
| 50 | `mcp serve messages_send` | penulis tidak terinventaris | AC-08 |
| 51 | Ctrl-Z (SIGTSTP) pada TUI di tengah transaksi tulis | menjadi #37, padahal bisa dicegah | AC-09 |

### Jumlah dan verdict

ID diverifikasi: OK **8** (AB-01..AB-05, AB-07..AB-09) · PARSIAL **1** (AB-06) · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **1** (AC-01) · MINOR **8** (AC-02..AC-09).

MAJOR terbuka untuk kriteria freeze: **1** (AC-01). BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-043 menutup kedua MAJOR sebelumnya (AB-01 barrier tanpa pemilik, AB-02 `boot_id` turunan jam). Klaim verifikasinya terbukti: DDL 0 error, trigger 86 = 36 + 37 + 13, 49 FK dengan cakupan transitif, dan mode fail-closed. §11.11 sebagai model tunggal pada umumnya konsisten dengan DDL. Keempat residu yang diakui (#9, #33, #37, #40) jujur, bermitigasi, dan tidak melanggar P1–P29, sehingga bukan MAJOR.

Satu MAJOR baru justru berasal dari mekanisme yang ditambahkan ADR-043 sendiri: kotak masuk `approval_responses` mengikat jawaban ke turn, bukan ke permintaan, dan tidak menetapkan otorisasi penjawab. Akibatnya, jawaban basi bisa menyetujui aksi irreversible lain di turn yang sama (kelas P26). Perbaikannya lokal dan tidak mengubah token, LOC, atau dependency: +1 kolom `request_id` di DDL, aturan konsumsi, scope token `approve`, dan 2 test. Setelah AC-01 ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi. AC-02..AC-09 masuk backlog ADR; AC-02 dan AC-03 sebaiknya ditutup sebelum F1, karena test SPEC sendiri akan gagal.

### Perintah reproduksi

```bash
# read-only: ekstrak 8 blok sql §11 (SPEC.md:1011-2073), eksekusi di /tmp, hitung trigger
PYTHONDONTWRITEBYTECODE=1 python3 - <<'EOF'
import os, re, sqlite3
L = open("SPEC.md").read().split("\n"); blocks, cur = [], None
for i, l in enumerate(L, 1):
    if not 1011 <= i <= 2073: continue
    if l.strip() == "```sql": cur = []; continue
    if cur is not None and l.strip() == "```": blocks.append("\n".join(cur)); cur = None; continue
    if cur is not None: cur.append(l)
st = {"maint": "", "tok": {}}
if os.path.exists("/tmp/adr043.db"): os.remove("/tmp/adr043.db")
c = sqlite3.connect("/tmp/adr043.db", isolation_level=None)
c.create_function("dmb_fts_norm", 1, lambda t: t, deterministic=True)
c.create_function("dmb_maint", 0, lambda: st["maint"])          # NULL/'gc'/'PURGE' diuji terpisah
c.create_function("dmb_lease_token", 1, lambda s: st["tok"].get(s))
c.execute("PRAGMA foreign_keys=ON")
for b in blocks: c.executescript(b)                              # 0 error
t = c.execute("select name, sql from sqlite_master where type='trigger'").fetchall()
g = [n for n, s in t if n.startswith("guard_")]
p9 = [n for n, s in t if "IS NOT 'purge'" in s and not n.startswith("guard_")]
fk = sum(len(c.execute(f"pragma foreign_key_list('{n}')").fetchall())
         for (n,) in c.execute("select name from sqlite_master where type='table'"))
print(len(blocks), len(t), len(g), len(p9), fk)                  # 8 86 36 37 49
EOF
```

## §Verifikasi ADR-044 (2026-10-18)

Bagian ini memverifikasi ADR-044 (docs/DECISIONS.md:909-984), yang mengklaim menutup AC-01 (MAJOR), AC-02..AC-09 (MINOR), dan residu AB-06 tanpa mekanisme baru. Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen. Dokumen lain read-only. Cakupannya:

- reproduksi DDL + uji fungsional read-only di `/tmp/adr044v.db`;
- review adversarial kotak masuk approval lewat model konsumen Python di atas DB yang sama, sesuai protokol SPEC.md:2222-2228;
- re-read §11.11 (SPEC.md:2081-2317) untuk mekanisme yang diubah: `takeover_ok()`, `owner_start`, barrier `migrate` + cek `user_version`, SIGTSTP, `db_write_stalled`, sesi pendek `messages_send`.

Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka. Residu yang diakui + bermitigasi bukan MAJOR.

### Cek mekanis dan reproduksi

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal |
| Link relatif markdown | 171 link, 0 rusak |
| Eksekusi DDL | 8 blok `sql` §11 (SPEC.md:1029-2077) di SQLite **3.53.4**, `dmb_*` terdaftar, `foreign_keys=ON`: **0 error** |
| Jumlah trigger | **88** = **36** guard + **39** P9 (`IS NOT 'purge'`, termasuk `sessions_no_delete`/`blobs_no_delete` SPEC.md:1339-1340) + **13** lain. Tidak ada sisa `= ''` atau `<> 'purge'`. Cocok dengan DECISIONS.md:959. |
| Graf FK | **49** kolom. 29 tabel menjangkau `sessions`. Satu-satunya kolom yang tidak tercakup adalah pengecualian tertutup `kanban_task_events.task_id`. Setiap tabel mutable punya guard UPDATE atau no-UPDATE. |
| Uji fungsional DB | **35/35** sesuai ekspektasi. Daftar 36 kasus penulis tidak tersedia, jadi saya menyusun kasus setara. Hasil per kelompok: **inbox** — duplikat `request_id` → `UNIQUE`, `allow_session` → CHECK, `edit` tanpa `edited_args` / `deny` dengan `edited_args` / JSON rusak → CHECK, `source` asing → CHECK, UPDATE ditolak, sesi berlabel → `session_purging`; **AC-05** — DELETE `sessions`/`blobs` ditolak di mode `''`, NULL, `'gc'`, `'restore'`, `'PURGE'` dan lolos di `'purge'`; **AC-03** — label `purge_state` ditolak di `'restore'` dan lolos di `'purge'`; **fail-closed** — NULL/`'gc'`/`'PURGE'` → P9 + P aktif; **fencing** dan **cek sesi-sama**; `foreign_key_check` = 0. Ada dua perilaku yang perlu dicatat: (a) setelah baris dikonsumsi (DELETE), jawaban ulang dengan `request_id` yang sama **diterima lagi**, karena `UNIQUE` hanya berlaku selama baris ada; (b) DB tidak memeriksa bahwa `request_id` atau `turn_id` cocok dengan journal yang sedang menunggu. |
| Simulasi konsumen (26 langkah, model protokol SPEC.md:2224-2226) | Dua approval berurutan: jawaban terlambat r1 lolos pra-cek karena TOCTOU, tetapi **tidak** menjawab r2, dan terhapus saat r2 diputuskan. Duplikat r2 ditolak `UNIQUE`. Jawaban ulang r2 setelah konsumsi menjadi baris yatim yang diabaikan. Token `send`, identitas lain, kanal lain non-owner, dan preview salah ditolak penjawab. Baris tidak sah yang lolos karena bug penjawab ditolak konsumen. `edit` dengan skema tidak valid → `deny`. **`edit` valid berisi perintah lain (`curl … \| sh`) → dipakai** (AD-02). Crash konsumen sebelum COMMIT → baris tetap, pengambil alih dengan `request_id` sama mengonsumsinya sekali (`approvals` = 1). Jawaban setelah `expires_at` → diabaikan. |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| AC-01 | OK | SPEC.md:1911-1922 (`request_id UNIQUE`, `preview_sha`, `decision ∈ {allow_once, deny, edit}`, CHECK `edited_args`), :1687 (`approval_request` memuat `request_id`, `preview_sha`, `channels[]`, `expires_at`), :2222-2228 (protokol penjawab/konsumen, scope `approve`), :2252 (I15), :2303 dan :2312-2315 (#43, #52–#55) | Cacat AC-01 (jawaban terikat turn) tertutup: pencocokan per `request_id`, dan baris basi dihapus di transaksi keputusan (direproduksi). Sisa celah ada di AD-02 dan AD-03. |
| AC-02 | OK | SPEC.md:2090 (cap start opaque OS, dibandingkan persis; API gagal → dianggap ada), :1362, :2283, :2304 (#44) | macOS dan Windows bertanda [INFERENCE, F1], jujur. Bila pemilik gagal membaca cap-nya sendiri, perilakunya tidak ditetapkan (AD-08). |
| AC-03 | PARSIAL | SPEC.md:1616 (3) (reapply di mode `'purge'`), :1620 (kalimat lama diganti rujukan), :2102, :2126, dan :2149 (satu semantik pelepasan per jenis), :2156 (field `value` = field `alive()`), :2100 (`poller_lease` lewat `alive()`/`takeover_ok()`), :2295-2302 (test bernama) ↔ SPEC.md:1621 (masih `value={pid, host, boot_id, start_ns, heartbeat_at, phase, excluded[]}`) | Satu definisi lama tertinggal, bertentangan dengan :2156 (tanpa `expires_at`, masih `start_ns`). Lihat AD-08. |
| AB-06 (residu) | OK | SPEC.md:1616 (3), :2109, :2169, :2219 | Direproduksi: label ditolak di `'restore'`, lolos di `'purge'`. |
| AC-04 | OK | SPEC.md:2100 (`takeover_ok()`: pemilik terbukti tidak ada → seketika; selain itu dua pengamatan ≥ 3 s dengan `heartbeat_at` tetap; berlaku untuk lease sesi, purge, dan poller), :2124, :2153, :2191, :2306 (#46) | Race laptop bangun tertutup untuk pengambilalihan. Jalur open-DB dan fase 0 purge masih memakai aturan lain (AD-04). |
| AC-05 | OK | SPEC.md:1339-1340, :2253, :2307 (#47) | Direproduksi di 5 mode. |
| AC-06 | OK | SPEC.md:2250 (I13 dipersempit ke rujukan terstruktur, payload JSON = best-effort), :2308 (#48) | Residu jujur. `docs/TRUST-MODEL.md` yang dirujuk adalah deliverable GAP-SEC-28 (SPEC.md:2775) dan belum ada filenya. |
| AC-07 | OK | SPEC.md:1021, :2162, :2169, :2171, :2177, :2220, :520 (`schema_newer`), :2309 dan :2317 (#49, #57) | Kasus TUI lama tertutup. Kasus `serve` lama tidak tertutup, dan inilah MAJOR baru (AD-01). |
| AC-08 | OK | SPEC.md:2210 (sesi pendek milik `mcp serve`, lease + journal + inbox + `approvals` + delivery; tanpa `serve` → `module_unavailable`), :2310 (#50) | Pembatalan atau timeout klien MCP tidak ditangani (AD-07). |
| AC-09 | OK | SPEC.md:2178 (handler SIGTSTP), :2179 (`db_write_stalled`), docs/SELF-HEALING.md:515, SPEC.md:2297 (#37 dipersempit), :2311 (#51) | Jalur pelaporan stall bergantung pada tulis DB (AD-06). Handler tidak menghentikan process group tool (AD-05). |

### Review adversarial kotak masuk approval (diminta khusus)

| Kasus | Hasil | Bukti |
|---|---|---|
| Dua approval berurutan di satu turn | aman: konsumen hanya mengambil `request_id` yang sedang menunggu; baris lain turn itu dihapus di transaksi keputusan | SPEC.md:2225; simulasi K1–K1d |
| Jawaban terlambat | aman: ditolak pra-cek penjawab. Bila lolos (TOCTOU), diabaikan konsumen dan terhapus saat keputusan berikutnya, saat journal ditutup, atau oleh housekeeping | SPEC.md:2225-2226; K1b, K2d |
| Jawaban duplikat | aman: `UNIQUE(request_id)` selama baris ada; setelah konsumsi, permintaan sudah tidak menunggu | SPEC.md:1915; uji ib2/ib10 |
| Penjawab tidak sah | aman di pra-cek. Konsumen bisa memvalidasi ulang identitas, kanal, preview, dan kedaluwarsa, tetapi **tidak** scope token, karena token tidak tersimpan di baris. Otoritas per kelas tool (P25: tool berbahaya hanya owner) tidak dinyatakan | SPEC.md:2224-2225, :122; K3–K3f → AD-03 |
| `edit` tidak valid | aman: skema P3 gagal → `deny` | SPEC.md:2225; K4b |
| `edit` valid tetapi aksinya berbeda atau kelasnya naik | **tidak terspesifikasi**: "klasifikasi ulang efek" tanpa aturan kelanjutan; dalam model, perintah arbitrer dieksekusi tanpa preview baru | SPEC.md:2225, :2315, :2662; K4c–K4d → AD-02 |
| Crash konsumen | aman, **asalkan** replay recovery `awaiting_approval` mempertahankan `request_id`. Hal ini tersirat dari journal yang durable tetapi tidak dinyatakan (tabel recovery SELF-HEALING.md:354-361 tidak punya baris `awaiting_approval`) | SPEC.md:2314 (#54); K5–K5e → AD-03 |
| Baris yang gagal validasi ulang | ambigu: kalimat "gagal → `deny`" bisa dibaca sebagai menolak **permintaan**, padahal ADR menyebut "diabaikan". Bila diabaikan tetapi tidak dihapus, baris itu menduduki `UNIQUE(request_id)`, sehingga penjawab sah mendapat "sudah dijawab" | SPEC.md:2225 ↔ DECISIONS.md:924 → AD-03 |

**Residu yang diakui putaran ini** semuanya jujur, bermitigasi, dan tidak melanggar P1–P29:

- #37 dipersempit ke SIGSTOP/debugger/hang;
- #40 time namespace;
- #48 payload JSON;
- pengambilalihan tertunda ≥ 3 s;
- `messages_send` membutuhkan `serve`;
- dua [INFERENCE] macOS/Windows untuk `owner_start`, diverifikasi F1.

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AD-01 | MAJOR | SPEC.md:2177 (cek `user_version` per transaksi: skema lebih baru → `schema_newer`, "proses menjadi read-only + notice"), :520, :1021 (migrasi oleh binary baru mana pun di bawah barrier) ↔ SPEC.md:2766 (distribusi resmi lewat Homebrew tap, winget, `.deb`/`.rpm`), docs/SELF-HEALING.md:399 (hanya `damnbot update` yang menukar binary + me-restart), SPEC.md:312 (exit 75 = "restart terencana"), SPEC.md:894 (offset `getUpdates` baru dipersist setelah baris inbound ter-commit), SPEC.md:119 (P22: 0 pesan masuk hilang, 0 cron di-pause, recovery ≤ 10 s) | Upgrade lewat jalur distribusi resmi di luar `damnbot update` (mis. `brew upgrade`, `apt upgrade`) menukar binary di disk tanpa me-restart `serve` yang sedang berjalan. Proses binary baru pertama yang membuka DB (mis. TUI) menjalankan migrasi di bawah barrier. `serve` lama ikut drain, membuka ulang, mendapat `schema_newer`, lalu menjadi **read-only tanpa batas waktu**. Proses itu tidak crash, sehingga supervisor tidak me-restart-nya. Inbound gateway tidak bisa di-commit, sehingga offset tidak maju dan Telegram hanya menyimpan update ≤ 24 jam ([Bot API getUpdates](https://core.telegram.org/bots/api#getupdates)). Akibatnya pesan hilang setelah 24 jam, dan adapter non-polling [INFERENCE] bisa lebih cepat. Cron dan kanban `serve` berhenti. Notice "perlu `damnbot update`" tidak sampai ke owner, karena pesan owner lewat outbox yang membutuhkan tulis DB [INFERENCE: SELF-HEALING §7 tidak punya jalur non-DB]. Ini outage gateway yang **senyap** dan melanggar P22, dipicu oleh jalur upgrade yang didukung. `TestMigrationWhileOldBinaryRuns` hanya menguji TUI. | (1) Proses `serve` yang mendapat `schema_newer` keluar dengan **exit 75**, sehingga supervisor menjalankan ulang binary di disk. Bila binary di disk masih lama, proses ditolak start (`user_version` > maksimum, exit 78, gate start + notifikasi lewat ledger). TUI/ACP/`run` tetap read-only + notice. (2) Pendaftar `run/maint.holders/<pid>` memuat `{kind, version}`. Migrator yang melihat `serve` berversi lebih lama mencatat bahwa `serve` akan di-restart, dan `serve` keluar 75 saat melihat marker `reason = migrate` alih-alih membuka ulang. (3) Test: `TestMigrationWhileOldServeRuns` (paket di-upgrade di luar `damnbot update`, TUI baru bermigrasi → `serve` exit 75 → supervisor start binary baru ≤ 10 s → 0 pesan inbound hilang, cron lanjut). |
| AD-02 | MINOR | SPEC.md:2225 ("untuk `edit`, validasi `edited_args` terhadap schema tool (P3) dan klasifikasi ulang efek; gagal → `deny`"), :2315 (#55 "preview pasca-edit dicatat"), :2662 (§18.2: `edit` lokal sama), :2209 (scope `approve`) ↔ SPEC.md:123 (P26: `irreversible`/`monetary`/`delegated_agent_bypass` 100% approval + **preview**), :2665 (`TestPreviewBeforeDangerous`) | Aturan kelanjutan setelah klasifikasi ulang tidak ditetapkan. `edit` valid-skema bisa mengubah permintaan `mutating` menjadi aksi lain di kelas yang lebih ketat, dan dieksekusi tanpa preview baru (direproduksi di model, K4d). Bagi klien `mcp serve`, scope `approve` berarti "jalankan argumen arbitrer untuk tool yang sedang meminta approval", bukan sekadar "setujui usulan agent". Klien MCP adalah agent yang bisa terkena prompt injection. Semantik `edit` lokal di §18.2 sudah ada sebelumnya. ADR-044 memperluasnya ke penjawab jarak jauh tanpa "preview ulang" yang diusulkan di AC-01. | Bila hasil klasifikasi ulang lebih ketat dari kelas asal, atau masuk kelas P26, atau `subject`/path/target berubah, `edit` **tidak dieksekusi**. Konsumen membuat permintaan baru (`request_id` baru + preview baru, termasuk dry-run) ke `channels[]`. `edit` dari `source = 'mcp_serve'` hanya dengan scope terpisah `approve_edit`. Test: `TestApprovalEditEscalationRequiresNewPreview`. |
| AD-03 | MINOR | SPEC.md:2224-2226, :1915, :1687, :122 (P25), :2511 dan :2760 (timeout "300 s gateway, tak terbatas interaktif"), :2664 (`approval.preview_timeout_s` = 600), :2188 ("≤ 24 jam"), :2226 (housekeeping > 24 jam), :2089 (jam), docs/SELF-HEALING.md:354-361 | Ada beberapa celah protokol: (1) Nasib baris yang gagal validasi ulang ambigu: menolak permintaan atau diabaikan. Bila diabaikan tanpa dihapus, baris itu menduduki `UNIQUE`. (2) Konsumen tidak bisa memverifikasi ulang scope token. (3) Otoritas penjawab tidak dibatasi oleh kelas tool, padahal P25 mewajibkan tool berbahaya hanya untuk owner. (4) Pertahanan `request_id` saat replay recovery (#54) tidak dinyatakan. (5) Jam untuk `expires_at` tidak ditetapkan (jam boot atau jam dinding), dan ada empat nilai timeout yang berbeda. Housekeeping 24 jam bisa menghapus jawaban sah untuk permintaan interaktif yang "tak terbatas". | (1) Baris yang gagal validasi di-DELETE + `approval_response_ignored`; permintaan tetap menunggu dan tidak pernah di-`deny` karena baris tidak sah. (2) Nyatakan bahwa scope diperiksa penjawab, dan simpan `responder_scope` di baris untuk audit. (3) Otoritas = aturan §14.1/P25 untuk kelas tool (berbahaya → owner saja), diperiksa kedua sisi. (4) Replay memakai `request_id` yang sama dari journal. (5) `expires_at` memakai jam boot; satu tabel timeout per kanal di §14.1; housekeeping hanya menghapus baris untuk permintaan yang sudah tidak menunggu. Opsional: guard INSERT memeriksa `NEW.request_id` = `json_extract(approval_request,'$.request_id')` journal `awaiting_approval` (1 lookup PK) untuk menutup TOCTOU. |
| AD-04 | MINOR | SPEC.md:2100 (`takeover_ok()` (b): dua pengamatan ≥ 3 s "menurut jam boot"), :2191 (pembersihan yatim "saat open DB" memakai `takeover_ok()`), :1623 (fase 0 purge mengecualikan sesi bila `alive()`), :109 (P12: frame TUI pertama ≤ 100 ms) | (1) Jalur open-DB tidak menyatakan bahwa pengamatan kedua asinkron. Implementasi literal menunggu ≥ 3 s sebelum tulis pertama, bertentangan dengan P12. (2) Fase 0 purge tetap memakai `alive()`. Sesi yang pemiliknya masih ada tetapi lease-nya baru kedaluwarsa, mis. tepat setelah laptop bangun atau TUI di-Ctrl-Z, dilabel dan dipurge, bukan dikeluarkan `session_busy`. Padahal AC-04 menjanjikan satu aturan. (3) Jarak 3 s diukur dengan jam boot, yang ikut berjalan saat sleep, sehingga pasangan pengamatan yang mengapit sleep sudah memenuhi syarat. | (1) Saat open DB hanya kasus (a) yang diambil alih seketika; kasus (b) dicatat lalu diselesaikan oleh pemindai `serve` atau di latar belakang, tanpa memblok. (2) Fase 0: X dikeluarkan bila `NOT takeover_ok(X)`. (3) Jarak diukur dengan jam monotonik yang **tidak** berjalan saat suspend (Linux `CLOCK_MONOTONIC`, macOS `mach_absolute_time`, Windows `QueryUnbiasedInterruptTime`). |
| AD-05 | MINOR | SPEC.md:2178 (handler SIGTSTP menutup DB dan melepas lock, lalu berhenti), :2316 (#56 "aman"), :2191 (pemindai yatim `serve` tiap 60 s), :2129 (process group tool baru dibunuh saat pemilik mendeteksi `lost`) | Tool `exec` berjalan di process group sendiri, jadi Ctrl-Z pada TUI **tidak** menghentikannya. Proses itu terus memutasi workspace selama TUI ter-suspend. Sekitar 13–70 s kemudian `serve` mengambil alih lease (pemilik ada, dua pengamatan), menutup journal `deferred`, dan melaporkan `outcome_unknown`. Bila user mengonfirmasi ulang, eksekusi terjadi ganda. Selain itu, turn yang berjalan selalu dibatalkan bila Ctrl-Z lebih lama dari ~1 menit dan `serve` hidup. Pemicunya umum, jauh lebih sering daripada residu #33. | Handler meneruskan SIGTSTP ke process group tool, dan SIGCONT saat resume (semantik job control). Pemindai yatim **tidak** mengambil alih lease yang pemiliknya dalam state stop job control (Linux `/proc/<pid>/stat` state `T`; macOS `SSTOP`). Pengambilalihan tetap terjadi bila user membuka sesi di proses lain (`session_busy` + tawaran fork). Test: `TestSigtstpStopsToolGroup`. |
| AD-06 | MINOR | SPEC.md:2179 (`db_write_stalled` = event `system_notice` + notifikasi owner + temuan `doctor`), docs/SELF-HEALING.md:493-502 (event `__system__` + pesan owner Telegram), :515 (contoh "tertahan … oleh PID 4121 (TUI)") | Saat stall, setiap `BEGIN IMMEDIATE` mendapat `db_busy`. Akibatnya event `system_notice` tidak bisa ditulis, dan pesan owner Telegram juga tidak bisa, karena lewat outbox `gateway_deliveries`. `doctor` tidak punya sumber data untuk `D-DB-STALL`. Pemegang lock SQLite juga tidak dapat diidentifikasi, karena `holders[]` berisi semua pendaftar. | Laporkan lewat jalur non-DB: log ERROR, banner TUI/ACP, file `run/db_stall.json` (dibaca `doctor`), dan pesan owner langsung ke API adapter tanpa outbox (best-effort, rate-limit §7). Event `system_notice` ditulis setelah stall berakhir, dengan `duration`. Ubah contoh pesan menjadi "pemegang kemungkinan: PID …". |
| AD-07 | MINOR | SPEC.md:2210 (`messages_send`: satu sesi pendek per permintaan, approval lewat `serve`), :2511 (timeout gateway 300 s) | Klien MCP umumnya memberi batas waktu request yang lebih pendek daripada approval, atau mengirim `notifications/cancelled`. `mcp serve` tetap menunggu, lalu mengirim pesan setelah klien menyerah. Bila klien me-retry, terbentuk permintaan baru dengan `request_id` dan `idem_key` baru, sehingga pesan keluar ganda tanpa label. | Pembatalan MCP atau transport tertutup → permintaan diputuskan `deny{reason:"cancelled"}` di transaksi keputusan, sebelum delivery dibuat. `messages_send` menerima `client_request_id` opsional sebagai `idem_key`, sehingga retry klien idempoten. Test: `TestMCPServeMessagesSendCancel`. |
| AD-08 | MINOR | SPEC.md:1621 (`value={pid, host, boot_id, start_ns, heartbeat_at, phase, excluded[]}`) ↔ :2156; SPEC.md:2257 ("status = hasil review ADR-043"); SPEC.md:2090 (tidak ada aturan bila pemilik gagal membaca cap start-nya sendiri); SPEC.md:2250 → `docs/TRUST-MODEL.md` (belum ada; deliverable SPEC.md:2775) | Ada sisa inkonsistensi dokumen. (1) Definisi lama field lease purge akan menggagalkan `TestConcurrencyDocRefs`. (2) Label status skenario sudah usang. (3) Bila pemilik menulis `owner_start = ''` karena API dirinya gagal, pengamat melihat "cap berbeda" dan langsung mengambil alih lease hidup. (4) Dokumentasi residu bergantung pada file yang belum ada. | (1) Ganti :1621 dengan rujukan ke §11.11.6. (2) "status = hasil review ADR-044". (3) `owner_start = ''` = tidak diketahui → dianggap ada (kasus (b)). (4) Daftarkan isi wajib TRUST-MODEL.md (residu #37, #40, #48, retensi backup) di GAP-SEC-28. |

### Skenario yang terlewat dari tabel 57 skenario (SPEC.md:2259-2317)

| # | Skenario | Status | Temuan |
|---:|---|---|---|
| 58 | Upgrade lewat package manager, lalu TUI baru bermigrasi saat `serve` lama berjalan | `serve` read-only senyap, inbound hilang > 24 jam | AD-01 |
| 59 | `edit` jarak jauh mengubah aksi ke kelas P26 | dieksekusi tanpa preview baru | AD-02 |
| 60 | Baris tidak sah dengan `request_id` yang sedang menunggu (bug/TOCTOU penjawab) | ambigu: `deny` atau menduduki `UNIQUE` | AD-03 |
| 61 | Purge dimulai tepat setelah laptop bangun atau saat TUI di-Ctrl-Z | sesi aktif dilabel (fase 0 memakai `alive()`) | AD-04 |
| 62 | Ctrl-Z pada TUI saat `exec` berjalan | tool terus berjalan; diambil alih `serve`; eksekusi ganda setelah konfirmasi | AD-05 |
| 63 | Pelaporan `db_write_stalled` selama stall | event dan pesan owner butuh tulis DB | AD-06 |
| 64 | Klien MCP timeout/cancel saat `messages_send` menunggu approval | pesan tetap terkirim; retry ganda | AD-07 |

### Jumlah dan verdict

ID diverifikasi: OK **9** (AC-01, AC-02, AB-06, AC-04..AC-09) · PARSIAL **1** (AC-03) · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **1** (AD-01) · MINOR **7** (AD-02..AD-08).

MAJOR terbuka untuk kriteria freeze: **1** (AD-01). BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-044 menutup AC-01 dengan benar: jawaban kini terikat per permintaan, tepat sekali, terbukti lewat DDL dan simulasi 26 langkah. AC-02..AC-09 juga tertutup (AC-03 dengan satu sisa dokumen), tanpa menambah mekanisme. Klaim DDL terbukti: 88 = 36 + 39 + 13, 49 FK, fail-closed, dan AC-05 di 5 mode. Review adversarial inbox tidak menemukan jalur yang membuat jawaban basi, duplikat, atau tidak sah menyetujui permintaan lain.

Satu MAJOR datang dari perilaku baru `schema_newer → read-only`. Bagi `serve`, perilaku ini menjadi outage gateway, cron, dan kanban yang senyap setelah upgrade lewat jalur distribusi resmi, dan melanggar P22. Perbaikannya lokal dan memakai kontrak exit yang sudah ada (exit 75 "restart terencana"), tanpa perubahan token, LOC, DDL, atau dependency. Setelah AD-01 ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; AD-02..AD-08 masuk backlog ADR. AD-02 dan AD-03 sebaiknya ditutup sebelum F4, yaitu fase implementasi approval.

### Perintah reproduksi

```bash
# read-only: DDL §11 → /tmp, hitung trigger/FK (hasil: 8 88 36 39 49)
PYTHONDONTWRITEBYTECODE=1 python3 - <<'EOF'
import os, sqlite3
L = open("SPEC.md").read().split("\n")
end = next(i for i, l in enumerate(L, 1) if l.startswith("## 12."))
blocks, cur = [], None
for i, l in enumerate(L, 1):
    if not 1011 <= i <= end: continue
    if l.strip() == "```sql": cur = []; continue
    if cur is not None and l.strip() == "```": blocks.append("\n".join(cur)); cur = None; continue
    if cur is not None: cur.append(l)
db = "/tmp/adr044v.db"
if os.path.exists(db): os.remove(db)
st = {"maint": "", "tok": {}}
c = sqlite3.connect(db, isolation_level=None)
c.create_function("dmb_fts_norm", 1, lambda t: t, deterministic=True)
c.create_function("dmb_maint", 0, lambda: st["maint"])
c.create_function("dmb_lease_token", 1, lambda s: st["tok"].get(s))
c.execute("PRAGMA foreign_keys=ON")
for b in blocks: c.executescript(b)
t = c.execute("select name, sql from sqlite_master where type='trigger'").fetchall()
g = [n for n, s in t if n.startswith("guard_")]
p9 = [n for n, s in t if "IS NOT 'purge'" in s and not n.startswith("guard_")]
fk = sum(len(c.execute(f"pragma foreign_key_list('{n}')").fetchall())
         for (n,) in c.execute("select name from sqlite_master where type='table'"))
print(len(blocks), len(t), len(g), len(p9), fk)
EOF
```

## §Verifikasi ADR-045 (2026-10-18)

Bagian ini memverifikasi ADR-045 (docs/DECISIONS.md:986-1066), yang mengklaim menutup AD-01 (MAJOR), AD-02..AD-08 (MINOR), residu AC-03, dan skenario #58–#64 tanpa mekanisme baru. Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen. Dokumen lain read-only. Cakupannya:

- reproduksi DDL + uji fungsional read-only di `/tmp/adr045v.db`;
- review adversarial spool inbound (crash saat tulis, ingest ganda, urutan, isi pesan), lewat simulasi Python di atas DB yang sama;
- review adversarial protokol approval baru (SPEC.md:2233-2253), lewat uji DB + model konsumen;
- re-read `takeover_ok()`, SIGTSTP, `db_write_stalled`, `messages_send`, dan `docs/TRUST-MODEL.md`.

Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka. Residu yang diakui + bermitigasi bukan MAJOR.

### Cek mekanis dan reproduksi

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal |
| Link relatif markdown | 177 link, 0 rusak (termasuk `docs/TRUST-MODEL.md` baru, 38 baris, diindeks SPEC.md:22) |
| Eksekusi DDL | 8 blok `sql` §11 (SPEC.md:1031-2084) di SQLite **3.53.4**: **0 error** |
| Jumlah trigger / FK | **88** = 36 guard + 39 P9 + 13 lain; **49** kolom FK; satu-satunya kolom tidak tercakup adalah pengecualian tertutup `kanban_task_events.task_id`. Cocok dengan DECISIONS.md:1042. |
| Uji fungsional DB | **35/35** sesuai ekspektasi; daftar 28 uji penulis tidak tersedia, jadi saya menyusun kasus setara. **Guard `approval_not_pending`** (SPEC.md:1939-1942): jawaban saat journal `running`, `request_id` basi, turn tanpa journal, `session_id` salah, dan jawaban untuk permintaan lama setelah superseded → semuanya ditolak; aktif juga di mode `'restore'`. **CHECK**: `mcp_serve` tanpa token, token untuk non-`mcp_serve`, `mcp_serve` + `edit`, dan `allow_session` → ditolak. **`UNIQUE`** → jawaban kedua ditolak. **Guard P** → jawaban untuk sesi berlabel ditolak. **AC-05** di 5 mode, fail-closed P9, fencing, dan cek sesi-sama juga lolos. `foreign_key_check` = 0. Satu catatan: bila konsumen menghapus baris **tanpa** mengeluarkan journal dari `awaiting_approval` (bug), jawaban ulang diterima, sehingga I15 bergantung pada keputusan dan DELETE yang atomik, sesuai yang dinyatakan. |
| Model konsumen approval (16 langkah) | Token dicabut setelah INSERT → `deny{scope}`. Non-owner untuk kelas berbahaya (bug pra-cek) → `deny{unauthorized}`. `edit` owner ke `git push --force` → permintaan baru (kelas `irreversible`, `request_id` berbeda), 0 `allow_once` sebelum disetujui, jawaban untuk permintaan lama → `approval_not_pending`. `edit` tidak valid → `deny{schema}`. Crash konsumen sebelum COMMIT → replay dengan `request_id` sama dikonsumsi sekali. Duplikat setelah keputusan ditolak. Kotak masuk kosong di akhir. |
| Simulasi spool inbound | Baris JSONL terpotong (crash saat tulis) diabaikan. Ingest kedua → 0 baris baru (dedupe PK). Setelah ingest dan hapus file, **3 baris `event_id NULL` tanpa isi pesan di mana pun**: dua pesan Telegram yang sudah ter-commit tetapi masih antre di memori, dan satu pesan bridge dari spool. Lihat AE-01. |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| AD-01 | PARSIAL | SPEC.md:2169 (pendaftar `{kind, version, schema_max, owner_start}`), :2176 (migrasi menunggu pendaftar lama keluar), :2185-2188 (`serve` lama: spool + exit 75; TUI lama menahan; `--force-migrate`), :1023, :984, docs/SELF-HEALING.md:53, :433 | Jalur "binary lama tetap menulis" tertutup, dan `serve` tidak lagi read-only senyap. Masih ada dua sisa. Spool dan antrean kehilangan **isi** pesan, karena `gateway_inbound` tidak menyimpan payload (AE-01, MAJOR). `serve` lama keluar sebelum migrasi dipastikan berjalan (AE-02). |
| AD-02 | OK | SPEC.md:2237-2241 (tabel otoritas), :2248 (`edit` jarak jauh → permintaan baru + preview + dry-run), :1927 (CHECK `mcp_serve` tanpa `edit`), :2344 (#59) | Direproduksi: uji g11 dan model A3–A3e. |
| AD-03 | OK | SPEC.md:1939-1942 (guard `approval_not_pending`), :521, :2248 (gagal validasi → `deny` tercatat, fail-closed), :1923, :1926, dan :2248 (`responder_token` diperiksa ulang), :2235-2243 (otoritas per kelas = P25/§14.1), :2233 (`request_id` tetap saat replay; `expires_at` jam dinding UTC dengan alasan tertulis), :2547 (satu tabel timeout), :2249 (housekeeping) | Direproduksi: g1–g8, g15, A1, A2, A4, A5. Pengecualian jam dinding untuk `expires_at` jujur: step jam hanya menggeser timeout. |
| AD-04 | OK | SPEC.md:2107 (kasus (b) hanya di latar; jam monotonik tanpa suspend), :2202, docs/SELF-HEALING.md:350, SPEC.md:2346 (#61, fase 0 memakai `takeover_ok()`) | — |
| AD-05 | OK | SPEC.md:2189 (SIGSTOP ke process group tool; SIGCONT bila lease masih milik, SIGKILL bila sudah diambil alih), :2347 (#62) | Kasus tepi: pemilik mati saat group tool ter-stop (AE-05). |
| AD-06 | OK | SPEC.md:2190 (log/stderr/banner/`run/db_stall.json`/API adapter tanpa outbox; event DB setelah pulih; "pemegang kemungkinan"), :2348 (#63) | — |
| AD-07 | OK | SPEC.md:2253 (`idempotency_key` → `client_key` + `idem_key`; cancel/timeout/transport tertutup → `deny{cancelled}` sebelum delivery), :2349 dan :2353 (#64, #68) | Key sama dengan argumen berbeda tidak ditetapkan (AE-04). |
| AD-08 + AC-03 | OK | SPEC.md:1623 (field `value` hanya di §11.11.6), :2282 (label "hasil review ADR-045"), :2097 (`owner_start = ''` = tidak diketahui → dianggap ada), docs/TRUST-MODEL.md (aktor, batas vs heuristik, 7 residu) | #55 masih memakai teks lama (AE-06). |

### Review adversarial spool inbound (diminta khusus)

| Kasus | Hasil | Bukti |
|---|---|---|
| Crash saat menulis spool | aman **hanya bila** ack ke platform dilakukan setelah `fsync` baris itu. Baris terpotong diabaikan saat ingest (direproduksi). Aturan ack-setelah-`fsync` hanya ada di tabel ADR (DECISIONS.md:1035), tidak di SPEC.md:2186 | → AE-03 |
| Ingest ganda (crash antara ingest dan hapus file, atau ingest diulang) | aman untuk **identitas** pesan: `INSERT OR IGNORE` + PK `(platform, external_id)` (direproduksi: ingest kedua 0 baris) | SPEC.md:2186, docs/SELF-HEALING.md:433 |
| Isi pesan | **tidak aman**: `gateway_inbound` hanya menyimpan id, chat, pengirim, dan waktu (SPEC.md:1454-1462). Ingest memindahkan pesan spool ke tabel tanpa isi lalu menghapus file | → AE-01 |
| Pesan sudah ter-commit tetapi masih antre (REL-11) saat exit 75 | **hilang isinya**: spool hanya untuk inbound yang "belum ter-commit" (SPEC.md:2186). Antrean kerja ada di memori, ≤ 50/sesi (SPEC.md:2822; ≤ 5/chat SPEC.md:461) | → AE-01 |
| Urutan | per platform: ingest sebelum polling, sesuai urutan baris; antarfile memakai `<ts>` di nama file (jam dinding) | → AE-03 |
| Kerahasiaan | spool = payload mentah plaintext di `run/`, di luar `storage.encrypt` (SPEC.md:1018; CROSS-CUTTING §1.3), dan mode file tidak ditetapkan | → AE-03 |
| Binary di disk masih lama | spool tetap ada, `serve` exit 78 + notifikasi crash ledger (#67): residu jujur | SPEC.md:2186, :2352 |

**Protokol approval baru** tidak memperlihatkan jalur yang membuat jawaban basi, duplikat, tidak sah, atau hasil edit dieksekusi tanpa permintaan + preview baru. Klien `mcp serve` ber-scope `approve` dengan identitas owner dapat menyetujui kelas P26. Ini keputusan desain eksplisit, didokumentasikan di TRUST-MODEL.md §2, jadi bukan temuan.

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AE-01 | MAJOR | SPEC.md:1454-1462 (`gateway_inbound`: `platform`, `external_id`, `chat_id`, `sender_id`, `event_id`, `received_at`; tanpa isi), docs/SELF-HEALING.md:419-432 (insert inbound + offset di-COMMIT, `user_message` baru di-append worker per chat di transaksi lain; "Crash setelah COMMIT sebelum worker → baris `event_id NULL` diproses saat start"), SPEC.md:2822 (REL-11: antrean kerja **in-memory** per sesi ≤ 50), :461 (antre ≤ 5 per chat saat turn berjalan), :2186 dan docs/SELF-HEALING.md:433 (spool di-ingest ke `gateway_inbound`, lalu file dihapus), SPEC.md:2188 (`TestMigrationWhileOldServeRuns`: "0 pesan inbound hilang") ↔ SPEC.md:120 (P22: 0 pesan masuk hilang; 0 pesan dibuang diam saat sibuk) | Durabilitas inbound hanya mencakup **identitas** pesan, tidak **isinya**. (1) Pesan yang sudah ter-commit dan offset-nya sudah maju, tetapi masih di antrean kerja di memori, misalnya karena turn chat itu sedang berjalan, kehilangan isinya pada setiap restart: exit 75 karena migrasi (AD-01), restart `damnbot update`, graceful stop, maupun crash. Barisnya tetap `event_id NULL` dan tidak dapat diproses, karena platform tidak mengirim ulang (offset sudah di-commit). (2) Pesan bridge yang di-spool kehilangan isinya setelah ingest dan penghapusan file. (3) Klaim recovery di SELF-HEALING.md:432 tidak dapat diimplementasikan, bahkan untuk jendela milidetik antara dua COMMIT. Semua ini direproduksi di simulasi: 3 baris tanpa isi setelah exit 75 dan ingest. Spool AD-01 hanya menutup inbound yang "belum ter-commit", jadi tidak menutup kasus (1). [INFERENCE] Pesan antre baru di-append saat gilirannya, karena "di-queue" (SPEC.md:461) dan "antrean kerja in-memory" (SPEC.md:2822). Kasus (2) dan (3) berlaku dalam tafsiran apa pun. | Simpan isi di baris inbound: kolom `payload` (teks setelah redaksi PROV-01 + rujukan lampiran sebagai `blob_refs` owner baru `gateway_inbound`; dicakup `storage.encrypt`) diisi pada INSERT yang sama dengan dedupe/offset. Worker membaca isi dari DB, bukan dari memori. Ingest spool mengisi `payload` dan menghapus file hanya setelah COMMIT. `gwin_update_guard` diperluas: `payload` boleh → NULL **hanya** bersama/setelah `event_id` terisi (opsional, menghemat ruang). Purge tidak berubah: baris ber-`event_id` terhapus di langkah 11; baris antre milik chat ber-sesi `purging` membuka sesi baru (AA-03). Test: `TestInboundQueuedSurvivesRestart` (5 pesan antre saat turn berjalan → kill -9 / exit 75 / graceful stop → 5 `user_message` dengan isi asli setelah start) dan `TestInboundCrashBetweenCommits`. |
| AE-02 | MINOR | SPEC.md:2173-2176 (marker ditulis di `owned → requested`; syarat "tidak ada pendaftar lama" baru diperiksa di `draining → exclusive`), :2186 (`serve` lama keluar saat **melihat** marker `migrate`), :2187 (migrator berhenti setelah 30 s bila TUI lama hidup), DECISIONS.md:1060 ("(+) … tidak lagi membekukan `serve`") | `serve` lama keluar (exit 75) begitu marker muncul, meskipun migrasi kemudian dibatalkan karena ada TUI/ACP lama yang masih hidup. `serve` baru yang dijalankan supervisor juga harus bermigrasi dan ikut tertahan TUI lama. Perilakunya tidak ditetapkan (kode exit, notifikasi), sehingga hasilnya restart berulang. Gateway, cron, dan kanban mati selama TUI lama terbuka. Bila migrator adalah `serve` sendiri (tanpa user di depan layar), outage itu senyap, karena notifikasi lewat outbox tidak bisa ditulis sebelum migrasi. | Migrator memeriksa `maint.holders` **sebelum** menulis marker. Bila ada pendaftar lama non-`serve` dan tanpa `--force-migrate`, migrasi ditolak seketika tanpa marker, dan `serve` lama tetap melayani di skema lama. Marker `migrate` hanya ditulis bila migrasi pasti berjalan. `serve` sebagai migrator yang tertahan: exit 75 dengan backoff, tidak dihitung ke crash-loop, dan notifikasi owner lewat jalur non-DB (seperti `db_write_stalled`). Opsional: `serve` memakai `--force-migrate` otomatis setelah N menit, karena TUI lama hanya menjadi read-only. Test: `TestMigrationRefusedKeepsOldServe`. |
| AE-03 | MINOR | SPEC.md:2186 (spool: tulis, `fsync`, exit 75; nama `<platform>-<ts>.jsonl`), DECISIONS.md:1035 (ack hanya setelah masuk spool), SPEC.md:1018 (`storage.encrypt` mengenkripsi konten + blob), docs/SELF-HEALING.md:57 (graceful stop) | (1) Urutan "ack platform hanya setelah `fsync` baris spool" tidak normatif di SPEC. (2) Spool menyimpan payload mentah **plaintext**, melewati `storage.encrypt`, dan mode file tidak ditetapkan. (3) Urutan antarfile bergantung pada `<ts>` jam dinding. (4) Spool hanya dipakai saat exit karena migrasi, padahal inbound push yang tidak dikirim ulang platform punya risiko yang sama pada graceful stop (exit 0) dan restart `damnbot update` (exit 75). | Normatifkan urutan: baris spool → `fsync` → ack. File `0600`, dienkripsi dengan kunci `storage.encrypt` bila aktif. Nama file memakai nomor urut monotonik per proses. Satu aturan untuk semua exit terencana (graceful stop, update, migrasi). Bila AE-01 diterapkan, spool tidak lagi perlu memuat inbound yang sudah ter-commit. |
| AE-04 | MINOR | SPEC.md:2253 ("Retry dengan key sama saat permintaan masih menunggu → menempel ke permintaan yang sama; setelah delivery dibuat → mengembalikan status delivery yang ada") | Retry dengan `idempotency_key` sama tetapi argumen berbeda (target/teks) diam-diam menempel ke permintaan lama, sehingga isi baru dibuang tanpa error. Praktik umum idempotency key adalah menolak ketidakcocokan. | Simpan `args_sha` bersama `client_key`. Key sama dengan `args_sha` berbeda → `invalid_args` dengan `reason: "idempotency_mismatch"`, tanpa menempel. Tambahkan kasus ke `TestMCPServeMessagesSendIdempotent`. |
| AE-05 | MINOR | SPEC.md:2189 (SIGSTOP ke group tool saat SIGTSTP), DECISIONS.md:1038 ("Proses mati saat ter-stop → group ikut dibunuh lewat kill-on-close/pgid"), docs/SELF-HEALING.md:58 | Bila TUI di-kill (SIGKILL/OOM) saat process group tool ter-SIGSTOP, POSIX mengirim SIGHUP lalu SIGCONT ke process group yatim yang ter-stop. Proses yang mengabaikan SIGHUP (`nohup`, daemon) **lanjut berjalan tanpa pengawas**, sementara pengambil alih menjalankan recovery. "Kill-on-close" hanya ada di Windows Job Object; Linux/macOS tidak punya padanan untuk pgid [INFERENCE]. | Catat `pgid` + cap start leader di `turn_journal` saat dispatch `exec`. Pengambil alih di host yang sama mengirim SIGKILL ke pgid itu (setelah verifikasi cap start) sebelum recovery. Linux: opsional jalankan tool di cgroup/scope sendiri, lalu `cgroup.kill`. Nyatakan residunya untuk macOS. |
| AE-06 | MINOR | SPEC.md:2340 (#55: "divalidasi P3 + klasifikasi efek ulang; preview pasca-edit dicatat; penjawab = owner/pemilik sesi yang sah") ↔ :2248 dan :2239; SPEC.md:2352 (#67 "Dua `serve` lama …") | Teks #55 masih mendeskripsikan perilaku ADR-044: edit dieksekusi setelah validasi, dan pemilik sesi boleh menjawab kelas berbahaya. Ini bertentangan dengan protokol baru dan akan menggagalkan `TestConcurrencyDocRefs`. Kalimat #67 rancu. | #55 → "edit jarak jauh → permintaan baru + preview; kelas berbahaya hanya owner; `mcp_serve` tidak bisa `edit`". #67 → "`serve` dijalankan ulang supervisor tetapi binary di disk masih lama → exit 78". |

### Skenario yang terlewat dari tabel 68 skenario (SPEC.md:2284-2353)

| # | Skenario | Status | Temuan |
|---:|---|---|---|
| 69 | Pesan Telegram ter-commit dan antre (turn chat berjalan), lalu `serve` exit 75 / update / crash | isi hilang, baris `event_id NULL` tidak dapat diproses | AE-01 |
| 70 | Pesan bridge di-spool, di-ingest, file dihapus, lalu worker memproses | isi tidak ada di DB | AE-01 |
| 71 | Migrasi dibatalkan karena TUI lama hidup, padahal `serve` lama sudah exit 75 | gateway mati sampai TUI ditutup | AE-02 |
| 72 | Crash saat spool sebelum ack, atau graceful stop dengan inbound push yang belum ter-commit | aturan ack/spool tidak normatif | AE-03 |
| 73 | `messages_send` di-retry dengan key sama dan argumen berbeda | argumen baru dibuang diam | AE-04 |
| 74 | TUI di-SIGKILL saat group tool ter-SIGSTOP | proses `nohup` lanjut tanpa pengawas | AE-05 |

### Jumlah dan verdict

ID diverifikasi: OK **7** (AD-02..AD-08 + AC-03) · PARSIAL **1** (AD-01) · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **1** (AE-01) · MINOR **5** (AE-02..AE-06).

MAJOR terbuka untuk kriteria freeze: **1** (AE-01). BLOCKER terbuka: 0.

**Freeze sah: TIDAK.** ADR-045 menutup AD-02..AD-08 dan residu AC-03 dengan benar. Klaimnya terbukti: DDL 0 error, 88 = 36 + 39 + 13, 49 FK, guard `approval_not_pending`, CHECK `mcp_serve`, dan fail-closed `deny`. Protokol approval baru lolos review adversarial: dua approval berurutan, jawaban terlambat, duplikat, tidak sah, edit tidak valid, edit eskalasi, dan crash konsumen. Residu #67/#68 dan pengecualian jam dinding `expires_at` jujur.

AD-01 hanya tertutup sebagian. Spool dan exit 75 mengandalkan `gateway_inbound` sebagai titik durabilitas, padahal tabel itu tidak menyimpan isi pesan. Akibatnya pesan yang antre dan pesan yang di-spool kehilangan isinya pada setiap restart, bertentangan dengan P22 dan dengan test AD-01 sendiri ("0 pesan inbound hilang"). Cacat ini sebenarnya sudah ada sejak SELF-HEALING §5.1, tetapi baru terlihat karena spool bergantung padanya.

Perbaikannya: +1 kolom `payload` + 1 owner `blob_refs`, worker membaca isi dari DB, dan 2 test, tanpa perubahan token, LOC, atau dependency. Setelah AE-01 ditutup lewat ADR dan lolos verifikasi titik, kriteria freeze terpenuhi; AE-02..AE-06 masuk backlog ADR, dan AE-02 sebaiknya ikut ditutup bersama AE-01.

### Perintah reproduksi

```bash
# read-only: DDL §11 → /tmp, hitung trigger/FK, cek kolom gateway_inbound
PYTHONDONTWRITEBYTECODE=1 python3 - <<'EOF'
import os, sqlite3
L = open("SPEC.md").read().split("\n")
end = next(i for i, l in enumerate(L, 1) if l.startswith("## 12."))
blocks, cur = [], None
for i, l in enumerate(L, 1):
    if not 1011 <= i <= end: continue
    if l.strip() == "```sql": cur = []; continue
    if cur is not None and l.strip() == "```": blocks.append("\n".join(cur)); cur = None; continue
    if cur is not None: cur.append(l)
db = "/tmp/adr045v.db"
if os.path.exists(db): os.remove(db)
st = {"maint": "", "tok": {}}
c = sqlite3.connect(db, isolation_level=None)
c.create_function("dmb_fts_norm", 1, lambda t: t, deterministic=True)
c.create_function("dmb_maint", 0, lambda: st["maint"])
c.create_function("dmb_lease_token", 1, lambda s: st["tok"].get(s))
c.execute("PRAGMA foreign_keys=ON")
for b in blocks: c.executescript(b)
t = c.execute("select name, sql from sqlite_master where type='trigger'").fetchall()
g = [n for n, s in t if n.startswith("guard_")]
p9 = [n for n, s in t if "IS NOT 'purge'" in s and not n.startswith("guard_")]
fk = sum(len(c.execute(f"pragma foreign_key_list('{n}')").fetchall())
         for (n,) in c.execute("select name from sqlite_master where type='table'"))
print(len(blocks), len(t), len(g), len(p9), fk)                 # 8 88 36 39 49
print([r[1] for r in c.execute("pragma table_info(gateway_inbound)")])  # tanpa kolom isi
EOF
```

## §Verifikasi ADR-046 (2026-10-18)

Bagian ini memverifikasi ADR-046 (docs/DECISIONS.md:1067-1138), yang mengklaim menutup AE-01 (MAJOR), AE-02..AE-06 (MINOR), residu AD-01, dan skenario #69–#74 (+ #75–#78 baru) tanpa mekanisme baru. Pemeriksaan dilakukan langsung oleh reviewer, tanpa subagen. Dokumen lain read-only. Cakupannya:

- reproduksi DDL + uji fungsional read-only di `/tmp/adr046v.db`;
- simulasi end-to-end poller → DB → "restart" → worker → event;
- review adversarial jalur inbound (SPEC.md:1455-1476, :1617, :2201, :2229; docs/SELF-HEALING.md:414-444) dan migrasi (SPEC.md:2178-2204).

Kriteria freeze: 0 BLOCKER, 0 MAJOR terbuka. Residu yang diakui + bermitigasi bukan MAJOR.

### Cek mekanis dan reproduksi

| Cek | Hasil |
|---|---|
| Diagram mermaid | 8 blok, 0 gagal |
| Link relatif markdown | 179 link, 0 rusak |
| Eksekusi DDL | 8 blok `sql` §11 (SPEC.md:1032-2093) di SQLite **3.53.4**: **0 error** |
| Trigger / FK / cakupan guard | **88** = 36 guard + 39 P9 + 13 lain; **49** FK; satu-satunya kolom tidak tercakup adalah pengecualian tertutup `kanban_task_events.task_id`. Cocok dengan DECISIONS.md:1115. |
| DDL `turn_journal` SPEC vs SELF-HEALING | identik setelah normalisasi komentar/spasi (termasuk `tool_pgid`, `tool_pgid_start`) |
| Uji fungsional inbound DB | **21/21** sesuai ekspektasi. Daftar 17 uji penulis tidak tersedia, jadi saya menyusun kasus setara. **INSERT biasa**: tanpa `payload` → CHECK; JSON rusak → CHECK. **Duplikat** → diserap, isi asli tetap. **`blob_refs`** owner `gateway_inbound` (`__system__`) diterima. **`gwin_update_guard`**: `event_id` tanpa NULL-kan `payload`, ubah `payload` saja, `payload`→NULL saja, dan UPDATE kedua → ditolak; `event_id` + `payload` NULL → lolos. **DELETE** ditolak di `''`, lolos di `'purge'`. **Worker ke sesi berlabel** → `session_purging`, baris tetap antre beserta isi. **Query worker** memakai `idx_gw_inbound_queue` (EXPLAIN). **`inbound_rejected`** (event `__system__` + `payload` NULL) lolos. `foreign_key_check` = 0. Dua temuan perilaku: **`INSERT OR IGNORE`** (pseudo-code SELF-HEALING.md:421) tanpa `payload` atau dengan ciphertext non-JSON → **0 baris, tanpa error** (AF-01); tie `received_at` → `'u:1000'` diproses sebelum `'u:999'` (AF-03). |
| Simulasi end-to-end | Poller: 5 pesan + offset dalam satu COMMIT. "Restart" (memori dibuang) → worker pemegang lease membaca antrean dari DB → 5 `user_message` dengan isi asli, urut, setiap UPDATE 1 baris → 0 `payload` tersisa → `foreign_key_check` = 0. AE-01 terbukti tertutup. |

### Status per ID

| ID | Status terverifikasi | Bukti file:line | Catatan |
|---|---|---|---|
| AE-01 | OK | SPEC.md:1455-1466 (`payload` JSON + CHECK "baris antre selalu membawa isi"), :1468 (`idx_gw_inbound_queue`), :1471-1475 (guard UPDATE diperketat), :1617 (owner `gateway_inbound` di `blob_refs`, hidup selama antre), :1759 (`owner_kind`), :2229 (penulis poller), docs/SELF-HEALING.md:416-442 (poller/worker/status turunan; antrean dari DB; spool hanya jembatan sampai COMMIT), docs/CROSS-CUTTING.md:22 (`storage.encrypt` mencakup `payload` + spool), SPEC.md:2368-2369 dan :2374-2376 (#69, #70, #75, #76) | Isi pesan kini durable dari COMMIT poller sampai `user_message`. Direproduksi end-to-end. Sisa celah ada di AF-01..AF-05. |
| AE-02 | OK | SPEC.md:2197 (cek pendaftar **sebelum** marker; ditolak tanpa marker; `serve` migrator exit 75 berbackoff + notifikasi non-DB + `--force-migrate` otomatis 10 menit), :2198 (`serve` lama drain dan exit 75 hanya setelah migrasi COMMIT; batal → lanjut melayani), :2187 (cek diulang di `draining`), :2200, :2370 dan :2377 (#71, #78), docs/SELF-HEALING.md:53 | Urutan AE-02 tertutup. Persistensi "tertahan 10 menit" lintas restart tidak ditetapkan (AF-08). |
| AE-03 | OK | SPEC.md:2201 (urutan tulis → `fsync` (+dir) → ack; `0600`/`0700`; AEAD per baris; nama `<boot_id>-<pid>-<owner_start>-<seq>`; ingest → COMMIT → hapus; semua exit terencana), :2371 (#72) | Cakupan dipersempit ke "push yang wajib di-ack" (AF-06). |
| AE-04 | OK | SPEC.md:2267 (`args_sha`; key sama + argumen beda → `idempotency_conflict`), :521, :2372 (#73) | — |
| AE-05 | OK | SPEC.md:1696-1697 (`tool_pgid`, `tool_pgid_start`), :2147 (SIGKILL pgid oleh pengambil alih sebelum recovery; `cgroup.kill` di Linux; Job Object di Windows; residu macOS), docs/SELF-HEALING.md:352, SPEC.md:2373 (#74), docs/TRUST-MODEL.md | Syarat "leader masih ada" menggagalkan kasus #74 itu sendiri bila tidak ada cgroup (AF-07). |
| AE-06 | OK | SPEC.md:2354 (#55), :2366 (#67), :2296 (label "hasil review ADR-046") | — |
| AD-01 (residu) | OK | AE-01 + AE-02 di atas | Upgrade lewat package manager: `serve` lama tetap melayani bila migrasi ditolak; setelah COMMIT, exit 75 → binary baru → antrean `queued` utuh. |

### Review adversarial jalur inbound dan migrasi (diminta khusus)

| Tahap | Kasus | Hasil | Temuan |
|---|---|---|---|
| Poller (long-poll) | Crash sebelum COMMIT | aman: offset tidak maju → dikirim ulang → dedupe PK | — |
| Poller | Normalisasi/redaksi/enkripsi menghasilkan `payload` NULL atau non-JSON | **hilang diam**: `INSERT OR IGNORE` mengabaikan pelanggaran CHECK/NOT NULL, lalu offset tetap maju di transaksi yang sama (direproduksi) | AF-01 |
| Poller | Unduh lampiran gagal | tidak ditetapkan: pesan menahan offset semua chat, atau ter-commit tanpa lampiran | AF-01 |
| Webhook/bridge | DB tidak bisa ditulis / exit terencana | aman untuk push yang wajib di-ack: spool → `fsync` → ack; baris terpotong tidak di-ack | — |
| Push tanpa ack/redelivery (F7: Discord Gateway WS, IRC, Mattermost WS) | DB tidak bisa ditulis / exit terencana | **tidak tercakup** spool | AF-06 |
| Spool → DB | Ingest ganda, crash antara COMMIT dan hapus file | aman: dedupe PK; file dihapus setelah COMMIT | — |
| DB antre | Crash/kill -9/exit 75/graceful stop setelah COMMIT poller | aman: baris `queued` beserta isi (direproduksi) | — |
| Worker | Urutan per chat | `ORDER BY received_at` (jam dinding) `, external_id` (TEXT): tie → urutan leksikografis; step jam → urutan terbalik | AF-03 |
| Worker | Sesi chat di-lease proses lain (mis. sesi gateway dibuka di TUI) | tidak ditetapkan: antrean menunggu tanpa balasan; overflow REL-11 butuh lease untuk mencatat `inbound_rejected` | AF-04 |
| Worker → event | Crash/drain setelah COMMIT worker (`user_message`, `event_id` terisi) sebelum `turn_start` | **pesan tak terjawab**: status turunan "done" (journal tidak ada); tabel recovery berbasis journal tidak melihatnya | AF-02 |
| Worker → event | Worker basi / sesi dilabel | aman: guard F/P (direproduksi, #76) | — |
| Purge | Lampiran inbound yang sudah diproses | ref `blob_refs` owner `gateway_inbound` (`__system__`, append-only) tidak dihapus purge, sehingga langkah 25 tidak menghapus blob; file tersisa sampai gc | AF-05 |
| Migrasi | TUI lama hidup | aman: ditolak tanpa marker, `serve` lama tetap melayani | — |
| Migrasi | Hanya `serve` lama hidup | aman: drain → COMMIT → `schema_newer` → spool push → exit 75 → binary baru | — |
| Migrasi | Pendaftar lama muncul di antara cek dan marker | aman: dicek ulang di `draining` → timeout → batal → `serve` lama lanjut | — |
| Migrasi | `serve` sebagai migrator tertahan | exit 75 berbackoff + notifikasi non-DB; timer 10 menit tidak dipersist lintas restart | AF-08 |

### Temuan baru

| ID | Severity | Lokasi | Temuan | Usulan perbaikan konkret |
|---|---|---|---|---|
| AF-01 | MINOR | docs/SELF-HEALING.md:421 (`INSERT OR IGNORE INTO gateway_inbound … payload = redact(normalize(u))`), :424-425 (offset di-UPSERT di transaksi yang sama), :442 dan SPEC.md:2201 (ingest spool "dedupe PK"), SPEC.md:1462-1466 (CHECK `json_valid(payload)` dan "baris antre selalu membawa isi"), docs/CROSS-CUTTING.md:22 (`payload` dienkripsi XChaCha20-Poly1305; format penyimpanan tidak ditetapkan), DECISIONS.md:1116 ("INSERT antre tanpa `payload` ditolak CHECK") | Di SQLite, `OR IGNORE` berlaku juga untuk CHECK dan NOT NULL, tidak hanya PK. Baris yang melanggar CHECK **dilewati tanpa error**, sementara offset tetap di-commit. Pesan hilang diam-diam (direproduksi: 0 baris, tanpa error). Pemicunya bisa bug normalisasi/redaksi, atau implementasi `storage.encrypt` yang menyimpan ciphertext/base64 apa adanya (bukan nilai JSON), yang bahkan membuat **semua** inbound hilang saat enkripsi aktif. Klaim uji ADR "ditolak CHECK" hanya benar untuk INSERT biasa. Kebijakan saat unduh lampiran gagal juga tidak ditetapkan. | Ganti dengan `INSERT … ON CONFLICT(platform, external_id) DO NOTHING` (dedupe tetap; CHECK/NOT NULL tetap error → rollback → offset tidak maju → `inbound_invalid` dilaporkan). Ini berlaku juga untuk ingest spool dan `blob_refs`. Tetapkan envelope enkripsi kolom ber-CHECK JSON sebagai nilai JSON (`{"v":1,"n":…,"c":…}`). Lampiran gagal diunduh → baris tetap di-commit dengan `attachments[].state = "pending"` + `file_id`, lalu diunduh worker. Test: `TestInboundInvalidPayloadNotSilent`, `TestInboundEncryptedRoundTrip`. |
| AF-02 | MINOR | docs/SELF-HEALING.md:429-437 (transaksi worker: `user_message` + UPDATE inbound), :337 (`turn_start` = INSERT journal di transaksinya sendiri), :341 (journal dihapus saat `turn_end`), :440 (status `done` = `event_id` terisi dan journal "tertutup"), SPEC.md:2186 (drain membatalkan turn tanpa menulis) | Antara COMMIT worker dan COMMIT `turn_start` ada jendela waktu. Bila crash, kill -9, atau drain barrier/graceful stop jatuh di jendela itu, `user_message` sudah tercatat tetapi tidak ada journal, sehingga status turunannya "done" dan recovery (yang berbasis journal) tidak melihatnya. Pesan tidak pernah dijawab. Klaim "(+) Tidak ada pesan masuk yang hilang pada restart apa pun setelah COMMIT" (DECISIONS.md:1133) tidak berlaku untuk jawaban. | Masukkan `turn_start` + INSERT journal `running` ke transaksi worker yang sama, sesuai prinsip "journal ikut transaksi event yang sudah ada". Alternatif: aturan recovery "sesi gateway yang event terakhirnya `user_message` ber-`gateway_inbound` tanpa `turn_start` sesudahnya → mulai turn". Tambahkan kasus ke `TestInboundCrashBetweenCommits` (kill setelah COMMIT worker). |
| AF-03 | MINOR | docs/SELF-HEALING.md:431-432 dan SPEC.md:1468 (`ORDER BY received_at, external_id`; `received_at` jam dinding, `external_id` TEXT) | Urutan per chat bergantung pada jam dinding (step mundur membalik urutan) dan perbandingan teks id (`'u:1000'` < `'u:999'`, direproduksi untuk satu batch getUpdates dengan `received_at` sama). | Urutkan dengan nomor urut monotonik penerimaan per platform (mis. kolom `recv_seq` dari `module_state('gateway','recv_seq')` di transaksi INSERT, juga diisi saat ingest spool sesuai urutan baris), atau `rowid`. `received_at` hanya untuk tampilan dan retensi. |
| AF-04 | MINOR | docs/SELF-HEALING.md:440 ("Pekerja = pemegang lease sesi chat"; batas REL-11 dari baris `queued`), :434 (`inbound_rejected` ditulis worker), SPEC.md:2846 (REL-11: overflow selalu dibalas), :120 (P22: 0 pesan dibuang diam saat sibuk) | Bila sesi chat di-lease proses lain, misalnya user membuka sesi gateway di TUI, worker tidak bisa mengambil lease. Antrean menumpuk tanpa balasan ke pengirim. Penolakan overflow juga tidak bisa dicatat, karena butuh lease. Jumlah baris `queued` bertambah tanpa batas. | Bila lease sesi chat dipegang proses lain, kirim satu balasan `__system__` per chat per kejadian (delivery tanpa `event_id`, tanpa lease), misalnya "sesi sedang dipakai di TUI; pesan antre N". Keputusan overflow (hitung `queued` > batas) dibuat poller dengan balasan `__system__` yang sama. Baris overflow tetap disimpan dan ditandai `inbound_rejected` oleh pekerja berikutnya. |
| AF-05 | MINOR | SPEC.md:1617 (ref owner `gateway_inbound` di `__system__`), :1652 (purge langkah 11 hanya DELETE baris `gateway_inbound`), :1666 (langkah 25 hanya untuk sha tanpa baris `blob_refs`), :1669 (langkah 28(b) hanya `session_id ∈ S_b`); docs/SELF-HEALING.md:434 (`inbound_rejected` "isi tetap tercatat", sesi tidak ditetapkan) | Lampiran pesan masuk punya baris `blob_refs` owner `gateway_inbound` di `__system__` yang append-only dan tidak pernah dihapus purge. Karena itu langkah 25 tidak menghapus blob lampiran sesi ter-purge. File baru hilang saat gc berikutnya, dan angka "blob yang akan dihapus" di preview salah. Isi pesan yang ditolak overflow tercatat di sesi yang tidak ditetapkan; bila di `__system__`, isinya bertahan setelah purge. | Purge langkah 11 juga menghapus `blob_refs` owner `gateway_inbound` dengan `owner_id` baris yang dihapus, sebelum langkah 25. `inbound_rejected` dicatat di sesi chat, sehingga ikut purge. Tambahkan kasus ke `TestPurgeLeavesNoSessionRef`/`TestRestoreMissingBlob` (lampiran inbound ter-purge → file masuk `.trash` di transaksi purge). |
| AF-06 | MINOR | SPEC.md:2201 (spool hanya untuk "push yang wajib di-ack platform"), DECISIONS.md:1088 ↔ SPEC.md:907 (platform F7: Discord Gateway WS, Slack Socket Mode, Mattermost, IRC lewat SEC-10, …) | Aliran push tanpa ack dan tanpa redelivery (Discord Gateway WS, IRC, websocket Mattermost) tidak tercakup. Event yang tiba saat DB tidak bisa ditulis atau saat exit terencana hanya ada di memori dan hilang. Rumusan ADR-045 ("tidak akan dikirim ulang platform") sebenarnya lebih tepat. | Cakupan spool = setiap inbound yang **tidak akan dikirim ulang** platform bila tidak di-commit, baik yang wajib di-ack maupun tanpa ack. Platform yang punya API riwayat memakai `gateway_cursors` untuk backfill sejak id terakhir yang ter-commit. Nyatakan residu per platform di TRUST-MODEL. |
| AF-07 | MINOR | SPEC.md:2147 (SIGKILL pgid "hanya bila leader masih ada dengan cap start yang sama"), :2373 (#74: TUI di-SIGKILL saat group ter-SIGSTOP → POSIX SIGHUP+SIGCONT ke group yatim) | Dalam skenario #74 sendiri, leader group (`sh -c …`) biasanya mati oleh SIGHUP, sementara anak `nohup` tetap hidup. Akibatnya syarat "leader masih ada" membuat pgid **tidak** dibunuh di Linux tanpa cgroup dan di macOS, padahal test `TestOrphanToolGroupKilled` mengharapkan sebaliknya. | Kirim SIGKILL ke pgid bila masih ada proses ber-`pgrp = tool_pgid` dengan cap start ≥ `tool_pgid_start` (pemindaian `/proc` / `proc_listpids`). Nomor PGID tidak dapat dipakai ulang selama group masih punya anggota [INFERENCE: semantik Linux/BSD], sehingga pemeriksaan anggota sudah cukup sebagai anti-reuse. |
| AF-08 | MINOR | SPEC.md:2197 (`serve` migrator: exit 75 backoff 30 s → … ≤ 300 s; "setelah tertahan 10 menit memakai `--force-migrate` otomatis"), DECISIONS.md:1085 dan :1136 | Setiap percobaan adalah proses baru yang menunggu ≤ 30 s. Tanpa cap waktu "tertahan sejak" yang dipersist, syarat 10 menit tidak pernah tercapai, atau bergantung pada implementasi. | Persist `run/migrate_blocked.json` (`{since_boot, boot_id, target_version}`, jam boot; reset saat `boot_id` atau target berubah). Paksaan berlaku bila `now_boot − since_boot ≥ 600 s`. Tambahkan kasus restart berulang ke `TestServeMigratorForcesAfter10Min`. |

### Skenario yang terlewat dari tabel 78 skenario (SPEC.md:2298-2377)

| # | Skenario | Status | Temuan |
|---:|---|---|---|
| 79 | `normalize`/`redact`/enkripsi menghasilkan `payload` tidak valid | hilang diam (`OR IGNORE`) | AF-01 |
| 80 | Drain/crash setelah COMMIT worker, sebelum `turn_start` | pesan tak terjawab | AF-02 |
| 81 | Dua update dalam satu batch dengan digit id berbeda; step jam mundur | urutan per chat terbalik | AF-03 |
| 82 | Sesi gateway sedang dibuka di TUI saat pesan masuk | antre tanpa balasan, tanpa batas | AF-04 |
| 83 | Purge sesi dengan lampiran inbound | file lampiran tersisa sampai gc | AF-05 |
| 84 | Event Discord WS tiba saat drain barrier | hilang dari memori | AF-06 |
| 85 | #74 tanpa cgroup: leader mati oleh SIGHUP, anak `nohup` hidup | group tidak dibunuh | AF-07 |
| 86 | `serve` migrator di-restart berulang oleh supervisor | timer 10 menit tidak terakumulasi | AF-08 |

### Jumlah dan verdict

ID diverifikasi: OK **7** (AE-01..AE-06, residu AD-01) · PARSIAL **0** · GAGAL **0**.

Temuan baru: BLOCKER **0** · MAJOR **0** · MINOR **8** (AF-01..AF-08).

MAJOR terbuka untuk kriteria freeze: **0**. BLOCKER terbuka: 0. Semua MAJOR dari putaran sebelumnya (B-, M-, N-, V-, W-, X-, Y-, Z-, AA-, AB-, AC-, AD-, AE-) sudah tertutup atau terverifikasi tertutup di bagian masing-masing.

**Freeze sah: YA.**

ADR-046 menutup AE-01: isi pesan masuk kini durable dari COMMIT poller sampai `user_message`, terbukti lewat DDL dan simulasi end-to-end. Ia juga menutup AE-02 (urutan migrasi: cek pendaftar sebelum marker, `serve` lama exit 75 hanya setelah COMMIT), AE-03..AE-06, dan residu AD-01, tanpa mekanisme baru. Klaim DDL terbukti: 88 = 36 + 39 + 13, 49 FK, guard `gwin_update_guard` yang diperketat, dan DDL journal SPEC = SELF-HEALING.

Review adversarial end-to-end tidak menemukan jalur yang melanggar P1–P29 dalam kondisi normal tanpa bug implementasi. Kedelapan MINOR adalah celah spesifikasi yang lokal; masing-masing punya perbaikan satu-dua kalimat/DDL kecil dan masuk backlog ADR. Saya sarankan dua di antaranya ditutup **sebelum implementasi gateway** (fase F2/F7), karena dampaknya pesan masuk hilang diam atau tak terjawab:

- **AF-01:** `OR IGNORE` → `ON CONFLICT DO NOTHING`, dan envelope JSON untuk kolom terenkripsi.
- **AF-02:** `turn_start` ikut transaksi worker.

Residu yang diakui (#37, #40, #48, #67, #68, macOS `setsid`, pengecualian jam dinding `expires_at`) jujur, bermitigasi, dan terdokumentasi di TRUST-MODEL.md.

### Perintah reproduksi

```bash
# read-only: DDL §11 → /tmp; hitung trigger/FK; tunjukkan OR IGNORE menelan CHECK
PYTHONDONTWRITEBYTECODE=1 python3 - <<'EOF'
import os, sqlite3
L = open("SPEC.md").read().split("\n")
end = next(i for i, l in enumerate(L, 1) if l.startswith("## 12."))
blocks, cur = [], None
for i, l in enumerate(L, 1):
    if not 1011 <= i <= end: continue
    if l.strip() == "```sql": cur = []; continue
    if cur is not None and l.strip() == "```": blocks.append("\n".join(cur)); cur = None; continue
    if cur is not None: cur.append(l)
db = "/tmp/adr046v.db"
if os.path.exists(db): os.remove(db)
c = sqlite3.connect(db, isolation_level=None)
c.create_function("dmb_fts_norm", 1, lambda t: t, deterministic=True)
c.create_function("dmb_maint", 0, lambda: "")
c.create_function("dmb_lease_token", 1, lambda s: None)
c.execute("PRAGMA foreign_keys=ON")
for b in blocks: c.executescript(b)
t = c.execute("select name, sql from sqlite_master where type='trigger'").fetchall()
g = [n for n, s in t if n.startswith("guard_")]
p9 = [n for n, s in t if "IS NOT 'purge'" in s and not n.startswith("guard_")]
fk = sum(len(c.execute(f"pragma foreign_key_list('{n}')").fetchall())
         for (n,) in c.execute("select name from sqlite_master where type='table'"))
print(len(blocks), len(t), len(g), len(p9), fk)                       # 8 88 36 39 49
c.execute("insert or ignore into gateway_inbound(platform,external_id,chat_id,sender_id,payload,received_at)"
          " values('telegram','u:1','c','u',NULL,1)")                   # tanpa error
print(c.execute("select count(*) from gateway_inbound").fetchone()[0]) # 0 → pesan hilang diam
EOF
```
