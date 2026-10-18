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

**Keputusan.** P13: full build ≤ 52 MB stripped (linux/amd64), arsip rilis ≤ 22 MB, build `-tags minimal` (core + TUI + ACP + code tools) ≤ 22 MB. Budget per modul di SPEC §3.2.4; kontribusi modul diukur di CI dengan build `nomod_<id>`.

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

**Keputusan.** Go: `go/parser`/`go/ast` (stdlib). Bahasa lain: grammar tree-sitter dikompilasi ke WASM, dijalankan dengan `github.com/tetratelabs/wazero` (pure Go). Grammar tidak di-embed di build default: diunduh saat pertama dipakai dari rilis Damn-Bot yang di-pin SHA-256 ke cache, atau `damnbot doctor grammars install --offline <bundle.tar.gz>` (stdlib, tanpa zstd); build `-tags grammars_embedded` menyertakan 12 grammar populer. Tanpa grammar → outline heuristik regex. LSP client (diagnostics, definition, references, rename) sebagai modul lazy memakai language server yang terpasang.

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

**Konsekuensi.** Spec tetap **belum** dibekukan; freeze menunggu integrasi GAP-SWEEP (SPEC §20). Biaya LLM fase F0 kecil (10 task × model murah); biaya run penuh ditunda ke RC.

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

**Keputusan.** Seluruh keputusan §22.2/§22.3 diterapkan ke bagian normatif SPEC (§2, §3.5, §4, §9, §10, §11 DDL, §12, §14, §15, §17.1); §22.2–§22.4 menjadi log historis. Desain P1 tanpa menaikkan batas: katalog deferred = baris grup modul ≤ 15 tok + satu baris gabungan MCP ≤ 60 tok + overflow `+N more groups` (worst case 450 ≤ 600), urutan grup dan nama server MCP alfabetis murni sehingga byte-identik per (versi, config hash) (dipertegas ADR-036, N-01); context tier = sub-cap fakta 60 / snapshot 150 / AGENTS.md 400 / memori 300 (pinned ≤ 150 + BM25, sisanya via `recall`) / skill 150 / pemisah 40 = 1.100, dengan urutan potong AGENTS.md → memori non-pinned → skill. CLI dipangkas ke 20 perintah top-level. Matriks build tunggal 8 target dengan tag `goolm`. FEATURE-PARITY mendapat kolom `status` (837 baris penyebut gate = 836 `planned` + 1 `external-bridge`; 17 `dropped-with-approval` di luar penyebut).

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
- **Angka tunggal:** Σ LOC 184.900 = Σ kolom fase ROADMAP (F1 29.500, F2 12.000, F3 21.500, F4 20.000, F5 30.500, F6 24.500, F7 25.100, F8 21.800; JS ≈ 9.900); dependency langsung rencana 26 ≤ 28; ACP 2.500 LOC; skill di context tier ≤ 150 tok; matriks test PR 6 target berrunner + 2 QEMU; job `sqlite-engine` 8 target; LOC per fitur ≤ 227 (190.000 ÷ 837); edge-case per kategori = katalog (Σ 166); CT-29 ditambahkan (30 ID berblok).
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

**Dampak terukur (SPEC §20).** Token 0, latensi 0, LOC 0 (Σ §3.2.4 tetap 184.900; spike = tooling/test F0), ukuran binary 0, dependency 0. Baris SCORECARD/MITIGATION tidak berubah. Migrasi: tidak ada. ROADMAP F0 bertambah 2 deliverable + 1 gate.

**Konsekuensi.**
- (+) Spec terkunci; perubahan berikutnya dapat dilacak per ADR.
- (+) Risiko utama (persistence) dibuktikan dengan kode sebelum fitur dibangun di atasnya.
- (−) F0 lebih panjang (spike + errata backlog + review non-persistence).

## ADR-048 — Budget waktu dinding di job terpisah tanpa `-race`

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-P08 (`docs/F0-REPORT.md` §"Temuan spec").

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

**Konteks.** Cakupan SPEC §11 (purge 31 langkah, gc dua fase, barrier, restore, migrasi, recovery journal, lease/fencing) terukur 2.598–2.610 LOC non-test di spike F0, walaupun langkah purge sudah dipindah ke `queries.sql` (tidak dihitung) dan protokol gateway/approval ke test (ADR-053). Budget E-19 2.500.

**Keputusan.** Budget E-19 `ctxdb` (tanpa file `.sql`) = **2.800** (`loc.budget`, tabel ENGINEERING §2.4). Total core per paket 31.150 → **31.450**, tetap ≤ 47.000 (gabungan SPEC §3.2.4 core + codetools + exec + heal). Baris SPEC §3.2.4 dan Σ 184.900 tidak berubah, sehingga cukup revisi tabel ENGINEERING (aturan §2.4); ADR ini mencatat keputusannya.

**Dampak terukur (SPEC §20).** LOC: budget +300 di `ctxdb`; Σ §3.2.4 tetap 184.900 ≤ 190.000. Token/latensi/binary/dependency 0. Headroom `ctxdb` saat ini ≈ 190 LOC.

**Konsekuensi.**
- (+) `make loc` hijau tanpa memecah paket secara artifisial.
- (−) Sisa ruang `ctxdb` kecil; fitur storage F1+ (memory, skills, journal) tetap di paketnya masing-masing (E-03/E-09), bukan di `ctxdb`.

## ADR-050 — E-70 (rasio test:source) menjadi gate mulai 5.000 LOC

**Status:** Accepted 2026-10-18 (keputusan user). Menutup TS-03.

**Konteks.** Rasio 0,8–1,2 tidak bermakna pada basis kode kecil yang didominasi test spike (scaffold 121:29; F0 3.391:2.639 = 1,28). Gate merah permanen membuat meta-test-nya tidak bermakna.

**Keputusan.** `make loc-ratio` selalu melaporkan rasio. Gate **gagal** hanya bila total Go non-test ≥ **5.000** LOC (`scc --no-gen`) dan rasio di luar 0,8–1,2. Di bawah ambang target exit 0 dengan baris `LOC ratio report-only: …`. Meta-test membuktikan gate tetap gagal pada pohon sintetis di atas ambang (5.000:5.000 lulus, +2.000 baris test gagal).

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency/LOC 0. ENGINEERING E-70 dan §8 diperbarui.

**Konsekuensi.**
- (+) Gate bermakna begitu kode produksi cukup besar (≈ F1).
- (−) Di bawah 5.000 LOC rasio hanya terlihat di laporan CI.

## ADR-051 — Toolchain go1.26.9 (GO-2026-6604)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup blocker `vuln` F0 (E-44/E-54).

**Konteks.** `govulncheck` melaporkan GO-2026-6604 (stdlib `os.Root`: `MkdirAll`/`Remove`/`RemoveAll`/`Rename`/`OpenFile`/`Stat` dapat mengikuti junction keluar root di Windows) terjangkau dari `internal/ctxdb`; diperbaiki di go1.26.9. Repo mem-pin go1.26.8.

**Keputusan.** Direktif `go 1.26.9` di `go.mod`, `bench/go.mod`, `tools/go.mod` (tanpa baris `toolchain` terpisah: `go mod tidy` membuangnya bila sama dengan `go`). CI `actions/setup-go` membaca `go-version-file` sehingga ikut naik; lokal `GOTOOLCHAIN=auto` mengunduh go1.26.9. Teks ENGINEERING yang menyebut 1.26.8 adalah catatan pengukuran historis, bukan pin.

**Dampak terukur (SPEC §20).** Token/latensi/LOC/dependency 0; ukuran binary diukur ulang oleh `make size-check`. `make vuln`: 0 vuln terjangkau.

**Konsekuensi.** (+) Gate E-54 hijau. (−) Kontributor dengan Go lokal < 1.26.9 bergantung pada unduhan toolchain otomatis.

## ADR-052 — Errata AF-01..AF-08 (pasca-freeze)

**Status:** Accepted 2026-10-18 (keputusan user). Menutup AF-01..AF-08 ([`REVIEW-1.0.md`](REVIEW-1.0.md) §"Verifikasi ADR-046"), backlog ADR-047 butir 2, TS-P01. Usulan ADR-E01..E08 di `docs/F0-REPORT.md`. Syarat ADR-047 "AF-01/AF-02 sebelum F4" terpenuhi.

**Keputusan** (masing-masing dibuktikan skenario spike F0; teks SPEC/SELF-HEALING terdampak dibaca bersama ADR ini):

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

**Konteks.** E-06 melarang modul gateway mengimpor `ctxdb`, tetapi protokol poller/worker/outbox/approval (SELF-HEALING §5.1, SPEC §11.11.10) memerlukan transaksi `ctxdb`. Spike F0 menulisnya sebagai implementasi referensi di `internal/ctxdb/gateway_ref_test.go` dan `approval_ref_test.go` di atas `Tx` publik.

**Keputusan.**
1. **Logika protokol** gateway dan approval (urutan poller → worker, spool, outbox, konsumen approval) milik paket `internal/gateway` (F4).
2. `ctxdb` hanya menyediakan **primitif storage**: tabel spool/outbox/approval/inbound dan metode transaksional bertipe. `internal/gateway` mengonsumsinya lewat interface kecil yang dideklarasikan di sisi konsumen (E-03, E-09, ≤ 5 method); `cmd/damnbot` yang menyambungkan. Adapter platform (`gateway/<platform>`) tetap di bawah E-06.
3. File `*_ref_test.go` tetap **test-only** sebagai oracle skenario sampai F4 memindahkan logikanya ke `internal/gateway`; setiap file diberi komentar penunjuk ke ADR ini.

**Dampak terukur (SPEC §20).** Token/latensi/binary/dependency 0. LOC: protokol dihitung di budget `gateway` SPEC §3.2.4 (bukan `ctxdb`). Arah import §2.2 tidak berubah.

**Konsekuensi.** (+) `ctxdb` tetap sempit; invarian ditegakkan guard DB apa pun pemanggilnya. (−) F4 harus memindahkan dan menyesuaikan implementasi referensi.

## ADR-054 — Errata pasca-freeze: klarifikasi + perbaikan DDL V-13/V-15

**Status:** Accepted 2026-10-18 (keputusan user). Menutup usulan ADR-E09, E11, E13, E14, E15, E17, E18, E19, E21, E22, E23, E24, E27, E28, E29, E30, E31, E32 (`docs/F0-REPORT.md` §"Errata backlog ADR-047"), backlog ADR-047 butir 2. V-13/V-15 mengubah DDL (fail-closed, normalisasi FTS) atas persetujuan eksplisit user; item lain yang mengubah perilaku/budget/invarian dipisah sebagai ADR-055..061 (Proposed).

**Aturan penerapan.** ADR ini normatif untuk bagian terdampak. Teks yang sudah diterapkan: DDL SPEC §11 + `internal/ctxdb/migrations` (V-13, V-15) dan ENGINEERING E-19/E-39/§8 (V-10, V-20, V-29). Bagian lain dibaca dengan ADR ini sebagai pengganti teks lama.

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

**Konsekuensi.** (+) 18 dari 25 MINOR non-AF (V-06..V-31 kecuali V-12) tertutup; 7 sisanya = ADR-055..061 (Proposed). Dua lubang DDL fail-open/inkonsisten diperbaiki dengan test. (−) Teks lama di beberapa dokumen pendamping masih harus dibaca bersama ADR ini.

## ADR-055 — Starlark: allowlist, `timeout_s`, mekanisme macOS (V-07)

**Status:** Proposed 2026-10-18 — mengubah budget/perilaku; menunggu auditor. Usulan ADR-E10.

**Konteks.** SPEC §9.2 mengizinkan "tool deferred/inti yang diizinkan approval", sedangkan EXEC-AND-CODING §B.7/COST-10 hanya 7 tool; batas waktu ≤ 30 s vs schema `timeout_s` 1–300; macOS `ulimit -v` vs `RLIMIT`.

**Keputusan (usulan).** §9.2 merujuk allowlist 7 tool §B.7; `timeout_s` Starlark ≤ 30; mekanisme macOS disamakan; "sama dengan" → "mekanisme sama, cap 256 MiB".

**Konsekuensi.** Mempersempit permukaan tool dan batas waktu (perubahan perilaku).

## ADR-056 — Literal baris overflow katalog tool (V-09)

**Status:** Proposed 2026-10-18 — menetapkan output golden model-facing; menunggu auditor. Usulan ADR-E12.

**Keputusan (usulan).** Satu literal di SPEC §5.2 (mis. `- +<K> more groups: tool_search`) dirujuk P1/PROV-02/ADR-035; pengurutan alfabetis berlaku selalu, bukan hanya saat overflow.

**Konsekuensi.** Golden prompt bergantung pada literal yang dipilih.

## ADR-057 — Kriteria salvage `gateway_inbound`/`gateway_deliveries` (V-14)

**Status:** Proposed 2026-10-18 — mendefinisikan perilaku recovery baru; menunggu auditor. Usulan ADR-E16.

**Keputusan (usulan).** `gateway_inbound`: insert dengan dedupe lalu UPDATE `event_id` NULL→nilai (sah menurut guard). `gateway_deliveries`: status "terjauh" menang (sent/uncertain atas queued), selaras GAP-REL-10.

**Konsekuensi.** Salvage menjadi deterministik untuk tabel mutable.

## ADR-058 — Satu rumus estimator token (V-18)

**Status:** Proposed 2026-10-18 — mengubah perhitungan budget; menunggu auditor. Usulan ADR-E20.

**Keputusan (usulan).** SPEC §4.5 merujuk COST-09 sebagai satu-satunya rumus; default rasio sesi tanpa ledger = turunan per-script; `bytes/3,6` dihapus.

**Konsekuensi.** Angka budget/compaction berubah untuk sesi tanpa ledger.

## ADR-059 — Daftar frasa tertunda SPEC §20 (V-23)

**Status:** Proposed 2026-10-18 — menetapkan isi gate dokumen; menunggu auditor. Usulan ADR-E25.

**Keputusan (usulan).** Daftar literal, case-sensitive (mis. `Usulan:`, `Usulan resolusi:`, `diterapkan saat implementasi`, `menunggu persetujuan`, `TBD`); "kandidat freeze (menunggu verifikasi …)" di baris Status/§20 bukan frasa tertunda.

**Konsekuensi.** Gate dokumen menjadi deterministik; isi daftar menentukan dokumen mana yang merah.

## ADR-060 — Enkripsi bundle diagnostik (V-24)

**Status:** Proposed 2026-10-18 — memilih perilaku user-visible; menunggu auditor/user. Usulan ADR-E26.

**Keputusan (usulan, pilih satu).** (a) default zip lokal ter-redaksi `0600`, `--upload` = enkripsi ke kunci maintainer; atau (b) selalu terenkripsi, `--upload` dihapus/diganti nama. SPEC §10 dan CROSS-CUTTING §1.7 disamakan.

**Konsekuensi.** Menentukan kontrak privasi bundle.

## ADR-061 — Himpunan EC-PLAT Telegram untuk gate F4 (V-31)

**Status:** Proposed 2026-10-18 — mengubah cakupan gate F4; menunggu auditor. Usulan ADR-E33.

**Keputusan (usulan, pilih satu).** ROADMAP F4 menyebut ID eksplisit (mis. "EC-PLAT-001..023 kecuali 008"), atau tanda "(Telegram)" ditambahkan di 001/002/003/007 dan jumlahnya menjadi 19.

**Konsekuensi.** Empat entri Telegram berpindah dari gate F7 ke F4.
