# Architecture Decision Records — Damn-Bot

Format: Konteks / Keputusan / Konsekuensi. Status semua ADR: **diterima** kecuali ditandai lain. Mengubah default perilaku atau invariant wajib ADR baru (lihat `docs/CROSS-CUTTING.md` §10).

---

## ADR-001 — Bahasa implementasi: Go

**Konteks.** Target: single static binary, startup ≤ 100 ms, RSS idle ≤ 50 MB, cross-compile Linux/macOS/Windows amd64+arm64. Toolchain terpasang: Go 1.26.8; Rust dan Bun tidak. Hermes (Python + Node) butuh 1,6–2,1 s hanya untuk import (BASELINE §6).

**Keputusan.** Go 1.26.x, `CGO_ENABLED=0` untuk binary utama.

**Konsekuensi.**
- (+) Cross-compile tanpa toolchain C; startup ~ms; concurrency (goroutine) cocok untuk tool paralel, gateway, streaming.
- (+) Satu toolchain untuk build dan runtime user (nol runtime eksternal).
- (−) Ekosistem ML/voice lokal lebih tipis dari Python → backend lokal dipanggil sebagai executable eksternal (SPEC §9.5).
- (−) Tree-sitter native memerlukan CGO → dipakai via WASM (ADR-015).
- (−) GC menambah RSS dasar ~5–10 MB vs Rust; masih di bawah target P18.

## ADR-002 — SQLite pure-Go (`modernc.org/sqlite`)

**Konteks.** Context DB butuh WAL, FTS5, transaksi; binary harus `CGO_ENABLED=0`.

**Keputusan.** `modernc.org/sqlite` (C SQLite yang ditranspilasi ke Go).

**Konsekuensi.**
- (+) Tanpa CGO; cross-compile semua target; FTS5 tersedia.
- (−) ~1,5–3× lebih lambat dari `mattn/go-sqlite3` (CGO) pada beban tulis berat [INFERENCE: angka umum komunitas]; probe ENGINEERING.md §4.3 (2.000 append + FTS, 3 run) tidak menunjukkan selisih berarti vs `ncruces/go-sqlite3`. Workload Damn-Bot (append pendek, query FTS) tetap dalam target `Store.Append` ≤ 10 ms p99 (`synchronous=FULL`, ADR-023).
- (−) Kontribusi binary ~6–8 MB [INFERENCE, diukur F2 dalam budget core 14 MB].
- Jalur keluar: interface `internal/ctxdb` mengisolasi driver; ganti driver = satu paket.

## ADR-003 — Provider: OpenAI-compatible di core, protokol lain sebagai modul lazy

**Konteks.** Hermes mendukung 40+ provider (`plugins/model-providers/*`). Mayoritas mengekspos endpoint OpenAI-compatible Chat Completions; beberapa tidak (Anthropic native caching, Bedrock, Vertex, Gemini, OpenAI Responses/Codex).

**Keputusan.** Core hanya mengimplementasi OpenAI-compatible Chat Completions + SSE. Protokol lain berada di modul `providers-extra` (lazy) dengan interface `provider.Client` yang sama. Preset provider berupa data (`providers.toml` embedded). Semua masuk rilis 1.0 (parity).

**Konsekuensi.**
- (+) Core kecil dan teruji dalam; satu jalur kode untuk OpenRouter, Ollama, llama.cpp, vLLM, LM Studio, DeepSeek, dsb.
- (+) Anthropic native memberi `cache_control` eksplisit di batas tier prompt → cache hit lebih tinggi untuk model Claude.
- (−) Lima adapter protokol tambahan untuk dipelihara; dimitigasi fixture SSE replay per protokol.

## ADR-004 — Transcript append-only, ringkasan sebagai index

**Konteks.** Hermes meringkas konteks dengan LLM, dapat meringkas ringkasan, dan compaction bisa fire berulang (`agent/context_compressor.py:2921-2932`); detail asli hilang dari konteks kerja ("pikun").

**Keputusan.** Tabel `events` append-only (trigger `RAISE(ABORT)` pada UPDATE/DELETE) dengan hash-chain per sesi. Ringkasan disimpan terpisah (`summaries`) dengan rentang `from_seq..to_seq`, tidak pernah menggantikan event, tidak pernah menjadi input ringkasan lain. Retrieval FTS5 atas event mentah. Fork/rewind = cabang baru tanpa menyalin event.

**Konsekuensi.**
- (+) Tidak ada kehilangan data; audit dan replay deterministik; `recall` bisa mengambil detail yang sudah keluar dari window.
- (−) DB tumbuh tanpa batas → `damnbot sessions purge` eksplisit + blob content-addressed (dedupe).
- (−) Data mutable (lease, outbox, status kanban) dipisah ke tabel yang ditandai mutable secara eksplisit.

## ADR-005 — Tanpa fuzzy repair (nama tool maupun pencocokan edit)

**Konteks.** Hermes memperbaiki nama tool salah dengan `difflib.get_close_matches(..., cutoff=0.7)` lalu mengeksekusinya (`agent/agent_runtime_helpers.py:2632-2679`); JSON rusak menjadi `{}` dan hasil tool sintetis. Pencocokan edit fuzzy dapat mengenai lokasi yang salah.

**Keputusan.** Nama tak dikenal → `unknown_tool`; argumen invalid → `invalid_args` dengan pelanggaran schema; edit hanya exact match + syarat unik + anchor hash (CODE-TOOLS.md §5). Tidak ada hasil tool atau history yang dibuat-buat. Paket edit-distance dilarang di jalur dispatch (lint).

**Konsekuensi.**
- (+) 0 eksekusi tool yang tidak diminta; 0 edit di lokasi salah.
- (−) Model lemah mendapat lebih banyak error dan butuh 1 iterasi tambahan untuk koreksi; diukur di SCORECARD (laju tool call invalid & iterasi per task). Error menyertakan `hint` dan kandidat agar koreksi tepat sekali.

## ADR-006 — Permukaan: TUI sendiri + server ACP (+ web UI, gateway)

**Konteks.** User ingin terminal bergaya tern dan dapat memakai Damn-Bot di dalam tern (`tern acp <agent>`). Hermes punya TUI Ink/React (`ui-tui/`), ACP adapter (`acp_adapter/`), desktop Electron, gateway chat.

**Keputusan.** Semua frontend memakai `internal/loop` yang sama lewat channel event + interface `Approver`: TUI bubbletea/lipgloss, `damnbot acp` (JSON-RPC stdio), web UI (ADR-011), gateway (SPEC §9.7), headless.

**Konsekuensi.**
- (+) Perilaku identik di semua permukaan; invariant diuji sekali di loop.
- (−) Lima frontend untuk dirawat; dibatasi budget LOC per modul (SPEC §3.2.4).
- Detail integrasi tern yang belum terverifikasi → OPEN-QUESTIONS Q1–Q4.

## ADR-007 — Karantina skill/memory tulisan agent

**Konteks.** Di hermes, skill tulisan agent dapat masuk prompt tanpa security scan (default off), dan background review default aktif (`hermes_cli/config_defaults.py:810`). Prompt injection persisten bisa ditanam lewat memory/skill.

**Keputusan.** Semua entri asal `agent`, `auto`, `hub`, atau `import` dengan provenance agent → `pending` → threat scan → hanya `approved` oleh user yang masuk prompt. Cap keras: memory 500 char/entry, 4.000 char/scope; skill 8.192 B. Status disimpan append-only (`memory_decisions`).

**Konsekuensi.**
- (+) Tidak ada jalur otomatis dari output model ke system prompt.
- (−) Butuh langkah review manual; dimitigasi `damnbot review --bulk`, notifikasi di TUI/web UI.

## ADR-008 — Validator JSON Schema subset milik sendiri

**Konteks.** P3 butuh validasi penuh untuk keyword yang dipakai; library JSON Schema lengkap menambah dependency dan ukuran untuk fitur yang tidak dipakai.

**Keputusan.** `internal/schema` mengimplementasi subset yang terdaftar di SPEC §4.4; schema internal dengan keyword lain gagal test; schema MCP pihak ketiga dinormalisasi dan keyword asing ditandai `schema_partial`.

**Konsekuensi.**
- (+) ±800 LOC, tanpa dependency, error path JSON presisi untuk model.
- (−) Tanggung jawab kebenaran validator milik kita → suite kasus resmi JSON-Schema-Test-Suite untuk keyword yang didukung dijalankan di CI.

## ADR-009 — Ekstensi = MCP server, hook executable, skill (tanpa plugin in-process)

**Konteks.** Plugin discovery hermes memakan 500–650 ms (`hermes_cli/main.py:2774,2817,3301`); plugin Python in-process dapat menjatuhkan atau memperlambat core. Go `plugin` tidak portabel (tanpa Windows, butuh CGO, versi toolchain harus identik).

**Keputusan.** Fitur parity dibangun sebagai modul built-in (ADR-013). Ekstensi pihak ketiga: (a) MCP server (tool), (b) hook executable (kebijakan/observasi, SPEC §4.10), (c) skill (pengetahuan). Kontrak versi: `damnbot.hook/v1`, MCP sesuai versi protokol yang dinegosiasi.

**Konsekuensi.**
- (+) Crash ekstensi tidak menjatuhkan core (proses terpisah, timeout); 0 ms discovery saat startup.
- (−) 647 manifest `plugin-catalog/` hermes yang berupa kode Python tidak jalan langsung; strategi di SCORECARD §4 dan OPEN-QUESTIONS Q18.

## ADR-010 — Budget ukuran binary 52 MB (full parity)

**Status:** Accepted 2026-10-17 (disetujui user).

**Konteks.** Target awal 30 MB ditetapkan sebelum scope parity penuh. Parity menambah 27 adapter platform, browser CDP, MCP, voice, code-intel (WASM runtime), WhatsApp Web (semula `whatsmeow`; dikeluarkan dari binary oleh ADR-034), Matrix E2EE.

**Keputusan.** P13: full build ≤ 52 MB stripped (linux/amd64), arsip rilis ≤ 22 MB, build `-tags minimal` (core + TUI + ACP + code tools) ≤ 22 MB. Budget per modul di SPEC §3.2.4; kontribusi modul diukur di CI dengan build `nomod_<id>`. Bentuk tag final: ENGINEERING E-39 (`-tags goolm,minimal`, `-tags goolm,nomod_<id>`; ADR-054 V-10).

**Konsekuensi.**
- (+) Masih satu file tanpa runtime; install hermes (venv Python + `node_modules` + Electron) diukur di F0 dan diperkirakan jauh lebih besar [INFERENCE sampai diukur F0].
- (−) 22 MB di atas target awal; user yang hanya butuh coding agent memakai build `minimal`.
- Revisi angka hanya lewat ADR baru dengan data ukur.

## ADR-011 — Web UI embedded menggantikan Electron

**Konteks.** Hermes desktop = Electron 40 (`apps/desktop/package.json`), 850.355 LOC di `apps/desktop` (BASELINE §1), ditambah dashboard `web/` (Vite/TS). Electron membawa Chromium sendiri (ratusan MB), toolchain Node untuk build, update terpisah.

**Keputusan.** `damnbot serve --web` menyajikan web UI dari `embed.FS`: Go `html/template` + SSE + ES module vanilla JS tanpa bundler/framework (≤ 1,5 MB aset). Desktop feel: `damnbot serve open --app` (Chromium/Edge mode app bila ada). Wrapper native opsional `damnbot-desktop` (OS webview, CGO) sebagai binary terpisah dengan `desktop/go.mod` sendiri: di luar E-39 (pure Go), E-45 (budget dependency), dan `make size-check` binary utama; punya job CI sendiri (m-25).

**Konsekuensi.**

| Aspek | Electron (hermes) | Web UI embedded (Damn-Bot) |
|---|---|---|
| Ukuran tambahan | Chromium bundel, ratusan MB [diukur F0] | ≤ 1,5 MB di binary yang sama |
| Toolchain | Node + Electron builder | tidak ada (Go saja) |
| Update | kanal terpisah | ikut binary (`damnbot update`) |
| Akses remote | tidak native | ya (loopback default, remote opt-in + auth) |
| Integrasi OS (tray, notifikasi native, global shortcut) | penuh | terbatas di browser; wrapper opsional menutup sebagian |
| Offline | ya | ya (localhost) |

- (−) Tanpa framework, UI kompleks (kanban, diff viewer) ditulis manual → budget 10.000 LOC JS/CSS (ADR-033); komponen dibatasi.
- (−) Fitur khusus Electron yang tidak bisa dipetakan dicatat `replaced` di FEATURE-PARITY.md dengan padanan.

## ADR-012 — Tool deferred lewat `tool_search` + `tool_invoke`, array `tools` konstan

**Konteks.** Hermes mengirim 19 schema (37.758 B, ~9.440 tok) per call dari 88 terdaftar, dengan 14 di-defer via `tool_search` (BASELINE §2). Menambah schema ke array `tools` di tengah sesi mengubah prefix request → cache miss.

**Keputusan.** Array `tools` = 10 schema eager, konstan sepanjang sesi. Tool lain: katalog satu baris per grup (≤ 600 tok); `tool_search` mengembalikan schema lengkap sebagai tool result (di-pin ≤ 3.000 tok, LRU); `tool_invoke{name,args}` memvalidasi `args` terhadap schema target; schema belum dimuat → `not_loaded`.

**Konsekuensi.**
- (+) P1 konstan berapa pun jumlah modul/MCP server; prefix cache utuh.
- (−) Tool deferred tidak mendapat constrained decoding native provider pada `args` → dikompensasi validasi penuh + error presisi; laju `invalid_args` tool deferred diukur di SCORECARD.
- (−) Satu round-trip tambahan pada pemakaian pertama tool deferred per sesi.

## ADR-013 — Parity penuh di 1.0 lewat modul lazy

**Konteks.** User menetapkan seluruh fitur hermes harus ada di 1.0, tanpa mengorbankan ringan, hemat token, dan stabil.

**Keputusan.** Core kecil + modul fitur built-in yang dikompilasi ke binary yang sama tetapi lazy (SPEC §3.2): registrasi statis tanpa efek samping, start pada pemakaian pertama, idle stop, kegagalan terisolasi. Fitur otomatis default OFF dengan budget token. Gate 1.0 = 100% baris FEATURE-PARITY.md berstatus selain `dropped-with-approval` + SCORECARD menang.

**Konsekuensi.**
- (+) Plug-and-play: tidak ada `pip install extras`; fitur aktif lewat satu section config.
- (+) P1/P12/P16 diuji dengan **semua** modul enabled, jadi parity tidak bisa diam-diam menaikkan biaya dasar.
- (−) Ukuran binary naik (ADR-010); LOC total ≤ 190.000 (SPEC P21, ADR-033).
- (−) Jadwal lebih panjang: 10 fase F0–F9 (ROADMAP).

## ADR-014 — Engine grep/glob sendiri; `rg` tidak dipakai sebagai akselerator

**Konteks.** Hermes menjalankan ripgrep yang di-pin dengan fallback `find`/`grep`, resolusi path `rg` khusus per OS, dan parsing output teks (`tools/file_operations_search.py:122-339`). Hasil bergantung versi `rg` dan shell transport.

**Keputusan.** Engine Go sendiri (walker paralel, gitignore, literal fast-path, RE2) — CODE-TOOLS.md §2. `rg` di PATH tidak dipakai, termasuk sebagai akselerator opsional.

**Konsekuensi.**
- (+) Hasil deterministik lintas OS/versi; output terstruktur tanpa parsing teks; tidak ada proses spawn per pencarian.
- (−) Harus mencapai target ≤ 1,2× waktu `rg` (literal) dan ≤ 1,5× (regex) pada korpus benchmark; gagal target = gate CI merah, bukan alasan memakai `rg`.
- Alasan menolak mode "akselerator identik": menjamin identik butuh menjalankan kedua engine (menghapus manfaat) atau menerima divergensi (melanggar determinisme).

## ADR-015 — Code intelligence: `go/ast` + tree-sitter via WASM (wazero) + LSP client

**Konteks.** Repo map, outline file besar, dan edit berbasis simbol butuh parser multi-bahasa. Binding tree-sitter native butuh CGO.

**Keputusan.** Go: `go/parser`/`go/ast` (stdlib). Bahasa lain: grammar tree-sitter dikompilasi ke WASM, dijalankan dengan `github.com/tetratelabs/wazero` (pure Go). Grammar tidak di-embed di build default: diunduh saat pertama dipakai dari rilis Damn-Bot yang di-pin SHA-256 ke cache, atau `damnbot doctor grammars install --offline <bundle.tar.gz>` (stdlib, tanpa zstd); build `-tags grammars_embedded` menyertakan 12 grammar populer (bentuk tag final: ENGINEERING E-39, `-tags goolm,grammars_embedded`; ADR-054 V-10). Tanpa grammar → outline heuristik regex. LSP client (diagnostics, definition, references, rename) sebagai modul lazy memakai language server yang terpasang.

**Konsekuensi.**
- (+) `CGO_ENABLED=0` tetap; modul code-intel ≤ 3 MB di binary default.
- (−) WASM ~2–5× lebih lambat dari native [INFERENCE]; outline di-cache per (path, mtime, size, hash) dan diinvalidasi fsnotify.
- (−) Pemakaian pertama bahasa non-Go butuh jaringan atau bundle offline (OPEN-QUESTIONS Q26).

## ADR-016 — Eksekusi terminal: PTY pure-Go + process group wajib

**Konteks.** Perintah interaktif/berwarna butuh PTY; perintah batch lebih cepat dan bersih tanpa PTY. Proses yatim dan hang adalah sumber ketidakstabilan agent (EXEC-AND-CODING.md §0).

**Keputusan.** Unix: `github.com/creack/pty` (tanpa CGO) + `Setpgid`; kill = `kill(-pgid)`. Windows: ConPTY via `golang.org/x/sys/windows` + Job Object `KILL_ON_JOB_CLOSE`. Mode PTY/non-PTY dipilih otomatis (EXEC-AND-CODING.md §A.1).

**Konsekuensi.**
- (+) 0 proses yatim sebagai invariant yang dapat diuji.
- (−) Dua implementasi per OS; matrix CI Windows wajib untuk paket `exec`.

## ADR-017 — Indeks trigram hanya untuk repo besar

**Konteks.** Grep walk penuh cukup cepat untuk repo kecil/menengah; indeks menambah RAM, disk, dan kompleksitas invalidasi.

**Keputusan.** Indeks trigram dibangun lazy di background hanya jika workspace > 50.000 file terindeks (setelah gitignore) atau user menjalankan `damnbot doctor index build`. Disimpan di `<data_dir>/index/<workspace-hash>/`, diinvalidasi fsnotify + verifikasi mtime saat query.

**Amandemen 2026-10-22 (retry-on-fail: gate merah diukur ulang sekali).** Butir 2 tidak berubah — statistik tetap p95 atas i dari (edit_i − ref_i), ambang tetap 30 ms, dan gate absolut Q14 tetap p95(edit) ≤ 20 ms. Yang ditambahkan: bila gate merah, benchmark menunggu **≥ 1 s**, lalu mengambil **set sampel baru (100 iterasi) di direktori temp baru** (engine baru, file baru), dan **hanya gagal bila set kedua juga merah**; kedua set dicatat di log (p50 dan p95 berpasangan, p95 edit dan referensi, lima outlier berpasangan terbesar) dan set yang memutuskan dilaporkan sebagai metrik (`paired-p50-ms`, `paired-p95-ms`, `p95-ms`, `ref-p95-ms`).

**Alasan (bukti fase 2026-10-21, 2 job `ubuntu-latest` AMD EPYC 9V74, 1.200 edit/job).** Seluruh ekor CT-09 berasal dari I/O durabel satu sisi: `fsync(file)` 185,5 / 103,1 / 37,2 / 23,8 ms pada iterasi yang referensinya 5,9 / 1,1 / 2,2 / 1,4 ms, `fsync(dir)` 69,3 ms, satu `readback` 134,6 ms (page-cache miss); fase milik edit_file sendiri tidak pernah melewati 6,8 ms (read p99 2,6; render p99 3,4; write p99 1,0; diff p99 1,5). Pemindaian marker menunjukkan laju stall 5/1.200 = 0,42 % pada job itu, dan kegagalan 2/14 di EPYC 7763 selalu berbentuk ≥ 5 iterasi berstall dari 100 (`paired` 49,5 dan 60,7 ms). Stall semacam itu transien: burst yang terukur berlangsung ≤ ~0,3 s, sehingga set kedua setelah jeda ≥ 1 s melihat keadaan disk yang berbeda. Regresi yang menambah kerja sinkron pada jalur edit menyentuh hampir semua iterasi, jadi ia gagal di kedua set.

**Perkiraan false-fail.** Bila kedua set saling bebas, laju false-fail menjadi p²: EPYC 7763 2 % → ≈ 0,04 % (batas atas dari sampel kecil (2/14)² ≈ 2 %); NVMe bursty 1/36 = 2,8 % → ≈ 0,08 %; EPYC 9V74 0,42 % stall/iterasi → ≈ 0,08 % per run. **Deteksi tidak berubah**: regresi aditif +35 ms tetap tertangkap (p95 berpasangan ≈ 39,8 ms di kedua set) dan +15 ms tetap lolos, sama seperti data sensitivitas butir di atas. Biaya: satu set 100 × 1 MB tambahan (≈ 0,2–1 s) plus jeda 1 s, hanya pada run yang merah.

**Amandemen 2026-10-22 (validity gate referensi bising + backstop nightly).** Menanggapi kegagalan 22 Oktober setelah retry-on-fail: dua run merah di EPYC 9V74 (main 3387c2f job 119618799254, #211 job 119635142064) menunjukkan `ref p95` 138–139 ms dengan `paired p50` 2,8–3,4 ms, dan retry-nya sendiri mati karena `b.Loop` tidak dapat dipakai untuk set kedua (diperbaiki #232). Aturan tambahan:

1. **Validitas set.** Set dengan `ref p95 > 30 ms` invalid: referensi tulis-durabelnya sendiri sedang stall, sehingga selisih berpasangan mengukur disk, bukan edit_file. Ambang 30 ms berada di celah lebar antara referensi sehat (p95 1,1–7,3 ms pada 15 eksekusi `perf` di EPYC 7763/9V45/9V74) dan yang bising (138–139 ms).
2. **Set dan keputusan.** Gate PR memakai maksimum 3 set (`DAMNBOT_CT09_SETS`, default 3), jeda ≥ 1 s dan direktori baru per set. Set invalid diulang. Set valid yang lulus → lulus. Dua set valid yang gagal → gagal. Bila semua set invalid (atau satu valid gagal dan sisanya invalid sampai batas) → **inconclusive**: log `CT-09 INCONCLUSIVE (noisy runner: ref p95 …)`, anotasi `::warning::` di GitHub, dan gate PR lulus.
3. **Backstop nightly.** Job `ct09` nightly menjalankan `make ct09-nightly` di main dengan 5 set; inconclusive di sana **gagal** (`DAMNBOT_CT09_STRICT_INCONCLUSIVE=1`), memakai mekanisme kegagalan nightly yang ada (tanpa layanan baru). Tujuannya: regresi yang lolos gate PR karena runner bising tetap ditandai pada main. Q14 tetap gate absolut p95(edit) ≤ 20 ms.
4. **Deteksi.** Regresi aditif (+35 ms) tidak menaikkan referensi, jadi setnya tetap valid dan gagal di dua set. Konsekuensi yang dicatat eksplisit: regresi yang memperlambat **kedua** sisi (edit dan referensi sekaligus) dapat diklasifikasikan inconclusive di PR, karena pada runner bersama ia tidak dapat dibedakan dari disk bising; nightly dengan 5 set dan gate absolut Q14 yang menangkapnya.
5. **Perkiraan false-fail.** Dari 17 eksekusi `perf` sejak #225 (15 dengan data): 2 merah, keduanya `ref p95` ≥ 138 ms, sehingga keduanya invalid dan tidak lagi gagal. Pada runner dengan referensi sehat, false-fail hanya muncul bila ≥ 5 iterasi stall satu sisi terjadi di dua set valid (≈ p²; fase terukur 0,42 % stall/iterasi di 9V74), yaitu di bawah 1 %.

**Konsekuensi.** (+) Repo raksasa (kernel-size) mendapat query sub-100 ms; (−) disk ≈ 0,3–0,5× ukuran teks repo [INFERENCE, diukur F1 pada korpus C2]; tidak aktif pada repo umum sehingga P16/P18 tidak terdampak.

## ADR-018 — Pemakaian ulang aset hermes

**Status:** diterima; lisensi Damn-Bot sendiri ditetapkan di ADR-021.

**Konteks.** Hermes berlisensi MIT (`LICENSE`, "Copyright (c) 2025 Nous Research").

**Keputusan.** Aset hermes (skill bawaan, string i18n, pola threat, preset provider, prompt) boleh dipakai ulang hanya dengan: (a) salinan teks lisensi MIT + copyright di `THIRD_PARTY_NOTICES.md`, (b) header asal di setiap file yang disalin/diturunkan, (c) daftar file turunan di `docs/ATTRIBUTION.md` yang dibuat bersama impor aset pertama. Kode Python tidak diterjemahkan baris-per-baris; desain ulang dari perilaku.

**Konsekuensi.** (+) Ekosistem skill/i18n hermes langsung terpakai; (−) kewajiban atribusi dicek CI (`TestAttributionHeaders`).

## ADR-019 — Registry provider deklaratif: semua provider hermes + omp + custom

**Konteks.** Keputusan user: 1.0 mendukung semua provider hermes dan omp, plus provider custom via API. [`PROVIDERS.md`](PROVIDERS.md): 88 provider (33 di keduanya, 18 hanya hermes, 37 hanya omp) + 6 server lokal; protokol dominan `openai-chat` (72 baris), lalu `anthropic-messages` (10), `openai-responses` (5), `gemini` (2), `bedrock` (1), proprietary (cursor, factory).

**Keputusan.**
- Provider = **data** (`providers.toml` embedded: id, alias, base_url, protokol, auth, header, model list/discovery, context window, kapabilitas, harga), bukan kode per provider.
- 4 adapter protokol: `openai-chat`, `openai-responses`, `anthropic-messages`, `gemini`. Transport/auth sebagai plugin kecil: bearer, header kustom, `key_cmd`, SigV4 (Bedrock: `anthropic-messages` lewat InvokeModel), Google service-account JWT (Vertex), OAuth device/PKCE (Copilot, Codex, Gemini CLI, Qwen, Kimi), none (lokal).
- Provider custom: 12 field skema PROVIDERS.md (`name`, `base_url`, `api`, `api_key_env|api_key|key_cmd`, `models[]` dengan `context_length`, `max_tokens`, `capabilities`, `extra_headers`, `extra_body`, `tls`, `session_affinity_header`, `streaming`); model list auto-discover via `GET /models` (openai), `/v1/models` (anthropic), `models.list` (gemini).
- Credential pool: beberapa key per provider, rotasi pada `rate_limited` dengan cooldown per key; setiap rotasi = event (aturan P7).
- Rekomendasi default: auto-pilih dari kredensial yang terdeteksi, referensi `provider/model`, **tanpa model default hardcoded** (wizard meminta pilihan).
- Protokol proprietary di luar 4 adapter (Cursor, Factory, Apple Foundation Models yang butuh framework native/CGO) ditandai di PROVIDERS.md; padanan dibahas di OPEN-QUESTIONS Q29.

**Konsekuensi.** (+) Menambah provider OpenAI-compatible = 1 entri data tanpa rilis kode (registry dapat di-override dari config); (+) satu jalur uji per protokol. (−) Quirk provider ditangani lewat field data (`extra_body`, `streaming=false`, `quirks = [...]`) yang harus terdokumentasi; quirk baru yang tidak bisa diekspresikan data butuh kode di adapter + regression test EC-PROV.

## ADR-020 — Telegram sebagai platform chat utama

**Konteks.** Keputusan user: Telegram prioritas pertama dan kualitas tertinggi, untuk menghindari risiko banned (protokol tidak resmi seperti WhatsApp Web). Hermes: PLAT-022 via `python-telegram-bot`.

**Keputusan.** Adapter Telegram ditulis langsung di atas Bot API HTTP (`net/http` via `internal/netx`), tanpa SDK: long polling (default) + webhook (secret token), rate-limit aware (`retry_after`, batas 30 msg/s global, 1 msg/s per chat, 20 msg/menit per grup), media (foto, dokumen, voice, video; unggah/unduh streaming), inline keyboard (approval, preview, plan), edit-stream (≤ 1 edit/1,5 s per pesan), topic/thread (`message_thread_id`), reconnect + offset persisten, outbox tahan restart. Dibangun di F4, sebelum platform lain (F7).

**Konsekuensi.** (+) Kontrol penuh atas ketahanan (SELF-HEALING.md §5) dan ukuran binary; (−) fitur Bot API baru harus ditambahkan manual (dipantau nightly smoke + changelog Bot API).

## ADR-021 — Lisensi MIT

**Konteks.** Keputusan user. Hermes MIT, memudahkan pemakaian ulang aset (ADR-018).

**Keputusan.** Damn-Bot berlisensi MIT, "Copyright (c) 2026 Damn-Bot contributors" (file `LICENSE`). Dependency hanya MIT/BSD/Apache-2.0/ISC; MPL-2.0 hanya untuk modul terisolasi tanpa modifikasi file MPL (Q6). Atribusi aset pihak ketiga mengikuti ADR-018.

**Konsekuensi.** (+) Adopsi & kontribusi mudah; (−) tidak ada perlindungan copyleft; GPL/AGPL/LGPL dilarang di dependency (`go-licenses` gate).

## ADR-022 — Sikap atas kandidat non-parity NP-01..NP-21

**Status:** Accepted 2026-10-17 — seluruh rekomendasi disetujui user (eks-Q19, eks-Q20); baris berlabel drop berstatus `dropped-with-approval`. Pemetaan NP → ID baris parity ada di kolom "Baris parity" tabel NP FEATURE-PARITY.md (17 baris `dropped-with-approval`; NP-21 ditambahkan ADR-033).

| NP | Kandidat ([`FEATURE-PARITY.md`](FEATURE-PARITY.md)) | Rekomendasi | Alasan |
|---|---|---|---|
| NP-01 | Entrypoint legacy `hermes-agent` | drop | Digantikan `damnbot`; tidak ada pengguna Damn-Bot lama |
| NP-02 | `hermes login` deprecated | drop | Padanan `damnbot setup` / `damnbot setup auth` |
| NP-03 | Extras kosong back-compat | drop | Tidak ada konsep extras (ADR-013) |
| NP-04 | Alias nama tool legacy | drop | Melanggar P4 (nama tool tunggal); importer memetakan nama lama di skill/config saat impor |
| NP-05 | Alias subcommand (`learning`, `memory-graph`, `gui`) | drop | Satu nama per command; `damnbot help` menyebut padanan hermes |
| NP-06 | 25 alias slash command | **keep** | Biaya 0 token (hanya UI), mempercepat migrasi; tabel alias eksplisit, tanpa fuzzy |
| NP-07 | Alias toolset `yuanbao` | drop | Grup modul bernama tunggal |
| NP-08 | `rl` tanpa subparser | drop | Bukan fitur |
| NP-09 | `spotify` di default-off toolset | drop | Sudah keluar dari core hermes |
| NP-10 | Home Assistant, Spotify, Langfuse (left core) | keep via ekstensi | Home Assistant/Spotify lewat MCP server; Langfuse lewat OTLP export (CROSS-CUTTING §5); tidak built-in |
| NP-11 | Updater lama | drop | `damnbot update` tunggal |
| NP-12 | Launcher gateway Windows legacy | drop | Windows Service baru (SELF-HEALING.md) |
| NP-13 | Impor hosted rooms legacy | drop | Importer hanya untuk format hermes terkini |
| NP-14 | Bot chat legacy dari lane CLI | drop | Pensiun di hermes |
| NP-15 | Migrasi satu-kali internal hermes | drop | Digantikan `damnbot import hermes` (format terkini + 2 skema state sebelumnya) |
| NP-16 | Provider `solstice` hidden | drop sementara | Pra-rilis; dapat ditambahkan sebagai entri data registry (ADR-019) saat publik |
| NP-17 | `kanban_schedule` | **keep** | Fungsional; tool deferred modul `kanban` |
| NP-18 | Internal/dev (`evals/`, `mini_swe_runner.py`, scripts) | keep evals & mini SWE runner (modul `batch`, dibutuhkan SCORECARD); drop scratch/scripts/`toolset_distributions.py` | Hanya yang punya nilai pengguna |
| NP-19 | Ekosistem Nous (Portal, free tier, anon sign-in, billing, telemetry, Tool Gateway) | drop-with-approval | Layanan proprietary Nous; Nous sebagai provider inference tetap didukung |
| NP-20 | `--version` vs `/version` (`/v`) | **keep** | Trivial; keduanya ada |
| NP-21 | Memory provider ByteRover (MEM-006), RetainDB (MEM-007), porsi PLUG-022 | drop | Dijadwalkan dihapus upstream 15 Okt 2026 (`plugins/AGENTS.md:19-20`); ditambahkan dan disetujui lewat ADR-033 |

**Konsekuensi.** Jumlah baris parity efektif = 854 − baris drop yang disetujui; dihitung otomatis oleh laporan parity.

## ADR-023 — Durabilitas: `synchronous=FULL` + turn journal

**Konteks.** Self-healing (keputusan user) mensyaratkan 0 korupsi dan 0 tool mutating terulang setelah mati listrik/kill -9/BSOD.

**Keputusan.** Semua koneksi tulis SQLite memakai WAL + `synchronous=FULL`. Tabel mutable `turn_journal` mencatat turn in-flight (iterasi, tool call terakhir, kelas idempotensi, idempotency key); recovery mengikuti [`SELF-HEALING.md`](SELF-HEALING.md) §3. Target `Store.Append` direvisi ≤ 10 ms p99 (dari ≤ 5 ms dengan `NORMAL`).

**Konsekuensi.** (+) Commit yang dilaporkan sukses bertahan mati listrik; (−) ±1–3 ms fsync per commit di SSD [INFERENCE, diukur F2]; dikompensasi batching event streaming (chunk token tidak di-commit per token, hanya pesan final per iterasi).

## ADR-024 — Memori terstruktur; embedding opsional

**Konteks.** Hermes: memori snapshot beku 2.200 + 1.375 char (`tools/memory_tool.py:56`), provider eksternal tanpa cap (`agent/memory_manager.py:501-508`). User: retrieval BM25 lokal + embedding opsional.

**Keputusan.** Memori = row terstruktur (kategori `fact|preference|project|procedure`, bukti `evidence_event_id`, `expires_at`, `last_confirmed_at`, `supersedes_id`), dedupe via normalisasi + similarity BM25, retrieval top-k ber-cap. Embedding opsional (`[memory] embeddings = "provider/model"`), disimpan sebagai BLOB float32 di SQLite, cosine brute-force (≤ 100.000 vektor), skor gabungan BM25 + cosine; default off.

**Konsekuensi.** (+) Tanpa vector DB/dependency; (−) embedding = call jaringan berbayar per entri baru (tercatat `purpose=media`, `feature=embedding`); brute-force > 100.000 vektor lambat → batas keras + pesan.

## ADR-025 — Undo universal lewat action journal

**Konteks.** Keputusan user: setiap aksi mutating bisa dibatalkan; aksi yang tidak bisa dibalik wajib ditandai dan disetujui sebelum jalan.

**Keputusan.** Paket `internal/journal` mencatat setiap aksi mutating dengan inverse: file (before/after blob), shell mutating (snapshot file yang terdampak bila dapat ditentukan dari parse AST + `git stash`-like ref untuk workspace git), memori/skill/config (row keputusan sebelumnya). Aksi tanpa inverse (HTTP POST eksternal, `send_message`, push git, perintah shell yang efeknya tak dapat ditentukan) = `irreversible` → approval wajib walau mode `auto`. Detail SPEC §18.1.

**Konsekuensi.** (+) Kepercayaan user untuk mode `auto-edit`; (−) penyimpanan blob bertambah → retensi journal 30 hari / 2 GiB (yang lebih dulu), aksi kedaluwarsa tidak bisa di-undo (ditampilkan).

## ADR-026 — Preview aksi berbahaya & plan mode

**Konteks.** Keputusan user: diff/dry-run sebelum aksi berbahaya; plan mode dengan persetujuan dan pelaporan deviasi.

**Keputusan.** Preview = bagian dari approval (SPEC §18.2): diff untuk file, dry-run (`git push --dry-run`, `rm` → daftar file) bila tersedia, ringkasan dampak; pilihan approve/reject/edit; timeout default = reject (TUI/web 10 menit, Telegram 10 menit). Plan mode (SPEC §18.3) = artefak `plan/v1` tervalidasi schema, eksekusi dengan progres per langkah, deviasi → event + jeda untuk persetujuan.

**Konsekuensi.** (+) Mengurangi aksi tak diinginkan; (−) plan mode menambah 1 LLM call perencanaan (tercatat, opt-in per turn via `/plan` atau `--plan`).

## ADR-027 — Budget guard berlapis

**Konteks.** Keputusan user: batas biaya/token harian, bulanan, per sesi, per provider; peringatan 80%, hard stop 100%.

**Keputusan.** `internal/budget` mengevaluasi semua cap sebelum setiap LLM call (estimasi) dan sesudahnya (usage aktual): turn, sesi, hari (zona waktu lokal), bulan, per provider, per fitur `auto`. Biaya = `usage.cost` provider bila ada, else harga registry (ADR-019) × token; biaya tak diketahui → cap token tetap berlaku + badge "cost unknown".

**Konsekuensi.** (+) Tidak ada tagihan mengejutkan; (−) hard stop dapat memutus tugas panjang → pesan menyebut cap, angka, dan cara menaikkan.

## ADR-028 — Scorecard otomatis mingguan

**Konteks.** Keputusan user: harness head-to-head vs hermes versi terbaru dijalankan mingguan dengan alarm.

**Keputusan.** Job cron bawaan `scorecard-weekly` (di `damnbot serve` mesin benchmark): tarik hermes `main` terbaru, jalankan subset SCORECARD (tipe (a) penuh + eval kualitas subset 60 task, model murah), simpan artefak, kirim alarm Telegram bila ada dimensi kalah atau regresi > 5%. Detail SPEC §18.5.

**Konsekuensi.** (+) Keunggulan dipantau terus setelah 1.0; (−) biaya model mingguan (budget job eksplisit, default USD 10/run).

## ADR-029 — Speed parity gate & teknik implementasi wajib

**Konteks.** Keputusan user: semua operasi yang dapat dieksekusi hermes harus jauh lebih cepat di Damn-Bot (SCORECARD §3).

**Keputusan.** Benchmark per baris FEATURE-PARITY, pemisahan waktu (a) agent vs (b) model/jaringan, target (a) ≥ 5× (≥ 10× untuk startup, CLI non-LLM, file tools, search sesi), (b) median ≥ 2×; regresi PR > 5% = gagal. Teknik wajib di SPEC §17.2 (zero-alloc SSE/JSON, HTTP/2 keep-alive pooling, prepared statements, worker pool terbatas, mmap/streaming file besar, render TUI diff-based, aset embedded, tanpa reflection di hot path, `damnbot doctor pprof`).

**Konsekuensi.** (+) Kecepatan menjadi properti yang diuji, bukan klaim; (−) CI lebih lama (job speed ±4 menit per PR), runner benchmark khusus untuk stabilitas angka.

## ADR-030 — Spec freeze & kebijakan perubahan

**Status:** Accepted 2026-10-17. Freeze berlaku setelah ADR-033..046 terintegrasi **dan** errata ADR-036..046 diverifikasi auditor; tanggal freeze dicatat di baris Status SPEC (SPEC §20).

**Konteks.** Keputusan user: setelah item fitur pembeda, backlog pasca-1.0, dan GAP-SWEEP masuk, spec dibekukan sebagai "Spec 1.0".

**Keputusan.** Perubahan setelah freeze hanya lewat ADR baru di dokumen ini yang menyertakan dampak terukur ke budget token (P1), latensi (P12/P17), LOC (P21), dan ukuran binary (P13), plus baris SCORECARD/MITIGATION yang terpengaruh. Detail SPEC §20.

**Konsekuensi.** (+) Scope stabil selama implementasi; (−) perubahan kecil pun butuh ADR (template ringkas ≤ 30 baris).

## ADR-031 — Pengecualian P5 untuk tool berhalaman (`read_file`, `grep`, `glob`)

**Status:** Accepted 2026-10-17 (eks-Q30, disetujui user).

**Konteks.** P5 (brief user): result > 4.000 char → spill ke disk + preview 800 char + path. Untuk tool yang membaca file workspace, "spill" berarti menyalin file yang sudah ada di disk ke blob lalu model membacanya lagi → loop persist→read→persist ([`CODE-TOOLS.md`](CODE-TOOLS.md) §1 butir 6). Memaksa halaman ≤ 4.000 char menambah round-trip, dan setiap round-trip mengirim ulang fixed overhead + history: membaca 16.000 char dalam 4 halaman ≈ 3 × (5.000 + history) token tambahan, lebih mahal daripada 1 halaman 16.000 char (≈ 4.000 tok).

**Keputusan.** `read_file`, `grep`, `glob` tidak di-spill: hasil dipotong di batas baris/match utuh dengan `next: offset=N`; cap halaman `page_chars = 16.000`, turun ke 4.000 char setelah agregat inline turn (24.000 char) terlampaui. Semua tool lain tetap mengikuti P5 apa adanya (inline ≤ 4.000, spill + preview 800). Agregat 24.000 char/turn berlaku untuk semua tool termasuk tool berhalaman.

**Konsekuensi.** (+) Lebih sedikit round-trip dan token per tugas baca; tidak ada blob duplikat isi workspace. (−) Satu result dapat mencapai 16.000 char (≈ 4.000 tok) — dibatasi agregat per turn; diuji `TestPageCap`, `TestTurnAggregate`.

## ADR-032 — Resolusi open questions (Q5–Q30)

**Status:** Accepted 2026-10-17. Keputusan user: setujui semua usulan; setiap Q memakai "default sementara" sebagai keputusan final, kecuali Q23 yang diubah. Yang masih terbuka: Q1–Q4, Q15, Q23-RC ([`OPEN-QUESTIONS.md`](OPEN-QUESTIONS.md)).

| Q | Topik | Keputusan final |
|---|---|---|
| Q6 | WhatsApp Web personal | **Diamandemen ADR-033 (GW-01) dan ADR-034:** Cloud API satu-satunya pilihan wizard; WhatsApp Web hanya lewat bridge eksternal `damnbot-wa-bridge` (tidak di-link ke `damnbot`), `--accept-ban-risk`, inbound-only default, cold outbound dilarang total di 1.0 |
| Q7 | Discord voice | `ffmpeg` eksternal bila terpasang, else `module_unavailable` dengan pesan |
| Q8 | Voice lokal | Executable eksternal (whisper.cpp, piper, engine wake word) + `damnbot doctor voice` berisi perintah instalasi per OS; tanpa installer model |
| Q9 | Google Meet bot | Modul `browser` + audio virtual PulseAudio/PipeWire, Linux saja; macOS/Windows `module_unavailable` |
| Q10 | Sandbox OS shell | Landlock opt-in (`exec.landlock = true`, Linux ≥ 5.13); OS lain tanpa sandbox OS (backend container untuk isolasi) |
| Q12 | Photon | Sidecar resmi sebagai proses eksternal bila tidak ada API publik |
| Q13 | Weixin iLink / Yuanbao | Direkayasa dari kode hermes (MIT) dengan atribusi (ADR-018) |
| Q14 | Mesin referensi | x86-64 4 core, NVMe, 16 GB RAM, Linux 6.x; ARM Raspberry Pi 4 4 GB, microSD A2 |
| Q16 | Bahasa Indonesia | Ya, `id` sebagai bahasa UI ke-18 (F8) |
| Q18 | Plugin catalog 645 entri | Padanan wajib hanya untuk kategori yang dipakai ≥ 1 baris FEATURE-PARITY; sisanya dipetakan ke MCP server publik bila ada (daftar F8) |
| Q19 | Ekosistem Nous | `dropped-with-approval` (ADR-022, NP-19); Nous sebagai provider inference tetap |
| Q20 | NP-01..NP-20 | Rekomendasi ADR-022 disetujui |
| Q21 | Telemetri | Hanya OTLP export ke endpoint milik user; tanpa endpoint Damn-Bot |
| Q22 | Harga model | Tabel `pricing` di `providers.toml` (diperbarui per rilis) + override config |
| Q23 | Jadwal benchmark LLM | **Diubah:** F0 hanya baseline gratis/tanpa LLM (token overhead, schema bytes, startup, RSS, code tools vs `rg`, speed parity tipe (a), install/config) + smoke LLM 10 task dengan model murah untuk menguji harness. Head-to-head penuh (SWE-bench Verified subset 100 × 3 run, model frontier, E2–E10) mulai RC F9; scorecard mingguan setelah ada build fungsional. Model & anggaran ditetapkan user saat itu (Q23-RC terbuka) |
| Q24 | Signal / SimpleX | Daemon pihak ketiga diterima sebagai prasyarat; `damnbot doctor gateway` mendeteksi & memberi instruksi |
| Q25 | Enkripsi at-rest | Default off; credential selalu di OS keychain; `doctor` merekomendasikan full-disk encryption |
| Q26 | Grammar tree-sitter | Unduh on-demand (pinned SHA-256) + bundle offline; `grammars_embedded` build terpisah |
| Q27 | Code signing OS | ed25519 + SHA-256 saja; macOS/Windows tanpa notarization/Authenticode di 1.0 |
| Q28 | Retensi data | Tanpa auto-purge; `doctor` memperingatkan bila DB > 5 GiB |
| Q29 | Provider proprietary | Cursor/Factory masuk bila protokolnya dapat direkayasa dari dokumentasi publik, else `dropped-with-approval`; Apple Foundation Models `dropped-with-approval` (alternatif model lokal) |
| Q30 | Paging tool berhalaman | ADR-031 Accepted |
| — | Binary 52 MB | ADR-010 Accepted |

**Konsekuensi.** *(Historis — status saat ADR ditulis. Status terkini: SPEC §20 + ADR-030/047; ADR-054 V-21.)* Spec tetap **belum** dibekukan; freeze menunggu integrasi GAP-SWEEP (SPEC §20). Biaya LLM fase F0 kecil (10 task × model murah); biaya run penuh ditunda ke RC.

## ADR-033 — Resolusi konflik GAP-SWEEP (eks-Q31–Q50)

**Status:** Accepted 2026-10-17. Keputusan user: setujui semua usulan resolusi SPEC §22.2. Q1–Q4, Q15 tetap asumsi kerja (diverifikasi manual di F3) dan Q23-RC tetap terbuka sampai RC; keduanya tidak memblokir freeze.

| Q | Gap | Keputusan final |
|---|---|---|
| Q31 | PROV-05 | ByteRover & RetainDB tidak di-port; MEM-006, MEM-007, porsi PLUG-022 = `dropped-with-approval` (NP-21, ADR-022) |
| Q32 | LOC | P21 → ≤ 190.000 LOC Go + ≤ 10.000 JS/CSS; §3.2.4 direvisi (core 30.000, exec 7.000, heal+obs 5.500, tui 10.000, acp 2.500, importer 9.000, gateway core 10.500, cron 3.500, exec backends 8.000, mcp 6.500, browser 5.500, media 9.500, platform lain 20.000, web UI 5.000 Go) |
| Q33 | Dependency | Batas dependency langsung → 28; tambah `go.starlark.net`, `github.com/ledongthuc/pdf`, `github.com/yuin/goldmark` (rencana 27 saat ADR ditulis; dikoreksi menjadi 26 oleh ADR-034/035 — ENGINEERING E-45) |
| Q34 | GW-01 | Amandemen ADR-032 Q6: Cloud API satu-satunya di wizard; WhatsApp Web hanya dengan `--accept-ban-risk`; inbound-only default; cold outbound dilarang total di 1.0 |
| Q35 | SEC-22 | Dua image resmi: `damnbot` (distroless, non-root) + `damnbot:toolbox` (interpreter untuk `execute_code`, tanpa compiler C/docker-cli) |
| Q36 | SEC-02 | `SSH_AUTH_SOCK` tidak diwariskan default; opt-in `exec.pass_ssh_agent = true` |
| Q37 | SEC-10 | IRC fail-closed tanpa SASL/`account-tag` |
| Q38 | SEC-01 | Tanpa mode backup plaintext; impor zip hermes read-only |
| Q39 | REL-17 | Listener observability `127.0.0.1:7879` terpisah dari web UI |
| Q40 | REL-07 | Batas wall-clock turn default 900 s (dapat dinaikkan per sesi) |
| Q41 | SEC-11/SEC-16 | Invariant baru P29 (privasi log + egress DLP) |
| Q42 | TOOL-06 | `damnbot doctor tools fetch` untuk executable ber-pin SHA-256 atas perintah user (amandemen SPEC §1.3 & ADR-032 Q8); model tidak diunduh |
| Q43 | GW-07 | `godmode`/account-pool tetap tersedia via hub dengan `tos_risk=high` + ack |
| Q44 | COST-03 | Default TTL cache prompt 1 jam untuk sesi interaktif |
| Q45 | OPS-08 | ≤ 20 perintah top-level; final = 20 (pemetaan normatif di SPEC §10, diperbaiki ADR-035/036) |
| Q46 | UX-01 | Terminal client ACP opt-in `acp.use_client_terminal = false`; P24 hanya untuk runner lokal |
| Q47 | UX-03 | `provider_error` di ACP = JSON-RPC error `-32603` + `data.stop_reason` (SPEC §3.5) |
| Q48 | UX-10 | Tanpa QR di 1.0; tautan sekali pakai via CLI/Telegram `/web` |
| Q49 | INF-03 | Image Docker ≤ binary + 2 MB / ≤ 23 MB terkompresi |
| Q50 | lain-lain §22.2 | Carrier opaque dikecualikan dari redaksi (teks thinking ter-redaksi → tidak di-replay); FTS contentless + `dmb_fts_norm` (CJK); REL-03 tanpa IPC single-writer (lock 1 `serve` per data dir); DB lebih baru → refuse start, inspeksi `mode=ro`; watchdog tick adaptif 1 s / 10 s idle; cron tetap `purpose=turn` dengan `cron.default_model`; kelas `delegated_agent_bypass`/`monetary` dikecualikan dari `allow_destructive`; approval tertunda di `turn_journal` |

**Konsekuensi.** Dampak terukur (dikoreksi integrasi review 1.0): P21 +30.000 LOC batas (rencana tunggal 184.900 = Σ §3.2.4); dependency langsung 26/28 setelah ADR-034 dan koreksi hitungan (ENGINEERING E-45); binary +≈ 2,0 MB (starlark 0,80, pdf 0,36, goldmark 0,86), Σ budget modul 48,4 MB ≤ 52 MB (P13); P1/P12/P17 tidak berubah (tool baru deferred, modul lazy). *(Historis — status saat ADR ditulis: spec belum dibekukan, menunggu review silang. Status terkini: SPEC §20 + ADR-030; freeze setelah ADR-034..036 dan verifikasi auditor.)*

## ADR-034 — WhatsApp Web lewat bridge eksternal; binary `damnbot` tetap 100% MIT-kompatibel

**Status:** Accepted 2026-10-17 (keputusan didelegasikan user sampai freeze; konsisten dengan prioritas MIT + Telegram + hindari banned). Menutup REVIEW-1.0 B-01 dan M-01.

**Konteks.** `go.mau.fi/whatsmeow` (MPL-2.0) wajib bergantung pada `go.mau.fi/libsignal` berlisensi **GPL-3.0** (diverifikasi review lewat pkg.go.dev). Me-link-nya menjadikan binary `damnbot` karya gabungan GPL-3.0 — bertentangan dengan ADR-021 (MIT, GPL ditolak) dan gate E-40. Parity PLAT-025 (WhatsApp Web) tetap diminta (ADR-013), sementara ADR-033 Q34 sudah membatasi mode Web (wizard hanya Cloud API, `--accept-ban-risk`, inbound-only, cold outbound dilarang).

**Keputusan.**
- `whatsmeow`/`libsignal` **tidak di-link** ke binary `damnbot` di build mana pun.
- PLAT-025 dipenuhi oleh **bridge eksternal opsional** `damnbot-wa-bridge`: proses terpisah, repo dan rilis terpisah dengan lisensinya sendiri (GPL-3.0), dipasang manual oleh user, tidak diunduh/di-embed oleh `damnbot` (termasuk tidak lewat `doctor tools fetch`). Status baris FEATURE-PARITY PLAT-025 = `external-bridge (ADR-034)`.
- Komunikasi lewat protokol IPC terdokumentasi dan berversi `damnbot.bridge/v1` (SPEC §9.7.1): JSON-RPC 2.0 di stdio pipe privat atau HTTP lokal loopback/Unix socket + bearer; handshake versi; method `send/ack/status/logout`, notifikasi `inbound/delivery/health`.
- Kebijakan ADR-033 Q34 tetap: default off, `damnbot gateway whatsapp-web enable --accept-ban-risk`, inbound-only default, cold outbound dilarang total (ditegakkan di kedua sisi).
- Modul di binary: `gateway.whatsapp-bridge` (klien protokol) budget 0,2 MB / 600 LOC (SPEC §3.2.4, turun dari 8,0 MB / 1.200 LOC). Σ budget binary = 48,4 MB ≤ 52 MB (P13, menutup M-01).
- Gate lisensi E-40/E-50 hanya memeriksa binary utama; `TestNoGPLInBinary` memastikan `go version -m damnbot` tanpa `whatsmeow`/`libsignal`/modul GPL.

**Dampak terukur (SPEC §20).** Token: 0 (P1 tidak berubah; grup katalog tetap satu baris). Latensi: +1 hop IPC lokal untuk pesan WhatsApp Web, target ≤ 5 ms p95 per pesan (`BenchmarkBridgeRoundTrip`), tidak memengaruhi P12/P17 (bridge hanya di `serve`, lazy P16). LOC: −600 di binary (modul 1.200 → 600); Σ rencana 184.900. Ukuran: −7,8 MB budget. Dependency: −1 (`whatsmeow`), total 26/28. SCORECARD: GAP-GW-01 + "Platform chat" (30 surface, PLAT-025 dihitung lewat bridge). MITIGATION: baris risiko lisensi copyleft (§9).

**Konsekuensi.** (+) Lisensi MIT binary utama terjaga; risiko ban terisolasi pada komponen yang dipasang sadar oleh user. (−) User WhatsApp Web harus memasang dan memperbarui bridge sendiri; bridge punya siklus rilis sendiri (kontrak protokol versi mayor menjaga kompatibilitas).

## ADR-035 — Integrasi REVIEW-1.0 sebelum freeze

**Status:** Accepted 2026-10-17. Menutup 65 temuan [`REVIEW-1.0.md`](REVIEW-1.0.md) (11 BLOCKER, 26 MAJOR, 28 MINOR) sebelum Spec 1.0 dibekukan. Verifikasi 2026-10-18 menilai 11 di antaranya parsial; penyelesaiannya di ADR-036.

**Keputusan.** Seluruh keputusan §22.2/§22.3 diterapkan ke bagian normatif SPEC (§2, §3.5, §4, §9, §10, §11 DDL, §12, §14, §15, §17.1); §22.2–§22.4 menjadi log historis. Desain P1 tanpa menaikkan batas: katalog deferred = baris grup modul ≤ 15 tok + satu baris gabungan MCP ≤ 60 tok + overflow `- +<K> more groups: tool_search` (literal final ADR-056) (worst case 450 ≤ 600), urutan grup dan nama server MCP alfabetis murni sehingga byte-identik per (versi, config hash) (dipertegas ADR-036, N-01); context tier = sub-cap fakta 60 / snapshot 150 / AGENTS.md 400 / memori 300 (pinned ≤ 150 + BM25, sisanya via `recall`) / skill 150 / pemisah 40 = 1.100, dengan urutan potong AGENTS.md → memori non-pinned → skill. CLI dipangkas ke 20 perintah top-level. Matriks build tunggal 8 target dengan tag `goolm`. FEATURE-PARITY mendapat kolom `status` (837 baris penyebut gate = 836 `planned` + 1 `external-bridge`; 17 `dropped-with-approval` di luar penyebut).

**Dampak terukur.** Token: P1 tetap 5.000 (hitungan per tier SPEC §2/§5.2/§6). Latensi: 0. LOC: Σ §3.2.4 = 184.900 (−1.600 vs rencana 186.500 sebelumnya; tidak ada scope yang dibuang — selisih = WhatsApp Web keluar dari binary dan konsolidasi angka). Ukuran: Σ 48,4 MB. Dependency: 26/28.

## ADR-036 — Errata pra-implementasi REVIEW-1.0

**Status:** Accepted 2026-10-18. Menutup seluruh temuan bagian "Verifikasi perbaikan (2026-10-18)" [`REVIEW-1.0.md`](REVIEW-1.0.md): 11 PARSIAL (B-03, B-06, M-02, M-03, M-07, M-08, M-10, M-14, M-20, m-15, m-24) dan 36 temuan baru (N-01..N-06 MAJOR, N-07..N-36 MINOR). Status SPEC kembali ke **kandidat freeze**; freeze (ADR-030) baru berlaku setelah auditor memverifikasi errata ini.

**Konteks.** Verifikasi independen atas ADR-034/035 menemukan bahwa sebagian perbaikan hanya diterapkan di SPEC, tidak di dokumen pendamping, dan bahwa perbaikan B-02 memperkenalkan konflik baru dengan P15. Status "FROZEN 2026-10-17" tidak sah karena kriteria freeze (BLOCKER + MAJOR tertutup) belum terpenuhi.

**Keputusan.**
- **N-01 (katalog deterministik):** urutan grup katalog deferred dan ≤ 8 nama server MCP = **alfabetis murni**; statistik pemakaian tidak memengaruhi katalog, sehingga katalog byte-identik per (versi, config hash) sesuai P15/§6 (`TestCatalogDeterministic`). Petunjuk berbasis pemakaian hanya boleh muncul di volatile tier (≤ 20 tok). Alternatif "snapshot pemakaian beku" ditolak: memecah prefix cache lintas sesi dan butuh golden snapshot.
- **N-02 (build tag):** tag tidak pernah lewat `GOFLAGS` (flag `-tags` di command line menimpanya); Makefile membangun satu daftar `TAGS ?= goolm` yang hanya ditambah → `-tags goolm,minimal|nomod_<id>|grammars_embedded` (ENGINEERING E-39, SPEC P13, CODE-TOOLS §6).
- **N-03 (bridge):** pin versi protokol WhatsApp Web = kewajiban bridge, dilaporkan `hello.capabilities.wa_version`; `health.reason ∈ {none, logged_out, temporary_ban, banned, network, protocol_outdated, unknown}` dipetakan ke breaker GW-11 (SPEC §9.7.1), diuji `TestBridgeProtocolConformance`. Satu kode error `cold_outbound_denied`; ack ban-risk di satu lokasi (config key + event audit).
- **N-04 / M-14 (FTS contentless):** recovery dan re-index FTS = `delete-all` + insert ulang dari tabel sumber lewat `dmb_fts_norm` (`blob_fts` dari file blob); perintah `rebuild` dihapus dari semua dokumen.
- **N-05:** P29 memakai angka GAP-SEC-11 (TTL isi pesan default 24 jam, maks 72 jam).
- **N-06 / M-20 / N-24..N-26, N-29 (CLI):** SPEC §10 menjadi satu-satunya sumber bentuk perintah; semua baris §22.1 dan dokumen pendamping memakai bentuk §10 (`sessions backup create|ls|verify [--include-secrets]`, `sessions restore <file> [--dry-run]`, `setup auth logout [--provider P|--all]`, `serve sessions revoke [--all|<id>]`, `doctor bundle [--out P] [--upload]`, `doctor tools fetch|verify|ls`, `serve open [--app|--remote]`, `setup --uninstall [--dry-run] [--purge-data]`, `setup secrets ls --refs`, `import archive <file>`, `setup completion <shell>`, flag service/supervisor/doctor SELF-HEALING); `help [topik]` = builtin parser, tidak dihitung. Nama test tunggal: `TestCLITopLevelCount`, `TestCLINoDuplicates`, `TestCLIDocsMatchHelp`.
- **DDL:** `gateway_inbound` mutable terbatas (trigger `gwin_update_guard` hanya mengizinkan `event_id` NULL→nilai); `gateway_identities.strength` = `cryptographic|platform_authenticated|weak`; `approvals.source` + `mcp_serve`; trigger `skills_au`/`skills_ad`; tabel append-only `blob_expirations` (gc hanya menghapus file blob; skema didesain ulang menjadi event-log `expired`/`restored` oleh ADR-037); kolom `usage_ledger.est_input_tokens` (EMA kalibrasi token diturunkan dari ledger saat load, tidak dipersist); stop reason `budget_gateway_user`, `budget_tools`; `hook_note` = `system_notice{kind:"hook_note"}`.
- **Angka tunggal:** Σ LOC 184.900 = Σ kolom fase ROADMAP (F1 29.500, F2 12.000, F3 21.500, F4 20.000, F5 30.500, F6 24.500, F7 25.100, F8 21.800; JS ≈ 9.900); dependency langsung rencana 26 ≤ 28; ACP 2.500 LOC; skill di context tier ≤ 150 tok; matriks test PR 6 target berrunner + 2 QEMU; job `sqlite-engine` 8 target; LOC per fitur ≤ 227 (190.000 ÷ 837); edge-case per kategori = katalog (Σ 166); CT-29 ditambahkan (30 ID berblok; *dikoreksi ADR-063 m-15: 31 ID = 14 + 10 + 7*).
- **Gate bernama:** P20 `TestScorecardGate`, P21 `TestBudgetTableSum`/`TestDirectDeps`/`TestFileLength`, P28 `BenchmarkSpeedParity*`; mode degradasi PROV-02 punya sub-cap + `TestContextTierBudget/degraded`; kebijakan cron overlap/gate/`idempotent`/`pause_after_crashes` ditulis di SELF-HEALING §6.

**Dampak terukur (SPEC §20).** Token: 0 (P1 tetap 5.000; urutan katalog tidak mengubah ukuran; sub-cap degradasi ada di dalam cap degradasi yang sudah ada). Latensi: 0 (P12/P17). LOC: 0 (Σ §3.2.4 tetap 184.900; trigger/tabel baru termasuk budget modul core/gateway). Ukuran binary: 0 (Σ 48,4 MB). Dependency: 0 (26/28). Baris SCORECARD/MITIGATION yang terpengaruh: SCORECARD token skill, LOC per fitur, platform chat, GAP-COST-10, GAP-OPS-08; MITIGATION T-07, GAP-REL-04, GAP-GW-01, GAP-GW-08, GAP-COST-16, GAP-PROV-02, GAP-SEC-18, GAP-OPS-08.

**Konsekuensi.** (+) Satu sumber normatif per angka/perintah; katalog dan prefix cache stabil lintas sesi. (−) Urutan katalog tidak lagi memprioritaskan grup yang sering dipakai saat overflow (> 24 grup); mitigasi: `tool_search` + petunjuk volatile tier. Freeze menunggu verifikasi auditor; setelah lolos, tanggal freeze ditulis di baris Status SPEC (§20).

## ADR-037 — Penutupan 5 MAJOR verifikasi errata ADR-036

**Status:** Accepted 2026-10-18. Menutup 5 MAJOR terbuka dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi errata ADR-036": V-01, V-02, V-03, V-04, dan residu N-06/M-20. MINOR tidak dikerjakan di ADR ini; semuanya dicatat sebagai errata backlog pasca-freeze di bawah. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-037.

**Keputusan.**
- **V-01:** 0006 menambah trigger `blobexp_no_update` dan `blobexp_no_delete` pada `blob_expirations` (SPEC §11.8). Kini 17/17 tabel di daftar append-only (`TestImmutableTables`) punya trigger no-UPDATE + no-DELETE.
- **V-02:** `blob_expirations` menjadi event-log `(id PK, sha256 FK, kind ∈ {expired, restored}, reason ∈ {journal_retention, orphan, quota, rewrite}, at)` dengan indeks `(sha256, id DESC)`. Status blob = baris terakhir per sha256. Writer blob yang menulis ulang isi ber-status `expired` meng-INSERT `restored/rewrite` dalam transaksi yang sama dengan `INSERT OR IGNORE INTO blobs`, sehingga tidak ada bentrok PK dan blob hidup tidak terbaca `expired` (SPEC §4.6 retensi, §11.6, §11.8, §18.1). `sessions purge` menghapus baris `blob_expirations` lalu `blobs` yatim **di dalam** transaksi purge, sebelum trigger dibuat ulang; file dihapus setelah commit. Test: `TestGCKeepsBlobRows`, `TestBlobRewriteAfterGC`.
- **V-03:** satu **daftar pengecualian dokumen** (SPEC §20, `testdata/docs_exclusions.txt`) berisi SPEC §22.2–§22.4 historis, OPEN-QUESTIONS, GAP-SWEEP, REVIEW-1.0, dan paragraf DECISIONS *(Historis …)*. Daftar ini dipakai bersama oleh `TestSpecNoPendingAmendments` dan `TestCLIDocsMatchHelp`. Cakupan `TestCLIDocsMatchHelp` ditetapkan di SPEC §10: code span `damnbot …` + baris code block `damnbot `/`$ damnbot ` dengan argumen pertama berupa perintah atau flag. Rujukan yang sama ada di ENGINEERING E-83.
- **V-04:** ukuran suite edit CT-20 = **≥ 325 kasus** (CODE-TOOLS §10.3) di SPEC §16 (tabel test), SPEC §22.1 GAP-TOOL-03, SCORECARD "Edit salah lokasi", MITIGATION H-08 / risiko validator / GAP-TOOL-03, dan ROADMAP F1.
- **N-06/M-20 residu:** kolom Verifikasi GAP-SEC-18 memakai `damnbot setup --uninstall --purge-data` (bentuk §10).

**Dampak terukur (SPEC §20).** Token: 0. Latensi: 0 (status blob = satu lookup indeks `(sha256, id DESC)` hanya untuk sha yang punya baris event-log). LOC: 0 (Σ §3.2.4 tetap 184.900). Ukuran: 0. Dependency: 0. Baris SCORECARD/MITIGATION yang terpengaruh: SCORECARD "Edit salah lokasi"; MITIGATION H-08 dan baris risiko "Validator schema & engine edit milik sendiri".

**Errata backlog pasca-freeze** (dikerjakan di awal F0, sebelum kode fitur pertama; lokasi = nomor baris saat verifikasi di REVIEW-1.0):

| ID | Lokasi | Inti |
|---|---|---|
| m-15 | CODE-TOOLS.md:551, SPEC.md:120, DECISIONS.md:448 | "30 ID" → 31 ID (14 + 10 + 7) |
| N-15 | SPEC.md:2113 (§18.4), :124 (P27), :99 (P2), :122 (P25) | cap gateway user/tool belum di daftar cap, P27, P2; nyatakan "200 turn/hari" ditolak pra-turn |
| N-24 | SCORECARD.md:171 | target "≤ 20 / 0" → "= 20 / 0" |
| N-33 | FEATURE-PARITY.md:66 | "`fase` diturunkan dari prefix" → deliverable F0 (`TestParityPhaseColumn`) |
| V-05 | SPEC.md:903, :2232 ↔ :233 | `needs_attention` tidak ada di enum `Module.Status` |
| V-06 | MITIGATION-MATRIX.md:197 | sisa risiko GW-11: rilis bridge (`protocol_outdated`), bukan rilis Damn-Bot |
| V-07 | SPEC.md:828 ↔ :2243, EXEC-AND-CODING.md:463, :489, :502 | Starlark: daftar tool, timeout, mekanisme macOS, batas memori vs §B.7 |
| V-08 | SPEC.md:710 ↔ :723 | baris Volatile §6: "nama skill/tool relevan baru (≤ 20 tok)" |
| V-09 | SPEC.md:98, :710, :2251, DECISIONS.md:430 | satu literal baris overflow katalog; alfabetis selalu berlaku |
| V-10 | SPEC.md:109, :115, :2002, DECISIONS.md:111, :176 ↔ ENGINEERING.md:416 | `TAGS += perf`; ADR-010/015 memakai bentuk `goolm,…` |
| V-11 | SPEC.md:118 ↔ ENGINEERING.md:422 | `TestDirectDeps` = parse `require` langsung |
| V-12 | SPEC.md:552 ↔ :1546 | aturan hapus baris `blobs` dan urutan purge. Teksnya ikut diubah ADR-037 karena tak terpisahkan dari V-01/V-02 (trigger `blobexp_no_delete` membuat penghapusan setelah commit mustahil); auditor cukup memverifikasi |
| V-13 | SPEC.md:1399 ↔ :2185 | `strength` DEFAULT fail-open → tanpa default atau `'weak'` |
| V-14 | SELF-HEALING.md:378 ↔ SPEC.md:1414-1429 | kriteria salvage `gateway_inbound`/`gateway_deliveries` |
| V-15 | SPEC.md:1277-1283 ↔ :1235-1237, SELF-HEALING.md:375 | `skills_fts.name` lewat `dmb_fts_norm` |
| V-16 | SELF-HEALING.md:375 ↔ SPEC.md:1352-1354 | recovery `tool_catalog_fts` |
| V-17 | MITIGATION-MATRIX.md:250 | GAP-INF-02 → SPEC §11.2, §11.6; SELF-HEALING §3.5 |
| V-18 | SPEC.md:535 ↔ :2241-2242 | satu rumus estimator token (COST-09) |
| V-19 | SPEC.md:427, :1953 ↔ :2250 | `system_notice{kind:"carrier_dropped", reason, count}` |
| V-20 | SCORECARD.md:3, :225, :246-257, ENGINEERING.md:308, EXEC-AND-CODING.md:545, ROADMAP.md:78, :173 | rujuk `TestScorecardGate`, `BenchmarkSpeedParity*`, `TestBudgetTableSum` |
| V-21 | DECISIONS.md:377 | penanda *(Historis …)* di ADR-032 |
| V-22 | ROADMAP.md:25 | syarat freeze = ADR-033..037 + verifikasi auditor |
| V-23 | SPEC.md:2137, :2297 ↔ PROVIDERS.md:164, EDGE-CASES-hermes.md (10 header) | isi `spec_pending_phrases.txt` |
| V-24 | SPEC.md:988, CROSS-CUTTING.md:47 ↔ SPEC.md:2192 | satu aturan enkripsi `doctor bundle` |
| V-25 | SELF-HEALING.md:515 ↔ SPEC.md:2280 | exit code `doctor` 0/1/2/3 (OPS-06 normatif) |
| V-26 | DECISIONS.md:266 ↔ SPEC.md:966, :990 | `--version` sebagai alias builtin di §10 |
| V-27 | SPEC.md:13 | indeks REVIEW-1.0: 65 temuan + errata ADR-036/037 |
| V-28 | ROADMAP.md:170 | gate parity 837/837 |
| V-29 | ENGINEERING.md:583 | pola grep tanpa `\|` di sel tabel GFM |
| V-30 | FEATURE-PARITY.md:740, :1032 ↔ SPEC.md:2254 | deskripsi PLUG-022 = slot Honcho |
| V-31 | SPEC.md:886, ROADMAP.md:65, :128 ↔ EDGE-CASES-hermes.md:215-237 | himpunan EC-PLAT Telegram eksplisit untuk gate F4 |

Setiap item backlog diselesaikan lewat satu PR dokumen di F0 yang merujuk ADR ini. Item backlog tidak memblokir freeze (kriteria REVIEW-1.0: BLOCKER + MAJOR tertutup).

**Konsekuensi.** (+) Gate P9 dan CLI-docs bisa hijau sejak commit pertama; satu angka suite edit. (−) Pembaca blob melakukan satu lookup tambahan di event-log; 31 MINOR menunggu F0.

## ADR-038 — gc blob berbasis perujuk hidup + penguncian lintas proses

**Status:** Accepted 2026-10-18; daftar perujuk, kueri liveness, dan trash global **digantikan ADR-039**. Menutup W-01 (MAJOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-037". Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-038.

**Konteks.** Blob disimpan berdasarkan isi (content-addressed), jadi satu sha256 bisa dirujuk sekaligus oleh before-image aksi yang sudah kedaluwarsa dan oleh spill atau lampiran transcript yang masih hidup. Aturan lama `doctor gc` ("yatim, atau ditahan aksi kedaluwarsa") menghapus file tanpa memeriksa perujuk lain. Model proses §3.3 juga multi-proses (satu writer per proses), jadi penghapusan file bisa berpacu dengan writer yang mempublikasikan sha yang sama.

**Keputusan.**
- **Daftar tertutup perujuk blob** di SPEC §11.6:
  - `events.blob_sha` dan kunci `blob_sha` di `events.payload`;
  - `event_attachments.blob_sha`;
  - `file_snapshots.before_sha`/`after_sha`;
  - `actions.inverse.$.blobs[]`;
  - `turn_journal.before_sha`/`after_sha` pada baris yang masih terbuka.
- Penambahan perujuk baru wajib lewat ADR; `TestBlobRefColumnsListed` memeriksa bahwa daftar lengkap.
- Dua perujuk pertama selalu hidup. Perujuk dari aksi atau snapshot hidup selama `created_at ≥ T_cut`; `T_cut` didefinisikan satu kali di §18.1. Perujuk `turn_journal` hidup selama journal terbuka.
- `doctor gc` hanya menghapus file (dan menulis baris `blob_expirations{kind:'expired'}`) untuk sha yang tidak punya perujuk hidup sama sekali.
- **Penguncian:** kunci lintas proses = lock tulis SQLite (`BEGIN IMMEDIATE`).
  - Writer hanya menulis dan meng-`fsync` file temp di luar lock. `rename` ke path final, `INSERT blobs`/`restored`, dan baris perujuk terjadi dalam satu transaksi.
  - gc dan `sessions purge` memindahkan file ke `blobs/.trash/` di dalam transaksinya, lalu `unlink` setelah COMMIT. Kedua `rename` berada di bawah lock yang sama, sehingga urutannya total.
  - Crash sebelum COMMIT dipulihkan dari `.trash/`. File temp hanya dihapus bila berumur > 24 jam.
  - gc berjalan per batch ≤ 500 sha dengan transaksi ≤ 200 ms.
- **Test:** `TestGCSharedBlobKept`, `TestGCWriterRace` (2 proses × 1.000 iterasi), `TestGCTrashRecovery`, `TestBlobRefColumnsListed`. Test lama tetap berlaku: `TestGCKeepsBlobRows`, `TestBlobRewriteAfterGC`.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900; masuk budget `ctxdb`/`journal`). Ukuran binary: 0. Dependency: 0.
- Latensi: publikasi blob kini memegang lock tulis selama `rename` + `fsync` direktori. Operasi ini sudah berada di transaksi `Append` yang sama, dan target `Store.Append` ≤ 10 ms p99 (Linux/Windows) tetap berlaku via `BenchmarkAppend`.
- DDL: komentar perujuk di `actions.inverse` dan `turn_journal.before_sha`/`after_sha`; tidak ada kolom atau tabel baru.
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Blob transcript tidak pernah hilang karena retensi undo.
- (+) gc dan purge aman dijalankan bersamaan dengan TUI/ACP/`serve`.
- (−) gc menjalankan kueri anti-join per batch.
- (−) Ruang disk dari before-image yang isinya identik dengan spill tidak dibebaskan. Ini disengaja.

## ADR-039 — `blob_refs` + gc dua fase + trash per transaksi

**Status:** Accepted 2026-10-18; protokol purge, pemicu recovery trash, dan klaim "purge vs outbox" **dikoreksi ADR-040**. Menutup W-01 (residu), X-01, X-02, X-03 (MAJOR), X-04, dan X-05 (MINOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-038". Desain mengikuti usulan auditor. ADR-038 tetap berlaku untuk publikasi writer di bawah lock dan `T_cut`; daftar perujuk, kueri liveness, dan trash global di ADR-038 **diganti** ADR ini. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-039.

**Konteks.** ADR-038 menurunkan liveness dari kolom DDL dan `json_tree` atas `events.payload`. Ada tiga masalah:

- Ref di `Meta` dengan nama kunci lain, yaitu `raw_blob` log mentah shell, terlewat (X-01).
- Kueri liveness tanpa indeks berjalan di bawah lock tulis global (X-03).
- `.trash/` dipakai bersama sehingga `unlink` milik satu proses bisa menghapus file transaksi lain yang belum commit, dan recovery tanpa lock bisa memulihkan file yang sudah di-purge (X-02).

Status preview approval (X-04) dan media outbox (X-05) sebagai ref juga belum diputuskan.

**Keputusan.**

1. **Tabel append-only `blob_refs`** (0006):
   - Kolom: `sha256`, `owner_kind`, `owner_id`, `ref_key`, `session_id`, `created_at`.
   - Indeks: `(sha256)`, `(session_id)`, dan UNIQUE `(owner_kind, owner_id, ref_key, sha256)` yang sekaligus menjadi indeks `(owner_kind, owner_id)`.
   - Trigger P9 `blobrefs_no_update`/`blobrefs_no_delete`; tabel ini masuk daftar append-only (18 tabel).
   - Diisi dalam transaksi `Store.Append` yang sama untuk setiap rujukan, lewat satu fungsi `ctxdb.BlobRefsOf(owner)` yang juga dipakai salvage, restore, dan `TestBlobRefsComplete`.
   - Tabel owner dan aturan hidupnya ada di SPEC §11.6: `event` dan `event_attachment` selalu hidup; `file_snapshot` dan `action` hidup selama `created_at ≥ T_gc = T_cut − 3.600 s`; `turn_journal` hidup selama state terbuka; `gateway_delivery` hidup selama `status ∈ {queued, sending, failed, uncertain}`.
2. **Konvensi kunci** `blob_sha`/`*_blob_sha`/`*_blob_shas` wajib untuk semua JSON: payload, `Meta`, `inverse`, `approval_request`.
   - `raw_blob` → `raw_blob_sha` (EXEC-AND-CODING).
   - `inverse.$.blobs[]` → `$.before_blob_shas[]`.
   - "preview sha" → `preview_blob_sha`.
   - `TestBlobRefColumnsListed` memindai DDL, registri skema payload per `events.kind`, skema `Meta` per tool, dan skema `inverse`/`approval_request`.
   - `json_tree` dihapus dari jalur gc.
3. **X-04/X-05 diputuskan sebagai ref:**
   - Preview approval tertunda (`turn_journal.approval_request.preview_blob_sha`, owner `turn_journal`).
   - Media outbound (kolom baru `gateway_deliveries.media_blob_sha`, owner `gateway_delivery`; bridge `send.media_ref` diturunkan dari kolom ini).
   - `turn_journal.before_sha`/`after_sha` dinyatakan hash pembanding, bukan ref; before-image dirujuk `actions`.
4. **gc dua fase:**
   - Pemilihan kandidat di luar lock, memakai snapshot WAL read-only.
   - Re-check per sha lewat `idx_blob_exp_latest` dan `idx_blob_refs_sha` di bawah `BEGIN IMMEDIATE`, per batch ≤ 500 sha dan ≤ 20.000 baris ref.
   - Deadline 150 ms per batch, jadi lock ≤ 200 ms per batch; kompleksitas O(batch × log n).
   - Duty cycle lock ≤ 20%; gc terjadwal berjalan saat idle.
5. **Trash per transaksi:**
   - File dipindah ke `blobs/.trash/<txid>/` dengan `txid = <pid>-<start_ns>-<seq>`.
   - Setelah COMMIT, sebuah proses hanya meng-`unlink` direktori `txid` miliknya sendiri.
   - Recovery berjalan di bawah lock (saat start dan sebelum setiap gc; pemicu dilengkapi ADR-040: setiap proses writer saat open DB, pemilik saat rollback, pembaca saat `ENOENT`). File hanya dikembalikan bila baris `blobs` masih ada, status terakhirnya bukan `expired`, dan path final belum ada; sisanya dibuang. Dengan begitu file purge yang sudah COMMIT tidak pernah dipulihkan.
   - Purge memakai protokol yang sama dan menghapus `blob_refs` sesi di transaksi yang sama.
6. **Hasil self-review subsistem blob** (masalah turunan yang ditutup sekaligus):
   - **Purge vs FK:** `DELETE blobs` tanpa indeks anak = full scan per baris. Ditambahkan indeks anak `idx_events_blob`, `idx_evatt_blob`, `idx_fsnap_before`/`after`, dan `idx_gw_deliveries_media`.
   - **Purge vs outbox:** *(dikoreksi ADR-040, Y-01 — klaim awal "delivery yang belum terkirim → `dead`/`purged`" salah: baris yang dipertahankan melanggar FK `event_id`/`media_blob_sha` dan menyimpan `body`.)* Purge **menghapus** semua baris `gateway_deliveries` sesi itu, termasuk `body`, sebelum `events`/`blobs` (SPEC §11.6, tabel urutan purge langkah 1 dan 10).
   - **Undo vs gc:** margin `T_gc` 1 jam. Pembaca yang menemui `ENOENT` membaca ulang status: `expired` → hasil `expired`; selain itu `blob_missing` (dilengkapi ADR-040: lebih dulu recovery trash untuk sha itu + baca ulang sekali).
   - **Windows:** pembaca memakai `FILE_SHARE_DELETE`. `rename` yang gagal → sha dilewati tanpa baris `expired`.
   - **Backup/restore dan salvage:** blob yang tidak ada di arsip → `expired`/`restore_missing` (nilai `reason` baru). Salvage menurunkan ulang `blob_refs` dan berjalan sebelum gc.
7. **Test:**
   - Baru: `TestGCSharedBlobKept`, `TestGCRawBlobKept`, `TestGCTrashPerTx`, `TestGCLockBudget` (1.000.000 event, batch ≤ 200 ms), `TestBlobRefsComplete`, `TestRestoreMissingBlob`.
   - Tetap berlaku: `TestBlobRefColumnsListed`, `TestGCWriterRace`, `TestGCKeepsBlobRows`, `TestBlobRewriteAfterGC`.
   - `TestGCTrashRecovery` (ADR-038) digantikan oleh `TestGCTrashPerTx`.

**Dampak terukur (SPEC §20).**
- Token: 0. Ukuran binary: 0. Dependency: 0.
- LOC: 0 (Σ §3.2.4 tetap 184.900; masuk budget `ctxdb`).
- DDL: +1 tabel (`blob_refs`), +2 trigger, +7 indeks (`idx_blob_refs_sha`, `idx_blob_refs_session`, dan 5 indeks anak FK), +1 kolom (`gateway_deliveries.media_blob_sha`), +1 nilai CHECK (`restore_missing`).
- Latensi: `Store.Append` mendapat ≤ 1 INSERT `blob_refs` per rujukan (biasanya 0–3 per event). Target ≤ 10 ms p99 tetap, dihitung termasuk INSERT ini (`BenchmarkAppend` kasus 3 ref/event). Selama gc, tunggu tambahan per Append ≤ 200 ms dengan duty cycle ≤ 20%.
- Disk: ≈ 120 B per rujukan.
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Liveness blob bisa dihitung lewat indeks dan lengkap terhadap semua sumber; gc, purge, dan recovery aman lintas proses.
- (−) Satu tabel append-only tambahan yang tumbuh sejajar dengan jumlah ref.
- (−) Setiap kunci JSON baru yang memuat blob wajib mengikuti konvensi; ini ditegakkan test.

## ADR-040 — Purge berbasis pohon sesi, tiga fase, terurut graf FK

**Status:** Accepted 2026-10-18; protokol fase (batching per sesi, fase 0 "sembunyikan", lease purge, siapa yang melanjutkan) **digantikan ADR-041**. Menutup residu X-02 dan Y-01, Y-02 (MAJOR) serta Y-03..Y-07 (MINOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-039". Keputusan semantik Y-02 diambil director atas delegasi user. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-040.

**Konteks.** Desain gc ADR-039 sudah benar; sisa masalahnya ada di jalur purge.

- Baris `gateway_deliveries` yang dipertahankan melanggar FK ke `events`/`blobs` dan menyimpan `body` (Y-01).
- Purge induk fork/rewind tidak terdefinisi: FK `parent_id` gagal, atau anak kehilangan prefix (Y-02).
- File hidup bisa tertahan di `.trash/` setelah purge di-rollback atau di-kill, karena recovery hanya dipicu saat start `serve`/TUI dan sebelum gc (residu X-02).

Self-review graf FK lengkap §11 menemukan masalah turunan berikut:

- 21 kolom FK anak tanpa indeks. SQLite memeriksa anak saat induk dihapus, jadi purge menjadi O(event × baris anak).
- Satu transaksi purge yang tak terbatas melanggar P19, karena proses lain terkena `SQLITE_BUSY` setelah `busy_timeout` 5 s.
- `usage_ledger.event_id NOT NULL`: menghapusnya membuat purge bisa mereset cap budget P27.
- `memory.evidence_event_id`/`source_session` dan CHECK bukti origin `agent`/`auto`.
- Salvage tidak memulihkan baris `sessions` induk.

**Keputusan.**

1. **Pohon sesi (Y-02).**
   - `sessions purge <id>` ditolak dengan `has_forks` bila sesi punya turunan (fork, rewind, delegate).
   - `--with-forks` mem-purge seluruh subtree; daftar sesi tampil di preview, lalu konfirmasi.
   - `--older-than` tanpa flag hanya mem-purge sesi tanpa turunan; dengan flag, hanya subtree yang semua anggotanya lebih tua.
   - Tidak ada salinan event ke sesi anak. `__system__` tidak dapat di-purge.
   - Test: `TestPurgeRefusesForks`, `TestPurgeWithForks`.
2. **Protokol tiga fase** (SPEC §11.6). Setiap transaksi ≤ 200 ms.
   - Fase 1 "tandai & lepas": label `session_labels.purge_state`, audit, outbox/journal/memori. Setelah fase ini sesi tidak terlihat dan purge tidak dapat dibatalkan. *(Digantikan ADR-041: fase 0 label terpisah dan fase 1/3 dibatch per sesi.)*
   - Fase 2: batch ≤ 2.000 event.
   - Fase 3: final.
   - Proses yang terputus dilanjutkan oleh proses writer berikutnya. *(Digantikan ADR-041: hanya `serve`/TUI/`doctor --fix`, dengan lease purge.)*
   - Test: `TestPurgeLockBudget` (100.000 event), `TestPurgeResumeAfterKill`.
3. **Tabel urutan purge** (31 langkah, anak sebelum induk) mencakup setiap FK ke `sessions`, `events`, `memory`, `actions`, `reasoning_carriers`, dan `blobs`. `TestPurgeFKOrder` memeriksanya secara statis dan lewat `PRAGMA foreign_key_check`.
   - **Y-01:** `gateway_deliveries` dihapus. Yang belum final dihapus di fase 1; sisanya per batch sebelum `events`. Sender yang memegang `sending` mendapat 0 rows affected dan menganggapnya `purged`. Delivery berisi konten sesi wajib punya `event_id` (`TestPurgeGatewaySession`, `TestDeliveryHasEvent`).
   - **Memori:** memori yang `source_session` atau `evidence_event_id`-nya di P ikut dihapus, dan ditampilkan di preview. `skills.memory_id` dan `supersedes_id` pihak lain di-NULL-kan.
   - **`usage_ledger`:** baris dipindahkan ke `__system__` (pengecualian P9 purge), jadi total budget dan usage tidak berubah (`TestPurgeKeepsBudgetTotals`).
   - Tabel mutable bernilai NULL-able (`cron_runs`, `kanban_tasks`) di-NULL-kan.
4. **21 indeks anak FK** di DDL §11 (`TestFKChildIndexes`: setiap kolom `REFERENCES` punya indeks dengan kolom itu di depan). Daftar: `summaries.event_id`, `usage_ledger.event_id`, `file_snapshots.event_id`, `approvals.event_id`/`session_id`, `memory.evidence_event_id`/`source_session`/`supersedes_id`, `skills.memory_id`, `imported_items.run_id`, `gateway_conversations.session_id`, `gateway_inbound.event_id`, `gateway_deliveries.event_id`, `cron_runs.session_id`, `kanban_tasks.session_id`, `kanban_task_events.task_id`, `actions.event_id`, `action_undos.event_id`, `plans.session_id`/`event_id`, `reasoning_carrier_rejections.session_id`.
5. **Residu X-02.** Recovery trash dipicu oleh:
   - (a) setiap proses writer saat open DB (SELF-HEALING §3.4);
   - (b) sebelum setiap gc;
   - (c) proses pemilik segera setelah rollback (`defer` di jalur error);
   - (d) pembaca yang mendapat `ENOENT` untuk sha hidup. Pembaca menjalankan recovery hanya untuk sha itu di bawah lock (≤ 50 ms), lalu membaca ulang sekali sebelum mengembalikan `blob_missing`.
   - Test: `TestGCTrashPerTx` diperluas dengan kasus "purge di-rollback/di-kill → pembaca lain berhasil ≤ 1 retry".
6. **Y-03:** file final tanpa baris `blobs` dipindah ke trash tanpa baris `expired`, setelah re-check di bawah lock (`TestGCOrphanFileNoRow`).
7. **Y-04:** salvage memakai urutan induk-dulu: `sessions`, lalu `blobs` (dibaca dari DB rusak atau placeholder `size`/`mime`), lalu owner, lalu `blob_refs`. Hasilnya 0 pelanggaran `foreign_key_check` (`TestSalvageMissingBlob`).
8. **Y-05:**
   - Ledger purge `<data_dir>/purge.log` (di luar DB, `fsync`, hanya id) dengan fase `intent`/`committed`/`aborted`.
   - Restore pengguna dan auto-restore `LAST_GOOD` menjalankan urutan yang sama: pass `restore_missing`, terapkan ulang ledger, lalu recovery trash.
   - Retensi data purge di backup otomatis dan arsip pengguna didokumentasikan dan ditampilkan di preview.
   - Test: `TestAutoRestoreReappliesPurge`, `TestAutoRestoreRestoreMissing`, `TestPurgeLedgerReapply`.
9. **Y-06:** satu aturan "jendela pemeliharaan gc/purge" dicantumkan di SPEC §4.6, §11.6, §17.1 (Append dan gateway inbound → `turn_start`), SELF-HEALING §3.1/P22-9, dan ROADMAP F2.
   - Satu nama benchmark: `BenchmarkAppend` (menggantikan `BenchmarkAppendFull`), ditambah `BenchmarkAppendDuringGC` (p99 ≤ 210 ms).
   - Idle = 0 lease yang belum kedaluwarsa di semua proses dan 0 inbound dalam 60 s. *(Dikoreksi ADR-042: idle = 0 turn aktif di `turn_journal` + 0 inbound 60 s.)*
10. **Y-07:** rujukan ke sha yang sudah ada tanpa `PutBlob` tetap wajib INSERT `blob_refs` dalam transaksi owner, setelah cek bahwa baris `blobs` ada dan status bukan `expired`. Bila cek gagal: `blob_expired` (kasus baru di `TestBlobRefsComplete`).

**Dampak terukur (SPEC §20).**
- Token: 0. Ukuran binary: 0. Dependency: 0.
- LOC: 0 (Σ §3.2.4 tetap 184.900; masuk budget `ctxdb`).
- DDL: +21 indeks anak FK, +1 nilai CHECK (`session_labels.key = 'purge_state'`), +1 file di luar DB (`purge.log`).
- Latensi: setiap INSERT ke tabel ber-FK menambah satu entri indeks. Target `Store.Append` ≤ 10 ms p99 tetap diukur `BenchmarkAppend` [INFERENCE: ± 1 entri B-tree per indeks, dikalibrasi F2]. Purge dan gc dibatasi ≤ 200 ms per transaksi.
- Disk: indeks anak ≈ 20–40 B per baris anak [INFERENCE].
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Purge selesai untuk sesi sebesar apa pun tanpa `SQLITE_BUSY` ke proses lain.
- (+) Tidak ada FK menggantung, isi pesan outbox, atau prefix anak yang hilang.
- (+) Blob hidup tidak tertahan di trash.
- (+) Purge tahan auto-restore.
- (−) Purge tidak atomik secara transaksional. Atomisitasnya logis: sejak fase 1, sesi tidak terlihat dan purge pasti diselesaikan.
- (−) Salinan teks memori P di `context_tier` beku sesi lain dan backup lama tidak dihapus; keduanya didokumentasikan dan ditampilkan di preview.
- (−) Purge induk mewajibkan `--with-forks`.

## ADR-041 — Purge per sesi berbatas lock, lease purge, resume terbatas

**Status:** Accepted 2026-10-18; kepemilikan lease sesi saat purge, pemicu resume, dan DROP/CREATE trigger **dikoreksi ADR-042**. Menutup Z-01 (MAJOR) dan Z-02..Z-07 (MINOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-040". Menggantikan bagian protokol fase ADR-040; tabel urutan FK dan semantik pohon ADR-040 tetap berlaku. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-041.

**Konteks.**
- ADR-040 membatasi transaksi purge ≤ 200 ms hanya terhadap jumlah **event**. Fase 1 dan fase 3 masing-masing "satu transaksi" yang berskala |P|, sehingga `--older-than --with-forks` atas puluhan ribu sesi kecil melanggar P19 (Z-01).
- Delivery `sent`/`dead` tanpa `event_id` membuat fase 3 gagal permanen di FK `media_blob_sha` (Z-02).
- Filter sesi yang sedang dipurge tidak tercermin di tabel kueri (Z-03).
- Sesi tanpa event tidak pernah memenuhi `--older-than` (Z-04).
- Lima kode error tidak ada di daftar tertutup (Z-05).
- Kepemilikan lease saat resume tidak didefinisikan (Z-06).
- Proses berumur pendek bisa ikut menjalankan purge besar di latar (Z-07).

**Keputusan.**

1. **Z-01 — empat fase, semuanya berbatas** (SPEC §11.6). Setiap transaksi ≤ 200 ms: batas jumlah **dan** deadline 150 ms, mana yang lebih dulu, plus duty cycle lock ≤ 20% seperti gc.
   - **Fase 0 "sembunyikan"** hanya menulis label `purge_state`, dalam urutan turunan-dulu (post-order). Satu transaksi bila |P| ≤ 2.000, selain itu batch ≤ 2.000 sesi.
   - **Fase 1 "lepas"** (langkah 1–8) dan **fase 3 "final"** (langkah 27–31) dibatch ≤ 50 sesi, juga turunan-dulu.
   - **Fase 2** tetap batch ≤ 2.000 event.
   - **Invarian I1:** himpunan berlabel selalu tertutup-turunan, jadi tidak ada sesi hidup yang punya leluhur berlabel.
   - **Invarian I2:** fase destruktif pertama baru dimulai setelah ledger `hidden`, yaitu setelah **seluruh** P final tersembunyi; sejak itu purge tidak dapat dibatalkan.
   - **Race fork di tengah fase 0:** sesi yang mendapat anak baru di luar P, atau ber-lease hidup, dikeluarkan bersama semua leluhurnya di P (`excluded`, dilaporkan). Fork baru dan prefix-nya tetap utuh. Ini konsisten dengan aturan pohon Y-02.
   - **Ledger:** `intent` (P berurut, ditulis dan di-`fsync` sebelum transaksi pertama), `excluded`, `hidden`, `progress` per batch, `committed`, `aborted`. Ledger boleh tertinggal; DB (label + event audit `purge`/`purge_done`) otoritatif. Resume idempoten karena setiap batch mengerjakan "sisa yang masih ada di DB".
   - Event audit hanya memuat `count`; daftar id sesi hanya ada di ledger.
   - Test: `TestPurgeLockBudget` ditambah kasus 50.000 sesi kecil; `TestPurgeResumeIdempotent`; `TestPurgeNewForkExcludes`.
2. **Z-02:** langkah 28 menghapus `gateway_deliveries` dengan status apa pun yang terikat ke sesi batch lewat `blob_refs` owner `gateway_delivery`, **sebelum** `DELETE blob_refs`/`blobs`. Aturan outbox: setiap delivery milik sesi selain `__system__` wajib `event_id` non-NULL (`TestDeliveryHasEvent` memeriksa writer outbox).
3. **Z-03:** tabel kueri §11.6 memuat filter `purging(s)` (lookup `idx_session_labels`, O(log n)) untuk `ls`, `show`/`export`/`why`, resume, gateway, `search`, `recall`, dan `doctor --verify`. Semua jalur baca sesi memakai helper `ctxdb.Visible` (`TestPurgeHiddenFromQueries`).
4. **Z-04:** aktivitas terakhir = `created_at` event ber-`seq` maksimum; tanpa event = `sessions.created_at`; aktivitas subtree = maksimum anggotanya. Kasus sesi 0 event ditambahkan ke `TestPurgeWithForks`.
5. **Z-05:** daftar tertutup §4.3 ditambah `db_busy`, `blob_missing`, `blob_expired`, `has_forks`, `session_purging`, serta `session_busy` (kode untuk `ErrSessionBusy` §3.3, agar pengeluaran di fase 0 bisa dilaporkan). Tabel kode di §4.3 mencantumkan sumber, arti, dan exit CLI: 4 = sementara, 1 = ditolak. Test `TestErrorCodesClosed`.
6. **Z-06 — lease purge per `txid`** di `module_state(module_id='purge')`, berisi `pid`, `host`, `boot_id`, `start_ns`, `heartbeat_at`, dan `phase`.
   - Heartbeat ≤ 5 s, TTL 30 s.
   - Setiap batch diawali cek pemilik (*fencing*).
   - Pemilik yang berhenti normal melepas lease (`heartbeat_at = 0`); baris baru dihapus di batch final, sehingga keberadaan baris = purge tertunda.
   - Pengambilalihan hanya bila lease dilepas, kedaluwarsa, atau pemiliknya mati di host + `boot_id` yang sama, lewat compare-and-swap; tepat satu proses yang melanjutkan.
   - Purge tidak memegang `session_leases` per sesi; label `purge_state` menolak lease baru (`session_purging`).
   - Test: `TestPurgeLeaseTakeover`.
7. **Z-07:** proses `sessions purge` asal menjalankan semua fase di foreground. Resume otomatis hanya oleh `serve` dan TUI (saat open DB lalu tiap 60 s) atau eksplisit `doctor --fix`. Proses berumur pendek (`run`, ACP, hook, CLI lain) hanya mencatat `purge_pending` (`doctor` WARN) dan tetap menjalankan recovery trash (SELF-HEALING §3.4). Test: `TestPurgeResumeOnlyLongLived`.
8. **Self-review skala besar dan proses mati di tengah batch:**
   - **Mati di tengah batch:** transaksi rollback; file kembali lewat recovery trash proses writer berikutnya; lease kedaluwarsa ≤ 30 s lalu diambil alih.
   - **Mati setelah COMMIT, sebelum tulis ledger:** ledger tertinggal, DB otoritatif; `committed` direkonsiliasi dari event `purge_done`.
   - **Proses terhenti lebih lama dari TTL** (SIGSTOP, laptop tidur): ditolak oleh fencing di batch berikutnya.
   - **Dua proses membuka DB bersamaan:** compare-and-swap lease, tepat satu yang melanjutkan.
   - **Tulis `intent` gagal (`disk_full`):** purge tidak dimulai.
   - **Ukuran payload audit:** event audit dengan 50.000 id tidak lagi dibuat (hanya `count`).
   - **Fork, lease, atau undo pada sesi berlabel:** ditolak `session_purging`.
   - **Fork dari leluhur yang belum berlabel:** leluhur itu dikeluarkan dari P (I1).
   - **`recall` selama purge besar:** filter per kandidat; target 50 ms p95 dinyatakan di luar purge berjalan.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900). Ukuran binary: 0. Dependency: 0.
- DDL: 0. Lease purge memakai `module_state` yang sudah ada; label memakai `session_labels.key = 'purge_state'` dari ADR-040.
- Latensi: lookup `purging(s)` O(log n) per sesi pada jalur baca (≤ 1 ms tambahan); purge ≤ 200 ms per transaksi untuk |P| dan ukuran event berapa pun.
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) P19 berlaku untuk purge banyak sesi.
- (+) Resume idempoten dengan pemilik tunggal; proses pendek tidak menanggung purge.
- (+) Sesi yang sedang dipurge tidak bocor ke kueri baca.
- (−) Selama fase 0 (≤ beberapa detik pada |P| besar), sebagian P sudah tersembunyi sementara sisanya belum.
- (−) P bisa menyusut bila ada fork atau lease baru; sesi yang dikeluarkan dilaporkan dan tidak dipurge.
- (−) Purge yang terputus menunggu `serve`/TUI/`doctor --fix`. Selama menunggu, data tetap tersembunyi dan `doctor` memperingatkan.

## ADR-042 — Token fencing lease, guard sesi di DB, barrier pemeliharaan

**Status:** Accepted 2026-10-18; barrier (marker), definisi `boot_id`, ekspresi mode, dan tabel skenario **dikoreksi/dikonsolidasi ADR-043** (SPEC §11.11). Menutup AA-01 (MAJOR) dan AA-02..AA-09 (MINOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-041". ADR ini juga mencatat hasil review adversarial menyeluruh atas subsistem persistence. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-042.

**Konteks.** Ini putaran ke-8 di subsistem yang sama, dan akar masalahnya selalu sama: invarian purge ditegakkan oleh **disiplin pemanggil** ("label menolak lease baru"), bukan oleh mekanisme yang tidak bisa dilewati.

- **AA-01:** pemegang lease basi (SIGSTOP, sleep, lompatan jam) dan recovery journal/auto-continue tetap bisa menulis ke sesi berlabel. Akibatnya event baru di luar `E_P` membuat langkah 30 macet permanen, dan riwayat sesi bisa terkirim ke provider.
- **AA-02..AA-09:** filter baca turunan (memori, outbox), gateway, exit code, purge overlap, DROP/CREATE trigger, resume ACP, budget `recall`, dan klaim idempoten.

**Keputusan.**

1. **AA-01: fencing ditegakkan di DB, bukan di pemanggil.**
   - `session_leases` mendapat `fence_token` (penghitung global monoton `module_state('ctxdb','fence_seq')`, `UNIQUE`), `owner_start_ns` *(diganti `owner_start` opaque oleh ADR-044/AC-02)*, dan `owner_boot_id`.
   - Waktu lease memakai **jam boot** yang ikut berjalan saat sleep dan hanya bermakna untuk `boot_id` yang sama. Lompatan jam dinding tidak berpengaruh.
   - Akuisisi/ambil alih dengan aturan liveness tunggal (kosong / kedaluwarsa / `boot_id` beda / pemilik mati di host + boot yang sama). Heartbeat bersyarat token.
   - Migrasi baru `0008_guards.sql` (SPEC §11.10): 33 trigger `BEFORE INSERT/UPDATE`. Guard **P** menolak baris yang merujuk sesi berlabel (`session_purging`) di setiap tabel FK ke sesi. Guard **F** menolak tulis transcript/journal/outbox bila `dmb_lease_token(s)` ≠ `fence_token` (`lease_fenced`; `__system__` dikecualikan).
   - Guard tidak aktif hanya di koneksi pemeliharaan (`dmb_maint() <> ''`).
   - Loop memanggil `CheckLease` sebelum setiap LLM call. Lease hilang → turn dibatalkan dan process group tool dibunuh.
   - Recovery journal melewati sesi berlabel dan mengambil lease sebelum menulis.
   - Fase 3 purge mengulang langkah 10–25 dan 1–3 dan menghapus lease sesi sebelum langkah 27–30 (pertahanan berlapis).
   - **Invarian I3:** sejak label ter-COMMIT, 0 transaksi non-purge yang berhasil menulis baris yang merujuk sesi itu.
   - Test: `TestStaleWriterFenced`, `TestJournalRecoverySkipsPurging`, `TestPurgeNeverStuck` (fuzz 10.000 iterasi), `TestSessionGuardsCoverFK`, `TestGuardsInactiveInMaint`.
2. **AA-02:** baris kueri "Memori relevan" dan "Outbox due" memfilter `purging`. UPDATE outbox untuk sesi berlabel juga ditolak guard. Keduanya masuk `TestPurgeHiddenFromQueries`.
3. **AA-03:** inbound gateway ke chat yang sesinya berlabel memperlakukan sesi itu sebagai tidak ada. Worker membuka sesi baru di transaksi `user_message` yang sama dan membalas **satu** pesan sistem per chat per `txid`. Tidak ada pesan yang dibuang (P22). Kasus ini ditambahkan ke `TestPurgeGatewaySession`.
4. **AA-04:** `session_purging` → exit **1** (permanen). Kode baru `lease_fenced` dan `purge_in_progress` → exit 4. Aturan prioritas: error sebelum logika perintah memakai tabel §4.3, sesudahnya kontrak perintah; exit 4 hanya untuk kondisi sementara (`TestExitCodePrecedence`).
5. **AA-05:** maksimal satu purge aktif, dicek ulang di bawah lock pada transaksi pertama; purge kedua ditolak `purge_in_progress`. P dihitung lewat `ctxdb.Visible`. Reapply ledger setelah restore berjalan berurutan. Test: `TestPurgeSingleActive`.
6. **AA-06:** DROP/CREATE TRIGGER diganti `WHEN dmb_maint() <> 'purge'` *(dikoreksi ADR-043/AB-03 menjadi `IS NOT 'purge'`, fail-closed)* pada 35 trigger P9 tabel yang disentuh purge. `sessions`/`blobs` no-UPDATE dan `kanban_task_events` tidak punya pengecualian. Fungsi per koneksi tanpa penyetel SQL, `SQLITE_INNOCUOUS`. Skema tidak berubah selama purge (`TestPurgeNoSchemaChange`). Pengecualian P9 tetap hanya purge, karena mode `restore` tidak melewati P9.
7. **AA-07:** ACP berumur ≥ 5 menit ikut me-resume purge. Purge tertunda tampil sebagai notice sekali per proses di TUI/ACP/`run`.
8. **AA-08:** pengecualian budget `recall` selama purge dicatat di SPEC §17.1 dan ROADMAP F2; kalimat Z-04 diperbaiki.
9. **AA-09:** pengeluaran fase 0 dan fase aktif dicatat di DB pada transaksi yang sama (`value.excluded[]`, `value.phase`); I2 bergantung pada `value.phase`, bukan ledger. `TestPurgeResumeIdempotent` dinyatakan dengan asumsi eksplisit *fake clock + lease beku*; tanpa asumsi itu, hasil = P dikurangi pengeluaran yang tercatat.
10. **Hasil review adversarial** (yang menghasilkan perubahan normatif):
    - **Restore/auto-restore multi-proses.** Proses lain bisa tetap menulis ke inode DB lama yang sudah dikarantina (POSIX), atau memblok penggantian file (Windows). Data hilang diam-diam. Perbaikan: barrier pemeliharaan `run/maint.lock` (bersama/eksklusif) + marker `run/maint.request`, dicek sebelum setiap `BEGIN IMMEDIATE`. Proses lain menutup koneksi, membuang token, lalu membuka ulang dan mengambil lease baru. Batas tunggu 30 s, lalu `db_busy`/SAFE MODE (`TestRestoreBarrierMultiProcess`).
    - **Restore menghidupkan journal lama.** Lease dari snapshot dikosongkan; journal terbuka dari snapshot ditutup `crashed` **tanpa** auto-continue (`TestRestoreNoAutoContinue`).
    - **Idle gc.** "0 lease hidup" membuat TUI yang dibiarkan terbuka menahan gc selamanya. Idle kini = 0 turn aktif di `turn_journal` + 0 inbound 60 s.
    - **Efek samping tool setelah fencing.** Fencing DB tidak menghentikan proses `exec` yang masih berjalan. Saat lease hilang, process group dibunuh.
    - **Undo lintas sesi.** Undo dicatat di sesi pemanggil dengan lease pemanggil. Pencatatan yang ditolak setelah file dipulihkan dilaporkan beserta daftar file (`TestUndoFencedReportsFiles`).
    - **Fitur `auto` tanpa lease** mencatat usage/event di `__system__` dengan `session_ref`. *(Dikoreksi ADR-043/AB-09: rujukan disimpan di tabel `auto_session_refs` ber-FK, dihapus purge.)*
    - **PID reuse antar-kontainer** di volume yang sama: cek "pemilik mati" hanya berlaku di host + boot yang sama.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900; masuk budget `ctxdb`). Ukuran binary: 0. Dependency: 0.
- DDL: +3 kolom `session_leases`, +1 migrasi (`0008_guards.sql`, 33 trigger guard), 35 trigger P9 mendapat klausa `WHEN`.
- Di luar DB: +2 file (`run/maint.lock`, `run/maint.request`).
- Latensi: INSERT `events` +2 lookup indeks + 1 panggilan fungsi; tabel ber-`event_id` +1 lookup PK. Semuanya termasuk target `Store.Append` ≤ 10 ms p99 (`BenchmarkAppend` kasus 3 ref/event) [INFERENCE: µs per lookup, dikalibrasi F2]. Setiap tulis +1 `stat` marker barrier. Purge: 0 perubahan skema, jadi 0 re-prepare di proses lain.
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Invarian purge dan kepemilikan sesi ditegakkan di DB untuk semua penulis, termasuk penulis masa depan.
- (+) Restore dan salvage aman dengan banyak proses.
- (+) gc tidak lagi tertahan TUI yang menganggur.
- (−) Setiap tabel ber-FK ke sesi yang ditambahkan di masa depan wajib punya guard; `TestSessionGuardsCoverFK` menegakkannya.
- (−) Alat SQLite eksternal tidak bisa menulis ke DB tanpa fungsi `dmb_*`. Sudah demikian sejak `dmb_fts_norm`; `doctor` menjelaskannya.
- (−) Residu terukur: ≤ 1 LLM call basi per turn bila proses terhenti tepat setelah `CheckLease`. Hasilnya tidak pernah ditulis.

**Skenario adversarial yang diperiksa** (ringkas; hasil = aman berkat mekanisme yang disebut):

| # | Skenario | Mekanisme / hasil |
|---:|---|---|
| 1 | Pemegang lease di-SIGSTOP > 10 s, proses lain ambil alih, lalu SIGCONT | token berubah → heartbeat 0 baris, guard F → `lease_fenced`; turn + tool dibunuh |
| 2 | Pemegang lease basi bangun setelah sesinya dilabel purge | guard P → `session_purging`; I3; langkah 30 tidak macet |
| 3 | Laptop sleep / NTP lompat ±1 jam | jam boot + `boot_id`; jam dinding tidak dipakai untuk lease *(dikoreksi ADR-043/AB-02: di Windows/macOS `boot_id` lama diturunkan dari jam dinding; kini identitas kernel opaque, SPEC §11.11.1)* |
| 4 | Reboot di tengah turn | `boot_id` beda → lease mati → recovery journal (lease baru) |
| 5 | Dua proses membuka DB bersamaan, sama-sama ingin recovery journal sesi X | akuisisi lease diserialkan `BEGIN IMMEDIATE`; yang kalah melewati X |
| 6 | Recovery journal / auto-continue setelah purge terputus di fase 0–1 | sesi berlabel dilewati; guard P sebagai pertahanan kedua |
| 7 | Crash di setiap langkah Append (sebelum/sesudah rename blob, sebelum COMMIT) | transaksi atomik; file tanpa baris `blobs` dibersihkan gc (Y-03); temp > 24 jam dihapus |
| 8 | Fork/rewind/delegate dari sesi berlabel atau di tengah fase 0 | guard P pada `sessions.parent_id`; fase 0 mengeluarkan leluhur (I1) |
| 9 | Undo aksi sesi yang mulai dipurge | guard dicek sebelum file disentuh; jendela sisa dilaporkan dengan daftar file |
| 10 | Gateway inbound ke chat yang sesinya berlabel | sesi baru + 1 balasan sistem; 0 pesan dibuang |
| 11 | Outbox sender mengirim delivery sesi berlabel | filter Outbox due + guard UPDATE `queued → sending` |
| 12 | Cron/kanban/fitur `auto`/kurator menulis ke sesi berlabel | guard P (cron_runs, kanban_tasks, session_labels, memory*); `auto` tanpa lease → `__system__` |
| 13 | ACP memegang sesi lama, user mem-purge | lease hidup → sesi dikeluarkan di fase 0, dilaporkan `session_busy` |
| 14 | Dua `sessions purge` bersamaan | dicek ulang di transaksi pertama → `purge_in_progress` |
| 15 | Purge besar sementara TUI/gateway aktif | ≤ 200 ms per transaksi, duty cycle ≤ 20%, 0 perubahan skema |
| 16 | `sessions restore` saat `serve` dan TUI berjalan | barrier pemeliharaan; proses lain menutup koneksi lalu membuka ulang dengan token baru |
| 17 | Auto-restore saat proses lain menulis | barrier sama; gagal ≤ 30 s → SAFE MODE, DB tidak diganti |
| 18 | Restore dari backup yang punya journal terbuka | ditutup `crashed` tanpa auto-continue; lease snapshot dikosongkan |
| 19 | Restore dari backup sebelum purge / di tengah purge | ledger diterapkan ulang berurutan; label otoritatif; trash tidak memulihkan file purge |
| 20 | Salvage menyisipkan baris tanpa lease | mode `restore`: guard nonaktif, P9 tetap aktif, urutan induk-dulu |
| 21 | gc vs writer vs purge vs recovery trash | semua `rename` di bawah lock yang sama; trash per txid |
| 22 | TUI dibiarkan terbuka berhari-hari | gc idle berbasis turn aktif, bukan lease |
| 23 | PID reuse / kontainer lain di volume yang sama | `owner_start_ns`; cek pemilik mati hanya di host + boot yang sama |
| 24 | Proses mati saat memegang lease purge | TTL 30 s jam boot → diambil alih `serve`/TUI/ACP ≥ 5 menit/`doctor --fix`; fencing per batch |
| 25 | Proses pendek (`run`) menemukan purge tertunda | tidak melanjutkan; notice sekali + `doctor` WARN |

## ADR-043 — Konsolidasi model konkurensi persistence (SPEC §11.11)

**Status:** Accepted 2026-10-18; desain kotak masuk approval, `owner_start_ns`, mode reapply, pelepasan lease, dan klaim I13 **dikoreksi ADR-044**. Menutup AB-01, AB-02 (MAJOR) dan AB-03..AB-09 (MINOR) dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-042", serta skenario adversarial #26–#34. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-043.

**Konteks.** Delapan putaran review berturut-turut menemukan celah di subsistem persistence. Penyebab strukturalnya: aturan lease, mode, barrier, purge, gc, recovery, dan restore tersebar di §3.3, §11.6, §11.10, §18.1, SELF-HEALING §3.4–§3.5, dan delapan ADR. Setiap perbaikan lokal tidak memeriksa interaksi dengan aturan di tempat lain.

Dua MAJOR terbaru berasal dari mekanisme baru ADR-042:
- **AB-01:** marker barrier tanpa pemilik bisa membekukan semua writer secara permanen.
- **AB-02:** `boot_id` di Windows/macOS diturunkan dari jam dinding.

**Keputusan.**

1. **Konsolidasi.** Bagian baru **SPEC §11.11** menjadi satu-satunya sumber normatif. Isinya:
   - asumsi lingkungan;
   - fungsi liveness tunggal `alive()`;
   - tabel mode koneksi;
   - state machine lease, sesi, purge, pemeliharaan, dan journal;
   - aturan blob;
   - tabel "siapa boleh menulis apa dalam mode apa";
   - urutan lock;
   - invarian I1–I14 + P9 beserta test;
   - tabel 42 skenario adversarial.

   §3.3, §4.6, §11.6, §11.10, §18.1, dan SELF-HEALING §1.2/§3.4/§3.5 dipangkas menjadi langkah operasional + rujukan; bila ada perbedaan, §11.11 berlaku (`TestConcurrencyDocRefs`).
2. **AB-01:**
   - Pemilik pemeliharaan memegang lock eksklusif `run/maint.owner`, yang lepas otomatis saat proses mati. Marker `run/maint.request` hanya informatif.
   - Setiap jalur gagal (timeout, error, Ctrl-C, panic) menghapus marker lewat `defer`.
   - Penunggu mencoba lock bersama non-blocking pada `maint.owner` setiap 1 s. Bila berhasil, marker basi dihapus dan penunggu lanjut ≤ 1 s.
   - Batas tunggu = 30 s + `expected_s`, lalu `db_busy` dengan retry berkelanjutan.
   - Urutan buka DB memberi preferensi pada restorer.
   - Test: `TestMaintMarkerStaleAfterKill`, `TestRestoreTimeoutClearsMarker`.
3. **AB-02:**
   - `boot_id` = identitas kernel opaque (Linux `boot_id`, macOS `kern.bootsessionuuid`, Windows registry `BootId` [INFERENCE: ketersediaan]); `''` bila tidak tersedia.
   - Aturan bebas jam: `heartbeat_at` dari masa depan menandakan lease dari boot lain, jadi lease dianggap mati.
   - Model satu kernel dinyatakan eksplisit; `owner_host` memuat pid-namespace. Time namespace tidak didukung (`doctor` WARN).
   - Skenario #3 dikoreksi.
   - Test: `TestLeaseLivenessClockStep`.
4. **AB-03:** P9 dan guard memakai `IS NOT 'purge'`; pernyataan guard F menambah `IS NOT 'restore'`. NULL atau nilai asing berarti semua aktif (fail-closed). Mode diikat ke transaksi lewat commit/rollback hook. Test: `TestMaintModeFailClosed`, `TestMaintResetOnPanic`.
5. **AB-04:**
   - Cek sesi-sama `event_id`/`session_id` pada `actions`, `approvals`, `plans`, `summaries`, `usage_ledger`.
   - Guard baru `guard_gwin_ins` dan trigger `gwconv_no_update`.
   - `skills` dan `memory_embeddings` ikut memeriksa sesi bukti.
   - `kanban_task_events` menjadi pengecualian tertutup.
   - `TestSessionGuardsCoverFK` didefinisikan transitif (FK ke induk yang terjangkau sesi + guard UPDATE atau no-UPDATE untuk tabel mutable).
6. **AB-05:** inventaris penulis (§11.11.10):
   - jembatan approval lintas proses → kotak masuk `approval_responses` (guard P; dikonsumsi pemegang lease);
   - `damnbot undo` dari shell → lease pendek atas sesi aksi;
   - import massal → lease per sesi per batch.
   - Test: `TestMCPServeApprovalRespond`, `TestUndoCLIShortLease`, `TestImportUnderGuards`.
7. **AB-06:**
   - Guard P tetap aktif di mode `'restore'`; I3 berlaku untuk semua kecuali koneksi purge.
   - Turn di proses lain dibatalkan tanpa tulis sebelum DB diganti.
   - Reapply purge di bawah barrier hanya fase 0, sebagai satu purge gabungan berbatas `expected_s`; fase 1–3 dilanjutkan di luar barrier.
   - Test: `TestRestoreRespectsPurgeLabel`, `TestRestoreCancelsRunningTurns`, `TestRestoreReapplyBounded`.
8. **AB-07:**
   - Idle gc menghitung hanya journal aktif yang lease-nya `alive()`.
   - Journal yatim dibersihkan saat open DB dan oleh `serve` tiap 60 s. Auto-continue hanya oleh frontend asal sesi; selain itu `deferred`.
   - Test: `TestGCIdleIgnoresOrphanJournal`.
9. **AB-08:**
   - `CheckLease` dijalankan sebelum setiap LLM call, sebelum setiap dispatch tool, dan sebelum commit hasil. Tool file in-process memeriksa ulang hash di bawah path lock.
   - Residu dinyatakan jujur: ≤ 1 LLM call **atau** ≤ 1 start tool basi per turn; `exec` yang sudah start dilaporkan `outcome_unknown`.
10. **AB-09:**
    - `payload.session_ref` diganti tabel append-only `auto_session_refs` (FK + guard P); dihapus purge langkah 2.
    - Kunci `purge_notice:*:<txid>` dihapus di batch final, ditambah housekeeping 7 hari untuk purge yang `aborted`.
    - Test: `TestPurgeLeavesNoSessionRef`.
11. **Skenario baru dari review ulang** (#35–#42, SPEC §11.11.13): dua restore bersamaan; purge saat barrier; SIGSTOP **di dalam** transaksi tulis; disk penuh di ledger/batch; OS tanpa `boot_id`; time namespace; jawaban approval untuk turn yang sudah selesai; snapshot di tengah purge dipulihkan di data dir lain.
    - **#37** adalah residu yang tidak bisa diselesaikan di level aplikasi: SQLite tidak bisa merebut lock dari proses yang terhenti. Mitigasinya error typed `db_busy` + laporan `db_write_stalled` + daftar pemegang, tanpa pembunuhan otomatis.

**Verifikasi DDL.** Delapan blok SQL §11 (0001–0008) dieksekusi di SQLite 3.53.4 dengan fungsi `dmb_*` terdaftar: 0 error. Hasil trigger: 86 total = 36 `guard_*` + 37 P9 ber-`IS NOT 'purge'` + 13 lainnya (FTS, `gwin_update_guard`, dan 6 no-UPDATE/no-DELETE tanpa pengecualian: `sessions`, `blobs`, `kanban_task_events` ×2, `approval_responses`, `gateway_conversations`). Graf FK: 49 kolom. Semua kolom FK ke induk yang terjangkau sesi diperiksa guard INSERT, dan semua tabel mutable punya guard UPDATE atau no-UPDATE. Uji fungsional 41/41 kasus lolos, termasuk mode NULL/`'gc'`/`'PURGE'`/`''` (fail-closed), mode `'restore'` (guard P dan P9 aktif, F dilewati), cek sesi-sama, kotak masuk approval, `auto_session_refs`, purge mini, `foreign_key_check` = 0, dan `schema_version` tetap.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900; masuk budget `ctxdb`). Ukuran binary: 0. Dependency: 0.
- DDL: +2 tabel (`approval_responses` mutable, `auto_session_refs` append-only), +2 indeks, +3 guard, +2 trigger no-UPDATE, +2 trigger P9; 37 trigger P9 + 36 guard memakai `IS NOT`.
- Di luar DB: +2 file (`run/maint.owner`, `run/maint.holders/`).
- Latensi: +1 lookup PK `events` pada 5 tabel ber-cek sesi-sama; `CheckLease` +1 lookup per dispatch tool. Semuanya termasuk target `Store.Append` ≤ 10 ms p99 [INFERENCE: µs].
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Satu model yang bisa diuji per state machine dan per invarian, dan satu tabel penulis × mode.
- (+) Bagian lain tidak lagi mendefinisikan ulang aturan.
- (−) §11.11 panjang (± 150 baris) dan wajib diperbarui pada setiap perubahan jalur tulis; `TestConcurrencyDocRefs` dan `TestSessionGuardsCoverFK` menjaganya.
- (−) Residu #37 (SIGSTOP di dalam transaksi) hanya dilaporkan, tidak diselesaikan.

## ADR-044 — Penutupan AC-01..AC-09: approval per permintaan, liveness tanpa jam, permukaan diperkecil

**Status:** Accepted 2026-10-18; `schema_newer` pada `serve`, otoritas/edit/nasib baris gagal di kotak masuk, jalur sinkron `takeover_ok()`, SIGTSTP tool, dan pelaporan `db_write_stalled` **dikoreksi ADR-045**. Menutup AC-01 (MAJOR), AC-02..AC-09 (MINOR), dan residu AB-06 dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-043", serta skenario #43–#51. Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-044.

**Konteks.** Pada putaran sebelumnya, setiap mekanisme baru membawa satu MAJOR baru. Aturan putaran ini:

- tutup temuan tanpa menambah mekanisme di luar yang diminta;
- bila ada pilihan, ambil yang menghapus kompleksitas;
- review adversarial setiap mekanisme yang diubah.

**Keputusan** (semua normatif di SPEC §11.11; bagian lain hanya merujuk).

1. **AC-01: approval lintas proses per permintaan** (§11.11.10 "Approval lintas proses"). Kolom baru di `approval_responses`: `request_id` (`UNIQUE`), `preview_sha`, `edited_args` (CHECK hanya untuk `edit`).
   - `approval_request` memuat `request_id`, `preview_sha`, `channels[]`, dan `expires_at`.
   - Penjawab sah = pemilik sesi atau owner. Token `mcp serve` wajib scope baru `approve`. Kanal penjawab harus ∈ `channels[]`, kecuali owner yang menjawab lintas kanal (dicatat).
   - Konsumen tunggal = pemegang lease turn. Dalam satu transaksi keputusan konsumen memvalidasi ulang, memvalidasi P3 untuk `edit`, INSERT `approvals`, lalu DELETE jawaban. Jawaban basi, terlambat, tidak sah, atau dengan preview berbeda diabaikan dan dicatat.
   - **Penyederhanaan:** kotak masuk tidak menerima `allow_session`.
   - Invarian baru I15. Test: `TestApprovalInboxStaleResponseIgnored`, `TestApprovalInboxUnauthorizedResponder`, `TestApprovalEditRevalidated`, `TestApprovalResponseAfterTurnEnd`.
2. **AC-02:** `owner_start_ns` → `owner_start` = cap start opaque dari OS, dibandingkan persis tanpa konversi jam. Linux `starttime`; macOS `pbi_start_tvsec/usec` dan Windows `CreationTime` bertanda [INFERENCE, F1]. Bila API gagal, pemilik dianggap ada.
3. **AC-03 + residu AB-06:**
   - Reapply fase 0 berjalan di mode `'purge'` dengan lease purge baru milik pemilik barrier. Kalimat reapply berurutan yang lama dihapus.
   - Satu semantik pelepasan per jenis lease: lease sesi = DELETE; lease purge dan poller = `expires_at = 0`.
   - Field `value` lease purge = field `alive()`; ada baris pelepasan normal di state machine purge.
   - `poller_lease` dinilai dengan `alive()`/`takeover_ok()`.
   - Lima skenario tanpa test diberi nama test: `TestRestoreConcurrent`, `TestDbWriteStalledReport`, `TestPurgeDiskFull`, `TestApprovalResponseAfterTurnEnd`, `TestRestoreForeignDataDirPurgesLabeled`.
4. **AC-04:** satu aturan `takeover_ok()` untuk ketiga lease. Pemilik yang terbukti tidak ada → ambil alih seketika. Selain itu wajib dua pengamatan kedaluwarsa berjarak ≥ 3 s dengan `heartbeat_at` tetap, sehingga laptop yang bangun dari sleep tidak membatalkan turn yang masih hidup.
5. **AC-05:** trigger `sessions_no_delete` dan `blobs_no_delete` (`WHEN dmb_maint() IS NOT 'purge'`). P9 = 39 trigger.
6. **AC-06:** I13 dipersempit ke **rujukan terstruktur** (kolom FK, `auto_session_refs`, kunci `module_state`). Payload JSON bebas = anonimisasi best-effort, didokumentasikan. Opsi pemindaian payload ditolak karena menambah mekanisme.
7. **AC-07:** migrasi oleh binary baru = pemeliharaan barrier yang sama (`reason = migrate`), bukan mekanisme baru. Setiap transaksi tulis memeriksa `user_version` bersama `os.SameFile`; skema lebih baru → `schema_newer` (kode baru, exit 1) dan proses menjadi read-only. Test: `TestMigrationWhileOldBinaryRuns`.
8. **AC-08:** `mcp serve messages_send` = sesi pendek milik `mcp serve` (lease, journal `awaiting_approval`, kotak masuk, `approvals`, delivery). **Tidak ada jalur tulis baru**; tanpa `serve` hidup → `module_unavailable` sebelum menulis. Test: `TestMCPServeMessagesSendUnderGuards`.
9. **AC-09:**
   - `db_write_stalled` didefinisikan: `db_busy` ≥ 30 s berturut-turut → event + notifikasi `warn` + temuan `D-DB-STALL`, diulang ≤ 1×/jam.
   - TUI/ACP/`run` menangani SIGTSTP: transaksi diselesaikan atau di-rollback, DB ditutup, lock bersama dilepas, baru proses berhenti (`TestSigtstpReleasesLocks`).
   - Residu #37 menyempit ke SIGSTOP, debugger, dan hang di dalam transaksi.

**Review adversarial mekanisme yang diubah/ditambah di putaran ini.**

| Mekanisme | Siapa boleh memanggil / identitas | Ganda | Terlambat / basi | Crash di tengah | Uji |
|---|---|---|---|---|---|
| Kotak masuk approval (`request_id`) | Penjawab: proses tanpa lease (`mcp serve` scope `approve`, `serve`). Identitas = pemilik sesi/owner, kanal ∈ `channels[]` atau owner lintas kanal. Konsumen: hanya pemegang lease turn (guard F pada `approvals`) | `UNIQUE(request_id)` → jawaban kedua ditolak DB; dua penjawab sah → yang pertama menang (#53) | `request_id` bukan yang menunggu atau lewat `expires_at` → diabaikan + log; baris yang lolos dihapus konsumen/housekeeping (#41, #43) | Konsumen crash sebelum COMMIT → rollback, jawaban tetap, pengambil alih mengonsumsi sekali (#54); penjawab crash setelah INSERT → baris tetap sah | DB: 8 kasus direproduksi; konsumen: 7 kasus simulasi; `TestApproval*` |
| `edited_args` | Hanya dari penjawab sah; dipakai hanya oleh konsumen | — | divalidasi saat konsumsi, bukan saat INSERT | — | `TestApprovalEditRevalidated` (#55) |
| Cap start opaque `owner_start` | Dibaca OS untuk proses mana pun; ditulis pemilik saat akuisisi | — | Tidak menua; step jam tidak mengubahnya [INFERENCE macOS/Windows, F1] | API gagal → dianggap ada (aman: hanya menunda ambil alih) | `TestLeaseLivenessClockStep` (#44) |
| `takeover_ok()` (dua pengamatan ≥ 3 s) | Semua pengambil alih: akuisisi lease, pemindai yatim, resume purge, poller | Dua pengambil alih → CAS/`BEGIN IMMEDIATE`, tepat satu yang menang | Pemilik yang bangun ≤ 2 s me-heartbeat → `heartbeat_at` berubah → tidak diambil alih (#46); pemilik yang benar-benar mati → seketika | Pengambil alih crash di antara dua pengamatan → tidak ada perubahan | `TestLeaseLivenessClockStep` kasus sleep |
| Reapply fase 0 mode `'purge'` | Hanya pemilik barrier eksklusif, dengan lease purge baru | Dua restore → `maint.owner` (#35) | Berbatas `expected_s`; sisanya resume biasa | Crash → barrier basi dibersihkan (I7); label yang sudah ditulis otoritatif (I1) | `TestRestoreReapplyBounded` (#45) |
| `sessions_no_delete`, `blobs_no_delete` | Hanya koneksi `'purge'` | — | — | — | DB: 8 kasus (#47) |
| Barrier `migrate` + cek `user_version` | Hanya binary baru pemilik barrier; semua proses memeriksa di awal transaksi | Dua binary baru → `maint.owner` | Binary lama yang start saat migrasi → terblok urutan buka, lalu `schema_newer` (#57) | Crash di tengah migrasi → transaksi migrasi rollback (§11.1), barrier basi dibersihkan | `TestMigrationWhileOldBinaryRuns` |
| Sesi pendek `messages_send` | Hanya `mcp serve` dengan scope `send` + allowlist target | Retry klien MCP → `request_id` baru per permintaan; idem delivery lewat `idem_key` | Approval kedaluwarsa → `deny`, sesi ditutup | Crash → journal yatim → pembersihan; delivery belum dibuat | `TestMCPServeMessagesSendUnderGuards` (#50) |
| Handler SIGTSTP | Proses sendiri | — | SIGCONT setelah lama → `CheckLease` → `lease_fenced` bila diambil alih (#56) | Rollback = pilihan aman | `TestSigtstpReleasesLocks` (#51) |
| `db_write_stalled` | Hanya `serve` | Diulang ≤ 1×/jam | — | — | `TestDbWriteStalledReport` |

**Verifikasi DDL.** Delapan blok SQL §11 dieksekusi di SQLite 3.53.4 (`/tmp/adr044.db`) dengan fungsi `dmb_*` terdaftar: 0 error. Trigger: 88 = 36 guard + 39 P9 + 13 lainnya. FK: 49 kolom. Uji fungsional 36/36 lolos, mencakup:

- fencing;
- cek sesi-sama;
- AC-05: DELETE `sessions`/`blobs` ditolak di mode `''`, NULL, `'gc'`, `'restore'` dan diizinkan di `'purge'`;
- AC-01 di DB: jawaban ganda ditolak `UNIQUE`; `allow_session` dan `edit` tanpa/berlebih `edited_args` ditolak CHECK; UPDATE ditolak;
- AC-01 di konsumen (simulasi Python atas DB yang sama): jawaban basi, preview salah, penjawab tidak sah, `edit` tidak valid → `deny`, dan jawaban terlambat untuk permintaan berikutnya; semuanya diabaikan, dan kotak masuk kosong setelahnya;
- label `purge_state` ditolak di mode `'restore'` dan diizinkan di `'purge'` (AC-03);
- mode NULL/`'gc'`/`'PURGE'` fail-closed;
- `foreign_key_check` = 0.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900). Ukuran binary: 0. Dependency: 0.
- DDL: kolom `owner_start_ns INTEGER` → `owner_start TEXT`; `approval_responses` +3 kolom −1 nilai `decision`; scope token +`approve`; +2 trigger P9.
- Kode error: +1 (`schema_newer`).
- Mekanisme baru: 0. Barrier, lease, journal, dan kotak masuk yang sudah ada dipakai ulang; `takeover_ok()` adalah aturan di fungsi liveness yang sama.
- Latensi: +1 baca `user_version` per transaksi tulis (header halaman 1, sudah di cache) [INFERENCE].
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Approval jarak jauh tidak lagi bisa menyetujui aksi yang preview-nya tidak dilihat penjawab.
- (+) Liveness benar-benar bebas jam di ketiga OS (dengan dua [INFERENCE] yang diverifikasi di F1).
- (+) Restore, reapply, dan migrasi berbagi satu barrier.
- (−) Ambil alih lease pemilik yang masih ada tertunda ≥ 3 s.
- (−) Id sesi di payload JSON bebas tidak dijamin terhapus purge (didokumentasikan).
- (−) `messages_send` membutuhkan `serve` hidup.

## ADR-045 — Penutupan AD-01..AD-08 tanpa mekanisme baru

**Status:** Accepted 2026-10-18; durabilitas isi inbound, waktu exit 75 `serve` lama, aturan spool, `idempotency_conflict`, dan group tool tanpa pengawas **dikoreksi ADR-046**. Menutup AD-01 (MAJOR), AD-02..AD-08 (MINOR), residu AC-03, dan skenario #58–#64 dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-044". Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-045.

**Aturan putaran.**
- Tidak ada mekanisme baru selain yang diminta.
- Bila ada pilihan, ambil yang lebih sederhana.
- Setiap mekanisme yang diubah di-review adversarial (siapa memanggil/identitas, ganda, terlambat/basi, crash di tengah, uji).

**Keputusan** (normatif di SPEC §11.11; bagian lain hanya merujuk).

1. **AD-01 — `serve` lama saat migrasi.**
   - Pendaftar `run/maint.holders/<pid>` memuat `{kind, version, schema_max, owner_start}`.
   - Migrasi `reason = migrate` hanya berjalan bila tidak ada pendaftar hidup ber-`schema_max` lebih lama, kecuali `--force-migrate` (flag global).
   - `serve` lama yang melihat marker `migrate` menyelesaikan atau me-rollback transaksinya, menulis inbound yang belum ter-commit dan tidak akan dikirim ulang platform ke spool append-only `run/inbound_spool/*.jsonl` (format payload mentah, tanpa skema DB), lalu **exit 75**. Supervisor menjalankan binary baru, yang meng-ingest spool sebelum polling.
   - Bila binary di disk ternyata masih lama: exit 78 + notifikasi (residu #67).
   - TUI/ACP/`run`/CLI lama menahan migrasi; dengan `--force-migrate` mereka menjadi read-only `schema_newer`.
   - Test: `TestMigrationWhileOldServeRuns`, `TestMigrationWhileOldBinaryRuns`.
2. **AD-02 — `edit` jarak jauh.** Dipilih aturan yang lebih sederhana dari klasifikasi-banding: `edit` jarak jauh **selalu** menghasilkan permintaan baru (`request_id` baru, preview + dry-run baru, kelas diklasifikasi ulang), tidak pernah dieksekusi langsung. Ini mencakup kasus eskalasi ke kelas P26. Klien `mcp serve` tidak bisa `edit` (CHECK DB). Test: `TestApprovalRemoteEditRequiresNewPreview`.
3. **AD-03 — protokol kotak masuk.**
   - Baris yang gagal validasi untuk permintaan yang sedang menunggu = **`deny` tercatat** dengan alasan, dan `request_id` terkonsumsi (fail-closed, sesuai arahan).
   - Jawaban untuk permintaan yang tidak menunggu tidak pernah tersimpan: guard INSERT memeriksa journal `awaiting_approval` dan `request_id` (`approval_not_pending`, kode baru). Ini memakai guard yang sudah ada, dan menghapus kebutuhan pembersihan baris basi di konsumen.
   - Konsumen memeriksa ulang token (`responder_token` → scope `approve`, belum dicabut, identitas sama).
   - Otoritas per kelas tool = P25/§14.1 (kelas berbahaya → owner saja), diperiksa kedua sisi.
   - `request_id` tetap sama saat replay.
   - `expires_at` memakai jam dinding UTC, dengan alasan tertulis (permintaan bertahan lintas reboot; step jam hanya menggeser timeout).
   - Satu tabel timeout di §14.1 menggantikan "tak terbatas interaktif".
   - Housekeeping hanya menghapus baris permintaan yang tidak menunggu.
   - Test: `TestApprovalReplayKeepsRequestId` + test AC-01 yang diperbarui.
4. **AD-04.**
   - Kasus (b) `takeover_ok()` tidak pernah ditunggu di jalur sinkron (open DB, fase 0 purge); diselesaikan pemindai latar (P12).
   - Fase 0 mengeluarkan sesi yang lease-nya tidak `takeover_ok()`.
   - Jarak 3 s diukur dengan jam monotonik tanpa suspend (Linux `CLOCK_MONOTONIC`, macOS `mach_absolute_time`, Windows `QueryUnbiasedInterruptTime`).
5. **AD-05.** Handler SIGTSTP mengirim SIGSTOP ke process group tool sebelum menghentikan diri. Saat SIGCONT: lease masih milik → SIGCONT ke group; sudah diambil alih → SIGKILL ke group + `lease_fenced`. Pengambil alih tunduk P22 (tidak menjalankan ulang `non_idempotent` tanpa konfirmasi). Test: `TestSigtstpStopsToolGroup`.
6. **AD-06.** `db_write_stalled` dilaporkan tanpa tulis DB: log ERROR, stderr, banner, `run/db_stall.json` untuk `doctor`, dan pesan owner langsung ke API adapter tanpa outbox (best-effort, ≤ 1×/jam). Event DB ditulis setelah stall berakhir. Pemegang lock disebut "kemungkinan".
7. **AD-07.** `messages_send` menerima `idempotency_key` dari klien (`approval_request.client_key` + `idem_key` delivery). Pembatalan, timeout, atau transport tertutup sebelum keputusan → `deny{cancelled}` sebelum delivery dibuat. Test: `TestMCPServeMessagesSendCancel`, `TestMCPServeMessagesSendIdempotent`.
8. **AD-08 + residu AC-03.**
   - SPEC.md:1621 merujuk ke definisi `value` tunggal di §11.11.6.
   - Label status skenario = "hasil review ADR-045".
   - `owner_start = ''` = tidak diketahui → pemilik dianggap ada.
   - **`docs/TRUST-MODEL.md` dibuat**: tabel aktor, batas vs heuristik, residu. Ditambahkan ke indeks SPEC, dan isi wajibnya ditulis di GAP-SEC-28.

**Review adversarial mekanisme yang diubah.**

| Mekanisme | Siapa / identitas | Ganda | Terlambat / basi | Crash di tengah | Uji |
|---|---|---|---|---|---|
| Guard `approval_not_pending` | Semua INSERT kotak masuk (proses tanpa lease) | `UNIQUE(request_id)` | Ditolak DB bila `request_id` tidak menunggu (#41, #43, #59) | Journal yang di-rollback tetap `awaiting_approval` → jawaban tetap sah | DB: 3 kasus |
| Validasi konsumen + `deny` fail-closed | Hanya pemegang lease turn (guard F pada `approvals`/`turn_journal`) | — | Token dicabut setelah INSERT → `deny{scope}` (#66) | Rollback → jawaban tetap; pengambil alih mengonsumsi sekali | Simulasi: 4 kasus |
| `edit` jarak jauh → permintaan baru | Owner interaktif (web/gateway); bukan `mcp_serve` (CHECK) | Jawaban untuk permintaan lama ditolak DB setelah superseded | `request_id` baru membatalkan jawaban lama | Rollback → permintaan lama tetap menunggu | DB + simulasi: 3 kasus |
| Spool inbound `serve` lama | Hanya `serve` yang keluar karena `migrate`; dibaca hanya `serve` baru saat start | Ingest dengan dedupe PK `gateway_inbound` | File spool bertahan sampai di-ingest | Crash saat menulis spool → baris JSONL terakhir terpotong diabaikan; inbound yang belum masuk spool belum di-ack ke platform | `TestMigrationWhileOldServeRuns` |
| Aturan migrasi menunggu proses lama | Migrator = pemilik `maint.owner`; `--force-migrate` eksplisit | Dua migrator → `maint.owner` | Proses lama yang start saat migrasi terblok urutan buka (#57) | Crash migrator → transaksi migrasi rollback, barrier basi dibersihkan (I7) | `TestMigrationWhileOldBinaryRuns` |
| `takeover_ok()` latar + jam tanpa suspend | Pemindai `serve`/goroutine latar | CAS | Pengamatan yang mengapit sleep tidak dihitung | Tanpa efek sampai ambil alih | `TestLeaseLivenessClockStep` |
| SIGTSTP → SIGSTOP group tool | Proses sendiri atas group tool miliknya | — | SIGCONT setelah diambil alih → SIGKILL group (#62) | Proses mati saat ter-stop → group ikut dibunuh lewat kill-on-close/pgid | `TestSigtstpStopsToolGroup` |
| `db_write_stalled` tanpa tulis DB | Hanya `serve` | ≤ 1×/jam | Event DB setelah pulih | File `db_stall.json` ditimpa atomik | `TestDbWriteStalledReport` |
| `idempotency_key` `messages_send` | Klien `mcp serve` scope `send` | Key sama → permintaan/delivery yang sama | Cancel setelah delivery dibuat tidak membatalkan (dilaporkan) | Crash sebelum keputusan → journal yatim → `deny` saat kedaluwarsa | `TestMCPServeMessagesSend*` |

**Verifikasi DDL.** Delapan blok SQL §11 dieksekusi di SQLite 3.53.4 (`/tmp/adr045.db`): 0 error. Trigger: 88 = 36 guard + 39 P9 + 13 lainnya. FK: 49 kolom. Uji fungsional 28/28 lolos, mencakup:
- `approval_not_pending` untuk `request_id` yang tidak menunggu dan untuk turn tanpa journal;
- CHECK `mcp_serve` tanpa `edit`, serta `responder_token` iff `mcp_serve`;
- `UNIQUE`;
- simulasi konsumen: token dicabut → `deny{scope}`; non-owner untuk kelas `irreversible` → `deny{unauthorized}`; `edit` jarak jauh → permintaan baru dan jawaban lama ditolak DB; `edit` tidak valid → `deny`; kotak masuk kosong;
- AC-05, fencing, guard P;
- mode NULL/`'gc'`/`'PURGE'`/`'restore'` fail-closed untuk P9 dan guard P;
- `foreign_key_check` = 0.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900). Ukuran binary: 0. Dependency: 0.
- DDL: +1 kolom (`approval_responses.responder_token`), +2 CHECK, +1 pernyataan di guard yang sudah ada.
- Kode error: +1 (`approval_not_pending`). CLI: +1 flag global (`--force-migrate`).
- Di luar DB: spool `run/inbound_spool/` dan `run/db_stall.json`; field pendaftar barrier yang sudah ada diperluas.
- Mekanisme baru: 0. Dokumen baru: `docs/TRUST-MODEL.md`, deliverable GAP-SEC-28 yang sudah dijanjikan.
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Upgrade lewat package manager tidak lagi membekukan `serve`.
- (+) Approval jarak jauh tidak pernah mengeksekusi argumen yang preview-nya belum dilihat.
- (+) Ctrl-Z tidak lagi membiarkan tool berjalan.
- (−) TUI lama yang terbuka menahan migrasi sampai ditutup, atau sampai `--force-migrate` dipakai.
- (−) `edit` jarak jauh selalu membutuhkan satu persetujuan tambahan.
- (−) `serve` dengan binary lama di disk berhenti dengan exit 78 sampai binary diperbarui (#67).

## ADR-046 — Isi inbound durable, exit `serve` lama setelah COMMIT, aturan spool tunggal

**Status:** Accepted 2026-10-18. Menutup AE-01 (MAJOR), AE-02..AE-06 (MINOR), residu AD-01, dan skenario #69–#74 dari [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-045". Status SPEC tetap **kandidat freeze**: freeze (ADR-030) berlaku setelah auditor memverifikasi ADR-046.

**Aturan putaran.** Tidak ada mekanisme baru. Setiap perubahan memakai tabel, guard, lease, barrier, atau spool yang sudah ada, dan di-review adversarial.

**Keputusan.**

1. **AE-01 — isi pesan masuk durable.**
   - `gateway_inbound.payload` (JSON ter-normalisasi, setelah redaksi §14.3, dicakup `storage.encrypt`) diisi pada INSERT yang sama dengan dedupe dan offset. CHECK: baris antre selalu membawa isi.
   - Lampiran dirujuk `blob_refs` owner baru `gateway_inbound` (sesi `__system__`, hidup selama antre).
   - Antrean kerja per chat **dibaca dari DB** (`idx_gw_inbound_queue`); memori hanya cache. Status turunan, tanpa kolom baru: `queued` = `event_id IS NULL`; `processing` = `event_id` terisi + journal terbuka; `done`. Pekerja = pemegang lease sesi chat.
   - `gwin_update_guard`: satu-satunya UPDATE sah = `event_id` NULL→nilai **dan** `payload`→NULL dalam satu UPDATE. Isi pindah ke `events`, bukan duplikat. Overflow REL-11 menghasilkan `system_notice{kind:"inbound_rejected"}`, sehingga baris tidak antre selamanya.
   - Spool hanya jembatan sampai COMMIT; file dihapus setelah COMMIT.
   - SELF-HEALING §5.1 ditulis ulang. Test: `TestInboundQueuedSurvivesRestart`, `TestInboundCrashBetweenCommits`.
2. **AE-02.**
   - Pendaftar lama non-`serve` diperiksa **sebelum** marker `migrate`. Bila ada dan tanpa `--force-migrate` → ditolak tanpa marker, dan `serve` lama terus melayani.
   - `serve` lama tidak keluar karena melihat marker. Ia drain, lalu keluar exit 75 hanya setelah migrasi ter-COMMIT (`schema_newer` setelah membuka ulang). Migrasi dibatalkan → `serve` lama lanjut, durasi drain dilaporkan lewat jalur non-DB.
   - `serve` sebagai migrator yang tertahan → exit 75 berbackoff (tidak dihitung crash-loop) + notifikasi non-DB; `--force-migrate` otomatis setelah 10 menit.
   - Test: `TestMigrationRefusedKeepsOldServe`, `TestServeMigratorForcesAfter10Min`.
3. **AE-03 — spool, satu aturan.**
   - Hanya untuk push yang wajib di-ack saat DB tidak bisa ditulis atau saat exit terencana apa pun (SIGTERM/0, 75, 78); long-poll cukup tidak memajukan offset.
   - Urutan normatif: tulis → `fsync` (+ dir) → ack.
   - File `0600`, AEAD per baris dengan kunci `storage.encrypt`, nama `<boot_id>-<pid>-<owner_start>-<seq>`.
   - Ingest urut nama/baris dengan dedupe PK; baris terpotong diabaikan.
   - Test: `TestInboundSpoolOrderAndAck`.
4. **AE-04.** `approval_request.args_sha` disimpan. Key sama + argumen berbeda → **`idempotency_conflict`** (kode baru, exit 1), tanpa menempel dan tanpa permintaan baru. Setelah delivery dibuat, kesamaan diperiksa terhadap `chat_id`/`body`/`media_blob_sha`.
5. **AE-05.**
   - `turn_journal.tool_pgid` + `tool_pgid_start` dicatat saat dispatch `exec`. Pengambil alih di host yang sama membunuh group (setelah verifikasi cap start leader) sebelum recovery.
   - Linux memakai `cgroup.kill` bila tool berjalan di cgroup sub-group. Klaim kill-on-close dibatasi ke Windows Job Object.
   - Residu macOS (`setsid` lolos) dicatat di TRUST-MODEL.
   - Test: `TestOrphanToolGroupKilled`. Salinan DDL journal di SELF-HEALING tetap SQL-identik.
6. **AE-06.** Teks #55 dan #67 diperbarui sesuai protokol yang berlaku.

**Review adversarial perubahan.**

| Perubahan | Siapa / identitas | Ganda | Terlambat / basi | Crash di tengah | Uji |
|---|---|---|---|---|---|
| `payload` + antrean dari DB | Poller/penerima (INSERT, `__system__`); worker = pemegang lease sesi chat (UPDATE, guard F lewat `events`) | PK `(platform, external_id)` + `INSERT OR IGNORE`; UPDATE bersyarat `event_id IS NULL` → 0 baris = sudah diproses | Worker basi (lease diambil alih) ditolak guard F pada `events` | Crash di antara COMMIT → baris tetap `queued` beserta isi (#75); kill -9 / exit 75 / graceful stop (#69) | DB: 7 kasus |
| `gwin_update_guard` diperketat | Semua koneksi kecuali purge (DELETE) | UPDATE kedua ditolak | — | — | DB: 4 kasus (#77) |
| Owner `gateway_inbound` di `blob_refs` | Poller | `UNIQUE(owner_kind, owner_id, ref_key, sha256)` | Hidup hanya selama antre; gc tidak menghapus lampiran yang masih antre | Lampiran dipublikasi di transaksi INSERT yang sama | DB: 2 kasus |
| Cek pendaftar sebelum marker | Migrator (pemilik `maint.owner`) | Dua migrator → `maint.owner` | Proses lama yang start setelah cek → terblok urutan buka (#57), dicek ulang di `draining` | Crash migrator → tidak ada marker / barrier basi dibersihkan (I7) | `TestMigrationRefusedKeepsOldServe` |
| Exit 75 `serve` lama setelah COMMIT | `serve` lama sendiri | — | Migrasi batal → lanjut melayani | Crash saat drain → supervisor restart; antrean di DB | `TestMigrationWhileOldServeRuns` |
| `--force-migrate` otomatis 10 menit | Hanya `serve` sebagai migrator | — | Proses lama → read-only `schema_newer` | — | `TestServeMigratorForcesAfter10Min` (#78) |
| Spool tulis → fsync → ack | Penerima push | Ingest dengan dedupe PK | Spool dari proses mati di-ingest `serve` berikutnya | Sebelum fsync → tidak di-ack (platform mengirim ulang); sesudah fsync → tepat sekali | `TestInboundSpoolOrderAndAck` (#72) |
| `idempotency_conflict` | Klien `mcp serve` scope `send` | Key + argumen sama → menempel | Setelah delivery → bandingkan ke delivery | — | `TestMCPServeMessagesSendIdempotent` (#73) |
| `tool_pgid` kill | Pengambil alih di host sama | — | Cap start leader beda (PID reuse) → tidak dibunuh | Pengambil alih crash sebelum kill → pengambil alih berikutnya | `TestOrphanToolGroupKilled` (#74) |

**Verifikasi DDL.** Delapan blok SQL §11 dieksekusi di SQLite 3.53.4 (`/tmp/adr046.db`): 0 error. Trigger: 88 = 36 guard + 39 P9 + 13 lainnya. FK: 49 kolom. DDL `turn_journal` SPEC = SELF-HEALING setelah normalisasi komentar dan spasi. Uji fungsional inbound 17/17 lolos:
- INSERT antre tanpa `payload` ditolak CHECK; duplikat diserap;
- crash di antara COMMIT poller dan worker → 5 baris tetap antre;
- setelah "restart" (state memori dibuang), worker membaca DB → 5 `user_message` dengan isi asli, urut;
- `payload` dikosongkan; lampiran pindah ke `event_attachments`;
- UPDATE kedua, pengisian `event_id` tanpa NULL-kan `payload`, perubahan `payload`, dan DELETE ditolak;
- owner `gateway_inbound` diterima `blob_refs`;
- worker ke sesi berlabel ditolak guard P dan baris tetap antre (#76);
- P9 fail-closed; `foreign_key_check` = 0.

**Dampak terukur (SPEC §20).**
- Token: 0. LOC: 0 (Σ §3.2.4 tetap 184.900). Ukuran binary: 0. Dependency: 0.
- DDL: +1 kolom (`gateway_inbound.payload`) + CHECK, +1 indeks parsial, +1 nilai `owner_kind`, +2 kolom `turn_journal`; guard UPDATE diperketat.
- Kode error: +1 (`idempotency_conflict`). Mekanisme baru: 0.
- Disk: isi pesan antre disimpan sampai diproses (lalu dikosongkan).
- Tidak ada baris SCORECARD/MITIGATION yang berubah.

**Konsekuensi.**
- (+) Tidak ada pesan masuk yang hilang pada restart apa pun setelah COMMIT.
- (+) `serve` lama tidak pernah keluar untuk migrasi yang tidak terjadi.
- (+) Spool punya urutan, enkripsi, dan cakupan yang jelas.
- (−) TUI lama yang dibiarkan terbuka menunda migrasi `serve` sampai 10 menit; sesudahnya TUI lama menjadi read-only.
- (−) Residu macOS untuk turunan tool yang lepas dari process group.

## ADR-047 — Spec 1.0 freeze

**Status:** Accepted 2026-10-18. Mengunci Spec 1.0 setelah [`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-046" menyatakan **"Freeze sah: YA"** (0 BLOCKER, 0 MAJOR, 8 MINOR AF-01..AF-08). Memenuhi syarat freeze ADR-030.

**Keputusan.**

1. **Freeze.** SPEC.md:3 → "Spec 1.0 — FROZEN (2026-10-18)"; SPEC §20 mencatat tanggal dan rujukan verifikasi. Sejak tanggal ini setiap perubahan spec hanya lewat ADR baru (§20). ADR ini tidak mengubah isi teknis apa pun.
2. **Errata backlog pasca-freeze** (dikerjakan di awal F0, masing-masing lewat ADR errata; deliverable F0 "Errata backlog ADR-047"). Isinya **semua MINOR yang masih terbuka**:
   - backlog ADR-037 (MINOR yang tidak dikerjakan ADR-037 dan ADR berikutnya);
   - MINOR berstatus terbuka dari putaran V, W, X, Y, Z, AA, AB, AC, AD, AE di `REVIEW-1.0.md`;
   - AF-01..AF-08 (`REVIEW-1.0.md` §"Verifikasi ADR-046"). **AF-01** (`INSERT OR IGNORE` dapat membuang payload tidak valid secara diam) dan **AF-02** (`turn_start` di luar transaksi worker, sehingga pesan bisa tak terjawab) **wajib selesai sebelum implementasi gateway** dimulai (F4).
   Status per item dilacak di artefak backlog F0 (ID temuan → ADR penutup → test); item tanpa ADR penutup memblok gate F0.
3. **Persistence (§11.11) = "desain final, wajib dibuktikan di F0"** (keputusan user). Bukti berupa *persistence spike* F0:
   - prototipe Go `ctxdb` + lease/fencing + purge + gc di atas DDL SPEC §11 (termasuk `0008_guards.sql`);
   - skenario adversarial **#1–#86** sebagai test otomatis (#1–#78: SPEC §11.11.13; #79–#86: `REVIEW-1.0.md` §"Verifikasi ADR-046"), 1 test per skenario, status tidak boleh "manual";
   - chaos kill -9 dan `TestMultiProcess`, ditambah fuzz pada urutan operasi lintas proses.
   Temuan spike yang mengubah desain = ADR baru (§20); tanpa itu desain §11.11 tidak berubah.
4. **Review penuh area non-persistence** oleh auditor (semua bagian di luar §11.11 dan dokumen persistence) **sebelum F1 dimulai**. Gate ROADMAP: F1 tidak dimulai sebelum review itu selesai dan temuan BLOCKER/MAJOR-nya ditutup lewat ADR.

**Amandemen 2026-10-20 (REVIEW-F1-SEC8 LP8-1..8).** (1) Hasil tool yang selesai saat ctx batal tetap ditulis sebagai `tool_result` sebelum stop `cancelled`/`cancelled_confirmed`. (2) Wait batch read dibatasi `KillGrace` (`waitTask`), sehingga Cancel tidak menggantung. (3) `allow_session` tidak pernah di-cache untuk subject kosong. (4) `Prepared.Approved` diset setelah Approver mengizinkan, untuk diteruskan adapter ke `DispatchPrepared`. (5) `no_progress` ditunda (`turn.pending`) sampai semua hasil batch tercatat. (6) Stream delta, assistant message, dan tool result di-redaksi sebelum event/stream. (7) Preview spill/truncate dihitung ke budget inline turn. (8) `StopDetail` menambah `max_wall` dan `max_iterations`. Regression: `TestLP81..TestLP88`.

**Dampak terukur (SPEC §20).** Token 0, latensi 0, LOC 0 (Σ §3.2.4 tetap 184.900; spike = tooling/test F0), ukuran binary 0, dependency 0. Baris SCORECARD/MITIGATION tidak berubah. Migrasi: tidak ada. ROADMAP F0 bertambah 2 deliverable + 1 gate.

**Konsekuensi.**
- (+) Spec terkunci; perubahan berikutnya dapat dilacak per ADR.
- (+) Risiko utama (persistence) dibuktikan dengan kode sebelum fitur dibangun di atasnya.
- (−) F0 lebih panjang (spike + errata backlog + review non-persistence).

## ADR-048 — Budget waktu dinding di job terpisah tanpa `-race`

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-P08 (`docs/F0-REPORT.md` §"Temuan spec"). *(Butir 1 dan 3 dikoreksi ADR-073: `make test-timing` juga menjalankan benchmark sekali (`-bench=. -benchtime=1x`), budget absolut benchmark termasuk `BenchmarkAppend*` ditegakkan di job `perf`, dan helper pindah ke `internal/testutil/timing` di Deliverable F1 #0.)*

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ENGINEERING E-55 dan urutan job §5.3, SPEC §16 (baris Unit); `Makefile` target `test-timing`.

**Konteks.** E-55 menjalankan semua test dengan `-race`. Di bawah `-race`, mesin SQLite modernc yang terinstrumentasi butuh 175–400 ms per transaksi bahkan untuk unit 1 event; tanpa `-race` transaksi purge terbesar S015 = 51 ms. Akibatnya I11 (setiap transaksi purge ≤ 200 ms, duty lock ≤ 20 %) mengukur detektor race, bukan kode, dan `make test` merah hanya karena S015.

**Keputusan.**
1. Asersi budget waktu dinding (I11 per transaksi + duty di S015, writer tidak terblok > 500 ms oleh proses yang menutup, #26 ≤ 10 s, dan budget waktu lain yang ditambahkan kemudian, termasuk `BenchmarkAppend*`) hanya **ditegakkan** di `make test-timing`: `DAMNBOT_TIMING=1 go test -tags goolm -count=1 -shuffle=on ./...`, **tanpa** `-race`. Job CI `test-timing` memanggil target itu.
2. Di `make test` (`-race`) skenario yang sama tetap berjalan dan memeriksa **semua** invarian kebenaran: 0 `db_busy`, purge selesai, `schema_version` tidak berubah, `integrity_check`, `foreign_key_check`, hash chain. Yang dilewati hanya perbandingan durasi; hasil ukurnya tetap dicatat di log test. Satu pengecualian ukuran: S015 di build `-race` mem-purge korpus 100 event (bukan 5.000), karena unit purge terinstrumentasi tetap beberapa event per transaksi dengan jeda ≥ 4× hold sehingga 5.000 event melewati batas `go test` 10 menit (terukur > 600 s); purge tetap berlangsung dalam puluhan transaksi melawan writer hidup. Korpus 5.000 event dan budgetnya berjalan di `make test-timing`.
3. Satu helper (`timingEnforced`/`checkBudget` di `internal/ctxdb/timing_test.go`) menjadi satu-satunya pintu asersi waktu. `DAMNBOT_TIMING=1` pada build `-race` adalah error test, bukan skip.
4. Meta-test CI punya gate `timing`: operasi nyata 300 ms terhadap budget 200 ms lewat helper yang sama harus membuat `make test-timing` gagal.

**Dampak terukur (SPEC §20).** Token 0, latensi 0, ukuran binary 0, dependency 0, LOC non-test 0. CI: +1 job (`test-timing`). Angka I11 tidak berubah; hanya job penegaknya. ENGINEERING E-55 dan urutan job §5.3 diperbarui.

**Konsekuensi.**
- (+) `make test` hijau tanpa melemahkan cek kebenaran apa pun; budget kinerja diukur di build yang representatif.
- (−) Satu job CI tambahan (suite tanpa `-race`).

## ADR-049 — Budget LOC `ctxdb` 2.800

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-P10.

*(Amandemen 2026-10-18: budget `ctxdb` 2.800 → 3.100 oleh ADR-087.)*

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ENGINEERING §2.4 (tabel budget, `ctxdb` 2.800, total 31.450); `loc.budget`.

**Konteks.** Cakupan SPEC §11 (purge 31 langkah, gc dua fase, barrier, restore, migrasi, recovery journal, lease/fencing) terukur 2.598–2.610 LOC non-test di spike F0, walaupun langkah purge sudah dipindah ke `queries.sql` (tidak dihitung) dan protokol gateway/approval ke test (ADR-053). Budget E-19 2.500.

**Keputusan.** Budget E-19 `ctxdb` (tanpa file `.sql`) = **2.800** (`loc.budget`, tabel ENGINEERING §2.4). Total core per paket 31.150 → **31.450**, tetap ≤ 47.000 (gabungan SPEC §3.2.4 core + codetools + exec + heal). Baris SPEC §3.2.4 dan Σ 184.900 tidak berubah, sehingga cukup revisi tabel ENGINEERING (aturan §2.4); ADR ini mencatat keputusannya.

**Dampak terukur (SPEC §20).** LOC: budget +300 di `ctxdb`; Σ §3.2.4 tetap 184.900 ≤ 190.000. Token/latensi/binary/dependency 0. Headroom `ctxdb` saat ini ≈ 190 LOC.

**Konsekuensi.**
- (+) `make loc` hijau tanpa memecah paket secara artifisial.
- (−) Sisa ruang `ctxdb` kecil; fitur storage F1+ (memory, skills, journal) tetap di paketnya masing-masing (E-03/E-09), bukan di `ctxdb`. *(Dikoreksi ADR-074: storage konsumen ada di `internal/ctxdb/<consumer>store` dengan baris `loc.budget` sendiri; rujukan E-03 seharusnya E-04.)*

## ADR-050 — E-70 (rasio test:source) menjadi gate mulai 5.000 LOC

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-03.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ENGINEERING E-70 dan §8, SCORECARD §1.7; `scripts/loc-check.sh`.

**Konteks.** Rasio 0,8–1,2 tidak bermakna pada basis kode kecil yang didominasi test spike (scaffold 121:29; F0 3.391:2.639 = 1,28). Gate merah permanen membuat meta-test-nya tidak bermakna.

**Keputusan.** `make loc-ratio` selalu melaporkan rasio. Gate **gagal** hanya bila total Go non-test ≥ **5.000** LOC (`scc --no-gen`) dan rasio di luar 0,8–1,2. Di bawah ambang target exit 0 dengan baris `LOC ratio report-only: …`. Meta-test membuktikan gate tetap gagal pada pohon sintetis di atas ambang (5.000:5.000 lulus, +2.000 baris test gagal).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. ENGINEERING E-70 dan §8 diperbarui.

**Konsekuensi.**
- (+) Gate bermakna begitu kode produksi cukup besar (≈ F1).
- (−) Di bawah 5.000 LOC rasio hanya terlihat di laporan CI.

## ADR-051 — Toolchain go1.26.9 (GO-2026-6604)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup blocker `vuln` F0 (E-44/E-54).

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ENGINEERING (baris Status, pin `go 1.26.9`); `go.mod`, `bench/go.mod`, `tools/go.mod`.

**Konteks.** `govulncheck` melaporkan GO-2026-6604 (stdlib `os.Root`: `MkdirAll`/`Remove`/`RemoveAll`/`Rename`/`OpenFile`/`Stat` dapat mengikuti junction keluar root di Windows) terjangkau dari `internal/ctxdb`; diperbaiki di go1.26.9. Repo mem-pin go1.26.8.

**Keputusan.** Direktif `go 1.26.9` di `go.mod`, `bench/go.mod`, `tools/go.mod` (tanpa baris `toolchain` terpisah: `go mod tidy` membuangnya bila sama dengan `go`). CI `actions/setup-go` membaca `go-version-file` sehingga ikut naik; lokal `GOTOOLCHAIN=auto` mengunduh go1.26.9. Teks ENGINEERING yang menyebut 1.26.8 adalah catatan pengukuran historis, bukan pin.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/dependency 0; ukuran binary diukur ulang oleh `make size-check`. `make vuln`: 0 vuln terjangkau.

**Konsekuensi.** (+) Gate E-54 hijau. (−) Kontributor dengan Go lokal < 1.26.9 bergantung pada unduhan toolchain otomatis.

## ADR-052 — Errata AF-01..AF-08 (pasca-freeze)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup AF-01..AF-08 ([`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-046"), backlog ADR-047 butir 2, TS-P01. Usulan ADR-E01..E08 di `docs/F0-REPORT.md`. Syarat ADR-047 "AF-01/AF-02 sebelum F4" terpenuhi.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC §9.7, §11.4, §11.6 (langkah 11/25), §11.11.4, §11.11.7, §11.11.10, §22.1 GAP-REL-11; SELF-HEALING §3.3, §5.1; TRUST-MODEL §3. Setiap passage mengutip "ADR-052 AF-0x".

**Keputusan** (masing-masing dibuktikan skenario spike F0):

| Temuan | Bagian terdampak | Keputusan | Bukti |
|---|---|---|---|
| AF-01 | SPEC §11.4 `gateway_inbound`, SELF-HEALING §5.1 | `INSERT … ON CONFLICT(platform, external_id) DO NOTHING` menggantikan `INSERT OR IGNORE` (juga ingest spool dan `blob_refs`). Dedupe PK tetap; pelanggaran CHECK/NOT NULL menjadi error → rollback → offset tidak maju → `inbound_invalid` dilaporkan, tidak pernah hilang diam-diam | #79 `TestScenario/S079_InvalidPayloadNotSilent` |
| AF-02 | SELF-HEALING §3.3/§5.1 | `turn_start` + INSERT journal `running` berada di transaksi worker yang sama dengan `user_message` dan pengisian `event_id` | #80 `S080_CrashAfterWorkerCommitAnswered` |
| AF-03 | SPEC §11.4 `idx_gw_inbound_queue`, SELF-HEALING §5.1 | Urutan kerja per chat = `rowid` `gateway_inbound` (urutan INSERT, juga saat ingest spool sesuai urutan baris); `received_at` hanya tampilan/retensi, bukan kunci urut | #81 `S081_PerChatOrderInsertion` |
| AF-04 | SELF-HEALING §5.1, SPEC §22 REL-11 | Sesi chat di-lease proses lain → satu balasan `__system__` per chat per kejadian (delivery tanpa `event_id`, tanpa lease); keputusan overflow REL-11 dibuat poller dengan balasan `__system__` yang sama | #82 `S082_ChatSessionBusyNotice` |
| AF-05 | SPEC §11.6 langkah 11/25 | Purge langkah 11 juga menghapus `blob_refs` owner `gateway_inbound` milik baris yang dihapus, sebelum langkah 25 | #83 `S083_PurgeInboundAttachment` |
| AF-06 | SPEC §11.11.7 spool, TRUST-MODEL | Cakupan spool = setiap inbound yang **tidak akan dikirim ulang** platform bila tidak di-commit (wajib-ack maupun tanpa ack); platform ber-API riwayat memakai `gateway_cursors` untuk backfill | #84 `S084_SpoolDuringDrainBarrier` |
| AF-07 | SPEC §11.11.4 (AE-05) | SIGKILL ke `tool_pgid` bila masih ada proses ber-`pgrp = tool_pgid` dengan cap start ≥ `tool_pgid_start` (pemindaian `/proc` / `proc_listpids`); leader yang sudah mati tidak membatalkan kill | #85 `S085_LeaderDeadNohupChildKilled` (Linux) |
| AF-08 | SPEC §11.11.7 migrator | Persist `run/migrate_blocked.json` `{since_boot, boot_id, target_version}` (jam boot; reset saat `boot_id`/target berubah); `--force-migrate` otomatis bila `now_boot − since_boot ≥ 600 s`, terakumulasi lintas restart | #86 `S086_MigratorRestartsAccumulate` |

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC: termasuk budget `ctxdb` ADR-049. DDL: 0 kolom baru (AF-03 memakai `rowid`). Mekanisme baru: 1 file state (`run/migrate_blocked.json`).

**Konsekuensi.** (+) Tidak ada pesan masuk yang hilang diam-diam atau tak terjawab pada jendela crash yang ditemukan auditor. (−) Indeks antrean `idx_gw_inbound_queue` disesuaikan dengan urutan `rowid` saat implementasi gateway F4 (kinerja, bukan semantik).

## ADR-053 — Lokasi protokol gateway/approval: `internal/gateway`

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-P09.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ENGINEERING §2.2. SPEC §3 sudah menulis `internal/gateway`; SELF-HEALING tidak menyatakan lokasi.

**Konteks.** E-06 melarang modul gateway mengimpor `ctxdb`, tetapi protokol poller/worker/outbox/approval (SELF-HEALING §5.1, SPEC §11.11.10) memerlukan transaksi `ctxdb`. Spike F0 menulisnya sebagai implementasi referensi di `internal/ctxdb/gateway_ref_test.go` dan `approval_ref_test.go` di atas `Tx` publik.

**Keputusan.**
1. **Logika protokol** gateway dan approval (urutan poller → worker, spool, outbox, konsumen approval) milik paket `internal/gateway` (F4).
2. `ctxdb` hanya menyediakan **primitif storage**: tabel spool/outbox/approval/inbound dan metode transaksional bertipe. `internal/gateway` mengonsumsinya lewat interface kecil yang dideklarasikan di sisi konsumen (E-03, E-09, ≤ 5 method); `cmd/damnbot` yang menyambungkan. Adapter platform (`gateway/<platform>`) tetap di bawah E-06.
3. File `*_ref_test.go` tetap **test-only** sebagai oracle skenario sampai F4 memindahkan logikanya ke `internal/gateway`; setiap file diberi komentar penunjuk ke ADR ini.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC: protokol dihitung di budget `gateway` SPEC §3.2.4 (bukan `ctxdb`). Arah import §2.2 tidak berubah.

**Konsekuensi.** (+) `ctxdb` tetap sempit; invarian ditegakkan guard DB apa pun pemanggilnya. (−) F4 harus memindahkan dan menyesuaikan implementasi referensi.

## ADR-054 — Errata pasca-freeze: klarifikasi + perbaikan DDL V-13/V-15

**Status:** Accepted 2026-10-18 (keputusan user). Menutup usulan ADR-E09, E11, E13, E14, E15, E17, E18, E19, E21, E22, E23, E24, E27, E28, E29, E30, E31, E32 (`docs/F0-REPORT.md` §"Errata backlog ADR-047"), backlog ADR-047 butir 2. V-13/V-15 mengubah DDL (fail-closed, normalisasi FTS) atas persetujuan eksplisit user; item lain yang mengubah perilaku/budget/invarian dipisah sebagai ADR-055..061 (Accepted 2026-10-18).

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18) ke semua bagian terdampak, masing-masing mengutip "V-xx/ADR-054" atau "ADR-054 V-xx": SPEC (indeks dokumen, P21, §4.1, §6, §9.7.1, §9.10, §10, DDL §11.2/§11.4 + `internal/ctxdb/migrations`, §14.3, §16, §22.1 GAP-PROV-05), SELF-HEALING §3.5, §8, ENGINEERING E-19/E-39/E-45/§8, SCORECARD §3/§3.3/§4, ROADMAP (milestone freeze, F0, F9), MITIGATION-MATRIX (GAP-GW-11, GAP-INF-02), FEATURE-PARITY PLUG-022, EXEC-AND-CODING (job `scorecard`), DECISIONS ADR-010/015 (V-10) dan ADR-032 (V-21).

| Temuan | Bagian terdampak | Keputusan |
|---|---|---|
| V-06 | MITIGATION-MATRIX, SPEC §9.7.1 | Sisa risiko WA = "perlu rilis `damnbot-wa-bridge` baru (`health.reason=protocol_outdated` → `needs_attention`)"; kolom verifikasi menyebut `TestBridgeProtocolConformance` |
| V-08 | SPEC §6 | Baris Volatile: "nama skill/tool relevan baru (≤ 20 tok)" |
| V-10 | ENGINEERING E-39, SPEC §16, ADR-010/015 | `TAGS += perf` → `-tags goolm,perf`; bentuk tag final = E-39 |
| V-11 | SPEC P21, ENGINEERING E-45 | P21 diukur dengan parse `require` langsung (non-`// indirect`) `go.mod` utama (`TestDirectDeps`) |
| V-13 | SPEC §11.4 `gateway_identities.strength` | `NOT NULL` **tanpa DEFAULT**; adapter wajib mengisi dari `Caps.IdentityStrength` (fail-closed). Test: `TestErrataV13IdentityStrengthRequired` |
| V-15 | SPEC §11.2 `skills_ai`/`skills_au` | Kolom `name` diindeks lewat `dmb_fts_norm(NEW.name)`, sama dengan `description`/`body`; re-index memakai ekspresi yang sama. Test: `TestErrataV15SkillNameNormalised` |
| V-16 | SELF-HEALING §3.5 | Recovery FTS mencakup `tool_catalog_fts`: `delete-all` + bangun ulang dari `tool_catalog` |
| V-17 | SELF-HEALING §3.5 | Rujukan DDL `fts_meta`/`blob_fts` = SPEC §11.2, §11.6; SELF-HEALING §3.5 |
| V-19 | SPEC §22 PROV-01 | Payload seragam `system_notice{kind:"carrier_dropped", reason, count}` |
| V-20 | SCORECARD §3.3/§4, ROADMAP F0/F9, ENGINEERING E-19 | Nama gate: `BenchmarkSpeedParity/<ID>` di `bench/speed`, `TestScorecardGate`, `TestBudgetTableSum` (E-19) |
| V-21 | DECISIONS ADR-032 | Kalimat status freeze ADR-032 bersifat *(Historis — status saat ADR ditulis. Status terkini: SPEC §20 + ADR-030/047.)* |
| V-22 | ROADMAP milestone freeze | Syarat freeze = ADR-033..036 terintegrasi + errata diverifikasi auditor (SPEC §20, ADR-030); terpenuhi oleh ADR-047 |
| V-25 | SELF-HEALING §8, GAP OPS-06 | Exit code `damnbot doctor` = tabel OPS-06 (0/1/2/3), OPS-06 sumber normatif |
| V-26 | SPEC §10 | `--version` = alias builtin untuk `version`, tidak dihitung |
| V-27 | indeks dokumen (SPEC §21) | "65 temuan (ADR-035) + errata verifikasi N-01..N-36 (ADR-036)" |
| V-28 | ROADMAP F9 | Gate parity = 837/837 baris penyebut (SPEC §8) |
| V-29 | ENGINEERING §8 | Pola `grep -E '^[|] [A-Z]+-[0-9]{3} '` |
| V-30 | FEATURE-PARITY PLUG-022, SPEC §9.10 | PLUG-022 = "slot/plugin memory provider (Honcho)", tetap planned; frasa "porsi PLUG-022 menjadi dropped-with-approval" tidak berlaku |

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. DDL: V-13 menghapus 1 DEFAULT, V-15 menambah normalisasi 1 kolom FTS (belum ada data rilis; migrasi tidak diperlukan). Baris SCORECARD/MITIGATION: hanya teks V-06/V-20.

**Konsekuensi.** (+) 18 dari 25 MINOR non-AF (V-06..V-31 kecuali V-12) tertutup; 7 sisanya = ADR-055..061 (Accepted 2026-10-18). Dua lubang DDL fail-open/inkonsisten diperbaiki dengan test. (−) Teks lama di beberapa dokumen pendamping masih harus dibaca bersama ADR ini.

## ADR-055 — Starlark: allowlist, `timeout_s`, mekanisme macOS (V-07)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup V-07 (usulan ADR-E10). Diimplementasikan di F5 (`execute_code` + Starlark, GAP-COST-10); tidak ada kode F0 terdampak.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC §9.2, §22.1 GAP-COST-10; EXEC-AND-CODING §B.7 (schema, binding, tabel limit, COD-11); MITIGATION-MATRIX GAP-COST-10.

**Konteks.** SPEC §9.2 mengizinkan "tool deferred/inti yang diizinkan approval", sedangkan EXEC-AND-CODING §B.7/COST-10 hanya 7 tool; batas waktu ≤ 30 s vs schema `timeout_s` 1–300; macOS `ulimit -v` vs `RLIMIT`.

**Keputusan.**
1. **Allowlist:** binding `tool(name, **args)` Starlark hanya untuk 7 tool §B.7: `read_file`, `write_file`, `edit_file`, `glob`, `grep`, `web_fetch`, `shell` (tanpa `background`/`session`). Tool lain → `invalid_args`. Setiap call tetap melewati pipeline SPEC §4.3 (schema, sandbox, approval).
2. **Waktu:** untuk `language = "starlark"`, `timeout_s` 1–30 (default 30); nilai > 30 → `invalid_args` (P3, tanpa clamp diam-diam). Bahasa lain tetap 1–300.
3. **Memori:** mekanisme sama dengan baris memori §B.7 per OS, dengan cap tetap 256 MiB untuk proses anak `damnbot __starlark`: Linux `RLIMIT_AS`, macOS shim `damnbot __exec-shim` `setrlimit` (`RLIMIT_DATA`; `ulimit -v` tidak dipakai), Windows Job Object. Frasa "batas OS yang sama dengan `execute_code`" dibaca "mekanisme sama, cap 256 MiB".

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0 (budget modul exec F5 tidak berubah). Permukaan tool Starlark menyempit ke 7 tool; batas waktu Starlark maks 30 s. Test F5: `TestStarlarkMemoryLimit`, `TestStarlarkStepLimit`, `BenchmarkProgrammaticBatch` tetap; kasus allowlist/`timeout_s` > 30 → `invalid_args` ditambahkan di suite schema `execute_code`.

**Konsekuensi.** (+) Satu kontrak Starlark; tidak ada jalur ke tool di luar §B.7. (−) Skrip yang butuh > 30 s atau tool lain harus memakai bahasa lain / loop tool biasa.

## ADR-056 — Literal baris overflow katalog tool (V-09)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup V-09 (usulan ADR-E12). Diimplementasikan di F2 (`modules` + katalog `tool_search`); tidak ada kode F0 terdampak.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC P1, §5.2, §22.1 GAP-PROV-02; DECISIONS ADR-035.

**Konteks.** Baris overflow katalog ditulis dalam tiga bentuk (`+N groups`, `- +<K> more groups: tool_search`, `+K more groups`), dan klausa (3) terbaca seolah pengurutan alfabetis hanya berlaku saat overflow. Golden test butuh satu literal.

**Keputusan.**
1. Satu-satunya bentuk baris overflow (mode normal dan mode degradasi PROV-02): `- +<K> more groups: tool_search`, `<K>` = jumlah grup yang tidak ditampilkan (desimal, tanpa pemisah ribuan). Bentuk lain di P1, PROV-02, dan ADR-035 merujuk literal ini.
2. Grup modul **selalu** diurutkan alfabetis (byte-wise, id modul), dengan atau tanpa overflow; overflow hanya memotong ekor daftar yang sudah terurut.

**Dampak terukur (SPEC §20).** Token 0 (literal ≤ 15 tok, sudah masuk hitungan 450 ≤ 600); latensi/binary/dependency/LOC 0. Golden: `TestStableTierGolden`, `TestCatalogDeterministic`, `TestContextTierBudget/degraded` memakai literal ini.

**Konsekuensi.** (+) Katalog byte-identik dan golden deterministik. (−) Mengubah literal = ADR baru.

## ADR-057 — Kriteria salvage `gateway_inbound`/`gateway_deliveries` (V-14)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup V-14 (usulan ADR-E16). Diimplementasikan di F4 (salvage `heal` + tabel gateway); salvage belum ada di spike F0, jadi tidak ada kode F0 terdampak.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SELF-HEALING §3.5 (langkah salvage), SPEC §11.11.10.

**Konteks.** Kriteria salvage `id >` maksimum backup tidak terdefinisi untuk `gateway_inbound` (baris backup `event_id NULL` vs DB rusak `event_id` terisi: INSERT gagal PK, satu-satunya UPDATE sah = NULL→nilai) dan melewatkan perubahan status `gateway_deliveries` (mutable) setelah backup.

**Keputusan.**
1. **`gateway_inbound`:** `INSERT … ON CONFLICT(platform, external_id) DO NOTHING` (bentuk ADR-052 AF-01), lalu `UPDATE … SET event_id = ? WHERE event_id IS NULL` bila baris salvage punya `event_id` dan baris hasil restore masih NULL (sah menurut `gwin_update_guard`). `event_id` non-NULL yang sudah ada tidak pernah diubah.
2. **`gateway_deliveries`:** dicocokkan per `idem_key` (bukan `id >`); status "terjauh" menang dengan urutan `queued` < `failed` < `sending` < `uncertain` < `dead` < `sent`; status sama → `updated_at` lebih besar. Status yang mungkin sudah terkirim (`sending`/`uncertain`/`sent`) tidak pernah mundur ke status yang memicu kirim otomatis, selaras GAP-REL-10 (bagian `sent` tidak pernah diulang).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0; DDL 0 (memakai guard dan UNIQUE yang ada). Test F4: kasus salvage gateway di `TestAutoRestore` (inbound NULL→nilai, delivery `sent` di DB rusak vs `queued` di backup → 0 kirim ulang).

**Konsekuensi.** (+) Salvage deterministik untuk tabel mutable; 0 duplikat keluar setelah auto-restore. (−) Salvage gateway membaca baris DB rusak per `idem_key`, bukan rentang `id`.

## ADR-058 — Satu rumus estimator token (V-18)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup V-18 (usulan ADR-E20). Diimplementasikan di F2 (budget/compaction, GAP-COST-09); tidak ada kode F0 terdampak (harness `bench/budget` memakai tokenizer o200k_base, bukan estimator runtime). *(Dikoreksi ADR-067: estimator teks diimplementasikan di **F1** dalam `internal/budget`; F2 hanya formula gambar + pemicu P8; `TestTokenEstimatorCalibration` korpus teks = acceptance F1.) *(Dikoreksi ADR-101: batas bawah kalibrasi 0,7 → 0,5; `est_input_tokens` = estimasi mentah tanpa kalibrasi.)*

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC §4.5, §22.1 GAP-COST-08/GAP-COST-09; MITIGATION-MATRIX GAP-COST-09.

**Konteks.** Ada tiga definisi normatif estimator token: unit byte vs char, default 3,6 vs 3,0 vs per-script, kalibrasi satu call vs EMA.

**Keputusan.**
1. GAP-COST-09 adalah satu-satunya rumus estimator runtime: `prompt_tokens` nyata terakhir + delta lokal; delta = Σ char / rasio per script (Latin 4, Kiril/Arab/Devanagari 2, CJK 1 char/tok) × faktor kalibrasi EMA (`input_tokens`/`est_input_tokens`, dibatasi 0,7–1,5), diturunkan ulang dari ≤ 50 baris `usage_ledger` `purpose='turn'` saat sesi di-load.
2. Sesi tanpa baris ledger: rasio per script tanpa kalibrasi (faktor 1,0).
3. `ceil(bytes/3,6)` di SPEC §4.5 dihapus; §4.5 merujuk COST-09.

**Dampak terukur (SPEC §20).** Token model 0; latensi/binary/dependency/LOC 0; DDL 0 (`usage_ledger.est_input_tokens` sudah ada). Angka pemicu budget/compaction berubah untuk sesi tanpa ledger (estimasi per script, bukan per byte). Test F2: `TestTokenEstimatorCalibration` (korpus en/id/ru/ja/gambar, rasio estimasi/real 0,9–1,1; SCORECARD GAP-COST-09).

**Konsekuensi.** (+) Satu rumus; teks non-Latin tidak lagi salah estimasi ~2×. (−) Sesi baru tanpa ledger memakai rasio statis sampai call pertama.

## ADR-059 — Daftar frasa tertunda SPEC §20 (V-23)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup V-23 (usulan ADR-E25). Diimplementasikan di F1 bersama gate dokumen `TestSpecDocs` (`TestSpecNoPendingAmendments`, `testdata/spec_pending_phrases.txt`; belum ada di F0, tidak ada kode F0 terdampak).

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC §20 (isi daftar merujuk paragraf Keputusan di bawah; entri pengecualian (6)); ROADMAP F1 Acceptance (`TestSpecDocs`, ditambahkan ADR-077, menutup AUD-080).

**Konteks.** Isi daftar frasa tidak didefinisikan. Bila "Usulan" dicocokkan apa adanya, 11 header tabel di PROVIDERS dan EDGE-CASES ("Usulan Damn-Bot", "Usulan regression test") membuat gate merah; bila daftar memuat "menunggu …", baris Status SPEC dan §20 ikut merah.

**Keputusan.**
1. Isi `spec_pending_phrases.txt` = tepat 5 literal, case-sensitive, dicocokkan sebagai substring apa adanya (bukan regex): `Usulan:`, `Usulan resolusi:`, `diterapkan saat implementasi`, `menunggu persetujuan`, `TBD`.
2. "kandidat freeze (menunggu verifikasi …)" di baris Status SPEC dan §20 bukan frasa tertunda (tidak cocok dengan literal mana pun). Header tabel "Usulan …" tanpa titik dua juga tidak cocok.
3. Daftar pengecualian dokumen (§20, ADR-037) ditambah entri (6): paragraf Keputusan ADR-059 ini (definisi daftar) dan `docs/F0-REPORT.md` §"Errata backlog ADR-047" (teks usulan asli, historis).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. Hit di luar pengecualian per 2026-10-18: 0 (kemunculan yang ada hanya di SPEC §22.2 historis dan entri (6)).

**Konsekuensi.** (+) Gate dokumen deterministik. (−) Menambah frasa atau pengecualian = ADR baru.

## ADR-060 — Enkripsi bundle diagnostik (V-24)

**Status:** Accepted 2026-10-18 (keputusan user, opsi a). Menutup V-24 (usulan ADR-E26). Diimplementasikan di F8 (GAP-SEC-17); tidak ada kode F0 terdampak.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): SPEC §10 (`doctor bundle`), §22.1 GAP-SEC-17; CROSS-CUTTING §1.7; MITIGATION-MATRIX GAP-SEC-17.

**Konteks.** SEC-17 berarti tanpa `--upload` hasilnya zip lokal tidak terenkripsi; SPEC §10 dan CROSS-CUTTING §1.7 menyatakan selalu terenkripsi.

**Keputusan.**
1. `damnbot doctor bundle [--out P]` default menulis **zip lokal ter-redaksi** (redaksi §14.3 + PII sesuai SEC-17) dengan mode file `0600` (ditulis ke temp `0600` lalu `rename`; Windows: ACL hanya user). Tidak terenkripsi.
2. `--upload` = enkripsi bundle yang sama ke kunci publik maintainer (X25519, embedded) dan menyimpan file terenkripsi untuk dilampirkan manual; 0 host upload/pastebin di kode. Tidak ada `--no-redact`.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. Verifikasi F8 (SEC-17): grep kode 0 host pastebin; test mode file bundle = `0600`; test `--upload` menghasilkan ciphertext yang hanya terbuka dengan kunci privat fixture.

**Konsekuensi.** (+) User bisa memeriksa isi bundle sebelum dikirim; kontrak privasi satu sumber. (−) Zip lokal tidak terenkripsi; perlindungannya mode `0600` + redaksi.

## ADR-061 — Himpunan EC-PLAT Telegram untuk gate F4 (V-31)

**Status:** Accepted 2026-10-18 (keputusan user, opsi a). Menutup V-31 (usulan ADR-E33). Gate dijalankan di F4; tidak ada kode F0 terdampak.

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ROADMAP (tabel kategori EC, acceptance F4 dan F7), SPEC §9.7 (regression), §16 (baris Edge-case).

**Konteks.** Bila hanya entri bertanda "(Telegram)" yang dihitung (009–023), empat entri Telegram lain (001/002/003/007) jatuh ke gate F7.

**Keputusan.** Gate F4 "EC-PLAT Telegram" = daftar ID eksplisit berikut (19 entri), diturunkan dari `docs/EDGE-CASES-hermes.md` §"Platform chat" dengan memeriksa setiap entri: EC-PLAT-001, 002, 003, 007 (gejala menyebut Telegram) dan EC-PLAT-009..023 (bertanda "(Telegram)"). Tidak termasuk: EC-PLAT-004, 005, 006, 008 (gateway umum, tanpa kaitan Telegram) dan EC-PLAT-024 (WhatsApp); kelimanya tetap di gate "EC-PLAT (non-Telegram)" F7. Contoh usulan "001..023 kecuali 008" tidak dipakai karena memasukkan 004–006.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. Cakupan gate F4: 15 → 19 entri `TestEdgeCases/EC-PLAT-*`; gate F7 berkurang 4 entri (Σ EC-PLAT tetap 24).

**Konsekuensi.** (+) Semua entri Telegram dibuktikan di fase Telegram. (−) Acceptance F4 bertambah 4 regression test.

## ADR-062 — Smoke LLM Damn-Bot pindah ke acceptance F1

**Status:** Accepted 2026-10-18 (keputusan user). *(Diamandemen ADR-075 dan ADR-085: run smoke Damn-Bot F1 baru boleh dimulai setelah hardening `bench/h2h` — ledger cap persisten tunggal, oracle netral-agent, isolasi agent — ada di kode beserta test-nya; default `-cap-usd` = cap ledger, sehingga sisa yang berlaku = US$2,00 − Σ ledger; `-cap-usd` hanya bisa menurunkan.)*

**Aturan penerapan.** Teks sudah diterapkan (2026-10-18): ROADMAP F0 (deliverable 5, acceptance smoke LLM) dan F1 (acceptance smoke LLM Damn-Bot + cap US$2,00).

**Konteks.** Acceptance F0 ROADMAP menuntut "Smoke LLM 10 task selesai untuk kedua agent". Di F0 belum ada agent loop Damn-Bot (`damnbot run` adalah deliverable F1), jadi sisi Damn-Bot tidak bisa dijalankan. Sisi hermes sudah selesai: `bench/h2h/results/2026-10-18/` — 10/10 task, 0 error harness, 60 call, US$0,0238 (`docs/F0-REPORT.md` §"Smoke LLM 10 task + `bench/h2h`").

**Keputusan.**
1. Acceptance F0 smoke LLM terpenuhi untuk hermes oleh run `bench/h2h` 2026-10-18 (harness + proxy rekam terbukti end-to-end).
2. Sisi Damn-Bot menjadi acceptance F1: run pertama `damnbot run` pada 10 task yang sama (seleksi deterministik `h2h-smoke/v1`) lewat `bench/h2h` dengan proxy rekam lengkap dan 0 error harness. Pass/fail tetap informatif (bukan gate kualitas vs hermes; perbandingan penuh di RC F9).
3. Cap biaya LLM F1 = **US$2,00 total** (seluruh run smoke/uji F1, dihitung proxy `bench/h2h`; cap keras `-cap-usd`).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. ROADMAP F0: 1 butir acceptance dipersempit ke hermes; ROADMAP F1: +1 butir acceptance. Biaya LLM F1 ≤ US$2,00.

**Konsekuensi.** (+) Gate F0 tidak menunggu kode F1; bukti harness tetap ada. (−) Smoke Damn-Bot pertama baru terlihat di F1.

**Bukti implementasi (2026-10-21).** Smoke final pada `main` `3a2a10d`: 10/10 task ran, 1 pass, 0 harness error; ledger `bench/h2h/results/f1-spend.json` 0,82340667 → 0,92200161 (sisa $1,07799839 dari cap $2,00). Matriks 29 skenario penggunaan: 24 PASS / 4 FAIL / 1 SKIP; detail dan defect terbuka di `docs/F1-LLM-SMOKE.md`. Tidak ada perubahan kode produk.

**Rerun pasca-perbaikan (2026-10-21).** Pada `main` `1b57ae9` (#134+#135), A4/C14/C15/D20/destruktif PASS; C13 create masih FAIL karena model tetap mengirim tag, meski tool path lolos via stub. Biaya rerun ≈$0,000679; total F1 ≈$0,922681 < $2,00. Detail `docs/F1-LLM-SMOKE.md`.

**C13 rerun (2026-10-21).** Pada `main` `428c775` (#137), create default dan tag-on-missing-path sama-sama membuat file byte-exact `hello` (tag diabaikan dengan notice); biaya $0,000526. Detail `docs/F1-LLM-SMOKE.md`.

## ADR-063 — Errata residu backlog ADR-037 (AUD-002)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan pemilik proyek: opsi paling konservatif, dalam budget SPEC §3.2.4/P1/P13/P21, tanpa dependency baru). Menutup AUD-002 ([`AUDIT-F0.md`](AUDIT-F0.md)): lima MINOR backlog ADR-037 yang tidak punya ADR penutup (m-15, N-15, N-24, N-33, V-05); backlog ADR-047 butir 2.

**Aturan penerapan.** Teks diterapkan (2026-10-18), setiap passage mengutip "ADR-063 <ID>": CODE-TOOLS §10 (baris P23); SPEC P2, P23, P25, P27, §3.2.1, §3.2.2, §18.4; DECISIONS ADR-036 (penanda koreksi); SCORECARD §1.9 GAP-OPS-08; FEATURE-PARITY "Konvensi kolom"; F0-REPORT Status #9 dan §"Errata backlog ADR-047".

**Konteks.** Ekstraksi backlog di F0-REPORT hanya membaca baris `| ID | MINOR |` tabel verifikasi REVIEW-1.0. Tabel backlog ADR-037 di DECISIONS tidak ikut dibaca, sehingga lima item tidak punya ADR penutup dan acceptance F0 "100% item punya ADR penutup + test" tidak terpenuhi. Total backlog yang benar = **38 item** (33 + 5).

**Keputusan.**

| Item | Keputusan | Test (fase) |
|---|---|---|
| m-15 | Jumlah ID CODE-TOOLS = **31** (CT-01..14 = 14, CT-20..29 = 10, CT-30..36 = 7; CT-15..19 tidak dipakai). Koreksi di CODE-TOOLS baris P23, SPEC P23, ADR-036 ("30 ID berblok") | `TestCodeToolsIDsComplete` di gate dokumen `TestSpecDocs` (F1): 31 ID unik, masing-masing punya baris verifikasi |
| N-15 | Daftar cap P2, P27, dan §18.4 memuat cap gateway per identitas role `user` (USD/hari, default 1,00; 200 turn/hari; stop `budget_gateway_user`) dan cap tool berbayar non-LLM (`budget.tools_session_usd`/`tools_daily_usd`; stop `budget_tools`, COST-11). Kedua cap gateway diperiksa **pra-turn** di admission gateway: turn ke-201 atau cap USD tercapai → ditolak sebelum `turn_start`, dengan balasan statis ≤ 1/menit, 0 LLM call, 0 tool call (selaras P25) | `TestBudgetGuardGatewayUser` (F4), `TestBudgetGuardTools` (F6, COST-11) |
| N-24 | Target SCORECARD GAP-OPS-08 = **`= 20 / 0`** (jumlah perintah top-level persis = SPEC §10) | `TestCLITopLevelCount` (F3) |
| N-33 | Kolom `fase` per baris FEATURE-PARITY adalah deliverable F0 (ADR-064, `TestParityPhaseColumn`), bukan diturunkan dari prefix | `TestParityPhaseColumn` (F0, lulus) |
| V-05 | `Module.Status` = `stopped\|starting\|ready\|degraded\|needs_attention\|failed`. `needs_attention` = kegagalan yang tidak pulih sendiri dan butuh tindakan owner (breaker GW-11: `logged_out`, `banned`, `temporary_ban`, `protocol_outdated`). Transisi baru: `ready\|degraded → needs_attention` (event `module_error`, notice owner); `needs_attention → starting` hanya lewat tindakan owner (login/pairing ulang adapter, restart `serve`) atau timer `temporary_ban` 24 jam (satu kali). Tidak ada auto-retry lain | `TestModuleLifecycle/needs_attention` (F2), `TestBridgeProtocolConformance` (F7) |

Metode hitung backlog (F0-REPORT): gabungan (a) baris MINOR REVIEW-1.0 yang tidak berstatus OK dan (b) tabel backlog ADR-037, dikurangi item yang disebut "Menutup" oleh ADR-037..062. Hasilnya 38 = AF-01..AF-08 (8) + V-06..V-31 kecuali V-12 (25) + m-15, N-15, N-24, N-33, V-05 (5).

**Dampak terukur (SPEC §20).** Token, latensi, LOC, ukuran binary, dependency: 0. Enum `Module.Status` +1 nilai (F2), tanpa DDL. Baris SCORECARD terpengaruh: GAP-OPS-08.

**Konsekuensi.** (+) Backlog ADR-047 lengkap 38/38. (−) Implementasi dua test cap gateway/tool baru ada di F4/F6.

## ADR-064 — Kolom `fase` FEATURE-PARITY + `TestParityPhaseColumn` (AUD-003)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-003 dan N-33 (bersama ADR-063). Diimplementasikan di F0: commit `09660fe` (kolom + test), koreksi EXEC-009 bersama ADR ini.

**Aturan penerapan.** Teks diterapkan (2026-10-18): FEATURE-PARITY (kolom `fase` di 18 tabel ID, "Konvensi kolom"); ROADMAP §"Pemetaan parity → fase" (rujukan ADR). Setiap passage mengutip ADR-064.

**Konteks.** Kolom `fase` per baris dan `TestParityPhaseColumn` (837/837) dinyatakan deliverable F0 tetapi belum ada, sehingga acceptance F1 "speed parity (a) untuk baris F1" dan gate parity per fase tidak terdefinisi.

**Keputusan.**
1. Setiap tabel ID FEATURE-PARITY punya kolom `fase` tepat sebelum `status` (`status` tetap sel terakhir, kontrak parser `bench/speed`). Nilai: `F1`..`F9` untuk 837 baris penyebut (planned + external-bridge); `—` untuk 17 baris `dropped-with-approval`.
2. Nilai harus berupa fase yang selnya tidak kosong untuk prefix baris itu di tabel ROADMAP §"Pemetaan parity → fase". `TestParityPhaseColumn` (`internal/archtest`, root module, stdlib) membaca kedua dokumen dan gagal bila: total baris ≠ 854, baris penyebut ≠ 837, ada `fase` di luar fase yang diizinkan prefixnya, baris dropped tanpa `—`, atau ada prefix yang tidak ada di tabel ROADMAP.
3. Hasil: F1 46, F2 81, F3 116, F4 56, F5 200, F6 86, F7 101, F8 146, F9 5 (Σ 837).
4. **Judgement call** (pemetaan kata kunci tidak tunggal). Aturan umum: CLI/SLASH mengikuti fase fitur yang dikendalikannya, atau F3 (permukaan CLI/REPL inti) bila tidak ada fitur khusus; tidak ada baris yang jatuh ke "sisanya" F9. Sistem plugin hermes ≙ ekstensi `damnbot config hooks` (F3). TOOL/TSET tidak punya sel F3/F9, sehingga toolset komposit permukaan inti → F2 ("toolsets → grup modul"). Default prefix: PLAT → F7 kecuali PLAT-022 (F4); PROV → F5 kecuali generik/custom (PROV-017, 052, 078 → F1); MCPX → F5; SKCAT → F8; MEDIA → F6 kecuali MEDIA-029/031 (F7). Pengecualian dan pilihan tidak jelas:

| ID | Fase | Alasan |
|---|---|---|
| CLI-001 `chat` | F3 | REPL interaktif = inti TUI, bukan `run` |
| CLI-073 `help`, CLI-074 `--version` | F1 | wajib ada di binary headless F1 |
| CLI-003, SLASH-036 (MoA); CLI-004 `fallback`; CLI-005/SLASH-014 worktree; CLI-007/008/034/062, SLASH-039 (secrets, egress, security, vault); CLI-010/SLASH-048 codex-runtime; CLI-013 `lsp`; CLI-020/021 auth; CLI-053/SLASH-067 tools; SLASH-026/033 | F5 | fitur yang dikendalikan ada di F5 (registry/OAuth/credential pool, EXEC F5, delegate/MoA) |
| CLI-006, SLASH-084 browser; SLASH-098/099, UI-008 (input gambar); TSET-034 | F6 | butuh browser atau pipeline vision dengan resize preventif |
| CLI-012 `proxy`, CLI-036/037/SLASH-102 debug dump, CLI-049/050/051/052, CLI-060, CLI-030/TOOL-072/TSET-022, TOOL-020/024/027..037/071, TSET-002/010/011/024, SLASH-030/031/034/037/049/077/078, MEM-009/010, SKILL-017/020, OPS-022 | F8 | UI proxy, debug dump, curator/pets/journey, memory provider eksternal, OTLP, projects/desktop, catalog/connectors, AUTO lanjutan |
| CLI-069 `serve`, UI-016 | F8 | `serve` hermes = backend desktop ≙ `damnbot serve --web` (web UI F8); UI tidak punya sel F4; runtime gateway `serve` dicakup GW-001/CLI-011 (F4) |
| CLI-038/039/040, SLASH-006/007/008/010/011/013/016/017/066/068, MEM-015/022/030/033, TSET-035, PLUG-014/036, CORE-005, SKILL-004/007/016/023, CLI-046/SLASH-069 | F2 | session store, checkpoints, backup/import, compaction, toolsets → grup modul, skill retrieval (hub tetap F8) |
| CLI-022/059/033/072/064/071/031/041/061/067/044/035, UI-027, SLASH-020/052/057/058/059/065, MEM-017, PLUG-001..008, CLI-048/075/SLASH-085, OPS-004, UI-013 | F3 | permukaan CLI/REPL/TUI, hook/ekstensi, import agent lain, rilis |
| CLI-023/024/SLASH-021, SLASH-001/003/022/023/041/043/056/089/095/096, TSET-044/045/062, AUTO-011/029, EXEC-016, OPS-033, GW-001..010/017..021/024/025 | F4 | gateway, emergency stop, approval gateway, cron/periodic scheduler, data cleanup |
| SLASH-012, TOOL-049..053, TSET-032/036, PLUG-021/024, UI-021, PLAT-025 (external-bridge), GW-011..016/022/023/026 | F7 | handoff, platform tools, rooms, kanban, API server, bridge WA, GW lanjutan |
| CORE-002, CORE-007, CORE-014, EXEC-015, EXEC-022 | F1 | prompt builder, interrupt, repair transcript (loop inti), allowlist, path safety |
| EXEC-009 background processes, TOOL-077 `process_manage` | F5 | ADR-072: F1 menolak `shell{background:true}`; tool `process_*` datang bersama kata kunci TOOL "process" (F5). EXEC tidak punya sel F2 |
| EXEC-023 plugin guard, PROV-027/075/076/077/079, PLUG-023, UI-020 | F5 | sandbox ekstensi/MCP, registry lokal/katalog/harga, MCP server |
| UI-018, OPS-029..032 | F9 | installer, Docker, Nix, Termux, skrip install |

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC: test 147 baris (bukan kode produksi). Baris SCORECARD terpengaruh: Feature parity (gate per fase kini terdefinisi).

**Konsekuensi.** (+) Gate parity per fase dan acceptance speed parity F1 terdefinisi dan ditegakkan test. (−) Mengubah fase baris = ubah FEATURE-PARITY + ADR (test menolak fase di luar tabel ROADMAP).

## ADR-065 — Cakupan baseline hermes tanpa LLM: status eksplisit per dimensi (AUD-004)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; pilihan konservatif: re-scope jujur, tanpa angka yang tidak diukur). Menutup AUD-004.

*(Amandemen 2026-10-18: dilengkapi ADR-083 — baris "Token skill per turn" (F2), `not_measured` di artefak, bullet baseline per fase.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): ROADMAP F0 Acceptance (baris baseline tanpa LLM); SCORECARD intro (aturan baca sel "diukur di F0"). Setiap passage mengutip ADR-065. Baris #4 F0-REPORT dimiliki worker infra dan merujuk ADR ini.

**Konteks.** ROADMAP mensyaratkan baseline hermes untuk "100% dimensi SCORECARD tipe tanpa LLM". Artefak `bench/speed/results/hermes-868b9f244f-nollm.json` berisi 11 kunci (9 dimensi). Banyak dimensi tanpa LLM membutuhkan harness pembanding yang baru bisa dibangun bersama komponen Damn-Bot yang setara (fake provider loop, TUI, soak `serve`, chaos), sehingga mengukur sisi hermes di F0 tidak menghasilkan angka yang sebanding.

**Keputusan.**
1. Acceptance F0 baseline tanpa LLM = **setiap** dimensi SCORECARD tanpa LLM punya status eksplisit di tabel ini: `F0` (angka di artefak bernama) atau `Fx` (fase pengukuran + alasan). Sel "diukur di F0" di SCORECARD untuk dimensi berstatus `Fx` dibaca "diukur di Fx (ADR-065)". Dimensi berbasis LLM tetap "diukur di RC F9" (ADR-032).
2. Sisi hermes dari dimensi `Fx` diukur di fase itu dengan harness yang sama untuk kedua agent dan wajib ada sebelum acceptance fase tersebut.

| Dimensi SCORECARD | Status | Artefak / alasan |
|---|---|---|
| §1.1 Fixed overhead (token + schema bytes) | F0 | `bench/budget/results/hermes-868b9f244f.json`; nollm `fixed_overhead_tokens`, `schema_bytes` |
| §1.1 Background token | F0 (statis) | baseline dari kode (`agent/turn_finalizer.py:777-779`) |
| §1.2 Startup | F0 | nollm `startup_first_frame_ms`, `startup_version_ms` |
| §1.2 Startup prompt-ready, Idle RSS | F0 bila artefak berisi angka; bila `null` → F3 | TS-11: chat di netns gagal ke gateway lokal. Pengganti: fixture TUI 2.000 event dengan harness PTY yang sama untuk kedua agent |
| §1.2 Overhead agent per iterasi | F1 | butuh endpoint fake provider latensi 0 yang dipakai kedua agent (`BenchmarkIterationOverhead`, P17) |
| §1.2 First token di UI | F1 (headless) / F3 (TUI) | fake provider bertanda waktu; TUI baru ada di F3 |
| §1.2 Peak RSS | F4 (gate F9) | butuh soak 1.000 turn (harness soak `serve`) |
| §1.2 CPU idle | F4 | butuh `serve` + fake Telegram untuk kedua agent |
| §1.2 Code tools | F0 | `bench/codetools/results/f0-baseline.json` |
| §1.2 Exec spawn overhead | F1 | benchmark A.9 + driver tool `terminal` hermes di harness yang sama |
| §1.2 Voice end-to-end, Image preflight | F6 | butuh fake STT/LLM/TTS dan suite 50 gambar (modul media/voice) |
| §1.3 Crash/hang rate | F4 (gate F9) | soak 24 jam `serve` |
| §1.3 Pemulihan `kill -9`, Self-healing | F4 | chaos suite SELF-HEALING untuk kedua agent |
| §1.3 Konkurensi | F2 | `TestMultiProcess` + harness multi-proses `hermes_state` |
| §1.3 Proses yatim / perintah hang | F1 | suite EXEC (`TestNoOrphans`, `TestInteractiveCorpus`) lewat tool `terminal` hermes |
| §1.3 Edit salah lokasi | F1 | CT-20 dijalankan pada tool edit kedua agent |
| §1.3 Error tertelan | F0 (statis) / F1 (injeksi) | angka statis BASELINE §5; 50 titik injeksi butuh fake provider |
| §1.3 Fallback tak terlihat | F1 | injeksi 503 lewat fake provider |
| §1.4 Ukuran install, Baris config minimum, Langkah setup | F0 | nollm `install_size_bytes`, `config_lines_min`, `setup_steps` |
| §1.4 Waktu install dari nol | F0 (workstation) / F9 (VM bersih 10 Mbit/s) | nollm `install_time_s` bukan VM bersih; angka gate diukur ulang di VM RC |
| §1.4 Error informatif | F3 | suite 60 kesalahan butuh permukaan CLI/config/setup |
| §1.5 Feature parity, Platform chat, Provider, Bahasa UI, Skill, MCP | F0 (statis) | inventaris FEATURE-PARITY |
| §1.5 Dukungan OS | F1 | matrix CI Damn-Bot (Deliverable F1 #0, ADR-073) + inventaris workflow CI hermes di job yang sama |
| §1.6 Secret redaction | F1 | 60 secret × 6 kanal butuh fake provider + kanal log/transcript |
| §1.6 Akses gateway orang asing | F4 | E8 fake Telegram |
| §1.6 Supply chain | F1 | `pip-audit` + `npm audit` hermes dijalankan di job `supply-chain` (ADR-073) |
| §1.6 Data at-rest | F0 (statis) | BASELINE (`.env` plaintext) |
| §1.7 LOC/fitur, file terbesar, fungsi, kompleksitas, siklus import, klasifikasi string, broad catch, state global, retry, dependency, rasio test | F0 (statis) | angka BASELINE/ENGINEERING §8 |
| §1.7 Flake rate, Waktu CI PR, Kode mati | F1 | job nightly `-count=20`, laporan CI, `deadcode` (ADR-073); sisi hermes dari riwayat CI/vulture di job yang sama |
| §1.7 Waktu build bersih/inkremental | F3 | job buildtime bersama release pipeline |
| §1.8 Fitur pembeda | n/a | target absolut, tanpa baseline hermes |
| §1.9 GAP-xx bertanda "diukur F0" | fase requirement GAP (tabel adopsi ROADMAP) | bukti GAP-SWEEP berlaku sebagai baseline kualitatif F0; angka hermes diukur bersama verifikasi requirement-nya, atau RC F9 bila butuh LLM |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Tidak ada angka baseline yang berubah. Acceptance F1–F4, F6, dan F9 mendapat kewajiban mengukur sisi hermes untuk dimensinya.

**Konsekuensi.** (+) Tidak ada angka palsu atau tidak sebanding di artefak F0. (−) Sebagian perbandingan tanpa LLM baru terlihat di F1–F4.

## ADR-066 — P1 terukur di F1: fixture `f1-core` + hook golden lintas modul (AUD-005)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-005.

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P1 (kolom verifikasi), ROADMAP F1 (Deliverable, Acceptance) dan F2 (Acceptance). Setiap passage mengutip ADR-066.

**Konteks.** P1 hanya didefinisikan untuk fixture "semua modul compiled-in dan enabled", yang butuh modul F2/F5. Angka F1 "≤ 4.000 (tool inti saja)" tidak punya definisi. `assemble` tidak ada di deliverable F1. Tokenizer ada di modul `bench`, sedangkan root `go.mod` tidak boleh me-require-nya (E-45).

**Keputusan.**
1. **P1 fase F1** = fixture `f1-core`: stable tier (varian tanpa modul, ADR-067) + katalog deferred = 0 + context tier diisi sampai cap per slot (fakta sistem ≤ 60, snapshot workspace ≤ 150, AGENTS.md fixture 20 KB dipotong ≤ 400, slot memori ≤ 300 dan skill ≤ 150 diisi teks sintetis sampai sub-cap, pemisah ≤ 40) + volatile tier maksimum (semua komponen ADR-067 terisi sampai sub-cap) + schema **8 tool inti eager** (tanpa `tool_search`/`tool_invoke`, F2). Gate: **≤ 4.000 tok o200k_base atas JSON request** (bukan usage provider). Inilah arti "tool inti saja".
2. **Hook lintas modul (tanpa dependency baru di root).** `internal/assemble` punya `TestFixedOverheadGolden`: merender request lengkap fixture (format wire openai-chat: `messages` + `tools`) ke `internal/assemble/testdata/p1/f1-core.request.json`, ditambah `f1-core.tiers.json` (`{stable, catalog, context, volatile, tools}` sebagai string). Test gagal bila file tidak byte-identik dengan hasil render (regenerasi: `-update`). Di modul `bench`, `TestFixedOverhead/f1-core` (`cd bench && go test ./budget -run TestFixedOverhead`, job CI `bench-budget`) menghitung o200k atas kedua file: total ≤ 4.000 dan tiap tier ≤ alokasi P1. Kedua test wajib hijau di CI.
3. **P1 penuh** (≤ 5.000, fixture `all-modules`: katalog, memori, dan skill nyata) = acceptance **F2**, memakai hook yang sama (`all-modules.request.json`). F5 tetap ("P1 ≤ 5.000 dengan 65 server tercache").
4. **Deliverable F1** bertambah `assemble` minimal (tier stable/context/volatile; tanpa katalog, retrieval memori, dan skill) dan `config` minimal (struct + validasi + default SPEC §12 untuk kunci yang dipakai paket F1; `TestConfigDefaultsMatchSpec` untuk kunci tersebut). LOC termasuk core F1 18.000 (SPEC §3.2.4 tidak berubah; `loc.budget` `assemble` 1.000 dan `config` 1.200 tetap).

**Dampak terukur (SPEC §20).** Token: gate F1 baru ≤ 4.000 (di dalam P1 5.000). LOC/binary/dependency 0. Latensi 0.

**Konsekuensi.** (+) P1 terukur sejak F1 tanpa menambah require di root. (−) Dua artefak golden harus diregenerasi setiap kali prompt berubah; perubahan itu tetap terlihat di review.

## ADR-067 — Tier prompt F1: aturan stable kondisional, volatile beku + sub-cap, estimator COST-09 di F1 (AUD-022, AUD-023, AUD-024)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-022, AUD-023, AUD-024. Mengoreksi ADR-058 (fase implementasi).

*(Amandemen 2026-10-18: butir 2 dikoreksi ADR-082 — sub-cap `<runtime>`, `user_changed` di slot notice volatile, kunci COST-07 tanpa `runtime`.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P1 (alokasi volatile), P15, §4.5, §6 (stable tier aturan 3/11, baris Volatile), §22.1 GAP-COST-08/GAP-COST-09; ROADMAP F1/F2; DECISIONS ADR-058 (penanda). Setiap passage mengutip ADR-067.

**Konteks.** Stable tier dikunci golden di F1, tetapi aturan 3 (katalog deferred, `tool_search`/`tool_invoke`) dan aturan 11 (`repo_map`) merujuk tool F2/F5. Volatile tier memuat `n/40` dan sisa budget yang berubah per iterasi, padahal tier ini dipasang di user message (render ulang memecah cache prefix), dan komponennya bisa melebihi 100 tok. P5/COST-08 (F1) memakai estimator COST-09 yang dijadwalkan F2.

**Keputusan.**
1. **Aturan stable kondisional (AUD-022).** Aturan 3 dirender hanya bila ≥ 1 grup deferred aktif; aturan 11 hanya bila `codeintel` aktif. Golden stable per (versi, config hash) sesuai P15; golden F1 = varian tanpa keduanya. Test: `TestStableTierGolden` dengan varian {tanpa modul, deferred aktif, codeintel aktif}; `TestPromptNoUnavailableTools` (stable tier tidak menyebut nama tool di luar array `tools` + katalog sesi).
2. **Volatile beku per turn (AUD-023).** Volatile tier dirender sekali saat turn dimulai dan tidak berubah selama turn. Sub-cap (Σ 100): waktu 12, sisa budget turn 15, todo 30 (≤ 3 item terbuka, tiap item dipotong 40 char, sisanya `+N more`), nama skill/tool relevan baru 20, satu slot notice gabungan 23 (notice pertama + `+N more`). Info per iterasi (`n/40`, notice proses, peringatan `no_progress`, `user_changed`) = sufiks `<runtime>` ≤ 40 tok pada `tool_result` terbaru, **disimpan di payload event `tool_result` itu** (`runtime`) saat iterasi berjalan, sehingga render ulang byte-identik (prefix cache utuh) dan tidak ada event/hasil sintetis (P4). Test: `TestVolatileTierCaps`, `TestPrefixStableWithinTurn` (request iterasi n+1 diawali byte request iterasi n).
3. **Estimator COST-09 di F1 (AUD-024).** Rumus tunggal ADR-058 (rasio per script + kalibrasi EMA dari `usage_ledger`) diimplementasikan di **F1** dalam `internal/budget` (ledger = deliverable F1). F2 hanya menambah formula token gambar dan pemicu compaction P8. `TestTokenEstimatorCalibration` korpus teks (en/id/ru/ja) masuk acceptance F1; varian gambar di F2.

**Dampak terukur (SPEC §20).** Token: volatile tetap ≤ 100; sufiks `<runtime>` ≤ 40 tok per iterasi masuk hitungan input turn (bukan fixed overhead). Cache: 0 cache break dalam turn karena volatile. LOC: dalam core F1 18.000. Binary/dependency 0.

**Konsekuensi.** (+) Prompt F1 tidak menyebut tool yang tidak ada; cache hit dalam turn terjaga. (−) Notice yang muncul di tengah turn baru terlihat lewat sufiks `<runtime>`, tidak di volatile tier.

## ADR-068 — Verifikasi GAP berfase F1: subset F1 + cek penuh di fase pemilik komponen (AUD-006)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-006.

*(Amandemen 2026-10-18: subset SEC-14 F1 memakai approver palsu ber-preview; headless produksi `denied` — ADR-084 butir 1.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §22.1 (kolom Fase baris di bawah), ROADMAP §"Adopsi GAP-SWEEP" (baris F1 + catatan subset). Setiap passage mengutip ADR-068.

**Konteks.** Sekitar separuh verifikasi GAP berfase F1 memakai komponen F2–F7, sehingga F1 tidak bisa diterima dengan bukti.

**Keputusan.** Kolom Fase §22.1 untuk baris di bawah ditulis `F1 (subset) / Fx (penuh)`. Acceptance F1 = cek subset; cek penuh menjadi acceptance fase pemilik komponen. Baris GAP berfase F1 lain (SEC-12, REL-01 bagian F1, REL-06, REL-15, COST-07, COST-15, TOOL-03, TOOL-04 bagian shell, INF-05, INF-07) sudah dapat diverifikasi penuh di F1 dan tidak berubah. Verifikasi REL-06 memakai status ADR-072.

| GAP | Cek subset F1 | Cek penuh → fase |
|---|---|---|
| SEC-02 | `TestEnvAllowlist` pada runner local pipe + PTY + sesi persisten × 70 var (50 pola + 20 non-pola) → 0 bocor | hook F3; execute_code, MCP stdio, LSP F5; bridge F7 |
| SEC-11 | `damnbot run` headless + provider openai-chat + 8 tool inti, 20 canary → 0 hit log ≥ INFO; `TestLogContentTTL`; analyzer 0 temuan | TUI/ACP F3; E2E gateway/cron F4; webhook F7 |
| SEC-14 | `TestNoSudoPassword`; sudo lewat approver headless dan fake approver → tanya per perintah, tanpa approver → `denied` 100% (ADR-069) | sudo dari fake Telegram F4 |
| SEC-16 | `TestEgressDLP/web_fetch` × 4 encoding → 4/4 diblok | vault F2; send_message F4; MCP HTTP F5; web_search/browser F6; webhook F7 |
| COST-02 | larangan probe: `TestNoContextProbe` + proxy rekam startup + 1 turn dengan openai-chat → 0 request tanpa konten user | Bedrock/SigV4 F5; persist F2 (tetap) |
| COST-08 | `TestTurnAggregateScaled` dengan estimator F1 (ADR-067) | — (penuh di F1) |
| PROV-02 | fallback window konservatif + mode degradasi tanpa katalog (`TestLearnedContextWindow`, `TestContextTierBudget/degraded` tanpa baris katalog) | katalog deferred di mode degradasi F2; persist F2 |
| REL-07 | fake provider diam → `provider_fallback` ≤ 90 s; fake clock ±1 jam → 0 kill palsu; status di `run` headless | status TUI/ACP F3; Telegram F4; web F8 |
| OPS-01 | `TestConfigGeneratedNoDrift`, forbidigo `os.Getenv` di luar `internal/config`, `TestConfigParityCLIServe` untuk `run` | TUI/`serve`/cron F3–F4; `config explain` F3 |
| OPS-02 | `TestEgressRegistryComplete` + proxy rekam `run` headless `background=false` → 0 koneksi selain host provider | `serve` + TUI 30 menit, `doctor --network` F3 |
| REL-01 | `TestFailedTurnPersisted` untuk 400 non-retryable, cancel, panic boundary | timeout compaction F2 |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Jumlah GAP per fase di tabel adopsi tidak berubah (118/118); hanya cakupan cek per fase yang dieksplisitkan.

**Konsekuensi.** (+) Acceptance F1 dapat dibuktikan. (−) Enam fase berikutnya mewarisi cek penuh yang harus diingat; tabel ini sumbernya.

## ADR-069 — Batas keamanan shell: classifier allowlist, path operand, irreversible, sudo, `shell_allow`, workspace trust (AUD-001, AUD-008, AUD-009, AUD-010, AUD-012, AUD-017)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; opsi paling aman di setiap titik). Menutup AUD-001 (BLOCKER), AUD-008, AUD-009, AUD-010, AUD-012, AUD-017.

*(Amandemen 2026-10-18: "budget exec 7.000" dibaca `loc.budget` `exec` 3.500 + `exec/classify` 1.500 (ADR-081); `allow_session` untuk shell dihapus dan `outside_read` didefinisikan ulang oleh ADR-088.)*

*(Amandemen 2026-10-18: butir 2, bagian auto butir 3, butir 4 (auto), butir 6, tabel 42 kasus, dan jalur auto verify **digantikan ADR-079** (shell tidak pernah di-auto-approve classifier; auto hanya `shell_allow` argv-persis). Butir 9 dikoreksi ADR-079 butir 8.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §4.3 (langkah 5 dan 7), §12 (`[exec]`, `[security]`, `deny_paths` default), §14.1, §14.2, §14.5; EXEC-AND-CODING §A.8 (klasifikasi, kebijakan, sandbox cwd) dan §A.10/EXC-13; CODE-TOOLS §8; CROSS-CUTTING §1; TRUST-MODEL §1–§3. Setiap passage mengutip ADR-069.

**Konteks.** Perintah shell `read_only` di-auto-approve di semua mode tanpa memeriksa operand path terhadap root dan `deny_paths` (`cat ~/.ssh/id_rsa`, `cat /proc/$PPID/environ`). Daftar `read_only` berbasis "contoh" juga memuat perintah yang dapat mengeksekusi program atau menulis file (`git` + config repo, `find -execdir`, `rg --pre`, `go env -w`). Interpreter yang membaca program dari stdin/pipe hanya `mutating`. Classifier tidak pernah menghasilkan `irreversible`, sudo hanya `mutating`, cara cocok `shell_allow` tidak didefinisikan, dan verify/`go test` menjalankan kode workspace tanpa gerbang trust. Pipeline §4.3 menolak baca di luar root di langkah 5, sehingga baris "tanya" di §14.1 tidak pernah tercapai. TRUST-MODEL menyebut DLP netx dan sandbox path sebagai boundary tanpa membatasi cakupannya.

**Keputusan.**

1. **Keluaran classifier** = `Verdict{class, flags, reasons}`. Kelas (urutan ketat approval): `read_only` < `mutating` < `destructive` = `unknown` < `irreversible`. Flag: `denied_path`, `outside_read`, `privileged`, `network`, `tainted_session`. Kebijakan memakai kelas tertinggi dari semua simple command, dan flag selalu berlaku.
2. **`read_only` = allowlist positif** (argv0 + flag), bukan daftar contoh. Perintah di luar tabel minimal `mutating`. Menambah entri = ADR.

   | argv0 | Flag/bentuk ditolak (→ minimal `mutating`) |
   |---|---|
   | `ls`, `cat`, `head`, `tail`, `wc`, `stat`, `file`, `du`, `pwd`, `which`, `basename`, `dirname`, `realpath`, `readlink`, `cut`, `tr`, `cmp`, `diff`, `true`, `false`, `echo`/`printf` (tanpa redirect) | redirect selain ke `/dev/null` |
   | `sort`, `uniq` | `-o`/`--output`, `--compress-program`, operand output kedua (`uniq`) |
   | `grep`/`egrep`/`fgrep` | — (operand `-f FILE` ikut dicek sebagai path) |
   | `rg` | `--pre`, `--pre-glob`, `-z`/`--search-zip` |
   | `find` | `-exec`, `-execdir`, `-ok`, `-okdir`, `-delete`, `-fprint`, `-fprint0`, `-fprintf`, `-fls` |
   | `tree` | `-o` |
   | `git status`, `diff`, `log`, `show`, `blame`, `rev-parse`, `ls-files`, `branch --list`, `remote -v` | `-c`, `--config-env`, `--exec-path`, `--git-dir`, `--work-tree`, `-C` di luar root, `--output`, `--ext-diff`, `--textconv`, `-O`/`--open-files-in-pager`; **dan** workspace belum trusted (butir 7) |
   | `go version`, `go env` | `-w`, `-u` |

   `git` berkelas `read_only` selalu dijalankan runner dengan `--no-pager -c core.fsmonitor= -c core.hooksPath=/dev/null -c diff.external= -c core.sshCommand=false`, ditambah `--no-ext-diff --no-textconv` untuk `diff`/`log`/`show`/`blame`, dan env `GIT_TERMINAL_PROMPT=0`. `go list` dan `go vet` (membangun paket, bisa mengunduh toolchain/modul) = `mutating`.
3. **Operand path (AUD-001).** Untuk **setiap** simple command (semua kelas), operand path di-resolve: argumen posisi perintah file, `-f`/`--file`/`--ignore-file`/`--exclude-from`, redirect `<`/`>`/`>>`, starting point `find`, pathspec dan `-C` git. Resolusi: ekspansi `~` dan `$HOME` saja, `..`, glob terhadap filesystem, lalu symlink per komponen seperti §14.2. Hasil:
   - cocok `deny_paths` → flag `denied_path` → error `denied_path` di langkah 5, **tidak bisa di-approve** di mode apa pun;
   - di luar root (bukan direktori blob) → flag `outside_read` (baca) atau kelas minimal `destructive` (tulis, aturan A.8 yang sudah ada) → baris §14.1 "Read/write di luar workspace": tanya, `allow_session` hanya untuk himpunan path persis; headless → `outside_workspace`; output dibungkus `<untrusted source="shell:outside_workspace">`;
   - operand yang tidak bisa di-resolve statis (variabel selain `HOME`, `$(…)`, backtick, `<(…)`) → `unknown`.
   Default `deny_paths` ditambah `~/.netrc`, `~/.git-credentials`, `~/.config/gh`, `~/.docker/config.json`, `~/.kube`, `<config_dir>/secrets.enc`, `/proc/*/environ`, `/proc/*/mem`.
4. **Interpreter dan sesi persisten (AUD-008).** argv0 ∈ {`sh`, `bash`, `zsh`, `dash`, `ksh`, `fish`, `python*`, `node`, `deno`, `bun`, `perl`, `ruby`, `php`, `lua`, `pwsh`, `powershell`, `osascript`} dengan program dari stdin, pipe, heredoc/here-string, parameter `stdin` schema, atau string `-c`/`-e` non-literal → `unknown`. `-c '<literal>'` tetap direkursi. `source`/`.` → `unknown` (menjalankan isi file). `awk`/`sed`/`xargs`/`env`/`nice`/`timeout`/`nohup` tidak pernah `read_only` (perintah di dalamnya tetap direkursi). Sesi persisten yang menjalankan `alias`, definisi fungsi, `hash -p`, `trap`, `enable`, `shopt`, atau assignment/export `PATH`, `LD_PRELOAD`, `LD_LIBRARY_PATH`, `BASH_ENV`, `ENV`, `PROMPT_COMMAND`, `IFS`, `GIT_*` → flag `tainted_session`: semua perintah berikutnya di sesi itu minimal `mutating` sampai sesi ditutup.
5. **`irreversible`, jaringan, dan sudo (AUD-009).**
   - `irreversible`: `git push` (semua bentuk; bentuk force juga `destructive`, preview memuat keduanya), `git send-email`; `npm\|pnpm\|yarn publish`, `npm unpublish\|deprecate\|dist-tag`, `cargo publish\|yank`, `twine upload`, `gem push\|yank`, `poetry publish`, `uv publish`, `docker push`, `helm push`; `gh`/`glab` yang mengubah state remote (`release`, `pr create\|merge\|close\|comment`, `issue create\|comment\|close`, `repo create\|delete\|edit`, `secret set`, `workflow run`, `api` dengan metode non-GET atau field `-f`/`-F`); klien HTTP (`curl`, `wget`, `http`, `xh`) dengan metode/body mutating (`-X` ≠ GET/HEAD, `-d*`, `--data*`, `-F`, `-T`, `--json`, `--post-*`, `--method`) atau host metadata/link-local (169.254.0.0/16, `fd00:ec2::254`, `metadata.google.internal`); `ssh`, `scp`, `sftp`, `rsync` ke host remote, `nc`/`ncat`/`socat`/`telnet`; package manager sistem (`apt`, `apt-get`, `dnf`, `yum`, `pacman`, `zypper`, `apk`, `brew`, `port`, `snap`, `flatpak`, `choco`, `winget`, `scoop`) yang memasang/menghapus, dan `pip install` di luar venv di dalam root.
   - Instal dependency lokal proyek (`npm install\|ci`, `pnpm i`, `yarn`, `pip install` di venv dalam root, `go mod download`, `cargo fetch`) dan klien HTTP GET ke host non-loopback = `mutating` + flag `network` (output dibungkus `<untrusted source="shell:network">`). Egress subprocess **tidak** melewati gerbang DLP/SSRF `netx`.
   - `sudo`/`doas`/`su`/`pkexec`/`runas` → flag `privileged`: tanya per perintah di **semua** mode termasuk `auto`, preview argv lengkap; `allow_session` hanya untuk subject persis; headless tanpa approver → `denied`; tidak pernah lewat `shell_allow`, `destructive_allow`, `allow_destructive`, atau `smart_approval` (SEC-14).
   - `allow_session`, `allow_destructive`, `destructive_allow`, dan `smart_approval` **tidak pernah** berlaku untuk kelas P26 (`irreversible`, `monetary`, `delegated_agent_bypass`). Perintah `destructive` sekaligus `irreversible` diperlakukan sebagai `irreversible`.
6. **`shell_allow` (AUD-010).** Entri = prefix argv (token) setelah normalisasi. argv0 harus berupa nama tanpa path yang di-resolve lewat `PATH` ke binary yang sama dengan resolusi entri saat sesi dimulai; argv0 relatif/`./x` tidak pernah cocok. Cocok per simple command; pipeline/list cocok hanya bila setiap simple command cocok entri atau `read_only`. Flag eksekusi ditolak di semua entri: `-exec`, `-toolexec`, `-vettool`, `--pre`, `-o` di luar root. `shell_allow` hanya menaikkan `mutating` → auto dan tidak pernah mengabaikan `denied_path`, `outside_read`, `privileged`, `tainted_session`, `unknown`, `destructive`, `irreversible`. Verify (B.2, `exec.auto_allow_verify`) dan entri `shell_allow` yang menjalankan kode workspace (`go test`, `go build`, `make`, `npm test`, …) hanya auto bila (a) workspace trusted, (b) turn tidak tainted (ADR-070), dan (c) tidak ada file yang ditulis tanpa approval eksplisit di turn itu. Selain itu → tanya.
7. **Workspace trust.** Saat root dibuka pertama kali secara interaktif: prompt "trust workspace <path>?". Keputusan disimpan per path kanonik di config `security.trusted_workspaces`. Headless `run`: untrusted kecuali path ada di `security.trusted_workspaces`. Workspace untrusted: AGENTS.md dan context file dibungkus `<untrusted source="workspace">` (data, bukan aturan), konten file workspace men-taint turn (ADR-070), verify-auto dan `shell_allow` mati, `git` read tidak `read_only`, hook proyek tidak berjalan (F3). TRUST-MODEL §2 ditambah aktor "konten repo/workspace".
8. **Baca di luar root lewat tool file (AUD-012).** Langkah 5 §4.3 menghasilkan verdict `outside_root` (bukan error) yang diteruskan ke langkah 7: interaktif → tanya "read outside workspace: <path>" (`allow_session` hanya path persis); headless → `outside_workspace`. `deny_paths` tetap error di langkah 5. Konten yang dibaca dibungkus `<untrusted source="file:outside_workspace">`. CODE-TOOLS §8 diselaraskan (baca dan tulis).
9. **Cakupan boundary (AUD-017).** TRUST-MODEL §1 dan P29: gerbang DLP egress = boundary **hanya** untuk egress tool built-in lewat `internal/netx`. Egress subprocess (shell, execute_code, hook, MCP stdio, LSP) = heuristik (classifier + approval). Sandbox path = boundary untuk tool file di semua mode **dan** untuk jalur auto-approve shell (`read_only` dengan operand ter-resolve). Shell non-`read_only` = heuristik; isolasi kuat = backend container. Vulnerability: perintah yang mengeksekusi kode, menulis, berkelas `destructive`/`irreversible`, atau membaca path `deny_paths`/luar root yang diklasifikasi `read_only` atau di-auto-approve (target EXC-13 = 0). Efek tersembunyi dalam perintah yang sudah berkelas `mutating`/`unknown` dan disetujui user = sisa risiko.

**Test acceptance F1** (classifier, `internal/exec/classify`): `TestClassifyCorpus` (EXC-13, ≥ 200 kasus, termasuk tabel di bawah; metrik: 0 `read_only` yang mengeksekusi, menulis, atau membaca path terlarang/luar root), `TestShellDenyPaths` (≥ 30 perintah `cat`/`head`/`grep`/`cp`/`tar`/`base64`/`rg -f` × `deny_paths` default → 0 tereksekusi), `TestShellIrreversibleAlwaysAsk`, `TestSudoAlwaysAsk`, `TestShellAllowMatching`, `TestWorkspaceTrustGate`, `TestReadOutsideWorkspaceAsks`, `TestSessionTaintPersistent`.

| # | Perintah (cwd = root trusted kecuali disebut) | Verdict wajib | `ask` | `auto` | Headless |
|---|---|---|---|---|---|
| 1 | `ls -la src` | `read_only` | auto | auto | auto |
| 2 | `cat README.md \| wc -l` | `read_only` | auto | auto | auto |
| 3 | `cat ~/.ssh/id_rsa` | `denied_path` | error | error | error |
| 4 | `cat /proc/$PPID/environ` | `unknown` (variabel) | tanya | tanya | `denied` |
| 5 | `cat /proc/1/environ` | `denied_path` | error | error | error |
| 6 | `head -c 100 ~/.aws/credentials` | `denied_path` | error | error | error |
| 7 | `grep -r token ~/.config` | `outside_read` | tanya | tanya | `outside_workspace` |
| 8 | `cat ../other/notes.txt` (di luar root) | `outside_read` | tanya | tanya | `outside_workspace` |
| 9 | `cat .env` (`.env` di `deny_paths` default) | `denied_path` | error | error | error |
| 10 | `cat $FILE` | `unknown` | tanya | tanya | `denied` |
| 11 | `find . -name '*.go' -execdir sh -c 'id' \;` | `unknown` (isi `-execdir` + interpreter) | tanya | tanya | `denied` |
| 12 | `find . -fls out.txt` | `mutating` | tanya | auto | `denied` |
| 13 | `rg --pre ./x.sh foo` | `mutating` | tanya | auto | `denied` |
| 14 | `git diff --output=/tmp/x` | `destructive` (tulis di luar root) | tanya + preview | tanya + preview | `denied` |
| 15 | `git status` (workspace trusted) | `read_only` + flag hardening | auto | auto | auto |
| 16 | `git status` (workspace untrusted) | `mutating` | tanya | auto | `denied` |
| 17 | `git -c core.pager=sh log` | `mutating` | tanya | auto | `denied` |
| 18 | `go env -w GOFLAGS=-x` | `mutating` | tanya | auto | `denied` |
| 19 | `echo 'rm -rf ~' \| bash` | `unknown` | tanya | tanya | `denied` |
| 20 | `base64 -d p.txt \| sh` | `unknown` | tanya | tanya | `denied` |
| 21 | `bash -c 'ls'` | `read_only` (rekursi literal) | auto | auto | auto |
| 22 | `python3 - <<'EOF' … EOF` | `unknown` | tanya | tanya | `denied` |
| 23 | `source ./env.sh` | `unknown` | tanya | tanya | `denied` |
| 24 | sesi persisten: `export PATH=./bin:$PATH` lalu `ls` | `ls` → `mutating` (`tainted_session`) | tanya | auto | `denied` |
| 25 | `git push origin main` | `irreversible` | tanya + preview | tanya + preview | `denied` |
| 26 | `git push --force origin main` | `irreversible` (+ `destructive`) | tanya + preview | tanya + preview | `denied` |
| 27 | `npm publish` | `irreversible` | tanya + preview | tanya + preview | `denied` |
| 28 | `curl -X POST https://api.example.com -d @a.json` | `irreversible` | tanya + preview | tanya + preview | `denied` |
| 29 | `curl http://169.254.169.254/latest/meta-data/` | `irreversible` | tanya + preview | tanya + preview | `denied` |
| 30 | `curl -s https://example.com` | `mutating` + `network` | tanya | auto (output `<untrusted>`) | `denied` |
| 31 | `sudo apt install jq` | `irreversible` + `privileged` | tanya + preview | tanya + preview | `denied` |
| 32 | `sudo ls /root` | `outside_read` + `privileged` | tanya | tanya | `denied` |
| 33 | `npm install` | `mutating` + `network` | tanya | auto | `denied` |
| 34 | `go test ./...` cocok `shell_allow`, trusted, turn bersih, tanpa file auto-written | `mutating` (allow) | auto | auto | `denied` kecuali `auto` |
| 35 | `go test ./...` setelah `edit_file` auto-approved di turn yang sama (mode `auto-edit`) | `mutating` | tanya | auto | `denied` kecuali `auto` |
| 36 | `go test -exec ./evil ./...` | `mutating` (flag ditolak `shell_allow`) | tanya | auto | `denied` kecuali `auto` |
| 37 | `./go test ./...` | `mutating` (argv0 relatif tidak cocok `shell_allow`) | tanya | auto | `denied` kecuali `auto` |
| 38 | `rm -rf ~` | `destructive` | tanya + preview | tanya + preview | `denied` |
| 39 | `cp ~/.git-credentials ./x` | `denied_path` | error | error | error |
| 40 | `tar czf a.tgz ~/.kube` | `denied_path` | error | error | error |
| 41 | `claude --dangerously-skip-permissions -p x` | `delegated_agent_bypass` | tanya + preview | tanya + preview | `denied` |
| 42 | `ssh host 'ls'` | `irreversible` | tanya + preview | tanya + preview | `denied` |

**Dampak terukur (SPEC §20).** Token 0. Latensi: resolusi operand ≤ beberapa `lstat` per perintah, di dalam `BenchmarkClassify` < 1 ms. LOC: dalam budget `exec` 7.000 dan `sandbox+redact` (SPEC §3.2.4 tidak berubah). Binary/dependency 0. Perilaku: lebih banyak prompt untuk perintah di luar allowlist, workspace untrusted, dan semua aksi P26 dari shell.

**Konsekuensi.** (+) Tidak ada jalur auto-approve shell yang membaca secret, mengeksekusi kode tersembunyi, atau menghasilkan efek eksternal tanpa approval. (−) Mode `ask` lebih sering bertanya; allowlist `read_only` harus dipelihara lewat ADR.

## ADR-070 — Taint lengket per turn (AUD-007)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-007.

*(Amandemen 2026-10-18: sumber taint ditambah setiap output shell di workspace untrusted (ADR-079 butir 6) dan output `outside_read` (ADR-088 butir 2).)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §4.3 (langkah taint baru), §14.1 (baris taint), §14.5; CROSS-CUTTING §1; SCORECARD E10 (rujukan mekanisme). Setiap passage mengutip ADR-070.

**Konteks.** Aturan taint berlaku untuk "aksi setelah membaca `<untrusted>` di iterasi yang sama". Dalam satu iterasi model memutuskan semua tool call sebelum melihat hasilnya, sehingga aksi yang dipicu injeksi selalu muncul di iterasi berikutnya dan aturan tidak pernah terpicu. CROSS-CUTTING menyebut "iterasi yang sama" sekaligus "per turn". Output shell tidak dibungkus `<untrusted>`.

**Keputusan.**
1. **Sumber taint:** setiap `tool_result` yang dibungkus `<untrusted>` (§14.5, termasuk file di luar root dan file workspace untrusted, ADR-069), dan output shell berflag `network` atau `outside_read` (ADR-069).
2. **Masa berlaku:** aktif sejak `tool_result` sumber di-commit ke konteks turn sampai `turn_end`, untuk semua iterasi berikutnya dalam turn itu. `user_message` berikutnya memulai turn tanpa taint.
3. **Efek:** selama taint aktif, setiap aksi non-read butuh approval di semua mode, tanpa `allow_session`; headless tanpa approver → `denied`. Termasuk: shell selain `read_only` murni di dalam root (tanpa flag), termasuk `shell_allow` dan verify; `write_file`/`edit_file`; HTTP mutating; `send_message`; `browser_eval`; MCP non-read; `delegate`; `execute_code`.
4. **Pipeline §4.3** mendapat langkah **6a "Taint gate"** di antara langkah 6 (hook) dan 7 (approval): menandai `ApprovalRequest.tainted` dan memaksa jalur tanya.
5. Test: `TestTaintForcesApproval` (wajib mencakup aksi di iterasi N+1 setelah `web_fetch` di iterasi N, dan reset pada turn baru), `TestShellNetworkOutputUntrusted`.

**Dampak terukur (SPEC §20).** Token: pembungkus `<untrusted>` pada output shell berjaringan (≤ 10 tok per hasil). Latensi/LOC/binary/dependency 0 (dalam budget `tools`/`exec`). Perilaku: turn yang membaca konten tak tepercaya meminta approval untuk aksi berikutnya.

**Konsekuensi.** (+) Mekanisme E10 benar-benar terpicu pada pola serangan nyata. (−) Task headless yang mengambil konten web lalu mengedit file berhenti di `denied` kecuali ada approver.

## ADR-071 — Env anak, redaksi secret, dan log DEBUG (AUD-011, AUD-013, AUD-014, AUD-015, AUD-016)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-011, AUD-013, AUD-014, AUD-015, AUD-016. AUD-011 menerapkan keputusan ADR-033 Q36 yang sudah ada.

*(Amandemen bertanggal 2026-10-18, commit `a88680e`: butir 1 (secret ref `keychain:<id>` di `exec.env`), butir 4 (CT-20 = ≥ 330), dan Aturan penerapan diubah di tempat — dicatat di sini (NEW-49). Butir 1 dan 3 diamandemen lagi oleh ADR-081.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P23, §12 (`[exec]`), §14.3, §15 (baris DEBUG), §16, §22.1 GAP-TOOL-03, P29; EXEC-AND-CODING §A.6, §A.8 (Env anak), EXC-15; CODE-TOOLS §5/§8 (guard sentinel) dan CT-20; CROSS-CUTTING §1; SCORECARD "Edit salah lokasi"; MITIGATION-MATRIX H-08, risiko validator, GAP-TOOL-03; ROADMAP F1. Setiap passage mengutip ADR-071.

**Keputusan.**
1. **Env anak (AUD-011).** EXEC §A.8 "Env anak" diganti rujukan allowlist GAP-SEC-02 (deny-by-default). Satu kunci pewarisan: `exec.env_allow` (daftar **nama persis** var induk yang boleh diwariskan selain allowlist SEC-02; tanpa glob). `exec.pass_env` dihapus. `exec.env` tetap ada dengan arti berbeda: map nama → nilai yang **di-set** untuk anak (dipakai §A.6), bukan pewarisan dari induk. Nilai `exec.env` boleh berupa referensi secret `keychain:<id>` (bentuk CROSS-CUTTING §1.2), di-resolve saat spawn, tidak pernah masuk konteks model, dan nilainya otomatis menjadi sumber redaksi §14.3; ini satu-satunya jalur menyuntik secret ke subprocess (menggantikan `pass_env`). Kunci provider hanya diwariskan bila namanya tercantum eksplisit di `exec.env_allow`. EXC-15 `TestChildEnvScrub` diganti `TestEnvAllowlist` (SEC-02; 0 var di luar allowlist + `exec.env_allow` + `exec.env`).
2. **Pola redaksi (AUD-013).** Tambahan pada §14.3:
   - blok PEM multi-baris `-----BEGIN [A-Z0-9 ]*PRIVATE KEY-----` sampai `-----END [A-Z0-9 ]*PRIVATE KEY-----` diganti seluruhnya; fail-closed: tanpa END → redaksi sampai akhir chunk/blob;
   - `(sk|rk)_(live|test)_[0-9A-Za-z]{20,}` (Stripe), `AIza[0-9A-Za-z_-]{35}` (Google), `glpat-[0-9A-Za-z_-]{20,}` (GitLab), `hf_[A-Za-z0-9]{30,}` (Hugging Face), `npm_[A-Za-z0-9]{36}` (npm), JWT `eyJ[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}\.[A-Za-z0-9_-]{8,}`;
   - userinfo URL `[a-z][a-z0-9+.-]*://[^/\s:@]+:[^@\s/]+@` (bagian password diganti, mis. `DATABASE_URL`).
   Korpus `TestRedaction`: testdata yang diturunkan dari daftar rule gitleaks versi ter-pin (data, bukan dependency; SHA-256 sumber dicatat di testdata), ≥ 1 sampel positif per pola §14.3 termasuk badan PEM penuh, ditambah 60 secret sintetis; korpus negatif ≥ 200 string mirip (hash git, UUID, base64 acak) dengan false positive dilaporkan.
3. **Redaksi stream (AUD-014).** Buffer tetap 256 B diganti: penyaring hanya menahan sufiks terpanjang yang masih bisa menjadi awal pola atau nilai secret yang dikenal (pencocokan parsial), dengan batas per pola. Chunk tanpa kecocokan parsial dikirim segera (0 latensi tambahan, P17). Pola dengan ekor tak terbatas (`Bearer \S+`) ditahan sampai terminator atau 4 KiB lalu diredaksi fail-closed. Setelah `-----BEGIN … PRIVATE KEY-----` terlihat, output ditahan sampai END atau akhir stream, lalu seluruh blok diredaksi. Test: `TestStreamRedactSplit` (setiap secret korpus dipotong di setiap offset byte antar 2 chunk → 0 bocor), `TestFirstTokenLatency` dengan penyaring aktif.
4. **Sentinel redaksi di tulis (AUD-015).** `old_string`, `new_string`, `new_text`, dan `write_file.content` yang memuat sentinel `[REDACTED:` → `invalid_args`, hint "secret redacted; edit around it". CT-20 bertambah 5 kasus (masing-masing field + kombinasi multi-edit): ukuran suite = **≥ 330 kasus**, satu angka di semua dokumen (menggantikan 325 dari ADR-037 V-04).
5. **Log DEBUG (AUD-016).** §15 baris DEBUG: request/response = metadata (model, ukuran, sha256, status, durasi, usage), **tanpa body**. Body (tetap ter-redaksi, > 64 KB dipotong + sha) hanya bila `log.message_content = true`, dengan TTL P29. `TestLogNoMessageContent` mencakup level DEBUG dengan `message_content = false`.

**Dampak terukur (SPEC §20).** Token 0. Latensi: first token tidak bertambah untuk chunk tanpa kecocokan parsial (P17 tetap). LOC: dalam budget `sandbox+redact` 1.300 dan `codetools`. Binary/dependency 0 (korpus gitleaks = testdata). Config: `exec.pass_env` dihapus (belum pernah dirilis, tanpa migrasi).

**Konsekuensi.** (+) Badan kunci privat dan format token umum tidak sampai ke model, DB, atau log; secret di file tidak pernah tertimpa sentinel. (−) Output yang mirip awal pola ditahan sebentar (dibatasi per pola).

## ADR-072 — Kontrak loop/tool F1: `content_filtered`, kode error, cancel, proses background (AUD-018, AUD-019, AUD-020, AUD-021)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; AUD-021 opsi (b), permukaan terkecil). Menutup AUD-018, AUD-019, AUD-020, AUD-021.

*(Amandemen 2026-10-18: butir 1 dan 3 dilengkapi ADR-082 — cabang `content_filter` + `tool_calls`, enum `ExecStatus`/`RecoveryStatus` tertutup.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P2, P24, §3.5, §4.2, §4.3 (daftar kode error), §5.1 (`shell`), §12 (`exec.kill_grace_s`), §13.3 (pemetaan ACP), §22.1 GAP-REL-06; EXEC-AND-CODING §A.2, §A.3, §A.4, §A.5, §A.10; ROADMAP F1. Setiap passage mengutip ADR-072.

**Keputusan.**
1. **`content_filtered` (AUD-018).** stop_reason baru `content_filtered` (ACP `refusal`), dipicu `finish_reason=content_filter` atau padanan typed per adapter. Tanpa retry dan tanpa fallback. Konten parsial disimpan sebagai event `assistant_message` dengan `filtered: true` dan tidak dipakai sebagai jawaban final. `damnbot run` exit 2. Test: `TestContentFilterStop`.
2. **Kode error (AUD-019).** Daftar tertutup §4.3 ditambah `write_verify_failed` (TOOL-03), `egress_disabled` (OPS-02), dan `invalid_skill` (SEC-26). `TestErrorCodesClosed` memakai daftar ini.
3. **Cancel dan timeout (AUD-020).**
   - 1 s (REL-06) = tenggat **pengembalian hasil** setelah cancel. Satu enum status hasil `shell`/tool: `exited`, `timeout`, `cancelled_confirmed`, `still_running`, `waiting_for_input`. Pohon proses yang belum terbukti mati dalam 1 s → `still_running` + `process_id` registry internal; urutan kill berlanjut di background dan hasilnya dicatat sebagai `system_notice{kind:"process_exit"}`.
   - §4.2: `tool_result{status:"cancelled_confirmed"|"still_running", by:"user"}`. Re-dispatch tool `non_idempotent` setelah `still_running` wajib menunggu `cancelled_confirmed` atau konfirmasi user (P22/REL-06).
   - Urutan kill: SIGTERM → `exec.kill_grace_s` → SIGKILL, dengan `exec.kill_grace_s` 1–3 (default 3; nilai > 3 ditolak validasi config). P24 "≤ `timeout_s` + 4 s" tetap berlaku.
   - Nama test di §A.10: `TestCancelConfirmed` (REL-06, F1), `TestCancelConfirmBeforeRedispatch` (F1), `TestACPChildStdinIsolation` (REL-13, F3).
4. **Proses background (AUD-021, opsi b).** Di build F1, `shell{background:true}` → `invalid_args` (hint "background not available; run in foreground with timeout_s"). Perintah foreground yang terdeteksi menunggu input: pohon proses di-kill lalu dikembalikan `status:"waiting_for_input"` dengan prompt yang tertangkap, `killed:true`, tanpa `process_id`. Background, `process_list/output/wait/send/kill`, dan pemindahan foreground → background datang di **F5** bersama TOOL-077/EXEC-009 (ADR-064). Bagian test P24/EXC-09/EXC-11 yang membutuhkan `process_*` pindah ke acceptance F5. Registry proses internal (untuk `still_running`) tetap F1.

**Dampak terukur (SPEC §20).** Token 0 (enum/kode tidak menambah schema eager; parameter `background` tetap di schema, 0 tok perubahan). Latensi 0. LOC: dalam budget `exec`/`loop`. Binary/dependency 0.

**Konsekuensi.** (+) Setiap skenario punya stop reason sah; tidak ada proses yang tidak bisa dikendalikan model di F1. (−) Server dev/watch mode tidak tersedia sampai F5.

## ADR-073 — CI hardening dan gate arsitektur: sebagian diterapkan di F0, sisanya Deliverable F1 #0 (AUD-031, AUD-032, AUD-033, AUD-034, AUD-035)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-031..AUD-035. Mengoreksi ADR-048 butir 1 dan 3.

*(Amandemen bertanggal 2026-10-18, commit `fc1a4a9`: Aturan penerapan diubah di tempat (menambah `scripts/ci-meta-test.sh`, commit `8288d53`) — dicatat di sini (NEW-49). Butir 1 (meta.yml) diamandemen ADR-085.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): ROADMAP F1 (Deliverable #0, Acceptance); ENGINEERING §5 (job wajib, E-55, E-64); SPEC §16 (baris Perf/Unit); SELF-HEALING (rujukan `BenchmarkAppend`); DECISIONS ADR-048 (penanda); kode: `internal/ctxdb/*_test.go` (commit `fcc0af6`), `Makefile` (`test-timing`, `vet-cross`, `check`), `scripts/ci-meta-test.sh` (gate `vet-cross`), `.github/workflows/ci.yml`, `.github/workflows/meta.yml` (commit `8288d53`). Setiap passage dokumen mengutip ADR-073.

**Keputusan.**
1. **Diterapkan di F0 (kecil, lingkup F0).**
   - AUD-033: test `internal/ctxdb` compile di windows/darwin (`os_linux_test.go`/`os_other_test.go`, `//go:build linux` untuk skenario `/proc`/SIGSTOP; set test Linux identik). Target `make vet-cross` = `go vet -tags goolm ./...` untuk `windows/amd64`, `windows/arm64`, `darwin/amd64`, `darwin/arm64`; job CI `vet-cross` (gate murah sampai runner macOS/Windows aktif).
   - AUD-034: `meta-test` pindah ke workflow `meta.yml`: berjalan pada PR/push yang menyentuh `Makefile`, `scripts/**`, `.github/**`, `.golangci.yml`, `loc.budget`, `deps.allow`, `go.mod`, `go.sum`, `tools/go.mod`, dan nightly (cron) terhadap `main`. Job CI per PR tidak lagi memuat meta-test (E-64 ≤ 12 menit).
   - AUD-035 (1): `make test-timing` menjalankan benchmark sekali (`-bench=. -benchtime=1x`) agar benchmark yang memakai helper budget ikut dieksekusi di job tanpa `-race`. `BenchmarkAppend*` (F2) **tidak** ditegakkan di `test-timing`; budget absolut benchmark ditegakkan di job `perf` (butir 2).
2. **Deliverable F1 #0 (CI hardening), wajib merge sebelum paket F1 pertama.** Setiap job baru punya ≥ 1 kasus di `scripts/ci-meta-test.sh`.
   - Matrix test ubuntu/macos/windows (E-55; 2 QEMU tetap di F9), build-matrix 8 target kanonik (E-39), fuzz per target yang ada (`FuzzSSEParse`, `FuzzToolArgs`, `FuzzEditApply`, …), coverage E-58, `deadcode` (3 GOOS), flake nightly `-count=20`.
   - `pr-meta` + adrgate: PR yang mengubah `deps.allow`, `loc.budget`, default config §12, atau SPEC §2/§3.2.4/§17 wajib mengubah `docs/DECISIONS.md`; CHANGELOG; ukuran PR. `.github/CODEOWNERS` + `TestCodeownersComplete` (E-79).
   - Job `perf` (`-run '^$' -bench`, runner tetap, ambang ×1,5 hanya untuk waktu absolut, ADR-077) untuk `BenchmarkShellSpawn*`, `BenchmarkClassify`, `BenchmarkIterationOverhead`, dan benchmark codetools.
   - Helper budget waktu pindah dari `internal/ctxdb/timing_test.go` ke paket non-test `internal/testutil/timing` (build tag `!race`; baris `loc.budget` sendiri) sebelum paket non-ctxdb pertama membutuhkan asersi waktu; ADR-048 butir 3 dibaca "satu helper di `internal/testutil/timing`".
   - Meta-test gate test/race/timing diuji pada pohon minimal (`./internal/metaviolation/...`) agar durasinya O(1) terhadap ukuran repo.
   - Gate arsitektur (AUD-032), sebelum `loop`/`netx`/`provider` di-merge: `TestImportGraph` (`go list -deps -json` vs `internal/archtest/allowed.go`; E-01/E-02/E-06/E-08), analyzer `nohttpclient` (E-05), `TestFileLength` (E-14/E-71), `TestGlobalAllowlist` (E-10), `TestGoleakMain` di setiap paket yang memulai goroutine (E-56), `TestBudgetTableSum` (P21).
   - Gate yang ditunda punya fase eksplisit: `TestNoCrossPackageEmbedding` F2 (bersama `modules`), `TestNolintDensity` acceptance F1.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0 (semua tool sudah dipin di `tools/go.mod` atau stdlib; `deadcode`/`staticcheck` mengikuti AUD-074 di backlog ADR-078). LOC non-test: `internal/testutil/timing` ≤ 80 (F1 #0). CI: +1 job `vet-cross` (F0); meta-test tidak lagi di setiap PR.

**Konsekuensi.** (+) Gate lintas OS dan arsitektur ada sebelum kode F1 yang bergantung padanya; waktu CI PR turun. (−) Perubahan infra di luar daftar path tidak memicu meta-test sampai nightly.

**Errata (2026-10-20, REVIEW-F1-SEC7 TL7-12).** Matriks E-01 (`internal/archtest/allowed.go`) dan graf ENGINEERING §2.2.1 mendapat edge `internal/tools → internal/boundary`. Edge ini diizinkan karena E-24 mewajibkan `recover` hanya di `internal/boundary`, dan `Prepare`/`DispatchPrepared` memakai `boundary.Run` agar panic sebuah tool menjadi error terklasifikasi (`internal`) alih-alih mematikan turn. Tidak ada keputusan policy yang berpindah ke `boundary`.

## ADR-074 — Storage modul konsumen di `internal/ctxdb/<consumer>store` (AUD-036)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-036. Mengoreksi kalimat konsekuensi ADR-049.

**Aturan penerapan.** Teks diterapkan (2026-10-18): ENGINEERING §2.2 (E-04, daftar storage), §2.4 (tabel budget); `loc.budget`; DECISIONS ADR-049 (penanda). Setiap passage mengutip ADR-074.

**Konteks.** ADR-049 menyatakan storage memory/skills/journal tetap di paket masing-masing, padahal E-04 hanya mengizinkan `ctxdb` mengimpor SQLite. Headroom `ctxdb` tinggal ≈ 190 LOC, sedangkan F1 butuh ledger dan F2 butuh FTS/recall/memory. E-03 yang dirujuk ADR-049 adalah aturan frontend.

**Keputusan.**
1. E-04: hanya `internal/ctxdb/...` (paket dan subpaketnya) yang boleh mengimpor driver SQLite.
2. Storage konsumen ada di `internal/ctxdb/<consumer>store`, masing-masing dengan baris `loc.budget` sendiri yang **dipotong dari baris konsumennya** (Σ tidak berubah): `budget` 700 → `budget` 400 + `ctxdb/ledgerstore` 300 (F1); `memory+skills` 2.200 → 1.200 + `ctxdb/memstore` 600 + `ctxdb/skillstore` 400 (F2); `journal` 900 → 500 + `ctxdb/journalstore` 400 (F2); `ctxdb/gwstore` 900 dihitung dari budget `gateway` SPEC §3.2.4 (F4, ADR-053).
3. Konsumen mengakses store lewat interface kecil di sisi konsumen (E-09, ≤ 5 method); `cmd/damnbot` menyambungkan.

**Dampak terukur (SPEC §20).** LOC: Σ `loc.budget` core tetap 31.450 (pemindahan antar baris); SPEC §3.2.4 tidak berubah. Token/latensi/binary/dependency 0.

**Konsekuensi.** (+) Satu pemilik SQLite; storage konsumen tidak mendesak headroom `ctxdb`. (−) Lima subpaket tambahan dengan budget kecil.

## ADR-075 — Hardening `bench/h2h` sebelum smoke LLM F1 (AUD-037, AUD-038, AUD-039)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-037, AUD-038, AUD-039, dan usulan TS-E05. Mengamandemen ADR-062 butir 2–3. **Butir 1 (AUD-037) diimplementasikan di F0** setelah perubahan `bench/h2h` worker lain di-commit: `bench/h2h/ledger.go`, `proxy.go`, `run.go`, `cmd/h2h/main.go`, test di `ledger_test.go`. **Butir 2–3 (AUD-038, AUD-039) ditunda ke F1** sebagai prasyarat run smoke Damn-Bot pertama: keduanya butuh driver `damnbot run` (deliverable F1) dan isolasi container, di luar tooling F0.

*(Amandemen bertanggal 2026-10-18, commit `4cf5b9e`: Status dan butir 1 diubah di tempat setelah implementasi AUD-037 — dicatat di sini (NEW-49). Butir 1 dan 3 diamandemen ADR-085: ledger wajib di setiap `Recorder` berkunci, path tunggal, flock; gate isolasi bernama.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): ROADMAP F0 deliverable 2 dan F1 (Deliverable, Acceptance smoke); SCORECARD §2.3/§2.5. Setiap passage mengutip ADR-075.

**Keputusan.** Run smoke Damn-Bot F1 (ADR-062) tidak boleh dimulai sebelum seluruh butir berikut ada di kode `bench/h2h` beserta test-nya:
1. **Cap biaya (AUD-037).** Ledger persisten `bench/h2h/results/f1-spend.json` (Σ lintas run, ditulis atomik setiap respons; cap US$2,00 disimpan di file saat dibuat dan tidak bisa dinaikkan dari command line); request ditolak bila Σ + reservasi berjalan + proyeksi terburuk > cap. Default `-cap-usd` = cap ledger, sehingga sisa yang berlaku = US$2,00 − Σ (bukan 3); `-cap-usd` hanya bisa menurunkan. Harga ≤ 0 saat cap > 0 → error harness (proxy) dan penolakan start (`cmd/h2h`). Cap dicek dengan reservasi proyeksi terburuk (input = ⌈byte body / 2⌉ token tanpa diskon cache + `max_tokens`/`max_completion_tokens`/`max_output_tokens` atau 32.768 × harga output) sebelum forward. Semua POST upstream dihitung (`/chat/completions`, `/responses`); POST ke path lain ditolak. `prompt_tokens == 0` pada request non-kosong atau usage tidak ada → error harness `usage_missing`, biaya = proyeksi terburuk. Request yang di-abort atau gagal transport dihitung sebesar proyeksi terburuknya. Test: `TestLedgerCapAcrossRuns`, `TestReservationRejectsOverCap`, `TestPriceRequired`, `TestUsageMissing`, `TestUnknownPathRejected`.
2. **Oracle netral-agent (AUD-038, TS-E05).** Setiap adapter agent menyediakan (a) kanal jawaban final dan (b) pemetaan nama tool → kelas `exec`/`fs_write`/`read`. Tool call diambil dari respons (`tool_calls`) termasuk respons terakhir, bukan hanya dari riwayat request berikutnya. Rule kind tak dikenal = error harness. Driver `damnbot run` untuk `bench/h2h` menjadi deliverable F1. Test: `TestOracleDamnbotToolNames`, `TestUnknownRuleKindFails`, `TestToolCallsFromFinalResponse`.
3. **Isolasi (AUD-039).** Agent berjalan di container rootless atau user+net namespace dengan uid terpetakan berbeda dari proxy; proxy memegang kunci upstream di memori dan file kunci dihapus setelah dibaca; env agent berbasis allowlist (bukan denylist `CleanEnv`); rekaman dipindai untuk kunci dalam bentuk literal, base64, hex, dan terpotong sebelum di-commit (gagal = rekaman tidak di-commit). Agent tidak dijalankan dengan approval mati di host bersama (`--yolo` hanya di dalam isolasi).
4. ROADMAP F0 deliverable 2 dan SCORECARD §2.3 dikoreksi sesuai implementasi: run berurutan per agent di F0/F1; run berpasangan dengan urutan diacak dan container wajib untuk RC F9.

**Dampak terukur (SPEC §20).** Token/latensi/LOC produksi/binary/dependency 0 (kode di modul `bench`). Biaya LLM F1 tetap ≤ US$2,00, kini ditegakkan kode.

**Konsekuensi.** (+) Cap ADR-062 tidak bisa dilewati; E6/E10 bermakna untuk kedua agent; host tidak terpapar agent. (−) Smoke F1 menunggu tiga perubahan harness.

## ADR-076 — Batas keamanan sebelum F4/F5: RPC `execute_code`, isolasi role gateway `user`, approval `mcp serve` (AUD-040, AUD-041, AUD-042)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-040 (F5), AUD-041 (F4), AUD-042 (F5). Tidak ada kode F0/F1 terdampak.

*(Amandemen 2026-10-18: butir 2 dan 3 dilengkapi ADR-084 — kanal P26 = kanal preview §18.2 termasuk ACP; NULL workspace = default turunan; field `origin` di `approval_request`.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §9.2, §9.6, §9.7, §14.1, §14.2, §22.1 GAP-COST-10; EXEC-AND-CODING §B.7; TRUST-MODEL §2. Setiap passage mengutip ADR-076.

**Keputusan.**
1. **`network = "deny"` mencakup RPC (AUD-040).** Bila `execute_code`/Starlark berjalan dengan `network = "deny"`, RPC/binding ke `web_fetch` → `denied{reason:"network_deny"}`. `shell` lewat RPC dijalankan dengan penegakan jaringan yang sama (netns/sandbox-exec); bila penegakan tidak tersedia di OS itu → `denied`. Test F5: `TestExecuteCodeRPCNetworkDeny`.
2. **Isolasi role gateway `user` (AUD-041).** Untuk role `user`, root sandbox = **hanya** `gateway.users.<id>.workspace` (default `<data_dir>/gw-workspaces/<identity_hash>`, dibuat otomatis, tidak pernah NULL), menggantikan cwd owner/`serve`. Pengecualian baca direktori blob hanya untuk blob milik sesi identitas itu. Shell untuk role `user` hanya lewat backend container; tanpa container → shell tidak tersedia untuk role `user`. Test F4: `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope`.
3. **`mcp serve` tidak menyetujui aksinya sendiri (AUD-042).** Approval dari sumber `mcp_serve` tidak boleh menjawab permintaan yang berasal dari `mcp_serve`, dan tidak boleh menjawab kelas P26 (`irreversible`, `monetary`, `delegated_agent_bypass`) dari sumber mana pun. Kelas itu hanya bisa disetujui lewat kanal owner interaktif yang merender preview (TUI/web/gateway owner). Test F5: `TestMCPServeCannotSelfApprove`.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0 (dalam budget modul F4/F5). Perilaku: role `user` tanpa backend container tidak punya shell.

**Konsekuensi.** (+) `deny` berarti deny; identitas lain tidak bisa membaca workspace owner; P26 tetap butuh manusia. (−) Deployment gateway multi-user butuh backend container untuk shell.

## ADR-077 — Acceptance F1 lengkap, edge case per fase, gate inkremental, metode ukur kinerja (AUD-025..AUD-030, AUD-080)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup AUD-025, AUD-026, AUD-027, AUD-028, AUD-029, AUD-030, dan AUD-080 (Aturan penerapan ADR-059 kini mencakup ROADMAP F1). Menutup TS-12.

*(Amandemen 2026-10-18: butir 3 (semantik `PHASE`) dan butir 2 (EC-TERM-001) dikoreksi ADR-080; butir 6 (target `patch`) dikoreksi ADR-082. Aturan penerapan juga mencakup EXEC-AND-CODING §A.9/§A.10 (commit `11626de`) dan SCORECARD §1.3 (commit `96e03a1`) — NEW-49.)*

*(Amandemen 2026-10-21: butir 4 dilengkapi ADR-127 — di runner CI bersama, CT-09 (edit + `fsync`) ditegakkan relatif terhadap referensi tulis-durabel yang diukur di runner yang sama; nilai absolut CT-09 tetap diverifikasi di Q14.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): ROADMAP §"Regression suite dari EDGE-CASES", F0 Acceptance (rujukan speed baseline), F1 Acceptance; SPEC §16 (baris Edge-case, ambang ×1,5), §17.1, §17.3; CODE-TOOLS §10 (CT-10, ambang ×1,5); SCORECARD §1.2 (Code tools), §3; DECISIONS ADR-059 (penanda). Setiap passage mengutip ADR-077.

**Keputusan.**
1. **Acceptance F1 eksplisit (AUD-025).** Selain butir ROADMAP yang ada: P7 (`TestFallbackVisible` untuk `run` headless), P26 bagian approval (`TestIrreversibleNeedsApproval`, `TestShellIrreversibleAlwaysAsk`, `TestAgentBypassFlagsAlwaysAsk`), P29 subset ADR-068, `TestTaintForcesApproval`, `TestRedaction`, `TestStreamRedactSplit`, suite sandbox CODE-TOOLS §8 termasuk `TestSymlinkEscapeRace` (CT-26), `TestClassifyCorpus` (EXC-13), `TestDestructiveAlwaysAsk` (EXC-14), test ADR-069, P23 = semua CT kecuali CT-08, CT-12, CT-13, CT-24, CT-28 (butuh `codeintel`/`codeindex`, F5), P24 = `TestNoOrphans` (200 iterasi), `TestInteractiveCorpus` (40 fixture × pipe/pty), `BenchmarkShellSpawn*`, `TestCancelConfirmed`, `TestCancelConfirmBeforeRedispatch` (REL-13 di F3; Windows di matrix), dan `TestSpecDocs` (`TestSpecNoPendingAmendments`, `TestCodeToolsIDsComplete`) + `TestCLIDocsMatchHelp` (butir 3) + kasus meta-test gate dokumen. Frasa "suite 200 skenario" diganti nama test dan jumlah kasus di atas.
2. **Edge case per fase (AUD-026, gaya ADR-061).** Satu fase per entri (fase pemilik komponen yang dibutuhkan untuk mereproduksi; entri yang butuh komponen F2+ bukan F1):

   | Kategori (Σ) | F1 | F2 | F3 | F4 | F5 | F6 | F7 |
   |---|---|---|---|---|---|---|---|
   | EC-PROV (30) | 001, 004..007, 009..013, 015, 016, 025, 028..030 (16) | 008 | — | — | 002, 003, 014, 017..023, 026, 027 (12) | 024 | — |
   | EC-TCALL (12) | 001..003, 005..009, 012 (9) | 010 | — | — | 004, 011 | — | — |
   | EC-FIO (19) | 001, 002, 004..006, 008, 009, 011..017, 019 (15) | — | 007, 018 | — | 003, 010 | — | — |
   | EC-TERM (10) | 001, 002, 006, 007 (4) | — | — | 008..010 | 003 | — | 004, 005 |
   | EC-SEC (8) | 001, 005, 007, 008 (4) | — | — | — | 002..004 | — | 006 |

   Alasan kunci: EC-PROV-008 butuh compaction (F2); 014 OAuth, 022 `key_cmd`, 023 alias lokal, 027 aturan `cache_control` per provider = registry/transport F5; 024 routing gambar = media F6. EC-TCALL-010 butuh `tool_search` (F2). EC-FIO-007 (pembaca config) dan 018 (NDJSON, pemakai pertama ACP) = F3. EC-TERM-004/005 butuh multiplex profil (F7); 008..010 = heal/GAP-REL-12 (F4); proses background EC-TERM-001/007 diuji lewat runner internal tanpa tool `process_*`. EC-SEC-002..004 butuh OAuth/credential pool (F5), 006 webhook generik (F7). Label ROADMAP lama ("EC-SEC F3 import, F4 gateway") diganti tabel ini.
3. **Gate inkremental (AUD-027).** `edgecases.json` mendukung status `pending:<Fx>`; `testdata/cli_surface.txt` memuat kolom fase per bentuk perintah. Fase berjalan dibaca dari file `PHASE` di root repo (dibuat di Deliverable F1 #0, isi `F1`). Gate (`TestEdgeCaseCatalogSync`, `TestCLIDocsMatchHelp`) gagal bila item dengan fase ≤ `PHASE` belum `covered`, atau bila masih ada `pending` saat `PHASE` = `F9`. Item `pending` tidak menghasilkan subtest (tanpa `t.Skip`, REL-15).
4. **Ambang ×1,5 (AUD-028).** ×1,5 hanya untuk metrik **waktu absolut** di runner CI bersama. Rasio terhadap referensi yang diukur di runner yang sama (`rg`, hermes, main) dipakai apa adanya. Acceptance F1: waktu absolut (spawn p95 < 15 ms, P17) diukur di mesin referensi Q14 (CI menegakkan ×1,5 di PR); rasio (grep ≤ 1,2× `rg`, regex ≤ 1,5×) diukur di runner CI apa adanya dan di Q14.
5. **Speed parity (AUD-029).** Gate F1 = `BenchmarkSpeedParity/<ID>` (testing.B di `bench/speed`, dibuat di F1) untuk setiap baris FEATURE-PARITY berfase F1 (ADR-064), dibandingkan dengan `bench/speed/results/hermes-868b9f244f.json`. Sampel CLI ≥ 20 run per sisi; baseline hermes untuk baris F1 diukur ulang dengan 20 run sebelum acceptance F1. ROADMAP F0 merujuk produsen baseline yang ada (`cd bench && go run ./speed/cmd/speed`).
6. **Code tools vs hermes (AUD-030, TS-12).** Satu target = **≥ 10× lebih cepat p50 per kategori** dari tool hermes (selaras P28 file tools dan SCORECARD); CT-10 dikoreksi dari 2×. Jumlah operasi = 70 (50 `search_files`, 10 `read_file`, 10 `patch`). Alat: `cd bench && go run ./codetools/cmd/codetools`. Referensi hermes = setelan artefak `bench/codetools/results/f0-baseline.json` (`HERMES_HOME` bersih, tanpa ripgrep dari package manager → backend fallback); golden CT-11 berlaku untuk setelan itu, dan perubahan commit/setelan hermes = ukur ulang.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Target CT-10 naik dari 2× ke 10× (sama dengan P28 yang sudah berlaku). Acceptance F1 bertambah daftar test eksplisit; tidak ada angka invariant yang dilonggarkan.

**Konsekuensi.** (+) Acceptance F1 dapat diperiksa per nama test; gate CI tidak merah sejak F1 dan tidak lulus kosong. (−) File `PHASE` harus dinaikkan di awal setiap fase.

## ADR-078 — Errata backlog MINOR AUDIT-F0 (AUD-043..AUD-083)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; pola ADR-047 butir 2). Menetapkan pemilik dan fase untuk 41 MINOR [`AUDIT-F0.md`](AUDIT-F0.md). Tidak ada teks yang diubah oleh ADR ini; setiap item ditutup lewat satu PR errata yang merujuk ADR ini, **paling lambat sebelum acceptance fase pemiliknya**. Item tanpa penutup memblok acceptance fase itu. AUD-080 sudah ditutup ADR-077.

**Aturan penerapan.** Tidak ada (backlog). Status per item dilacak di [`AUDIT-F0-RESOLUTION.md`](AUDIT-F0-RESOLUTION.md).

| ID | Inti | Fase pemilik |
|---|---|---|
| AUD-043 | A.3 langkah 7 merujuk rumus P5/COST-08 dan mode degradasi PROV-02 | F1 (sebelum `exec`) |
| AUD-044 | P3 merujuk §4.4 sebagai daftar keyword tunggal | F1 (sebelum `schema`) |
| AUD-045 | P6 worst case 9 + 1 + 1 + 1; retry `empty_response`/`context_length`/carrier diizinkan eksplisit di luar netx (forbidigo) | F1 (sebelum `provider`) |
| AUD-046 | `no_progress`: penghitung direset per `user_message` | F1 (sebelum `loop`) |
| AUD-047 | Config eval (`max_wall_s = 1800`, cap per turn) di artefak eval; definisi cap input vs `cached_input_tokens` | F1 (sebelum smoke F1) |
| AUD-048 | Sisa risiko orphan TRUST-MODEL berlaku juga untuk shell Linux (atau sub-cgroup) | F1 |
| AUD-049 | (a) tertutup ADR-069; (b) frasa kunci per aturan di testdata `TestPromptLint`; (c) `ResponseFormat *schema.JSON` di `provider.Request` | F1 |
| AUD-050 | §9.6 scope token `read\|send\|approve` | F5 |
| AUD-051 | SCORECARD CPU idle mengikuti §17.1 (web + 3 adapter + cron) | F4 |
| AUD-052 | §13.2 merujuk string CSP SEC-06 sebagai sumber tunggal | F8 |
| AUD-053 | Satu angka cap eval scorecard terjadwal (INF-06 vs §18.5) | F4 |
| AUD-054 | P11 → "default per fitur menurut tabel §9.11"; satuan mingguan insights | F8 |
| AUD-055 | Nama test P26 disatukan dengan §22 | F1 |
| AUD-056 | spec-guard: label `spec-typo` + 1 approver; laporan via `docs_exclusions` | F1 (Deliverable #0) |
| AUD-057 | CROSS-CUTTING §7 diselaraskan dengan §16 (ACP + headless di 6 runner) | F1 (Deliverable #0) |
| AUD-058 | Default `exec.auto_allow_verify` di §12; EXEC A.8 "tanya + preview" | F1 (sebelum `exec`) |
| AUD-059 | E-58 += `codetools`, `exec` (≥ 80%) | F1 (Deliverable #0) |
| AUD-060 | ADR-057 butir 1 dan SELF-HEALING §3.5 → `SET event_id = ?, payload = NULL WHERE event_id IS NULL` | F4 |
| AUD-061 | Penanda status ADR-046 ("dikoreksi ADR-052") dan *(Historis …)* ADR-036..046 | F1 |
| AUD-062 | Satu rumusan syarat freeze (= SPEC §20) | F1 |
| AUD-063 | Baris `loc.budget` per modul §3.2.4 saat modul dibuat; GW-03 → `internal/gateway/**` | F4 |
| AUD-064 | Satu definisi `TestBudgetTableSum` di P21 | F1 (Deliverable #0) |
| AUD-065 | Errata ROADMAP/SPEC/SCORECARD pasca ADR-047/048 (CI skeleton, migrasi 0001–0008, status SPEC:3, path `bench/evals/`) | F1 |
| AUD-066 | size-check gagal bila minimal == full setelah modul non-core ada; composition root F1 mengimpor paket core; kasus meta `size-minimal` | F1 |
| AUD-067 | Exclusion golangci dijangkar (`^cmd/`, `^internal/gateway/[^/]+/`, `^internal/modules/`) | F1 (Deliverable #0) |
| AUD-068 | depguard edit-distance (mis. `github.com/agnivade/levenshtein`) untuk `internal/{loop,tools,modules}/**` | F1 (sebelum `loop`/`tools`) |
| AUD-069 | `deps.mpl` + test vs `go-licenses report` | F1 (Deliverable #0) |
| AUD-070 | Action dipin SHA, `persist-credentials: false`, Dependabot github-actions | F1 (Deliverable #0) |
| AUD-071 | `on.schedule` harian untuk job vuln | F1 (Deliverable #0) |
| AUD-072 | Meta-test gate `bench-budget` dan `depguard` | F1 (Deliverable #0) |
| AUD-073 | loc-check gagal untuk `internal/*` tak terpetakan; pengecualian generated hanya allowlist path | F1 (Deliverable #0) |
| AUD-074 | Errata infra: E-46 `go mod tidy -diff` + modul `tools`/`bench`; tool dipin vs ENGINEERING; jumlah linter; header toolchain F0-REPORT; nama job `size-check` | F1 (Deliverable #0) |
| AUD-075 | CI `bench-budget` → `cd bench && go test ./...`; skip → fail bila `CI=true`; `referenceAnswer` dari fixture; verifikasi E1 sebagai test | F1 (sebelum smoke F1) |
| AUD-076 | TS-07: satuan "o200k atas JSON request" (sebagian sudah ADR-066); profil h2h dipin | F1 (sebelum smoke F1) |
| AUD-077 | Seleksi `h2h-smoke/v1` = 10 ID konstan; verifikasi MANIFEST sebelum run | F1 (sebelum smoke F1) |
| AUD-078 | Grader h2h: jawaban final saja + tolak nilai umpan; file berubah ⊆ {solution.*} | F1 (sebelum smoke F1) |
| AUD-079 | Test regenerasi C3 vs `corpora.lock`; cek C1 bersih | F1 (sebelum acceptance codetools) |
| AUD-081 | SCORECARD §2.5 baris smoke F1 (ADR-062) | F1 |
| AUD-082 | ROADMAP:88 merujuk perintah baseline yang ada (substansi ADR-077 butir 5) | F1 |
| AUD-083 | `.gitignore` + run TS-11 ke direktori terpisah | F1 (Deliverable #0) |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0.

**Konsekuensi.** (+) Semua MINOR punya pemilik dan tenggat yang ditegakkan acceptance fase. (−) Backlog errata F1 cukup panjang (33 item).

## ADR-079 — Shell tidak pernah di-auto-approve oleh classifier bawaan; auto hanya lewat `shell_allow` argv-persis (AUD-001, AUD-008, AUD-010, NEW-01, NEW-03, NEW-04, NEW-09)

**Status:** Accepted 2026-10-18 (keputusan direktur proyek). **Menggantikan** ADR-069 butir 2 (allowlist `read_only` + hardening git), butir 3 bagian "auto" (operand tetap dipakai untuk label/`denied_path` best-effort), butir 4 (`tainted_session` untuk auto), butir 6 (`shell_allow` prefix), tabel 42 kasus, dan jalur auto verify di ADR-069/SPEC §14.1. ADR-069 butir 1, 5 (kelas `irreversible`/`privileged`, P26), 7 (workspace trust), 8 (baca di luar root lewat tool file), dan 9 (cakupan boundary, dikoreksi di sini) tetap berlaku. Menutup sisa AUD-001/008/010 (`AUDIT-F0-REVERIFY.md`), NEW-01, NEW-03, NEW-04, NEW-09; ikut menutup NEW-06 (verify), NEW-07 (definisi vulnerability), NEW-22, NEW-23, NEW-24, NEW-25, NEW-26, NEW-27.

*(Amandemen 2026-10-18: `allow_session` tidak pernah berlaku untuk shell, definisi `outside_read`, dan jalur execve untuk `shell_allow` — ADR-088; `execute_code` bahasa shell = model ini — ADR-089.)*

*(Amandemen bertanggal 2026-10-18 saat penerapan: (a) kunci `shell_allow` tetap di section `[approval]` contoh config SPEC §12 (bukan `[exec]`; tanpa rename kunci); (b) daftar label butir 2 mencakup atribut `non_mutating` (best-effort, hanya membatasi di plan mode butir 11); (c) aturan stable tier "baca lewat tool kode" butir 1 diwujudkan sebagai perluasan aturan 5 draf §6.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P26 (rujukan), §4.3 (langkah 5/7), §5.1 (baris `shell`), §10 (`run --unsafe-auto-shell`), §11 (komentar `approvals.decision` `auto_class_readonly` dicadangkan), §12 (`[exec]` `shell_allow = []`, `exec.auto_allow_verify` dihapus, `allow_destructive`/`destructive_allow` dicadangkan), §14.1, §14.2, §14.5, §18.3 (plan mode), §22.1 GAP-SEC-28, GAP-UX-02; EXEC-AND-CODING §A.8, §A.10, §B.2/§B.5 (verify), §D (EXC-13/EXC-14); CODE-TOOLS §8; TRUST-MODEL §1–§3; CROSS-CUTTING §1; ROADMAP F1 Acceptance; SCORECARD (catatan UX). Setiap passage mengutip ADR-079.

**Konteks.** Dua putaran audit menemukan bypass baru di setiap versi allowlist `read_only` (`sort --files0-from`, rekursi lewat symlink, `git blame --contents`, `~user`/brace, `printf -v PATH`, filter `clean` git, `cd` sebelum operand relatif). Classifier shell berbasis AST tidak dapat dibuktikan lengkap terhadap semantik bash dan ratusan program; menambal per kasus tidak konvergen.

**Keputusan.**
1. **Tidak ada auto-approve shell berbasis classifier, di mode apa pun.** Baris "`shell` `read_only` → auto" dan allowlist `read_only` bawaan dihapus. Membaca/mencari file dilakukan lewat tool kode tersandbox (`read_file`, `grep`, `glob`) yang menegakkan root + `deny_paths` lewat `openat2(RESOLVE_BENEATH)`/verifikasi fd (§14.2). Stable tier menyatakan aturan ini kepada model.
2. **Classifier tetap ada hanya untuk label risiko dan preview**: `mutating`/`destructive`/`unknown`/`irreversible`/`monetary`/`delegated_agent_bypass`, flag `privileged`/`network`, dan `denied_path` best-effort (operand yang ter-resolve statis ke `deny_paths` → ditolak tanpa bisa di-approve; ketiadaan label ini **tidak** menjamin apa pun). Label menentukan bentuk prompt (preview, larangan `allow_session` untuk P26) dan penolakan headless, tidak pernah menghasilkan auto.
3. **`shell_allow` = satu-satunya jalur auto.** Default `shell_allow = []`. Entri = daftar token argv **persis** (contoh `["go", "test", "./..."]`); perintah cocok hanya bila string perintah di-parse menjadi **tepat satu** simple command tanpa metakarakter/ekspansi apa pun (tanpa `|`, `;`, `&`, `&&`, `||`, subshell, `$`, backtick, `~`, glob `*?[`, brace, redirect, assignment `VAR=`, heredoc, parameter `stdin`) dan token-token-nya sama persis dengan satu entri. argv0 entri harus nama tanpa path yang di-resolve lewat `PATH` sesi ke binary yang sama saat sesi dimulai. `cwd` dilacak: auto hanya bila `cwd` call (non-persisten) atau `cwd` hidup sesi persisten (dibaca runner lewat sentinel `pwd -P` setiap perintah) berada di dalam root, dan sesi persisten hanya pernah menjalankan perintah yang cocok `shell_allow` (perintah lain yang di-approve manual membuat sesi itu permanen tidak memenuhi syarat). Syarat tambahan: workspace trusted (ADR-069 butir 7), turn tidak tainted (ADR-070), kelas bukan `destructive`/`unknown`/P26 dan tanpa flag `privileged`, dan tidak ada file yang ditulis tanpa approval eksplisit di **sesi** ini sejak approval shell manual terakhir (NEW-27).
4. **Kebijakan per mode untuk `shell`:** `ask`, `auto-edit`, dan `auto` interaktif → **tanya** (preview sesuai label) kecuali cocok butir 3. Headless tanpa approver → `denied` kecuali cocok butir 3, atau headless `--approval auto` **dengan** flag eksplisit `damnbot run --unsafe-auto-shell`: perintah berkelas `mutating` (dengan/tanpa `network`) berjalan tanpa approval, hanya bila workspace trusted dan turn tidak tainted; `destructive`, `unknown`, P26, `privileged`, dan `denied_path` tetap `denied`. Help flag dan dokumentasi menyatakan risikonya: "runs arbitrary shell commands chosen by the model without confirmation; use only in a disposable container".
5. **Verify (NEW-06).** `exec.auto_allow_verify` dihapus. Verify runtime (B.2/B.5, `verify_on_stop = run`) mengikuti butir 3–4: auto hanya bila perintah verify persis ada di `shell_allow`; selain itu interaktif → tanya, headless → tidak dijalankan dan hasil `unverified` + `evidence_gap`.
6. **Taint (NEW-09).** Selain sumber ADR-070, **setiap** output shell di workspace untrusted dibungkus `<untrusted source="workspace">` dan men-taint turn; output shell berflag `network` tetap men-taint di workspace trusted.
7. **Tool kode (NEW-22, NEW-23).** Symlink yang ter-resolve keluar root diperlakukan sama dengan path di luar root (verdict `outside_root` → tanya; headless `outside_workspace`), bukan error. Default `deny_paths` += `/proc/*/task/*/environ`.
8. **Boundary dan vulnerability (NEW-07, koreksi ADR-069 butir 9).** Sandbox path = boundary untuk tool kode. Shell **bukan** boundary path; kontrol shell = approval. Vulnerability = (a) perintah shell yang berjalan tanpa approval eksplisit, kecuali cocok butir 3 atau jalur `--unsafe-auto-shell` butir 4 yang terdokumentasi; (b) tool kode yang mengakses luar root/`deny_paths` tanpa verdict yang benar; (c) egress tool built-in yang lolos DLP `netx`. Efek perintah yang sudah di-approve user (termasuk filter git, rekursi symlink) = sisa risiko, dicatat di TRUST-MODEL §3 beserta TOCTOU antara preview dan eksekusi.
9. **Kunci auto non-shell (keputusan direktur).** `exec.allow_destructive`, `exec.destructive_allow`, dan `smart_approval` **tidak berlaku untuk `shell`**: untuk shell satu-satunya jalur auto adalah kecocokan persis `shell_allow`, dan perintah shell berlabel `destructive`, `irreversible`, `monetary`, atau `delegated_agent_bypass` tidak pernah auto walau cocok (selalu tanya + preview; headless → `denied`). Cakupan non-shell kunci itu = operasi destruktif tool file (mis. hapus/timpa file yang tidak dilacak git); cakupan ini **belum didefinisikan** di dokumen mana pun, sehingga kunci tersebut berstatus "dicadangkan untuk desain F1" dengan default konservatif **tanpa efek** sampai ADR F1 mendefinisikannya.
10. **Nilai `approvals.decision = 'auto_class_readonly'`.** Tetap di DDL (tanpa churn migrasi), berstatus dicadangkan dan **tidak pernah dihasilkan**; test F1 `TestNoAutoClassReadonlyDecision` memastikan nilai itu tidak pernah ditulis.
11. **Plan mode (GAP-UX-02, §18.3).** Selama plan mode, `shell` hanya diizinkan bila perintah cocok persis `shell_allow` **dan** classifier memberi label `non_mutating` (label best-effort yang hanya dipakai untuk **membatasi**, tidak pernah untuk mengizinkan di luar `shell_allow`); perintah lain ditolak di plan mode. Test `TestPlanModeShell`.

**Test spec F1 (`internal/exec`, menggantikan tabel ADR-069).** `TestShellApprovalModel` (tabel di bawah, semua baris), `TestShellAllowExactMatch` (≥ 60 kasus: setiap metakarakter/ekspansi/redirect/assignment/pipeline × entri cocok → tidak auto), `TestShellAllowCwdTracked`, `TestShellNoClassifierAuto` (≥ 300 perintah korpus EXC-13, mode `ask`/`auto-edit`/`auto`, `shell_allow` kosong → 0 dieksekusi tanpa approval), `TestUnsafeAutoShellFlag`, `TestVerifyNeedsShellAllow`, `TestShellOutputUntrustedWorkspace`, `TestCodeToolsSymlinkOutsideRoot`. Konteks default: `shell_allow = [["go","test","./..."]]`, workspace trusted, turn bersih, tidak ada tulis auto di sesi.

| # | Perintah / kondisi | Label | `ask` | `auto-edit` | `auto` | Headless | Headless `--unsafe-auto-shell` |
|---|---|---|---|---|---|---|---|
| 1 | `ls -la src` | `mutating` | tanya | tanya | tanya | `denied` | auto |
| 2 | `cat README.md \| wc -l` | `mutating` | tanya | tanya | tanya | `denied` | auto |
| 3 | `git status` | `mutating` | tanya | tanya | tanya | `denied` | auto |
| 4 | `cat ~/.ssh/id_rsa` | `denied_path` | error | error | error | error | error |
| 5 | `sort --files0-from=- <<< ~/.ssh/id_rsa` | `unknown` | tanya + preview | tanya + preview | tanya + preview | `denied` | `denied` |
| 6 | `grep -R token .` (symlink keluar root di bawah `.`) | `mutating` | tanya | tanya | tanya | `denied` | auto (sisa risiko terdokumentasi) |
| 7 | `printf -v PATH %s ./bin` (sesi persisten) | `mutating` | tanya | tanya | tanya | `denied` | auto; sesi tidak lagi memenuhi syarat `shell_allow` |
| 8 | `cd ~ && cat .ssh/id_rsa` | `unknown` (cwd tak terlacak statis) | tanya + preview | tanya + preview | tanya + preview | `denied` | `denied` |
| 9 | `go test ./...` | cocok `shell_allow` | auto | auto | auto | auto | auto |
| 10 | `go test ./... -run X` | `mutating` (tidak persis) | tanya | tanya | tanya | `denied` | auto |
| 11 | `go test ./... \| tee out` | `mutating` (pipeline) | tanya | tanya | tanya | `denied` | auto |
| 12 | `GOFLAGS=-x go test ./...` | `mutating` (assignment) | tanya | tanya | tanya | `denied` | auto |
| 13 | `go test ./...` di workspace untrusted | `mutating` | tanya | tanya | tanya | `denied` | `denied` |
| 14 | `go test ./...` saat turn tainted | `mutating` | tanya | tanya | tanya | `denied` | `denied` |
| 15 | `go test ./...` setelah `edit_file` auto di sesi ini (mode `auto-edit`) | `mutating` | tanya | tanya | tanya | `denied` | auto |
| 16 | `./go test ./...` | `mutating` (argv0 relatif) | tanya | tanya | tanya | `denied` | auto |
| 17 | `go test ./...` di sesi persisten yang sebelumnya menjalankan perintah manual | `mutating` | tanya | tanya | tanya | `denied` | auto |
| 18 | `go test ./...` dengan `cwd` sesi di luar root | `mutating` | tanya | tanya | tanya | `denied` | `denied` |
| 19 | `echo 'rm -rf ~' \| bash` | `unknown` | tanya + preview | tanya + preview | tanya + preview | `denied` | `denied` |
| 20 | `rm -rf ~` | `destructive` | tanya + preview | tanya + preview | tanya + preview | `denied` | `denied` |
| 21 | `git push origin main` | `irreversible` | tanya + preview (tanpa `allow_session`) | sama | sama | `denied` | `denied` |
| 22 | `npm publish` | `irreversible` | tanya + preview | sama | sama | `denied` | `denied` |
| 23 | `curl -X POST https://api.example.com -d @a.json` | `irreversible` | tanya + preview | sama | sama | `denied` | `denied` |
| 24 | `curl -s https://example.com` | `mutating` + `network` | tanya | tanya | tanya | `denied` | auto (output men-taint) |
| 25 | `sudo apt install jq` | `irreversible` + `privileged` | tanya + preview | sama | sama | `denied` | `denied` |
| 26 | `sudo ls /root` | `mutating` + `privileged` | tanya per perintah | sama | sama | `denied` | `denied` |
| 27 | `claude --dangerously-skip-permissions -p x` | `delegated_agent_bypass` | tanya + preview | sama | sama | `denied` | `denied` |
| 28 | `npm install` | `mutating` + `network` | tanya | tanya | tanya | `denied` | auto (output men-taint) |
| 29 | `ssh host 'ls'` | `irreversible` | tanya + preview | sama | sama | `denied` | `denied` |
| 30 | `git diff` dengan `filter.x.clean` di `.git/config` | `mutating` | tanya | tanya | tanya | `denied` | auto (sisa risiko, workspace trusted) |
| 31 | output `cat x` (di-approve) di workspace untrusted | — | output `<untrusted source="workspace">`, turn tainted | sama | sama | — | — |
| 32 | `read_file ~/.ssh/id_rsa` | `denied_path` (tool kode) | error | error | error | error | error |
| 33 | `read_file ../notes.txt` (luar root) | `outside_root` | tanya | tanya | tanya | `outside_workspace` | `outside_workspace` |
| 34 | `read_file link` (symlink di root → luar root) | `outside_root` | tanya | tanya | tanya | `outside_workspace` | `outside_workspace` |
| 35 | `read_file /proc/self/task/1/environ` | `denied_path` | error | error | error | error | error |
| 36 | verify `go vet ./...` (tidak ada di `shell_allow`) | `mutating` | tanya | tanya | tanya | tidak dijalankan → `unverified` | auto |

**Biaya UX vs parity hermes (dicatat jujur).** Hermes menjalankan perintah non-berbahaya tanpa konfirmasi di mode non-interaktif dan punya mode yolo interaktif; Damn-Bot kini **bertanya untuk setiap perintah shell** yang tidak persis ada di `shell_allow`, termasuk `ls`/`git status` dan termasuk mode `auto` interaktif. Akibatnya: lebih banyak prompt per task coding (setiap verify/build/test baru, setiap inspeksi git) dan task headless tanpa `shell_allow` atau `--unsafe-auto-shell` berhenti di `denied`. Mitigasi: tool kode untuk semua baca/cari; `allow_session` untuk subject persis (bukan P26); konfigurasi `shell_allow` per proyek; preview ringkas. Baris parity EXEC-013 (mode approval) dan EXEC-015 (allowlist) tetap dipenuhi secara fungsi, tetapi semantik `auto` interaktif lebih ketat dari yolo hermes; padanan yolo hanya headless + `--unsafe-auto-shell`. SCORECARD tidak punya metrik jumlah prompt; dampak ini diterima sebagai harga keamanan dan diamati di smoke F1/RC F9.

**Dampak terukur (SPEC §20).** Token: aturan stable "baca lewat tool kode" ≤ 25 tok di dalam cap 1.200. Latensi 0. LOC: classifier mengecil (tanpa allowlist/resolusi auto); budget `exec/classify` ADR-081. Binary/dependency 0. CLI: +1 flag `run --unsafe-auto-shell` (§10; jumlah perintah top-level tetap). Default config: `shell_allow` dari 8 entri → `[]`; `exec.auto_allow_verify` dihapus.

**Konsekuensi.** (+) Tidak ada lagi permukaan bypass classifier untuk eksekusi tanpa approval; boundary baca = satu implementasi tersandbox. (−) Lebih banyak prompt; pengguna yang ingin otomatisasi harus mengisi `shell_allow` atau memakai headless + container.

## ADR-080 — Semantik `PHASE` gate inkremental, sinkronisasi katalog, EC-STATE/EC-WIN per ID (NEW-02, AUD-027, NEW-19, NEW-30)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengoreksi ADR-077 butir 2 (EC-TERM-001) dan butir 3 (semantik `PHASE`). Menutup NEW-02 (BLOCKER), sisa AUD-027, NEW-19, NEW-30.

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §10 (definisi `TestCLIDocsMatchHelp`), §16 (baris Edge-case); ROADMAP §"Regression suite", F1 #0, F1/F2/F4/F5/F7 Acceptance (EC); DECISIONS ADR-077 (penanda). Setiap passage mengutip ADR-080.

**Keputusan.**
1. **Gate PR** (`TestEdgeCaseCatalogSync`, `TestCLIDocsMatchHelp`, dan gate inkremental lain): item dengan fase **<** `PHASE` wajib `covered` (subtest ada dan lulus); item dengan fase = `PHASE` boleh `pending:<PHASE>` atau `covered` (bila subtest ada, wajib lulus); item dengan fase > `PHASE` wajib `pending`. Dengan `PHASE = F1` di Deliverable F1 #0, gate hijau sebelum paket F1 ditulis.
2. **Job acceptance fase** (`make phase-acceptance`, dijalankan saat menaikkan `PHASE` dari Fx ke Fx+1, dan di PR yang mengubah file `PHASE`): semua item fase ≤ Fx wajib `covered`; PR penaikan `PHASE` gagal bila tidak. Di `PHASE = F9` tidak boleh ada `pending`.
3. **Sinkronisasi katalog:** himpunan ID katalog = ID subtest ∪ ID berstatus `pending`; keduanya saling lepas. `n/a` beralasan dihitung `covered`.
4. **EC per ID:** EC-TERM-001 → F5 (butuh proses background, ADR-072); EC-TERM F1 = 002, 006, 007 (3), F4 = 008..010, F5 = 001, 003, F7 = 004, 005. EC-STATE (16): F2 = 001, 002, 003, 005..011, 013..016 (14); F4 = 012; F7 = 004. EC-WIN (17): F1 = 001, 009, 010, 012, 013, 014, 016, 017 (8); F2 = 002, 005, 011; F4 = 003, 004, 006; F5 = 007, 008, 015. Entri EC-WIN dijalankan di runner Windows matrix CI (F1 #0) dan masuk acceptance fasenya; "penutupan F9" hanya untuk sisa yang belum covered karena bug, bukan fase default.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Acceptance F1 EC-TERM 4 → 3; F5 +1.

**Konsekuensi.** (+) Gate inkremental dapat hijau sejak F1 #0 dan tetap tidak lulus kosong saat fase ditutup. (−) Satu job tambahan saat penaikan fase.

## ADR-081 — Exec, redaksi, dan budget `exec`: secret ref terbatas, redaksi stream terukur, cakupan GAP-SEC-28 (NEW-05, NEW-08, NEW-20, sisa AUD-014, AUD-017)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengamandemen ADR-071 butir 1 dan 3. Menutup NEW-05, NEW-08, NEW-20, sisa AUD-014 dan AUD-017.

*(Amandemen 2026-10-18: butir 1 dilengkapi ADR-088 — secret ref hanya lewat jalur execve langsung, `TestSecretRefScope`.)*

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P17, §3.2.4 (catatan exec), §12, §14.3, §22.1 GAP-SEC-28; EXEC-AND-CODING §A.6, §A.8; CROSS-CUTTING §1.2; TRUST-MODEL §3; ENGINEERING §2.4 + `loc.budget`. Setiap passage mengutip ADR-081.

**Keputusan.**
1. **Secret ref `exec.env` (NEW-05).** Entri bernilai `keychain:<id>` wajib punya cakupan `commands = ["gh", …]` (nama argv0). Var itu hanya disuntikkan ke proses yang dijalankan **langsung** (tanpa shell interpreter) sebagai simple command dengan argv0 ∈ `commands`; tidak pernah ke sesi shell, `bash -c`, atau proses lain. Perintah yang mereferensikan var secret (`$X`, `${X…}`) → label `unknown` (preview menandai "references secret"). Nilai tetap sumber redaksi §14.3. Sisa risiko (transformasi nilai oleh program yang di-approve user) dicatat di TRUST-MODEL §3; klaim "tidak pernah masuk konteks model" diganti "tidak pernah disuntikkan ke konteks model oleh Damn-Bot".
2. **Redaksi stream (NEW-08, AUD-014).** Penahanan pola userinfo baru dimulai setelah literal `://` terlihat; pola Telegram setelah ≥ 8 digit diikuti `:`; prefix nilai secret yang dikenal hanya ditahan bila kecocokan parsial ≥ 4 char; pola berprefix literal (`sk-`, `ghp_`, `AKIA`, `-----BEGIN`, …) ditahan sejak prefix literal lengkap. Klaim "0 latensi tambahan" diganti target terukur: penyaring menambah ≤ 1 ms p95 pada first token untuk korpus teks natural (en/id, 200 respons) — `TestFirstTokenLatency/redact` dengan penyaring aktif, bagian dari P17 F1.
3. **GAP-SEC-28 (AUD-017).** Teks requirement dibatasi sesuai TRUST-MODEL §1 (ADR-079 butir 8): DLP boundary hanya egress built-in `netx`; sandbox path boundary hanya tool kode; aktor "konten repo/workspace" ditambahkan.
4. **Budget LOC `exec` (NEW-20).** `loc.budget`: `exec` 3.000 → **3.500** (runner PTY/pipe + Windows, env allowlist, registry `still_running`) dan baris baru `exec/classify` **1.500** (`internal/exec/classify`: parser label risiko + `shell_allow` argv-persis). Σ `exec` 5.000 ≤ SPEC §3.2.4 exec 7.000 (sisa 2.000 untuk background `process_*` F5). Total tabel ENGINEERING §2.4 31.450 → 33.450 ≤ 47.000 (SPEC §3.2.4 tidak berubah).

**Dampak terukur (SPEC §20).** Token 0. Latensi: ≤ 1 ms p95 first token (target baru, terukur). LOC: `loc.budget` +2.000 dari headroom 47.000 − 31.450. Binary/dependency 0.

**Konsekuensi.** (+) Secret subprocess tidak bisa dibaca lewat shell; klaim P17 terukur. (−) Skill/alat yang butuh secret harus dijalankan langsung, bukan lewat skrip shell.

## ADR-082 — Kontrak loop/assemble/codetools: `content_filter`, enum status tunggal, sub-cap `<runtime>`, schema `shell` per fase, target `patch` setara durabilitas (sisa AUD-018, AUD-020, AUD-023, NEW-10, NEW-11, NEW-17, NEW-18)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengamandemen ADR-067 butir 2, ADR-072 butir 1/3, ADR-077 butir 6, dan SPEC P28. Menutup sisa AUD-018, AUD-020, AUD-023, serta NEW-10, NEW-11, NEW-17, NEW-18.

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC P28, §4.2, §4.3, §5.1, §6, §22.1 GAP-COST-07, GAP-REL-13; EXEC-AND-CODING §A.2, §A.3, §A.5, §A.10, EXC-10; SELF-HEALING §3.4; CODE-TOOLS §10 (CT-10); SCORECARD §1.2 (Code tools), §3; MITIGATION-MATRIX T-13; ROADMAP F1 Acceptance. Setiap passage mengutip ADR-082.

**Keputusan.**
1. **`content_filter` (AUD-018).** Klasifikasi respons §4.2: `finish_reason=content_filter` → stop `content_filtered`, **apa pun** isinya; tool call di respons itu tidak dieksekusi dan disimpan di event `assistant_message{filtered:true}` bersama teks parsial. Test `TestContentFilterStop` mencakup kombinasi dengan `tool_calls`.
2. **Satu enum status (AUD-020).** SPEC §4.3 memuat dua enum tertutup, `TestToolResultStatusClosed`: `ExecStatus` = `exited`, `signaled`, `timeout`, `idle_timeout`, `waiting_for_input`, `cancelled_confirmed`, `still_running`, `spawn_failed` (§5.1, A.3, A.10 memakai daftar ini persis; SIGTSTP = `signaled` dengan `signal:"SIGTSTP"`); `RecoveryStatus` (`by:"recovery"`, SELF-HEALING §3.4) = `interrupted`, `applied_verified`, `not_applied`, `conflict`, `outcome_unknown`. Nama test REL-13 tunggal: `TestACPChildStdinIsolation`.
3. **Sub-cap `<runtime>` (AUD-023, NEW-10).** Sufiks ≤ 40 tok dengan urutan potong tetap: `n/40` 6, peringatan `no_progress` 14, notice proses 20 (notice pertama + `+N more`). `user_changed` diketahui saat turn mulai, jadi masuk slot notice volatile tier (beku per turn), bukan `<runtime>`. Notice proses EXEC §A.5 ≤ 20 tok, maks 1 per request + `+N more (process_list)` (F5). Kunci detektor COST-07 = `(tool, sha256(args kanonik), sha256(result tanpa field runtime))`. EXEC §A.5/EXC-10 dan MITIGATION T-13 diselaraskan.
4. **Acceptance P15 F1 (NEW-11).** Acceptance F1 memuat "P15 subset ADR-067/082": `TestStableTierGolden` (varian), `TestPromptNoUnavailableTools`, `TestVolatileTierCaps`, `TestPrefixStableWithinTurn`, `TestRuntimeSuffixCaps`, `TestShellSchemaVariant`.
5. **Schema `shell` per fase (NEW-18).** Deskripsi dan parameter schema eager dibangkitkan per build: varian F1 tanpa kalimat background dan tanpa parameter `background` (P3 menolak argumen tak dikenal → `invalid_args`, konsisten ADR-072); varian F5 menambahkannya. Golden P15 per varian (`TestShellSchemaVariant`).
6. **Target `patch` setara durabilitas (NEW-17).** Hermes menulis tanpa fsync; Damn-Bot wajib fsync file + rename + fsync dir. P28 dan CT-10 diubah: `search_files` dan `read_file` ≥ 10× p50; `patch`/tulis ≥ **2×** p50 terhadap baseline hermes yang diukur ulang dengan durabilitas setara (driver hermes menambahkan `fsync(file)` + `fsync(dir)` pada path yang sama di dalam jendela waktu), dan ≤ 6 ms p95 absolut di mesin referensi Q14. Baseline durabel diukur di F1 sebelum acceptance codetools. Tidak ada operasi yang boleh lebih lambat dari hermes (P28 tetap).

**Dampak terukur (SPEC §20).** Token: schema `shell` F1 berkurang (tanpa `background`). Target kinerja: `patch` 10× → 2× setara durabilitas (P28 dikoreksi, bukan dilonggarkan untuk operasi baca). LOC/binary/dependency 0.

**Konsekuensi.** (+) Semua target F1 dapat dicapai dan diverifikasi; cache prefix dan detektor repetisi konsisten. (−) Perbandingan `patch` butuh driver hermes durabel tambahan.

## ADR-083 — Baseline hermes per fase di acceptance, sisa AUD-004, test tertunda di acceptance fase (sisa AUD-004, NEW-12, NEW-39, NEW-42, NEW-46)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Melengkapi ADR-065. Menutup sisa AUD-004, NEW-12, NEW-39, NEW-42, NEW-46.

**Aturan penerapan.** Teks diterapkan (2026-10-18): ROADMAP F1–F9 Acceptance; SCORECARD §1.1 (Token skill per turn); F0-REPORT Status #4; `bench/speed/results/hermes-868b9f244f-nollm.json` (`not_measured`) + `bench/speed/nollm_test.go`. Setiap passage mengutip ADR-083.

**Keputusan.**
1. Tabel ADR-065 ditambah: **Token skill per turn** → F2 (butuh index skill Damn-Bot; sisi hermes diukur dari request yang direkam proxy dengan katalog skill bawaan).
2. Artefak nollm memuat `not_measured: [{dimension, phase, reason}]` untuk setiap dimensi ADR-065 berstatus Fx; `TestNoLLMBaselineCoverage` (`bench/speed`) memastikan setiap entri punya fase F1..F9 dan alasan, dan daftar dimensi = tabel ADR-065/083.
3. Acceptance setiap fase F1, F2, F3, F4, F6, F9 memuat bullet: "Baseline hermes untuk dimensi ADR-065/083 berstatus Fx diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`)".
4. Test yang ditunda ke fase lain tercantum di acceptance fasenya: F1 `TestConfigDefaultsMatchSpec` (kunci F1), test ADR-075 (`TestOracleDamnbotToolNames`, `TestUnknownRuleKindFails`, `TestToolCallsFromFinalResponse`, `TestAgentIsolation`, `TestRecordingsNoKey`); F2 `TestModuleLifecycle/needs_attention`; F4 `TestBudgetGuardGatewayUser` (termasuk penolakan pra-turn turn ke-201), `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope`; F5 `TestMCPServeCannotSelfApprove`, `TestExecuteCodeRPCNetworkDeny`; F6 `TestBudgetGuardTools`.
5. Acceptance F1, F4, F5, F8 memuat "backlog ADR-078/ADR-086 berfase Fx tertutup"; ROADMAP F1 #0 mendaftar item backlog berlabel "Deliverable #0".
6. F0-REPORT baris #4 diperbaiki: pengecualian = semua dimensi berstatus Fx di ADR-065/083 (bukan hanya install VM); dimensi LLM diatur ADR-032 (bukan ADR-065).

**Dampak terukur (SPEC §20).** Token/latensi/LOC produksi/binary/dependency 0.

**Konsekuensi.** (+) Kewajiban baseline dan test tertunda dapat diperiksa per fase; artefak terbaca mesin. (−) Acceptance setiap fase lebih panjang.

## ADR-084 — Kanal approval P26, workspace role `user`, asal permintaan `mcp serve` (NEW-16, sisa AUD-041, AUD-042)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengamandemen ADR-076 butir 2 dan 3. Menutup NEW-16 dan sisa AUD-041/AUD-042.

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §9.6, §9.7, §11.4 (komentar kolom), §11.11.10 (tabel otoritas), §12 (`[gateway.users.<id>]`), §14.1, §14.2, §18.2, §22.1 GAP-SEC-14, GAP-SEC-21, GAP-GW-02; TRUST-MODEL §2; ROADMAP F1/F4/F5 Acceptance. Setiap passage mengutip ADR-084.

**Keputusan.**
1. **Kanal P26 (NEW-16).** Kelas P26 hanya bisa disetujui lewat kanal interaktif yang merender preview §18.2: TUI, ACP (`session/request_permission` dengan konten preview), web UI, dan gateway owner (inline keyboard + preview). Bukan `mcp_serve`, bukan headless. Headless produksi tidak punya approver → P26 `denied`. Test F1 memakai approver palsu ber-preview (test double di paket test, tidak pernah di binary) untuk membuktikan alur tanya; subset SEC-14 F1 = sudo → prompt per perintah lewat approver palsu, tanpa approver → `denied`.
2. **Workspace role `user` (AUD-041).** Kolom `gateway_identities.workspace` tetap nullable di DDL; NULL berarti default turunan `<data_dir>/gw-workspaces/<identity_hash>` (dibuat saat pairing), sehingga root tidak pernah kosong. Sumber kebenaran runtime = kolom (bila non-NULL) atau default turunan; kunci config `[gateway.users.<id>] workspace` (§12) menimpa dan ditulis ke kolom saat pairing/startup. Test `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope` masuk §22.1 GAP-GW-02 dan acceptance F4.
3. **Asal `mcp serve` (AUD-042).** Payload JSON `approval_request` (turn_journal, tanpa DDL baru) memuat `origin` ∈ {`tui`, `acp`, `web`, `gateway`, `headless`, `mcp_serve`}. Tabel otoritas §11.11.10: sumber `mcp_serve` hanya boleh menjawab permintaan dengan `origin ≠ mcp_serve` dan kelas non-P26. `TestMCPServeCannotSelfApprove` masuk GAP-SEC-21 dan acceptance F5.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0; DDL 0 (field JSON).

**Konsekuensi.** (+) ACP tetap kanal approval sah; tidak ada jalur self-approve. (−) Field `origin` wajib diisi semua pembuat `approval_request`.

## ADR-085 — CI meta-test wajib, ledger biaya tunggal, isolasi h2h terukur, ctxdb lintas OS nyata (NEW-13, NEW-14, NEW-15, sisa AUD-034, AUD-037, AUD-039)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengamandemen ADR-073 butir 1 (meta.yml) dan ADR-075 butir 1/3. Menutup NEW-13, NEW-14, NEW-15, sisa AUD-034/037/039.

*(Amandemen 2026-10-18: "dalam budget ctxdb 2.800" dibaca 3.100 (ADR-087); butir 4 dilengkapi ADR-090 — LockFileEx Windows.)*

**Aturan penerapan.** Diterapkan (2026-10-18): kode `.github/workflows/meta.yml`, `bench/h2h/{ledger.go,proxy.go,cmd/h2h,cmd/startup}`, `bench/h2h/results/f1-spend.json`, `internal/ctxdb/env_*.go` + test; teks ENGINEERING §5 (E-78, meta), ROADMAP F1 #0/F1, SCORECARD §2.3. Setiap passage mengutip ADR-085.

**Keputusan.**
1. **meta-test (NEW-13, AUD-034).** Workflow `meta` berjalan di **setiap** PR dan push `main`; job menghitung file berubah (`git diff --name-only` terhadap base) dan menjalankan `make meta-test` bila ada path gate yang berubah, selain itu lulus dengan pesan "no gate paths changed" — status selalu terlapor dan dijadikan required check (E-78). Path gate: `Makefile`, `scripts/**`, `.github/**`, `.golangci.yml`, `loc.budget`, `deps.allow`, `deps.mpl`, `go.mod`, `go.sum`, `tools/go.mod`, `tools/go.sum`, `internal/archtest/**`, `internal/testutil/**`, `internal/metaviolation/**`, `internal/ctxdb/timing_test.go`, `internal/ctxdb/race_*_test.go`, `internal/analyzers/**`. Nightly menjalankan semua gate tanpa syarat.
2. **Ledger tunggal (NEW-14, AUD-037).** Setiap `Recorder` dengan kunci upstream wajib punya `Ledger` (request ditolak tanpa ledger). Path tunggal F1 = `bench/h2h/results/f1-spend.json` (konstanta `h2h.F1LedgerPath`, di-commit, cap 2,00 tersimpan di file); `cmd/h2h` dan `cmd/startup` sama-sama memakainya, tanpa flag path lain. Pembaruan ledger = read-modify-write di bawah `flock` eksklusif (proses lain melihat belanja terbaru sebelum reservasi); di OS tanpa `flock` ledger menolak dibuka (fail-closed). `Price.Valid` mengizinkan `CachedInput = 0` (provider tanpa diskon cache) tetapi input/output wajib > 0.
3. **Isolasi h2h terukur (AUD-039).** Gate bernama, prasyarat smoke F1: `TestAgentIsolation` (uid agent dalam user namespace ≠ uid proxy, file kunci tidak ada setelah proxy membaca, env agent ⊆ allowlist) dan `TestRecordingsNoKey` (semua rekaman ter-commit dipindai pola token generik; bila `H2H_KEY_FILE` diset, juga literal/base64/base64url/hex/potongan 12 char kunci itu).
4. **ctxdb lintas OS (NEW-15).** Probe proses nyata untuk darwin (sysctl `kern.proc.pid` start time, `kill(pid,0)`, process group via `kern.proc.pgrp`) dan windows (`OpenProcess` + `GetProcessTimes`); skenario yang butuh fitur yang tidak ada di OS itu (SIGSTOP/process group di Windows) adalah **platform-n/a** dengan daftar eksplisit yang dicek test (`TestPlatformNAList`), bukan skip diam-diam. Daftar ada di komentar test dan di `AUDIT-F0-RESOLUTION.md`.

**Dampak terukur (SPEC §20).** Token/latensi/LOC produksi root (ctxdb probe dalam budget `ctxdb` 2.800)/binary/dependency 0. CI: workflow `meta` kini selalu melapor status.

**Konsekuensi.** (+) Gate tidak bisa dilemahkan tanpa meta-test; cap biaya F1 tidak bisa dilewati lewat driver lain atau ledger baru. (−) Setiap PR menjalankan job meta ringan.

## ADR-086 — Errata backlog MINOR putaran 2 (NEW-21..NEW-52) + keputusan item ADR-078 tanpa resolusi

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; pola ADR-078). Menetapkan fase untuk MINOR baru `AUDIT-F0-REVERIFY.md`, memperbaiki pelacak, dan memberi keputusan konkret untuk AUD-047, 048, 053, 064, 068 (NEW-48). Item ditutup paling lambat sebelum acceptance fase pemiliknya.

**Aturan penerapan.** Teks diterapkan (2026-10-18) untuk item berstatus "ditutup": SPEC §18.5 + GAP-INF-06 (AUD-053), P21 (AUD-064); DECISIONS (penanda NEW-49); `internal/archtest/parity_phase_test.go` (NEW-43); `.gitignore` (NEW-51); `AUDIT-F0-RESOLUTION.md` (NEW-47).

**Keputusan item ADR-078 (NEW-48).**
- **AUD-047:** cap input per turn menghitung **semua** token input termasuk `cached_input_tokens` (konservatif); config eval (`max_wall_s = 1800`, cap per turn sama untuk kedua agent) ditulis di artefak eval. Fase F1 (sebelum smoke).
- **AUD-048:** TRUST-MODEL §3 menyatakan sisa risiko orphan juga berlaku untuk `shell` di Linux (tanpa sub-cgroup di 1.0). Fase F1.
- **AUD-053:** cap per run eval scorecard terjadwal = **US$5,00** (angka lebih rendah; §18.5 diselaraskan dengan INF-06). Ditutup sekarang.
- **AUD-064:** `TestBudgetTableSum` = Σ kolom binary §3.2.4 ≤ 52 MB **dan** Σ kolom LOC §3.2.4 ≤ 190.000 **dan** Σ `loc.budget` baris core ≤ 47.000 (core + codetools + exec + heal). Ditutup sekarang (P21).
- **AUD-068:** depguard menolak `github.com/agnivade/levenshtein`, `github.com/texttheater/golang-levenshtein`, `github.com/lithammer/fuzzysearch`, `github.com/sahilm/fuzzy` untuk `internal/{loop,tools,modules}/**`. Fase F1 (sebelum `loop`/`tools`).

**Pelacak (NEW-47).** AUD-081, AUD-082, AUD-049(a) = closed (teks sudah ada).

| ID | Inti | Fase / status |
|---|---|---|
| NEW-21 | Pola PGP `BEGIN PGP PRIVATE KEY BLOCK`; userinfo username kosong | F1 (sebelum `redact`) |
| NEW-22, NEW-23, NEW-24, NEW-25, NEW-26, NEW-27 | Symlink/`/proc` task/korpus/`network`/sisa allowlist/syarat per sesi | ditutup ADR-079 |
| NEW-28 | `TestShellNetworkOutputUntrusted` → `TestShellOutputUntrustedWorkspace` (ADR-079), DEBUG di subset SEC-11, P17 dengan penyaring (ADR-081) | ditutup ADR-079/081 + ROADMAP F1 |
| NEW-29 | Kode error `stale_tag` (petakan ke `stale_read`), `backup_auth_failed` (F2), `no_text_layer`/`unsupported` (F6) | F1 (§4.3 errata sebelum `tools`) |
| NEW-30 | EC-TERM-001 → F5 | ditutup ADR-080 |
| NEW-31 | P23 F1 mengecualikan CT-34 (`TestIndexActivation`), sub-baris codeindex CT-35/CT-36 | F1 (sebelum acceptance codetools) |
| NEW-32 | Nama test eksplisit di acceptance; definisi `TestGoleakMain` = `goleak.VerifyTestMain` per paket | F1 (Deliverable #0) |
| NEW-33 | ENGINEERING §5 job list = ROADMAP F1 #0 | F1 (Deliverable #0) |
| NEW-34 | Baris `loc.budget` untuk `internal/testutil`, `internal/archtest` non-test, `internal/analyzers`; catatan background exec F5 | F1 (Deliverable #0) |
| NEW-35 | Helper timing pakai pasangan konstanta `race`/`!race`, menerima `testing.TB` | F1 (Deliverable #0) |
| NEW-36 | ≥ 1 kasus meta-test per gate arsitektur | F1 (Deliverable #0) |
| NEW-37 | Ledger h2h: flock, `CachedInput = 0`, test abort/reservasi | ditutup ADR-085 (flock, cached) + test abort F1 sebelum smoke |
| NEW-38 | Job `perf` memakai `-tags goolm,perf`; `ctxdb/ledgerstore` di deliverable F1 | F1 (Deliverable #0) |
| NEW-39 | F0-REPORT #4, Token skill per turn | ditutup ADR-083 |
| NEW-40 | Catatan GAP-COST-09 F1 (teks) di baris F1; fase cek penuh ADR-068 di baris fase pemilik | F1 |
| NEW-41 | Alokasi P1 f1-core: stable ≤ 1.000, context ≤ 1.100, volatile ≤ 100, tool eager ≤ 1.800 (Σ 4.000); `tiers.json` memuat sub-slot context | F1 (sebelum `assemble`) |
| NEW-42 | Test tertunda di acceptance fase | ditutup ADR-083 |
| NEW-43 | `TestParityPhaseColumn` assert jumlah per fase | ditutup (kode) |
| NEW-44 | Transisi `starting → needs_attention`; rujuk Discord 4004/401 GW-11 | F2 |
| NEW-45 | Ukur ulang baris `budget` 400 setelah estimator | F1 |
| NEW-46 | Tenggat backlog di ROADMAP | ditutup ADR-083 |
| NEW-47 | Pelacak basi | ditutup |
| NEW-48 | Keputusan 047/048/053/064/068 | ditutup (di atas) |
| NEW-49 | ADR diedit tanpa penanda | ditutup: penanda amandemen ditambahkan di ADR-071, 073, 075; ADR-062 diselaraskan dengan ADR-075 |
| NEW-50 | Speed parity F1 diukur di mesin referensi Q14, baseline hermes dan Damn-Bot berpasangan di mesin yang sama | F1 |
| NEW-51 | `.gitignore` | ditutup (kode); `sisasisa` milik user tidak disentuh |
| NEW-52 | Penomoran aturan stable: aturan dinomori ulang berurutan setelah aturan kondisional dihilangkan; F1 memakai varian aturan 11 tanpa `repo_map` (panduan grep/glob tetap) | F1 (sebelum `assemble`) |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Default: cap eval scorecard per run US$10 → US$5 (AUD-053).

**Konsekuensi.** (+) Semua MINOR putaran 2 punya pemilik dan tenggat; lima item tanpa keputusan kini punya keputusan. (−) Backlog F1 bertambah.

## ADR-087 — Fairness writer lintas proses di `ctxdb` (flake `TestMultiProcess` CI) + budget `ctxdb` 3.100

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Menutup flake `TestMultiProcess` (GitHub Actions run 39117760522 attempt 1: "child multi exited before READY"). Menaikkan budget `ctxdb` untuk ADR-085 butir 4 (NEW-15) dan fairness ini.

*(Amandemen 2026-10-18: butir 3 — Windows kini punya LockFileEx dan `fairLocks = true` (ADR-090).)*

**Aturan penerapan.** Diterapkan (2026-10-18): kode `internal/ctxdb/tx.go` (`begin`, `yieldIfWaiters`), `store.go`, `lock_unix.go`/`lock_other.go` (`fairLocks`), `chaos_test.go` (diagnostik anak); `loc.budget` dan ENGINEERING §2.4 (`ctxdb` 3.100, total 33.750); SPEC §11.11.11 (urutan lock: catatan fairness). Setiap passage mengutip ADR-087.

**Konteks.** Di runner CI (2 vCPU, fsync lambat, `-race`) anak ke-4 `TestMultiProcess` gagal saat `Open` ±5 s setelah start (total test 7,31 s), sedangkan tiga anak lain sudah menulis terus-menerus. Tidak tereproduksi di workstation dengan CPU dibatasi (`taskset -c 0,1`/`-c 0`, `GOMAXPROCS=1..2`, 4–8 anak, 50+ run, seed shuffle CI), tetapi **tereproduksi** dengan fsync diperlambat 30 ms (`strace -e inject=fsync,fdatasync:delay_exit=30000`): 1 dari 3 run gagal dengan `append after 5.067 s: db_busy (SQLITE_BUSY)`. Akar masalah: handler busy SQLite melakukan polling dengan jeda yang membesar sampai 100 ms dan tidak FIFO; saat lock tulis jenuh (hold lama karena fsync), proses yang commit lalu langsung `BEGIN IMMEDIATE` lagi terus menang, sehingga proses baru (`startupRecovery` di `Open`) atau writer lain menunggu melewati `busy_timeout` 5 s. `busy_timeout` yang lebih besar tidak menyelesaikan apa pun karena tanpa fairness tidak ada batas tunggu.

**Keputusan.**
1. Writer mengumumkan `BEGIN IMMEDIATE` yang tertunda dengan flock **shared** pada `run/writer.wait` selama `BEGIN` berlangsung. Setelah COMMIT/ROLLBACK, writer mencoba flock **exclusive** non-blocking pada file itu; gagal = ada writer lain yang menunggu, sehingga Store ini tidak memulai transaksi berikutnya sebelum `writerYield` = 150 ms.
2. Batas: 150 ms > jeda terpanjang handler busy SQLite (100 ms), sehingga setiap penunggu melakukan polling minimal sekali selama lock bebas. Waktu tunggu penunggu menjadi ≤ (jumlah writer − 1) × (hold + 150 ms); untuk 4 proses dan hold ≤ 400 ms (`-race`, ADR-048) ≈ 1,65 s < `busy_timeout` 5 s. Tanpa kontensi probe hanya satu syscall flock per transaksi; tidak ada jeda.
3. Di OS tanpa flock (`!unix`, `fairLocks = false`) probe mati; operasi barrier di sana tetap fail-closed seperti sebelumnya (Windows LockFileEx = kerja F1).
4. Diagnostik: anak yang keluar sebelum `READY` kini melaporkan exit status, stdout, dan stderr-nya.
5. Budget `ctxdb` 2.800 → **3.100** (probe darwin/windows NEW-15 + fairness), total tabel ENGINEERING §2.4 33.450 → 33.750 ≤ 47.000; SPEC §3.2.4 tidak berubah.

**Dampak terukur (SPEC §20).** Token/binary/dependency 0. Latensi: 0 tanpa kontensi; di bawah kontensi writer yang baru commit menunggu ≤ 150 ms sebelum transaksi berikutnya (throughput turun, ekor latensi penunggu terbatas). LOC: `ctxdb` +300.

**Konsekuensi.** (+) `db_busy` tidak lagi muncul karena kelaparan writer pada beban `TestMultiProcess`/P19; waktu tunggu terbatas. (−) Throughput tulis agregat sedikit turun saat banyak proses menulis bersamaan.

## ADR-088 — Residu model shell: `allow_session` tidak berlaku untuk shell, satu definisi `outside_read`, jalur execve runner untuk `shell_allow` dan secret ref (NEW2-01, NEW2-02, NEW2-06, NEW2-15, sisa NEW-05)

**Status:** Accepted 2026-10-18 (keputusan direktur proyek: opsi paling konservatif). Mengamandemen ADR-079 butir 2–3 dan ADR-081 butir 1. Menutup NEW2-01, NEW2-02, NEW2-06 (MAJOR), NEW2-15 (MINOR), dan sisa NEW-05 ([`AUDIT-F0-REVERIFY2.md`](AUDIT-F0-REVERIFY2.md)).

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §5.1 (baris `shell`), §13.3 (`allow_always` ACP), §14.1, §14.2; EXEC-AND-CODING §A.2, §A.6, §A.8 (tabel kebijakan, `shell_allow`, secret ref, taint), §D (EXC-15); ROADMAP F1 Acceptance. Setiap passage mengutip ADR-088.

**Keputusan.**
1. **`allow_session` tidak pernah berlaku untuk `shell` (NEW2-01).** Untuk tool `shell`, keputusan approval interaktif hanya `allow_once`, `deny`, atau `edit`; opsi `allow_session` tidak ditawarkan (TUI/web/gateway) dan ACP `allow_always` untuk shell dipetakan ke `allow_once`. Satu-satunya jalur auto shell tetap `shell_allow` argv-persis beserta syarat 1–5 ADR-079 butir 3. Kalimat "`allow_session` untuk subject persis" pada baris `privileged`/`outside_read` dihapus. `allow_session` tetap berlaku untuk tool lain sesuai §14.1 (P26 tidak pernah). Test F1: `TestShellNoAllowSession` (tabel ADR-079 + baris 37: `allow_session` diminta untuk `go test ./...` di `auto-edit`, lalu `edit_file` auto, lalu `go test ./...` → tanya; ACP `allow_always` → efek `allow_once`).
2. **Satu definisi `outside_read` (NEW2-02).** `outside_read` = label best-effort classifier untuk operand baca yang ter-resolve statis keluar root (bukan boundary, ADR-079 butir 8). Efeknya fail-closed dan sama di semua dokumen: preview menandai "reads outside workspace"; headless, termasuk `--unsafe-auto-shell`, → `outside_workspace`; output yang di-approve dibungkus `<untrusted source="shell:outside_workspace">` dan men-taint turn; tidak pernah auto (juga bila cocok `shell_allow`). SPEC §14.2 kalimat "tidak ada flag `outside_read` untuk shell" diganti definisi ini.
3. **Jalur execve runner (NEW2-06, NEW2-15).** Runner A.2 punya dua mode one-shot: (a) `bash -c` (default untuk perintah biasa, perilaku lama) dan (b) **execve langsung** (`exec.Cmd` dengan argv hasil parse, tanpa interpreter, `PATH` sesi dipakai hanya untuk resolve argv0 ke path absolut yang dicatat saat sesi mulai; PTY bila diminta). Mode (b) dipakai untuk **semua** eksekusi yang cocok `shell_allow` dan untuk setiap perintah yang memerlukan secret ref `exec.env`. Perintah yang mengandung karakter kontrol (U+0000–U+001F selain tab, U+007F), CR, atau NUL tidak pernah cocok `shell_allow` dan tidak pernah memakai mode (b). "Binary yang sama" = path absolut hasil resolve sama **dan** (dev, inode, size, mtime) sama dengan saat sesi mulai. Windows: `shell_allow` hanya untuk argv0 yang di-resolve ke `.exe` lewat `PATH` sesi (cwd tidak pernah dicari; `.bat`/`.cmd` tidak pernah cocok), dieksekusi via `CreateProcess` tanpa `cmd.exe`.
5. **`shell_allow` hanya one-shot (amandemen penerapan 2026-10-18).** Call `shell` dengan parameter `session` (sesi persisten) **tidak pernah** cocok `shell_allow`; eksekusi `shell_allow` selalu one-shot mode (b) dengan `cwd` call di dalam root. Syarat 1–2 ADR-079 butir 3 (cwd hidup/eligibility sesi persisten) diganti aturan ini.
4. **Secret ref (sisa NEW-05).** Var secret `exec.env` hanya disuntikkan pada mode (b) untuk argv0 ∈ `commands`; mode (a), sesi persisten, dan proses lain tidak pernah menerimanya. Test F1: `TestSecretRefScope` (argv0 dalam `commands` via mode (b) → var ada; `bash -c` dengan argv0 sama → var tidak ada; sesi persisten → tidak ada; argv0 lain → tidak ada; perintah yang mereferensikan `$X` → label `unknown`), dan kasus scope ditambahkan ke EXC-15 `TestEnvAllowlist`. `TestShellAllowExactMatch` += CR, NUL, karakter kontrol, `.bat`/cwd Windows.

**Dampak terukur (SPEC §20).** Token/binary/dependency 0. LOC: dalam budget `exec` 3.500 + `exec/classify` 1.500 (ADR-081). UX: satu prompt per perintah shell non-`shell_allow`, tanpa "izinkan untuk sesi" (tambahan biaya prompt di atas ADR-079, dicatat jujur).

**Konsekuensi.** (+) Tidak ada jalur auto shell selain `shell_allow`; semantik `shell_allow` tidak bergantung pada kesetaraan parser mvdan vs bash. (−) Pengulangan perintah shell yang sama di satu sesi tetap meminta approval kecuali ditambahkan ke `shell_allow`.

## ADR-089 — Approval `execute_code` dan gerbang trust codeintel/LSP (NEW2-03, NEW2-04)

**Status:** Accepted 2026-10-18 (keputusan direktur proyek). Melengkapi ADR-055, ADR-069 butir 7, ADR-076 butir 1, ADR-079. Menutup NEW2-03, NEW2-04 (MAJOR, sebelum F5).

**Aturan penerapan.** Teks diterapkan (2026-10-18): SPEC §9.2, §12 (`[exec] code_auto_languages`), §14.1 (baris `execute_code`), §22.1 GAP-COST-10; EXEC-AND-CODING §B.3/§B.4 (codeintel/LSP), §B.7; CODE-TOOLS §6 (codeintel); TRUST-MODEL §1–§3; ROADMAP F5 Acceptance. Setiap passage mengutip ADR-089.

**Keputusan.**
1. **`execute_code` (NEW2-03).** Baris §14.1 baru: `ask` dan `auto-edit` → **selalu tanya** + preview (bahasa, ringkasan kode, `network`); `auto` → tanya kecuali `language` ada di allowlist config `exec.code_auto_languages` (default `[]`, nilai ∈ {`python`, `javascript`, `go`, `starlark`}); headless → `denied` kecuali `language` ada di allowlist **dan** `--approval auto`. Syarat tambahan untuk auto: workspace trusted, turn tidak tainted, tidak ada tulis tanpa approval di sesi (syarat 3–5 ADR-079 butir 3). `language` `bash`/`sh`/`zsh`/`pwsh` (atau interpreter shell apa pun) diperlakukan persis seperti tool `shell` di bawah ADR-079/088: auto hanya bila isi kode cocok persis `shell_allow`, tidak pernah lewat `code_auto_languages`. `allow_session` tidak berlaku untuk `execute_code`. Panggilan tool dari dalam skrip (RPC/binding) tetap melewati pipeline §4.3 masing-masing (ADR-055, ADR-076). Definisi vulnerability (a) ADR-079 butir 8 diperluas: "perintah shell **atau kode `execute_code`** yang berjalan tanpa approval eksplisit di luar jalur auto terdokumentasi". Test F5: `TestExecuteCodeApproval` (mode × bahasa × allowlist × trust × taint; `bash` = model shell).
2. **Codeintel/LSP (NEW2-04).** Language server, build script, proc-macro, plugin `node_modules`, Gradle/Maven, dan alat lain yang mengeksekusi kode proyek **tidak pernah** dimulai otomatis di workspace untrusted: diagnostik pasca-edit dan `repo_map` memakai jalur tanpa eksekusi (go/ast, tree-sitter WASM) saja; tool codeintel yang membutuhkan LSP → `module_unavailable` dengan hint "workspace not trusted". Di workspace trusted, LSP mulai otomatis dengan setting yang mematikan eksekusi build script/proc-macro bila server mendukungnya (mis. rust-analyzer `cargo.buildScripts.enable=false`, `procMacro.enable=false`; tsserver tanpa plugin workspace); eksekusi yang tidak bisa dimatikan = sisa risiko TRUST-MODEL §3. Test F5: `TestLSPWorkspaceTrustGate` (untrusted → 0 proses LSP/build; trusted → setting aman diterapkan).
3. **Amandemen penerapan 2026-10-18.** Setiap operasi codeintel yang akan memulai LSP (termasuk `code_diagnostics`, `code_rename`) → `module_unavailable` di workspace untrusted; tool dengan fallback tanpa eksekusi (tags/tree-sitter/grep) memakai fallback itu. ACP `allow_always` untuk `execute_code` dipetakan ke `allow_once`.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0 (dalam budget F5). Config: +1 kunci `exec.code_auto_languages` (default `[]`).

**Konsekuensi.** (+) Tidak ada jalur auto-eksekusi kode workspace di luar `shell_allow`/allowlist bahasa yang eksplisit. (−) `execute_code` di mode `auto` meminta approval sampai pengguna mengisi allowlist; diagnostik LSP tidak ada di workspace untrusted.

## ADR-090 — Lock file Windows (LockFileEx) di `ctxdb` (NEW2-05, sisa NEW-15)

**Status:** Accepted 2026-10-18 (keputusan didelegasikan). Mengamandemen ADR-085 butir 4 dan ADR-087 butir 3. Menutup NEW2-05 dan sisa NEW-15. Diimplementasikan di F0: commit `962c065`.

**Aturan penerapan.** Diterapkan (2026-10-18): kode `internal/ctxdb/lock_windows.go` (baru), `lock_other.go` (`!unix && !windows`), `lock_test.go`; teks SPEC §11.11.1/§11.11.11, ROADMAP F1 #0 Acceptance. Setiap passage mengutip ADR-090.

**Keputusan.**
1. `tryLock` Windows = `LockFileEx` (`golang.org/x/sys/windows`, dependency yang sudah ada) pada byte 0 file lock, `LOCKFILE_FAIL_IMMEDIATELY` + `LOCKFILE_EXCLUSIVE_LOCK` untuk eksklusif; `ERROR_LOCK_VIOLATION` (dan `ERROR_IO_PENDING`, yang hanya mungkin pada handle overlapped) → `ErrDBBusy`, error lain → error nyata (bukan kontensi). Lock milik handle; dilepas saat handle ditutup atau proses mati. `fairLocks = true` di Windows (ADR-087 aktif).
2. `TestFileLockSemantics` (semua OS CI) membuktikan semantik shared/exclusive non-blocking per handle yang dipakai barrier, urutan open, dan fairness.
3. Acceptance Deliverable F1 #0: job matrix Windows menjalankan `go test ./internal/ctxdb/` hijau, termasuk `TestFileLockSemantics`, S023, S044, dan `TestPlatformNAList`; kegagalan probe Windows di runner = perbaikan F1 #0, bukan skip.
4. OS non-CI (`!unix && !windows`) tetap fail-closed (`db_busy`).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC: `ctxdb` 2.857 / 3.100 (ADR-087).

**Konsekuensi.** (+) Store bisa dibuka dan probe Windows tereksekusi di matrix F1 #0. (−) Belum dieksekusi di runner Windows sampai F1 #0 (hanya vet + compile + lint lintas OS di F0).

## ADR-091 — Errata backlog MINOR putaran 3 (NEW2-07..NEW2-27) + sisa partial AUD-020/NEW-10

**Status:** Accepted 2026-10-18 (pola ADR-078/086). Menetapkan fase untuk MINOR [`AUDIT-F0-REVERIFY2.md`](AUDIT-F0-REVERIFY2.md); item ditutup paling lambat sebelum acceptance fase pemiliknya. NEW2-12 dan NEW2-13 (residu partial AUD-020, NEW-10) dan NEW2-15 (ADR-088) ditutup sekarang.

**Aturan penerapan.** Teks diterapkan (2026-10-18) untuk item "ditutup": SPEC §6, §17.4 (SIGTSTP); EXEC-AND-CODING §B (notice `user_changed`); SCORECARD; MITIGATION-MATRIX. Setiap passage mengutip ADR-091.

| ID | Inti | Fase / status |
|---|---|---|
| NEW2-07 | `TestNoLLMBaselineCoverage` sematkan peta dimensi → fase; `bench-budget` = `cd bench && go test ./...` | F1 (Deliverable #0, bersama AUD-075) |
| NEW2-08 | `--unsafe-auto-shell` tanpa `--approval auto` = error penggunaan (exit 2); event `system_notice{kind:"unsafe_auto_shell"}` + field hasil `unsafe_auto_shell:true` | F1 (sebelum `exec`) |
| NEW2-09 | Hook `pre_tool` `allow` = lanjut ke kebijakan normal, tidak pernah meng-approve | F3 |
| NEW2-10 | Gateway/cron: `shell_allow` tidak berlaku untuk turn gateway role `user`; owner gateway dan cron mengikuti trust root `serve` (workspace cron = trusted hanya bila tercantum `security.trusted_workspaces`) | F4 |
| NEW2-11 | Syarat "tulis tanpa approval" direset hanya lewat review daftar file yang ditulis auto; dihitung per data dir lintas sesi | F1 (sebelum `exec`) |
| NEW2-12 | SIGTSTP live = `signaled` (`signal:"SIGTSTP"`), bukan `interrupted`; `TestACPChildNoStdin` → `TestACPChildStdinIsolation` | **ditutup** |
| NEW2-13 | Notice `user_changed` dirender ≤ 23 tok (path pertama + `+N more`) | **ditutup** |
| NEW2-14 | Redaksi stream: lookbehind ≤ panjang prefix maksimum atas teks yang belum dipancarkan; "bocor" = ≥ 4 char nilai secret di output | F1 (sebelum `redact`) |
| NEW2-15 | Parser `shell_allow` vs bash; Windows | **ditutup** ADR-088 butir 3 |
| NEW2-16 | `non_mutating` = label hanya untuk perintah tanpa redirect/flag tulis yang **terdaftar** di testdata classifier; default perintah lain = tidak `non_mutating`; `TestPlanModeShell` F3 di SPEC/EXEC; penanda amandemen ADR-070; `TestShellAutoKeysIgnored` (F1); K-02/S-15 diselaraskan | F1 (sebelum `exec`) |
| NEW2-17 | Ringkasan P28 (SPEC §1, ROADMAP F9, SCORECARD §3) menyebut pengecualian `patch` 2× | F1 |
| NEW2-18 | Gate pengulangan `non_idempotent` setelah `outcome_unknown` menang atas `shell_allow`; baris 38 tabel ADR-079 | F1 (sebelum `exec`) |
| NEW2-19 | Field `origin` di daftar field `approval_request` (SPEC, SELF-HEALING, komentar `0005_recovery.sql`, spike approval) | F5 |
| NEW2-20 | `origin` cron/batch/kanban → `headless`; delegate → origin induk | F5 |
| NEW2-21 | Verify tanpa `shell_allow` di headless → "unverified (`evidence_gap` menurut §7.2)" | F1 |
| NEW2-22 | Bullet `make phase-acceptance` hijau di acceptance tiap fase; item covered lebih awal (fase > `PHASE`) diizinkan | F1 (Deliverable #0) |
| NEW2-23 | `TestWriterFairness` (asersi tunggu maks, `DAMNBOT_TIMING`), analisis 50 klien, pengecualian Append di §17, `tryLock` membedakan `ErrDBBusy` dari error lain (Windows sudah, ADR-090), skenario "penunggu SIGSTOP" §11.11.13 | F2 |
| NEW2-24 | Ledger h2h: `reserved_usd` di ledger di bawah flock, proxy berhenti bila `Add` gagal, path di-resolve terhadap direktori modul | F1 (sebelum smoke) |
| NEW2-25 | Penanda amandemen ADR-049/068/069/085 dan komentar `loc.budget` (ADR-087) | **ditutup** (penanda ditambahkan bersama ADR ini) |
| NEW2-26 | adrgate E-82 memicu pada `loc.budget` dan tabel ENGINEERING §2.4 | F1 (Deliverable #0) |
| NEW2-27 | Komentar `0003_gateway.sql:11`, ROADMAP:117 pengecualian CT-34/35/36, bullet backlog F2, driver damnbot h2h + catatan asimetri `--yolo` di SCORECARD | F1 (0003/ROADMAP/SCORECARD) / F2 (bullet) |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0.

**Konsekuensi.** (+) Semua MINOR putaran 3 punya pemilik dan tenggat. (−) Backlog F1 bertambah.

## ADR-092 — Errata backlog MINOR putaran 4 (NEW3-01..NEW3-04)

**Status:** Accepted 2026-10-18 (pola ADR-078/086/091). Menetapkan fase untuk MINOR [`AUDIT-F0-REVERIFY3.md`](AUDIT-F0-REVERIFY3.md); item ditutup paling lambat sebelum acceptance fase pemiliknya. Tidak ada item yang ditutup sekarang; perbaikan mengikuti rekomendasi audit.

**Aturan penerapan.** Belum ada teks/kode yang diterapkan; setiap item diterapkan di fase pemiliknya dan passage-nya mengutip ADR-092.

| ID | Inti | Fase pemilik |
|---|---|---|
| NEW3-01 | `unlockFile` Windows memanggil `windows.UnlockFileEx` sebelum `Close` (pelepasan lock saat close bergantung sumber daya sistem); `TestFileLockSemantics` dan probe fairness ADR-087 stabil di runner Windows | F1 (Deliverable #0, bersama matrix Windows ctxdb ADR-090) |
| NEW3-02 | Auto headless `execute_code` untuk bahasa di `code_auto_languages` mensyaratkan `--unsafe-auto-shell` (atau flag `--unsafe-auto-code`), atau dicatat eksplisit sebagai residu TRUST-MODEL §3 | F5 (sebelum `execute_code`) |
| NEW3-03 | Runner mode (b): perintah ber-secret-ref yang bukan simple command literal (glob/`~`/ekspansi) → mode (a) **tanpa** secret dengan notice, atau tolak `invalid_args`; kasus ditambah ke `TestSecretRefScope` | F1 (sebelum `internal/exec`) |
| NEW3-04 | argv0 `shell_allow` yang ter-resolve ke path di dalam root (atau di bawah entri `PATH` relatif) tidak pernah cocok; kasus ditambah ke `TestShellAllowExactMatch` | F1 (sebelum `internal/exec`) |

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0.

**Konsekuensi.** (+) Semua MINOR putaran 4 punya pemilik dan tenggat; gate F0 → F1 tidak tertahan (MINOR tidak menahan gate). (−) Backlog F1 #0 dan `internal/exec` bertambah.

## ADR-093 — Penutupan temuan spec F0 yang belum diputuskan (TS-01..TS-10, TS-P02..P07, TS-E01..E04)

**Status:** Accepted 2026-10-18 (keputusan direktur atas delegasi user: opsi paling konservatif, dalam budget, tanpa dependency baru). Menutup temuan [`F0-REPORT.md`](F0-REPORT.md) §Temuan spec yang belum punya ADR: TS-01, 02, 04, 05, 06, 08, 09, 10, TS-P02..P07, TS-E01..E04. Aturan umum: "Asumsi yang dipakai" F0 menjadi keputusan, kecuali bertentangan dengan aturan yang ada; pengecualian dicatat per baris (TS-P03, TS-P05, TS-P07, TS-E03).

**Aturan penerapan.** Diterapkan (2026-10-18): kode `internal/ctxdb/errors.go` (`raiseCode` cocok persis), `store.go` (`trusted_schema`), komentar `recovery.go`/`append.go`, test `internal/ctxdb/errors_test.go` (`TestRaiseCodeExact`, `TestTrustedSchemaExplicit`, `TestNonRefShaClosedList`), komentar `bench/budget`; teks SPEC §2 (P13), §11 (PRAGMA), §11.6, §11.10, §11.11.3, §11.11.13; ENGINEERING header, E-21, E-39; SELF-HEALING §3.4; BASELINE-hermes §2; SCORECARD §2.2, §3.3; EXEC-AND-CODING §C. Setiap passage mengutip ADR-093.

| ID | Keputusan | Diterapkan di |
|---|---|---|
| TS-01 | Path modul `github.com/damnbot/damnbot` (+ `/bench`, `/tools`), tanpa vanity import | ENGINEERING header |
| TS-02 | "MB" P13 = desimal, 1 MB = 1.000.000 B (lebih ketat dari MiB) | SPEC §2 |
| TS-04 | Pengecualian bernama E-39: target `licenses` memakai `GOFLAGS=-tags=goolm` (go-licenses v2 tanpa `-tags`) | ENGINEERING E-39 |
| TS-05 | Pengecualian CGO E-39: hanya job `test` (`-race`) `CGO_ENABLED=1`; rilis/size/build-matrix/test-timing tetap 0 | ENGINEERING E-39 |
| TS-06 | Kolom BASELINE §2 = "Char" (Python `len`), bukan byte | BASELINE-hermes §2 |
| TS-08 | "prompt 20.240 B" floor = informatif, bukan gate; overhead 14.500 tok tetap gate ±5% | BASELINE-hermes §2 |
| TS-09 | Sumber normatif op speed = `bench/speed/catalog.json` + `catalog.go`; op `proxy: true` hanya baseline, acceptance baris mengukur perintah asli | SCORECARD §3.3 |
| TS-10 | `task_id` baru per run (`fresh_task`); tool yang diintersep hermes diukur end-to-end lewat fake provider (F1), protokol sama untuk kedua agent | SCORECARD §3.3 |
| TS-P02 | Baris `sessions('__system__')` dibuat setiap writer saat open (`INSERT … ON CONFLICT DO NOTHING`, langkah 2 SELF-HEALING §3.4) | SPEC §11, SELF-HEALING §3.4 |
| TS-P03 | `sqlite.Driver` privat per Store (writer/reader); **konflik** dengan SPEC "`SQLITE_INNOCUOUS`" (tidak dapat diset di modernc) → diganti: `trusted_schema=ON` **eksplisit** di DSN semua koneksi (tidak bergantung default kompilasi) | SPEC §11; `store.go`, `TestTrustedSchemaExplicit` |
| TS-P04 | Daftar tertutup kunci 64-hex non-ref: `preview_sha`, `args_sha`, `payload_sha`; perubahan lewat ADR | SPEC §11.6; `TestNonRefShaClosedList` |
| TS-P05 | **Konflik** dengan E-21 → mekanisme patuh: satu fungsi `ctxdb.raiseCode`, hanya untuk kode extended 1811, **kesamaan penuh** pesan driver dengan `constraint failed: <token> (1811)` untuk token tertutup → sentinel; selain itu `internal` (fail-closed). Tanpa substring/prefix/parse; pengecualian tunggal E-21, di-allowlist `nostringclassify` (F1) | ENGINEERING E-21, SPEC §11.10; `errors.go`, `TestRaiseCodeExact` |
| TS-P06 | Tanpa retry `BEGIN IMMEDIATE` baru (`netx/retry`): akar starvation ditutup jeda purge/gc ≥ 110 ms (`4c342e8`) + fairness writer ADR-087; test #15 memakai laju realistis; `TestWriterFairness` tetap F2 (ADR-091 NEW2-23) | — (ADR-087 sudah diterapkan) |
| TS-P07 | **Konflik** dengan E-24 (`recover` dilarang) → `TestMaintResetOnPanic` dibaca sebagai abort goroutine (`runtime.Goexit`, `defer` + rollback hook) = `TestScenario/S028_MaintModeFailClosedAndAbort`; panic sungguhan = proses mati, koneksi dan mode ikut mati | SPEC §11.11.3, §11.11.13 |
| TS-E01 | Root eval set = `bench/evals/` (modul `bench`) | SCORECARD §2.2, EXEC-AND-CODING §C |
| TS-E02 | E4 = 20 recall + 10 koreksi = 30 | SCORECARD §2.2 |
| TS-E03 | **Konflik** dengan EXEC-AND-CODING §C (aturan seleksi normatif sudah ada) → §C menang: `evalset/v1` hanya smoke informatif; `evalset/v2` sesuai §C (hash `damnbot-1.0:` + stratifikasi, Terminal-Bench 2.0 di-pin, komposisi internal 3/2/2/1/2) wajib sebelum scorecard mingguan pertama (F3) | SCORECARD §2.2, EXEC-AND-CODING §C |
| TS-E04 | Query E9 = deskripsi skill tanpa token nama skill (sama untuk kedua agent); angka absolut dilaporkan dengan catatan | SCORECARD §2.2 |

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC `ctxdb` 2.851 / 3.100 (`raiseCode` lebih pendek; +1 pragma).

**Konsekuensi.** (+) Tidak ada temuan spec F0 tanpa keputusan; satu-satunya pembacaan teks error kini kesamaan penuh yang diuji. (−) `evalset/v2` menjadi pekerjaan sebelum F3; `trusted_schema=ON` eksplisit (residu: schema DB dipercaya — hanya ditulis migrasi Damn-Bot di data dir 0700).

## ADR-094 — Penerapan Deliverable F1 #0 (CI hardening): klarifikasi dan deviasi

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; opsi paling konservatif dalam batas menit Actions repo privat). Menerapkan ADR-073 butir 2, ADR-077 butir 3–4, ADR-080, ADR-085 butir 1, ADR-090 butir 3; menutup backlog "Deliverable #0" ADR-078 (AUD-056, 057, 059, 067, 069, 070, 071, 072, 073, 074, 083), ADR-086 (NEW-32..36, NEW-38), ADR-091 (NEW2-22, NEW2-26; NEW2-07 bagian `bench-budget`), ADR-092 (NEW3-01). Mengamandemen daftar path ADR-085 butir 1 dan penempatan analyzer NEW-34.

*(Amandemen 2026-10-21: butir 5 dilengkapi ADR-127 — runner tetap `bench-ref` tetap belum ada (keputusan owner), sehingga CT-09 di job `perf` memakai gate relatif interim.)*

**Aturan penerapan.** Diterapkan (2026-10-18), branch `f1-ci-hardening`: kode `Makefile`, `scripts/{ci-meta-test,coverage,fuzz,deadcode,pr-meta,loc-check}.sh`, `.github/workflows/{ci,meta,pr-meta,nightly}.yml`, `.github/{CODEOWNERS,dependabot.yml}`, `.golangci.yml`, `loc.budget`, `deps.mpl`, `PHASE`, `CHANGELOG.md`, `testdata/docs_exclusions.txt`, `internal/testutil/{timing,phase}`, `internal/archtest/*`, `tools/analyzers/*`, `internal/ctxdb/{lock_windows,restore,tx}.go` + test; teks ENGINEERING §2.4/§4.4/§5/§7.2, CROSS-CUTTING §7, SPEC §20, ROADMAP F1. Rincian per item + kasus meta-test: [`F1-REPORT.md`](F1-REPORT.md) §"Deliverable F1 #0". Setiap passage mengutip ADR-094.

**Keputusan.**
1. **Analyzer di modul `tools`.** `nohttpclient` (E-05) dan vettool `damnbot-vet` ada di `tools/analyzers` (sesuai ENGINEERING §5.1/E-21), bukan `internal/analyzers`: `golang.org/x/tools/go/analysis` tidak boleh masuk `go.mod` utama (E-45). Path gate meta ADR-085 menjadi `tools/**` (menggantikan `tools/go.mod`, `tools/go.sum`, `internal/analyzers/**`); `internal/ctxdb/timing_test.go` dan `race_*_test.go` dihapus dari daftar karena helper pindah ke `internal/testutil/timing`; `testdata/docs_exclusions.txt` ditambahkan (mendefinisikan spec-guard). Baris `loc.budget` infra di luar Σ core (NEW-34): `testutil/timing` 80, `testutil` 200, `archtest` 300, `analyzers` 400.
2. **E-84 dihitung atas baris Go non-test.** "≤ 600 baris non-test" = baris ditambah + dihapus pada file `.go` bukan `_test.go` dan bukan generated (konsisten dengan "LOC Go non-test" E-19/E-70); dokumen, skrip, YAML, dan testdata tidak dihitung (dokumen dijaga spec-guard).
3. **Matrix E-55.** 6 runner: `ubuntu-latest`, `ubuntu-24.04-arm`, `macos-latest`, `macos-15-intel`, `windows-latest`, `windows-11-arm`; windows/arm64 tanpa `-race` (`make test RACE=`), karena Go tidak punya race detector di windows/arm64. Gate lain (lint, size, loc, coverage, fuzz, deadcode, perf, …) hanya di ubuntu: hasilnya tidak bergantung OS host, dan menit macOS/Windows dihitung ×10/×2.
4. **Flake nightly** = `make flake` (`go test -count=20 -shuffle=on`, tanpa `-race`, sama dengan E-73); job lulus dengan pesan bila tidak ada commit di `main` 24 jam terakhir (HEAD itu sudah diuji 20× malam sebelumnya). `workflow_dispatch` selalu menjalankan.
5. **Job `perf`** berjalan di runner GitHub bersama (belum ada runner tetap `bench-ref`): `-tags goolm,perf`, helper `timing.Check` memberi margin ×1,5 hanya di build `perf` (ADR-077 butir 4). Regex benchmark: `BenchmarkShellSpawn`, `BenchmarkClassify`, `BenchmarkIterationOverhead`, `BenchmarkCodetools`, `BenchmarkAppend`; 0 benchmark cocok = lulus sampai paket F1 menambahkannya.
6. **Helper waktu** `internal/testutil/timing` (`Enforced`/`Check`, `testing.TB`, pasangan konstanta `RaceEnabled` race/!race, NEW-35); helper `PHASE` `internal/testutil/phase` (`Current`, `Item`; semantik ADR-080 + NEW2-22) dengan `TestPhaseFile`.
7. **LOC (AUD-073).** scc tanpa `--no-gen`; file generated hanya dikecualikan lewat baris `GENERATED <glob>` di `loc.budget`; file Go non-test di `internal/*` tanpa baris = gagal; fixture `testdata/` tidak dihitung.
8. **Lisensi (AUD-069).** `deps.mpl` + `TestMPLIsolation` (build tag `licenses`, dijalankan `make licenses`): setiap modul MPL-2.0 dari `go-licenses report` ⊆ `deps.mpl`; lisensi di luar allowlist E-40 = gagal.
9. **spec-guard (AUD-056)** di `scripts/pr-meta.sh`: diff `SPEC.md`/`docs/*.md` wajib `ADR-NNN` di deskripsi PR + `docs/DECISIONS.md` berubah; pengecualian tunggal label `spec-typo` + ≥ 1 approver. Entri seluruh-file `testdata/docs_exclusions.txt` dikecualikan; entri baru (SPEC §20 butir "menambah entri = ADR"): laporan `docs/F[0-9]-REPORT.md`, `docs/AUDIT-*.md`, `docs/HANDOFF-*.md`.
10. **adrgate (E-82 + NEW2-26)** memicu pada `deps.allow`, `loc.budget`, `internal/config/defaults*.go`, serta hunk diff yang jatuh di SPEC §2, §3.2.4, §17, atau ENGINEERING §2.4 (pemetaan judul Markdown, sisi lama dan baru).
11. **deadcode "gabungan" (E-53):** fungsi mati = dilaporkan `deadcode -test` di setiap GOOS (linux, darwin, windows) yang meng-compile file-nya. `ctxdb.BackupPath` (tak terjangkau di ketiga GOOS) dihapus.
12. **Coverage (E-58):** satu profil `-coverpkg=./internal/...`, blok dihitung sekali; paket berfungsi yang tidak muncul di profil = gagal.
13. **Nama job** `size-check` → `size` (AUD-074(e), = ENGINEERING E-60).
14. **Branch protection.** *(Amandemen 2026-10-18: repo dijadikan publik oleh owner.)* `main` diproteksi: PR wajib, 0 approval (repo satu pemilik; E-79 `require_code_owner_reviews` belum aktif), status check strict = semua job F1 #0 (`ci`: setiap entri `test (…)`, lint, dst.; `meta-test`; `pr-meta`), dicatat di F1-REPORT. Merge lewat API merge PR (rebase), bukan push langsung.
15. **fsync direktori di Windows = no-op** (`internal/ctxdb/dirsync_windows.go`): handle direktori tidak bisa di-`FlushFileBuffers` ("Access is denied"); NTFS menjurnal metadata direktori, dan VFS Windows SQLite juga tidak men-sync direktori. Isi file tetap di-fsync sebelum setiap rename. POSIX tetap `fsync(dir)`.
16. **Kebocoran handle = bug Windows.** `MaintenancePending` membiarkan `os.Root` terbuka saat marker tidak ada (1 fd bocor per panggilan; di Windows data dir tidak bisa dihapus/diganti) — diperbaiki. Setiap `newEnv` test `ctxdb` kini menegakkan 0 descriptor terbuka di bawah data dir setelah cleanup (`openUnder`, `/proc/self/fd` di Linux), sehingga kelas bug ini tertangkap di job Linux juga.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency produksi 0 (`golang.org/x/tools` v0.52.0, `golang.org/x/perf`, `honnef.co/go/tools` v0.8.2, `cyclonedx-gomod` v1.12.0 hanya di `tools/go.mod`, sesuai ENGINEERING §4.4). LOC produksi: `ctxdb` −3 (BackupPath) +8 (UnlockFileEx). LOC infra: `testutil` ≈ 130, `archtest` 63, `analyzers` 105. CI: +15 job PR (6 di antaranya matrix OS), +2 job harian; menit per run dicatat di F1-REPORT.

**Konsekuensi.** (+) Semua gate ADR-073 punya job dan kasus meta-test; Windows/macOS/arm64 dieksekusi nyata. (−) Biaya menit Actions naik (macOS ×10 per run PR); E-78/E-79 menunggu langkah manusia.

## ADR-099 — Header asal ADR-018 (b) untuk file data: sidecar `ORIGIN.md` per direktori + `TestAttributionHeaders`

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; opsi paling konservatif, tanpa dependency baru). Menerapkan ADR-018 butir (b) untuk file turunan yang formatnya tidak bisa memuat komentar; menutup backlog F1 "ADR-018(b) + `TestAttributionHeaders`" (`docs/F1-REPORT.md`).

**Konteks.** ADR-018 (b) mewajibkan "header asal di setiap file yang disalin/diturunkan". Semua file turunan pihak ketiga saat ini berformat data (JSON, `.jsonl.gz`, `.tiktoken.gz`, TSV, daftar ID, teks prompt yang dibandingkan byte-persis oleh harness budget): menyisipkan header akan mengubah byte (merusak SHA/golden) atau membuat file tidak valid (JSON, gzip). `TestAttributionHeaders` dirujuk ADR-018 tetapi belum ada.

**Keputusan.**
1. **Sidecar per direktori.** Setiap direktori yang memuat file turunan punya `ORIGIN.md` = header asal untuk file di direktori itu (bukan subdirektori). Format: bagian `## <sumber>` dengan baris wajib `- Sumber: <URL>`, `- Commit: <commit/revisi>`, `- Lisensi: <lisensi + path notice>` (opsional `- Isi:`), lalu tabel `| File | SHA-256 |` berisi basename dalam backtick dan SHA-256 hex byte-persis. Satu direktori boleh memuat beberapa sumber (mis. `bench/evals/sources`).
2. **Himpunan file turunan = glob `docs/ATTRIBUTION.md`** (ADR-018 (c)); `**` = nol atau lebih segmen. Satu sumber kebenaran: file yang cocok glob wajib tercantum di `ORIGIN.md`, entri `ORIGIN.md` wajib menunjuk file yang ada dan cocok glob, glob yang tidak cocok file apa pun dan notice `third_party/manual/*` yang hilang = gagal.
3. **Byte-persis lintas OS.** SHA-256 dihitung atas byte checkout tanpa normalisasi EOL; path turunan dipasang `-text` di `.gitattributes` (Windows `core.autocrlf` tidak mengubahnya).
4. **File kode sumber turunan** (Go, Markdown, YAML yang bisa memuat komentar) tetap wajib header asal di file itu sendiri menurut ADR-018 (b) apa adanya; ADR ini hanya menambah bentuk sidecar untuk data. Saat ini belum ada file kode turunan.
5. **Test.** `TestAttributionHeaders` (`internal/archtest`, semua OS matrix) memeriksa butir 1–3 atas repo; `TestAttributionChecker` memastikan setiap kelas pelanggaran (sidecar hilang, SHA beda, file tak tercantum, entri basi, field hilang, glob mati, notice hilang) terdeteksi. Paket F1 yang menambah testdata pihak ketiga (JSON-Schema-Test-Suite `internal/schema`, korpus gitleaks `internal/redact`) menambah baris `ATTRIBUTION.md` + `ORIGIN.md` di PR-nya.

**Dampak terukur (SPEC §20).** Token/latensi/LOC produksi/binary/dependency 0 (test + dokumen). Mengubah file turunan tanpa memperbarui SHA di `ORIGIN.md` = merah di semua OS.

**Konsekuensi.** (+) Asal, revisi, dan lisensi setiap file turunan dapat dicek per file tanpa mengubah byte data; perubahan diam-diam pada data pihak ketiga terdeteksi. (−) Memperbarui data turunan wajib memperbarui SHA di sidecar.

## ADR-100 — `internal/netx`: paket `netx/retry`, semantik kelas di luar tabel P6, parser SSE, penjaga dial/egress

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; opsi paling konservatif dalam budget `netx` 1.800 LOC, tanpa dependency baru). Melengkapi SPEC P6/§4.1/§4.1.1, ENGINEERING E-05/E-30/E-32/E-35/E-69 untuk hal yang tidak ditetapkan teks.

**Aturan penerapan.** Diterapkan lewat PR berurutan (E-84 ≤ 600 baris Go non-test per PR): (1) `internal/netx/retry` + parser SSE (`internal/netx/sse.go`); (2) klien HTTP: transport bersama, penjaga dial, watchdog stream, error fase koneksi; (3) gerbang DLP egress + log metadata lewat `internal/redact` (setelah `redact` di `main`). ENGINEERING §1.3 (pohon paket), §2.2.1 (node NET), E-06 dan `internal/archtest/allowed.go` mencantumkan `netx/retry` sebagai bagian node NET.

**Keputusan.**
1. **Paket `internal/netx/retry`** (path yang sudah dirujuk E-30/E-32 dan exclusion forbidigo `time.Sleep`) = bagian node NET: tanpa impor internal; `netx` dan pemakai NET (provider, acp, modul) boleh mengimpornya. Satu implementasi retry (`retry.Do`), satu breaker (`retry.Breaker`). Tunggu antar attempt memakai timer + `ctx` (tanpa `time.Sleep`, dapat diuji `testing/synctest`). Budget: dihitung di baris `netx` `loc.budget` (prefix terpanjang), Σ netx 1.800 tidak berubah.
2. **Kelas.** Himpunan SPEC §4.1.1 + `unclassified` (error yang tidak dikenali aturan typed mana pun; tidak pernah di-retry, tidak memicu fallback). Status HTTP di luar tabel (mis. 405, 409, 501, 505) = `bad_request` (tidak di-retry). 429 = `rate_limited` apa pun isi body (EC-PROV-029). `empty_response` dan `context_length` **tidak** retryable di `netx/retry`: satu call ulangnya dimiliki loop (ADR-078 AUD-045). `ctx` pemanggil berakhir → tidak pernah retry; kelas = `cancelled` (cancel) atau `timeout` (deadline).
3. **Backoff.** Tunggu setelah attempt 1 = 1 s, setelah attempt 2 = 4 s, masing-masing × U[0,8; 1,2) (jitter seragam ±20 %); `Retry-After` > 0 dan ≤ 30 s menggantikan backoff; `Retry-After` > 30 s → berhenti seketika dengan `ErrRetryAfterTooLong` (kelas tetap `rate_limited`, sehingga rotasi credential/fallback berjalan tanpa menahan turn; EC-PROV-010). Total tunggu terburuk per model = 2 × 30 s. Setiap retry terjadwal dilaporkan ke `Policy.Notify` (WARN/event oleh pemanggil, SPEC §15).
4. **Breaker (SPEC §4.1) in-process** per rute: 5 kegagalan kelas retryable dalam jendela geser 60 s → open 30 s → half-open tepat 1 probe (pemanggil lain `ErrCircuitOpen`); probe gagal → open lagi, probe selesai dengan kelas lain → closed + penghitung direset. Kelas non-retryable netral saat closed. `ErrCircuitOpen` berkelas `overloaded` (fallback boleh) tetapi tidak pernah di-retry ke model yang sama. Berbagi lintas proses (`provider_circuits`, GAP-COST-06) tetap F4.
5. **Parser SSE** (WHATWG HTML §9.2.6): akhir baris LF/CR/CRLF (termasuk CRLF terbelah antar `Read`), BOM hanya di awal stream, komentar tidak pernah menjadi event (keepalive tidak me-reset watchdog, EC-PROV-009), `id` dengan NUL diabaikan, `retry` hanya digit ASCII (≤ 12). Event terakhir tanpa baris kosong dibuang; provider menilai `stream_broken` dari `finish_reason` yang belum terlihat. Batas satu baris dan data satu event = 4 MiB (`ErrEventTooLarge`, sticky). Slice event menunjuk buffer parser sampai `Next` berikutnya; 0 alloc/event setelah warmup (`TestSSEZeroAlloc`, `BenchmarkSSEParse`); `FuzzSSEParse` diferensial terhadap implementasi referensi naif dan tiga pola pemotongan baca.
6. **Penjaga dial (PR 2).** Satu `http.Transport` per instance jaringan (pooling, `TestConnReuse`). Alamat yang benar-benar di-dial diperiksa di `net.Dialer.ControlContext` (setelah resolusi DNS, anti DNS rebinding): binary test (`testing.Testing()`) dan mode `offline` hanya mengizinkan IP loopback (E-69 `TestNoNetwork`, CROSS-CUTTING §4); pelanggaran berkelas `offline_mode`. Egress berlabel `background` saat `network.background = false` ditolak `egress_disabled` (kelas `offline_mode`, GAP-OPS-02). Kegagalan sebelum koneksi terbentuk dibedakan dari putus di tengah request (EC-PROV-030; keduanya `network`). Egress `tool` memakai transport terpisah tanpa proxy yang hanya mendial IP publik (tolak loopback, privat RFC 1918/ULA, link-local termasuk metadata 169.254.169.254, CGNAT 100.64/10, unspecified, multicast, broadcast; IPv4-mapped di-unmap) → `blocked_host` (kelas `bad_request`, SPEC §14.4); transport terpisah mencegah koneksi pool inference ke host lokal dipakai ulang oleh egress tool. Di binary test, egress `tool` ke loopback diizinkan (semua dial memang loopback-only) agar tool dapat diuji dengan `httptest`. Pesan error transport tidak memuat URL (query/path bisa memuat credential), hanya nama egress, metode, dan host. Status non-2xx = `*StatusError` (body ≤ 64 KiB untuk `error.code` provider, `Retry-After` detik atau HTTP-date).
7. **Watchdog stream (PR 2, GAP-REL-07).** TTFB = 25 s + 5 s per 32K token input, maks 90 s; idle antar event 60 s; hanya event SSE yang di-dispatch yang me-reset idle. Kedaluwarsa = kelas `timeout`.
8. **DLP + log (PR 3; direvisi setelah [`REVIEW-F1-SEC1.md`](REVIEW-F1-SEC1.md) NETX-2..14).** Request berlabel `tool` di-scan atas semua yang dikirim ke wire: URL (userinfo, host, path, query, fragment), `Host`, header, trailer, dan body. Body dibaca **sekali** (≤ 8 MiB; lebih → `dlp_limit`, fail-closed) lalu dipasang ulang dari byte yang di-scan (yang dikirim = yang di-scan). Kanonisasi: lapisan mentah + ≤ 3 lapisan percent/form-decode (`%XX` hex huruf besar/kecil, `+` = spasi) sampai stabil; setiap lapisan dicocokkan dengan pola §14.3 (`redact`) dan nilai secret persis secara case-insensitive ASCII (hex huruf campuran, uppercase), dengan CR/LF dibuang (base64 MIME terbungkus) bila tidak ada bentuk secret yang memuatnya. Bentuk secret: mentah (panjang berapa pun), hex, escape URL kanonik, escape string JSON (dengan/tanpa escape HTML), base64 std/url di 3 alinyemen byte (substring grup 4-char berisi byte secret saja, ≥ 8 char). Matcher: indeks prefix 8 byte di belakang prefilter bitset 2^20 (tanpa Redact penuh + salinan). Batas kerja: decode tidak pernah memperbesar lapisan, sehingga kerja ≤ (1 + 3) × panjang input; pola §14.3 dijalankan atas setiap *run* non-whitespace utuh yang memuat byte hasil decode non-spasi (pola `jwt`, `url_password`, dan semua badan token punya konteks tanpa batas atas, tetapi hanya berupa byte non-whitespace; satu-satunya pola yang memuat spasi — header `authorization` dan PEM `-----BEGIN` — kepalanya terbatas, dan bila salah satunya ada di lapisan itu, atau daftar posisi melebihi len/16, seluruh lapisan dipindai; run-run itu dipindai dalam satu panggilan, digabung dengan `\n`, karena tidak ada pola lain yang cocok melintasi whitespace), REVIEW-F1-SEC3 NETX-26, diuji diferensial terhadap pemindaian penuh setiap lapisan (`TestDLPPatternDifferential` 20.000 kasus, `FuzzDLPPatterns`); sedangkan matcher secret hanya dipindai di sekitar byte hasil decode yang bisa menjadi bagian kemunculan secret — byte yang ada di salah satu bentuk ter-fold, atau CR/LF yang dibuang fold (menyambung tetangganya); byte lain (spasi dari `+`, `,`, `;`, … bila tidak ada di bentuk mana pun) memutus setiap kemunculan — dengan jendela ± panjang bentuk secret terpanjang dihitung dalam byte **setelah folding** (NETX-26, NETX-31: batas minimum 256 B hanya untuk pola dan sudah tidak dipakai), karena kemunculan yang tidak menyentuh byte itu sudah dicek di lapisan sebelumnya (REVIEW-F1-SEC1 NETX-15/16). Diuji diferensial terhadap pemindaian penuh (`TestDLPSecretDifferential`, 30.000 kasus; dua mutan aturan byte relevan tertangkap). Batas fail-closed = ≤ 256 KiB byte verifikasi bentuk panjang per request (banjir tabrakan prefix adversarial) → `dlp_limit`; secret pendek (< 8 B, kunci 2 byte yang sering muncul di prosa) memakai anggaran terpisah 256 KiB + 8 B per byte input (NETX-27), sehingga input sah tidak ditolak karena kerja. Daftar kandidat escape `int32`, ≤ len/16 entri; lebih → lapisan berikutnya diperiksa penuh (NETX-28: body 8 MiB berisi `%` tidak lagi mengalokasi ratusan MiB). Ukuran (`BenchmarkDLPScan`, 8 KiB; korpus `plain`, `pct` = body form dengan `+`/escape + satu redirect_uri ter-encode dua kali, `prefix`/`decode` adversarial; secret `long50`, `long200`, `short50`; runner CI AMD EPYC 7763, job `perf`): plain 15,8–21,7 µs, pct 65,2–73,6 µs, prefix 18–26 µs, decode 99–110 µs. Budget 50 µs (×1,5 = 75 µs di runner bersama, ADR-077) untuk `plain`/`pct`; batas 200 µs untuk input adversarial. Ditegakkan `timing.Check` di `BenchmarkDLPScan` lewat `make perf` (`PERF_BENCH`) di setiap PR, dan diukur ulang di mesin referensi Q14 saat acceptance. Pengoptimalan (REVIEW-F1-SEC1 NETX-16; deteksi diuji diferensial terhadap pemindaian penuh oleh `TestDLPLongContext` setelah NETX-26): prefilter pasangan byte di `redact` (#29), prefilter 4-gram stride 4 di atas indeks prefix 8 byte, probe 2-gram stride untuk secret pendek, decode lapisan ≥ 2 hanya di posisi kandidat, buffer di-pool (0 alokasi). Hit → `secret_egress`, keduanya kelas `bad_request`, tidak dikirim; pesan error dan log request yang diblok tidak memuat host/path/query (`[blocked]`); log request lain me-redaksi host dan path dengan pola + nilai secret. `Purpose` nol tidak valid (`ErrPurpose`), sehingga klien tanpa purpose tidak melewati DLP diam-diam. Set secret diganti atomik lewat `Network.SetSecrets` (vault/OAuth F2). Di F1 semua header `tool` di-scan; pengecualian header auth runtime ke host pemilik credential ditambahkan bersama tool pertama yang membutuhkannya (F6 `web_search`) lewat ADR. Sisa risiko yang dicatat (NETX-8/9/12): entitas HTML, escape `\uXXXX`, base32, gzip/kompresi, transformasi kustom tidak dikanonisasi; nilai vault pendek (mis. `admin`) memblok URL yang memuatnya sebagai substring (fail-closed, terlihat sebagai `secret_egress`); event `dlp_blocked` + `blocked_host{reason}` typed ditunda ke F2 (bersama vault). Log DEBUG request/response = metadata (metode, host, path ter-redaksi tanpa query, status, durasi, ukuran) — tanpa body (ADR-071 butir 5). SSRF (NETX-14): IPv4 tertanam di NAT64 `64:ff9b::/96`, IPv4-compatible `::/96`, dan 6to4 `2002::/16` diperiksa sebagai IPv4; `0.0.0.0/8`, `192.0.0.0/24`, `198.18.0.0/15`, `240.0.0.0/4`, `fec0::/10` non-publik.
9. **`netx/jsonrpc` ditunda ke F3** (pemakai pertama ACP); sisa budget netx dicadangkan untuknya.
10. **Kebijakan redirect (diamandemen 2026-10-20, REVIEW-F1-SEC3 PROV-M4).** `Client` Inference tidak pernah mengikuti redirect (`CheckRedirect` → respons 3xx dikembalikan): header kredensial `Route.Headers` (`api-key`, `x-api-key`) dan body prompt tidak boleh sampai ke host lain, dan Bearer tidak boleh turun ke http. Purpose lain hanya mengikuti GET/HEAD, ≤ 10 hop, tidak pernah https → http, dan saat host berubah semua header kecuali `Accept`/`Accept-Language`/`User-Agent` dibuang (net/http hanya membuang `Authorization`/`Cookie`). Ditolak: mengikuti redirect same-host untuk Inference — tidak ada provider yang membutuhkannya dan downgrade skema tetap mungkin. Test: `TestRedirectPolicy`, `TestRedirectChecks`.

**Dampak terukur (SPEC §20).** Token 0. Latensi: parser SSE ≈ 0,15 µs/event, 0 alloc (E-35). LOC: PR 1 ≈ 500 dari budget `netx` 1.800. Binary: belum di-link ke `damnbot` sampai `provider` F1. Dependency 0.

**Konsekuensi.** (+) Satu kebijakan retry teruji matriks penuh (14 kelas × 3 jitter) dengan waktu palsu; perilaku tepi P6 yang tidak tertulis kini eksplisit. (−) `Retry-After` > 30 s langsung memindahkan beban ke fallback/rotasi; error tak dikenal tidak pernah di-retry (lebih konservatif dari hermes).

## ADR-095 — `internal/schema`: semantik subset, JSON ketat, mode MCP, jalur dingin `encoding/json`

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; opsi paling konservatif dalam budget, tanpa dependency baru). Melengkapi ADR-008 dan SPEC §4.4/P3 untuk hal yang tidak ditetapkan teks.

**Aturan penerapan.** Diterapkan di `internal/schema` lewat tiga PR berurutan (E-84, ≤ 600 baris Go non-test per PR): (1) tokenizer ketat `scan.go`/`walk.go`/`errors.go`; (2) compiler `compile.go`/`decode.go`; (3) validator `validate.go`/`object.go`/`equal.go` + JSON-Schema-Test-Suite + `FuzzToolArgs`. Test bernama di setiap butir.

**Keputusan.**
1. **Semantik keyword = draft 2020-12** untuk subset SPEC §4.4: `integer` menerima angka berpecahan nol (`1.0`, `1e2`); `minLength`/`maxLength` menghitung code point Unicode (bukan byte/UTF-16); `enum: []` sah dan tidak cocok dengan nilai apa pun; `type` boleh string atau array; `enum`/`const` memakai kesamaan JSON (angka menurut nilai, objek tanpa urutan, tanpa lintas tipe); `required` tanpa entri `properties` diatur `additionalProperties`; `default`/`description` diabaikan saat validasi. Bilangan = float64; di luar jangkauan → ±Inf (bukan `integer`, gagal batas berhingga). `pattern` = RE2 (`regexp`), tidak berjangkar, bukan ECMA-262 (`\s` RE2 = ASCII; lookaround tidak didukung). Test: `TestJSONSchemaTestSuite`, `TestEnumEquality`, `TestValidate`.
2. **JSON ketat, fail-closed.** Selain RFC 8259 dan kunci duplikat (SPEC §4.4, juga bila sama setelah unescape): UTF-8 tidak valid dan escape surrogate UTF-16 tunggal (`\ud800`) = syntax error (tidak diganti U+FFFD seperti `encoding/json`); nesting > `MaxDepth` = 64 = error `KindDepth` (juga untuk schema). Error sintaks di mana pun dalam dokumen didahulukan atas pelanggaran keyword. Test: `TestCheck`, `TestJSONNoDuplicateKeys`, `TestValidateNoDuplicateKeys`, `FuzzJSONCheck`.
3. **Pesan model-facing stabil.** Satu error pertama (deterministik): `invalid JSON at byte <n>: <alasan>` atau `<JSON Pointer RFC 6901 | (root)>: <alasan>`; `additionalProperties:false` menyebut ≤ 20 properti yang diizinkan (urut abjad). Semua error `errors.Is(err, schema.ErrInvalid)` (dipetakan `invalid_args` oleh pipeline tool); `*schema.Error` membawa `Kind`/`Offset`/`Pointer`/`Keyword`. Pesan dikunci di tabel `TestValidate` (golden E-68).
4. **`ModeInternal` vs `ModeMCP`.** Internal: keyword di luar subset, `anyOf`/`oneOf`, `items` bentuk tuple, dan `pattern` non-RE2 = compile error (`CompileError.Unsupported`; `TestSchemaKeywordsSupported`). MCP: keyword di luar subset diabaikan dan didaftar `Schema.Partial()` (→ `schema_partial`); anotasi murni (`title`, `$schema`, `$id`, `$comment`, `examples`, `deprecated`, `readOnly`, `writeOnly`) tidak menandai partial; `oneOf` ditegakkan sebagai `anyOf` + partial (lebih lemah dari deklarasi, tidak pernah menolak data yang sah); `items` tuple, `pattern` non-RE2, `$ref`/`$defs`, `format`, dst. diabaikan + partial. Resolusi `$ref` lokal = urusan modul `mcp` F5 (ADR baru bila butuh LOC di luar budget). Nilai malformed untuk keyword yang dikenal (`type: "any"`, `minLength: -1`, `required` duplikat, …) = schema ditolak di **kedua** mode (tool tidak didaftarkan). Test: `TestCompileMCPPartial`, `TestCompileInvalidSchema`, `TestAnyOf`.
5. **Jalur panas tanpa reflect, jalur dingin `encoding/json`.** Validasi berjalan satu kali di atas tokenizer sendiri tanpa membangun nilai: 0 alloc/op pada jalur sukses (`BenchmarkValidate`, argumen 1 KiB ≈ 1,8 µs, E-37). Compile schema dan `enum`/`const` untuk instance non-string men-decode dengan `encoding/json` (`UseNumber`) **setelah** dokumen lolos pemindaian ketat (paket tidak mengimpor `reflect`, E-34). Scratch per panggilan di `sync.Pool` milik `*Schema` (bukan global); referensi ke buffer pemanggil dibuang sebelum dikembalikan ke pool (`TestValidateDoesNotRetainInput`).
6. **JSON-Schema-Test-Suite sebagai data.** Commit `55719286c33ae0d1823bfb65fea10338a19bc37c` (2026-10-13), `tests/draft2020-12/` 17 file (satu per keyword subset + `boolean_schema`, `default`), lisensi MIT di `third_party/manual/json-schema-test-suite.txt`; asal dan SHA-256 per file di sidecar `internal/schema/testdata/jsts/ORIGIN.md`, diverifikasi `TestAttributionHeaders` (ADR-099). Grup yang memakai keyword di luar subset dilewati dan dihitung; ≥ 344 kasus wajib dieksekusi (`suiteMinExecuted`).

**Dampak terukur (SPEC §20).** Token 0 (paket belum dipakai prompt). Latensi: validasi 1 KiB ≈ 1,8 µs, 0 alloc (budget E-37 ≤ 20 µs/≤ 2 alloc). LOC `schema` 1.128 / 1.200 (scc). Binary: belum di-link ke `damnbot` (dipakai `tools` F1). Dependency 0 (suite = testdata).

**Konsekuensi.** (+) Validator dengan error presisi, kebenaran diuji suite resmi + fuzz diferensial terhadap `encoding/json`. (−) Dokumen dengan surrogate tunggal/UTF-8 rusak ditolak (model harus memperbaiki); `pattern` mengikuti RE2, bukan ECMA-262; sisa budget `schema` 72 LOC.

## ADR-098 — E-70: paket test-only dikeluarkan dari rasio test:source lewat allowlist `TESTONLY`

**Status:** Accepted 2026-10-19 (keputusan direktur proyek atas usulan pelaksana F1). Mengamandemen ADR-050 dan ENGINEERING E-70. Nomor ADR-098 semula dicadangkan untuk `internal/boundary`; paket itu tidak memerlukan deviasi, jadi bila kelak butuh ADR ia memakai nomor bebas berikutnya.

**Aturan penerapan.** Diterapkan (2026-10-19): `scripts/loc-check.sh` (mode `ratio` dan `budget`), `loc.budget` (baris `TESTONLY`), `internal/archtest/repometa_test.go` (`TestBudgetTableSum` memvalidasi baris `TESTONLY`), `scripts/ci-meta-test.sh` (gate `loc-ratio-testonly`, penanda `LOC GAGAL ratio`), ENGINEERING E-70.

**Konteks.** Bagian 3/3 `internal/schema` membuat Go non-test repo melewati 5.000 LOC, sehingga gate E-70 aktif pertama kali (ADR-050): rasio 6.753:5.091 = 1,33 > 1,20. Penyumbang utama `internal/archtest` (1.092 baris test : 66 baris kode) — paket test arsitektur tanpa kode produksi, yang tidak mengukur kecukupan test kode produksi mana pun. Tanpa paket test-only rasio = 1,08–1,13.

**Keputusan.**
1. **Allowlist eksplisit, bukan heuristik.** Paket test-only = direktori yang tercantum sebagai baris `TESTONLY <dir>` di `loc.budget`: `internal/archtest` dan `internal/testutil` (beserta subdirektorinya). Paket baru yang berat test tetap masuk rasio sampai ADR menambahkannya ke allowlist. `TestBudgetTableSum` menolak baris `TESTONLY` yang menunjuk direktori di luar infra non-produksi (ADR-086 NEW-34: `internal/testutil`, `internal/archtest`, `tools/analyzers`), sehingga paket produksi tidak bisa dikecualikan.
2. **Dikecualikan dari rasio dan dari ambang 5.000 LOC; tetap dihitung di budget.** `make loc` tetap menghitung LOC paket itu terhadap baris budget-nya dan total P21; `make loc-ratio` mencetak satu baris `LOC ratio dikecualikan (TESTONLY, ADR-098) <dir> test:source = a:b` per direktori.
3. **Penanda kegagalan.** Gate rasio yang gagal mencetak `LOC GAGAL ratio <r> di luar 0,80–1,20 (E-70)`; meta-test `loc-ratio` dan `loc-ratio-testonly` mencocokkan penanda itu (sebelumnya baris laporan yang juga muncul saat lulus).
4. **Meta-test.** `loc-ratio`: pohon sintetis 5.000:5.000 lulus, +2.000 baris test gagal (ADR-050, tetap). `loc-ratio-testonly`: pohon yang sama + paket berat test (1:2.000) di `internal/archtest` lulus; isi yang sama di paket non-allowlist `internal/metaheavy` gagal.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC produksi 0. Rasio repo saat ini (main + `schema` 3/3): 5.529:4.890 → laporan saja sampai Go non-test produksi ≥ 5.000 LOC.

**Konsekuensi.** (+) E-70 mengukur test kode produksi; paket baru tidak bisa lolos diam-diam. (−) Ambang 5.000 LOC tercapai sedikit lebih lambat (infra test-only ≈ 200 LOC tidak dihitung).

## ADR-104 — Artefak review (`docs/REVIEW-*.md`) dikecualikan dari spec-guard

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; opsi paling sempit). Melengkapi ADR-078 AUD-056 (laporan bukan spec).

**Konteks.** Auditor independen menulis temuan review paket F1 di `docs/REVIEW-<topik>.md` (mis. `REVIEW-F1-SEC1.md`, PR #23). Dokumen itu laporan temuan + bukti, bukan requirement, tetapi spec-guard (`scripts/pr-meta.sh`) memperlakukan setiap `docs/*.md` baru sebagai perubahan spec yang wajib merujuk ADR. `docs/AUDIT-*.md` dan `docs/F[0-9]-REPORT.md` sudah dikecualikan dengan alasan yang sama. `docs/REVIEW-1.0.md` (review spec historis) sudah dikecualikan sebagai seluruh file.

**Keputusan.** `testdata/docs_exclusions.txt` menambah pola seluruh-file `docs/REVIEW-*.md`. Temuan review yang mengubah requirement tetap wajib diterapkan lewat ADR + perubahan dokumen spec (spec-guard tetap berlaku untuk dokumen itu).

**Dampak terukur (SPEC §20).** Token/latensi/LOC/binary/dependency 0. Satu pola pengecualian tambahan.

**Konsekuensi.** (+) PR auditor docs-only lolos spec-guard tanpa ADR per review. (−) Isi `docs/REVIEW-*.md` tidak pernah normatif; requirement di dalamnya baru berlaku setelah diangkat ke spec lewat ADR.

## ADR-096 — `internal/redact`: penahanan stream fail-closed, pola tanpa jangkar, sumber secret tanpa batas panjang

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; untuk setiap celah dipilih opsi paling konservatif = paling sedikit byte secret yang mungkin keluar, tanpa dependency baru). Melengkapi SPEC §14.3, ADR-071 butir 2–4, dan mengamandemen ADR-081 butir 2 (aturan mulai menahan).

**Aturan penerapan.** Diterapkan di `internal/redact` lewat dua PR berurutan (E-84, ≤ 600 baris Go non-test per PR): (a) pola + matcher + API whole-blob `patterns.go`/`match.go`/`redact.go`; (b) penyaring stream `stream.go`. Satu matcher dipakai keduanya, jadi output stream = output whole-blob untuk pemotongan chunk apa pun selama satu tahanan tidak mencapai batas 4 KiB. Tanpa `regexp`: setiap pola = scanner tulisan tangan dengan biaya per byte terbatas (EC-SEC-008). Review keamanan independen sebelum merge (F1-REPORT §"Review keamanan").

**Keputusan.** Setiap butir menyebut opsi yang ditolak.
1. **Prefix literal parsial ditahan.** Akhir chunk yang merupakan awal prefix literal pola (`gh`, `s`, `sk`, `-----BEG`, `:`, `:/`, …) ditahan, bukan hanya setelah prefix lengkap (ADR-081 butir 2). Ditolak: menahan hanya setelah prefix lengkap — chunk yang berakhir di tengah prefix mengirim prefix itu, lalu badan token di chunk berikutnya tidak lagi cocok dan bocor utuh. Test: `TestStreamRedactSplit` (setiap offset), `TestStreamHoldMinimal`.
2. **Prefix secret yang dikenal ditahan sejak 1 char** (ADR-081: ≥ 4). Ditolak: ambang 4 — sampai 3 byte awal secret keluar sebelum kecocokan diputuskan. Biaya terukur: tahanan 1–2 byte pada 115/200 respons natural; first token p95 ≈ 1,2 µs ≪ 1 ms (`TestFirstTokenLatency/redact`, ditegakkan lewat `testutil/timing`).
3. **Pola tanpa jangkar, persis teks SPEC.** Ditolak: batas kata `\b` — token setelah escape (`\n`, `%3D`, `=`) tidak lagi cocok. Konsekuensi: false positive pada slug seperti `risk-assessment-…` (`sk-` + ≥ 20 char token) diterima dan dilaporkan (kategori `slug` di `TestRedaction/negative`, tidak di-gate); kategori lain (hash git SHA-1/SHA-256, UUID, base64, base64url, look-alike; ≥ 300 string) wajib 0.
4. **Badan berpanjang tetap `{n}` dicocokkan sebagai `{n,}`** (`ghp_`, `npm_`, `AKIA`, Telegram `{35}`, …). Ditolak: panjang persis — run yang lebih panjang tidak cocok sama sekali (regex tanpa jangkar akan mencocokkan 36 char pertama dan membiarkan ekornya); di sini seluruh run diredaksi.
5. **Telegram: hanya bagian `:token` yang diganti; ID bot (≥ 8 digit, tanpa batas atas 10) tetap terlihat.** ID bot bukan kredensial (publik lewat `getMe`); kredensial = bagian setelah `:`. Mengikuti aturan tahan ADR-081 butir 2 (menahan setelah ≥ 8 digit diikuti `:`), sehingga run digit biasa tidak pernah ditahan; whole-blob memakai aturan yang sama agar output identik. Ditolak: mengganti ID juga — menuntut penahanan setiap run digit ≥ 8 (latensi pada teks numerik) tanpa melindungi kredensial tambahan.
6. **Userinfo URL tanpa cek skema dan tanpa batas panjang (diamandemen 2026-10-19, REVIEW-F1-SEC1 R-02; mengamandemen regex userinfo SPEC §14.3/ADR-071).** `://` cukup sebagai jangkar (skema apa pun, termasuk yang ditulis salah), begitu pula bentuk JSON-escaped `:\/\/` (`https:\/\/app:pw@db`). Bentuk efektif: `://[^/\s:@]*:\S+@` dengan password berjalan sampai `@` **terakhir** sebelum whitespace: user boleh kosong (`redis://:pw@cache`), password boleh memuat `/` dan `@` (`postgres://app:pa/ss@db`, `postgres://app:p@ss@db` → `p@ss` diredaksi utuh). Ditolak: regex SPEC asli `[^/\s:@]+:[^@\s/]+@` — keempat bentuk tadi lolos sebagian atau utuh. Konsekuensi: false positive bila host:port diikuti `@` tanpa whitespace di antaranya (`http://h:8080/p@v1` → `8080/p` diredaksi), diterima sesuai prinsip paling konservatif. Bagian user dan password masing-masing tanpa batas atas (sebelumnya diusulkan 255/2048 — password lebih panjang akan lolos utuh). Bagian user ikut dipindai, sehingga token yang dipakai sebagai nama user (`https://ghp_…:x@`) juga diredaksi. Linear karena setiap kandidat dimulai di `://`, run user tidak memuat `/`/`:`, dan percobaan password yang gagal (tanpa `@` sampai whitespace) membuat semua kandidat lain di run yang sama dilewati (memo `skip`). Stream menahan kandidat `://user:` sampai whitespace. Run userinfo yang belum berakhir saat tahanan stream mencapai 4 KiB mengikuti butir 9: kandidat diredaksi dan sisa run `\S` dibuang sampai whitespace — fail-closed, dengan konsekuensi teks tanpa whitespace (mis. JSON minified setelah `http://host:port`) ikut terbuang sampai whitespace berikutnya. Test: `TestRedaction/url_password_forms` (R-02), `TestKnownSecrets` (user 300 B, password 3.000 B), `TestEdgeCases/EC-SEC-008`.
7. **Header Authorization: skema `Bearer`, `Basic`, `token`, huruf apa pun, framing JSON/dict Python/map Go.** Nilai berakhir di whitespace atau tanda kutip, sehingga JSON tetap valid. Tidak membocorkan token sah: karakter b64token (RFC 6750 §2.1) dan token68 (RFC 7617) tidak memuat kutip. Ditolak: `\S+` persis — menelan kutip penutup dan merusak struktur JSON log tanpa manfaat keamanan.
8. **PEM:** label yang berakhir `PRIVATE KEY` dan `PRIVATE KEY BLOCK` (PGP) diterima.
9. **Batas tahanan 4 KiB fail-closed.** Kandidat yang masih terbuka saat tahanan mencapai `holdCap` = 4.096 B diredaksi, dan sisa token dibuang saat tiba (ADR-071 butir 3). Untuk PEM, semua isi sampai baris END dibuang; ekor yang disimpan untuk mendeteksi END dibatasi 78 B; END tidak pernah datang = semua sisa stream dibuang. Setiap pola punya set `drop`; tanpa itu potongan paksa `url_password` mendereferensi pointer nil (REVIEW-F1-SEC1 S-01). Test: `TestStreamHoldCap`, `TestStreamHoldCapPerPattern` (setiap pola bawaan dari `r.pats` didorong melewati 4 KiB dengan chunk 7/64 B: tanpa panic, tanpa bocor, tahanan terbatas; secret dikenal > 4 KiB = whole-blob; S-02), `TestStreamUserinfoHoldCap`/`TestStreamUserinfoHoldCapRepro` (repro S-01, chunk 64/1 B), seed `FuzzRedactStream` > 4 KiB dengan oracle "beda dari whole-blob hanya lewat potongan fail-closed bersentinel".
10. **Secret yang dikenal yang dimulai di dalam atau tepat setelah kecocokan lain memperluas kecocokan itu**, sehingga ekornya tidak tersisa (`ghp_…` + secret menempel).
11. **Tanpa panjang minimum untuk `Options.Secrets`.** `MinSecretLen` = 8 hanya menyaring nilai env di `SecretsFromEnv` (aturan env SPEC §14.3). Nilai keychain/`secrets.enc`/token OAuth diredaksi berapa pun panjangnya (kecuali string kosong). Ditolak: menerapkan ≥ 8 ke semua sumber — secret eksplisit yang pendek akan bocor. Konsekuensi: secret eksplisit yang sangat pendek meredaksi kemunculannya di teks biasa (pilihan konfigurasi pengguna).
12. **Prefix sekeluarga dari gitleaks ditambahkan:** `ASIA` (AWS STS), `gho_`/`ghu_`/`ghs_`/`ghr_` (GitHub), `xoxe-`/`xapp-` (Slack), Stripe `sk_prod_`/`rk_prod_`.

**Di luar lapisan ini.** Bentuk ter-encode (url-encoded, base64, hex) dari secret = GAP-SEC-16 di `netx`. Field tulis yang memuat sentinel `[REDACTED:` ditolak `invalid_args` oleh pemanggil lewat `ContainsSentinel` (ADR-071 butir 4).

**Korpus.** `testdata/gitleaks-v8.28.0.tsv`: 42 sampel positif sintetis per rule (`gitleaks.toml` v8.28.0, commit `4fb43823ef3d152d239e92d7d5cb04783b548062`, sha256 `c3bee40d87bb4689e3f5ffa5cc0315f7e6e4d43f4e9388397fca59f29407b432`, dicatat di header file dan di sidecar `testdata/ORIGIN.md`, ADR-099; notice `third_party/manual/gitleaks.txt`), termasuk 6 badan PEM penuh dari byte acak; `«»` di dalam prefix literal agar push protection GitHub tidak menandai file (dibuang loader). Ditambah 60 secret sintetis (seed tetap), tiap sampel diuji di 6 kanal (plain, JSON, log key=value, query URL, blok kode, pesan error).

**Dampak terukur (SPEC §20).** Token 0. Latensi: first token p95 ≈ 1,2 µs (budget ADR-081 1 ms); `BenchmarkRedact` 4 KiB ≈ 200 MB/s, 0 alloc; `BenchmarkStreamRedact` ≈ 215 MB/s, 0 alloc/Push. LOC `redact` 511 (scc), baris `sandbox+redact` 1.300. Dependency 0 (korpus = testdata).

**Konsekuensi.** (+) 0 kebocoran di korpus untuk setiap offset pemotongan; biaya linear untuk input patologis. (−) False positive pada slug ber-`sk-`; ID bot Telegram tetap terlihat; secret eksplisit pendek meredaksi teks biasa.

## ADR-101 — `internal/budget` F1: estimator COST-09 (batas kalibrasi 0,5–1,5, `est_input_tokens` mentah), biaya, ledger `ctxdb/ledgerstore`

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; opsi paling konservatif dalam budget `budget` 400 + `ctxdb/ledgerstore` 300, tanpa dependency baru). Menerapkan ADR-058/ADR-067 (estimator di F1) dan ADR-074 (storage ledger). Mengoreksi ADR-058 butir 1 (batas bawah).

**Aturan penerapan.** Teks diterapkan: SPEC §4.5 dan §22.1 GAP-COST-09 (0,7–1,5 → 0,5–1,5); DECISIONS ADR-058 (penanda). Kode: `internal/budget` (`estimate.go`, `ledger.go`), `internal/ctxdb/ledgerstore`, korpus `internal/budget/testdata/calibration/` (teks asli proyek, bukan turunan pihak ketiga) dengan hitungan o200k_base `o200k.tsv` yang diverifikasi `bench/budget` `TestCalibrationCorpusCounts`.

**Konteks.** Diukur atas korpus en/id/ru/ja (6 paragraf per bahasa, o200k_base): rasio estimasi statis/real = en 1,20–1,34, id 1,06–1,27, ru 1,59–2,01, ja 1,22–1,34. Rasio Kiril 2 char/tok (ADR-058) melebihi o200k (≈ 3,9 char/tok di prosa) ≈ 1,8×; dengan batas bawah 0,7 estimasi berkalibrasi bahasa Rusia tetap ≈ 1,13–1,3× real, sehingga acceptance ADR-058 (0,9–1,1) mustahil.

**Keputusan.**
1. **Batas faktor 0,5–1,5.** Rasio per script ADR-058 tetap (prior konservatif: sesi baru tidak pernah meng-under-estimate untuk tokenizer modern); hanya batas bawah kalibrasi diturunkan ke 0,5 sehingga data nyata boleh mengoreksi prior sampai 2×. Batas atas tetap 1,5.
2. **`est_input_tokens` = estimasi mentah** seluruh request (Σ char per script / rasio, tanpa jangkar dan tanpa faktor). Faktor = EMA `input_tokens / est_input_tokens` (α = 0,3 untuk sampel terbaru, sampel pertama sebagai nilai awal) atas ≤ 50 baris `purpose='turn'` terbaru dengan estimasi > 0, dihitung ulang saat sesi di-load; jendela incremental identik dengan derivasi ulang (`TestCalibrate`). Menyimpan estimasi berkalibrasi akan membuat rasio konvergen ke 1 dan menghapus kalibrasi.
3. **Kelas script.** Latin = ASCII + script Latin/Common/Inherited (4); CJK (Han, kana, Hangul, tanda baca CJK/fullwidth), rune di luar BMP (emoji), dan byte UTF-8 tidak valid = 1 char/tok; script lain (Kiril, Arab, Devanagari, Yunani, Ibrani, Thai, …) = 2.
4. **Biaya.** `usage.cost` provider (dibulatkan ke micro-USD) menang; selain itu tabel harga (micro-USD per juta token, `cached` = 0 berarti tanpa diskon → harga input), dibulatkan **ke atas**; tanpa harga → `cost_usd_micros` NULL, `cost_source='unknown'`, `pricing_version='none'`. Usage negatif atau `cached > input` → `ErrInvalidUsage`.
5. **Ledger.** `budget.Entry` dan `ledgerstore.Row`/`Sample` field-identik: komposisi mengonversi dengan konversi tipe Go biasa tanpa saling impor (matriks E-01: `budget` tanpa impor internal, `ledgerstore` hanya `ctxdb`). `ledgerstore.Insert` menulis di transaksi pemanggil (bersama event `llm_call`, P9); baca kalibrasi/total lewat `Store.Write` (satu-satunya API kueri multi-kolom `ctxdb`; dipanggil sekali saat sesi di-load). Guard budget P27 (cap, 80 %/100 %) tetap F2 sesuai ROADMAP.
6. **Batas plausibilitas dan aritmetika tercek (diamandemen 2026-10-20, REVIEW-F1-SEC3 LED-2/3).** `budget.Cost` menolak (`ErrInvalidUsage`, pemanggil fail-closed) jumlah token > `MaxTokens` (10¹²) per kolom, biaya provider > `MaxProviderCostUSD` (10⁶ USD per call, termasuk +Inf), harga negatif, dan biaya tabel harga yang overflow; perkalian tokens × µUSD/MTok memakai `bits.Mul64`/`Add64`, sehingga biaya tidak pernah membungkus ke negatif atau mendekati 0. Ditolak: saturasi ke `MaxInt64` — `SUM` di `SessionTotals` lalu overflow dan error di tempat yang lebih jauh dari sumbernya. Test: `TestCost` (1e14 USD, +Inf, 4e12 token, `MaxInt64/2`, 10²⁰ overflow, batas tepat). LED-1: `TestLedgerAppendOnly` kini mengembalikan error `UPDATE`/`DELETE` apa adanya dan memeriksa baris tidak berubah (mutasi trigger terdeteksi).

**Dampak terukur (SPEC §20).** Token model 0. Akurasi (`TestTokenEstimatorCalibration`, sesi 6 call, estimasi COST-09 vs o200k_base seluruh request): call ≥ 2 = 0,95–1,02 untuk keempat bahasa (call 1 tanpa kalibrasi 1,27–1,87, selalu over-estimate). `BenchmarkCount` ≈ 90 MB/s (teks campuran Latin/Kiril/CJK), 0 alokasi. LOC: `budget` + `ledgerstore` ≈ 230 dari 700. Binary/dependency 0.

**Konsekuensi.** (+) Estimasi per bahasa akurat setelah satu call; prior tetap aman untuk budget. (−) Faktor dapat memangkas estimasi sampai setengah bila provider memakai tokenizer jauh lebih efisien dari prior; `est_input_tokens` di ledger bukan estimasi yang dipakai runtime.

## ADR-102 — `internal/provider` F1: decoder delta tanpa reflect, adapter openai-chat, fallback, `providertest`

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; opsi paling konservatif dalam budget `provider` 2.000 LOC termasuk `providertest`, tanpa dependency baru). Melengkapi SPEC §4.1/§4.1.1, ENGINEERING E-36/E-69, ADR-072/082 untuk hal yang tidak ditetapkan teks.

**Aturan penerapan.** Diterapkan lewat PR berurutan (E-84 ≤ 600 baris Go non-test per PR): (1) decoder delta (`jscan.go`, `delta.go`); (2) tipe + adapter openai-chat; (3) rute/fallback/breaker + `providertest` + entri EC-PROV F1.

**Keputusan.**
1. **Decoder delta (E-36).** Pemindai JSON sendiri atas frame SSE (tanpa `encoding/json` reflektif, tanpa `reflect`): kunci dicocokkan **persis** (berbeda dari `encoding/json` yang case-insensitive), `null` di posisi objek/array = kosong, UTF-8 tidak valid dan surrogate tunggal → U+FFFD (sama dengan `encoding/json`), angka mengikuti tata bahasa RFC 8259; hitungan token wajib bilangan bulat ≥ 0 ≤ 2^53, selainnya chunk ditolak. Hanya `choices[0]` yang dibaca (n = 1). String tanpa escape menunjuk frame; yang ber-escape di-unescape ke buffer scratch berkapasitas ≥ panjang frame (tidak pernah realokasi di tengah decode), sehingga 0 alloc/chunk setelah warmup (`TestDeltaDecodeZeroAlloc`, termasuk chunk tool call dan usage). `FuzzDeltaDecode` diferensial terhadap `encoding/json` (pencocokan kunci persis): valid ⇔ diterima, nilai sama. Chunk yang tidak bisa di-decode di tengah stream 2xx = kelas `stream_broken` (retryable).
2. **Nama field `Error`.** `provider.Error` memakai field `Delay` untuk Retry-After karena method `RetryAfter()` (antarmuka `retry.ServerHint`) tidak boleh bernama sama dengan field; field lain sesuai SPEC §4.1 ditambah `Param` (`error.param`).
3. **Rakitan tool call (EC-PROV-001).** Fragmen masuk ke call dengan `id` yang sama bila ada; selain itu ke call ber-`index` sama; fragmen tanpa `id` dan tanpa `name` dengan `index` baru disambung ke call terakhir; selainnya call baru. Argumen disimpan mentah (gate P3).
4. **Finish.** `tool_calls`/`function_call`/`tool_use` → `tool_calls`; `length`/`max_tokens` → `length`; `content_filter` tetap; nilai lain non-kosong → `stop`. Stream yang berakhir (EOF atau `[DONE]`) tanpa `finish_reason` → `ErrStreamBroken` (kelas `stream_broken`). `finish=stop` tanpa konten dan tanpa tool call (termasuk reasoning-saja) → `ErrEmptyResponse` (kelas `empty_response`, tidak di-retry di `netx/retry`; satu call ulang dimiliki loop, EC-PROV-007).
5. **Reasoning (EC-PROV-004/005/006).** Nilai reasoning (`reasoning_content` atau `reasoning`) disimpan apa adanya per pesan assistant (`Message.Reasoning`, nama field asli) dan dikirim balik di field yang sama saat replay; delta string digabung menjadi satu string JSON, potongan non-string (array/objek) disimpan sebagai array JSON potongan. `ReasoningText` = teks gabungan (string + field `text`/`thinking`) untuk tampilan.
6. **Token kontrol (EC-PROV-013).** Hanya di **akhir** konten final, berulang: `<|im_end|>`, `<|endoftext|>`, `<|eot_id|>`, `<|end|>`, `</s>` (beserta spasi di sekitarnya) dibuang; kemunculan di tengah teks tidak diubah.
7. **Parameter ditolak (EC-PROV-011/012).** Hanya dari field terstruktur `error.param` ∈ {`temperature`, `reasoning_effort`, `top_p`} pada 400: parameter itu ditandai tidak didukung untuk rute (provider+model, in-process) dan request dikirim ulang **sekali** tanpa parameter itu (bukan retry backoff; pola call ulang yang diizinkan di luar `netx/retry`, ADR-078 AUD-045). Pesan teks error tidak pernah diparse (E-21); endpoint yang tidak mengisi `param` tetap gagal `bad_request`.
8. **Fallback (P6/P7).** Rantai = model primer lalu `provider.fallback` (≤ 2, lebih → error konfigurasi). Setiap call **mulai dari primer** (EC-PROV-016: sesi kembali ke primer begitu pulih; circuit open primer → langsung ke fallback tanpa request). Fallback hanya untuk kelas retryable setelah attempt habis; kandidat tanpa rute terkonfigurasi dilewati (EC-PROV-015) dan kandidat dengan `context_window` < estimasi mentah request (ADR-101) dilewati (EC-PROV-025). Pindah model dilaporkan lewat `Response.FallbackFrom` + callback `OnFallback` (event `provider_fallback`, P7). Breaker `netx/retry` per (provider, credential).
9. **`providertest` (E-69).** Server palsu OpenAI-compatible di `httptest` loopback: skrip balasan berurutan (status, header, body, atau frame SSE mentah/fixture rekaman), rekaman request yang diterima, dan latensi per frame dari `Clock` injeksi (`FakeClock` maju manual), sehingga test tidak memakai jam dinding.
10. **Batas rakitan & antarmuka.** Endpoint dianggap tidak tepercaya: konten + reasoning + nama/argumen tool call yang dirakit ≤ 16 MiB dan ≤ 128 tool call per respons, lebih → `ErrResponseTooLarge` (kelas `unclassified`, tidak di-retry, tidak memicu fallback); satu event SSE ≤ 4 MiB (ADR-100 butir 5); error di dalam stream 2xx = kelas `server`; frame JSON rusak = `stream_broken`. Nama dan argumen tool call tidak ditafsirkan di `provider` (lookup + gate P3 di `tools`). `Client.Do` menerima `*Request` (bukan nilai seperti sketsa SPEC §4.1) agar request besar tidak disalin per call; semantik sama.
11. **Waktu tunggu retry yang diinjeksi.** `retry.Policy.Wait` (nil = timer nyata) dan `provider.Config.Wait` meneruskannya; dipakai test di atas socket loopback nyata yang tidak bisa berjalan di bubble `synctest` (backoff dicatat, tidak ditunggu). Produksi tidak mengisinya.
12. **Nesting dibatasi 10.000 tanpa rekursi (diamandemen 2026-10-20, REVIEW-F1-SEC3 PROV-B1).** `jscan` melacak kedalaman absolut (`object`/`array`/`skip`); level ke-10.001 = `errJSON`, sama dengan `maxNestingDepth` encoding/json sehingga `FuzzDeltaDecode` tetap diferensial. `skip` iteratif (satu bit per level, word pertama tanpa alokasi), jadi frame SSE 4 MiB berisi `[` tidak memakan stack (sebelumnya rekursi tanpa batas: stack overflow fatal di arm64, stack 512 MiB di amd64). Ditolak: batas lebih kecil — mengubah hasil diferensial terhadap encoding/json tanpa manfaat keamanan tambahan. Test: `TestDeltaDeepNesting` (batas, 10.001, frame 4 MiB array/objek; job test linux/arm64), seed fuzz 10.000/10.001.
13. **Batas biaya rakitan terhadap endpoint jahat (diamandemen 2026-10-20, REVIEW-F1-SEC3 PROV-M1..M5).** (a) Nama tool dirakit dengan `append` ke `[]byte` dan dibatasi `maxToolName` = 256 B (> 256 → `ErrResponseTooLarge`; OpenAI membatasi 64 char), bukan konkatenasi string kuadratik. (b) Satu chunk memuat ≤ 256 entri `tool_calls` dihitung per entri yang didekode, bukan per indeks array, sehingga key `tool_calls`/`delta`/`choices` berulang tidak mereset batas (`ErrTooManyToolDeltas` → `ErrResponseTooLarge`, tanpa retry/fallback), dan setiap fragmen dibebankan 64 B ke `maxResponseBytes`, sehingga `{}` kosong tidak gratis. Konsekuensi terukur: argumen yang di-stream satu token per fragmen (≈ 4–8 B + 64 B beban) dibatasi sekitar 0,9–1 MiB per respons; argumen tool sah jauh di bawahnya (REVIEW-F1-SEC3 N3). (c) `maxResponseBytes` menghitung setiap byte yang disimpan untuk reasoning: teks, part yang disimpan, dan nilai replay (panjang ter-quote encoding/json, `quotedLen`, termasuk ekspansi ×6 `\u00XX`); array part dirakit sekali tanpa encode ulang (byte-identik dengan part yang diterima). (d) Redirect tidak pernah diikuti untuk egress Inference (ADR-100 butir 10); 3xx = `bad_request` non-retryable. (e) `OnFallback` dipanggil sebelum setiap percobaan fallback (`Done` false: prompt akan dikirim ke `To`) dan sesudahnya (`Done` true, `Outcome`), sehingga fallback gagal tetap terlihat (P7). Ditolak: event hanya saat sukses — prompt sudah terkirim ke provider kedua tanpa jejak. Test: `TestToolNameBound`, `TestEmptyToolFragments`, `TestReasoningPartsBound`, `TestQuotedLen`, `TestRedirectNotFollowed`, `TestFallbackFailedVisible`.
14. **Kunci duplikat = last-wins seperti `encoding/json` (errata 2026-10-21, crash `FuzzDeltaDecode` CI run 39558102158, REVIEW-F1-SEC3 PROV-m1).** Butir 1 menjanjikan "valid ⇔ diterima, nilai sama" terhadap `encoding/json`, tetapi decoder memproses setiap kemunculan kunci yang dikenal: nilai pertama yang salah tipe langsung menolak chunk (`{"choices":[{"delta":0,"delta":{}}]}` → "malformed JSON chunk at byte 21", padahal `encoding/json` menerimanya) dan nilai berulang digabung. Kini, per objek: nilai **terakhir** dari kunci yang sama menang dan **menggantikan** semua field yang di-decode nilai sebelumnya (`delta`/`message`, `content`, `tool_calls`, `choices`, `usage`, `function`, …); nilai sebelumnya cukup JSON yang well-formed. Nilai salah tipe di-rewind (kursor, kedalaman, scratch) lalu di-skip dan ditandai satu bit per kunci; kemunculan berikutnya yang sah menghapus bitnya, dan objek gagal bila bit tersisa (`jscan.settle`/`unresolved`). JSON rusak tetap ditolak di mana pun. Diselaraskan sekaligus dengan referensi diferensial: `message` mengalahkan `delta` di mana pun posisinya; bila `reasoning_content` dan `reasoning` sama-sama ada, `reasoning` diterapkan terakhir; `index: null` = tanpa index. Batas 256 entri `tool_calls` per chunk (butir 13b) tetap dihitung lintas duplikat, bukan per array yang menang. Tetap 0 alloc/chunk; biaya jalur panas ≈ +6 % `BenchmarkDeltaDecode` (A/B berselang di VPS yang sama: ≈ 683 → 727 ns/op). Ditolak: menolak kunci duplikat — bertentangan dengan butir 1 dan dengan provider yang mengirim chunk sah menurut `encoding/json`. Test: `TestDeltaDuplicateKeys`, korpus `testdata/fuzz/FuzzDeltaDecode/a5bde4902149433c`.

**Dampak terukur (SPEC §20).** Token 0. Latensi: `BenchmarkDeltaDecode` ≈ 0,36 µs/chunk (≈ 420 MB/s), 0 alloc. LOC: PR 1 ≈ 450 dari budget `provider` 2.000. Binary/dependency 0.

**Konsekuensi.** (+) Jalur panas streaming tanpa reflect/alokasi dengan kebenaran diferensial terhadap stdlib; perilaku EC-PROV F1 eksplisit. (−) Endpoint yang menolak parameter tanpa `error.param` tidak dipulihkan otomatis; kunci JSON beda kapitalisasi diabaikan.

## ADR-097 — `internal/sandbox`: pencocokan `deny_paths`, open anti-TOCTOU per OS, macOS tanpa `F_GETPATH`

**Status:** Accepted 2026-10-18 (keputusan didelegasikan; untuk setiap celah dipilih opsi paling konservatif = fail-closed, tanpa dependency baru dan tanpa `unsafe`). Melengkapi SPEC §14.2, CODE-TOOLS §8, ADR-069, ADR-079 (verdict `outside_root` untuk symlink keluar root), ADR-084 butir 2.

**Aturan penerapan.** Diterapkan di `internal/sandbox` lewat dua PR berurutan (E-84, ≤ 600 baris Go non-test per PR): (a) verdict `Check` + `deny_paths` + normalisasi Windows `sandbox.go`/`normalize.go`; (b) open anti-TOCTOU `open*.go`/`walk_unix.go`. Review keamanan independen sebelum merge (F1-REPORT §"Review keamanan"). Sandbox = boundary hanya untuk tool kode (ADR-079 butir 8).

**Mekanisme open (setelah `Check` = `Inside`, path yang dibuka = path ter-resolve).**

| OS | Anti-TOCTOU | Verifikasi setelah open |
|---|---|---|
| Linux/Android ≥ 5.6 | `openat2(RESOLVE_BENEATH\|RESOLVE_NO_SYMLINKS\|RESOLVE_NO_MAGICLINKS)` relatif ke fd root; `EAGAIN`/`EINTR` diulang ≤ 16× lalu `ErrOutsideWorkspace` | cek ulang deny (butir 10) |
| Linux < 5.6, atau openat2 `ENOSYS`/`EPERM` (seccomp) | buka per komponen `openat(O_NOFOLLOW)` | `readlink /proc/self/fd/N` harus sama persis; `/proc` tidak ada = gagal |
| macOS/BSD | buka per komponen `openat(O_NOFOLLOW)` | device+inode fd = `lstat(path)` (butir 4) |
| Windows | `os.Root` | `GetFinalPathNameByHandle` dinormalisasi, sama persis (case-insensitive) |

`RESOLVE_NO_SYMLINKS` (bukan hanya `BENEATH`): symlink yang muncul setelah `Check` ditolak walaupun menunjuk ke dalam root, karena symlink ke `.env` di dalam root akan melewati `deny_paths`. Karena alasan yang sama `os.Root` (mengikuti symlink di dalam root) hanya dipakai di Windows (dengan verifikasi path final) dan OS non-target.

**Keputusan.** Setiap butir menyebut opsi yang ditolak.
1. **`deny_paths` dicocokkan dua kali**: pada path leksikal (sebelum resolve) dan pada path ter-resolve; cocok di salah satu = `denied_path`. Pola yang cocok dengan direktori menolak semua turunannya. Pola relatif `x` = `**/x`; `*`/`?`/`[..]` dalam satu komponen, `**` lintas komponen; `~/` = `Home`. Placeholder (`<config_dir>`) di-expand pemanggil. Ditolak: hanya path ter-resolve (symlink rusak/loop ke path deny lolos ke error lain) atau hanya leksikal (symlink ke path deny lolos). Test: `TestDenyPaths`, `TestCodeToolsSymlinkOutsideRoot`, `TestDenyAliases`. **Amandemen 2026-10-19 (REVIEW-F1-SEC1 S15-1):** pola dikompilasi dari teks, sehingga direktori deny bersymlink (`~/.ssh -> ~/dotfiles/ssh`, pola stow/chezmoi) atau Home bersymlink (`/home -> /var/home`) membuat path fisik tidak cocok bentuk mana pun. Di `New`, prefix bebas-glob setiap pola absolut (setelah `~` di-expand) di-resolve (`resolve`: symlink + junction, ekor yang belum ada ditambahkan leksikal) dan bentuk kanoniknya ditambahkan sebagai pola kedua; path leksikal dan ter-resolve dicocokkan dengan kedua bentuk. Prefix yang gagal di-resolve (izin, loop) hanya memakai bentuk konfigurasi. Symlink yang dibuat setelah `New` ditangkap oleh cek pasca-open (S16-2, PR (b)). Ditolak: resolve per `Check` (biaya syscall per pola per panggilan).
2. **Deny case-insensitive di semua OS (diamandemen 2026-10-19, REVIEW-F1-SEC1 S15-3; sebelumnya hanya Windows dan macOS).** Linux juga me-mount FS case-insensitive (WSL `/mnt/c` drvfs, vfat/exFAT, ext4/f2fs casefold, SMB), dan jenis FS target tidak bisa diketahui murah per path, sehingga pola dan path di-lower-case (`strings.ToLower`) di semua OS: fail-closed. Sisa risiko yang diterima: false positive untuk file yang hanya beda huruf di FS case-sensitive (`root/.ENV` ditolak walau berbeda dari `.env`); case folding Unicode APFS/NTFS dan normalisasi NFC/NFD tidak disamakan (S15-4, MINOR). Prefix root case-insensitive hanya di Windows; di macOS beda huruf terhadap root = `outside_root` (minta approval), bukan `inside`. Ditolak: case-sensitive di Linux (di proyek WSL `/mnt/c`, `root/.ENV` membuka `.env` dengan verdict `inside`). Test: `TestDenyPaths` (`case folded`).
3. **`OpenApproved` (SPEC tidak menetapkan cara membuka path `outside_root` setelah approval).** `deny_paths` tetap menang (`ErrDeniedPath`); target ter-resolve dibuka di bawah direktori induknya dengan jaminan tanpa-symlink yang sama dengan `OpenFile`. Ditolak: pemanggil membuka sendiri dengan `os.Open` (symlink yang ditukar setelah approval diikuti).
4. **macOS/BSD tanpa `F_GETPATH`.** `golang.org/x/sys` v0.48.0 hanya mengekspor konstanta `F_GETPATH`; `FcntlInt` menerima argumen `int`, sehingga buffer path hanya bisa dioper lewat `unsafe.Pointer` (dilarang, E-38). Dipilih: walk per komponen `openat(O_NOFOLLOW)` dari fd root (setara `RESOLVE_BENEATH|RESOLVE_NO_SYMLINKS` untuk path ter-resolve tanpa `..`) + **cek inode setelah open** (`verifyInode`): `lstat(path ter-resolve)` harus menunjuk device+inode yang sama dengan fd, jika tidak → `ErrOutsideWorkspace`. Ditolak: pengecualian `unsafe` satu file (melanggar E-38 tanpa menutup risiko yang tersisa di bawah), atau hanya walk tanpa cek (rename direktori di tengah walk tidak terdeteksi). **Sisa risiko (TRUST-MODEL §3):** (a) rename yang dikembalikan sebelum `lstat` (ABA) tidak terdeteksi, tetapi walk tidak pernah melewati symlink, sehingga objek yang dibuka selalu dicapai lewat entri direktori di bawah root saat walk; (b) hard link ke file luar root yang dibuat di dalam root tidak terdeteksi (sama di semua OS, juga `openat2`); (c) rename di dalam root antara `Check` dan open (mis. `.env` → nama lain) tidak terdeteksi oleh `deny_paths` — pelakunya butuh shell, yang memang bukan boundary (ADR-079 butir 8). Test: `TestVerifyInode` (semua Unix), `TestSymlinkEscapeRace` dan `TestOpenFile` dijalankan nyata di runner macOS CI.
5. **Race terdeteksi → `ErrOutsideWorkspace`**: symlink yang muncul antara `Check` dan open (`ELOOP`, `EXDEV`, `EMLINK`, atau `ENOTDIR` yang ternyata symlink), verifikasi pasca-open gagal, `/proc` tidak tersedia di jalur fallback Linux. Pemanggil boleh mengulang; `Check` berikutnya melihat keadaan baru. Ditolak: mengulang otomatis di dalam paket (menyamarkan serangan sebagai sukses tertunda).
6. **Windows fail-closed**: path final yang tidak sama setelah normalisasi = `ErrOutsideWorkspace`, termasuk drive mapped/`subst` (false positive yang diketahui); `\\?\Volume{GUID}\…` tidak dinormalisasi sehingga selalu `outside_root`. Sisa risiko: `O_CREATE` lewat symlink yang ditukar ke target **di dalam** root dapat membuat file kosong di sana sebelum verifikasi menolak open (tidak ada data yang terbaca atau keluar root).
7. **Path MSYS/Cygwin** `/x/…` dan `/cygdrive/x/…` dengan satu huruf selalu diartikan sebagai drive `X:\` (EC-WIN-012), hanya di Windows; `\??\` dan `\\?\UNC\` dinormalisasi. Ditolak: menebak berdasarkan keberadaan direktori `\x` (hasil bergantung keadaan disk).
8. **Test symlink tidak pernah di-skip.** `os.Symlink` yang ditolak = test gagal, juga di Windows (runner CI punya hak symlink); proses pembalik symlink `TestSymlinkFlipHelper` keluar tanpa skip saat dijalankan biasa.
9. **Junction Windows di-resolve seperti symlink (2026-10-19, REVIEW-F1-SEC1 S15-2).** Sejak Go 1.23 (`winsymlink`) junction dilaporkan `ModeIrregular` dan tidak diikuti `filepath.EvalSymlinks`. Di Windows, `resolve` (juga untuk root dan prefix pola deny) memeriksa setiap komponen path hasil `EvalSymlinks`; komponen `ModeSymlink|ModeIrregular` yang target-nya terbaca lewat `os.Readlink` diikuti (batas 40 hop bersama symlink), reparse point lain (tanpa target link) tetap file biasa. Akibatnya junction ke `~/.ssh` = `denied_path`, junction keluar root = `outside_root`, dan path ter-resolve bebas reparse point sehingga `os.Root` bisa membukanya (S16-1). Ditolak: `GODEBUG=winsymlink=0` (berlaku untuk seluruh proses dan akan dihapus Go). Test: `TestJunctionVerdicts` (Windows, `mklink /J`, tanpa skip), `TestFirstLink` (semua OS, lewat symlink).
10. **Cek ulang deny setelah setiap open (2026-10-19, REVIEW-F1-SEC1 S16-2).** `OpenFile` dan `OpenApproved` memeriksa handle yang sudah terbuka (`recheckDeny`): (a) path nyata handle, bila OS melaporkannya (`readlink /proc/self/fd/N` di Linux, juga di jalur openat2; `GetFinalPathNameByHandle` dinormalisasi di Windows), dicocokkan lagi dengan `deny_paths`; (b) identitas objek (device+inode di Unix, volume+file ID di Windows, lewat `os.SameFile` atas `Stat` handle) dibandingkan dengan objek yang dinamai pola deny literal penuh (tanpa glob, mis. `~/.netrc`, `/etc/shadow`; bentuk konfigurasi dan kanonik) yang di-`Stat` saat `New`. Cocok = handle ditutup dan `ErrDeniedPath`; handle yang tidak bisa di-`Stat` = gagal. Di Windows path final yang berbeda dari path ter-resolve tetapi cocok deny (alias `subst`, `\\?\Volume{GUID}`, `\\localhost\C$`) = `ErrDeniedPath`, bukan lagi `ErrOutsideWorkspace`. Menutup: hard link ke file deny literal (S15-5 untuk target literal), alias volume Windows. Sisa risiko: macOS/BSD tidak punya path handle (butir 4), sehingga di sana hanya (b) yang berlaku; symlink deny yang dibuat setelah `New` tidak terdeteksi oleh identitas. Hard link ke file *di dalam* direktori deny atau ke target glob ditangani butir 12. Biaya: 1 `readlink`/`GetFinalPathNameByHandle` + 1 `fstat` per open. Ditolak: hanya cek path (hard link lolos). (Penolakan otomatis untuk `st_nlink > 1` semula ditolak di sini; diganti butir 12: bukan ditolak, melainkan butuh approval.) Test: `TestOpenRecheckDeny`, `TestRecheckDenyClosedHandle`, `TestOpenDriveAlias` (Windows, `subst`).
11. **Junction di dalam root bisa dibuka (2026-10-19, REVIEW-F1-SEC1 S16-1).** `os.Root` menolak target reparse absolut, sedangkan target junction selalu absolut. Karena `Check` kini me-resolve junction (butir 9), path yang dibuka bebas reparse point: junction yang berujung di dalam root dibuka lewat target-nya dan diverifikasi path final; yang keluar root = `outside_root`. Test: `TestJunctionOpen` (Windows, `mklink /J`, kedua mode open).
12. **File ber-hard-link butuh approval (2026-10-19, REVIEW-F1-SEC1 Putaran 2 S16-7).** `denyIDs` (butir 10) hanya mengenal objek pola literal; hard link di root ke file *di dalam* direktori deny (`~/.ssh/x`) atau ke target pola glob (`**/.env`, `*.pem`) lolos semua cek path, dan di Linux `readlink /proc/self/fd/N` mengembalikan nama di root. Aturan konservatif: file reguler di dalam root dengan jumlah link > 1 (`st_nlink` lewat `lstat`/`fstat` di Unix; `NumberOfLinks` lewat `GetFileInformationByHandle` di Windows) tidak pernah dibuka otomatis, untuk baca maupun tulis. `Check` melaporkan verdict baru `HardLinked` (`hardlinked_file`) bila target ada; `OpenFile` mengembalikan `(nil, HardLinked, nil)`, dan file yang mendapat link kedua setelah `Check` ditutup lewat cek handle dengan verdict yang sama. Pemanggil memperlakukan setiap verdict selain `Inside` seperti `outside_root`: minta approval, lalu `OpenApproved` (yang tetap menegakkan `deny_paths` + `recheckDeny`); headless gagal `ErrOutsideWorkspace`. `O_TRUNC` diterapkan (`Truncate(0)`) hanya setelah handle lolos semua cek, sehingga file yang ditolak (deny lewat identitas atau hard link) tidak pernah terpotong. Direktori dan objek non-reguler tidak dihitung. OS tanpa API jumlah link (wasip1/js/plan9) menganggap semua file ber-link tunggal. Konsekuensi: file dari store pnpm (`node_modules` ber-hard-link), objek git hasil `clone --local`, dan backup ber-hard-link butuh approval per file (false positive yang diterima; fail-closed). Ditolak: mencatat inode setiap file di bawah direktori deny saat `New` (biaya walk tak terbatas, file baru tidak tercakup) atau menolak tanpa opsi approval (file sah tidak bisa dibaca sama sekali). Sisa risiko: approval untuk `hardlinked_file` membuka file apa pun yang dinamai link itu, kecuali objek deny literal (TRUST-MODEL §3). **Budget LOC:** `sandbox` + `redact` 1.300 → 1.450 (`loc.budget`, ENGINEERING §2.4; total core 33.750 → 33.900 ≤ 47.000, SPEC §3.2.4 tidak berubah). Terukur (scc, semua GOOS dihitung): main sebelum butir ini 1.254, dengan butir ini 1.335; sisa 115 untuk dua perubahan yang sudah diajukan di paket yang sama (fast path matcher deny untuk walker codetools ≈ 62, prefilter bigram redact untuk budget DLP netx ≈ 31), sehingga budget tidak dinaikkan berulang. Test: `TestHardLinkNeedsApproval` (hard link ke `~/.ssh/x` dan ke `.env` di luar root: `Check`/`OpenFile` baca dan tulis+truncate = `hardlinked_file`, isi target utuh; link kedua setelah `Check`; file nlink 1 terbuka; kedua mode open), `TestOpenRecheckDeny`.

**Dampak terukur (SPEC §20).** Token 0. Latensi `BenchmarkOpenFile` ≈ 9 µs/op, 34 alloc (Linux, openat2) saat butir 1–8; setelah butir 9–12 ≈ 11,6 µs/op, 3,16 kB, 44 alloc/op (REVIEW-F1-SEC1 S16-8; cek ulang deny pasca-open ≈ 2,5–3 µs). LOC `sandbox` 574 (scc), baris `sandbox+redact` 1.085/1.300 (2026-10-19: 1.335/1.450 setelah butir 9–12). Dependency 0 (`golang.org/x/sys` sudah ada).

**Konsekuensi.** (+) Tidak ada open yang mengikuti symlink yang muncul setelah `Check` di keempat jalur; deny case-insensitive di FS case-insensitive. (−) macOS memakai cek inode alih-alih path fd (sisa risiko butir 4); drive mapped/`subst` di Windows ditolak; jalur Windows/macOS hanya diverifikasi di runner CI.

## ADR-103 — `internal/codetools`: keputusan implementasi F1 di luar teks CODE-TOOLS

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; untuk setiap celah dipilih opsi paling konservatif, tanpa dependency di luar `deps.allow`). Melengkapi CODE-TOOLS §2–§5, §7, §10, ADR-014, ADR-031, ADR-071, ADR-079, ADR-082, ADR-097.

**Keputusan** (setiap butir menyebut opsi yang ditolak):
1. **Edge `codetools → boundary`** ditambahkan ke graf ENGINEERING §2.2.1 dan `archtest/allowed.go`: walker paralel dan indeks async butuh goroutine, dan E-26 hanya mengizinkan `boundary`. Ditolak: walker serial (gagal CT-01).
2. **API sandbox walker (`Denied`, `OpenResolved`; REVIEW-F1-SEC2 CT27-1/6/13).** Entri walk yang ditemukan di bawah direktori ter-`Check` tanpa mengikuti symlink dicocokkan `deny_paths` secara leksikal (`Denied`, tanpa syscall; fast-path komponen literal dan `*suffix`, hasil sama dengan `path.Match`, 0 alokasi) lalu dibuka tanpa symlink di **seluruh** path: satu `openat2(AT_FDCWD, path, RESOLVE_NO_SYMLINKS|RESOLVE_NO_MAGICLINKS)` (fallback walk `O_NOFOLLOW` dari `/` + verifikasi `/proc/self/fd` atau inode). Di dalam root setelah open: cek identitas deny dan jumlah link pada handle (`nlink > 1` → verdict `HardLinked`, butuh approval seperti ADR-097 butir 12). Di luar semua root (hanya setelah approval) open dimulai dari root volume dengan aturan tanpa-symlink yang sama, ditambah cek ulang deny penuh (path nyata handle + identitas), sehingga tidak pernah lebih lemah dari `OpenApproved`. Test: `TestOpenResolvedSwapRace` (kedua mode × kedua cabang, ≥ 2.000 iterasi, asersi opened > 0 dan refused > 0), `TestOpenResolvedHardLinks`, `TestDenied`. Ditolak: `Check`+`OpenFile` per file (13,6 µs/file vs 3 µs, 280 ms CPU di C1) dan `os.Open` biasa (TOCTOU).
3. **Urutan output** = DFS dengan nama byte-sorted per direktori (bukan sort byte path utuh); jendela reorder **64 × GOMAXPROCS** (bukan 4×: 4× = 108 ms vs 69 ms di C1 karena head-of-line blocking). Penghitung `skipped:` diisi oleh emitter sesuai urutan sehingga deterministik saat early exit.
4. **Ignore** (CT27-3): berkas di atas root sesi tidak pernah dibaca (`.gitignore` repo induk di luar root diabaikan). File ignore in-tree (`.gitignore`, `.ignore`, `.git/info/exclude`) hanya dibaca lewat `OpenResolved` tanpa approval bila verdict-nya Inside dan berupa file reguler; symlink (seperti git ≥ 2.32), verdict lain, FIFO/device = dianggap tidak ada. `core.excludesFile`/`include.path` dari `.git/config` repo wajib Inside. Hanya path global tetap (config git XDG, `~/.gitconfig` dan `include.path`-nya, excludes global) boleh dibaca di luar root, lewat `OpenApproved` (deny tetap menang, isi tidak ke model). `includeIf` tidak didukung; `.ignore` di-reset bersama `.gitignore` di batas repo bersarang. Konsekuensi yang diterima (lebih banyak file tampil, tidak ada kebocoran): grep/glob atas path `outside_root` yang di-approve hanya menerapkan excludes global; `.gitignore` ber-hard-link (`nlink > 1`) dianggap tidak ada. Test: `TestIgnoreSymlinkOutsideRoot`, `TestRepoExcludesFileOutsideRoot`, `TestFIFONoHang`. Ditolak: membaca ke atas root (melanggar sandbox), meminta approval per grep.
5. **Racy stat-tag** (§5.5): hash konten penuh dihitung oleh pass indeks (sync/async) hanya bila mtime ≤ 2 s; edit tanpa hash itu → `stale_read`.
6. **read_file**: UTF-16 dimuat penuh ≤ 64 MiB (lebih → `too_large`); `invalid_utf8=K` dihitung atas baris yang ditampilkan; tail dingin dinomori `L-N..-1/?`; file semu (`/proc`, ukuran stat 0) dibaca sampai EOF. Deskripsi biner berbahasa Inggris dengan desimal titik (`1.2 MB`).
7. **edit_file**: `new_text` satu `\n` di akhir = terminator baris terakhir, `""` = hapus baris; region multi-baris memakai EOL baris pertama region; line-range > 64 MiB distream, no-op terdeteksi selama stream (temp dibuang). Flock advisory pada temp tidak dipakai (temp unik tidak mengecualikan siapa pun): kunci in-process + re-stat sebelum rename. Kunci (CT27-5) dikunci pada path ter-resolve symlink (case-folded di Windows/macOS); file ber-hard-link (ditulis in-place) dikunci pada device+inode sehingga semua namanya berbagi satu kunci; kunci tanpa pemegang dihapus dari map.
8. **write_file baru**: mode dari umask (temp 0666), bukan 0644 paksa; direktori induk dibuat per komponen setelah `Check` (sisa risiko race direktori, file sendiri tetap via open tanpa symlink).
9. **Temp yatim** dibersihkan lewat `Engine.CleanOrphanTemps` (dipanggil starter sesi): hanya nama `^\..+\.damnbot-[a-z2-7]{12}$` (CT27-10), di-unlink relatif ke fd direktori yang dibuka lewat sandbox (`unlinkat`; Windows by path setelah verifikasi direktori). Backup in-place `.<nama>.damnbot-bak-<rand>` (butir 13) sengaja tidak dihapus.
10. **Spill (P5)**: codetools hanya memotong halaman (`Opts.PageChars`, ADR-031); spill/agregat turn milik pipeline `internal/tools`.
11. **Benchmark absolut** CT-04..07, CT-09 bernama `BenchmarkCodetools/<Spec>` agar cocok regex job `perf` (ADR-094 butir 5); CT-04 di test utama memakai korpus berbentuk C3 yang dibangkitkan di test, C3 asli diukur `bench/codetools`.
12. **xattr** disalin di Linux dan macOS; OS lain melapor `xattr_preserved=false`. Windows: `MoveFileEx(REPLACE_EXISTING|WRITE_THROUGH)`, fsync dir no-op (ADR-094 butir 15); file pengganti mendapat owner pembuat dan ACL warisan direktori, sehingga `owner_preserved=false` dilaporkan di Windows untuk tulis via rename (CT27-12). Bit setuid/setgid/sticky ikut dipertahankan; `mode_preserved` diverifikasi dengan `fstat` setelah `fchmod` (kernel bisa membuang setgid) (CT27-9).
13. **Tulis in-place file ber-hard-link** (CT27-2, CT27-8; CODE-TOOLS §5.4). Hanya setelah approval `hardlinked_file` (ADR-097 butir 12). Urutan: (a) isi baru ditulis lengkap ke temp `.<nama>.damnbot-<rand>` + `fsync` — validasi, no-op, atau disk penuh berhenti di sini dan target tidak tersentuh; (b) re-stat (`stale_read` bila berubah); (c) byte lama ke callback `Config.Snapshot` (pipeline menyimpannya di `file_snapshots`, SPEC §4.3) dan ke backup `.<nama>.damnbot-bak-<rand>` + `fsync`; (d) target dibuka tanpa `O_TRUNC` dan identitasnya (dev, ino, size, mtime) dicocokkan; (e) pre-alokasi ukuran baru (`fallocate(FALLOC_FL_KEEP_SIZE)` di Linux, ENOSPC berhenti sebelum byte pertama); (f) salin temp → target dari offset 0, `ftruncate` ke ukuran baru, `fsync`; (g) read-back sha256. Error di (f) memulihkan target dari backup sehingga target byte-identik; bila pemulihan gagal, backup dipertahankan dan path-nya disebut di error. Sisa risiko: `kill -9`/mati listrik di tengah (f) meninggalkan target parsial; isi lama utuh ada di backup ber-`fsync` dan di snapshot (CT-25 versi hard link: `TestInPlaceKill9`). Ditolak: `O_TRUNC` lalu stream dari inode yang sama (memotong file > 64 MiB, temuan CT27-2), menolak semua edit file ber-hard-link (repo pnpm/`cp -al` tidak bisa diedit).
14. **Titik kill CT-25** (CT27-6): `TestAtomicWriteKill9` memarkir proses anak di titik commit bernama (`mid-temp`: temp setengah tertulis, `pre-rename`, `post-rename`) lewat hook test `Engine.hook`, plus kill waktu acak; titik yang tidak tercapai = gagal, sehingga jalur yang menulis target langsung (tanpa temp + rename) tertangkap.
15. **FIFO/device** (CT27-4): sandbox menolak objek non-reguler non-direktori sebelum open (lstat di `Check`) dan sesudahnya (`fstat` handle; open komponen akhir dengan `O_NONBLOCK`, dibersihkan untuk file reguler), sebagai `ErrNotRegular` → `invalid_args`. Walker melewati entri non-reguler dan file ignore non-reguler. Sisa risiko: swap antara lstat dan open di dalam root masih bisa membuka device (tanpa blok, lalu ditolak); di wasip1/plan9 tidak ada `O_NONBLOCK`.
16. **Residual race rename/mkdir/remove** (CT27-11, diterima): `rename` temp → target, `mkdir` induk baru, dan penghapusan temp dilakukan berdasarkan path setelah `Check`, bukan `renameat`/`mkdirat` relatif ke fd direktori. Leluhur yang ditukar dengan symlink di antara `Check` dan syscall bisa mengarahkan rename/mkdir ke luar root. Batasnya: temp dibuat lewat open tanpa symlink di direktori ter-resolve, sehingga rename hanya memindahkan byte yang ditulis tool sendiri, tidak pernah membaca isi di luar root; read-back sha256 lewat `OpenResolved` atas path target melapor `write_verify_failed` bila rename tidak mendarat di sana. Dicatat juga di TRUST-MODEL §3. Ditolak untuk F1: `renameat`/`mkdirat` per OS (Windows tidak punya padanan relatif handle yang setara di `x/sys`; menambah ±150 LOC di tiga OS).

**Dampak terukur.** LOC `codetools` ≈ 4.200/4.500 (scc). Dependency langsung baru: `github.com/bmatcuk/doublestar/v4`, `golang.org/x/text` (keduanya sudah di `deps.allow`). Token schema: lihat CT-27.

## ADR-105 — `internal/exec` F1: reaper per pid tanpa subreaper, capture head+tail, Job Object tersuspensi, pengecualian E-38 Windows

**Status:** Accepted 2026-10-20 (keputusan didelegasikan; untuk setiap celah dipilih opsi paling konservatif dalam budget, tanpa dependency di luar ENGINEERING §4). Melengkapi EXEC-AND-CODING §A.1–A.4, ADR-072, ADR-082, ADR-088.

**Aturan penerapan.** Diterapkan di `internal/exec` lewat PR berurutan (E-84, ≤ 600 baris Go non-test per PR): (1) tipe hasil + lapisan proses per OS (`proc_unix.go`, `proc_windows.go`) + capture; (2) `Runner` (loop timeout/cancel/kill order, sweep marker); PR berikutnya (env, mode one-shot, prompt interaktif, idle, ConPTY, summarize, P24) menambah butir di ADR ini. Review keamanan independen sebelum merge (F1-REPORT §"Review keamanan"); tidak ada PR `exec` yang di-merge tanpa approval auditor.

**Keputusan.** Setiap butir menyebut opsi yang ditolak.
1. **Leader di-reap per pid (`os.Process.Wait` = `wait4(pid)`), tanpa `wait4(-1)` dan tanpa `PR_SET_CHILD_SUBREAPER`** (mengamandemen §A.1 "satu goroutine reaper" dan §A.4 subreaper). Proses yang lolos dari process group (`setsid`, double-fork) dibunuh lewat sweep marker `DAMNBOT_EXEC_ID`; setelah di-kill ia di-reap init. Ditolak: reaper global `wait4(-1)` — mencuri status exit anak milik pemakai `os/exec` lain di proses yang sama (test, modul F3+), dan subreaper tanpa reaper global menumpuk zombie. Konsekuensi: P24.1 tetap diuji lewat marker (`assertNoMarker`, `TestNoOrphans`), bukan lewat parent.
2. **`Pdeathsig = SIGKILL` (Linux) tanpa goroutine spawner ber-`LockOSThread`.** Runtime Go hanya mengakhiri thread OS saat goroutine yang terkunci selesai; `Run` tidak pernah mengunci, jadi sinyal terpicu bersama matinya proses damnbot. Ditolak: goroutine spawner khusus — satu goroutine permanen + antrean tanpa manfaat terukur.
3. **Capture = 8 MiB awal + ring 8 MiB akhir** (`OutputCap` 16 MiB), `BytesRaw` menghitung semua byte. Biaya O(n) untuk output berapa pun (ring tetap, tanpa geser). Ringkasan/spill §A.3 bekerja di atas capture ini; byte tengah yang dibuang ditandai `Truncated`. Ditolak: menyimpan semua output — memori tak terbatas untuk perintah yang mencetak tanpa henti sampai timeout.
4. **Windows: `CREATE_SUSPENDED` → `AssignProcessToJobObject` (`KILL_ON_JOB_CLOSE`) → `NtResumeProcess`.** `os.StartProcess` tidak memberi handle thread utama, jadi resume lewat `NtResumeProcess` (ntdll). Kill = `TerminateJobObject` langsung: tidak ada padanan SIGTERM untuk pohon console sembarang, sehingga `kill_grace_s` tidak berlaku di Windows. Ditolak: `CreateProcess` mentah — duplikasi logika pewarisan handle `syscall`.
5. **Pengecualian E-38 (`unsafe`) hanya untuk `internal/exec/*_windows.go`** (`.golangci.yml`: depguard + gosec G103 di path itu). API Job Object (dan ConPTY, PR berikutnya) menerima pointer struct. Ditolak: tanpa Job Object — melanggar P24.1 di Windows.
6. **Edge import `internal/exec → internal/boundary`** (E-26): reader output, waiter, penulis stdin, dan penyelesai `still_running` = anggota satu `boundary.Group` milik `Runner` (`SetLimit` 768, E-27); `Close` menunggu semuanya.
7. **ExecID = `<pid damnbot>.<16 hex acak>`.** Pid pemilik memungkinkan sweep pasca-crash membedakan pohon milik instance yang sudah mati dari milik instance lain yang hidup.
8. **One-shot F1: sisa pohon setelah leader exit langsung di-SIGKILL + sweep**, lalu output dibaca sampai EOF dengan batas 250 ms. Background adalah F5 (§A.5); di F1 `background` = `invalid_args`. Ditolak: menunggu EOF pipe tanpa batas — `sleep 1000 &` menahan call (hang klasik `Cmd.Wait`).
9. **Cancel (ADR-072/082):** SIGTERM ke pgid, tunggu ≤ 1 s; mati → `cancelled_confirmed`; belum → `still_running` + `Result.Done`, kill order berlanjut di background (sisa grace → SIGKILL → sweep) dan id tercatat di `Runner.Live` sampai mati.
10. **Sweep:** Linux `/proc/*/environ` (proses uid sendiri; zombie ber-environ kosong dilewati), macOS `kern.procargs2` untuk uid sendiri, sampai 5 putaran selama marker baru masih muncul. Unix lain hanya process group.
11. **Gagal spawn** → `Result{Status: spawn_failed}` + error ter-wrap; tidak ada fallback runner diam-diam.
12. **Env anak (ADR-071, GAP-SEC-02, §A.6) — PR 3.** `EnvPolicy.Env`: induk hanya lewat allowlist SEC-02 (nama persis + `LC_*`, `XDG_*`, `*_HOME` yang namanya tidak memuat `KEY|TOKEN|SECRET|PASSWORD|PASSWD|AUTH`), `exec.env_allow` (nama persis), `SSH_AUTH_SOCK` hanya dengan `pass_ssh_agent`; `HTTP(S)_PROXY`/`NO_PROXY` tanpa userinfo, dan nilai proxy ber-`@` yang tidak bisa di-parse dibuang (bukan diteruskan). Lalu tabel §A.6, `GIT_SSH_COMMAND` hanya bila user tidak menyetel/mewariskannya, `exec.env` menang, `GIT_TERMINAL_PROMPT=0`/marker/`DAMNBOT` keras; marker palsu dari induk dibuang. `TERM` tidak diwariskan (dumb/xterm-256color per runner).
13. **Windows:** padanan HOME/USER/TMPDIR dan var yang dibutuhkan proses Win32 untuk start ikut allowlist (`SystemRoot`, `SystemDrive`, `windir`, `ComSpec`, `PATHEXT`, `TEMP`, `TMP`, `USERPROFILE`, `USERNAME`, `USERDOMAIN`, `APPDATA`, `LOCALAPPDATA`, `ProgramData`, `ProgramFiles*`, `CommonProgramFiles*`, `NUMBER_OF_PROCESSORS`, `PROCESSOR_ARCHITECTURE`, `OS`); nama var tidak peka huruf. Ditolak: tanpa var ini — `cmd.exe`/`pwsh` gagal start atau berperilaku salah.
14. **Mode one-shot (§A.2, ADR-088) dan NEW3-03/04.** `Prepare`: mode (b) bila pemanggil memberi argv (`classify.DirectArgv`), else mode (a) `bash -c` → `sh -c` (Windows `pwsh` → `powershell` `-NoProfile -NonInteractive -Command` → `cmd /d /s /c`). argv0 di-resolve lewat `PATH` anak **hanya dari entri absolut** (entri kosong/relatif = direktori kerja = workspace, dilewati; NEW3-04 di sisi runner) dan di Windows hanya `.exe`/`.com` (`.bat`/`.cmd` dijalankan `CreateProcess` lewat `cmd.exe`). NEW3-03: perintah ber-secret-ref yang bukan simple command literal berjalan di mode (a) **tanpa** secret, dengan notice `secret env X not injected…` di hasil (tidak ditolak `invalid_args`: label `unknown` sudah memaksa approval + preview). Ditolak: menolak — perintah sah seperti `gh api … | jq` tidak bisa dijalankan sama sekali, tanpa manfaat keamanan (secret tetap tidak disuntikkan).
15. **Pemilihan PTY (§A.1):** `NeedsTTY` atas argv tiap simple command; nilai opsi tidak dilacak, jadi `ssh -p 22 host` dianggap punya perintah remote → pipe (gagal cepat, bukan hang).
16. **Idle (§A.4) — PR 4.** Jendela idle direset oleh output, CPU pohon ≥ 1 % satu core (akumulatif sejak awal jendela), atau I/O ≥ 64 KiB; sampel tiap 1 s. Linux: `utime+stime` (`/proc/<pid>/stat`) dan `rchar+wchar` (`/proc/<pid>/io`) per pid bermarker, pid baru dihitung penuh. Windows: total CPU/I/O Job Object (`JobObjectBasicAndIoAccountingInformation`). macOS: sysctl tidak memberi counter CPU/I/O per proses (`p_uticks`/`p_rtime` selalu 0, `p_stat` = `SRUN` untuk setiap proses hidup) dan `proc_pid_rusage` butuh libproc (cgo) atau syscall mentah + `unsafe`; pohon di macOS tidak pernah di-idle-kill (timeout tetap berlaku), seperti Unix lain tanpa akuntansi. Ditolak: idle hanya dari output — `go build` yang sunyi akan dibunuh.
17. **Prompt interaktif (§A.6) — PR 4.** Deteksi hanya untuk runner PTY dan pipe ber-stdin (pipe tanpa stdin = EOF, gagal cepat). Diam ≥ 1,5 s + baris parsial terakhir (setelah `\n` terakhir dan `\r` terakhir, tanpa escape ANSI, ≤ 200 char) cocok tabel §A.6 → `waiting_for_input` + `prompt` + `input_kind`, pohon di-kill (urutan A.4), tanpa `process_id` (F1). Linux wchan: nama tanpa sufiks compiler (`poll_schedule_timeout.constprop.0`); di PTY, tunggu `poll`/`select`/`epoll` juga dihitung (vim/top menunggu tombol lewat poll, tidak lewat `n_tty_read`), setelah diam 5 s. Env §A.6 ditambah `LESS=-Ps--More--`: layar pertama `less` menampilkan nama file, bukan prompt, sehingga di macOS (tanpa wchan) tidak terdeteksi; prompt tetap `--More--` membuatnya kelas `pager`. Ditolak: memperluas wchan poll/epoll ke runner pipe — server/klien jaringan yang menunggu socket akan dibunuh.
18. **`EPERM` saat sinyal ke grup = tidak ada yang bisa disinyal, bukan error.** macOS mengembalikan `EPERM` untuk `kill(-pgid)` bila anggota grup tinggal zombie; anggota yang berganti uid (setuid) memang tidak bisa dibunuh pemanggil. Ditolak: memperlakukannya sebagai kegagalan cleanup — call yang sudah selesai dilaporkan error.
19. **Ringkasan ke model (§A.3) — PR 5.** Pipeline ada di paket `internal/exec` (`summarize.go`, `Summarizer`), bukan subpaket `internal/exec/summarize`: menghindari simpul baru di matriks impor (ENGINEERING §2.4) tanpa beda perilaku. Langkah 1–5 persis §A.3 (CRLF bukan redraw; escape string DCS/SOS/PM/APC ikut dibuang; satu byte invalid = satu U+FFFD; `[×N]` yang sudah ada dijumlahkan saat run digabung lagi); redaksi dengan `redact.Redactor` milik sesi (pola + secret dikenal). Spill: `exec` tidak menulis blob — `spill(log, raw)` dari pemanggil menyimpan kedua blob (`blob_sha`, `raw_blob_sha`, SPEC §11.6) dan mengembalikan path yang ditampilkan di preview. Preview: head 160 + ≤ 3 baris error unik (≤ 80 char, dari bagian tengah) + tail 400, jatah head/error tak terpakai pindah ke tail; bila sisa jatah inline turn < 4.000 char, preview pendek 80/120/200 (P5). Header `[exit N|none · <ms dengan pemisah ribuan titik> ms · signal <nama>|none · runner pipe|pty · status <ExecStatus>]`. `BenchmarkSummarize1MB` ≤ 15 ms ditegakkan job `perf` (`PERF_BENCH`).
20. **P24 (§A.10) — PR 6.** `SweepStale` = sweep "pada start berikutnya": membunuh proses bermarker yang pemiliknya (pid di ExecID, butir 7) sudah tidak hidup; dipanggil sekali oleh composition root saat start, bukan oleh `New` (beberapa `Runner` per proses). Marker milik instance lain yang hidup tidak disentuh; pid pemilik yang didaur ulang hanya membuat sweep melewatinya. Setelah `kill -9` damnbot di Linux, `Pdeathsig` membunuh leader saja; anggota pohon lain (termasuk yang tidak `setsid`) hidup sampai start berikutnya — sama dengan macOS; P24.1 untuk kasus ini diukur setelah start berikutnya di semua Unix (Windows: Job Object mati bersama handle pemiliknya). `TestNoOrphans` 200 iterasi: exit normal dengan bentuk lepas (`&`, subshell, double fork, `nohup`, `disown`, `setsid`), timeout pohon yang mengabaikan TERM, cancel, dan `kill -9` proses pemilik (subproses test) + sweep; dua mutan (tanpa sweep per call, tanpa `SweepStale`) gagal. `BenchmarkShellSpawnPipe`/`PTY` (< 15 ms) dan `BenchmarkShellSpawnWindows` (< 40 ms, pipe) menegakkan p95 overhead (durasi `Run` − median start+wait langsung) di job `perf`.
21. **ConPTY (§A.1) — PR 7.** Runner PTY Windows = pseudo console 120×40 di dalam Job Object call: `CreatePseudoConsole` atas pipe input (stdin ter-pipe yang di-duplikasi, atau pipe baru yang ujung tulisnya tetap terbuka sehingga baca console memblok seperti terminal) dan pipe output (teks VT = output call); proses dibuat lewat `CreateProcess` langsung dengan `STARTUPINFOEX` + `PROC_THREAD_ATTRIBUTE_PSEUDOCONSOLE` (os.StartProcess tidak mendukungnya), `CREATE_SUSPENDED` → job → `ResumeThread`, env sebagai blok UTF-16. Setelah leader di-reap, `ClosePseudoConsole` dipanggil **sebelum** drain output dihentikan (conhost menutup pipe → EOF; di sebagian build Windows Close menunggu output terbaca). Ditolak: library ConPTY pihak ketiga — dependency baru untuk ≈ 150 baris.
22. **Sesi persisten (§A.2) — PR 8.** `Runner.NewSessions(dir, env)`: ≤ 4 sesi per sesi agent (`ErrSessionLimit`), nama `^[a-z0-9_-]{1,32}$`, idle 30 menit di-reap saat sesi lain dipakai (tanpa goroutine timer). Unix: shell `$SHELL` bila bash/zsh, else bash, else sh, di PTY dengan line editing mati (`--noediting`, zsh `+Z`: tanpa bracketed paste/redraw), `stty -echo`, prompt kosong, alias mati. fish tidak dipakai (line editor-nya menggemakan dan menggambar ulang input apa pun mode TTY-nya, sehingga sentinel tidak bisa dibingkai andal) → bash. Windows: pwsh → powershell (`-ExecutionPolicy Bypass` hanya untuk proses itu, agar dot-source file perintah sendiri berjalan di Windows PowerShell yang default-nya Restricted) → `cmd /d /q`, lewat **pipe** stdin, bukan ConPTY (echo console tidak bisa dimatikan). Perintah ditulis ke `<dir>/<exec id>/c<n>.<ext>` (0600) lalu di-source; sentinel `\x1eDB:<nonce 128-bit>:<exit>:<pwd -P>\x1e` (cmd: baris terpisah karena `%errorlevel%` diekspansi saat baris dibaca). Timeout: SIGINT ke pgrp foreground (`TIOCGPGRP`) + sentinel dikirim ulang (shell interaktif membuang sisa baris yang terinterupsi) → `timeout`, sesi tetap; tanpa sentinel dalam 3 s (atau di Windows, yang tidak punya padanan SIGINT untuk pipe) → shell dibunuh (urutan A.4 + sweep), `SessionReset`. Prompt (§A.6) dan cancel juga me-reset sesi. Env sesi dibangun tanpa argv0 langsung, jadi tanpa secret ref (ADR-088 butir 4). Ditolak: idle-timeout pohon di dalam sesi — shell sesi menunggu input secara sah di antara perintah.

**Dampak terukur (SPEC §20).** Token 0. Dependency +1 (`github.com/creack/pty`, sudah di `deps.allow`). LOC `exec` dihitung per PR di F1-REPORT. Latensi spawn diukur `BenchmarkShellSpawn*` (PR P24).

**Konsekuensi.** (+) Tidak ada interaksi dengan status anak milik kode lain; output tidak pernah memblokir call. (−) Proses yang membuang marker (`env -i`) *dan* keluar dari process group tidak terdeteksi di Linux/macOS (sisa risiko, TRUST-MODEL: shell bukan boundary); di Windows Job Object menangkap semuanya.

## ADR-106 — `internal/exec/classify` F1: label risiko shell, operand path, `shell_allow` argv-persis, kebijakan approval, budget `exec`/`exec/classify`

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; opsi paling konservatif di setiap titik yang tidak ditetapkan teks; dependency hanya `mvdan.cc/sh/v3` yang sudah ada di ENGINEERING §4 / `deps.allow`). Melengkapi EXEC-AND-CODING §A.8, SPEC §14.1/§14.2/§22.1 (GAP-SEC-12/14/15), ADR-069, ADR-079, ADR-081, ADR-088, ADR-092 (NEW3-04). Pemindahan budget butir 10 disetujui owner.

**Aturan penerapan.** Diterapkan lewat PR bertumpuk (E-84 ≤ 600 baris Go non-test per PR): (1) tipe API + operand path + `Decide`; (2) parser bash/POSIX + simple command generik; (3) tabel label, wrapper, interpreter; (4) HTTP/remote, lexer PowerShell/cmd, `DirectArgv`/`MatchAllow`, test acceptance. Paket murni (tanpa I/O kecuali `PathResolver` yang diinjeksi runner), tanpa import paket internal (`archtest`). Teks budget diterapkan di `loc.budget`, SPEC §3.2.4 (baris exec), ENGINEERING §2.4.

**Keputusan.**
1. **API.** `Classify(cmd, *Env) Verdict` tidak pernah gagal (yang tidak bisa dianalisis = `unknown`). `Env{Roots, Cwd, Home, BlobDir, Venv, DenyPaths, SecretVars, MonetaryHosts, Resolver, Dialect}`; `Verdict{Class, Flags, Reasons ≤ 5 × ≤ 120 byte (UTF-8 utuh), Argv (kata non-literal = teks sumber), NonMutating}`; `DirectArgv(cmd, Dialect)`; `MatchAllow(*AllowRequest)`; `Decide(*Verdict, *Context) Decision`; `OutputSource(*Verdict, trusted)`. Flag tambahan `references_secret` (ADR-081/088 butir 4: preview "references secret", label `unknown`, tidak pernah `shell_allow`). Ditolak: classifier membaca filesystem sendiri (tidak bisa diuji deterministik, dan runner sudah memegang sandbox/resolusi).
2. **Urutan kelas internal.** `mutating < unknown < destructive < irreversible < monetary < delegated_agent_bypass`; `unknown` dan `destructive` diperlakukan identik oleh kebijakan (A.8 "destructive = unknown"), urutan internal hanya menentukan label yang ditampilkan bila keduanya muncul (`curl … | sh` = `destructive`, bukan `unknown`). Kelas tertinggi semua simple command; flag selalu berlaku.
3. **Operand.** (a) Perintah file dikenal (`cat`, `ls`, `grep`…, `rm`, `mv`, `cp`, `tee`, `chmod`…): semua argumen posisi = operand (pola pertama `grep`/`sed`/`awk`/`jq` dilewati; argumen terakhir `cp`/`ln`/`install` = tulis); perintah lain: hanya argumen berbentuk path (`/`, `~`, `\`, huruf drive) dan nilai `--opsi=path`; opsi daftar file (`--files0-from`, `--files-from`, `--exclude-from`, `--file`…) dengan nilai `-` → `unknown`. (b) `deny_paths` dicocokkan pada path leksikal **dan** ter-resolve, case-insensitive di semua OS (sama dengan sandbox ADR-097 butir 2); cocok → hanya `denied_path` (error, tanpa label lain). (c) Tulis di luar root → `destructive`; baca di luar root → `outside_read`, kecuali direktori blob (baca) dan `/dev/null|zero|random|urandom|stdin|stdout|stderr|tty|fd/*`. (d) Setelah `cd`/`pushd`/`Set-Location` operand relatif → `unknown` (baris 8 ADR-079), target `cd` sendiri tidak dilabel. (e) Glob: prefix statis selalu dilabel, hasil `Resolver.Glob` juga; komponen akhir murni wildcard (`rm -r *`) dianggap menghapus direktori itu (root → `destructive`). (f) `rm -r` pada root atau `.git` → `destructive`. Ditolak: memperlakukan setiap argumen sebagai path (pesan commit, pola `find -name '*.pem'` akan menjadi `denied_path` yang tidak bisa di-approve).
4. **Interpreter dan rekursi.** `sh|bash|zsh|dash|ksh -c '<literal>'` direkursi (sh/dash dengan dialek POSIX); `-c` non-literal, program dari stdin/pipe/heredoc/here-string, atau tanpa program → `unknown`; unduhan yang di-pipe ke interpreter/`iex` → `destructive`. Interpreter non-shell (`python*`, `node`, `perl`, `ruby`, `php`, `lua`, `deno eval`, `bun -e`, `osascript -e`, `fish -c`) dengan kode inline literal → `unknown` (teks tidak menyebut; isi bahasa lain tidak dianalisis — opsi konservatif); file skrip = `mutating` + operand baca; `python -m pip` = aturan pip. `eval`/`iex` literal direkursi; `su -c` direkursi; `su`/`runas`/`sudo` tanpa perintah → `unknown`. `xargs` menambah argumen dinamis (operand → `unknown`); `find -exec` direkursi dengan `{}` = starting point pertama. `git -c`/`--exec-path`/`--config-env` dan assignment `PATH`/`LD_*`/`DYLD_*`/`BASH_ENV`/`ENV`/`IFS`/`NODE_OPTIONS`/`PYTHONPATH`/`PYTHONSTARTUP`/`PERL5OPT`/`RUBYOPT`/`GIT_SSH_COMMAND`/`GIT_EXEC_PATH`/`PROMPT_COMMAND` sebelum perintah → `unknown`. Fungsi yang memanggil dirinya → `destructive` (fork bomb). Nesting > 8 → `unknown`.
5. **`DirectArgv` (mode (b) execve).** Byte terlarang di mana pun, termasuk di dalam kutip: ``| & ; < > ( ) $ ` \ ~ * ? [ ] { } ! #`` (bash/POSIX) dan tambahan `% ^ @ , ' "` (PowerShell/cmd); karakter kontrol selain tab, DEL, dan rune Unicode non-printable (NBSP, zero-width, bidi override) ditolak. Bash/POSIX di-parse mvdan menjadi tepat satu `CallExpr` tanpa redirect/assignment/negasi/background; Windows dipecah spasi/tab. Ditolak: mengizinkan karakter terlarang di dalam kutip (beda kecil parser mvdan vs bash tidak lagi relevan karena eksekusi lewat execve, tetapi tidak ada kebutuhan yang membenarkan permukaannya).
6. **`MatchAllow`.** Token sama persis dengan satu entri; `cwd` absolut di dalam root; argv0 tanpa separator/`:`; `Resolve(argv0)` saat ini = rekaman awal sesi (path + dev, inode, size, mtime); path biner absolut (entri `PATH` relatif → tidak cocok), **di luar** setiap root dan cwd (NEW3-04); path ber-drive/UNC wajib `.exe`; `.bat`/`.cmd`/`.ps1`/`.com` tidak pernah cocok. Syarat konteks (one-shot, tanpa `stdin`, trusted, tidak tainted, tanpa tulis tanpa approval) di `Decide`.
7. **`Decide` (urutan).** `denied_path` → error; plan mode → auto hanya `shell_allow` lengkap + `non_mutating`, selain itu `denied`; `shell_allow` lengkap (kelas `mutating`, tanpa `privileged`/`outside_read`/`references_secret`) → auto; headless: kelas ≠ `mutating` atau `privileged` → `denied`, `outside_read` → `outside_workspace`, `--unsafe-auto-shell` + trusted + tidak tainted + cwd di root → auto, verify → `unverified`; interaktif: kelas ≠ `mutating` → tanya + preview, `privileged` → tanya per perintah, `outside_read` → tanya + preview, selain itu tanya. Baris 26 ADR-079 (`sudo ls /root`, juga `outside_read`) = tanya per perintah/`denied`; baris 33/34 (`read_file` luar root) dimodelkan sebagai tanya + preview (prompt memuat path) dan `outside_workspace`. `allow_session` tidak dimodelkan (tidak pernah untuk shell, ADR-088 butir 1).
8. **Pilihan label konservatif di luar daftar A.8.** `terraform apply`, `npm|pnpm -g`, `gh api` dengan `-X` apa pun → `irreversible`; `git config --global|--system` → `destructive`; `kill … -1` di posisi mana pun → `destructive`; tujuan HTTP yang bukan loopback (termasuk argumen yang tidak bisa diurai sebagai host) → `network`; `0.0.0.0` bukan loopback. Agent CLI tanpa flag bypass = `mutating` + alasan "consider a worktree or container backend" (GAP-SEC-12). `pip install` di luar `Env.Venv` di dalam root → `irreversible`.
9. **Lexer PowerShell/cmd.** Token: spasi/tab; `;`/baris baru (PowerShell) dan `&`/`&&`/`||`/`|`; kutip `'…'`/`"…"`; escape `` ` ``/`^`; `$nama`, `$env:NAMA`, `%NAMA%`, `!NAMA!` → dinamis kecuali `HOME`/`USERPROFILE` (= Home); var secret → `references_secret`. `(`, `)`, `{`, `}`, `@` → `unknown` ("unrecognised structure", ≥ minimum `mutating` A.8) dan perintah lain tetap dilabel. Alias (`Remove-Item`/`rd`/`del` → `rm`, `Get-Content`/`type` → `cat`, `Out-File`/`Set-Content` → `tee`, …) memakai aturan POSIX; `/s` dan `-Recurse` (singkatan) = rekursif; switch `/x` pendek bukan operand; nama case-insensitive, `.exe` dibuang. `pwsh -EncodedCommand` → `unknown`, `-Command` direkursi.
10. **Budget.** `loc.budget` `exec` 3.500 → 3.400 dan `exec/classify` 1.500 → 1.600; Σ core dan baris exec SPEC §3.2.4 (5.000 di F1) tetap. Alasan: cakupan lengkap A.8 (parser bash + lexer PowerShell/cmd + tabel label + `shell_allow`) ≈ 1.525 scc; proyeksi runner `exec` 2.700–3.100. Ditolak: memadatkan kode atau membuang aturan agar muat 1.500 (preferensi kejelasan dari owner).
11. **Edge case.** EC-TERM-006 (perintah yang tak bisa dipindai ditolak tanpa alasan) dimiliki `exec/classify`: setiap label `unknown` membawa alasan (`TestEdgeCases/EC-TERM-006`).

**Dampak terukur (SPEC §20).** Token 0. Latensi: `BenchmarkClassify` (perintah 2 KiB) ≈ 0,7 ms/op < 1 ms (dicek `timing.Check` di `make perf`). LOC `exec/classify` ≈ 1.525/1.600 scc. Dependency: `mvdan.cc/sh/v3` (sudah di `deps.allow`, ENGINEERING §4). Binary: paket `syntax` mvdan (tanpa `interp`/`expand`).

**Konsekuensi.** (+) Satu-satunya jalur auto shell (`shell_allow`) bergantung pada kecocokan byte-ketat + identitas biner, terlepas dari label; label tidak pernah memberi auto, sehingga celah classifier hanya menurunkan kualitas preview. (−) Lebih banyak `unknown` (kode inline bahasa lain, operand setelah `cd`, `xargs`); perintah ber-kutip yang memuat karakter terlarang tidak bisa masuk `shell_allow`.

## ADR-107 — Lingkungan pengembangan di VPS Debian dan bootstrap idempoten

**Status:** Accepted 2026-10-20 (keputusan user). Panduan operasional; tidak mengubah perilaku produk, spec, atau protokol.

**Konteks.** Pengembangan dilakukan dari ponsel terhadap VPS Debian. Mesin pengembangan perlu menyediakan toolchain repo (Go 1.26.9, race detector, `make`), akses jarak jauh yang tahan putus, dan hardening dasar tanpa membuka desktop ke internet. Lingkungan ini tidak diatur SPEC karena bukan requirement produk, tetapi harus dapat dibuat ulang dan diaudit.

**Keputusan.**
1. **Host pengembangan = Debian 12/13 (Linux amd64/arm64).** Windows+RDP ditolak: test dengan build tag `linux || darwin` (proses grup) serta `openat2`/sandbox dan semantik setgid/inode hanya nyata di Unix. Portabilitas Windows/macOS tetap dicakup matriks CI (`.github/workflows/ci.yml`, `windows-ctxdb.yml`), bukan mesin pengembangan.
2. **Satu skrip bootstrap idempoten** `scripts/vps-bootstrap.sh` (`set -euo pipefail`, `--help`, root check, `--code-server`, `--desktop`, `--apply-ssh`): paket dasar, user `dev` + grup sudo + salinan `authorized_keys`, snippet `/etc/ssh/sshd_config.d/99-damnbot.conf` (`PasswordAuthentication no`, `PermitRootLogin no`) yang hanya direload dengan `--apply-ssh`, ufw (default deny, allow port SSH efektif, deny 3389/5900), Go 1.26.9 dari tarball resmi, gh 2.102.0, dan drop-in `user-<uid>.slice` (CPU 350%, RAM 6G). Tidak ada `curl | bash`; setiap unduhan dipin versi + sha256; tidak ada rahasia di argv.
3. **Akses grafis bukan jalur utama dan tidak pernah publik.** code-server (opsional) hanya bind `127.0.0.1:8080` dengan password acak bermode 0600. `scripts/vps-desktop.sh` memasang XFCE (default) atau Cinnamon + xrdp, menulis `~dev/.xsession`, mematikan compositor XFCE/efek Cinnamon, mengatur `--res 1280x720|1920x1080` (best-effort; xorgxrdp mengikuti klien), menolak 3389 publik, dan hanya mengizinkan `tailscale0`. `deploy/cloud-init.yaml` memberi jalur RDP-first tanpa SSH. `deploy/guacamole/` memberi Guacamole clientless (webapp bind `127.0.0.1:8080`, image tag+digest). RustDesk, MeshCentral, noVNC/TigerVNC, dan Sunshine/Moonlight didokumentasikan sebagai alat host — bukan dependency repo.
4. **`docs/VPS.md` adalah panduan operasional, bukan spec.** Seluruh file dikecualikan dari spec-guard lewat `testdata/docs_exclusions.txt` (pola sama dengan ADR-104 untuk artefak review); perubahan requirement tetap wajib lewat ADR.
5. **Verifikasi tanpa eksekusi nyata di CI:** `bash -n`, `shellcheck` (bila tersedia), `--help`, `yaml.safe_load` untuk user-data/compose, dan `xmllint` untuk contoh user-mapping; skrip tidak dijalankan pada CI/repo. `docker compose config` dijalankan bila plugin Compose tersedia.

**Dampak terukur (SPEC §20).** Token, latensi, LOC produk, ukuran binary, dan dependency: 0. Dua skrip operasional, satu user-data cloud-init, satu compose, satu contoh konfigurasi, dan satu entri pengecualian dokumen.

**Konsekuensi.** (+) Lingkungan pengembangan/desktop dapat dibuat ulang, hardened secara default, dan tidak bergantung pada RDP publik. (−) Versi Go/gh/code-server serta digest image Guacamole harus dinaikkan manual; panduan VPS tidak normatif dan tidak diuji otomatis selain syntax/YAML.

## ADR-111 — Budget LOC `codetools` 4.500 → 4.800 (perbaikan REVIEW-F1-SEC2)

**Status:** Accepted 2026-10-19. Merevisi tabel ENGINEERING §2.4 dan `loc.budget`; SPEC §3.2.4 tidak berubah.

**Konteks.** `internal/codetools` mencapai tepat 4.500 baris (scc) sebelum perbaikan review keamanan REVIEW-F1-SEC2 atas PR #27. Perbaikan yang wajib sebelum merge menambah kode produksi yang tidak bisa dihilangkan tanpa melemahkan jaminan:
- tulis in-place file ber-hard-link lewat temp lengkap, snapshot, backup ber-`fsync`, pre-alokasi, dan pemulihan (CT27-2, CT27-8; ADR-103 butir 13), termasuk `prealloc_*.go`;
- kunci edit per identitas file (CT27-5), mode setuid/setgid/sticky terverifikasi (CT27-9), penghapusan temp yatim lewat fd direktori dengan pola ketat (CT27-10);
- pemuatan ignore yang tidak pernah membaca file workspace di luar root (CT27-3) dan lewat entri non-reguler (CT27-4);
- dua bug parity `rg` (baris kosong semu setelah newline akhir, `$` sebelum `\r\n`) dan prefilter literal untuk CT-01/CT-02.

Duplikasi sudah dibuang lebih dulu (satu jalur open sandbox untuk baca/tulis; `recheckDeny`/`recheckIdentity`/`refuseLinked` disatukan di sandbox). Hasil terukur setelahnya: `codetools` 4.764 (scc, semua GOOS).

**Keputusan.** Baris `codetools` di tabel ENGINEERING §2.4 dan `loc.budget` naik 4.500 → 4.800; total core per paket 33.900 → 34.200 ≤ 47.000 (budget gabungan SPEC §3.2.4 untuk core + codetools + exec + heal, ADR-033). `sandbox` + `redact` tetap 1.450 (terukur 1.447).

**Ditolak.** (a) Membuang fitur atau test seam untuk masuk 4.500: jalur in-place tanpa backup/pemulihan adalah temuan BLOCKER CT27-2. (b) Memindahkan kode ke paket lain demi angka (memecah satu engine tanpa alasan desain). (c) Menaikkan baris codetools di SPEC §3.2.4: tidak perlu, karena tabel per paket berada di dalam budget gabungan.

**Dampak terukur (SPEC §20).** Token 0, dependency 0. LOC: lihat di atas; sisa 36 baris untuk optimasi kinerja CT-01..03.

## ADR-108 — Budget LOC `codetools` 4.800 → 5.350 dan `sandbox`+`redact` 1.450 → 1.700 (prefilter CT-01..03, open relatif fd direktori)

**Status:** Accepted 2026-10-20; diperbarui 2026-10-20 (codetools 5.250 → 5.350 setelah pengukuran open relatif fd direktori + pruning `deny_paths`). Merevisi tabel ENGINEERING §2.4 dan `loc.budget`; SPEC §3.2.4 tidak berubah.

**Konteks.** ADR-111 menyisakan 36 baris untuk optimasi CT-01..03; pengukuran pada korpus terkunci menunjukkan target CODE-TOOLS §10.2 belum tercapai (C1 literal 1,84×, regex 2,68×, glob 1,75× `rg`). Optimasi terukur yang tidak bisa dihilangkan tanpa kehilangan hasil: buka file lewat `openat(O_NOFOLLOW)` relatif ke direktori walk yang sudah divalidasi + pruning `deny_paths` per direktori (`sandbox`), ignore terindeks, prefilter kelas wajib regex, dan sniff ASCII satu lintasan. Hasil: C1 regex 2,68× → 1,85×, glob 1,75× → 1,34×, literal 1,84× → 1,75×; kode produksi menjadi `codetools` 5.235 dan `sandbox`+`redact` 1.639 (scc --no-gen, semua GOOS).
**Keputusan.** `codetools` 4.800 → 5.350 dan `sandbox`+`redact` 1.450 → 1.700 di tabel ENGINEERING §2.4 dan `loc.budget`. Total core per paket 34.200 → 35.000 ≤ 47.000; total proyek ≤ 190.000 (tetap). Tidak ada baris lain yang berubah. Sisa gap terhadap 1,2×/1,5× bersifat struktural: setiap baris kandidat diverifikasi RE2 Go (backtracker ≈1,5 µs/baris), sedangkan `rg` memakai lazy DFA + SIMD memchr; menutupnya butuh executor DFA byte-level (di luar lingkup F1).

**Ditolak.** (a) Membuang prefilter untuk muat 4.800: kehilangan perbaikan terukur di atas. (b) `//nolint` massal untuk kompleksitas: E-52 membatasi density; fungsi dipecah menjadi helper. (c) Menaikkan baris `codetools` di SPEC §3.2.4.

**Dampak terukur (SPEC §20).** Token 0, dependency 0.

## ADR-113 — `internal/config` F1: kunci minimum, TOML ketat, DI path, dan immutabilitas E-13

**Status:** Accepted 2026-10-19 (keputusan didelegasikan). Menutup butir `config` ADR-066 butir 4; tidak mengubah SPEC §12 dan tidak menambah dependency di luar `deps.allow`.

**Aturan penerapan.** Diterapkan di `internal/config` lewat dua PR (E-84): (a) struct/default/decode ketat/DI; (b) validasi rentang + path per OS/profil + `Clone` immutable. Rujukan: SPEC §4.9, §12, §11 (lokasi `<data_dir>`), P29/GAP-SEC-11 (`log.message_content*`); ENGINEERING E-11, E-13, E-19, E-48; OPS-01 (`os.Getenv` hanya di `internal/config`).

**Konteks.** ADR-066 mewajibkan `config` minimal di F1: struct + validasi + default SPEC §12 hanya untuk kunci yang dipakai paket F1. Config penuh §12 memuat section F2+ (`[context]`, `[memory]`, `[recovery]`, `[ui]`, modul); memasukkannya sekarang akan mengunci desain section yang belum dibangun. `github.com/BurntSushi/toml` sudah ada di `deps.allow`.

**Keputusan.**
1. **Cakupan kunci F1.** `[provider]` model/fallback/light/max_output; `[loop]` max_iterations/max_wall_s/max_identical/turn_max_input_tok/turn_max_output_tok/parallel_tools; `[budget]` turn_usd/session_usd/daily_usd/monthly_usd/warn_at/providers; `[approval]` mode/shell_allow/deny_paths/add_dirs/preview_timeout_s; `[exec]` default_timeout_s/idle_timeout_s/allow_destructive/kill_grace_s/env_allow/env/pass_ssh_agent; `[agent]` verify_on_stop; `[log]` level/message_content/message_content_ttl. Section F2+ **ditolak sebagai key tak dikenal**; menambah section kelak murni aditif. Ditolak: menerima semua section §12 sekarang (mengunci desain F2) atau `map[string]any` (kehilangan tipe/validasi).
2. **Default = SPEC §12.** `Default()` memuat angka §12 persis; `log.message_content=false` dan TTL 24 jam dari P29/GAP-SEC-11 (maks 72 jam). `TestConfigDefaultsMatchSpec` membandingkan struct dengan angka tersebut.
3. **Decode ketat.** Decode di atas `Default()` sehingga key absen mempertahankan default; `MetaData.Undecoded()` menghasilkan `ErrUnknownKey` untuk key pertama dalam urutan dokumen, dengan path bertitik dan nomor baris hasil pemindaian section/`key = value` (BurntSushi tidak mengekspos posisi key yang tak ter-decode). Error sintaks memakai `toml.ParseError.Position.Line`.
4. **Rentang validasi.** `max_iterations` 1..40; `max_identical` 2..5; `turn_max_input_tok` ≤ 400.000; `turn_max_output_tok` ≤ 32.000; `kill_grace_s` 1..3; `default_timeout_s`/`idle_timeout_s` 1..600; `warn_at` (0, 1]; semua USD finite ≥ 0; `mode`/`verify_on_stop`/`log.level` enum tertutup; `shell_allow` = daftar argv non-kosong tanpa whitespace/NUL; `env_allow`/`env` = nama variabel POSIX; `message_content_ttl` (0, 72h]. Pelanggaran = `ErrInvalidValue` dengan path key.
5. **Immutabilitas E-13.** `Load`/`Parse` mengembalikan `Config` by value; `Validate`/`Clone`/`DenyPaths` memakai pointer receiver tetapi tidak pernah memutasi receiver; `Clone` menyalin setiap slice/map (termasuk `shell_allow` bersarang). `TestConfigImmutable` menjalankan 30 skenario baca/clone lalu `cmp.Diff` kosong. Reload tetap tugas `cmd/damnbot` (`atomic.Pointer`).
6. **DI eksplisit (E-11).** `Deps{ReadFile, Getenv, HomeDir, GOOS}`; `New` menolak dependency nil (`ErrDeps`). Konstruktor wiring produksi (`OS()`) **ditambahkan bersama konsumen pertamanya** (composition root `cmd/damnbot`) agar tidak ada simbol ekspor mati di PR config; sampai saat itu seluruh test menginjeksi `Deps`. Tidak ada fallback diam-diam ke I/O nyata.
7. **Path SPEC §4.9/§11.** Linux: `$XDG_CONFIG_HOME|~/.config` + `damnbot`, `$XDG_DATA_HOME|~/.local/share` + `damnbot` (XDG relatif diabaikan); macOS: `~/Library/Application Support/damnbot` untuk keduanya; Windows: `%APPDATA%\damnbot` dan `%LOCALAPPDATA%\damnbot`. Profil = `<base>/profiles/<name>`; nama profil dibatasi `[a-z0-9][a-z0-9_-]{0,63}` sehingga tidak bisa keluar direktori. `DenyPaths(configDir)` menyalin pola dan mengganti `<config_dir>`.
8. **Config minimum 2 baris.** Loader tidak menegakkan minimum (ROADMAP F3 yang menegakkannya di perintah config); file dua baris `[provider]\nmodel = "…"` valid sekarang (`TestConfigMinimum`).
9. **Error tanpa panic.** Semua kegagalan adalah `*Error` yang membungkus sentinel `ErrUnknownKey/ErrInvalidValue/ErrSyntax/ErrRead/ErrTooLarge/ErrDeps/ErrProfile/ErrHome`; `Unwrap() []error` mempertahankan cause (mis. `fs.ErrNotExist`). `FuzzConfigTOML` memastikan input arbitrer tidak panic dan hanya menghasilkan error bertipe.

**Dampak terukur (SPEC §20).** Token 0. LOC `config` 648/1.200 (scc Code). Dependency: +`BurntSushi/toml` v1.6.0 (runtime) dan `google/go-cmp` v0.7.0 (test), keduanya sudah di `deps.allow`; binary bertambah < 100 KB.

**Konsekuensi.** (+) Default F1 terukur dan typo key gagal saat start dengan path/baris. (−) File config yang sudah memuat section F2 ditolak sampai fase pemiliknya menambah struct; ini disengaja agar penambahan section terlihat di review.

## ADR-112 — Budget `exec/classify`: 1.600 → 1.800 (aturan keamanan stack classify)

**Status:** Accepted 2026-10-20 (keputusan owner saat review stack classify #41–#46; melengkapi ADR-106 butir 10; naikkan budget, jangan pangkas aturan).

**Konteks.** ADR-106 butir 10 menetapkan `exec/classify` 1.600 dengan proyeksi ≈1.525 scc. Implementasi menambah aturan yang tidak ada di proyeksi: rekursi builtin `cmd` (`start`/`call`; `if`/`for`/`goto` = `unknown`), wrapper launcher (`setsid`, `busybox`, `unshare`, `systemd-run`, `taskset`), lexer token PowerShell/cmd (alias, `$env:`/`%VAR%`, `-EncodedCommand`), serta `DirectArgv`/`MatchAllow` untuk `shell_allow` argv-persis dan identitas biner (NEW3-04). `loc-budget` merah: #111 = 1.682, #112 = 1.737 baris non-test (scc), > 1.600.

**Keputusan.** `loc.budget` `exec/classify` 1.600 → 1.800; Σ core 33.900 → 34.100 (≤ 47.000, ADR-033); TOTAL 190.000 tidak berubah. Headroom 63 baris (3,6%) untuk #112. Tidak ada aturan atau test yang dipangkas. Test table-driven yang menyertai aturan tidak dihitung gate (hanya file non-test).

**Alternatif ditolak.** Memangkas komentar/aturan agar muat 1.600: bertentangan dengan preferensi kejelasan ADR-106 butir 10 dan menurunkan cakupan label keamanan. Memecah paket: churn API/archtest tanpa manfaat.

**Konsekuensi.** Cakupan label keamanan dan `shell_allow` tetap utuh; budget naik tercatat (bukan diam-diam), dengan sisa 63 baris sebelum perlu ADR berikutnya.

## ADR-114 — `internal/assemble` F1: tier, injeksi estimator/redaksi, sub-cap, dan fixture `f1-core`

**Status:** Accepted 2026-10-19 (keputusan didelegasikan). Menerapkan ADR-066/067/082 di F1 dan menutup alokasi NEW-41 (ADR-086).

**Aturan penerapan.** Diterapkan di `internal/assemble` lewat tiga PR (E-84): (a) stable/katalog/volatile/runtime + DI; (b) context + filter schema per fase; (c) `Build`/wire + fixture P1 + hook modul `bench`. Rujukan: SPEC §4.5, §5.1, §5.2, §6; ADR-066, ADR-067, ADR-079 butir 1, ADR-082, ADR-086 NEW-41, NEW-52; ENGINEERING §2.2.1 (graf impor), E-68, E-11.

**Konteks.** Graf impor (`internal/archtest/allowed.go`) hanya mengizinkan `assemble → loop/event`: paket ini tidak boleh mengimpor `provider`, `budget`, `sandbox`, atau `redact`, sedangkan SPEC §4.5 menulis `Build(...) (provider.Request, …)` dan formula token COST-09 hidup di `budget` serta tokenizer o200k hanya di modul `bench`. Context file wajib dibaca lewat sandbox (SPEC §14.2), dan P1 harus terukur lintas modul tanpa menambah require root (ADR-066).

**Keputusan.**
1. **DI menggantikan impor.** `Deps{Tokens Counter, Scrub func(string) string}`; keduanya wajib (`ErrDeps`). Composition root mengisi `budget.Count(s).Raw` dan `redact.Redactor.String`. `Scrub` diterapkan ke **semua** teks pemanggil sebelum masuk prompt: file konteks (setelah dibaca), facts, workspace, memori, skill, todo, nama baru, dan notice volatil. Context file dibuka hanya lewat `FileReader.Open` (implementasi composition root memakai `sandbox.OpenFile` + verdict `Inside`); `ErrUnavailable`/`fs.ErrNotExist` membuat file dihilangkan, bukan dibaca di luar sandbox. `Build` mengembalikan `assemble.Request` (messages + tools), yang dipetakan loop ke `provider.Request`; ini deviasi sadar dari tanda tangan SPEC §4.5 yang tidak mungkin dipenuhi tanpa edge impor baru.
2. **Stable tier kondisional (ADR-067, NEW-52).** Tabel aturan §6 dirender berurutan; aturan 3 hanya dengan `StableOptions.Deferred`, dan frasa `repo_map` hanya dengan `Codeintel` (varian F1 tetap memuat panduan grep/glob). Aturan dinomori ulang contiguous: F1 = 14 aturan, coding = 10–14. Golden per varian {F1, deferred, codeintel} (`TestStableTierGolden`); `TestPromptNoUnavailableTools` memastikan tidak ada nama tool di luar array tools + katalog sesi.
3. **Context tier (SPEC §6).** Sub-cap: facts 60, workspace 150, files 400 (min 200), memory 300 (pinned ≤ 150 dulu), skills 150 (≤ 3), total 1.100; heading termasuk dalam cap slot dan pemisah dihitung di total. Urutan potong: files → 200, lalu memori non-pinned dari ekor, lalu skill dari ekor, terakhir hard clip. File dipotong dengan penanda `[truncated; read_file <path>]`; satu file dibatasi 256 KiB saat dibaca.
4. **Volatile beku per turn (ADR-067 butir 2).** `Assembler.Freeze(*VolatileInput) *Turn` merender sekali; sub-cap waktu 12, budget 15, todo 30 (≤ 3 item terbuka, tiap item 40 char, `+N more`), nama 20, notice gabungan 23; Σ 100 dengan urutan buang notice → nama → todo → budget, lalu clip. `Turn.String()` dipakai ulang setiap iterasi dan ditambahkan ke **user message terakhir**, sehingga prefix tidak bergerak (`TestPrefixStableWithinTurn`). `user_changed` masuk slot notice, bukan `<runtime>` (ADR-082).
5. **Sufiks `<runtime>` terpisah (ADR-082).** `Assembler.Runtime` merender komponen `n/40` (6), `no_progress` (14), notice proses (20) dan `RuntimeString` menjaga total ≤ 40 dengan membuang proses lebih dulu, lalu `no_progress`. Sufiks disimpan loop di payload `tool_result`; assemble tidak pernah menulis ulang `tool_result` lama, yang membuat render ulang byte-identik.
6. **Schema per fase + wire.** `FilterTools(PhaseF1|F5, …)` memvalidasi JSON, mengurutkan nama byte-wise, dan untuk F1 menghapus properti/`required`/kalimat `background` dari schema `shell` (idempoten). `MarshalRequest` menghasilkan `{"messages":…,"tools":…}` tanpa HTML escaping (sama dengan encoder `provider`).
7. **Fixture P1 `f1-core` (ADR-066).** `TestFixedOverheadGolden` merender request + `f1-core.tiers.json` (stable/catalog/context/volatile/tools + `context_slots`) dengan `-update`; `bench/budget TestFixedOverhead/f1-core` mengukur keduanya dengan o200k_base dan menegakkan total ≤ 4.000 serta alokasi NEW-41 (stable ≤ 1.000, context ≤ 1.100, volatile ≤ 100, tools ≤ 1.800) dan sub-slot context. Fixture mengisi setiap slot sampai cap estimator (fixture memakai `Counter` ASCII = `ceil(len/4)`, identik dengan `budget.Count.Raw` untuk teks ASCII) dan memakai filler yang rasio o200k-nya ≥ 4 byte/token sehingga estimator tidak pernah under-count. Hasil terukur: stable 341, catalog 0, context 812, volatile 92, tools 897, **total 2.203** token o200k (gate 4.000; hermes 13.819–16.843).
8. **F2 tetap aditif.** Katalog deferred, retrieval memori, skill nyata, schema pinning, summary, dan riwayat panjang tidak diimplementasikan di F1; `Catalog` menerima grup dari pemanggil dan `Estimate.Pinned/Summaries/Retrieval` tetap 0.

**Dampak terukur (SPEC §20).** Token: P1 `f1-core` 2.203 o200k (stable 341 + context 812 + volatile 92 + tools 897); volatile tetap ≤ 100 dan `<runtime>` ≤ 40 per iterasi. LOC `assemble` 993/1.000 (scc Code). Latensi/binary/dependency 0 (semua lewat DI).

**Konsekuensi.** (+) P1 terukur sejak F1 tanpa require baru di root dan tanpa melanggar graf impor; prompt tidak pernah menyebut tool yang tidak ada dan prefix stabil dalam turn. (−) Dua artefak golden harus diregenerasi setiap prompt/schema berubah (diff terlihat di review); `Build` memakai tipe `assemble.Request`, sehingga loop perlu satu pemetaan kecil ke `provider.Request`.

## ADR-109 — `internal/tools` F1: pipeline dispatch, approval model, spill, dan journal konsumen

**Status:** Accepted 2026-10-19 (keputusan didelegasikan). Menerapkan SPEC §4.3, §4.5, §5.1, §7, §14.1–§14.2, §14.5; ADR-008, ADR-031, ADR-069, ADR-070, ADR-079, ADR-082, ADR-088, ADR-095, ADR-103, ADR-105, ADR-106; ENGINEERING E-09, E-21, E-37, E-68.

**Aturan penerapan.** Diterapkan di `internal/tools` sebagai PR berantai (E-84): tipe/registry/approval, pipeline dispatch, tool file + todo + clarify, shell, lalu dokumen. Tool file memakai `internal/codetools`, shell memakai `internal/exec` + `internal/exec/classify`, validasi argumen memakai `internal/schema`, dan path file melewati `internal/sandbox`. Setiap celah memilih opsi paling konservatif di dalam budget 1.800 LOC.

**Errata (2026-10-20, REVIEW-F1-SEC7 TL7-12).** Edge `internal/tools → internal/boundary` ditambahkan ke matriks E-01 (`internal/archtest/allowed.go`) dan graf ENGINEERING §2.2.1 (`TOOLS --> BND`). Edge ini diizinkan karena hanya `internal/boundary` yang boleh memanggil `recover` (E-24), dan `Prepare`/`DispatchPrepared` memakai `boundary.Run` agar panic sebuah tool menjadi error terklasifikasi (`internal`) alih-alih mematikan turn. Tidak ada keputusan policy yang berpindah ke `boundary`.

**Keputusan.**
1. **Journal konsumen (E-09), wajib.** `tools.Journal` = 2 metode (`ToolDispatch`, `ToolResult`) dengan parameter primitif (`turnID`, `callID`, `tool`, `effect`, hash argumen 32 byte, `ok`, kode, konten); tidak ada implementasi no-op. `Config.Journal` wajib di `NewDispatcher`; composition root mengadaptasi ke event `tool_dispatch`/`tool_result` (loop/event) saat store F2 tersedia. Kegagalan `ToolDispatch` menghentikan call sebelum `Run` (`internal`); kegagalan `ToolResult` tidak bisa membatalkan efek samping yang sudah terjadi dan diteruskan ke terminal turn lewat jalur journal store (dicatat sebagai alasan F2 menambah transaksi tunggal).
2. **Satu otoritas per langkah (`loop.Toolset`).** `Dispatcher.Prepare(ctx, Call) (Prepared, error)` menjalankan langkah 1–6 (lookup, JSON/schema, effect, sandbox, taint gate, `Inspector`) dan mengembalikan `Prepared{Call, Args, Effect, Decision (Requirement, Paths persis, Preview, Class, AllowSession), Env, Approved}`; ia tidak memanggil approver, journal, atau `Run`. `Dispatcher.DispatchPrepared(ctx, *Prepared)` menjalankan langkah 8–10 (journal, `Run`, redaksi/paging/spill, journal result) tanpa policy ulang. `Dispatcher.Dispatch(ctx, Call)` = `Prepare` + approval dispatcher + `DispatchPrepared` untuk frontend mandiri. Loop memakai `Prepare` + approver-nya + `DispatchPrepared` (adapter composition root), sehingga policy/approval tepat sekali; `Decision.Paths` adalah subject `allow_session` persis dan shell selalu `AllowSession=false`.
4. **Approval kelas.** Tool boleh mengimplementasikan `Inspector` (satu metode) untuk memutuskan `RequireAuto|Ask|Deny|Outside|DeniedPath` beserta subject path, preview, label audit, dan `AllowSession`. Tool tanpa Inspector memakai kebijakan kode-tool: baca auto; tulis auto di `auto-edit`/`auto`, tanya di `ask`, `denied` di headless. `deny_paths` selalu `denied_path` dan tidak dapat di-approve. Verdict `outside_root`/`hardlinked_file` menjadi `outside_workspace` di headless dan prompt di mode interaktif; `allow_session` hanya untuk himpunan path persis. Approver nil atau jawaban tak dikenal gagal tertutup (`denied`). Shell memakai `classify.Decide` apa adanya: label tidak pernah menghasilkan auto; satu-satunya auto adalah `shell_allow` argv-persis dengan resolver binary dari composition root (paket tools tidak membaca PATH/disk), dan `allow_session` tidak pernah ditawarkan.
3. **Taint gate (ADR-070).** `Dispatcher.MarkTainted`/`BeginTurn` menyimpan taint per turn (`atomic.Bool`). Setiap effect non-read saat tainted dipaksa `Ask` tanpa `allow_session`, dan headless menjadi `denied`; baca workspace tetap auto. Hasil `read_file`/`grep`/`glob` di luar root dibungkus `<untrusted source="file:outside_workspace">`, dan semua baca di workspace untrusted dengan `<untrusted source="workspace">`; keduanya men-taint turn.
4. **Spill (P5/ADR-031).** `tools.Spill` = `Put`/`Get`; `FileSpill` menulis `<data_dir>/blobs/<sha[0:2]>/<sha>` lewat `blobs/tmp` + `fsync` + rename (layout SPEC §11.6). Result non-paged > 4.000 char atau melewati agregat turn 24.000 char di-spill dengan preview 800 char (400 char setelah agregat habis) plus path; `read_file`/`grep`/`glob` tidak pernah di-spill dan menerima `PageChars` 16.000 → 4.000 setelah agregat (ADR-031). Shell menyerahkan ringkasan ke `exec.Summarizer` dan menyimpan blob `log`/`raw` lewat `Spill` yang sama.
5. **Kode error tertutup (E-21).** Seluruh kode error adalah nilai bertipe `tools.Code` dari daftar SPEC §4.3; `*codetools.ToolError` dipetakan lewat `errors.As` (bukan teks), kode di luar daftar menjadi `internal`, dan error tak dikenal dibungkus `internal`. Envelope error dirender JSON deterministik `{"ok":false,"error":{code,message,hint}}`.
6. **Validasi argumen.** `internal/schema` mode internal mengompilasi `parameters` setiap tool; `Registry.Validate` menjalankan satu lintasan (0 alokasi untuk 1 KiB) dan melaporkan satu pelanggaran pertama dengan JSON Pointer (≤ 5 sesuai SPEC). Nilai `null` pada level teratas dibuang sebelum validasi sehingga argumen opsional memakai default schema (EC-TCALL-008); properti wajib yang `null` tetap gagal `invalid_args`.
7. **Idempotensi call.** `Dispatcher` menolak `tool_call_id` yang berulang dalam satu turn dengan `idempotency_conflict` dan tidak menjalankan tool dua kali (EC-TCALL-002); `BeginTurn` mereset penjaga, taint, dan agregat inline.
8. **Tool inti.** Delapan tool eager: lima file (adapter `codetools`), `shell`, `todo`, `clarify`. `clarify` ditambahkan sesuai FEATURE-PARITY TOOL-021/CORE-019 dengan `Asker` (satu metode); headless menolak `denied`. `todo` menimpa daftar penuh dan menolak id duplikat. Shell F1 tidak mengekspos `background` (ditolak schema) dan menolak kombinasi `session`+`stdin`/`session`+`cwd` dengan `invalid_args`; sesi persisten memakai `exec.Sessions` (tanpa stdin, ADR-105 butir 22).
9. **Preview tanpa membaca workspace.** Preview `write_file`/`edit_file` dibentuk dari argumen (≤ 20 baris/120 char) karena pipeline tidak boleh menyentuh workspace sebelum approval; preview shell memuat command, kelas, flag, dan ≤ 3 alasan classifier.
10. **Timeout.** `Config.ToolTimeout` (0 = hanya context pemanggil) membungkus `Run`; shell tetap memakai `timeout_s` 1–600 detik. Context yang berakhir menghasilkan `timeout` + `Outcome.Cancelled`, dan journal tetap mencatat hasil.
12. **Batas ukuran.** `Result.Content` di-redaksi dengan `redact.Redactor` sesi sebelum paging/spill; `Meta` tidak pernah memuat isi file, hanya fakta (`blob_sha`, `chars`, status/exit shell).

**Dampak terukur (SPEC §20).** Token: 0 tambahan tetap (spill hanya mengganti isi model dengan preview ≤ 800 char). Latensi E-37 lokal linux/amd64 (load 1,0–2,4, `-cpu 2`): `BenchmarkDispatch` 1 KiB **2,8 µs/op, 0 allocs/op** (target ≤ 20 µs/≤ 2 alloc). LOC `internal/tools` 1.743 baris kode (hitungan non-komentar/non-kosong; scc Code ≤ angka itu) di dalam baris `tools 1800` `loc.budget`. Dependency 0 baru. Test: golden sukses+error per tool (E-68), `TestDispatchAllocs`, matriks approval 5 mode × 7 aksi, matriks taint, spill roundtrip/agregat, EC-TCALL 001–003/005–008, `TestErrorCodeSet`.

**Konsekuensi.** (+) Satu jalur dispatch untuk semua tool; kebijakan shell sepenuhnya di classifier; tidak ada akses workspace sebelum approval; error model-facing stabil dan tertutup. (−) `Journal` wajib sehingga unit test dan composition root harus menyediakan implementasi (test double di test); transaksi tunggal `tool_dispatch + turn_journal + tool_result` baru terpenuhi saat store F2 ada; preview write/edit bukan diff terhadap isi file sampai frontend F3/F4 menghitungnya dari snapshot.

**Review keamanan (REVIEW-F1-SEC7, 2026-10-20).** 1 BLOCKER + 5 MAJOR + 3 MINOR; semuanya ditangani sebelum merge. (a) **TL7-4 (BLOCKER)** `shell` tidak lagi menurunkan mode: `Inspect` menyimpan mode eksekusi yang disetujui (`Prepared` per `call_id`, argv persis atau shell) dan `Run` menolak (`denied`) bila `shell_allow` muncul/hilang/berubah argv di antaranya — tidak ada fallback ke `sh -c`. (b) **TL7-1** `Result.Err`/`OK` dihormati: kegagalan yang dilaporkan tool tanpa error Go dirender sebagai error, bukan sukses. (c) **TL7-2** escape batas `<untrusted>` tidak lagi case-sensitive (`</UNTRUSTED>` tidak bisa menutup wrapper). (d) **TL7-3** `Redactor` wajib di `NewDispatcher` (tanpa redaksi = gagal tutup). (e) **TL7-5** `cwd` di luar root ditolak `outside_workspace` di `Inspect` dan `Run`. (f) **TL7-6** tidak ada `OneShot.SecretRefs` di API `exec` final; `exec.EnvPolicy.Secrets` (scope binary, ADR-088 butir 4) sudah diteruskan `shell` dan dibuktikan `TestShellInjectsScopedSecret`. (g) **TL7-7** `Effect` memakai `cwd` panggilan. (h) **TL7-8** agregat inline habis tetap 0 (tidak direset ke 24.000). (i) **TL7-11** `DispatchPrepared` menolak `Prepared` yang belum diotorisasi (`authorize` dipanggil bila perlu), menegakkan `RequireDeny`/`RequireDeniedPath` tanpa melihat klaim `Approved`, dan menolak eksekusi kedua. (j) MINOR: `clarify` menjadi `non_idempotent` (interaksi, bukan baca), stdin kosong = `nil`. Test baru: `TestShellAllowTOCTOUFailsClosed`, `TestToolReportedFailureIsRendered`, `TestUntrustedBoundaryCaseInsensitive`, `TestShellCwdOutsideWorkspaceDenied`, `TestShellInjectsScopedSecret`, `TestShellEffectUsesCallCwd`, `TestShellEmptyStdinIsNil`, `TestShellInlineBudgetStaysSpent`, `TestDispatchPreparedDeniesAndIsSingleUse`.

## Catatan implementasi — resolusi `docs/ENGINEERING.md` §2.4 (2026-10-20)

Branch `f1-tools-base` mewarisi penanda konflik yang belum diselesaikan di tabel budget ENGINEERING §2.4 dari penggabungan `f1-classify-4`. Resolusi (REVIEW-F1-SEC7 TL7-14) memakai angka `loc.budget` sebagai otoritas: `codetools` 4.800 (ADR-105), `exec` 3.400 dan `exec/classify` 1.600 (ADR-081, ADR-106). Tidak ada budget yang berubah; hanya penanda konflik yang dihapus dan prosa total core dirapikan (Σ core tetap 34.200).

## ADR-110 — `internal/loop` F1: mesin turn, kosakata stop reason, approval/taint, dispatch, determinisme

**Status:** Accepted 2026-10-19 (keputusan didelegasikan; opsi paling konservatif dalam budget `loop` 1.500 LOC termasuk `loop/event`, tanpa dependency baru). Melengkapi SPEC §3.4, §3.5, §4.2, §4.3, §7.2, §14.1, P5, COST-07/COST-15, REL-06 dan ADR-067/072/082/100/102.

**Aturan penerapan.** Diterapkan di `internal/loop` + `internal/loop/event` lewat lima PR berurutan (E-84): (a) `loop/event` + tipe/interface, (b) turn core (streaming, budget, stop reason), (c) rakitan tool call + dispatch + spill, (d) approval/taint + context guard + cancel/kill grace + no_progress, (e) dokumen + golden. Setiap PR memuat `CHANGELOG`; ADR ini ada di PR (e). Tidak ada perubahan SPEC; setiap penyimpangan istilah dicatat di bawah.

**Keputusan.**
1. **Dua tingkat stop reason.** `event.StopReason` adalah keadaan terminal kasar: `end_turn`, `max_iterations`, `max_tokens`, `cancelled`, `cancelled_confirmed`, `content_filtered`, `error`, `approval_denied`, `tool_error`. `event.StopDetail` membawa nama SPEC §3.5 persis (`completed`, `empty_response`, `no_progress`, `invalid_answer`, `output_truncated`, `context_overflow`, `budget_input_tokens`, `budget_output_tokens`, `cancelled`, `content_filtered`, `provider_error`, `internal`, `approval_denied`, `tool_error`, `max_wall`). Frontend/`run` memetakan `StopDetail` ke exit code/ACP; `end_turn` = SPEC `completed`. Ditolak: memakai nama ACP sebagai `stop_reason` di event `turn_end` (bertentangan §3.5) atau memaksa semua detail menjadi enum kasar (kehilangan alasan yang diwajibkan `sessions why`).
2. **Cancel (REL-06, ADR-072).** `Cancel(session)` dan pembatalan `ctx` menghentikan turn. `cancelled_confirmed` hanya bila pembatalan terjadi saat dispatch dan **setiap** tool yang berjalan mengembalikan `status:"cancelled_confirmed"`; `still_running` atau pembatalan di luar dispatch → `cancelled`. Loop memberi `ctx` yang sama ke `Toolset.Dispatch` dan menunggu `Config.KillGrace` (default 1 s) untuk hasil; tool yang tidak kembali dalam grace dilaporkan `still_running` dan goroutine-nya berakhir saat tool menghormati `ctx` (kontrak Toolset). Batas wall-clock (`loop.max_wall_s`) → `cancelled`/`max_wall`. Ditolak: memetakan `cancelled` ke `cancelled_confirmed` (menyembunyikan proses yang belum terbukti mati).
3. **`approval_denied` vs `denied`.** Penolakan biasa adalah hasil tool `denied` yang dikembalikan ke model (turn lanjut, SPEC §4.3 langkah 7) dan event `tool_rejected`. `approval_denied` hanya untuk kelas yang `Prepared.Terminal` (mis. persetujuan plan) karena turn tidak punya kelanjutan yang sah. Tanpa `Approver` (headless) setiap approval = deny; error `Approver` = deny non-terminal; `ctx` batal saat approval = `cancelled`. `allow_session` disimpan per `(tool, subject)` dan tidak pernah untuk `NoSession`, aksi tainted non-read, atau kelas P26.
4. **`tool_error`.** Hanya error Go non-`*ToolError` dari `Toolset.Prepare`/`Dispatch` (termasuk panic yang di-recover `boundary`) yang mengakhiri turn sebagai `tool_error`; `*ToolError` adalah data ke model dan turn lanjut. Ditolak: mengubah semua error tool menjadi stop (model kehilangan kesempatan memperbaiki args).
5. **Dispatch paralel (SPEC §4.2).** Batch hanya paralel bila **semua** call `EffectRead`, `ApprovalNone`, dan tidak dipaksa taint; batas `Config.ParallelTools` (default 4). Selain itu serial sesuai urutan model agar taint/approval melihat hasil yang sudah commit. Hasil selalu dikembalikan dalam urutan `tool_calls`. Tool read yang butuh approval memaksa jalur serial.
6. **Re-call milik loop (ADR-078 AUD-045).** `empty_response` di-retry tepat sekali ke model yang sama tanpa nudge/prefill (termasuk konten fallback yang hanya spasi, EC-TCALL-012); `context_length` memicu satu compaction lalu satu call ulang; gagal lagi → `max_tokens`/`context_overflow`. Provider tidak mengulang kedua kelas ini (ADR-102).
7. **Taint (ADR-070).** `ToolResult.Tainted` membuat flag turn lengket; setiap aksi non-read berikutnya dipaksa `ApprovalAsk`, `allow_session` diturunkan menjadi `allow_once` dan tidak di-cache. Loop tidak membungkus ulang konten (pembungkusan `<untrusted>` milik tool).
8. **Spill (P5).** Per hasil: > `MaxInline` (4.000 char) → `Spiller.Spill`, preview 800 char + `[spilled: ref]`; tanpa Spiller → envelope `too_large`. Agregat turn > `MaxInlineTurn` (24.000 char) → preview 400 char, dan tanpa Spiller → penanda `[truncated: turn inline budget]` (tidak pernah diam). Hasil sukses yang muat diteruskan byte-identik (EC-TCALL-007).
9. **Determinisme.** Satu-satunya pembacaan jam adalah `Config.Now` (default `time.Now`, test menyuntik clock tetap); `Config.NewTurnID` dapat disuntik; tidak ada `time.Sleep`, `os`, `net/http`, atau goroutine di luar `boundary.Go` (E-26). `TestMain` memakai `goleak.VerifyTestMain` (E-29). `loop` hanya mengimpor `loop/event`, `boundary`, dan stdlib (E-02).
10. **Interface konsumen (E-09).** `Provider` 1 method (`Stream(ctx, *Request, emit)`), `Toolset` 2 (`Prepare`, `Dispatch(*Prepared)`), `Assembler` 1, `Compactor` 1, `Store` 1 (`Append(*event.Event)`), `Approver` 1 (`Approve(*ApprovalRequest)`), `Ledger` 1 (`Record(*Usage)`), `Redactor` 1, `Spiller` 1. `Prepared`/`ToolResult`/`Request`/`Reply`/`Update`/`Event`/`Config`/`Deps` yang besar dilewatkan sebagai pointer (gocritic hugeParam), mengikuti ADR-102 untuk `provider.Request`.
11. **Kosakata event.** `loop/event` menduplikasi `events.kind` SPEC §11.2 sebagai konstanta tertutup; update stream (`text_delta`, `tool_call_start`, `tool_call_args`, `tool_call_end`, `tool_result`, `usage`, `notice`, `stop`) terpisah dan tidak pernah dipersist. Event audit (`approval`, `tool_rejected`, `provider_fallback`, `system_notice`, `error`, `turn_end`) ditulis best-effort melalui context tanpa cancel; kegagalan Store yang sebenarnya terlihat pada append kritis berikutnya (`user_message`, `turn_start`, `assistant_message`, `llm_call`, `tool_dispatch`, `tool_result`) yang mengakhiri turn `error`/`internal`. Ditolak: menelan error Store pada append kritis.
12. **Cakupan EC F1.** Loop mengklaim EC-PROV-001/015 dan EC-TCALL-001, 002, 003, 005, 006, 007, 008, 012 (subtest + `phase.Item`). EC-TCALL-009 (final degeneratif) bukan perilaku loop: deteksinya milik `tools`/verifikasi; loop tidak mengarang aturan baru. EC-TCALL-004/010/011 dan EC-PROV lain berfase F5/F2 (ADR-077).
13. **Batas token/iterasi.** `MaxIterations` 40, `MaxInputTokens`/`MaxOutputTokens` dicek sebelum dan sesudah call, `MaxIdentical` 3 untuk COST-07 (kunci `sha256(tool|args|result)`). Budget guard penuh (harian/bulanan/provider) tetap F2.

**Amandemen 2026-10-20 (REVIEW-F1-SEC8 LP8-16..20).** (1) Dispatch tidak mengembalikan outcome terminal sebelum setiap call yang sudah dijalankan memiliki `tool_result`; call yang tidak pernah mulai ditandai `not_started`, sehingga assistant `tool_calls` selalu berpasangan (termasuk `tool_error` dan `cancelled`). (2) Grant `allow_session` yang di-cache tetap menyetel `Prepared.Approved` sebelum `DispatchPrepared`. (3) `KillGrace` hanya membatasi turn yang dibatalkan: read yang masih hidup selalu mengambil hasilnya, termasuk hasil yang tiba di dalam grace. (4) Redaksi stream memakai penyaring lintas-delta (`Deps.NewRedactStream`, semantik `internal/redact.Stream` ADR-081 butir 3); tanpa penyaring, teks dan argumen tool ditahan sampai assembly lalu diredaksi utuh, sehingga secret yang terpecah antar-delta tidak lolos. (5) Member batch yang tidak pernah mulai dilaporkan `not_started`, bukan `cancelled_confirmed`. Regression: `TestLP816..TestLP820`.

**Dampak terukur (SPEC §20).** Token 0 (tidak ada teks prompt/schema baru). Latensi: turn tanpa tool menambah satu goroutine `boundary` dan satu channel; tidak ada I/O sinkron baru. Penyaring lintas-delta hanya menahan sufiks parsial; tanpa penyaring delta ditahan sampai call selesai (fallback aman, bukan jalur produksi). LOC `loop` + `loop/event` = 1.757 (scc, non-test) / 1.800 (amandemen LP8-16..20); dependency 0. Test: 89,7 % statement; matrix 9 stop reason + EC subtests + golden urutan update dan render tool (E-68).

**Konsekuensi.** (+) Semua keadaan terminal punya alasan SPEC yang eksplisit, approval/taint/cancel dapat diuji tanpa jaringan, dan paket tetap murni (tanpa I/O/jam nyata). (−) Paralelisme read hanya untuk fast path all-read; batch campuran berjalan serial sampai tools membuktikan kontrak ctx per tool. `cancelled_confirmed` adalah istilah internal, bukan `stop_reason` SPEC; pemetaan ke `cancelled` dilakukan di frontend.

## ADR-115 — Toolchain Go 1.26.10 (GO-2026-6809)

**Status:** Accepted 2026-10-21. Menggantikan angka pin ADR-051; ADR-051 tetap menjadi catatan historis keputusan 1.26.9.

**Konteks.** `govulncheck` (job `vuln`) melaporkan GO-2026-6809: stdlib `crypto/x509` masih mempercayai sertifikat intermediate yang seharusnya ditolak. Perbaikan tersedia di Go 1.26.10. Repo mem-pin `go 1.26.9` di tiga modul (ADR-051), sehingga `vuln` merah tanpa ada dependency baru.

**Keputusan.** Direktif `go 1.26.10` di `go.mod`, `bench/go.mod`, `tools/go.mod` (tanpa baris `toolchain` terpisah: `go mod tidy -diff` bersih di ketiganya). CI `actions/setup-go` membaca `go-version-file` (`go.mod`/`bench/go.mod`) sehingga ikut naik; tidak ada pin versi literal di `.github/` atau `Makefile`. Baris Status `docs/ENGINEERING.md` diperbarui ke 1.26.10. Catatan pengukuran historis yang menyebut `go1.26.8`/`go1.26.9` tidak diubah.

**Konsekuensi.** (+) `vuln` hijau untuk GO-2026-6809; toolchain lokal dan CI konsisten di 1.26.10. (−) Kontributor dengan Go lokal lebih lama bergantung pada `GOTOOLCHAIN=auto` untuk mengunduh 1.26.10.

## ADR-116 — `write_file` membuat path yang belum ada tanpa memedulikan tag

**Status:** Accepted 2026-10-21 (temuan C13 matriks skenario LLM; menggantikan aturan CODE-TOOLS §5.6 "tag pada file baru → `not_found`").

**Konteks.** Untuk prompt "buat file baru", model mengirim `tag:"00000000"` sebagai placeholder karena schema hanya mewajibkan `path`/`content`. Tool menolak dengan `not_found`, model mengulang bentuk panggilan yang sama, dan turn berakhir `no_progress`. Path yang belum ada tidak punya snapshot untuk diverifikasi, sehingga tag tidak memberi jaminan keamanan di jalur create.

**Keputusan.** `write_file` membuat path yang belum ada apa pun nilai `tag`; format `^[0-9a-f]{8}$` tetap divalidasi seperti argumen lain, tag diabaikan, dan hasil menandai `tag_ignored` plus notice singkat. Untuk file eksisting tidak ada perubahan: tanpa tag → `stale_read`, tag basi → `stale_read`, tag benar → timpa. Deskripsi tool dan CODE-TOOLS §5.6 diperbarui.

**Konsekuensi.** (+) Bentuk panggilan natural model berhasil dan stale-read untuk file eksisting tetap fail-closed. (−) Deskripsi schema bertambah ~10 karakter (masih ≤ 160 char); tidak ada I/O atau alokasi baru di jalur create. Test: create dengan tag nol/arbitrer, `stale_read` untuk tag salah, overwrite dengan tag benar.

## ADR-117 — `internal/journal` F2: interface konsumen, `journalstore`, dan undo terverifikasi

**Status:** Accepted 2026-10-21. Implementasi deliverable F2 `journal` (SPEC §18.1, P26; ROADMAP F2).

**Konteks.** SPEC §18.1 mewajibkan setiap aksi file dicatat sebelum side effect dengan inverse before-image, dan undo harus mengembalikan byte identik atau menolak `conflict` bila file berubah. Tabel `actions`/`action_undos` sudah ada di migrasi 0006 dan append-only (§11.8). Dua hal tidak dinyatakan eksplisit: (a) di mana hash after-image disimpan, karena `actions` tidak dapat di-UPDATE dan `turn_journal` dihapus saat turn selesai; (b) bentuk interface antara paket konsumen dan storage (ADR-074 mewajibkan interface di konsumen, E-09 ≤ 5 method).

**Keputusan.**
1. `internal/journal` mendeklarasikan tipe (`Record`, `Action`, `Image`, `Status`) dan satu interface `Store` 5 method (`Record`, `Get`, `List`, `Blob`, `Finish`). Paket ini tidak mengimpor `ctxdb`; ia boleh mengimpor `sandbox` untuk executor undo.
2. `internal/ctxdb/journalstore` mengimplementasikan `journal.Store`. Berbeda dari `ledgerstore` (tipe kembar tanpa saling impor), `journalstore` mengimpor `internal/journal` agar hanya ada satu definisi tipe aksi; arah edge `journalstore → journal` ditambahkan ke matriks E-01 dan tidak membuat siklus (`journal` tidak mengimpor storage).
3. Before-image **dan** after-image disimpan sebagai blob content-addressed dengan kunci `inverse.before.blob_sha`/`inverse.after.blob_sha` (`BlobRefsOf` menurunkan `blob_refs` owner `action`). After-image memberi hash pembanding penuh untuk deteksi konflik **dan** memungkinkan undo atas undo (redo) tanpa membaca state lain; `bytes_held` menjumlahkan byte unik kedua image.
4. Crash safety memakai urutan record-sebelum-side-effect, bukan kolom state (tabel append-only). Undo tidak mempercayai journal sendirian: ia menghitung sha256 isi sekarang dan membandingkannya dengan before/after. Isi = before → no-op idempotent (`undone`), isi = after → pulihkan before, selain itu `conflict` (kecuali `--force`). Aksi yang tercatat tetapi crash sebelum side effect otomatis aman. Aksi dengan `turn_journal` masih terbuka ditolak `pending`.
5. Retensi: `T_cut = max(now − retention_days, created_at aksi tertua yang masih muat saat bytes_held dijumlahkan dari terbaru ≤ max_bytes)`; default 30 hari / 2 GiB. Aksi `created_at < T_cut` → undo `expired`; rujukan blob dilepas gc pada `T_gc` yang sudah ada (§11.6).
6. Executor undo memvalidasi path lewat `sandbox.Check`/`OpenFile` (deny_paths menang, symlink keluar root ditolak, hard link ditolak kecuali `--force` + tulis in-place), menulis byte sebelum lewat temp + fsync + rename dengan mode before (termasuk setuid/setgid/sticky), dan mencatat hasil lewat `Finish` (event `undo` + `action_undos`) di sesi pemanggil.

**Ambiguitas SPEC §18.1.** Lokasi hash after-image tidak ditentukan. Pilihan konservatif: simpan after-image sebagai blob (bukan hash telanjang di JSON, yang akan ditolak validator `BlobRefsOf` karena bukan kunci `*_blob_sha`) sehingga konflik selalu dibandingkan dengan sha256 penuh. Blob after hanya dipakai nilai sha-nya untuk deteksi konflik (isi tidak dibaca saat undo); blob **before** yang hilang membuat undo `failed`, tidak pernah sukses diam.

**Konsekuensi.** (+) 100% aksi file punya inverse byte-identik; undo idempotent dan aman terhadap crash; retensi mengikuti angka SPEC. (−) Dua image per aksi menambah penyimpanan (dibatasi retensi); `journalstore` bergantung pada paket konsumen (bukan sebaliknya), satu edge matriks baru. LOC: `journal` ≤ 500 (diamandemen: 545 di Amandemen PR3, 560 di Amandemen PR4), `ctxdb/journalstore` ≤ 400 (baris `loc.budget` sudah ada, tidak dinaikkan). Dependency baru: 0. Test: `TestRecordGetRoundTrip`, `TestRecordBlobRefs`, `TestRetentionExpiry`, `TestAppendOnly`, `TestOpenTurnRefuses`, `TestUndoSuite` (≥ 100 skenario).

**Amandemen PR2 (executor undo).** Edge `journal → sandbox` ditambahkan ke matriks E-01/§2.2.1: executor memakai satu-satunya jalur akses file yang sudah diaudit (deny_paths, anti-TOCTOU, hard-link verdict). Hard link hanya ditulis in-place saat `--force`; tanpa `--force` undo menolak (`ErrUnsafePath`). Aksi `FileMove` memverifikasi kedua sisi; bila tujuan hilang → `failed`, bukan sukses diam. Test executor: `TestUndoWriteRestoresBytesAndMode`, `TestUndoCreateRemovesFile`, `TestUndoDeleteRecreates`, `TestUndoMove`, `TestUndoConflictAndForce`, `TestUndoIdempotent`, `TestUndoRefusesOutsideAndSymlink`, `TestUndoRefusals`, `TestUndoMissingBlob`, `TestUndoHardlink`, `TestUndoLastReverseOrder`.

**Amandemen review F2-SEC1 (JN2-1..15).** Executor kini membuka komponen akhir pada path **asli** (bukan hasil resolusi `sandbox.Check`) lewat `os.Root` direktori induk dengan `O_NOFOLLOW`, dan melakukan remove/rename/chmod dirfd-relative; symlink di dalam root dan hard link pada undo create ditolak (`ErrConflict`/`ErrUnsafePath`). Device+inode tetap dijurnal (`Snapshot.Device/Inode`) tetapi tidak ditegakkan sebagai syarat undo: codetools menulis lewat temp+rename sehingga inode after-image berbeda dari before-image pada setiap undo write yang sah, dan baris `actions` append-only tidak dapat menyimpan identitas after. Pemeriksaan yang ditegakkan adalah `O_NOFOLLOW` pada path asli + sha256 penuh before/after + verdict hard-link; ini menutup eksploit symlink JN2-1 tanpa membuat undo sah selalu `conflict`. Blob before diverifikasi sha256 dan panjang sha (JN2-3/JN2-7); mode 0000 dibedakan dengan `ModeSet` (JN2-4); chmod dilakukan setelah byte ditulis sehingga setuid/setgid bertahan (JN2-5); mode dipulihkan pada move/hard-link (JN2-13); `T_cut` menghitung byte unik lintas aksi dan aksi terbaru tidak pernah kedaluwarsa karena cap (JN2-8/JN2-9); hash isi dibaca dengan buffer 64 KiB (JN2-11). Owner/xattr/ACL/SELinux tidak dipulihkan (residu platform; mode adalah kontrak byte-identik SPEC §18.1) — didokumentasikan, bukan disembunyikan. Dampak LOC: `journal` 508 baris kode (dari 500) → baris `loc.budget` dinaikkan 500 → 520; Σ core naik 20 menjadi 35.520 ≤ 47.000.

**Amandemen PR3 (gate acceptance + JN2-14/16..20).** `TestUndoSuite` adalah gate P26: 200 skenario (5 operasi × 10 isi × 4 mode; LF/CRLF/BOM/biner/unicode/64 KiB) yang membuktikan byte dan mode kembali identik, undo kedua idempotent, dan baris `action_undos` `undone` tercatat. Refusal tests menutup crash sebelum side effect, konten parsial, konflik/`--force`, luar root, symlink escape/symlink di dalam root, kedaluwarsa retensi, turn terbuka, blob before hilang/korup, aksi irreversible, hard link (create dan write), mode 0000/setuid, tujuan move hilang; `TestUndoSuiteMultiFile` membuktikan `undo N` berjalan mundur. `UndoLast` memakai `BatchFinisher` (satu transaksi `FinishBatch`) dan `List` membaca cutoff + semua baris dalam satu snapshot; `TestUndo50Files` terukur 74–90 ms di ext4 dan 28–36 ms di tmpfs (target SPEC §18.1 ≤ 200 ms, ditegakkan `internal/testutil/timing`, bukan dilonggarkan); LOC `journal` menjadi 539 baris kode, baris `loc.budget` dinaikkan 520 → 545 (Σ core 35.545 ≤ 47.000). Symlink yang ditolak selalu `conflict` (bukan `failed`). `maxBlobSize` = 1 GiB adalah batas keras memori per before-image; blob lebih besar = `ErrBlob` (JN2-19). `ModeSet` opt-in untuk mode 0000: caller wajib mengisi `ModeSet=true`; `journalstore.Record` mengisinya otomatis bila `Mode != 0` (JN2-20). Edge `journalstore → sandbox` hanya dipakai file `_test.go` suite ini; kode produksi `journalstore` tetap hanya `ctxdb` + `journal`.

**Amandemen PR4 (budget `journal`, #143).** Baris `loc.budget` `journal` dinaikkan 545 → 560 saat merge #143 tanpa entri ADR; amandemen ini mencatatnya. Alasan: `makeReplaceable` (`fsys_unix.go`/`fsys_windows.go`; NTFS menolak rename menimpa file read-only) dan penerimaan path absolut native Windows di `absolute`. Terukur `make loc` di main 4766cb9: 557 baris kode (> 545), sehingga 560 dipertahankan (headroom 3). Σ core `loc.budget` 35.665 → 35.680 ≤ 47.000 (angka Σ 35.545 di Amandemen PR3 tidak memperhitungkan ADR-123 `budget` 400 → 520; Σ sebenarnya saat itu 35.665). Sesudah ADR-118, ADR-119, ADR-122, ADR-125, dan ADR-126, Σ core `loc.budget` = 36.770 ≤ 47.000 (sama dengan baris Total core ENGINEERING §2.4).

## ADR-118 — `ctxdb` F2 read surface: History fork-aware, Search bertipe, Fork, benchmark, dan EC-STATE

**Status:** Accepted 2026-10-21 (delegated). Mengimplementasikan SPEC §4.6, §11.6 (History/recall/visibility), §11.11.10 (fork lease), ADR-046/047/074/087/110; menaikkan budget `ctxdb` 3.100 → 3.400.

**Konteks.** `internal/ctxdb` sudah memiliki migrasi 0001–0008, lease/fencing, purge, gc, recovery, dan `Visible`, tetapi permukaan baca F2 belum ada: `History` masih berupa pemindaian JSON seluruh tabel di composition root, `Search` mengembalikan `[]string` tanpa identitas event/skor/ snippet, dan `Fork` belum diimplementasikan. ROADMAP F2 juga menuntut `BenchmarkAppend`, `BenchmarkAppendDuringGC`, `BenchmarkRecall` (termasuk CJK; harness Recall dimiliki PR #152), serta EC-STATE 001/002/003/005..011/013..016.

**Keputusan.**
1. **`History(ctx, sessionID, fromSeq, opts ...HistoryOption)`.** Tanda tangan SPEC `History(ctx, sessionID, fromSeq)` tetap kompatibel (variadic); opsi menambah batas, raw-summary, dan include-excluded. Lineage memakai recursive CTE `sessions.parent_id` dengan batas `fork_seq` per leluhur, filter `Visible`/`purging`, dan `path` sebagai penjaga siklus (EC-STATE-014). `fromSeq > 0` berarti ekor inkremental sesi target saja; prefix leluhur hanya dikirim pada `fromSeq = 0`. Event di-scan kolom-per-kolom (`LIMIT`), bukan `json_group_array`. Ringkasan terbaru per segmen lineage menggantikan event mentah yang dicakupnya dengan satu event sintetis `summary_ref` (kecuali `WithHistoryRaw`), dan `context_exclude{turn_id|from_seq/to_seq}` menyembunyikan turn terkait (kecuali `WithHistoryExcluded`). Lampiran diambil dari `event_attachments` dalam chunk ≤ 500 id. Default batas 10.000, cap 100.000; `fromSeq < 0` → `ErrInvalidSeq`.
2. **`Search(ctx, Query) ([]Hit, error)`.** `Hit{EventID, SessionID, Seq, Kind, Score, Snippet}`; `Query{Text, Limit, SessionID}` dengan k default 10 dan cap 1.000. `events_fts` dan `summaries_fts` dicari terpisah dengan `bm25` lalu digabung dan diurutkan (bm25 negatif; kecil lebih baik). Setiap kandidat difilter `NOT purging(session_id)` sampai k hasil. Query dinormalisasi `dmb_fts_norm` di Go, lalu setiap token dikutip (operator `-`, `*`, `:`, `NEAR(` menjadi data); bigram CJK yang tumpang-tindih di-OR dan token CJK satu rune menjadi prefix query. Snippet dibangun dari teks baris yang sudah di-scan (tabel FTS `contentless_delete` tidak mendukung `snippet()`), dibatasi 160 rune.
3. **`Fork(ctx, sessionID, atSeq)`.** Membuat sesi anak `parent_id/fork_seq`, tanpa menyalin event; lease anak diambil di transaksi yang sama (`CreateSession`). Rewind = `Fork` pada seq lebih awal, cabang lama tetap. `atSeq` wajib di `[0, head]` (`ErrInvalidSeq`), sumber hilang/berlabel `ErrNotFound`/`ErrSessionPurging`, dan guard P pada `parent_id` tetap berlaku. Metadata `origin/cwd/model/config_sha/context_tier` disalin; `Session.ContextTier` ditambahkan agar tier beku tidak hilang saat fork.
4. **Migrasi vs init bersamaan (EC-STATE-003).** `migrateIfNeeded` menunggu barrier yang dipegang proses lain dengan retry terbatas (`BarrierWait`, polling 50 ms), membaca ulang `user_version` setelah barrier didapat, dan keluar bila proses pemenang sudah menyelesaikan ladder. Tanpa ini, dua proses yang membuka data dir kosong bersamaan gagal `db_busy`/“table already exists”.
5. **EC-STATE yang diklaim.** 002 (TRUNCATE checkpoint hanya pemilik jangka panjang), 003 (init schema dengan busy_timeout/retry), 005 (satu writer handle per proses; Append serial dan seq kontigu), 006 (identitas file berubah → tulis ke generasi lama ditolak), 008 (`SQLITE_NOTADB/CORRUPT` bukan disk penuh; klasifikasi berdasarkan kode hasil), 010 (query per sesi memakai `UNIQUE(session_id, seq)`), 011 (timestamp STRICT INTEGER menolak 5e246), 013 (History murni baca; re-encode tidak menambah baris), 014 (lineage >100 generasi + cycle guard), 015 (tidak ada data dir produksi implisit), 016 (flock warisan child, fail-closed; POSIX saja). 001 (retry IOERR transien pembaca), 007/009 (klasifikasi/rebuild FTS) tetap pending sampai implementasinya ada; 004 milik F7 dan 012 milik F4 (ADR-080).
6. **Benchmark.** `BenchmarkAppend` (synchronous=FULL, 3 `blob_refs`/event) dan `BenchmarkAppendDuringGC` (5.000 blob yatim + GC berjalan) selalu tersedia dan melaporkan `p50/p95/p99-ms`. `BenchmarkRecall`/harness 1M **tidak diduplikasi**: PR #152 (`internal/ctxdb/recall_perf_test.go`, `ctxdb.Recall`) memilikinya; typed `Search` di sini adalah permukaan baca yang dapat memberi kandidat ke `Recall` setelah #152 mendarat. `searchExpr` sengaja dinamai berbeda dari `ftsQuery` milik #152 agar tidak bentrok saat rebase.
7. **Budget.** `loc.budget` `ctxdb` 3.100 → 3.400 (terukur 3.343; headroom 57 baris), Σ core 36.120 → 36.420 ≤ 47.000 (ADR-033); `TestBudgetTableSum` tetap hijau. Tidak ada dependency baru.

**Errata (tanpa mengubah SPEC).** SPEC §4.6 menulis `Search(ctx, Query)` dan `History(...fromSeq)`; implementasi menambah opsi variadic pada History (kompatibel) dan `Query`/`Hit` konkret. `fromSeq` pada sesi fork didefinisikan sebagai ekor sesi target (SPEC tidak merinci semantik lintas lineage). `Search` menyertakan ringkasan sebagai `Kind:"summary_ref"` dengan `EventID` = `summaries.event_id`, sesuai §11.6 yang menyebut `events_fts`/`summaries_fts`.

**Windows/macOS.** Target `Store.Append` macOS 30 ms p99 (SPEC §4.6; Linux/Windows 10 ms) — angka benchmark di PR adalah Linux. `syncDir` adalah no-op di Windows (`dirsync_windows.go`), sehingga publikasi blob mengandalkan rename + fsync file; perilaku itu tidak berubah. EC-STATE-016 memakai `ExtraFiles` (POSIX-only) dan tidak diklaim di Windows; seluruh kode baca memakai abstraksi yang sama di ketiga OS. `os.SameFile` untuk deteksi generasi file berlaku di semua OS.

**Dampak terukur (SPEC §20).** Token 0; binary/dependency 0; LOC `ctxdb` 3.343/3.400. Benchmark Linux x86-64 (Xeon Gold 6152, `GOMAXPROCS=1`, `-benchtime=50x`/`20x`): `BenchmarkAppend` p50 1,224 ms / p95 5,639 ms / p99 9,160 ms (target 10 ms p99; macOS 30 ms), `BenchmarkAppendDuringGC` p50 1,614 / p95 11,76 / p99 24,01 ms (target 210 ms). Recall ≤50 ms p95 @1M diukur oleh harness PR #152 (`TestRecallP95`/`BenchmarkRecall`), bukan PR ini; typed `Search` diuji unit (termasuk CJK) di `read_test.go`. Test: `read_test.go` (fork/rewind, summary, context_exclude, batas, typed search, CJK, metadata fork), `edgecases_test.go` + `edgecases_unix_test.go` (EC-STATE), `bench_test.go`. `go test -race ./internal/ctxdb/...` hijau.

**Amandemen 2026-10-21 (rebase #152).** PR #151 di-rebase ke main setelah `ctxdb.Recall` + harness 1M (#152) mendarat; History/Fork/Search bertipe dan Recall kini satu paket. `make loc` mengukur `ctxdb` **3.418** baris (batas 3.400) sehingga budget dinaikkan ke **3.500** (headroom 82). Σ core `loc.budget` menjadi **36.520** (dari 36.420), masih ≤ 47.000; baris `Total core` tabel dan prosa ENGINEERING §2.4 disamakan dengan angka ini (`TestBudgetTableSum`). Tidak ada perubahan non-test Go pada amandemen ini; `scripts/pr-meta.sh` tetap di bawah 600 baris.

## ADR-119 — F2 modules: kontrak host, registry statis, lifecycle lazy, katalog, dan `tool_search`/`tool_invoke`

**Status:** Accepted 2026-10-21 (keputusan didelegasikan). Menerapkan ROADMAP F2 `modules` + `tool_search`/`tool_invoke`, SPEC §3.2/§5.2/§14.1, dan menutup `TestModuleLifecycle/needs_attention` (ADR-083). Nomor 117 (journal) dan 118 (ctxdb) dipakai irisan F2 lain; 120 dipakai memory/skills.

**Konteks.** SPEC §3.2.1 mendeklarasikan `Module.Invoke(...) (tools.Result, error)` dan `Descriptor.Factory func(host Host, cfg toml.Primitive)`, tetapi E-06 hanya mengizinkan modul mengimpor `internal/modules/host`, `internal/netx`, dan `internal/schema`: `internal/tools` mengimpor `internal/modules` (arah sebaliknya = siklus), dan `internal/modules` tidak boleh mengimpor `internal/config` atau TOML. SPEC §3.2.2 juga memakai nama status modul (`stopped|starting|…`) yang berbeda dari nama state lifecycle internal. Registry harus tetap satu-satunya global yang diizinkan E-10, `Register` terjadi di `init()` tanpa efek samping (P16), dan `tool_invoke` tidak boleh melewati approval/taint/spill (SPEC §14.1, ADR-079).

**Keputusan.**
1. **Hasil tool modul memakai tipe host.** `Module.Invoke` mengembalikan `host.Result`/`host.Error` (kode = string dari himpunan tertutup SPEC §4.3). Adaptor di `internal/tools` memetakan `host.Result` → `tools.Result` dan memvalidasi `host.Error.Code` terhadap `tools.CodeSet`; kode asing menjadi `internal` (fail-closed). Ini menggantikan `tools.Result` di SPEC §3.2.1 tanpa mengubah perilaku pipeline.
2. **Config modul lewat `host.Config`.** `Descriptor.Factory` menerima `(host.Host, host.Config)` dengan `Config.Key` (default `modules.<id>`) dan `Raw` berisi section yang sudah di-encode JSON oleh composition root. Decode TOML tetap milik `internal/config` (F3); modul memiliki schema-nya sendiri.
3. **Registry statis + freeze.** `modules.Register(Descriptor)` hanya mencatat data; error validasi dikembalikan dan disimpan, `Freeze()` menutup registrasi (pendaftaran setelah freeze = `ErrFrozen`) dan menjalankan `Validate()`. `Validate()` mengembalikan `errors.Join` dan tidak pernah panic. `defaultRegistry` adalah satu-satunya global baru (allowlist E-10); seluruh dependency lain lewat `Deps` (E-11).
4. **Lifecycle lima state.** `dormant|activating|active|needs_attention|failed` dengan pemetaan ke `host.Status` (`stopped|starting|ready|needs_attention|failed`). `degraded` (circuit breaker GW-11) tidak dipakai di F2 karena breaker gateway baru ada di F4; state itu tetap ada di vocabulary `host.Status`. `Ensure` single-flight (satu Factory/Start per aktivasi, pemanggil konkuren menunggu channel), panic Factory/Start ditangkap `internal/boundary` → `failed` (P14), `Stop` dibatasi 3 s, dan epoch mencegah aktivasi basi menimpa `Stop`/`Resolve`/`Fail`.
5. **`needs_attention` sekali otomatis.** Masuk hanya dari `active`; event `module_error` dipancarkan dan instance lama dihentikan. `Ensure` menolak dengan `ErrNeedsAttention`; timer `temporary_ban` 24 jam memberi tepat satu percobaan otomatis (`retryUsed` sticky), lalu hanya `Resolve()` (tindakan owner) yang mengembalikan ke `dormant`. `failed` boleh dicoba ulang setelah 60 s. Test: `TestModuleLifecycle/needs_attention` (+ dormant/failure/idle/concurrent/stop).
6. **Idle stop eksplisit.** `Descriptor.IdleStop` + `Runtime.Sweep` menghentikan modul aktif yang menganggur; timer pemanggil dimiliki composition root, bukan package global.
7. **Katalog.** `Descriptor.Catalog` adalah satu baris ≤ 15 token dan `Descriptor.Group` adalah nama grup; katalog deferred dirender `internal/assemble.Catalog` yang sudah ada (F1). Hook budget `all-modules` (ROADMAP F2) dibangun dari `Registry.Descriptors()` oleh modul `bench`; `internal/assemble` tidak berubah sehingga gate P1 `f1-core` ≤ 4.000 token tetap utuh.
8. **`tool_search`/`tool_invoke` di `internal/tools`.** Keduanya tool eager di registry (schema konstan sepanjang sesi, ADR-012). `tool_search` mencari metadata (nama/deskripsi/tag) dengan hasil terbatas `k` 1–5 dan `query` ≤ 200 karakter; schema lengkap hanya dikembalikan untuk hasil itu. `tool_invoke` membuka envelope `{name, arguments}`, mengaktifkan modul pemilik lewat `Ensure` (modul tool start pada invoke pertama, SPEC §3.2.2), lalu menjalankan **pipeline yang sama** dengan nama/argumen dalam: schema, effect, sandbox, taint, approval, journal, redaksi, spill, dan batas inline. Karena unwrap terjadi sebelum `Prepare`, loop melakukan approval untuk tool dalam, bukan untuk `tool_invoke`; tidak ada jalur approval kedua atau bypass.
9. **Budget.** Baris `modules` = 1.000 LOC (terukur 881 termasuk `modules/host`), tidak berubah. `tool_search`/`tool_invoke` + adaptor modul menambah `internal/tools` melewati headroom 40 baris, sehingga baris `tools` naik 1.800 → 2.200 (+400) pada PR yang sama dengan kodenya; Σ core tetap ≤ 47.000 (terukur 26.408 dari 190.000 P21). Graf E-01 mendapat satu edge baru `internal/tools` → `internal/modules/host` (adaptor `host.Result` → `tools.Result`).

**Dampak terukur (SPEC §20).** Token: satu baris katalog per grup (≤ 15 tok) + 2 schema meta-tool konstan; P1 `f1-core` tidak berubah (fixture tidak memuat katalog modul). Latensi: aktivasi hanya pada invoke pertama; `Ensure` aktif = satu lock + timestamp; `tool_search` dibatasi 5 hasil dan 8 term. LOC: `modules` 881/1.000; `tools` 2.150/2.200 (terukur). Binary: tipe/interface host saja, tanpa dependency baru. Dependency langsung tetap 28.

**Konsekuensi.** (+) Modul tidak bisa menyentuh pipeline atau approval; registry deterministik dan immutable setelah freeze; `needs_attention` tidak pernah auto-retry diam-diam. (−) `Module.Invoke` mengembalikan tipe host, bukan `tools.Result` (selisih dari teks SPEC §3.2.1 yang dicatat di sini); `degraded` belum punya produsen sampai breaker F4; konversi TOML → JSON config dilakukan composition root.

## ADR-120 — `internal/memory` + `internal/skills`: karantina, retrieval, dan storage konsumen F2

**Status:** Accepted 2026-10-21 (keputusan didelegasikan; opsi paling konservatif untuk setiap detail SPEC yang ambigu: karantina/deny). Melengkapi SPEC §4.8, §6, §11.2, §11.6, §14.4, P10; ADR-024, ADR-074. Diterapkan lewat PR bertumpuk: (a) `internal/memory`, (b) `internal/ctxdb/memstore`, (c) `internal/skills`, (d) `internal/ctxdb/skillstore`, (e) `ctxdb.Recall` + harness benchmark.

**Konteks.** Migrasi `0001_core.sql` sudah memiliki `memory`, `memory_decisions`, `memory_pins`, `v_memory_status`, `memory_fts`, `skills`, `skills_fts` beserta trigger append-only dan pengecualian purge; baris `loc.budget` `memory+skills`, `ctxdb/memstore`, dan `ctxdb/skillstore` sudah ditetapkan ADR-074. F2 tinggal menulis paket konsumen + storage. SPEC tidak menetapkan beberapa detail: apakah "update/delete" boleh mengubah baris, apakah skill pending boleh masuk indeks FTS, apakah tulisan user ikut dikarantina, dan bagaimana `recall` diukur pada 1.000.000 event.

**Keputusan.**
1. **Tanpa migrasi 0009.** Tidak ada DDL baru: seluruh tabel/indeks/FTS/trigger yang dibutuhkan sudah ada di `0001`–`0008`. `memstore` dan `skillstore` hanya kode Go di atas `ctxdb` (ADR-074); `sqlite` tetap hanya diimpor `internal/ctxdb/...` (E-04).
2. **Interface konsumen ≤ 5 method.** `memory.Store` = `Insert`, `Decide`, `Pin`, `Search`, `Get`; `skills.Store` = `Index`, `Search`, `Quarantine`, `Approve`, `Status`. Storage mendeklarasikan tipe `Row`/`Query`/`Decision` dengan field identik (pola `ledgerstore`), sehingga `memstore`/`skillstore` tidak mengimpor paket konsumen dan matriks impor E-01 tidak berubah; `cmd/damnbot` yang mengonversi.
3. **Update/delete = baris keputusan baru.** `memory`, `memory_decisions`, dan `memory_pins` append-only (P9). "Delete" di API = `Retire` (approved → retired); penghapusan fisik tetap hanya lewat `sessions purge` (pengecualian `dmb_maint() = 'purge'`).
4. **Kebijakan tulis.** Hanya origin `user` yang langsung `approved` setelah redaksi (tulisan manual user, SPEC §4.8). `agent`, `auto`, `import`, dan `hub` selalu diskrining §14.4 lebih dulu: severity `high` → `rejected` seketika, selain itu `scanned` (menunggu review user). `agent`/`auto` wajib menyertakan `evidence_event_id` (CHECK skema dicerminkan di Go dengan `ErrInvalid`). Approval selalu `actor = 'user'`.
5. **Redaksi sebelum simpan.** `memory.Service` dan `memstore.Insert` sama-sama menerapkan `redact.Redactor` (defense in depth); service gagal tutup bila redactor nil. Scan memakai redactor yang sama: teks yang berubah berarti secret → `high`. Temuan `scan_result` tidak pernah memuat nilai secret. Pola §14.4 lain: prompt-injection, karakter tak terlihat (high); perintah destruktif dan URL (medium, advisory, tidak pernah auto-reject).
6. **Cap P10 ditegakkan transaksional di storage.** 500 karakter/entri dan 4.000 karakter entri `approved` per scope. `memstore` mengembalikan error yang mengimplementasikan `memory.LimitError` (`TooLarge() bool`), sehingga konsumen memetakannya ke `memory.ErrTooLarge` tanpa impor silang.
7. **Retrieval.** Hanya keputusan terakhir `approved`, tidak kedaluwarsa (`expires_at > now`), dan bukan provenance sesi yang sedang dipurge (`source_session` maupun sesi `evidence_event_id`, SPEC §11.6). Urutan: `pinned` dulu (≤ 150 tok di assembler), lalu `bm25(memory_fts)`; query kosong = pinned + terbaru. Scope filter diteruskan pemanggil (`user` + `project:` + `gateway:*` yang relevan).
8. **Skill pending tetap tidak terlihat.** `skills` row (termasuk body di `skills_fts`) dibuat saat instal agar `path`/`description`/provenance tidak hilang, tetapi setiap jalur retrieval menggabungkan `v_memory_status` lewat `skills.memory_id`; `(memory_id IS NULL OR status = 'approved')` adalah satu-satunya bentuk baca. `user`/`builtin` langsung `approved`; `agent`/`hub`/`import` menjadi baris `memory(kind='skill')` + `pending`/`scanned`, lalu di-approve user. Instal idempoten lewat `UNIQUE(name, sha256)` + upsert. Scanner diinjeksi (`skills.Scanner`) agar `internal/skills` tidak mengimpor `internal/memory` (E-01); scanner nil = gagal tutup untuk instal tidak tepercaya.
9. **`recall` diukur apa adanya.** `ctxdb.Recall(ctx, text, k)` menambahkan `LIMIT k` pada query §11.6 yang sudah memfilter `purging`; `BenchmarkRecall` memakai korpus `DAMNBOT_RECALL_EVENTS` (default 200.000; angka acceptance 1.000.000) dan test `DAMNBOT_RECALL_BENCH=1` mengukur p95 sampel query. Target tetap ≤ 50 ms p95 @ 1.000.000 event di Q14; harness bukan gate CI per-PR.
10. **Tanpa state global.** Clock dan redactor disuntik per `Service`; satu-satunya variabel paket adalah error sentinel.

**Dampak terukur (SPEC §20).** Token 0 (tidak ada teks prompt/schema baru; retrieval ke prompt tetap sub-cap §6). DDL 0. Dependency 0 baru. LOC: `memory+skills` 1.200, `ctxdb/memstore` 600, `ctxdb/skillstore` 400 (ADR-074, tanpa perubahan `loc.budget`).

**Konsekuensi.** (+) Satu jalur kebijakan untuk memori dan skill, karantina fail-closed, dan storage tetap satu pemilik SQLite. (−) Approval skill harus mempertahankan `skills` row pra-approve; membuangnya berarti kehilangan `path`/`description`. (−) `memstore`/`skillstore` mengakses primitif `ctxdb` lewat transaksi tulis (belum ada read-tx publik), sehingga retrieval memori berbagi writer dengan append; target ≤ 5 ms @ 10.000 entri masih terpenuhi dan read-tx publik dapat ditambahkan F2 lanjutan tanpa mengubah API konsumen.

## ADR-121 — `internal/assemble` F2: katalog dari registry modul, context tier ber-retrieval, summary/retrieval, dan fixture P1 `all-modules`

**Status:** Accepted 2026-10-21 (keputusan didelegasikan; opsi paling konservatif untuk setiap detail SPEC yang ambigu). Menerapkan ROADMAP F2 (P1 penuh `all-modules`, P15), SPEC §4.5, §5.2, §6; melanjutkan ADR-066, ADR-067, ADR-082, ADR-114; konsumen ADR-119 (modules), ADR-120 (memory/skills), dan ADR-122 (compaction). Nomor 121 dicadangkan untuk irisan ini. PR bertumpuk (E-84): PR1 katalog (butir 1–4), lalu amandemen per PR berikutnya.

**Konteks.** Graf impor (`internal/archtest/allowed.go`) hanya mengizinkan `assemble → loop/event`, sehingga registry modul, memori, skill, dan compaction tidak boleh diimpor (E-01); semuanya masuk lewat data pemanggil atau interface konsumen ≤ 5 method (E-09). ADR-119 butir 7 menetapkan `Descriptor.Catalog` sebagai satu baris ≤ 15 token, tetapi `assemble.Catalog` F1 merender `- <name>: <tool, tool>` dari nama tool; nama tool modul (`code_definition`, `browser_screenshot`, …) membuat baris melewati 15 token. Baris `loc.budget` `assemble` 1.000 sudah terpakai 995 di F1.

**Keputusan (PR1, katalog).**
1. **Katalog dari registry modul.** `CatalogGroup{Name, Summary}` = `modules.Descriptor{Group, Catalog}`, satu per modul enabled, dipetakan composition root. `Assembler.Catalog` merender header tetap `Deferred tool groups (tool_search, then tool_invoke):` lalu `- <group>: <summary>`; prefix `- <group>:` yang sudah ada di `Descriptor.Catalog` diterima apa adanya. Nama dan summary diredaksi `Deps.Scrub` lalu dirapatkan ke satu baris: whitespace (termasuk newline/U+2028) menjadi satu spasi dan rune kontrol/format (ESC, bidi U+202E/U+2066–2069, zero-width) dibuang, sehingga summary tidak bisa menambah struktur prompt atau menyamarkan teks (REVIEW-F2-SEC4 ASM4-5; path file konteks diperlakukan sama, tanpa merapatkan spasi). Grup bernama sama digabung menjadi satu baris (summary diurutkan, unik, dipisah `; `), urutan byte-wise menurut nama grup, tanpa statistik pemakaian (P15, ADR-056). Batas: ≤ 24 grup dan ≤ 600 tok termasuk header; ekor diganti satu baris `- +<K> more groups: tool_search`. Batas ≤ 15 tok per baris (`CatalogLineMax`) ditegakkan saat registrasi (ADR-119), diukur o200k di modul `bench`, dan juga di `Catalog` dengan `Deps.Tokens` (estimator COST-09, ≥ o200k untuk ASCII; REVIEW-F2-SEC4 ASM4-4): baris grup (termasuk baris gabungan) > 15 tok tidak dipotong — potongan di tengah kata dapat menghasilkan nama tool palsu — melainkan dikeluarkan dan dihitung di baris overflow `- +<K> more groups: tool_search`, sehingga grup tetap dapat ditemukan lewat `tool_search` dan slotnya dipakai grup berikutnya (`TestCatalogLineLimit`, `TestCatalogSanitized`, `TestContextPathSanitized`).
2. **Aturan 3 mengikuti katalog.** `Build` menimpa `StableOptions.Deferred` dengan `catalog != ""`: aturan 3 (menyebut `tool_search`/`tool_invoke` dan "deferred catalog below") dirender tepat saat katalog dirender (`TestBuildRule3FollowsCatalog`). Pemanggil tetap menaruh `tool_search`/`tool_invoke` di array tools bila katalog tidak kosong (ADR-119 butir 8).
3. **MCP F5.** Baris gabungan MCP (`- mcp: <N> servers …`, ≤ 60 tok) belum dirender karena client MCP adalah F5; headroom katalog tetap dicadangkan untuknya.
4. **Budget.** `loc.budget` `assemble` 1.000 → 1.200 (scc Code: 995 di main, 1.034 sesudah PR1); Σ core dihitung ulang dari jumlah baris core `loc.budget` (sudah memuat `tools` 2.200 ADR-119, `journal` 560 ADR-117, dan `budget` 560 ADR-125, serta `ctxdb` 3.500 ADR-118, `provider/registry` 300 + `ctxdb/limitstore` 100 ADR-126, dan `ctxdb/compactstore` 250 ADR-122): 36.770 → 36.970 ≤ 47.000. Tabel ENGINEERING §2.4 diselaraskan dengan `loc.budget` (`tools` 2.200, `journal` 560, `budget` 560, `assemble` 1.200; Total core 36.970 = jumlah tabel).

**Dampak terukur (SPEC §20).** Token: `f1-core` tidak berubah (katalog kosong, golden byte-identik); header katalog ≈ 13 tok hanya bila ada grup deferred. LOC +39 (PR1). Dependency 0. Latensi: satu map + sort + satu hitung token per baris grup per `Build`.

**Konsekuensi.** (+) Katalog byte-identik per himpunan modul enabled, berapa pun urutan registrasi; aturan 3 tidak pernah menyebut tool yang tidak ada. (−) Composition root belum memetakan `modules.Default().Descriptors()` ke `SessionView.Catalog`: runner headless belum memasang `tool_search`/`tool_invoke`, jadi pemetaan itu ikut irisan integrasi yang menyambungkannya.

**Amandemen PR2 (context tier, summary, retrieval).**
5. **Retriever konsumen (E-09).** `assemble.Retriever` (2 method: `Memories(ctx, query) ([]Memory, error)`, `Skills(ctx, query) ([]string, error)`) menggantikan `ContextInput.Memory/Skills`. Composition root mengadaptasi `memory.Service.Recall` (approved, belum kedaluwarsa, scope sesi, pinned dulu lalu BM25; ADR-120 butir 7) dan `skills.Service.Offer` (≤ 3 baris `PromptLine`); `ContextInput.Query` = pesan user pertama sesi (SPEC §4.8). Urutan hasil adalah kontrak: assemble tidak me-rank ulang. Error retrieval dikembalikan, tidak pernah di-skip diam-diam.
6. **Pinned → ranked, potong di batas entri.** Memori pinned (urut rank) dirender ≤ 150 tok, lalu ranked sampai slot ≤ 300 tok; entri yang tidak muat menghentikan larinya (tanpa loncat ke entri lebih kecil berperingkat lebih rendah) dan dihitung di penutup `[+N memories: recall]`, yang kini hanya muncul bila N > 0. Entri pinned tidak lagi dipotong di tengah. Setiap entri memori dan skill dirapatkan ke satu baris, dan awalan `- ` dari `PromptLine` tidak digandakan. Sub-cap dan urutan potong ADR-114 butir 3 tetap.
7. **Context beku per sesi (P15).** `SessionView.Context` kini teks `ContextResult.Text` yang dirender sekali oleh `Assembler.Context`; `Build` tidak melakukan I/O dan tidak memanggil retriever (`TestContextTierFrozen`). `Build` memvalidasi wrapper `<untrusted>` teks itu dengan parser yang sama dengan summary (butir 9) dan menolak teks asing yang tidak valid (`ErrUntrusted`); tier > 1.100 tok dipotong sadar-wrapper — potongan mundur sebelum tag yang akan terbelah dan wrapper yang tertinggal terbuka ditutup sesudah penanda — sehingga hasilnya tetap valid (no-op untuk tier yang dirender `Context`; hard clip akhir `Context` memakai fungsi yang sama) (REVIEW-F2-SEC4 ASM4-3, `TestBuildContextWrapper`). Runner headless merender context sekali per run.
8. **File konteks untrusted.** `ContextInput.UntrustedFiles` (workspace untrusted, SPEC §14, ADR-069) membungkus file konteks dalam `<untrusted source="workspace">…</untrusted>`, dan pemotongan hanya terjadi di dalam wrapper sehingga penutup selalu ada (`TestContextUntrustedFiles`). Setiap teks caller di context tier (fakta, workspace, isi dan path file, memori, skill; trusted maupun untrusted) diredaksi lalu setiap batas `<untrusted`/`</untrusted` — segala kapitalisasi ASCII, apa pun sesudahnya (atribut, whitespace, `/>`, tag parsial), pencocokan pada offset byte asli — dinetralkan dengan menyisipkan `\\` sesudah `<` (bentuk penutup sama dengan `internal/tools`); tag pembuka ikut dinetralkan sehingga data tidak bisa membuka wrapper bersarang, dan satu-satunya tag hidup di tier adalah wrapper milik assemble (REVIEW-F2-SEC4 ASM4-2, `TestEscapeUntrusted`, `TestNestedOpenEscaped`).
9. **Summary aktif (tier 5) lewat data.** `internal/compaction` (ADR-122) belum di main, jadi hanya seam konsumen yang ditambahkan: `SessionView.Summaries []string` = blok yang sudah dirender compaction (butir 12 ADR-122; urut lama → baru; blok tainted sudah dibungkus dan isinya di-escape produsen). `Build` meredaksi setiap blok, lalu mem-parse setiap batas `<untrusted` secara exact (fail-closed, `ErrUntrusted`): pembuka hanya `<untrusted>` atau `<untrusted` + whitespace + atribut + `>` (bukan self-closing), penutup hanya `</untrusted` + whitespace opsional + `>`, huruf kecil, tidak bersarang, tidak ada penutup tanpa pembuka, dan wrapper tidak tertinggal terbuka; batas lain (`<untrusted` parsial, `<untrusted/>`, `<untrustedx>`, kapitalisasi lain, penutup beratribut) ditolak, bukan ditebak, karena model tetap bisa membacanya sebagai batas. Ini menggantikan penghitungan substring yang dapat diakali dengan pembuka parsial + satu penutup nyata (REVIEW-F2-SEC4 ASM4-1, `TestSummaryWrapperBypass`). Blok terbaru yang totalnya ≤ 3.000 tok disimpan sebagai satu pesan `user` sesudah `system`; blok lebih tua keluar utuh, tidak pernah dipotong. `Estimate.Summaries` terisi.
10. **Retrieval per turn (tier 6).** `SessionView.Retrieval []Snippet{Source, Text}` (recall FTS5 atau memory provider eksternal, terbaik dulu) dirender sebagai satu pesan `user` sesudah summary: header data tetap, setiap hit diredaksi, dinetralkan seperti butir 8 (pembuka dan penutup), dan dibungkus `<untrusted source="<label>">` (label hanya `[a-z0-9:_-]`); hit pertama yang tidak muat dipotong di dalam wrapper lalu mengakhiri tier; total ≤ 800 tok (`TestRetrievalTier`, `TestNestedOpenEscaped`). `Estimate.Retrieval` terisi.
11. **Schema pinned.** `Estimate.Pinned` tetap 0: schema hasil `tool_search` yang di-pin adalah `tool_result` di history (dikecualikan dari prune, ADR-122 butir 3), dan LRU ≤ 3.000 tok / `not_loaded` milik `internal/tools` (SPEC §5.2); `Message{Role, Content}` tidak membawa nama tool sehingga assemble tidak dapat menghitungnya tanpa menebak.
12. **LOC/test.** `assemble` terukur 1.236 (scc Code) sesudah PR2: 1.195 untuk tier F2 dan +41 untuk perbaikan REVIEW-F2-SEC4 ASM4-1..3 (parser wrapper, netralisasi pembuka, clip sadar-wrapper). `loc.budget` `assemble` 1.200 → 1.300; Σ core dihitung ulang dari jumlah baris core `loc.budget`: 36.970 → 37.070 ≤ 47.000, tabel ENGINEERING §2.4 diselaraskan. Golden `f1-core` byte-identik. Test baru: `TestContextRetrieval`, `TestContextMemoryCloser`, `TestContextUntrustedFiles`, `TestContextTierFrozen`, `TestSummaryTier`, `TestRetrievalTier`, `TestPrefixStableAllTiers`, `TestEscapeUntrusted`, `TestSummaryWrapperBypass`, `TestNestedOpenEscaped`, `TestBuildContextWrapper`, `TestImportsAllowed` (impor paket, termasuk test, hanya stdlib + `internal/loop/event`).

## ADR-122 — `internal/compaction` F2: prune deterministik tahap 1, summary tunggal tahap 2, dan storage summary

**Status:** Accepted 2026-10-21 (F2; keputusan didelegasikan, opsi konservatif). Menerapkan SPEC §4.7/P8, tier 5/7 §4.5, dan kategori EC-CTX (ROADMAP F2); melengkapi ADR-123 (pemicu 75/90), ADR-074 (storage konsumen), dan ADR-117 (store mengimpor paket konsumen). Nomor 122 dicadangkan untuk compaction; 118/119/120/123 dipakai irisan F2 lain. Diterapkan dalam PR bertumpuk: PR1 tahap 1 (butir 1–4), amandemen per PR berikutnya.

**Konteks.** SPEC §4.7 menetapkan lima aturan prune tahap 1 "hanya view request; DB tidak berubah", tetapi tidak menetapkan: (a) arti "lebih tua dari N" (N terakhir dipertahankan atau N+1); (b) bentuk stub bila hasil tool tidak punya blob atau path; (c) teks assistant mana yang "antar-tool"; (d) cara menjamin pasangan `tool_calls`/`tool_result` dan pembungkus `<untrusted>` tetap utuh; (e) baris `tool_result` ganda (journal dispatcher + loop mencatat satu baris per call, F1).

**Keputusan (PR1, tahap 1).**
1. **Fungsi murni.** `compaction.Prune(events []Event) ([]Message, PruneStats)` membangun view dari event `user_message`/`assistant_message`/`tool_result` sesudah batas summary dan menerapkan aturan 1–5 berurutan; tidak ada I/O, state global, atau penulisan DB. Baris `tool_result` kedua untuk call yang sama digabung ke yang pertama (isi pertama yang tidak kosong, blob/failed/tainted di-OR); id call yang dipakai ulang oleh assistant berikutnya memulai pasangan baru. Hasil tanpa call di rentang event (yatim) dibuang dan dihitung `PruneStats.Orphans`, karena mengirimnya sendirian membuat request 400 (EC-CTX-011).
2. **Jendela "lebih tua dari N" = N terbaru dipertahankan.** Aturan 3: 6 `tool_result` terbaru utuh; aturan 4: 10 pesan assistant terbaru utuh dan hanya pesan assistant yang membawa `tool_calls` yang dipotong (jawaban final tidak pernah), menjadi 199 rune + `…` (= 200 char); aturan 5: gambar dua turn user terbaru dikirim, sisanya `[image <sha12> omitted]` ditambahkan ke teks pesan user. Aturan 2: mutasi = `write_file`/`edit_file` sukses (payload tanpa `error` dan journal tidak `ok:false`) atas path yang sama persis (byte, tanpa normalisasi: dua ejaan path tidak pernah digabung, sehingga hanya menyisakan lebih banyak isi); stub menyebut seq hasil mutasi terdekat sesudah read.
3. **Format stub.** Aturan 3 = `[pruned: <n> chars, blob <sha12>; read_file <path> or recall]` dengan `n` = jumlah rune; bagian `, blob …` hanya bila hasil punya blob spill, bagian `read_file <path> or ` hanya untuk `read_file` dengan argumen `path`. Hasil `tool_search` (schema yang di-pin) dikecualikan dari aturan 3. Stub hanya dipasang bila lebih pendek dari isi aslinya (prune tidak pernah memperbesar request), dan satu pesan hanya diganti satu aturan.
4. **Invarian.** Prune hanya mengganti isi: pesan tidak pernah ditambah, dihapus (selain hasil yatim), atau diurutkan ulang, sehingga setiap `tool_calls` tetap berpasangan dengan hasilnya. Stub mengganti isi **utuh**, jadi pembungkus `<untrusted>` tidak pernah terpotong terbuka; aturan 4 hanya menyentuh teks assistant. Ronde tool terakhir yang belum dibaca model (hasil sesudah `tool_calls` assistant terakhir di ujung history) tidak disentuh aturan 1–3 (EC-CTX-019), termasuk read dan write paralel atas path yang sama.

**Dampak terukur (SPEC §20).** Token: tahap 1 hanya mengurangi isi request (stub dipasang hanya bila lebih pendek). Dependency baru 0; LOC `compaction` tetap dalam baris 900. Test: `TestPruneDedupe`, `TestPruneStaleRead`, `TestPruneOldResults`, `TestPruneTrimAndImages`, `TestPrunePairing` (pasangan, urutan, `<untrusted>` seimbang, determinisme), `TestPrunePendingRoundUntouched`, `TestPruneHistoryNormalisation`, `TestResultFlags`.

**Konsekuensi.** (+) Tahap 1 deterministik, tanpa LLM, dan dapat dijalankan ulang di setiap proses dengan hasil byte-identik. (−) Path yang dieja berbeda tidak dianggap file sama (read basi tetap utuh); mutasi lewat `shell` tidak membuat read basi (tidak dapat dideteksi tanpa menebak).

**Amandemen PR2 (storage summary).**
5. **Seam.** `compaction.Store` (2 method: `Load`, `Commit`; E-09) dan tipe `Summary`/`Snapshot`/`Call`/`Attempt`/`Outcome`/`Commit` (plus `Reason` di `trigger.go`) dideklarasikan di `internal/compaction`. `internal/ctxdb/compactstore` mengimplementasikannya dan mengimpor `internal/compaction` (satu definisi tipe, pola ADR-117) serta `ctxdb/ledgerstore` (satu pemilik bentuk baris `usage_ledger`); edge baru di matriks E-01 dan ENGINEERING §2.2.1.
6. **Load.** Satu transaksi: sesi hilang atau berlabel `purge_state` → `ErrNotFound` (visibilitas §11.6); dari event `compaction` sesi: `Compacted` (ada satu pun) dan `LastCall` (yang terbaru dengan `called`, lewat `idx_events_kind`); semua summary urut `to_seq`, lalu event `user_message`/`assistant_message`/`tool_result` dengan `seq` > `to_seq` summary terakhir beserta lampiran ber-mime `image/*` (urut `ord`). History fork-aware (PR ctxdb F2 yang belum di main) belum dipakai: compaction membaca event sesi itu sendiri, sama dengan history composition root F1.
7. **Commit.** Satu transaksi per `Run` compaction, semua event ber-`Commit.TurnID`: bila ada balasan (`Call` ≠ nil), event `llm_call` (payload `purpose:"compaction"`, model, token, biaya) + baris `usage_ledger` `purpose='compaction'` ber-`event_id` llm_call itu — setiap call summary yang membalas meninggalkan jejak (P11), juga bila balasannya ditolak; call gagal tanpa balasan tidak membuat baris ledger tanpa biaya; bila summary diterima, event `summary_ref` (payload `from_seq`/`to_seq`/`tainted`) lalu baris `summaries` dengan `event_id` = event `summary_ref`; **selalu** event `compaction` (kind sudah ada di `events.kind`) berpayload `{outcome, reason, at_ms, called}` (`Attempt`, review F2-SEC3 JN4-2) — satu-satunya state compaction, sehingga prune dan cooldown berlaku sama di setiap instance/proses dan ikut terhapus saat purge. Kegagalan di langkah mana pun membatalkan semuanya (EC-CTX-022). Biaya provider di luar [0, 10⁶] USD atau NaN disimpan `cost_usd_micros` NULL/`unknown` (bukan integer terbungkus); `pricing_version` `none` seperti ledger turn F1. Taint disimpan di payload `summary_ref` karena `summaries.content` adalah JSON summary tervalidasi dengan skema tetap.
8. **LOC.** Baris baru `ctxdb/compactstore` 250 (terukur `make loc` 248; `compaction` tetap 900, terukur 437 pada PR2 dan 788 sesudah PR3, butir 21); Σ core `loc.budget` 36.520 (sesudah `journal` 560, ADR-117, `tools` 2.200, ADR-119, `budget` 560, ADR-125, `ctxdb` 3.500, ADR-118, dan `provider/registry` 300 + `ctxdb/limitstore` 100, ADR-126) → 36.770 ≤ 47.000, sama dengan baris Total core ENGINEERING §2.4. Test: `TestLoadCommitRoundTrip` (termasuk `Compacted`/`LastCall`), `TestCommitRejectedSummary`, `TestCommitAtomic`, `TestInvisibleSession`, `TestCost`.

**Amandemen PR3 (tahap 2 dan `Compactor`).**
9. **Seam loop/assembler.** `compaction.Compactor` (`NewCompactor(Deps)`, E-11; nama `New` sudah dipakai pemicu ADR-123) memenuhi `loop.Compactor` secara struktural (`Compact(ctx, sessionID, contextLength bool) error`, butir 19) tanpa edge import baru (E-01/E-02). Interface konsumen (E-09): `Store` (PR2), `Summarizer` (1 method: satu call `purpose=compaction`, diimplementasikan composition root di atas provider) dan `Meter` (1 method: estimasi COST-09 request untuk sebuah `View` + window, diimplementasikan composition root dengan assembler yang sama dengan loop — satu rumus, ADR-058). `View(ctx, sid)` adalah history yang dikirim assembler sesudah tier system: summary aktif sebagai satu pesan role `user`, request yang ter-summary di-restate, lalu pesan sesudah batas. `Compactor` tidak menyimpan state per sesi: prune aktif dan cooldown tahap 2 diturunkan dari event `compaction` sesi itu (butir 17), sehingga tidak dibagi antar-sesi (EC-CTX-014) dan berlaku sama di setiap instance/proses.
10. **Tahap 2.** Hanya bila view tahap 1 ≥ 60% window (persen integer seperti pemicu; window tidak diketahui → tidak ada call berbayar). "8 event terakhir" ditafsirkan sebagai 8 **pesan model-visible** terakhir (bukan event pembukuan `llm_call`/`turn_start`), opsi yang menyisakan lebih banyak konteks mentah; batas digeser mundur sampai ekor tidak diawali `tool_result` (EC-CTX-011/012). Input = isi mentah (tanpa stub) dari batas summary sampai sebelum ekor; `from_seq` = seq pesan pertama, `to_seq` = seq sebelum pesan pertama ekor. Prompt sistem konstan menyatakan isi `<untrusted>` sebagai data.
11. **Validasi.** Balasan harus satu objek JSON dengan tepat lima kunci (`goal` string; `decisions`/`files`/`open_items`/`facts` array string); satu code fence pembungkus ditoleransi; data sisa, kunci asing, tipe salah, atau `null` = invalid. Disimpan dalam bentuk kanonik `encoding/json` (urutan kunci tetap; `<`/`>` di-escape sehingga teks tersimpan tidak bisa menutup `<untrusted>`). Ditolak bila > 600 tok (COST-09) atau tidak lebih kecil dari inputnya (EC-CTX-015); call tetap dicatat. Token input/output summary memakai usage provider, atau estimasi lokal bila provider tidak mengirim usage (EC-CTX-018).
12. **Render.** Summary aktif = summary terbaru yang totalnya ≤ 3.000 tok; yang lebih tua keluar dari request (tetap di DB/FTS) dan tidak pernah digabung/diringkas ulang. Setiap blok `[summary of events A-B: earlier context, not a new request]`, JSON, `[end of summary]` (EC-CTX-010/013); summary dari input tainted dibungkus `<untrusted source="summary">`. Bila tidak ada `user_message` sesudah batas, `user_message` terakhir sebelum batas di-restate verbatim (EC-CTX-009).
13. **Anti-thrash.** Kegagalan call, balasan ditolak, atau summary yang masih menyisakan request ≥ `compact_at` (75%, outcome `ineffective`) menutup tahap 2 selama `Cooldown` (default 10 menit, jam disuntik; persisten, butir 17); `Run` pertama sesudahnya adalah satu probe dan kegagalannya menutup lagi (EC-CTX-001/002/003/005). Tahap 1 tetap berjalan. Kegagalan summary bukan error turn: `Compact` mengembalikan nil dan guard window loop yang memutuskan `context_overflow`; error hanya untuk storage/meter/pembatalan, dan pembatalan tidak menyimpan apa pun (EC-CTX-016) — call yang dibatalkan sesudah sukses tidak dicatat, sama seperti `llm_call` turn yang dibatalkan. Mitigasi EC-CTX-005 berbeda dari hermes (tanpa summary deterministik): transcript append-only dan tahap 1 hanya view, sehingga overload berulang tidak pernah menghapus sesi.
14. **EC-CTX.** Covered (`TestEdgeCases`, fase F2): 001–005, 008–016, 018–022 (19 dari 22). 006 kini covered: hasil `skill_view` yang dipangkas compaction menjadi stub `[SKILL_PRUNED]` (`internal/compaction`, ADR-173). 007 kini covered: direktif delivery `MEDIA:` dibuang dari input summarizer (`stripDeliveryDirectives`, PR #249). 017 kini covered: guard window mengurangi `max_tokens` dari window (`windowRoom`, PR #245) dan adapter assembler mengisi `WindowTokens`, sehingga pemicu aktif di runner headless.
15. **Belum disambungkan.** `cmd/damnbot` belum mengisi `loop.Deps.Compactor`, belum memakai `Compactor.View` di adapter assembler, dan belum menyediakan `Summarizer`/`Meter`; pemicu kompaksi otomatis karena itu belum aktif di runner headless (guard window dan stop overflow sudah aktif lewat `WindowTokens`). Itu irisan integrasi terpisah.
16. **LOC/test.** `compaction` terukur 788/900 (butir 21). Test: `TestCompaction` (golden `testdata/TestCompaction/{prune,summary,overflow}.json`, fixture ≥ 150K tok: tahap 1 saja; tahap 2 sesudah prune tidak cukup; > 90% sesudah keduanya → `Decide` = overflow → `context_overflow`), `TestEdgeCases`, `TestValidateSummary`, `TestViewBeforeAndAfterRun`, `TestNewValidatesDeps`, `TestAtLeast`, `TestCut`, `TestCompactorOverCtxdb` (store nyata). `archtest/globals.allow` + 1 baris (flag `-update` golden, test-only).

**Amandemen PR #245 (EC-PROV-008/EC-CTX-017).** EC-PROV-008 dibaca sebagai "output `finish_reason=length` ditandai partial dan tidak dipersist sebagai pesan assistant final": event assistant tetap disimpan dengan `payload.partial=true` (transkrip pengguna utuh), tetapi tidak direplay oleh `ctxStore.History` dan tidak masuk snapshot kompaksi (`compactstore` menyaring `partial`); event partial yang membawa tool call tetap direplay agar pasangan `tool_result`-nya tidak yatim. EC-CTX-017 memakai `windowRoom = window - max_tokens`; guard memakai room itu untuk pemicu kompaksi dan stop overflow. Kompaksi otomatis di runner headless tetap menunggu `Deps.Compactor` (butir 15).

**Amandemen review F2-SEC3 (PR #162, JN4-1..4).**
17. **Cooldown dan prune persisten (JN4-2).** Setiap `Run` yang lolos pengukuran berakhir dengan tepat satu `Store.Commit` (butir 7) yang selalu menulis event `compaction`; `at_ms` = jam `Deps.Now`. Efektivitas summary diukur **sebelum** commit atas view dalam memori (summary baru + event sesudah `to_seq` + request), sehingga outcome `ineffective` tersimpan atomik bersama summary; meter yang gagal di titik itu = `ineffective` (konservatif, jejak call tetap tersimpan). View di-prune bila `Snapshot.Compacted` (juga di proses baru). Tahap 2 tertutup selama `at < LastCall.at_ms + Cooldown` kecuali `LastCall` = `summarized`. Map in-memory dan mutex dihapus: memori tidak tumbuh dengan jumlah sesi. Turn id event = turn event model-visible terakhir (`Commit.TurnID`; `Call.TurnID` dihapus).
18. **Redaksi summary (JN4-3).** `Deps.Redactor` (interface konsumen 1 method `String(string) string`, dipenuhi `*redact.Redactor` tanpa adapter; wajib, E-11) diterapkan pada setiap string yang sudah di-decode (`goal` dan semua elemen array) sebelum kanonikalisasi, batas token, dan `Commit` — secret yang di-escape JSON (`ghp\u005f…`) ikut tertangkap. `Result.Cause` (teks error provider/meter) juga diredaksi. Input summary tidak diredaksi ulang: isinya event yang sudah diredaksi saat persist (loop).
19. **Alasan pemicu (JN4-4).** `compaction.Reason` (`estimate`, `context_length`) di `trigger.go`. Seam loop berubah menjadi `Compact(ctx, sessionID string, contextLength bool) error`: parameter bertipe bawaan agar `Compactor` tetap memenuhinya secara struktural tanpa `internal/compaction` mengimpor `internal/loop` (dan sebaliknya); guard 75% memanggil dengan `false`, `retryContextLength` dengan `true`. `ReasonContextLength` memaksa tahap 2 apa pun estimasinya (termasuk window tidak diketahui), tetapi tidak melewati cooldown (P11: kegagalan yang baru terjadi tidak dibayar ulang; loop lalu memutuskan `context_overflow`). Reason disimpan di payload event `compaction`. EC-CTX-017 tetap `pending:F2`: dampaknya (400 sebelum compaction) kini ditangani tahap 2 paksa, tetapi mitigasi katalog — ambang yang memperhitungkan `max_tokens` — milik guard window `internal/loop` dan belum diubah.
20. **Rebase (JN4-1).** Tumpukan di-rebase ke `origin/main` sesudah #140/#146/#155/#156; ADR-120 (memory) mendahului ADR-122 tanpa perubahan isi.
21. **LOC/test.** Terukur `make loc` sesudah PR3: `compaction` 788/900, `ctxdb/compactstore` 248/250; baris budget tidak berubah dan Σ core `loc.budget` = 36.770 (butir 8, ENGINEERING §2.4). Test baru: `TestContextLengthForcesSummary`, `TestCooldownAcrossInstances`, `TestFailureCauseRedacted`, kasus redaksi `TestValidateSummary`; `ctxdb/compactstore`: `TestCooldownAcrossProcesses` (dua `Compactor` yang hanya berbagi DB: 0 call di dalam cooldown, tepat 1 probe sesudahnya, 1 baris ledger) dan `TestCompactorOverCtxdb` (summary tersimpan teredaksi dengan `redact.Redactor` nyata, view di-prune di instance baru); loop: `TestContextGuard`/`TestContextLengthReCall` memeriksa argumen `contextLength`.

**Amandemen PR3 (fixture P1 `all-modules`).**
13. **Fixture.** Belum ada modul fitur yang mendaftar di main (modul F5–F8), dan `internal/assemble` maupun modul `bench` tidak boleh/tidak bisa mengimpor `internal/modules`; fixture `all-modules` (test-only, `fixture_allmodules_test.go`) karena itu memodelkan `all-modules.toml` sebagai 24 baris registry realistis (bentuk `Descriptor.Catalog`, satu per grup model-facing dari SPEC §3.2.4/§5.2: kasus terburuk 24 grup), stable varian deferred + codeintel, facts/workspace diisi sampai cap, AGENTS.md 20 KiB dari baris pedoman nyata (dipotong ≤ 400), memori 2 scope × 4.000 char tersimpan (entri ≤ 500, 6 pinned) yang dilewatkan `Retriever` (pinned dulu), 211 skill terinstal dengan top-3 menurut overlap term terhadap pesan user pertama dalam bentuk `skills.PromptLine`, volatile maksimum, dan 10 schema eager (8 inti + `tool_search`/`tool_invoke` persis seperti `internal/tools`; varian shell F1). `TestAllModulesFixtureShape` mengunci komposisi itu.
14. **Hook.** `TestFixedOverheadGolden` kini punya subtest `f1-core` (byte-identik, alokasi NEW-41) dan `all-modules` (alokasi P1: stable ≤ 1.200, katalog ≤ 600, context ≤ 1.100 + sub-cap, volatile ≤ 100, tools ≤ 2.000, total ≤ 5.000) atas `testdata/p1/all-modules.{request,tiers}.json`. `bench/budget` `TestFixedOverhead/all-modules` menegakkan alokasi yang sama dengan o200k_base plus aturan baris katalog SPEC §5.2: tepat 24 baris grup, masing-masing ≤ 15 tok o200k. Gate itu menemukan dua draf baris yang lolos validasi registry (≤ 15 kata) tetapi 16/18 tok o200k; validasi kata ADR-119 lebih longgar dari batas token, jadi baris katalog modul nyata wajib ikut diukur di hook ini. Sejak `Assembler.Catalog` menegakkan ≤ 15 tok per baris dengan estimator runtime (butir 1, REVIEW-F2-SEC4 ASM4-4), baris fixture `code` dipendekkan (`symbols, ` dihapus: 16 → 13 tok estimator) agar 24 grup tetap dirender tanpa overflow; `TestAllModulesFixtureShape` menegakkannya. Hanya baris itu yang berubah di golden `all-modules` (9 byte); golden `f1-core` byte-identik.
15. **Terukur (o200k_base, `cd bench && go test ./budget -run TestFixedOverhead -v`).** `all-modules` total **2.808** ≤ 5.000: stable 376, katalog 285, context 878 (facts 56, workspace 128, files 352, memori 246, skill 94), volatile 92, tools 1.070. `f1-core` tidak berubah: total 2.203 (stable 341, katalog 0, context 812, volatile 92, tools 897). Estimator fixture (ASCII, ≥ o200k): `all-modules` 2.980, `f1-core` 2.355.

**Dampak terukur akhir (SPEC §20).** Token: P1 `all-modules` 2.808/5.000 (headroom 2.192, cukup untuk baris MCP F5 ≤ 60); `f1-core` 2.203 tetap. LOC `assemble` 1.236/1.300. Dependency 0. Graf impor tidak berubah (`assemble → loop/event`).

## ADR-123 — `internal/budget` F2: guard harian/bulanan/provider, formula token gambar COST-09, dan pemicu compaction P8

**Status:** Accepted 2026-10-21 (F2; keputusan didelegasikan, opsi konservatif). Menerapkan SPEC §18.4, §12, §4.5/§4.7, P8/P27 dan GAP-COST-09; melengkapi ADR-058/067 (estimator teks), ADR-101 (biaya/ledger), dan ADR-074 (storage konsumen).

**Konteks.** ADR-067/101 menunda guard P27, formula token gambar, dan pemicu P8 ke F2. Registry model F1 belum membawa data `image_token_formula`, sehingga formula harus punya preset tertutup yang dipetakan composition root; guard butuh agregat periode dari `usage_ledger` yang belum ada (hanya `SessionTotals`), dan loop sudah punya `Compactor`/`CompactAt`/`OverflowAt` tetapi belum ada tipe trigger bersama.

**Keputusan.**
1. **Guard.** `budget.Guard` mengevaluasi cap harian, bulanan, dan per provider lewat `Checker.Check(ctx, Request) (Decision, error)`; `Request` membawa provider dan proyeksi biaya. Storage adalah interface konsumen satu method `TotalsSource.Totals(ctx, fromMS, toMS, provider) (costMicros, unknown, err)` (E-09); `ctxdb/ledgerstore.Source` memenuhi method set tanpa mengimpor `budget`. Guard tidak punya state global: map peringatan milik instance, jam disuntik (`func() time.Time`), dan aman dipakai konkuren.
2. **Ambang dan periode.** `0` berarti tanpa cap (SPEC §12). Proyeksi `used + estimate ≥ limit` menolak (100% inklusif); `≥ warn_at` (default 0,80) mengembalikan peringatan sekali per cap per periode. Harian/bulanan memakai kalender lokal dari zona jam yang disuntikkan; provider memakai bulan yang sama. Urutan evaluasi tetap harian → bulanan → provider, sehingga alasan penolakan deterministik. Reset = batas periode berikutnya dalam unix ms.
3. **Biaya tak diketahui gagal-tertutup.** Baris ledger ber-`cost_usd_micros` NULL, proyeksi yang tidak punya harga (negatif), atau jumlah negatif/overflow membuat guard menolak dengan `budget_unknown_cost` bila ada cap moneter. SPEC §18.4 tetap memberlakukan cap token; guard memilih tidak meloloskan belanja yang tidak bisa dibuktikan. Ditolak: menghitung unknown sebagai 0 (cap USD tidak bisa ditegakkan) dan saturasi ke `limit` (menyembunyikan penyebab).
4. **Dikonsultasikan sebelum call.** `provider.Config.Guard` menerima `budget.Checker`; `OpenAI.Do` memanggilnya sebelum setiap route (termasuk fallback). Proyeksi = estimasi input mentah × harga input + `max_tokens` × harga output lewat `budget.Cost` (dibulatkan ke atas); harga tidak diketahui → `-1`. Penolakan dikembalikan sebagai `*budget.DeniedError` berkelas non-retryable dan **tidak** mencoba fallback (fallback hanya menambah belanja). `Config.OnBudget` melaporkan verdict/peringatan ke pemanggil untuk event `budget_warning`.
5. **Formula token gambar.** Empat preset di `internal/budget/image.go`, tanpa dependency decoding: `openai_tiles` (fit 2048², sisi terpendek 768, `85 + 170 × tile 512²`), `anthropic_area` (`⌈w×h/750⌉`), `gemini_tiles` (`258` bila ≤384², selain itu `258` per tile 768²), dan `area_750` sebagai fallback konservatif untuk model tak dikenal. `MaxImageDim` 1.048.576 px; dimensi di luar batas atau formula tak dikenal = `ErrImage` (tidak pernah 0 token). Nama preset adalah nilai `image_token_formula` yang dipetakan composition root dari registry/model limits (GAP-COST-02 tetap deliverable terpisah).
6. **Pemicu P8.** `internal/compaction` memiliki `Checker`, `Trigger{Kind, Percent, Estimate, Window}`, dan interface satu method `Decider` sebagai seam assembly/loop. Ambang default 75% (compaction) dan 90% (overflow), inklusif pada batas; estimasi negatif atau window ≤ 0 menghasilkan `KindNone`/`Percent -1`; aritmetika persen integer dibatasi agar tidak membungkus. Prune deterministik dan summary tetap implementasi compaction (tidak ada di PR ini).
7. **LOC.** `budget` 400 → 520 (terukur 481, scc non-test; guard + formula gambar) dan Total core ENGINEERING §2.4 35.500 → 35.620; `compaction` tetap 900 (terukur 82). `ctxdb/ledgerstore` tetap 300 (terukur 116).

**Dampak terukur (SPEC §20).** Token model/prompt 0; dependency baru 0. `BenchmarkGuard` (tiga cap, sumber in-memory) ≈ 278 ns/op, 0 alloc/op; `BenchmarkImageTokens` 4,5–6,1 ns/op, 0 alloc/op. Agregat guard memakai `idx_usage_time(created_at, provider)`; pada ledger 1.000.000 baris, query range harian + filter provider terukur p50 66,9 ms, p95 76,9 ms, max 78,2 ms (20 query, Linux amd64), cukup untuk pemeriksaan pra-call. Test: `TestBudgetGuardDaily/Monthly/Provider/WarningOncePerPeriod/UnknownCostFailsClosed/DeterministicOrder/ZeroIsUnlimited`, `TestImageTokens`, `TestParseImageFormula`, `TestCompactionTrigger`, `TestWindowTotals`, dan `TestBudgetGuardBeforeCall` (provider tidak menerima request setelah penolakan).

**Konsekuensi.** (+) Cap harian/bulanan/provider dapat diuji deterministik tanpa jam dinding; gambar menambah estimasi konservatif tanpa decoder; ambang P8 punya satu tipe. (−) Provider tanpa harga akan ditolak saat cap USD aktif sampai harga/`tool_pricing` tersedia (sengaja fail-closed); formula OpenAI untuk rasio ekstrem bisa melebih-lebihkan (aman untuk budget, bukan untuk tampilan). Model limits/harga efektif (GAP-COST-01/02) tetap pekerjaan F2 lanjutan dan tidak diubah di PR ini.

## ADR-125 — Harga bertanggal GAP-COST-01: `budget.Prices`, pemilihan per waktu call, `pricing_version` di ledger

**Status:** Accepted 2026-10-21 (F2; keputusan didelegasikan, opsi konservatif). Menerapkan GAP-COST-01 (SPEC §22.1, §4.1, §11.2, §18.4) dan melanjutkan ADR-101 butir 4 serta ADR-123 butir 3–4 yang menunda harga efektif.

**Konteks.** `budget.Price` F1 adalah satu konstanta per route (`provider.Route.Price`) tanpa tanggal berlaku, sehingga perubahan harga terjadwal (bukti GAP-SWEEP COST-01: Sonnet 5 $2/$10 sampai 2026-08-31 lalu $3/$15) tidak bisa direpresentasikan dan ledger mencatat versi apa pun yang kebetulan terpasang. Registry `providers.toml` belum ada (paket `provider/registry` masih kosong), jadi bentuk data harus netral terhadap sumber (registry embedded atau `config`).

**Keputusan.**
1. **Entri bertanggal.** `budget.PriceEntry{Price, From, Until}` berlaku pada interval setengah terbuka `[effective_from, effective_until)` dalam instan UTC; `Until` nol = terbuka (belum ada pengganti). `budget.NewPrices(...)` mengurutkan entri dan menolak (`ErrPricing`) `From` nol, `Until ≤ From`, `pricing_version` kosong atau `none`, harga negatif, dan **rentang yang tumpang tindih** (termasuk entri terbuka yang diikuti entri lain, awal yang sama, dan rentang bersarang). Rentang bersebelahan (`Until` = `From` berikutnya) sah. Ditolak: memilih entri terbaru saat tumpang tindih (tagihan ambigu disembunyikan).
2. **Pemilihan per waktu call.** `Prices.At(t)` mengembalikan entri yang memuat `t`, atau `nil` sebelum entri pertama, di celah, atau sejak `Until` terakhir (kedaluwarsa); 0 alokasi. `provider.Route.Price` diganti `Route.Prices`; `provider.Config.Now` (nil = `time.Now`) disuntik dan `OpenAI.Do` membaca jam **sekali di awal call**, sehingga proyeksi guard semua route dalam rantai fallback dan `Response.Price` memakai instan yang sama.
3. **Ledger.** `Response.Price` adalah harga yang dipakai proyeksi; `budget.Entry.Account(usage, resp.Price, raw)` mengisi `usage_ledger.pricing_version` dari entri itu (tetap `none` bila biaya tidak diketahui, ADR-101 butir 4). `usage.cost` provider tetap menang atas tabel.
4. **Gagal-tertutup.** Harga tidak ada atau kedaluwarsa = biaya tidak diketahui: proyeksi `-1`, guard menolak `budget_unknown_cost` bila ada cap moneter (ADR-123 butir 3), request tidak dikirim, dan tidak ada fallback.
5. **LOC.** `budget` 520 → 560 (terukur 530, scc non-test); Total core ENGINEERING §2.4 35.620 → 35.660 ≤ 47.000. `provider` tetap 2.000 (terukur 1.652).

**Dampak terukur (SPEC §20).** Token model/prompt 0; dependency baru 0; latensi: `BenchmarkPricesAt` (3 entri) 23,6–24,1 ns/op, 0 alloc/op. Test: `TestPricingEffectiveDate` (contoh SPEC: request 2026-10-17 → $3/$15, batas `from` inklusif/`until` eksklusif, instan UTC lintas zona, ledger `pricing_version`), `TestPricingExpiredFailsClosed`, `TestPricingRejectsInvalid` (tumpang tindih 4 bentuk + entri cacat), `TestBudgetGuardTimeCorrectPrice` (guard nyata: proyeksi dan `Response.Price` mengikuti jam call; tepat di `Until` pengganti berlaku; setelah kedaluwarsa `budget_unknown_cost` dengan 0 request).

**Konsekuensi.** (+) Perubahan harga terjadwal dapat dicatat sebelum berlaku tanpa rilis pada tanggalnya; ledger dapat diaudit per versi. (−) Belum ada sumber data: tabel `pricing` `providers.toml` + override `config` (Q22), gate CI `TestPricingTableNotExpired`, dan peringatan `damnbot doctor` > 60 hari tetap pekerjaan registry; sampai itu tersedia composition root tidak memasang harga sehingga cap USD yang aktif menolak call (perilaku ADR-123 tidak berubah).

**Amandemen 2026-10-21 (ADR-128, REVIEW-F2-SEC6-6).** Untuk route yang punya entri tabel harga, butir 3 diubah: biaya yang ditagih adalah `max(biaya provider valid, harga tabel)`, sehingga reseller/proxy yang melaporkan 0 atau nilai kecil tidak dapat melewati cap moneter. `cost_source`/`pricing_version` mencatat nilai yang benar-benar ditagih (provider → `pricing_version = none` karena nilai itu tidak berasal dari tabel). Route tanpa entri tabel tetap mengikuti butir 3 apa adanya.

## ADR-126 — `model_limits` GAP-COST-02/PROV-02: `registry.Limits`, `ctxdb/limitstore`, dan pemetaan model → `image_token_formula`

**Status:** Accepted 2026-10-21 (F2; keputusan didelegasikan, opsi konservatif). Menerapkan bagian persist F2 GAP-COST-02 dan GAP-PROV-02 (SPEC §22.1, §11.9 `model_limits`, §4.1) serta pemetaan `image_token_formula` yang ditunda ADR-123 butir 5; storage mengikuti ADR-074 (E-04/E-09).

**Konteks.** Larangan probe (subset F1, ADR-068) sudah ada, tetapi window model belum punya sumber berurutan maupun persistensi: `Route.ContextWindow` diisi langsung oleh composition root dan tabel `model_limits` (migrasi 0007) belum dipakai. Formula token gambar (ADR-123) punya preset tetapi belum ada pemetaan per model atau jalur ke estimator.

**Keputusan.**
1. **Urutan sumber.** `internal/provider/registry.Limits.Window(ctx, provider, model)` mengikuti teks SPEC GAP-COST-02: override `config` → `registry` → `discovery` (`/models`) → `learned` (error `context_length` nyata/usage) → `fallback` 32.768 (PROV-02). Ditolak: registry di atas config — SPEC menyebut config sebagai *override* dan PROV-02 menandai override per model `source=config`. Window < 8.192 dari storage = `ErrWindowTooSmall` (model ditolak dengan pesan, tanpa floor 64K); `config`/`registry` < 8.192 ditolak saat `NewLimits`. Error storage dikembalikan, tidak diganti fallback. Tidak ada request apa pun ke provider (COST-02).
2. **Yang dipersist.** Hanya `discovery` dan `learned` yang ditulis ke `model_limits`; `config`/`registry` adalah input statis `map["provider/model"]` dan `fallback` dihitung, sehingga baris `config` basi tidak bisa mengalahkan discovery setelah config berubah. Dalam satu baris PK `(provider, model)`, upsert bersyarat satu statement: `discovery` mengganti `learned` atau discovery lama; `learned` hanya mengganti `learned` (tanpa read-modify-write lintas proses). `Observe` menolak (`ErrLimits`) sumber lain, window ≤ 0, provider/model kosong; `observed_at` dari jam yang disuntik.
3. **Seam storage.** `registry.Store` = interface konsumen 2 method (`Load`, `Save`) bertipe primitif; `internal/ctxdb/limitstore.Store` memenuhi method set tanpa mengimpor `registry` (matriks import: `storeImports()`), dicek `var _ registry.Store = limitstore.Store{}` di test composition root.
4. **Gambar.** `budget.NewImageFormulas(map["provider/model"]nama)` memvalidasi nama `image_token_formula` yang dideklarasikan registry/config (nama tak dikenal = `ErrImage`); `For(ref)` memberi preset model atau `area_750` bila tidak dideklarasikan. Ditolak: menebak dari nama keluarga model, karena anggota satu keluarga memakai formula berbeda (mis. varian mini OpenAI) dan tebakan salah meng-under-estimate. `Counts.Image` + `Counts.AddImage(f, w, h)` memasukkan token gambar ke estimator COST-09 yang sama (`Raw` = `est_input_tokens`, `Estimate` terkalibrasi); dimensi/formula buruk = `ErrImage`, tidak pernah 0 token.
5. **LOC.** `provider/registry` 400 → 300 dan baris baru `ctxdb/limitstore` 100 (dipotong dari baris konsumennya, ADR-074; Total core tetap 36.520); terukur `provider/registry` 84, `ctxdb/limitstore` 46, `budget` 559/560 (tanpa kenaikan).

**Dampak terukur (SPEC §20).** Token model/prompt 0; dependency baru 0; 0 request probe. `Window` = paling banyak 1 query PK `model_limits` (lookup map lebih dulu). Test: `TestModelLimitsPrecedence`, `TestLearnedContextWindow`, `TestModelLimitsFailClosed` (`registry`), `TestModelLimitsRoundTrip` (tutup/buka ulang DB), `TestModelLimitsDiscoveryOverLearned` (`limitstore`), `TestModelLimitsPersisted` (registry + limitstore + ctxdb nyata, seluruh urutan sumber setelah restart), `TestImageFormulaMapping`, `TestEstimatorImages`.

**Konsekuensi.** (+) Window model lokal/tak dikenal mulai di 32K dan terkoreksi permanen setelah satu observasi; formula gambar per model eksplisit dan gagal-tertutup. (−) Sumber data masih harus disambungkan: `providers.toml` (window + `image_token_formula`), kunci config `[[providers.custom]] models[].context_length`, discovery `/models`, parser window dari error `context_length`, dan pemakaian `Limits` oleh composition root/assemble (mode degradasi 8K–64K) — sampai itu `Route.ContextWindow` tetap dari `loop.turn_max_input_tok`.

## ADR-127 — Gate CT-09 di runner CI bersama: p95 selisih berpasangan terhadap referensi tulis-durabel (interim tanpa `bench-ref`)

**Status:** Accepted 2026-10-21 (keputusan owner atas draft opsi 2; opsi 1, runner tetap `bench-ref`, ditolak untuk sekarang). Statistik gate direvisi sebelum merge (keputusan owner 2026-10-21): p95 selisih berpasangan per iterasi menggantikan p95(edit) − p95(referensi). Melengkapi ADR-077 butir 4 dan ADR-094 butir 5. Tidak mengubah ADR-073 (budget absolut benchmark tetap di job `perf`, sampel 100, p95).

**Aturan penerapan.** Diterapkan (2026-10-21): CODE-TOOLS §10 (prosedur ukur) dan §10.2 (baris CT-09, kedua tabel); kode `internal/codetools/bench_test.go` (`measureDurable`, `durableWrite`, `durableGate`), `internal/testutil/timing` (`SharedRunner`); bukti `bench/codetools/results/ct09-ci-jobs-2026-10-21.csv`. Setiap passage mengutip ADR-127.

**Konteks.** CT-09 (edit_file 1 MB termasuk `fsync` file + dir, p95 ≤ 20 ms) ditegakkan job `perf` di runner GitHub bersama dengan margin ×1,5 (ADR-077 butir 4, ADR-094 butir 5), padahal ROADMAP F1 #0, E-59, dan ADR-073 meminta runner tetap. Dari 315 job `perf` (19–21 Oktober 2026, semua attempt; CSV di atas), 38 gagal CT-09 (12,1 %): EPYC 7763 2 %, EPYC 9V74 31 %, 9V45 15 %, Xeon 8573C 21 %. Pohon `internal/codetools` yang sama lulus dan gagal, dan rerun commit yang sama berpindah antara 44 dan 268 ms. Sub-benchmark CPU di job yang gagal normal, sedangkan iowait 17–41 %. Di instance 9V74 dengan disk `MSFT NVMe Accelerator`, write request 3–13 ms dan p95 per sampel dari 3.000 edit = 30,1 ms, tanpa terpengaruh `sync(2)`, isolasi, atau `nodiscard`. Margin ×1,5 menutup runner yang seragam lebih lambat, bukan ekor I/O per instance. Akibatnya gate absolut mengukur disk VM, bukan kode.

**Keputusan.**
1. **Referensi.** `BenchmarkCodetools/Edit1MB` mengukur, berselang-seling di setiap iterasi (urutan bergantian, di luar ns/op), referensi tulis-durabel: isi berukuran sama ditulis ke temp di direktori yang sama, `fsync(file)`, rename menimpa, `fsync(dir)` bila platform punya (primitif engine yang sama: `renameReplace`, `dirSyncable`). Referensi ini tidak mengerjakan apa pun milik edit_file (baca, match, diff, hash, salin mode/xattr, read-back). Tiap iterasi i menghasilkan satu pasangan (edit_i, ref_i). Metrik `paired-p95-ms`, `p95-ms`, dan `ref-p95-ms` selalu dilaporkan.
2. **Gate runner bersama (build `perf`).** p95 atas i dari (edit_i − ref_i) ≤ 20 ms × 1,5 = 30 ms (hasil negatif = 0). Stall disk yang mengenai satu iterasi jatuh ke kedua sampel iterasi itu dan saling meniadakan; biaya yang ditambahkan edit_file di atas satu tulis durabel tidak. Berbeda dengan p95(edit) − p95(referensi), burst yang hanya menaikkan referensi di beberapa iterasi tidak lagi menurunkan statistik di iterasi lain, sehingga tidak bisa meloloskan regresi nyata.
3. **Gate absolut di Q14.** Di luar build `perf` (`DAMNBOT_TIMING=1`, ≥ 100 sampel) yang ditegakkan tetap p95(edit) ≤ 20 ms apa adanya. Nilai ini wajib diverifikasi di mesin referensi Q14 saat acceptance/RC (seperti CT-10 ≤ 6 ms p95).
4. **CT-04..CT-07 tetap absolut ×1,5.** Hanya CT-09 yang dibatasi `fsync`; benchmark lain tidak berubah.
5. **Interim.** Bila runner tetap `bench-ref` tersedia, job `perf` pindah ke sana dan butir 2 dicabut lewat ADR baru.

**Dampak terukur (SPEC §20).** Token/latensi produksi/binary/dependency 0. LOC: `internal/testutil/timing` +2 (konstanta `SharedRunner`). Waktu job `perf` naik ≈ 100 tulis durabel 1 MB per run (≈ 0,1–1 s di runner sehat). Validasi lintas runner dilakukan dengan workflow diag sekali pakai: 8 gelombang × 3 job `ubuntu-latest`, run 39601466828 … 39609554582. Ke-24 job terdiri dari EPYC 7763/`sda` 18, 9V74/`sda` 1, 9V45/NVMe 3, Xeon 6973P/NVMe 1, dan 9V74/NVMe 1. Tiap job menjalankan 12 run `BenchmarkCodetools` 100× ditambah regresi buatan per edit.
- **Statistik yang dipilih, p95 selisih berpasangan (gelombang 7–8):** pada disk NVMe yang bursty gagal 1/36 run tanpa regresi dan menangkap regresi +35 ms 6/6.
- **Pembanding, p95(edit) − p95(referensi) (gelombang 1–8):** di runner sehat (`sda`) 228/228 run tanpa regresi lulus (selisih 5–9 ms, referensi p95 1,3–3,6 ms), +35 ms tertangkap 19/19, +15 ms lolos 76/76, +4 tulis durabel tertangkap 3/76. Di disk NVMe yang bursty 2/60 run tanpa regresi gagal (48,5 ms Xeon, 72,3 ms 9V45; gate absolut ×1,5 gagal ≥ 4/60 pada run yang sama), dan +35 ms hanya tertangkap 5/8. Di instance 9V74/NVMe hanya 1/4, karena p95 referensi ikut melonjak (19–136 ms) dan memotong regresi. Kegagalan ini yang ditutup oleh pemasangan per iterasi.
- **Ambang tetap 30 ms.** Data di atas tidak menuntut nilai lain: overhead sehat 5–9 ms menyisakan ruang ≥ 21 ms terhadap false failure, dan +35 ms tertangkap seluruhnya dengan statistik berpasangan.

**Konsekuensi.** (+) Gate CT-09 di PR tidak lagi mengukur disk VM. Regresi besar yang menambah kerja sinkron di jalur edit (+35 ms: 6/6) tetap tertangkap, juga bila burst menaikkan referensi. (−) Sensitivitas di runner bersama lebih rendah daripada gate absolut. Batas sensitivitasnya ≈ +15 ms: regresi di bawahnya tidak tertangkap di PR (+15 ms lolos 76/76 di runner sehat, overhead sehat 5–9 ms + 15 ms < 30 ms), dan regresi antara ≈ +15 dan +35 ms hanya tertangkap sebagian. Regresi itu baru tertangkap oleh gate absolut ≤ 20 ms di Q14 saat acceptance/RC. Di disk bursty, gate masih kadang gagal tanpa regresi (1/36 pada sampel di atas). Sampel statistik berpasangan (36 run bursty, 6 run regresi) lebih kecil daripada sampel pembanding.


## ADR-128 — Tabel harga registry `providers.toml`: kunci base_url+model, otoritas `vendor`/`operator-contracted`, validasi saat load

**Status:** Accepted 2026-10-21 (F2/W5; keputusan pemilik 2026-10-21 untuk harga TeamRouter). Menutup konsekuensi ADR-125 ("belum ada sumber data") dan menerapkan tahap registry Q22 (tanpa override config).

**Konteks.** ADR-125 menunda sumber harga ke `providers.toml`, sementara route komersial tidak selalu berasal dari vendor model: smoke F1 memakai `https://api.teamorouter.com/v1` + `gpt-5.4-mini` dengan harga yang disepakati operator ($0,15/$0,015/$0,60 per Mtok). Harga daftar vendor untuk nama model yang sama tidak boleh dipakai untuk route reseller (bisa over- atau under-charge), dan belum ada jalur data untuk mencatat harga kontrak.

**Keputusan.**
1. **Data embedded.** `internal/provider/registry/providers.toml` (`go:embed`) memuat `version` tabel dan `[[price]]`: harga integer micro-USD per Mtok (`input`/`cached_input`/`output`), `from`/`until` (tanggal UTC atau RFC3339; `until` opsional), `source` wajib, `kind` (`vendor` = harga daftar vendor, `operator-contracted` = harga kontrak reseller), dan `id` unik. `pricing_version` = `<version>/<id>` dan dicatat ledger. API: `registry.ParsePriceTable`/`DefaultPriceTable`.
2. **Kunci route aktual.** `Lookup(base_url, model)` mencocokkan base_url ternormalisasi + model persis; model yang sama di base_url lain tetap tidak berharga (route reseller tidak mewarisi harga vendor). `NormalizeBaseURL` menurunkan scheme/host, membuang port default dan garis miring akhir, membandingkan escaped path persis, dan menolak query/fragment/userinfo, sehingga URL aneh tidak pernah "kebetulan" cocok (tetap unpriced = arah gagal-tertutup).
3. **Validasi saat load.** `ErrPrice` untuk TOML rusak, kunci tak dikenal (typo field), `version`/`id`/`source`/`model` kosong, `kind` tak dikenal, tanggal hilang/rusak, `until ≤ from`, harga negatif, `id` duplikat, dan rentang tumpang tindih per route. Composition root (`cmd/damnbot run`) memanggil `DefaultPriceTable` + `provider.RoutePrices` saat start dan gagal bila tabel tidak valid; tidak ada fallback diam-diam ke "semua unpriced".
4. **Penyambungan.** `provider.RoutePrices(table, baseURL, model)` mengubah entri registry menjadi `budget.Prices` (tipe ADR-125; tidak ada konvensi kedua) dan dipasang pada `provider.Route.Prices`, sehingga guard dan ledger memakai jalur lookup yang sama. Route tanpa harga → proyeksi `-1`; cap moneter menolak `budget_unknown_cost` sebelum request (ADR-123 butir 3, ADR-125 butir 4).
5. **Harga yang dicatat.** Satu entri `operator-contracted`: TeamRouter `https://api.teamorouter.com/v1` + `gpt-5.4-mini`, $0,15/$0,015/$0,60 per Mtok (150000/15000/600000 µUSD/Mtok), `from = 2026-10-21`, `source = "operator-contracted, owner decision 2026-10-21; docs/F1-LLM-SMOKE.md"`. Tidak ada harga vendor yang ditambahkan: hanya harga yang dapat dikutip dari dokumen repo.
6. **LOC.** `provider/registry` tetap 300 (terukur 282, sisa 18; file TOML tidak dihitung). Amandemen SEC6-6 menaikkan `budget` 560 → 580 (terukur 571); pada rebase ke main (total core 38.090 saat itu) Total core menjadi 38.110.

**Dampak terukur (SPEC §20).** Dependency baru 0 (`github.com/BurntSushi/toml` sudah require langsung); 0 request jaringan; tabel di-parse sekali per `run`. Test: `TestDefaultPriceTableTeamRouter` (lookup base_url+model, alias normalisasi, model sama di base_url lain unpriced), `TestNormalizeBaseURL`, `TestParsePriceTableRejectsInvalid` (TOML rusak, kunci tak dikenal, source/harga/tanggal/kind cacat, id duplikat, tumpang tindih; rentang bersebelahan sah), `TestRoutePricesTeamRouter` (1.000/200/500 token = 423 µUSD, `pricing_version` ledger, pembulatan ke atas), `TestRoutePricesResellerRouteFailsClosed` (guard harian nyata → `budget_unknown_cost`, 0 request).

**Konsekuensi.** (+) Harga reseller dicatat dengan otoritas dan sumber yang dapat diaudit; route reseller tidak bisa memakai harga vendor; tabel yang salah ketik gagal saat start. (−) Override `[[providers.custom]]`/config (Q22), gate `TestPricingTableNotExpired`, dan peringatan `doctor` > 60 hari belum ada; menambah harga vendor perlu kutipan dokumen repo, dan perubahan harga operator-contracted perlu keputusan pemilik baru.

**Amandemen 2026-10-21 (REVIEW-F2-SEC6, #178).** Menutup SEC6-1..4 dan menetapkan pilihan SEC6-5.
1. **Ledger `run`.** `event.Usage.Price` (tipe wire di `loop/event`, tetap leaf) membawa entri jadwal dari `provider.Response.Price` melalui `loop.Reply`; `ctxLedger.Record` memanggil `budget.Entry.Account` dan menulis baris `usage_ledger` dari `budget.Entry` (konversi polos). Biaya route berharga = `max(harga tabel, biaya provider valid)` (SEC6-6, amandemen round 2); biaya negatif/NaN tidak pernah mengurangi spend (jatuh ke tabel atau unknown), `+Inf`/di atas `MaxProviderCostUSD`, token negatif, dan `cached > input` menghasilkan `ErrInvalidUsage` yang menggagalkan turn (tidak ada baris yang bisa menutupi cap). `pricing_version` diisi versi entri tabel bila tabel yang ditagih, `none` bila provider yang ditagih atau biaya tidak diketahui.
2. **Kolom harga wajib.** `input`, `cached_input`, dan `output` adalah pointer TOML: kunci yang tidak ada = `ErrPrice` saat load, bukan harga 0. Harga negatif tetap ditolak.
3. **Id/model.** `plainToken` (`[A-Za-z0-9._:/-]+`) menolak spasi (termasuk di tepi), karakter kontrol, dan karakter lain, sehingga `pricing_version` tidak dapat memuat id dengan spasi/kontrol dan model spasi tidak lolos.
4. **Entri open-ended (SEC6-5).** Pilihan: **tanpa batas umur**. Harga kontrak berlaku sampai pemilik menetapkan pengganti; memaksa `until` akan membuat call setelah tanggal itu unknown-cost dan menolak semua turn di bawah cap sampai rilis berikutnya. Gate `TestPricingTableNotExpired` dan peringatan `doctor` > 60 hari tetap konsekuensi yang belum dikerjakan.
5. **Test.** `TestRunLedgerRecordsRoutePrice` (adapter → `loop.Reply` → ledger: 423 µUSD, `pricing_table`, versi entri), `TestRunLedgerValidatesProviderCost` (provider valid menang; negatif/NaN tidak menurunkan spend; `+Inf`/`cached > input`/token negatif gagal tanpa baris), serta kasus tolak kolom harga hilang dan id/model berspasi di `TestParsePriceTableRejectsInvalid`.

**Amandemen round 2 — 2026-10-21 (REVIEW-F2-SEC6 SEC6-6/7/8).**
1. **SEC6-6 (HIGH), penagihan route berharga.** `budget.Cost` menagih `max(harga tabel, biaya provider valid)` bila entri tabel berlaku: reseller/proxy yang mengirim `usage.cost = 0`, kecil, negatif, atau NaN tidak dapat menurunkan spend di bawah harga kontrak. `cost_source` dan `pricing_version` menggambarkan nilai yang benar-benar ditagih: provider yang menang → `provider`/`none`, tabel → `pricing_table`/`<version>/<id>`. Route tanpa entri tabel tidak berubah (ADR-125 butir 3, diamandemen oleh catatan di ADR-125).
2. **SEC6-8 (MEDIUM), usage tidak masuk akal tetap terlihat.** Bila `budget.Entry.Account` menolak usage (`+Inf`/di atas batas, token negatif, `cached > input`), `ctxLedger.Record` tetap menulis baris `usage_ledger` dengan `cost_usd_micros = NULL`, `cost_source = unknown`, `pricing_version = none`, dan token di-clamp ke nilai non-negatif/konsisten; `Record` lalu mengembalikan error sehingga turn gagal secara terlihat. Guard melihat baris NULL sebagai biaya tidak diketahui dan menolak call berikutnya di bawah cap (ADR-123 butir 3) — tidak ada lagi call tak terbatas yang tak terlihat.
3. **SEC6-7 (stack).** PR di-rebase ke `main`: `DefaultPriceTable` + `provider.RoutePrices` dibangun di dalam `newRunProvider`/`newRunProviderWithTable`, dan `Prices` terpasang pada route. Guard harian/bulanan belum ada di main; kombinasi harga × guard diuji oleh #173 (`TestRunBudgetGuardPricedRoute`) setelah wiring budget mendarat.
4. **Nit id.** `id` entri tidak boleh memuat `/` (pemisah `pricing_version`); `model` tetap boleh (`org/model`).
5. **Test baru/ubah.** `TestRunLedgerRecordsRoutePrice` (adapter → `loop.Reply` → ledger: 423 µUSD, `pricing_table`, versi entri), `TestRunLedgerValidatesProviderCost` (provider di bawah tabel dinaikkan, di atas tabel menang, negatif/NaN/0 tidak menurunkan; route tanpa harga memakai biaya provider; usage mustahil → baris NULL), `TestCost`/`TestAccount` (max dan versi `none` saat provider menang), `TestParsePriceTableRejectsInvalid` (id dengan `/`).
6. **LOC.** `budget` 560 → 580 (terukur 571; Σ core +20), Total core 38.090 → 38.110 ≤ 47.000; `provider/registry` tetap 300 (terukur 282).

## ADR-131 — Carrier reasoning opaque GAP-PROV-01: `ctxdb/carrierstore`, kunci (family, model), proyeksi per route, dan penolakan signature tahan restart

**Status:** Accepted 2026-10-21 (F2 storage; proyeksi provider/assemble menyusul di PR terpisah pada workstream yang sama). Menerapkan GAP-PROV-01 (SPEC §4.1, §11.9, §14.3; CROSS-CUTTING §1.3) di atas tabel `reasoning_carriers`/`reasoning_carrier_rejections` yang sudah ada di migrasi 0007.

**Konteks.** Provider mewajibkan blok reasoning opaque dikembalikan byte-identik ke provider yang sama (Gemini `thought_signature`, Anthropic `thinking.signature`/`redacted_thinking`, OpenAI Responses `encrypted_content`, Copilot `reasoning_opaque`). Tanpa replay, sesi tool-calling Gemini 3/Claude thinking gagal 400 setelah resume; mengirimkannya ke route lain membocorkan data lintas provider dan tetap ditolak. Tabelnya sudah ada, tetapi belum ada store Go maupun proyeksi per route.

**Keputusan.**
1. **Store konsumen.** `internal/ctxdb/carrierstore.Store{DB}` (ADR-074, E-09) mengimpor hanya `ctxdb`. API: `Save(ctx, sessionID, eventID, []Carrier) ([]Ref, error)`, `Load(ctx, sessionID, eventIDs) ([]Stored, error)`, `Reject(ctx, sessionID, carrierIDs, code, at)`, `Rejected(ctx, sessionID)`, plus `PayloadSHA`/`Refs` agar pemanggil dapat menaruh `carriers:[{ord,sha}]` di payload event assistant **sebelum** event di-append (hash-chain P9 mencakup byte carrier).
2. **Byte-identik.** `payload` disimpan apa adanya (tanpa re-encode JSON) dan `payload_sha` = hex sha256; `Load` mengembalikan salinan byte. `Replayable=false` untuk carrier plaintext yang teksnya diredaksi (nanti di-drop dengan `carrier_dropped{reason:"redacted"}`); carrier opaque tidak di-scan regex (SPEC §14.3).
3. **Ikatan sesi.** `Save` memverifikasi `eventID` milik `sessionID` sebelum menulis; event sesi lain, event hilang, `ord` duplikat/negatif, kind di luar himpunan CHECK, payload kosong, atau field kotor → `ErrCarrier` tanpa baris. `Load` hanya mengembalikan carrier untuk event milik sesi itu.
4. **Append-only.** Kedua tabel append-only (trigger 0007); tidak ada update/delete di store. Purge tetap milik tangga `ctxdb`.
5. **Penolakan signature.** `Reject` menulis `reasoning_carrier_rejections(carrier_id, session_id, error_code, created_at)` dengan `INSERT OR IGNORE` (PK `(carrier_id, session_id)`, idempoten), menolak carrier sesi lain, dan tahan restart (dibaca lewat `Rejected`). Ini dipakai saat provider membalas 400 signature invalid: carrier ditandai untuk sesi itu lalu request diulang **sekali** tanpa carrier tersebut (diimplementasikan di PR proyeksi).
6. **Proyeksi per route (desain; PR berikut).** `provider.Carrier{Family, Model, Kind, Anchor, Raw []byte}` pada `Message` assistant dan `Response`; `Route.Family` eksplisit (family protokol, bukan nama reseller). Encoder mengirim carrier hanya bila `(family, model)` route = asal; bila berbeda, carrier **tidak dikirim** dan `system_notice{kind:"carrier_dropped", reason, count}` dicatat (payload seragam, ADR-054 V-19), **bukan** baris rejection. Baris rejection hanya untuk signature yang ditolak provider. Tidak ada pencocokan host/prefix atau warisan lintas provider.
7. **LOC.** Baris baru `ctxdb/carrierstore` 250 (terukur 237; ADR-074), Total core 37.070 → 37.320 ≤ 47.000; `ctxdb` tetap 3.500 (terukur 3.421). `provider`/`loop`/`assemble` belum berubah di PR penyimpanan ini.

**Dampak terukur (SPEC §20).** Dependency baru 0; 0 request jaringan. Test: `TestCarrierRoundTrip` (byte biner/JSON identik, urutan `ord`, ref sha256), `TestCarrierLoadScoped` (event sesi lain/daftar kosong), `TestCarrierSaveValidation` (payload kosong, kind asing, ord negatif/duplikat, field kotor, event sesi lain/hilang), `TestCarrierAppendOnly` (UPDATE/DELETE ditolak trigger), `TestCarrierRejections` (idempoten, scoped, tahan restart).

**Amandemen SEC8 (#190).** (a) Payload assistant memakai `carriers:[{ord,sha}]` (SPEC §4.1); kunci `sha` dikecualikan dari validator 64-hex non-ref **hanya pada path `carriers[N].sha`** (bersama `preview_sha`/`args_sha`/`payload_sha` yang tetap berdasarkan nama kunci), sehingga `{"x":{"sha":H}}` dan `attachments[].sha` tetap ditolak (SEC8-7). (b) Batas ukuran (SEC8-5): satu carrier ≤ 256 KiB dan total carrier satu pesan ≤ 1 MiB; `Save` menolak (`ErrCarrier`) sebelum menulis, dan capture/proyeksi (PR proyeksi) membuang yang melebihi batas dengan `carrier_dropped{reason:"too_large"}`. (c) `make loc` di head ini memakai baris `ctxdb/carrierstore` 300 (terukur 263; Total core 37.070 → 37.370 ≤ 47.000), sehingga merge #190 sendirian tetap lolos.

**Konsekuensi.** (+) Carrier dapat disimpan dan diaudit per event dengan hash-chain; penolakan signature tahan restart; ukuran dibatasi; batas kepercayaan jelas (store tidak memilih route). (−) Capture/proyeksi provider, event `carrier_dropped`, retry sekali tanpa carrier, dan enkripsi at-rest `payload` (CROSS-CUTTING §1.3) belum ada di PR penyimpanan ini; urutan merge dengan stack assemble (#166–#168) harus dijaga agar proyeksi memakai satu titik (assembler/encoder), bukan dua.

**Amandemen round 2 — 2026-10-21 (proyeksi/capture carrier di provider).**
1. **Tipe wire.** `provider.Carrier{Family, Model, Kind, Anchor, Raw, Replayable, SHA}` pada `Message` (assistant) dan `Response`; `Route.Family` eksplisit, dengan fallback ke `Provider` (tanpa pencocokan prefix/host, sehingga reseller tidak mewarisi family vendor).
2. **Capture.** `Response.Reasoning` (array JSON) dipisah: part dengan `thought_signature`, `encrypted_content`, `reasoning_opaque`, atau `signature` (type `thinking`/`redacted_thinking`) disalin **byte-identik** ke `Response.Carriers` beserta sha256 dan anchor indeks; `Reasoning` hanya menyisakan part plaintext. Family/model dicap dari route yang menghasilkan, bukan dari pemanggil.
3. **Proyeksi.** Encoder hanya mengirim carrier dengan `(family, model)` **persis** sama dan `replayable=1`; selain itu tidak ada byte carrier di body request dan `CarrierDrops{reason: "route_mismatch"|"redacted", count}` dilaporkan (event `system_notice{kind:"carrier_dropped", reason, count}`). Tidak ada warisan lintas provider/prefix/credential.
4. **Signature invalid.** 400 dengan `error.code`/`error.type` ∈ {`invalid_signature`, `signature_invalid`} (terstruktur; teks pesan tidak diparse, E-21) dan ada carrier yang terkirim → **tepat satu** retry tanpa seluruh carrier; carrier yang dihilangkan dilaporkan sebagai `RejectedCarriers`. Tanpa carrier, 400 tetap non-retryable. Caller (loop) menulis baris rejection per sesi dengan mencocokkan sha256 carrier tersimpan.
5. **Persistensi & replay.** Loop menyimpan carrier bersama event assistant (`carriers:[{ord,sha}]` di payload sehingga hash-chain mencakup byte), memuatnya di `ctxStore.History` per sesi (join `events`, jadi sesi lain/fork tidak pernah menyumbang carrier). Bila redaksi mengubah teks reasoning, carrier **tidak disimpan sama sekali** (bukan sekadar `replayable=0`): byte plaintext yang terkena redaksi tidak boleh masuk DB/backup (SEC8-3). Carrier opaque tidak dipindai regex (SPEC §14.3).
6. **LOC.** `provider` 2.000 → 2.100 (terukur 2.010), `loop` 1.800 → 1.900 (terukur 1.851), `ctxdb/carrierstore` 250 → 300 (terukur 263), Total core 37.320 → 37.570 ≤ 47.000.
7. **Test.** `TestCarrierReplaySameRoute` (byte carrier ada di body), `TestCarrierReplayDropsAcrossRoutes` (family/model/prefix/reseller → 0 byte carrier + `route_mismatch`), `TestCarrierRedactedDropped`, `TestCarrierCapture` (byte identik, plaintext dipisah), `TestCarrierSignatureRetry` (2 request; kedua tanpa carrier; `RejectedCarriers`), `TestCarrierSignatureRetryOnce`, `TestCarrierSignatureIgnoredWithoutCarriers`, `TestCarrierPersistedWithEvent` (simpan+ref hash+notice+rejection), `TestCarrierRedactedNotReplayable`, `TestCarrierHistoryIsolationEndToEnd` (byte AAA/BBB tidak menyeberang sesi di body request), `TestCarrierRejectBySHA`.


**Amandemen SEC8 (#190/#193).**
1. **Payload ref (SEC8-1).** Kunci `sha` pada `carriers:[{ord,sha}]` (SPEC §4.1) dikecualikan dari validator 64-hex **hanya pada path `carriers[N].sha`** (SEC8-7); turn ber-carrier tidak lagi gagal `ErrInternal`. Test `TestCarrierRunPersistsWithRealStore` menjalankan `run` nyata + ctxdb nyata dan memeriksa baris `reasoning_carriers` serta ref di payload assistant.
2. **Rejection effective (SEC8-2).** `ctxStore.loadCarriers` menyaring `Rejected(session)`; carrier yang ditolak provider tidak pernah dimuat/di-replay lagi. `TestCarrierRejectedNotReplayed` memeriksa body request setelah penolakan.
3. **Redaksi (SEC8-3/8).** Bila redaksi mengubah `ReasoningText`, seluruh carrier respons dibuang sebelum persistensi; #193 juga memeriksa 0 baris, 0 byte secret di file DB/WAL, dan tidak ada carrier yang dimuat lagi (`TestCarrierRedactionDropsBytes`). Pemindaian setiap string dan **nama kunci** di dalam carrier ada di #200; yang dilewati hanya pasangan (tipe blok, field) opaque yang persis (`redacted_thinking.data`, `reasoning.encrypted_content`, `thought_signature`, `reasoning_opaque`, `thinking`/`redacted_thinking.signature`) (`TestCarrierNestedSecretDropped`, `TestCarrierNestedRedactionDropsBytes`, `TestCarrierNonOpaqueDataScanned`, `TestCarrierSecretInKeyDropped`, `TestCarrierOpaquePairsKept`).
4. **Family = route aktual (SEC8-4/9).** `run` memakai `provider.NormalizeBaseURL(base_url)` sebagai `Route.Family` (bukan nama provider); normalisasi menurunkan scheme/host, membuang port default, userinfo/query/fragment, dan garis miring akhir; URL yang tidak bisa diparse **ditolak dengan error** (tidak mengembalikan kredensial mentah) dan bracket IPv6 dipertahankan (`TestNormalizeBaseURL`: `/v1` = `/v1/`, userinfo dibuang, `[::1]:443` → `[::1]`).
5. **Batas ukuran (SEC8-5).** `carrierstore.Save` menolak satu carrier > 256 KiB dan total per pesan > 1 MiB (`ErrCarrier`, #190); capture/proyeksi (#200) membuang blok berlebih dengan `carrier_dropped{reason:"too_large"}` dan tidak pernah mengirimnya.
6. **LOC.** `loop` 1.900 → 1.950 (pemindaian carrier rekursif + nama kunci, terukur 1.913), Total core 37.270 → 37.320; `provider` tetap 2.100 (terukur 2.056).

## ADR-132 — GAP-REL-04/REL-05: probe engine SQLite + surface `version --verbose`, dan mode baca-saja untuk skema lebih baru

**Status:** Accepted 2026-10-21 (F2; keputusan didelegasikan, opsi konservatif). Menerapkan GAP-REL-04 dan GAP-REL-05 (SPEC §11.1, §11.11.7, §22.1; SELF-HEALING §1.2).

**Konteks.** `ctxdb.Open` menolak DB dengan `user_version` > skema binary (`ErrSchemaNewer`) dan `run` memetakannya ke exit 2, bukan 78; tidak ada jalur inspeksi read-only. Engine SQLite yang ter-link (modernc v1.60.1 → SQLite 3.53.4) belum diverifikasi terhadap lantai REL-04 (3.51.3, FTS5 `contentless_delete`, trigram, `json_valid`). `damnbot doctor` adalah F3 dan belum ada di main; `sessions ls/show/export` juga belum ada (wiring stack), jadi tidak ada perintah CLI inspeksi untuk disambungkan di PR ini.

**Keputusan.**
1. **Probe engine (REL-04).** `ctxdb.ProbeEngine(ctx)` membuka `:memory:` dengan driver yang sama dan mengembalikan `EngineInfo{Version, Major/Minor/Patch, FTS5, ContentlessDelete, Trigram, JSON}`; `Supported()` mensyaratkan ≥ `MinSQLiteVersion` 3.51.3 dan keempat probe. `TestSQLiteEngineVersion` gagal bila engine lebih lama/kehilangan fitur; test berjalan di job `test` matrix 6 runner (linux amd64/arm64, darwin amd64/arm64, windows amd64/arm64), sementara `build-matrix` mencakup 8 target kanonik (linux/arm dan android/arm64 hanya compile, sesuai ADR-073). Tidak ada `doctor` F2.
2. **Surface (REL-04).** `damnbot --version --verbose` mencetak `sqlite <versi> fts5=… contentless_delete=… trigram=… json=… supported=…` lewat probe yang sama; flag `--verbose` tanpa `--version` tetap diabaikan (perilaku usage lama).
3. **Baca-saja (REL-05).** `ctxdb.Options.ReadOnly` membuka **hanya** read pool dengan `mode=ro&_pragma=query_only(1)`; tidak menjalankan migrasi, barrier, holder, recovery, atau watcher, dan **tidak membuat** data dir, `blobs/`, `run/maint.holders/`, atau `backups/` (SEC9-1): `os.OpenRoot` atas direktori yang sudah ada. `Write`/`WriteContext` mengembalikan `ErrReadOnly` sebelum menyentuh handle. DB yang lebih baru (`user_version` > schemaMax) boleh dibaca; DB yang tidak ada gagal. `Close` menutup read pool tanpa menyentuh lock/checkpoint. Pada media read-only SQLite tidak dapat membuat `-shm`; bila tidak ada `-wal` yang berisi data, open ulang memakai `immutable=1` (snapshot lengkap). Fallback fail-closed: error selain SQLITE_READONLY/SQLITE_CANTOPEN, direktori yang masih bisa ditulis, holder yang hidup/terbaca-tidak-valid/host tak dikenal, atau status `-wal` yang tidak diketahui semuanya menolak fallback; penolakan menyebut alasan dan menyarankan menyalin data dir ke lokasi yang bisa ditulis. Fixture read-only Windows memakai deny ACE (chmod direktori tidak membatasi tulis di Windows). DSN dibangun dengan meng-escape `%`, `#`, dan `?` pada path sehingga `mode=ro`/`query_only` tidak pernah terbuang (SEC9-4). `Count` (satu-satunya permukaan SQL bebas) menolak selain SELECT/WITH pada store RO, sehingga `ATTACH` dan `VACUUM INTO` tidak dapat membuat file (SEC9-3).
4. **Penulis tetap menolak.** `Open` normal pada DB lebih baru tetap `ErrSchemaNewer`; `runHeadless` memetakannya ke **exit 1** dengan pesan `schema_newer` (SPEC §4.3 baris `schema_newer`; §7.2 kontrak `run` 0/2 dan §11.11.7 "read-only + notice" tidak menyebut 78). GAP-REL-05 di SPEC §22.1 dan baris MITIGATION-MATRIX sudah diselaraskan ke exit 1; migrasi yang gagal tetap exit 78. Penulis tetap berhenti tanpa retry.
5. **Cakupan CLI.** Karena `sessions ls/show/export` belum ada di main, PR ini menyediakan seam storage `Open(ReadOnly)` + test end-to-end dengan DB nyata; perintah inspeksi akan memakai opsi ini saat wiring stack mendarat. `doctor` tetap F3.
6. **LOC.** `ctxdb` 3.500 → 3.700 (terukur 3.533; Σ core +200), Total core 37.730 → 37.930 ≤ 47.000; amandemen SEC9-7 (tokenizer gate SQL) menaikkan `ctxdb` 3.700 → 3.800 (terukur 3.725; Σ core +100), Total core 37.930 → 38.030.

**Dampak terukur (SPEC §20).** Dependency baru 0. Test: `TestSQLiteEngineVersion` (versi + 4 probe + perbandingan semver), `TestVersionVerbose` (surface), `TestReadOnlyNewerSchema` (writer `ErrSchemaNewer`, read-only membuka/membaca, `Write` `ErrReadOnly`), `TestReadOnlyMissingDB`, `TestReadOnlyMissingDirCreatesNothing`, `TestReadOnlyOnReadOnlyMedia`, `TestReadOnlyDSNPaths`, `TestReadOnlyRefusesAttachVacuum`, `TestRunSchemaNewerExits1` (CLI exit 1 + pesan schema).

**Konsekuensi.** (+) Engine lama/kehilangan FTS5 menggagalkan CI sebelum rilis; inspeksi DB yang dimigrasi binary lebih baru tidak perlu downgrade/restore; penulis gagal dengan exit yang benar. (−) `doctor`/`version --verbose` di GAP REL-04 awalnya menunjuk F2 tetapi `doctor` adalah F3 — surface yang dipakai sekarang adalah `version --verbose`; `sessions` read-only menunggu perintahnya ada. SQLite tetap dapat membuat sidecar `-shm/-wal` saat membaca DB WAL dengan `mode=ro`; itu bukan lock/holder/migrasi.

## ADR-133 — Skill `side_effects`/`requires` dan lint skill: GAP-SEC-15 (gerbang skill pembayaran) dan GAP-SEC-26 (referensi + `credential_access`)

**Status:** Accepted 2026-10-21 (keputusan didelegasikan; opsi paling konservatif untuk setiap detail yang tidak ditetapkan SPEC). Menerapkan SPEC §22.1 GAP-SEC-15 (pemicu (a) skill) dan GAP-SEC-26; melengkapi SPEC §4.8, §14.1, §14.4, ADR-079, ADR-088, ADR-120. Tanpa DDL, tanpa dependency, tanpa perubahan `loc.budget` (`memory+skills` 1.079/1.200, `exec/classify` 1.755/1.800, `tools` 2.153/2.200).

**Konteks.** ADR-129 mencatat SEC-15 "sebagian" (kelas `Monetary` dan aturan argv sudah ada; deklarasi `side_effects: [payment]`, gerbang skill, dan default testnet belum) dan SEC-26 "belum mulai" (`internal/skills` hanya mem-parse `name`/`description`). SPEC tidak menetapkan: himpunan nilai `side_effects`, bentuk `requires`, apa yang dihitung "path script", kapan skill dianggap "dipakai", dan pola `credential_access` yang persis. Skill tidak dieksekusi oleh sistem — model membaca SKILL.md lalu memanggil `shell` — sehingga satu-satunya titik paksa yang nyata untuk aksi skill adalah tool `shell`.

**Keputusan.**
1. **Front matter.** `side_effects` dan `requires` hanya bentuk inline (`[a, b]` atau `a, b`); nilai kosong (awal block list YAML) dan kurung tidak seimbang → `invalid_front_matter`, agar deklarasi `payment` tidak pernah hilang diam-diam oleh parser baris. `side_effects` himpunan tertutup `write`, `network`, `payment` (nilai lain → `invalid_skill`); kunci absen = tidak dideklarasikan, `[]` = dideklarasikan tanpa efek. `requires` = daftar nama skill (bentuk `validName`). `requires` berbentuk objek untuk biner/OS/GPU (GAP-TOOL-10, F3/F5/F8) tidak diterima oleh parser ini.
2. **Gerbang skill pembayaran = jalur approval Monetary shell.** `skills.PaymentDirs` mengembalikan direktori skill yang mendeklarasikan `payment`; `tools.ShellConfig.MonetaryPaths` meneruskannya ke `classify.Env.MonetaryPaths`. Classifier menaikkan kelas ke `Monetary` bila operand path apa pun (termasuk script interpreter, `sh -c`, operand baca) atau argv0 berbentuk path — leksikal maupun hasil resolve symlink — berada di bawah direktori itu. `Monetary` adalah P26: `classify.Decide` tidak pernah auto (juga bila cocok `shell_allow`), preview di `ask`/`auto-edit`/`auto`, headless dan `--unsafe-auto-shell` → `denied`; `tools` tidak pernah menawarkan maupun menghormati `allow_session` untuk shell (ADR-088). `ShellConfig.MonetaryHosts` menambahkan seam yang sama untuk `security.monetary_hosts` (pemicu (c)). `smart_approval` tetap tanpa efek (ADR-079 butir 9).
3. **Default testnet.** Skill `builtin` yang teks atau script-nya memuat endpoint mainnet (URL ber-host/path `mainnet`, atau `--url|--rpc-url|--network|--cluster|--chain mainnet…`, atau `-um`) → `invalid_skill`; sumber lain → temuan `mainnet_default` (medium, review). Endpoint mainnet tanpa kata `mainnet` tidak terdeteksi (sisa risiko).
4. **Lint (`skills.Lint`)** dijalankan oleh `Service.Install` untuk setiap sumber (bawaan saat dimuat, hub, import, agent, user) sebelum scan. Error (`invalid_skill`, tidak ada yang disimpan): nama `requires` atau link `skill://<name>` yang bukan skill itu sendiri dan tidak `Known`; referensi script yang keluar dari direktori skill, tidak ada, bukan file reguler, atau > 256 KiB. **Referensi script** = token sesudah placeholder `$SKILL_DIR/`, `${SKILL_DIR}/`, `{baseDir}/`; token berawalan `scripts/` atau `./scripts/`; atau token ber-ekstensi script (`.sh .bash .zsh .py .js .mjs .cjs .ts .rb .pl .ps1 .bat .cmd`) yang absolut, `~`, `..`, atau ber-drive letter. Path relatif lain dianggap file workspace dan tidak diperiksa. Direktori skill dibuka composition root sebagai `os.Root` FS sehingga symlink keluar direktori gagal; `Known`/`Files` nil = gagal tertutup.
5. **Kategori `credential_access` (high).** Per baris (lower-case) atas deskripsi, body, dan setiap script yang dirujuk: perintah kredensial (`gh auth token`, `--show-token`, `git credential fill`, `git config --get credential`, `security find-generic-password`/`find-internet-password`/`dump-keychain`, `secret-tool lookup`, `keyring get`, `cmdkey /list`, `get-storedcredential`, `kwallet-query`, `gcloud auth print-access-token`, `az account get-access-token`, loader dotenv); file kredensial (`.env`/`.env.*` — bukan `process.env`/`.envrc` — `.git-credentials`, `.netrc`, `.npmrc`, `.pypirc`, `.aws/credentials`, `gh/hosts.yml`, `.docker/config.json`, `.ssh/id_*`, `.kube/config`, `secrets.enc`) bersama kata kerja baca pada baris yang sama; mencetak secret (verb cetak + referensi variabel bernama `key|token|secret|password|passwd|credential(s)`, dump env `env`/`printenv`/`set`/`export -p`/`Get-ChildItem env:`, atau instruksi "print/echo/show/display/output/reveal/paste [the|your …] api key|access token|…|password|credentials"). Tidak ada pengecualian negasi ("never read .env" juga terdeteksi): fail-closed dipilih atas false negative. High pada sumber tidak tepercaya → `rejected` (auto-reject §14.4); pada `user`/`builtin` → `invalid_skill` (tidak pernah diindeks).
6. **Pembayaran tak dideklarasikan.** Pola `stripe`, `payment_intent`, `spend-request`, `link-cli`, `solana transfer`, `cast send`, `gh sponsor`, `payout`, `sendtransaction`/`send_transaction` tanpa `side_effects: [payment]` → `payment_undeclared` (medium, SPEC §22.1 SEC-15); deklarasi `payment` → temuan `payment` (medium) agar disorot saat review.
7. **scan_result.** Temuan lint digabung ke JSON scanner (`{"severity","findings"}` milik `memory.Scan`) dengan severity tertinggi; `skills.Finding` identik bentuknya sehingga `internal/skills` tetap tidak mengimpor `internal/memory` (E-01). Detail temuan tidak pernah memuat nilai secret.
8. **Belum disambungkan.** `cmd/damnbot` belum memuat skill dari disk (pola ADR-120/122): pengisian `skills.Config.Known`/`Files` (os.Root per direktori skill), `ShellConfig.MonetaryPaths` dari `PaymentDirs` skill approved, dan `MonetaryHosts` dari config (`security.monetary_hosts` belum ada di `internal/config`) adalah irisan wiring skill. Belum ada skill bawaan di repo; begitu ada, setiap skill bawaan melewati `Install` (lint) saat dimuat, dan korpus `internal/skills/testdata/lint` adalah gate CI-nya saat ini.

**Test.** `TestLintCorpus` (4 skill baik × 3 sumber tanpa temuan selain `payment`; 17 skill buruk: path hilang/keluar/home/placeholder, `requires`/`skill://` menggantung, mainnet builtin vs hub, 8 varian `credential_access`, pembayaran tak dideklarasikan), `TestLintFailsClosed` (resolver nil, script > cap, direktori, symlink keluar `os.Root` tidak dibaca), `TestParseSideEffects`, `TestInstallLintInvalid`, `TestInstallLintFindings`; `classify.TestPaymentSkillMonetary` (eksekusi langsung, interpreter, `sh -c`, `cd`, symlink, prefix saudara bukan skill; matriks `PPPDD` dengan `shell_allow` cocok); `tools.TestShellPaymentSkillGate`, `TestShellPaymentSkillHeadless` (kontrol: tanpa deklarasi cocok `shell_allow` jalan headless), dan `TestMonetaryAlwaysPrompts` (15 perintah korpus, ketiga pemicu: mode `auto` 15/15 prompt tanpa `allow_session`, headless 15/15 `denied` tanpa eksekusi).

**Dampak terukur (SPEC §20).** Token 0 (tidak ada teks prompt/schema baru). DDL/dependency/binary 0. LOC di atas, dalam budget.

**Konsekuensi.** (+) Script skill pembayaran tidak pernah berjalan tanpa approval manual, di semua mode. (+) Skill dengan referensi menggantung atau akses kredensial tidak pernah diindeks. (−) Label classifier tetap best-effort (ADR-079 butir 8): pembayaran lewat perintah yang tidak menyebut file skill, host tak terdaftar, atau endpoint tanpa pola tidak terdeteksi — tetap ditanya karena shell tidak pernah auto di luar `shell_allow`, tetapi preview tidak berlabel `monetary`. (−) Lint berbasis pola; false positive (mis. kalimat negasi tentang `.env`) menolak skill, false negative mungkin untuk obfuscation.

**Amandemen review F2-SEC10 (PR #207, bagian gate).** Diterapkan 2026-10-21 atas `docs/REVIEW-F2-SEC10.md`. Butir 10 memperluas cakupan `PaymentDirs`; butir 8 (wiring) tetap terbuka. Lint mendalam (SEC10-2/4/5) dan wiring skill (SEC10-6) menyusul di PR bertumpuk `f2-skills-sec10b`.
9. **`cd` dan argv0 relatif (SEC10-1).** `cd`/`pushd`/`chdir`/`set-location` dengan target literal yang ter-resolve ke direktori skill pembayaran menaikkan kelas `Monetary`; cwd tetap tidak dilacak sesudah `cd` (ADR-079 butir 2). argv0 berbentuk path yang tidak dapat di-resolve statis (mis. `./charge.sh` sesudah `cd $VAR`, `cd /tmp`) menjadi `Unknown`, sehingga tidak pernah auto di `auto`, `--unsafe-auto-shell`, atau `shell_allow`. `popd` tetap `Unknown`. Test: `TestPaymentSkillMonetary` (`cd P/scripts; ./charge.sh`, `cd P && ./scripts/charge.sh`, `cd /tmp && ./charge.sh` → `Unknown`) dan `tools.TestShellUnsafeAutoShellPayment` (dispatcher nyata, `--approval auto --unsafe-auto-shell`: 0 prompt, 0 eksekusi).
10. **Gate skill (SEC10-3).** `PaymentDirs` memakai `Skill.Sensitive()` = mendeklarasikan `payment` **atau** teks deskripsi/body memuat pola pembayaran (termasuk yang belum dideklarasikan), tanpa memfilter status — skill `scanned`/quarantine ikut di-gate karena filenya sudah ada di disk; direktori di-dedupe.
11. **Host header dan GET (SEC10-7).** HTTP ke host `security.monetary_hosts` berkelas `Monetary` untuk **semua** metode (termasuk GET/HEAD), dan header `Host: <monetary-host>` pada klien HTTP mana pun menaikkan kelas yang sama. Ini lebih ketat dari SPEC §22.1 (yang menyebut HTTP mutating) dan dicatat sebagai label heuristik.

**Amandemen lanjutan review F2-SEC10 (PR bertumpuk, SEC10-2/4/5/6).** Diterapkan 2026-10-21; melengkapi butir 9–11.
12. **Scan seluruh direktori (SEC10-2).** `Lint` memindai **setiap file reguler** di direktori skill (kecuali `SKILL.md`), bukan hanya yang dirujuk body; batas `MaxScanFiles` 64 dan `MaxScanBytes` 1 MiB, melewati batas = `invalid_skill` (tidak ada scan parsial). `Files` nil = `invalid_skill` bahkan tanpa referensi. Referensi script tetap diperiksa terpisah (ada, di dalam direktori, ≤ `MaxScriptBytes`). Symlink tidak diikuti (`os.Root`).
13. **Normalisasi dan obfuscation (SEC10-4/5).** Teks yang dicocokkan dinormalisasi NFKC + lower-case + fold confusable Cyrillic/Greek (mis. `gh аuth token`). `base64 -d | sh`, `eval "$(…)` dan `exec(base64…)` menjadi temuan `obfuscated_exec` (medium, tidak auto-reject). Bentuk ber-kutip `"$SKILL_DIR"/scripts/x` (dan `${…}`, kutip tunggal) dibuka sebelum tokenisasi agar tidak salah dibaca sebagai path absolut.
14. **Wiring gate (SEC10-6).** `cmd/damnbot` memuat `<config>/skills/*/SKILL.md` saat `run` mulai, mem-parse, dan menjalankan `Lint` lewat `os.Root` (temuan high → error), lalu meneruskan `PaymentDirs` ke `ShellConfig.MonetaryPaths`. Skill yang rusak/gagal di-lint menggagalkan `run` (fail closed), bukan meninggalkan file tanpa gate. `security.monetary_hosts` masih belum punya kunci config; `ShellConfig.MonetaryHosts` adalah seam-nya (residu).
**Amandemen lanjutan review F2-SEC10 (ronde 2–5: SEC10-8..12, SEC13).** Diterapkan 2026-10-21; melengkapi butir 12–14.
15. **Direktori ber-titik (SEC10-8).** `scanDir` tidak lagi melewati direktori yang namanya diawali `.`; setiap file reguler di bawah skill dipindai (batas 64 file/1 MiB tetap). Kode di `.lib/`, `lib/.x/` atau `.hidden/` karena itu memicu `credential_access`/`payment_undeclared` dan dapat menolak skill.
16. **Sensitivitas dari file (SEC10-9).** `Skill.AddFindings` menandai skill sensitif bila lint menghasilkan `payment` atau `payment_undeclared` dari file mana pun; `PaymentDirs` memakai flag itu, sehingga kode pembayaran yang hanya ada di script (`tools/pay.py`) tetap menggerbang direktori skill-nya. `paymentSkillDirs` memanggil `AddFindings` setelah lint.
17. **Symlink dan direktori tanpa SKILL.md (SEC10-10).** `loadSkills` memakai `os.Stat` (mengikuti symlink): symlink ke direktori skill di-parse, di-lint lewat `os.Root` pada target, dan **kedua** path (link + target hasil `filepath.EvalSymlinks`) masuk `MonetaryPaths`; symlink rusak = error (gagal-tertutup). Direktori di bawah `<config>/skills` tanpa `SKILL.md` tidak dianggap skill, tetapi direktori itu tetap dimasukkan ke gate (semua file-nya diperlakukan sebagai file skill).
18. **Normalisasi ronde 2 (sisa SEC10-4).** Sebelum pencocokan: NFKC + lower-case, hapus pasangan kutip kosong (`tok""en`), substitusi assignment literal sederhana (`a=gh; $a auth token`), hapus karakter kategori `Cf` (zero-width), dan fold confusable termasuk Cyrillic `г→r`. Pola `obfuscated_exec` ditambah `b64decode`/`base64.b64decode`, `zcat`/`gunzip`/`rev` yang dipipe ke shell. Zero-width di body SKILL.md sudah ditolak `Parse` (`invalid_skill`, karakter bidi) sebelum lint; normalisasi tetap berlaku untuk skill yang dibangun lewat API. Ini tetap heuristik: konkatenasi non-literal dan decoder lain tidak terdeteksi.
19. **Normalisasi linear (SEC10-11).** `expandLiterals` mengumpulkan maksimum 32 assignment literal dalam satu pencarian, lalu mengganti referensi `$x`/`${x}` dengan satu `ReplaceAllStringFunc`; tidak ada penggantian seluruh teks per assignment. `credential` membangun daftar perintah/file sekali per panggilan, bukan per baris. `TestLintLargeFile` (1 MiB, banyak assignment) mensyaratkan < 1 s (terukur ≈ 0,35 s) dan `BenchmarkLintLargeFile` disediakan.
20. **Symlink (SEC10-12).** `scanDir` menolak symlink apa pun di direktori skill (`invalid_skill`, tidak diikuti), termasuk symlink ke target di dalam; `SKILL.md` dibaca lewat `os.Root` sehingga symlink yang keluar direktori tidak pernah dibaca. Classifier tetap menandai path link di dalam direktori skill pembayaran sebagai `Monetary` (uji `cat P/scripts/link.py` dengan target di luar).
21. **LOC.** `memory+skills` 1.230 (terukur 1.215; `lint` linear + scan symlink). `loc.budget` `memory+skills` 1.200 → 1.230 dan ENGINEERING §2.4 (Total core 38.030 → 38.060, sesudah #211 dan #203) diperbarui di PR ini. #211 (`loop`/`compactstore`) sudah ada di main sebelum PR ini, sehingga Σ di atas sudah memuatnya.
22. **Kunci path lintas platform (SEC13).** Perbandingan gate pembayaran memakai kunci kanonik, bukan string path mentah: `classify.Env.MonetaryKey` (disuntik `ShellConfig.Canonicalize`) diterapkan ke direktori gate dan ke setiap path perintah. `cmd/damnbot.monetaryKey` = absolut + `filepath.EvalSymlinks` + slash; di Windows ditambah `GetLongPathName` (8.3 → long) dan lower-case; di Darwin lower-case (APFS default case-insensitive); Linux/other case-sensitive. Direktori gate dikembalikan apa adanya (link dan target collapse ke kunci yang sama), sehingga `/var` vs `/private/var` dan `RUNNER~1` vs nama panjang tidak lagi melewati gate. `MonetaryKey` nil = perbandingan leksikal lama (hanya untuk unit test/pemanggil non-produksi); `cmd/damnbot` selalu mengisinya. Test: `TestPaymentKeySimulatedForms` (short-name dan case-fold disimulasikan), `TestMonetaryKeySymlink` (link = target), `TestPaymentGateResolvedTarget` (perintah memakai target hasil resolve), dan job Windows/macOS yang menjalankan `TestPaymentSkillDirs`/`TestPaymentSkillGateWired`.
23. **Kanonikalisasi ronde 5 (SEC10-14/16/17/18, sisa SEC10-15).** `cmd/damnbot` sekarang me-resolve awalan path terpanjang yang ada (`existingPrefix`), lalu: Windows membuka awalan itu dengan `FILE_FLAG_BACKUP_SEMANTICS` dan memakai `GetFinalPathNameByHandle` (junction/subst/8.3), membuang prefiks `\\?\` dan `\\?\UNC\`, menambahkan sisa path, dan lower-case; Darwin menormalkan NFC sebelum lower-case (APFS); OS lain hanya EvalSymlinks. `NEWSHELL`: `tools.NewShell` mengembalikan error bila `MonetaryPaths` diisi tanpa `Canonicalize` (SEC10-17), sehingga fallback leksikal tidak dapat dipakai di produksi. SEC10-15: prefiks `\\?\`/`\\.\`/`\\?\UNC\` pada operand shell dideteksi lewat jalur mentah di `classify.operand` sebelum path dibersihkan, dan `cleanDevicePrefix` menanganinya; bentuk `\\localhost\C$` yang menunjuk drive lokal tetap residual karena bukan spelling yang sama dengan `C:\` (masih selalu ditanya/ditolak headless, hanya kehilangan label `monetary`). Test: `TestMonetaryKeyJunction` (Windows, `mklink /J`), `TestMonetaryKeyNFC` (Darwin), `TestMonetaryKeyMissingTail`, `TestCleanDevicePrefix`, `TestPaymentDevicePath`, `TestNewShellNeedsCanonicalize`.

## ADR-134 — GAP-REL-01: `context_exclude` append-only untuk turn gagal, panic, dan crash

**Status:** Accepted 2026-10-21 (keputusan didelegasikan; opsi paling konservatif untuk detail yang tidak ditetapkan SPEC). Menerapkan SPEC §22.1 GAP-REL-01, §3.3 (recovery), §3.4, §4.5, §11.2; melengkapi ADR-070 (taint), ADR-079, ADR-120/122 (pola seam), P9. Tanpa DDL (kind `context_exclude` sudah ada di CHECK `0001_core`), tanpa dependency, tanpa perubahan `loc.budget` (`loop` 1.789/1.800, `ctxdb` 3.427/3.500, `ctxdb/compactstore` 249/250).

**Konteks.** ADR-129 mencatat REL-01 "sebagian": `user_message` dan `tool_result` sudah di-commit sebelum/terlepas dari error (#126/#127), dan `ctxdb.History` sudah memfilter turn yang ditandai `context_exclude` (default) dengan opsi `WithHistoryExcluded`. Yang belum ada: **penulis** marker itu. Akibatnya turn yang gagal (400 non-retryable, overflow, cancel, panic) tetap dikirim ulang pada turn berikutnya alih-alih dihindari, dan panic tidak meninggalkan `turn_end` sama sekali. SPEC tidak menetapkan: outcome mana yang di-exclude, urutan penulisan marker vs `turn_end`, dan apakah compaction boleh membaca rentang ter-exclude.

**Keputusan.**
1. **Penulis di loop.** `turn.finish` menulis tepat satu `context_exclude{turn_id, reason}` (bila outcome termasuk kelas gagal) lalu `turn_end`, keduanya lewat jalur audit yang mengabaikan pembatalan (`context.WithoutCancel`), sekali per turn. Marker ditulis **sebelum** `turn_end`: marker adalah pengaman poison-loop, jadi kegagalan append audit tidak boleh menghilangkannya; `turn_end` tetap selalu dicoba dan tetap menjadi satu-satunya stop yang dilaporkan ke frontend. Tidak ada UPDATE/DELETE baris (P9).
2. **Panic boundary.** `turn.run` memasang `defer` yang, bila panic sebelum `turn_end`, menulis `turn_end{stop_reason:"error",detail:"internal"}` + `context_exclude{reason:"panic"}` lalu me-`panic` ulang. Panic tetap ditangkap `internal/boundary` (E-26) dan stream tetap ditutup; terminal state tidak bergantung pada ada/tidaknya pembaca yang menguras channel, karena jalur panic tidak mengirim update stream. nilainya tidak dimasukkan ke event (bisa memuat secret); `Error` = `"panic"`.
3. **Himpunan outcome.** Exclude: `provider_error` (retry+fallback habis atau non-retryable), `internal` (termasuk panic dan gagal compaction/assemble), `context_overflow`, `cancelled`, dan `max_wall`. Tidak di-exclude: `completed`, `empty_response`, `no_progress`, `invalid_answer`, `output_truncated`, `budget_*`, `max_iterations`, `content_filtered`, `tool_error`. Alasannya: yang di-exclude adalah turn yang tidak menghasilkan history yang aman dikirim ulang (request-level failure, abort, overflow); stop kebijakan tetap menyisakan history yang valid dan menghilangkannya akan mengubah semantik lanjutan/`max_iterations`/budget tanpa dasar SPEC. Daftar ini satu fungsi (`excludeReason`) dan diuji per kelas.
4. **Crash recovery.** `RecoverJournals` → `closeCrashed` menulis `turn_end{crashed}`, lalu `context_exclude{turn_id,reason:"crashed"}`, lalu `crash_recovered` dalam transaksi yang sama. Turn yang terputus `kill -9` karenanya tidak pernah masuk request berikutnya, tetapi barisnya tetap ada dan dapat di-recall (`TestKill9MidTurnExcludes`: child process nyata di-SIGKILL).
5. **Konsumsi.** `ctxdb.History` tetap satu-satunya jalur history untuk assembler (`ctxStore.History` di composition root) dan sudah melewati turn ter-exclude secara default; `WithHistoryExcluded` adalah jalur audit/TUI yang menampilkan transkrip penuh. `context_exclude` sendiri tetap tersimpan sebagai baris audit. `Search`/`recall` **tidak** memfilter marker: turn gagal tetap dapat di-recall (SPEC §22.1).
6. **Compaction.** `compactstore.Load` menambahkan predikat yang sama pada query event model-visible dan pada query "user message terakhir" (`taskQ`), sehingga summary tidak pernah menyerap rentang ter-exclude dan restatement task tidak memakai user message ter-exclude. Predikat mendukung `turn_id` dan span `from_seq`/`to_seq` (bentuk span sudah didukung `History`), meskipun penulis saat ini hanya mengirim `turn_id` sesuai SPEC.
7. **Pesan user.** Turn yang di-exclude mencakup `user_message`-nya: itulah inti pencegahan 400 berulang. Pesan tetap tersimpan (P9), terlihat di transkrip dengan `WithHistoryExcluded`/UI, dapat di-recall, dan user dapat mencoba lagi dengan mengirim ulang; yang tidak terjadi hanyalah pengiriman otomatis rentang itu ke provider.
8. **Belum disambungkan.** Retrieval otomatis FTS5/`recall` per turn (#167) belum ada di `origin/main`; saat menambahkannya, hasil retrieval **wajib** memfilter `context_exclude` (kalau tidak, turn gagal bisa masuk kembali lewat retrieval). Frontend yang menampilkan transkrip harus memakai `WithHistoryExcluded` untuk melihat turn gagal. Keduanya dicatat sebagai residu wiring, bukan perilaku yang diasumsikan sudah ada.

**Test.** `loop.TestFailedTurnExcludes` (provider_error, context_overflow, cancelled, panic: tepat 1 `turn_end`, marker dengan turn+reason, `user_message` tidak hilang, urutan marker→turn_end), `TestCompletedTurnNotExcluded` (completed/empty_response/content_filtered); `ctxdb.TestCrashExcludesDanglingTurn` (recovery nyata via lease mati: nilai `turn_end{crashed}`, marker, 2 baris turn utuh, `History` default menyembunyikan, `WithHistoryExcluded` menampilkan, `Search` tetap menemukan) dan `TestKill9MidTurnExcludes` (SIGKILL proses anak nyata); `compactstore.TestLoadSkipsExcluded` (turn ditandai `turn_id` dan span tidak masuk view, baris tetap ada); `cmd/damnbot.TestFailedTurnPersisted` (DB nyata: 400 non-retryable setelah `tool_result` → baris user/tool tetap 1/1, `turn_end{provider_error}` + marker, turn berikut pada sesi yang sama tidak mengirim teks poison maupun `tool_call_id`-nya, baris tetap 2/1, `Search` menemukan).

**Dampak terukur (SPEC §20).** Token 0. DDL/dependency/binary 0. LOC: `loop` 1.881/1.900 di base PR, `ctxdb` 3.455/3.500, `ctxdb/compactstore` 253/260. Amandemen review menaikkan `loc.budget` `loop` +100 (1.800 → 1.900 di base PR) dan `ctxdb/compactstore` 250 → 260. Sesudah rebase di atas carrier (ADR-131, `loop` 1.950) delta yang sama menjadi `loop` 1.950 → 2.050 (terukur `make loc` 2.037), Σ Total core 37.620 → 37.730; ENGINEERING §2.4 diselaraskan.

**Konsekuensi.** (+) Turn gagal tidak pernah hilang dan tidak pernah diulang otomatis; panic dan crash meninggalkan `turn_end` + marker yang dapat diaudit. (+) Summary tidak dapat "menghidupkan kembali" rentang ter-exclude. (−) Turn `cancelled` juga hilang dari konteks berikutnya (termasuk user message)-nya; user harus mengirim ulang, sesuai SPEC yang meminta rentang turn di-exclude. (−) Marker adalah satu append tambahan per turn gagal; budget `loop` tersisa 11 LOC untuk F2 lanjutan.

**Amandemen review F2-SEC11 (SEC11-1..6).** Diterapkan 2026-10-21; melengkapi butir 1–8.
9. **Summary dari turn gagal (SEC11-1).** Exclusion diterapkan sebelum summary: `History` mengumpulkan turn ter-exclude, membuang summary yang rentang `[from_seq,to_seq]`-nya memuat event turn itu, lalu `applySummaries`. `compactstore.summariesQ` memakai predikat yang sama. Summary yang dibuat compaction di dalam turn yang kemudian gagal karena itu tidak pernah masuk konteks lagi.
10. **Journal produksi (SEC11-2).** Seam opsional `loop.TurnJournal` (`BeginTurn`, `EndTurn(ctx, sid, turn, payload)`). `ctxStore` mengimplementasikannya dengan `Tx.StartTurn`/`Tx.EndTurnWith` dalam satu transaksi; loop tidak lagi menulis `turn_start`/`turn_end` terpisah bila seam ada. `Tx.EndTurnWith` menyimpan payload `turn_end` lengkap (stop, detail, usage, error) sambil menghapus baris journal. `run` karenanya meninggalkan journal yang dapat dipulihkan `kill -9`.
11. **Efek samping tetap terlihat (SEC11-3).** Tool non-read yang sukses dicatat di turn (`Prepared.SideEffect` diisi adapter dari registry; `trackEffect`). Saat turn di-exclude, loop menulis `system_notice{kind:"failed_turn_effects", turn_id, effects[]}` **tanpa turn_id** (turnless, tidak ikut ter-exclude); `ctxStore.History` merendernya sebagai pesan `system` yang menyebut aksi yang sudah dieksekusi dan meminta tidak mengulanginya. Payload hasil tool tetap tidak dimasukkan ulang (menghindari urutan tool tanpa pasangan dan konten beracun).
12. **Span dihapus (SEC11-4).** `context_exclude` hanya meng-exclude berdasarkan `turn_id`; `from_seq`/`to_seq` diabaikan oleh `History` dan compaction. Tidak ada penulis span; marker `from_seq` saja tidak lagi membisukan turn berikutnya.
13. **Recall menandai hit (SEC11-5).** `ctxdb.Hit.Excluded` diisi dari marker `turn_id` (event) atau irisan rentang summary; recall manual tetap mengembalikan baris ter-exclude dengan penanda, bukan menyembunyikannya.
14. **Kegagalan marker (SEC11-6).** Append marker memakai jalur yang mengembalikan error; kegagalan disimpan di `markerErr` dan dipancarkan sebagai notice `context_exclude append failed: …` pada stream. Panic path tidak punya pembaca, jadi kegagalan di sana hanya terlihat lewat `turn_end`/recovery (residu).
15. **ID tool tidak tepercaya (SEC11-7).** `failed_turn_effects` hanya memuat nama tool dan call id yang lolos `[A-Za-z0-9_-]{1,64}`; id lain diganti rujukan indeks (`tool#N`), sehingga newline/instruksi dari provider tidak masuk pesan `system`.
16. **Timeout/outcome tidak diketahui (SEC11-8).** Tool non-read yang gagal, `still_running`, `outcome_unknown`, atau `timeout` dicatat di daftar `uncertain` dan dirender sebagai "may have run with unknown outcome"; tool sukses tetap di daftar `effects`.

## ADR-135 — Antrean CI: PR docs-only, matrix bertingkat PR vs `main`/nightly, agregat `ci-required`, dan concurrency khusus PR

**Status:** Accepted 2026-10-21 (keputusan owner: empat perubahan disetujui). Memperpendek antrean CI tanpa mengurangi apa pun yang melindungi `main`. Melengkapi ADR-073 (job wajib), ADR-048 (`test-timing`), ADR-085 (meta-test), dan ADR-094 (hemat menit runner); tidak mengubah budget absolut benchmark (ADR-073) maupun gate waktu (ADR-048/127).

**Aturan penerapan.** Diterapkan (2026-10-21), branch `ci-speedup`: `.github/workflows/ci.yml` (job `changes`, kondisi per job, matrix `pr`, job agregat `ci-required`, concurrency), `.github/workflows/nightly.yml` (job `ci-full` memanggil workflow `ci`), `.github/workflows/meta.yml` (concurrency khusus PR); teks ENGINEERING §5.3 (urutan job PR, E-64) dan §7.2 (E-78); `CHANGELOG.md`. Branch protection `required_status_checks` pindah ke `ci-required` + `meta-test` + `pr-meta` — diterapkan owner **setelah** workflow baru hijau di PR-nya sendiri (butir 5). Setiap passage mengutip ADR-135.

**Konteks.** Repo privat di GitHub Free: ± 20 job konkuren, menit macOS ×10 dan Windows ×2. Setiap push PR menjalankan 23 job workflow `ci` (6 di antaranya `test` lintas OS) ditambah `meta-test` dan `pr-meta` = 25 job. Yang membengkakkan waktu tunggu bukan durasi kerja melainkan antrean: pada run `ci` push `main` 39660658150 (21 Oktober 2026) total run 47 m 50 s, tetapi job `test (ubuntu-latest, -race, 1)` baru mulai 42 m 19 s setelah run dimulai dan selesai dalam 5 m 30 s; job `test (macos-15-intel)` selesai dalam 9 m 25 s setelah mengantre 17 m 34 s. Sekaligus, `concurrency` memakai `cancel-in-progress: true` untuk semua event, sehingga push `main` berikutnya membatalkan run `main` yang sedang berjalan (run 39597219322 berstatus `cancelled`) — justru run yang menjadi gerbang utama repo. Branch protection saat itu mewajibkan 24 konteks individual (`lint`, keenam nama `test (…)`, tiga `fuzz (…)`, `coverage`, `perf`, `size`, `loc-budget`, `supply-chain`, `vuln`, `vet-cross`, `build-matrix`, `deadcode`, `bench-budget`, `test-timing`, `phase-acceptance`, `meta-test`, `pr-meta`), sehingga setiap job yang dilewati atau tidak ada di tier mana pun akan menggantung sebagai "Expected — waiting".

**Keputusan.**
1. **Concurrency khusus PR.** `ci` dan `meta` memakai `cancel-in-progress: ${{ github.event_name == 'pull_request' }}`. Push baru ke PR yang sama membatalkan run lama (tidak ada gunanya menyelesaikan commit yang sudah digantikan), sedangkan push `main` dan nightly (`workflow_call`) tidak pernah dibatalkan: run berikutnya menunggu di grup yang sama hingga run sebelumnya selesai.
2. **PR docs-only.** Job `changes` menghitung `git diff --name-only <base> <head>`. Path yang semuanya `docs/**` atau Markdown di akar repo (`^[^/]+\.md$`, mis. `README.md`/`CHANGELOG.md`) melewati seluruh job Go: build, test, `test-timing`, vet, fuzz, coverage, deadcode, perf, bench, size, loc, supply-chain, vuln, phase-acceptance. Markdown di subdirektori mana pun (`internal/**/testdata/SKILL.md`, golden, template, `.github/*.md`) **bukan** docs-only: file itu dibaca kode/test Go dan tetap memicu semua job Go. Gate dokumen tetap berjalan: `pr-meta` (E-80..E-84, adrgate, spec-guard) dan `meta-test` bila path gate berubah. Pengecualian wajib: bila diff menyentuh dokumen yang divalidasi test Go — `SPEC.md`, `docs/ENGINEERING.md` (§2.4 dan budget), `docs/DECISIONS.md`, `docs/ROADMAP.md`, `docs/FEATURE-PARITY.md`, `docs/CODE-TOOLS.md`, dan `docs/ATTRIBUTION.md`, yang dibaca `internal/archtest` (`TestBudgetTableSum`, `TestParityPhase`, `TestCodetoolsIds`, `TestAttribution`), atau `loc.budget`/`deps.allow`/`deps.mpl` — semua job Go tetap berjalan, karena perubahan itu justru divalidasi oleh test tersebut. Base yang tidak diketahui = jalankan semua job (fail-closed).
3. **Matrix bertingkat.** `test` menyimpan enam target berrunner, tetapi hanya tiga yang berjalan di `pull_request`: `ubuntu-latest` (`-race`), `macos-latest` (arm64, `-race`), `windows-latest` (amd64, `-race`). `ubuntu-24.04-arm`, `macos-15-intel`, dan `windows-11-arm` hanya berjalan pada push `main` dan nightly. `nightly.yml` memanggil workflow `ci` lewat `workflow_call` sehingga matrix penuh — termasuk `coverage`, `perf`, `fuzz`, dan seluruh gate lain — berjalan terhadap `main` setiap hari, bukan hanya gate harian yang lama. `vet-cross` (windows/darwin × amd64/arm64) dan `build-matrix` (8 target) tetap di PR: keduanya murah (± 2–3 menit di ubuntu) dan menangkap sebagian besar kerusakan platform yang tidak terlihat di tiga OS tier PR.
4. **Cache dan sharding.** Cache modul + build sudah disediakan `actions/setup-go` (GOMODCACHE + GOCACHE, kunci memuat `go.sum` dan versi Go); ADR ini hanya menegaskan `cache-dependency-path` eksplisit di setiap job (termasuk `go.sum` untuk `bench-parity` yang sebelumnya hanya memakai `bench/go.sum`). Sharding paket lambat **ditolak** setelah pengukuran: pada job `test (ubuntu-latest)` run 39660658150, `internal/ctxdb` 262,9 s, `internal/codetools` 94,5 s, `internal/exec` 92,9 s dari 330 s job; memecah per paket tidak memecah `internal/ctxdb` (pemecahan per nama test mengubah flag `-run`), sehingga jalur kritis tetap ± 4,5 menit sementara jumlah job bertambah — memperburuk antrean yang justru menjadi 90 % waktu tunggu (butir "Dampak terukur").
5. **Satu status wajib: `ci-required`.** Job agregat `if: always()`, `needs` seluruh job `ci`, dan lulus bila setiap hasil `success` atau `skipped`, gagal bila ada `failure`/`cancelled`; ia juga mencatat hasil tiap gate di log. Branch protection wajib menjadi `ci-required`, `meta-test`, dan `pr-meta` — bukan lagi 24 konteks individual. Perubahan branch protection adalah langkah manusia (admin) dan baru dijalankan setelah workflow baru hijau pada PR yang sama; sampai saat itu konteks lama tidak dihapus agar tidak ada jendela tanpa gerbang.
6. **`main` merah = blocker.** Karena tier PR lebih kecil, kegagalan yang hanya muncul di target penuh (arm, intel macOS, windows/arm) pertama terlihat di push `main` atau nightly. Run `main` yang merah memblokir merge berikutnya sampai hijau (aturan merge yang sudah berlaku), dan run `main` tidak pernah dibatalkan (butir 1) sehingga sinyalnya selalu lengkap.

**Dampak terukur (SPEC §20).** Token/latensi produksi/binary/dependency 0; LOC Go 0 (hanya YAML + dokumen). Jumlah job per push PR: 25 → 24 untuk PR Go (23 job `ci` − 3 target `test` + 2 job baru `changes`/`ci-required`, ditambah `meta-test`/`pr-meta`) dan 25 → 4 untuk PR docs-only (2 job `ci` + `meta`/`pr-meta` yang tidak menjalankan test). Menit runner yang dihemat per PR Go: satu leg macOS ×10 (9 m 25 s kerja pada run di atas ⇒ ± 94 menit tertagih), satu leg windows/arm ×2, satu leg ubuntu/arm ×2, dikurangi ± 20 s `changes` dan ± 10 s `ci-required`. Antrean: pada run 39660658150, 6 leg `test` + 3 `fuzz` + 12 job gate lain memperebutkan ± 20 slot; menghapus 3 leg dari setiap PR mengurangi okupansi slot pada jam sibuk (job mulai 5–42 menit setelah run). Cache: log job `test (ubuntu-latest)` menunjukkan `Cache hit` 139.203.047 B (± 133 MB) dan restore selesai dalam ± 3 s; kunci `setup-go-Linux-x64-ubuntu24-go-1.26.10-397cfa8…` sudah memuat versi Go + hash `go.sum`, jadi tidak ada perubahan perilaku yang diklaim dari butir 4. Sharding: lihat butir 4 — ditolak dengan angka di atas. E-64 (≤ 12 menit p90) tetap berlaku untuk PR Go; PR docs-only jauh di bawahnya.

**Konsekuensi.** (+) Waktu tunggu PR turun karena job yang tidak relevan tidak lagi memesan runner, terutama macOS ×10; PR docs-only selesai dalam hitungan detik; push `main` tidak lagi dibatalkan oleh push berikutnya; job yang dilewati tidak lagi menggantung karena status wajib berupa agregat. (−) Kegagalan khusus platform arm/intel baru terdeteksi di `main`/nightly (tertutup oleh butir 6: merge berikutnya menunggu `main` hijau). (−) `ci-required` menyembunyikan nama gate individual di daftar status wajib; diagnosis tetap mudah karena log agregat mencetak hasil setiap `needs`. (−) Job `changes` menambah satu langkah serial ± 15–20 s sebelum job lain mulai. (−) Dokumen di luar daftar butir 2 (mis. `docs/REVIEW-*.md`) tidak lagi divalidasi test Go di PR docs-only; `pr-meta` (spec-guard) dan run `main`/nightly penuh tetap menjadi jaring pengaman.
24. **UNC dan cd cmd/PowerShell (SEC10-19/20).** Operand/argv0 berawalan `\\` (UNC, `\\?\UNC\`, device) tidak pernah menyentuh filesystem: `classify` menandainya `Unknown` sebelum `paymentFile`, dan `cmd/damnbot` mengembalikan kunci leksikal untuk path remote (UNC atau volume `DRIVE_REMOTE`, dicek lewat `GetDriveType` pada root volume tanpa membuka share). `cd`/`chdir`/`pushd`/`Set-Location`/`Push-Location` di cmd/PowerShell memeriksa setiap argumen literal non-flag terhadap gate (termasuk `cd /d X`, `-Path`, `-LiteralPath`), dan perintah apa pun setelah `cd` yang cwd-nya tidak terlacak menjadi `Unknown` (tidak pernah auto). `;` mengikuti lexer cmd/PowerShell yang ada. Test: `TestWindowsCdGate`, `TestPaymentDevicePath` (Unknown), `TestRemotePathNoFilesystem` (seam filesystem gagal bila dipanggil). Budget: `exec/classify` 1.800 → 1.815 (terukur 1.810; Total core 38.060 → 38.075) — satu-satunya kenaikan di ronde ini.
25. **cd remote dan narrowing cmd (SEC10-22/23).** `changeDir` menandai argumen `cd`/`chdir`/`pushd`/`Set-Location`/`Push-Location` yang berawalan `\\` atau `//` (termasuk `\\?\UNC\`) sebagai `Unknown` tanpa menyentuh share. Aturan "perintah setelah cd" dipersempit ke dialek `Cmd` saja: target cd yang tidak terlacak, argv0 relatif, atau bare command yang tidak ada di tabel non-mutating → `Unknown`; bare command allow-listed dengan target lokal terlacak dan PowerShell kembali ke klasifikasi normal, sehingga `cd sub && git status`, `cd sub && echo hi`, dan `Set-Location sub; Get-ChildItem` berperilaku seperti sebelumnya. Test: `TestWindowsCdGate` (positif dan negatif). Budget `exec/classify` 1.815 → 1.830 (terukur 1.823; Total core 38.075 → 38.090).
26. **cmd cwd lookup dan pengecualian pasca-cd (SEC10-25/26).** Setiap child shell Windows mendapat env keras `NoDefaultCurrentDirectoryInExePath=1` (dibangun di `exec.EnvPolicy.Env`, sehingga PowerShell yang melahirkan cmd juga mewarisinya); `git.bat` di cwd tidak lagi dijalankan untuk `git status` (test Windows `TestNoDefaultCurrentDirectoryInExePath` memakai marker). Aturan pasca-cd cmd mengecualikan `popd` serta `cd`/`pushd` tanpa argumen, dan prefiks `//` hanya dianggap remote pada dialek Windows (Cmd/Pwsh); `cd //tmp` di bash diklasifikasikan normal. Test: `TestWindowsCdGate`. Budget `exec/classify` tetap 1.826–1.829/1.830.

## ADR-137 — Recall memori: kandidat BM25 lintas term kueri (F2/GAP-INF-06)

**Status:** Accepted 2026-10-22 (F2; keputusan paling konservatif terhadap SPEC §4.8/B-03). Diterapkan di PR bertumpuk: (a) paket ini (`ctxdb/memstore` + `ctxdb.FTSNorm`), (b) seam injeksi jalur `run`, (c) `damnbot eval recall`. Tidak mengubah skema, migrasi, atau dependency.

**Konteks.** SPEC §4.8/B-03 meminta memori `approved` diurutkan `pinned` lalu skor BM25 terhadap pesan user pertama. Implementasi `memstore.searchIDs` merangkai setiap term kueri menjadi ekspresi FTS5 AND (`ftsQuery`), sehingga kueri pertanyaan hanya cocok bila fakta memuat **setiap** kata pertanyaan. Pada E4 (pertanyaan seperti "What is my locker number?" terhadap fakta "my locker number is 4417") hasilnya 0/30: kata tanya tidak pernah ada di fakta. `dmb_fts_norm(?)` dipakai pada MATCH, dan normalisasi itu menurunkan huruf, sehingga `OR` yang ditulis pemanggil akan menjadi term biasa.

**Keputusan.**
1. **Kandidat = OR.** Kueri teks menjadi disjungsi term yang sudah dinormalisasi; `bm25(memory_fts)` tetap yang menentukan urutan (pinned dulu), dan `LIMIT` milik pemanggil membatasi kandidat. Ini semantik BM25 yang lazim: setiap term memberi skor, dokumen yang cocok sebagian tetap kandidat.
2. **Normalisasi di Go.** `ctxdb.FTSNorm` mengekspor normalisasi index (lowercase + bigram CJK) agar `memstore` dapat menyusun operator sendiri; ekspresi di-bind langsung ke `memory_fts MATCH ?` (bukan `dmb_fts_norm(?)`). Index tetap dinormalisasi oleh trigger.
3. **Setiap token di-quote, di-dedupe, dan dibatasi.** Kata user yang mengeja operator (`or`, `and`, `near`) tetap term, dan teks operator (`"`, `(`, `NEAR)`) tidak bisa menjadi syntax error (properti lama dipertahankan). Term unik maksimal 64; duplikat hanya diskor sekali (REVIEW-SEC14-5).
4. **Filter tidak berubah.** Scope/status/`expires_at`/`purging`/`evidence_event_id` dan urutan pinned tetap; `memory.Service.Recall` dan test karantina tidak berubah perilaku selain kandidat yang lebih luas.
5. **Test.** `TestSearchQuestionRecallsFact` (kueri pertanyaan mengembalikan fakta; term yang tidak ada tidak mengembalikan apa pun), `TestSearchRanksByRareTerm` (term langka menang atas term umum), `TestSearchOperatorWordsAreTerms` (kata/teks operator aman), dan `TestSearchFilters` disesuaikan (dua baris berbagi term "alpha" kini sama-sama kandidat, urutan tetap pinned dulu).
6. **Eval.** Runner E4 (ADR-138) memakai `memory.DefaultRecall` (10) sebagai limit kandidat; hasil dry run fake provider 30/30 (180 call, proyeksi US$0,037069) dilaporkan sebelum run berbayar.

**Dampak terukur (SPEC §20).** Dependency 0, migrasi 0, token prompt 0, perubahan indeks 0 (hanya ekspresi kueri). LOC `ctxdb/memstore` 485/600 dan `ctxdb` 3.429/3.500 (tanpa perubahan `loc.budget`). Biaya kueri: satu MATCH dengan OR dan `bm25` pada kandidat; LIMIT 10 per retrieval.

**Konsekuensi.** (+) Kueri natural bahasa manusia dapat menemukan fakta yang relevan; operator FTS tetap tidak dapat disuntikkan user. (−) Kandidat bisa lebih banyak pada korpus besar (term umum seperti "my"); `bm25` + LIMIT menahan biaya, tetapi kualitas ranking bergantung pada tokenisasi unicode61 (tanpa stopword removal).

## ADR-163 — Tindak lanjut review F2: EC-CTX-007 (direktif MEDIA), tie-break recall, dan bukti GAP-COST-04

**Status:** Accepted 2026-10-22 (delegasi; menutup ACC-6, ACC-12, dan GAP-COST-04 dari review PR #249/#251/#250). Diterapkan di PR tindak lanjut F2.

**Aturan penerapan.** Teks diterapkan (2026-10-22): `internal/compaction/summary.go` + `edgecases_test.go`, `internal/ctxdb/memstore/memstore.go` + `restart_test.go`, `cmd/damnbot/fallback_prefix_test.go`. Mengutip SPEC §22.1 (GAP-SEC-26/EC-CTX-007, GAP-INF-06, GAP-COST-04) dan ADR-122/ADR-133/ADR-137.

**Konteks.** Review menemukan tiga sisa: (a) EC-CTX-007 hanya membuang `MEDIA:` yang berisi nilai, sehingga `MEDIA:` telanjang lolos ke summarizer; (b) tie-break `memstore` tidak deterministik untuk skor bm25 yang sama, dan judul test INF-06 mengklaim penutupan yang belum ada (runner eval belum ada); (c) GAP-COST-04 diklaim lewat test prefix antar-turn yang tidak pernah menjalankan fallback provider.

**Keputusan.**
1. **EC-CTX-007.** `deliveryDirective = MEDIA:\s*\S*` membuang direktif dengan nilai maupun `MEDIA:` telanjang dari isi pesan dan argumen tool call sebelum summarizer; test `TestEdgeCases/EC-CTX-007` mencakup empat bentuk (tool result, assistant, trailing bare, argumen). Status EC-CTX-007 = covered; pembaruan kalimat ADR-122 butir 14 dibawa PR #245 (bila kedua PR mendarat, kalimat yang sama cukup sekali). PR ini juga menambahkan stripper yang sama dengan #249: land satu lebih dulu, PR kedua menjadi delta (catatan merge).
2. **Recall deterministik.** `searchIDs` memakai `ORDER BY m.pinned DESC, bm25(memory_fts), m.id` (dan `created_at DESC, m.id` untuk kueri tanpa teks). Test `TestRecallDeterministicAcrossRestart` menyisipkan dua baris identik (skor bm25 sama) dan memastikan urutan sama pada rerun dan sesudah reopen. Test ini adalah proksi jalur recall yang dinilai E4 (ADR-137), **bukan** penutupan GAP-INF-06: runner eval, baseline nightly, dan cap biaya live tetap terbuka.
3. **GAP-COST-04 (parsial).** `TestFallbackPrefixStable` mengonfigurasi dua route provider A dan B dengan `Fallback: [fake/B]`, memaksa fallback lewat `provider.Do` (tiga 503 pada A lalu sukses di B), lalu memanggil A lagi dan menghitung sha256 pesan system + array tools dari setiap body yang direkam proxy: identik pada kelima attempt; hanya field `model` yang berubah A,A,A,B,A. Ini membuktikan prefix+cache-key di lapisan provider untuk A→B→A. **Status GAP-COST-04 tetap parsial**: composition root `run` belum meneruskan `cfg.Provider.Fallback` ke `provider.Config`, sehingga fallback sesi-nyata baru bisa diuji setelah irisan F5 itu; ledger F2 tetap mencatat baris itu belum tertutup.

**Dampak terukur (SPEC §20).** LOC: `compaction` +≈10, `ctxdb/memstore` +0 (satu klausa ORDER BY), test +≈150; token/binary/dependency 0. Tidak ada perubahan `loc.budget`.

**Konsekuensi.** (+) Tiga temuan review tertutup dengan bukti yang bisa gagal; EC-CTX-007 tidak lagi bisa membocorkan direktif telanjang. (−) Dua PR berbagi kode EC-CTX-007/INF-06 dengan #249/#251; urutan merge harus dijaga agar tidak ada duplikasi diam-diam.

## ADR-139 — Klasifikasi 59 baris parity F2: headless F2, permukaan slash F3, dan tiga baris dipindah

**Status:** Accepted 2026-10-22 (keputusan owner: F2 hanya punya `damnbot run` headless; semua permukaan slash menunggu REPL/TUI F3). Memperbaiki per-row phase ADR-064 untuk 22 baris; baris lain tidak berubah. Diterapkan di PR dokumen ini + PR implementasi lanjutan. Tidak mengubah SPEC, skema, dependency, atau `loc.budget`.

**Konteks.** [`F2-ACCEPTANCE.md`](F2-ACCEPTANCE.md) (PR #242) mengaudit 81 baris FEATURE-PARITY berfase F2 terhadap test di `main` dan setiap head PR: 18 `covered`, 4 `in-flight`, 17 `surface` (kapabilitas ada, permukaan CLI/slash belum), dan 42 `missing`. Ledger itu juga mencatat bahwa F2 belum punya REPL: hanya ada `damnbot run` headless, sehingga baris slash tidak dapat diuji sebagai surface di F2. ADR-064 menempatkan CLI/SLASH mengikuti fase fitur yang dikendalikan; untuk baris sesi/memori/skill keputusan itu membuat permukaan slash jatuh ke F2, padahal permukaannya baru ada di F3 (inti TUI/REPL). Tanpa koreksi, acceptance F2 menuntut surface yang tidak mungkin dibangun di fase itu, dan gate per fase kehilangan arti.

**Keputusan.**
1. **Tiga bucket.** Setiap baris F2 yang `missing` atau `surface` diklasifikasikan: (a) dibangun di F2 sebagai kapabilitas/tool/toolset/subcommand headless; (b) kapabilitas intinya F2 (test-nya ditulis di F2) tetapi permukaan yang dinamai baris (slash/interaktif) pindah ke F3; (c) seluruh baris pindah ke fase lain dengan alasan komponen pemiliknya.
2. **Fase baris = fase selesainya baris.** Untuk bucket (b) kolom `fase` menjadi F3, karena baris itu mendeskripsikan permukaan `/…`; kapabilitas F2 tetap ditagih lewat baris kapabilitas (kolom `Catatan`/ADR ini), bukan dengan membiarkan baris slash berfase F2 tanpa surface. Jenis ini bukan penurunan acceptance: test kapabilitas F2 tetap wajib hijau sebelum F3.
3. **Bucket (a) tetap F2** dan dikerjakan sebagai rangkaian PR kecil: sesi (export/prune/titles), tools (`memory`, `session_search`, `skill_view`/`skills_list`/`skill_manage`), toolsets, `@`-ref, context pin, retry, prompt caching, memory nudge/trim, checkpoints, skills (index, discovery, external dirs, preprocessing, bundles, ledger), dan CLI `sessions`/`skills`/`bundles`/`checkpoints`. Tidak ada baris (a) yang boleh dibiarkan `planned` tanpa PR: daftar PR ada di butir 7.
4. **Bucket (b): 19 baris → F3.** Delapan belas baris `SLASH-*` (002, 006, 007, 008, 010, 011, 013, 015, 016, 017, 044, 045, 066, 068, 069, 070, 071, 083) dan `MEM-019` (`/compress` manual). Semua kapabilitasnya sudah ada di F2 atau menjadi baris (a)/(in-flight) di F2. Tabel pemetaan kapabilitas ada di bawah.
5. **Bucket (c): 3 baris dipindah.** `MEM-022` native compaction provider → F5: API compaction native (Codex/Anthropic) hanya ada bersama adapter provider registry F5; route F2 adalah `openai-chat` generik yang tidak punya endpoint compaction. `MEM-023` dan `PLUG-014` (context engine plugin) → F8: keduanya membutuhkan host plugin/memory provider yang dijadwalkan F8; F2 hanya memiliki engine `compressor` bawaan, dan slot `TSET-035` (komposisi toolset `context_engine`) tetap F2 sebagai data komposisi.
6. **Hitungan baru.** F1 46, F2 59, F3 135, F4 56, F5 201, F6 86, F7 101, F8 148, F9 5 (Σ 837). `ROADMAP.md` §"Pemetaan parity → fase" menyebut angka ini dan sel F5 prefix MEM mendapat `native compaction provider (MEM-022)` agar `TestParityPhaseColumn` tetap konsisten; `want` di test diperbarui. 18 covered + 4 in-flight + 37 (a) = 59 baris F2.
7. **Rencana PR implementasi (a).** (i) `sessions` CLI: export HTML/MD/JSON, prune/archive, titles/rename, list (CLI-057, MEM-013/014/015/016; `sessions show`/resume milik worker lain dan tidak diduplikasi); (ii) tools `memory` + `session_search` (TOOL-070, TOOL-073); (iii) toolsets + config per-platform + custom (TSET-021/023/025/035/038/039/040/041/043, CORE-004/005); (iv) tools `skill_view`/`skills_list`/`skill_manage` (TOOL-074/075/076, SKILL-002); (v) bundles + CLI `bundles` (SKILL-007, CLI-047); (vi) skills index/discovery/external dirs/preprocessing/ledger + CLI `skills` lokal (SKILL-004/009/010/019/023, CLI-046); (vii) `@`-ref, context pin, prompt caching (MEM-024/025/034); (viii) retry, memory nudge, memory trim (MEM-030/003/033); (ix) checkpoints CLI (CLI-039). Setiap PR wajib test; PR yang menambah CLI wajib skenario `internal/e2e` (harness PR #239).
8. **Scope per baris CLI besar.** Acceptance F2 `CLI-046` adalah subcommand lokal yang dinamai deskripsinya (install/list/inspect/check/update/audit/uninstall/reset/diff/opt-in/opt-out/config/trust/untrust); subcommand jaringan hub (`browse`, `search`, `publish`, `snapshot`, `tap`, `repair-official`) milik baris skills hub F8 dan tidak diklaim F2. Acceptance F2 `CLI-057` adalah `list`, `rename`, `export`, `prune`, `delete`, `archive`, `stats`, `pin`, `unpin`, `pinned`; subcommand perbaikan/optimasi (`optimize*`, `clean-markers`, `repair*`, `recover`, `set-journal-mode`, `retitle-skills`, `browse`, `import`) adalah operasi pemeliharaan F8/F9 dan dicatat sebagai sisa, bukan dijanjikan diam-diam.
9. **Tidak ada surface palsu.** Baris bucket (b) tidak boleh ditutup dengan membantu REPL-mini di F2; F2 hanya membuktikan kapabilitas lewat API/tool/CLI headless yang disebut tabel di bawah. Baris (c) tidak meninggalkan kode mati: `TSET-035` tetap data komposisi, dan tidak ada slot plugin setengah jadi.

**Tabel bucket (a) — dibangun di F2.**

| ID | Status ledger | Rencana F2 |
|---|---|---|
| CLI-039 | missing | `damnbot checkpoints` (status/list/prune/clear/clear-legacy) di atas snapshot journal |
| CLI-046 | surface | `damnbot skills` subset lokal (butir 8); hub F8 |
| CLI-047 | missing | `damnbot bundles` (list/show/create/delete/reload) |
| CLI-057 | surface | `damnbot sessions` subset butir 8; `show`/resume dari worker lain |
| TOOL-070 | missing | tool `memory` (tambah/ganti/hapus catatan + profil user) memakai `memory.Service` |
| TOOL-073 | missing | tool `session_search` (FTS + ringkasan) memakai `ctxdb.Search` |
| TOOL-074 | missing | tool `skill_manage` (buat/edit/hapus SKILL.md + file pendukung, lewat lint/install) |
| TOOL-075 | missing | tool `skill_view` (isi skill + file terkait) |
| TOOL-076 | missing | tool `skills_list` (nama + deskripsi) |
| TSET-021 | missing | toolset `memory` (komposisi + filter ketersediaan) |
| TSET-023 | missing | toolset `session_search` |
| TSET-025 | missing | toolset `skills` |
| TSET-035 | missing | toolset komposit `context_engine` (data komposisi; engine plugin F8) |
| TSET-038 | missing | toolset komposit `debugging` (anggota disaring dari tool terdaftar) |
| TSET-039 | missing | toolset komposit `safe` |
| TSET-040 | missing | toolset komposit `coding` |
| TSET-041 | missing | toolset komposit `hermes-acp` |
| TSET-043 | missing | toolset komposit `hermes-cli` |
| MEM-003 | missing | memory nudge periodik (config interval, dititipkan ke loop) |
| MEM-013 | surface | resume bernama/terakhir + recap di `run --session/--continue` (flag milik worker w5; recap milik PR ini) |
| MEM-014 | missing | export sesi HTML/MD/JSON (renderer murni + CLI) |
| MEM-015 | missing | prune/archive sesi + VACUUM (memakai `ctxdb.Purge`/maintenance) |
| MEM-016 | surface | judul sesi via `session_labels(key='title')` + rename |
| MEM-024 | missing | `@file`/`@url`/`@folder` expansion di prompt headless |
| MEM-025 | missing | context pin: pesan/konteks dikecualikan dari compaction |
| MEM-030 | missing | retry pesan terakhir (headless `run --retry`/API loop) |
| MEM-033 | missing | trim RSS periodik (`debug.FreeOSMemory` terjadwal, injectable) |
| MEM-034 | missing | breakpoint prompt caching pada batas tier stable/context |
| SKILL-002 | missing | lihat TOOL-074/075/076 |
| SKILL-004 | missing | preprocessing SKILL.md (template vars; inline shell default off) |
| SKILL-007 | missing | bundles skill (alias satu nama → beberapa skill) |
| SKILL-009 | missing | project skill discovery (dir proyek trusted) |
| SKILL-010 | missing | external skill dirs dari config |
| SKILL-019 | surface | ledger & provenance skill (sha + asal + riwayat) |
| SKILL-023 | missing | skills index (build + delta) |
| CORE-004 | missing | komposisi toolset + aktif/nonaktif per platform (headless dulu) |
| CORE-005 | missing | custom toolset dari config |

**Tabel bucket (b) — kapabilitas F2, permukaan F3.**

| ID | Kapabilitas F2 (test) |
|---|---|
| SLASH-002 | sesi baru: `ctxdb.CreateSession` (`TestCreateSessionMetadata`); surface REPL F3 |
| SLASH-006 | riwayat: `TestHistoryForkAware` |
| SLASH-007 | export: MEM-014 (`TestSessionExport*`) |
| SLASH-008 | retry: MEM-030 (`TestRetryLastMessage`) |
| SLASH-010 | undo: `internal/journal` `TestUndoSuite` |
| SLASH-011 | judul: MEM-016 (`TestSessionTitle`) |
| SLASH-013 | branch: `TestForkAndRewind` + CLI fork |
| SLASH-015 | compress: `compaction.Compactor` (`TestCompaction`, manual trigger) |
| SLASH-016 | rollback: `TestUndoSuite`/`TestUndoSuiteRefusals` |
| SLASH-017 | snapshot: backup/restore OPS-006 (PR #197) |
| SLASH-044 | resume: MEM-013 (`run --session`) |
| SLASH-045 | sessions: CLI-057 list |
| SLASH-066 | autoprune: MEM-015 + config `sessions.auto_prune` |
| SLASH-068 | toolsets: TSET-021..043 |
| SLASH-069 | skills: TOOL-074/075/076 + CLI-046 |
| SLASH-070 | memory: `TestQuarantine*`/approval; surface `/memory` F3 |
| SLASH-071 | bundles: SKILL-007 |
| SLASH-083 | reload-skills: SKILL-023 index rebuild |
| MEM-019 | compress manual: trigger compaction manual (test paket `compaction`) |

**Dampak terukur (SPEC §20).** Kode/dependency/token/migrasi 0. Dokumen: ADR ini, 22 sel `fase` FEATURE-PARITY, angka fase ROADMAP, `want` `TestParityPhaseColumn`, satu baris CHANGELOG. Gate parity F2 kini 59 baris, F3 135, F5 201, F8 148.

**Konsekuensi.** (+) Acceptance F2 hanya menuntut yang dapat dibangun dengan `run` headless, dan setiap baris yang pindah punya test kapabilitas F2 yang eksplisit. (+) Gate per fase kembali bermakna: F2 tidak lagi memuat 19 permukaan slash yang mustahil. (−) F3 menerima 19 baris permukaan; REPL/TUI F3 harus menutupnya, dan baris F8/F5 tetap terbuka lebih lama. (−) Setiap perpindahan fase berikutnya tetap wajib ADR + perubahan angka di ROADMAP dan test (ADR-064).

## ADR-155 — Penutupan EC-FIO-006 dan EC-TCALL-009: agregat inline berskala window dan validasi jawaban final

**Status:** Accepted 2026-10-22 (F2 acceptance; mengamandemen ADR-129 butir 12 dan menegakkan SPEC P5 amandemen/GAP-COST-08). Tidak mengubah SPEC, skema, migrasi, atau dependency; `loop` 2.050 → 2.100 dan `tools` 2.200 → 2.250 (Total core 38.090 → 38.190, terukur 2.048/2.178).

**Konteks.** Dua entri F1 tidak punya katalog maupun test: EC-FIO-006 ("cap hasil tool dijepit ke window model") dan EC-TCALL-009 ("final answer hanya fragmen"). SPEC P5 sudah menetapkan agregat inline per turn `min(24.000 char, 0,30 × context_window_tok × rasio char/tok sesi)` (GAP-COST-08) tetapi `internal/tools` hanya memakai konstanta 24.000; `internal/loop` sudah punya `event.DetailInvalidAnswer` tanpa produsen. ADR-129 butir 12 menolak heuristik teks di loop: deteksi milik verifikasi. SPEC §3.5/§7.2 mendefinisikan `invalid_answer` sebagai jawaban yang tidak lolos `--answer-schema`, sehingga aturan itu sudah ada di kontrak dan tidak perlu dikarang.

**Keputusan.**
1. **Agregat berskala.** `tools.Config.WindowTokens` dan `CharsPerToken` (0 = tidak diketahui) membentuk `tools.TurnInlineCap(window, ratio) = min(24.000, 0,30 × window × ratio)`; `pageCap`, `inlineLeft`, dan `spillResult` memakai nilai itu, dan page cap dijepit ke agregat. `cmd/damnbot` mengisi window dari `loop.turn_max_input_tok` (window route yang tersedia saat ini) dan rasio dari estimator COST-09 atas prompt run (`budget.Count`), jadi perilaku default (window besar) tetap 24.000.
2. **Validasi jawaban sebagai seam.** `loop.Deps.ValidateAnswer func(string) error` (nil = tanpa skema) dipanggil setelah pesan assistant tersimpan; kegagalan berakhir `end_turn/invalid_answer` dan tidak meng-exclude turn (ADR-134). Loop tidak menambah aturan teks: `cmd/damnbot --answer-schema <file>` mengompilasi JSON Schema pihak ketiga (`schema.ModeMCP`) dan memasang validator itu. Jawaban kosong tetap `empty_response`.
3. **Test.** `TestTurnAggregateScaled` (32K × rasio 1 → 9.830 char; lima hasil 40.000 char tetap ≤ 30 % window; tiga hasil 4.000 char: yang ketiga spill) dan `TestEdgeCases/EC-TCALL-009` + `TestRunAnswerSchema` (fragmen → `invalid_answer`, exit 2; jawaban sesuai → `completed`).

**Konsekuensi.** (+) Dua entri F1 punya katalog dan test tanpa heuristik bahasa di loop; aturan validasi tetap di `internal/schema`. (−) `--answer-schema` baru memvalidasi keluaran, belum mengirim `response_format` ke provider; window yang dipakai adalah cap input konfigurasi sampai `registry.Limits` terhubung penuh (GAP-PROV-02).

## ADR-173 — Tool skill: `skills_list`, `skill_view`, `skill_manage`

**Status:** Accepted 2026-10-23 (F2; TOOL-074/075/076, SKILL-002, EC-CTX-006, SEC16-1..5, ACC-56..61). Budget `tools` 2.200 → 2.500 (+300; terukur 2.315) dan `ctxdb/skillstore` 400 → 460 (+60; terukur 438); dependency 0; DDL 0 (ADR-074). Diterapkan bertahap dalam dua PR berurutan: lapisan baca (`skills_list`, `skill_view`) lalu lapisan tulis (`skill_manage`) beserta penguatan SEC16-5.

**Konteks.** Agent hanya dapat menjalankan instruksi skill bila dapat menemukannya dan memuatnya, tetapi hasil `skill_view` adalah data pihak ketiga dan `skill_manage` menulis file yang dieksekusi pada run berikutnya. `ctxdb/skillstore` sudah menyimpan metadata dan status review; lapisan tool dan jalur baca/tulisnya belum ada.

**Keputusan.**
1. **Dua tool baca.** `skills_list` mengembalikan hanya skill yang lolos review (nama + deskripsi, cap 50); `skill_view` mengembalikan `SKILL.md` + file pendukung (≤ 20 file, ≤ 64 KiB total, penanda `[skill_view truncated]`). Skill terkarantina tidak muncul di daftar dan `skill_view`-nya `not_found`.
2. **Untrusted.** `skill_view` menandai hasilnya `UntrustedSource = "skill:<name>"`; dispatcher membungkusnya sebagai `<untrusted>` seperti output file/web. Instruksi di dalam skill tidak pernah menjadi system/developer message.
3. **Lookup eksak.** `skillstore.ByName` menggunakan indeks `name` (bukan FTS top-k) sehingga skill ke-51 dengan nama mirip tetap dapat dimuat; `Search` tetap dipakai untuk daftar.
4. **Tool tulis terkarantina.** `skill_manage` (`create`/`update`/`delete`, efek `non_idempotent`) hanya menulis ke `<data>/skills/agent/<name>` (tidak pernah ke `<config>/skills`); `create` menolak nama yang sudah ada di DB **atau** di disk (`<config>/skills/<name>`, termasuk skill pembayaran yang belum terdaftar). Versi baru divalidasi dan ditulis ke staging, lalu `skills.Service.Install`; hanya setelah status `scanned` direktori ditukar dan baris agent lama diganti. Update yang ditolak/gagal mengembalikan versi approved lama; `rejected` menghapus direktori staging. Materialisasi ke `<config>/skills/<name>` terjadi pada run berikutnya setelah user meng-approve, dan tidak pernah menimpa direktori yang bukan milik baris itu.
5. **Approval selalu.** `skill_manage` mengimplementasikan `Inspector` dengan `RequireAsk` dan `AllowSession:false` di semua mode (`auto`, `auto-edit`, headless); tanpa approver tool gagal tertutup, sehingga `side_effects: [payment]` tidak pernah melewati review.
6. **Filesystem/validasi.** Nama `^[a-z0-9][a-z0-9._-]{0,63}$`; file pendukung relatif menolak path absolut, `..`, kosong, NUL, dan backslash sebelum mutasi apa pun; baca/tulis memakai `os.Root` dan temp+rename; body/front matter divalidasi parser yang sama; `description` satu baris tanpa kontrol dan di-quote. **SEC16-5:** basename `SKILL.md` ditolak secara case-insensitive (mis. `skill.md`, `Skill.MD`) sebagai file pendukung, sehingga tidak dapat menimpa dokumen `SKILL.md` pada filesystem yang tidak case-sensitive; jalur baca melewati `SKILL.md` dengan aturan case-insensitive yang sama.
7. **EC-CTX-006.** Hasil `skill_view` yang dipangkas compaction menjadi stub kanonik berawalan `[SKILL_PRUNED]`, sehingga model tidak dapat menganggap isi skill masih termuat setelah dipangkas.
8. **Test.** Unit tool (list metadata, untrusted view, `not_found`, efek non-idempotent, `Inspector` selalu `RequireAsk` termasuk `auto`/`auto-edit`), manager (karantina → approval user → view; skill terkarantina tidak muncul di list dan view-nya `not_found`; create menolak skill user yang hanya ada di disk tanpa mengubah file/gate pembayaran; update rejected/gagal mempertahankan versi approved lama; rejected create menghapus file; delete hanya baris agent; trusted tidak dapat ditimpa/dihapus; materialisasi hanya setelah approval; lookup eksak di antara > 50 baris; cap payload dan penolakan path, termasuk `skill.md`/`Skill.MD`), e2e binary asli (list/view skill approved dan pembungkusan `<untrusted>`; skill terkarantina tidak terlihat; `skill_manage` ditolak tanpa approver dan tidak menulis file apa pun), `TestEdgeCases/EC-CTX-006`.

**Konsekuensi.** (+) Skill dapat ditemukan dan dimuat secara eksplisit; kontennya tetap data dan approval tetap milik user. (+) Tanpa storage baru (DDL 0) dan tanpa dependency baru. (−) `skill_manage` tidak tersedia pada run headless non-interaktif (fail closed); skill agent baru aktif setelah approval + materialisasi pada run berikutnya; `skill_view` menambah token sesuai file yang diminta (≤ 64 KiB/20 file, body ≤ 8 KiB).

## ADR-176 — Job acceptance fase menyertifikasi fase yang ditinggalkan: himpunan wajibnya fase ≤ `PHASE`−1 (off-by-one #244)

**Status:** Accepted 2026-10-22 (keputusan owner/director). Mengoreksi implementasi ADR-080 butir 2 di PR #244; norma ADR-080 butir 2, ADR-077, dan tabel fase mana pun **tidak** berubah.

**Aturan penerapan.** Teks diterapkan (2026-10-22): `internal/testutil/phase/acceptance_test.go` (mode acceptance + log diagnostik) dan tes `internal/testutil/phase/acceptance_rules_test.go`; klausa penjelas di `docs/ROADMAP.md` §"Regression suite". Mengutip ADR-080 butir 2, ADR-077 butir 3, ROADMAP §"Regression suite", SPEC §16.

**Konteks.** ROADMAP §"Regression suite" menetapkan norma job acceptance fase: *"Job acceptance fase `make phase-acceptance` (dijalankan saat menaikkan `PHASE` dari Fx ke Fx+1 dan di setiap PR yang mengubah file `PHASE`): semua item fase ≤ Fx wajib `covered`, PR penaikan gagal bila tidak; di `PHASE = F9` tidak boleh ada `pending`."* Job berjalan *ketika* `PHASE` menjadi Fx+1, jadi fase yang disertifikasi adalah Fx = `PHASE`−1. Kalimat yang sama juga memberi semantik gate PR: fase **<** `PHASE` wajib `covered`, fase **=** `PHASE` boleh `pending:<PHASE>` atau `covered`, fase **>** `PHASE` wajib `pending`.

Implementasi #244 (`checkRequiredIDs`/`checkCovered`) memakai `cur` = isi file `PHASE` apa adanya, sehingga pada `PHASE = F3` ia menuntut himpunan fase ≤ 3. Itu off-by-one: fase yang **dimasuki** (`= PHASE`) ikut dituntut `covered`/berbaris katalog, padahal dokumen hanya mewajibkan fase ≤ `PHASE`−1. Dampaknya konkret pada tabel EC ROADMAP (166 id: F1 55, F2 41, F3 10, F4 26, F5 24, F6 1, F7 9): `PHASE = F3` menuntut tambahan 10 id fase F3 (EC-CFG-001..008, EC-FIO-007, EC-FIO-018) yang komponennya justru baru mulai dibangun di F3, sehingga penaikan `PHASE` ke F3 menjadi mustahil tanpa menandai `covered` tanpa tes — persis yang dilarang. Himpunan wajib yang benar sama dengan gate PR: fase < `PHASE` (96 id pada `PHASE = F3`).

**Keputusan.**
1. **Himpunan wajib acceptance = fase < `PHASE`, yaitu ≤ `PHASE`−1.** Id pada fase itu wajib punya baris katalog dan berstatus `covered`/`n/a`; baris yang absen atau `pending` → `PHASE GAGAL`. Pada `PHASE = F1` himpunan ini kosong (tidak ada F0), sehingga gate tetap hijau di awal tanpa melonggarkan apa pun untuk fase berikutnya.
2. **Fase yang dimasuki (`= PHASE`) boleh `pending:<PHASE>`, `covered`, `n/a`, atau belum punya baris katalog.** Barisnya tidak boleh menyandang status fase lain (mis. `pending:F2` pada id fase F3): itu tetap GAGAL, sejalan dengan `Gate.Item`, yang menolak label salah di runtime. Ketegasan lama untuk fase < `PHASE` tidak berkurang sedikit pun.
3. **`PHASE = F9` tidak boleh meninggalkan `pending`** (ADR-080 butir 2): pada F9 tidak ada Fx+1, jadi seluruh baris katalog wajib non-`pending`. Id di atas `PHASE` tetap hanya di-log.
4. **Log diagnostik.** Id pada fase ≥ `PHASE` yang belum berbaris atau masih `pending` dicetak sebagai daftar "open" (`logOpen`) supaya laporan run menampilkan apa yang akan dituntut oleh penaikan berikutnya. Status itu tidak menggagalkan job.

**Bukti sebelum/sesudah** (perintah: `DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase/ -run TestEdgeCaseCatalogSync -count=1`, `PHASE` diubah hanya di worktree). Pohon F2-lengkap (katalog fase ≤ F2 terisi):

| `PHASE` | gate sebelum (#244) | gate sesudah (ADR-176) |
|---|---|---|
| F1 | ok | ok |
| F2 | ok | ok |
| F3 | **FAIL** — 10 `PHASE GAGAL` (EC-CFG-001..008, EC-FIO-007, EC-FIO-018) | **ok** — 70 id fase ≥ F3 dicatat sebagai open |

Pohon stack F1 (tambahan #242/#254/#255/#257, belum termasuk baris F2 dari PR pemiliknya): `PHASE = F2` berubah dari FAIL (6 lubang F2) menjadi ok; `PHASE = F3` berubah dari 16 `PHASE GAGAL` menjadi 6 (hanya lubang F2 yang memang belum ditutup), sementara 10 id F3 pindah ke daftar open.

**Dampak terukur (SPEC §20).** Token/biaya/binary/dependency/migrasi 0; LOC produksi 0 (hanya berkas tes `internal/testutil/phase`); tidak ada perubahan `loc.budget`.

**Konsekuensi.** (+) Penaikan `PHASE` menyatakan tepat apa yang sudah selesai — fase yang ditinggalkan — sehingga penaikan ke F3 tidak lagi menuntut komponen yang baru dimulai di F3. (+) Gate tidak kehilangan ketegasan: semua id fase < `PHASE` tetap wajib `covered`/`n/a`, termasuk id yang sebelumnya `pending:F1`. (−) Satu id fase yang dimasuki bisa tertinggal `pending` sampai penaikan berikutnya; karena itu daftar "open" dicetak pada setiap run.

## ADR-178 — Budget `loop` 2.050 → 2.100 setelah penanda partial EC-PROV-008 dan `windowRoom` EC-CTX-017

**Status:** Accepted 2026-10-22 (F2; EC-PROV-008, EC-CTX-017). Budget `loop` 2.050 → 2.100 (+50; terukur 2.051 sesudah penanda partial, guard `windowRoom`, dan penyaringan snapshot kompaksi). Dependency 0; DDL 0 (ADR-074); total core 38.470 → 38.520 ≤ 47.000.

**Konteks.** PR #245 menambah baris di `internal/loop`: penanda `partial` pada payload event assistant dan jalur replay (`EC-PROV-008`), guard window `windowRoom = window - max_tokens` beserta pemanggilnya (`EC-CTX-017`), dan pengisian `Prompt.WindowTokens` dari window efektif. Baris `loop` di `loc.budget` tadinya 2.050 (ADR-134) dengan `scc` terukur 2.043 di atas head #250; penambahan itu membawa terukur ke 2.051, satu baris di atas maksimum.

**Keputusan.**
1. **Naikkan barisnya.** `loop` 2.050 → 2.100 di `loc.budget` (+50, terukur 2.051), dengan "Total core" `docs/ENGINEERING.md` disinkronkan ke Σ core `loc.budget` = 38.520 ≤ 47.000. Baris SPEC §3.2.4 tidak berubah, sehingga tidak perlu persetujuan baris SPEC (SPEC §20).
2. **Tanpa pemecahan paket.** Guard window dan penanda partial tetap di `internal/loop` (loop memiliki guard dan payload event); memecahnya demi 1 baris akan menambah seam baru tanpa manfaat dan memecah invariant `turn`.
3. **Di luar §3.2.4.** `ctxdb/compactstore` (penyaring snapshot) dan `cmd/damnbot` tidak memakai budget baru; tidak ada dependency atau DDL baru.

**Konsekuensi.** (+) Guard EC-CTX-017 dan penanda EC-PROV-008 hidup di paket yang memang memiliki guard dan payload event; Σ core tetap jauh di bawah 47.000 (sisa 8.480). (−) Sisa headroom `loop` hanya 49 baris (2.100 − 2.051) untuk lanjutan F2; kenaikan lain di `internal/loop` kembali butuh revisi tabel + ADR.

## ADR-179 — Pengemasan ulang job CI: gate pendek dibundel, shard fuzz berurutan (slot runner)

**Status:** Accepted 2026-10-22 (delegasi owner: "kurangi konsumsi slot runner tanpa mengurangi cakupan tes, dilarang melonggarkan gate, men-skip tes, atau menghapus cakupan"). Mengamandemen ADR-135 butir 3 pada **pengemasan job**; isi gate, tier path, dan daftar konteks wajib tidak berubah.

**Konteks.** Satu PR-run `ci` menjadwalkan 22 instance job (18 definisi + matrix `test` ×3 + shard `fuzz` ×3; main/nightly 25). Dengan pool runner repo privat yang terbatas, banyak job pendek per PR membuat run lain menunggu di antrean (terukur: job `ci-required`/`test` sempat menunggu >15 menit pada 2026-10-22). Sebagian besar job itu hanya 20–120 detik dan tidak saling bergantung: `loc` (20 s), `loc-ratio`, `size-check` (37 s), `deadcode` (39 s), `vet-cross` (2 m 18 s), `build-matrix` (3 m 41 s), `bench-parity` (2 m 13 s), `bench-budget` (38 s), `tidy-check`/`mod-verify`/`deps-check`/`licenses` (36 s), `vuln` (26 s), `perf` (1 m), `coverage` (2 m 31 s). Tiap job juga membayar satu checkout + satu `setup-go` (~30–40 s).

**Keputusan.**
1. **Gate statis → satu job `static`.** `make loc` (E-19, E-61), `make loc-ratio` (E-70), `make size-check` (E-62), `make deadcode` (E-53) berjalan berurutan dengan satu checkout/setup.
2. **Portabilitas → satu job `portability`.** `make vet-cross` (ADR-073/AUD-033) + `make build-matrix` (E-39/E-65).
3. **Benchmark modul → satu job `bench`.** `make bench-parity` (CT-21, korpus C1+C3 tetap di-cache) + `make bench-budget` (NEW2-07).
4. **Supply chain → satu job `supply`.** `make tidy-check`, `mod-verify`, `deps-check`, `licenses` (E-46/E-47/E-50, ADR-074 AUD-074(a)) + `make vuln` (E-54).
5. **Cakupan/benchmark absolut/acceptance → satu job `extra`.** `make perf` dijalankan **pertama** (budget absolut, ×1,5 runner bersama, ADR-077/NEW-38) agar diukur sebelum langkah berat lain, lalu gate `PHASE` (ADR-080, `fetch-depth: 0`) dan `make coverage` (E-58/AUD-059).
6. **Fuzz → satu job.** Tiga shard (`make fuzz SHARD=1/3|2/3|3/3`, FUZZTIME 60 s/target) berjalan berurutan; timeout 15 → 30 menit.
7. **Yang tidak berubah.** Setiap target Makefile tetap dipanggil tepat sekali dengan perintah, tag, dan variabel yang sama (`make test RACE=…`, `make test-timing`, `make fuzz SHARD=i/3`, …). `lint` (golangci-lint + `vet-custom`) dan `test-timing` tetap job tersendiri: lint untuk umpan balik cepat penulis PR, `test-timing` karena mengukur budget waktu dinding (ADR-048) dan tidak dibagi dengan langkah berat lain. Matrix `test` tetap tinggal matrix (3 target di PR, 6 di main/nightly, ADR-135). `ci-required` tetap ada dengan `needs` yang diperbarui agar tetap menjumlahkan seluruh gate; konteks wajib branch protection (`ci-required`, `meta-test`, `pr-meta`) tidak berubah.
8. **Tidak diambil:** memangkas matrix OS di PR (mis. macOS/Windows hanya bila diff menyentuh kode lintas-OS). Bukti menolak: kegagalan **hanya-Windows** nyata pada PR dokumen (#242, `TestKill9Chaos` "child chaos not ready" di `test (windows-latest)` saat ubuntu/macos hijau) — memangkas matrix akan menghilangkan deteksi itu. Opsi ini diajukan terpisah ke owner, bukan diterapkan.

**Dampak terukur (SPEC §20).** Job per PR-run: 22 → 13 (−41 %); main/nightly: 25 → 16. Gate Makefile yang hilang: 0 (diverifikasi dengan membandingkan himpunan perintah `make …` kedua versi workflow). Machine-minute ≈ sama atau turun karena setup (checkout + `setup-go`) berkurang 22 → 13 per run. Wall-clock PR tetap dibatasi `test` (timeout 25 m); job baru (`static` ≈ 3 m, `portability` ≈ 6 m, `bench` ≈ 3 m, `supply` ≈ 2 m, `extra` ≈ 5 m, `fuzz` ≈ 12 m) berada di bawahnya.

**Konsekuensi.** (+) Antrean PR turun tajam tanpa kehilangan satu gate pun; (−) satu job bundle berarti satu kegagalan menandai langkahnya di dalam job (nama langkah tetap memuat E-id agar triase secepat job terpisah); (−) `fuzz` menjadi berurutan (wall-clock ~3× shard tunggal) sebagai ganti tiga slot paralel; bila wall-clock itu mengganggu, jalan keluarnya adalah menaikkan `FUZZTIME` per shard atau mengembalikan tiga job — bukan menurunkan FUZZTIME.
