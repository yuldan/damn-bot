# CROSS-CUTTING — area kualitas lintas modul

Bagian dari Spec 1.0 (dirujuk dari SPEC.md §21). Setiap area: mekanisme, target terukur, verifikasi. Baris pembanding ada di [`SCORECARD.md`](SCORECARD.md) dan [`MITIGATION-MATRIX.md`](MITIGATION-MATRIX.md).

---

## 1. Keamanan menyeluruh

### 1.1 Prompt injection dari tool/web/file/chat
- Semua konten dari luar sesi user dibungkus `<untrusted source="…">` (SPEC §14.5): `web_fetch`, `web_search`, browser, MCP result, memory provider eksternal, lampiran/forward chat, isi file yang dibaca dari luar workspace (`file:outside_workspace`), skill hub sebelum approve; ditambah AGENTS.md/context file workspace untrusted (`workspace`) dan output shell berflag `network`/`outside_read` (`shell:network`, `shell:outside_workspace`) (ADR-069).
- Stable tier rule 7: konten `<untrusted>` adalah data. Penanda tidak bisa dipalsukan: runtime meng-escape string `</untrusted` di dalam konten (`<\/untrusted`).
- Taint lengket per turn (ADR-070): sumber = setiap `tool_result` ber-`<untrusted>` (termasuk file di luar root dan file workspace untrusted) dan output shell berflag `network`/`outside_read`. Aktif sejak `tool_result` sumber di-commit ke konteks turn sampai `turn_end`, untuk semua iterasi berikutnya dalam turn itu; `user_message` berikutnya memulai turn tanpa taint. Selama aktif, setiap aksi non-read (shell selain `read_only` murni di dalam root tanpa flag, termasuk `shell_allow` dan verify; `write_file`/`edit_file`; HTTP mutating; `send_message`; `browser_eval`; MCP non-read; `delegate`; `execute_code`) butuh approval di semua mode termasuk `auto`, tanpa `allow_session`; headless tanpa approver → `denied`. Penegakan di langkah 6a "Taint gate" §4.3 (`ApprovalRequest.tainted`; dicatat di `approvals.source = 'taint'`).
- Classifier shell (ADR-069): `read_only` = allowlist positif argv0 + flag (di luar tabel minimal `mutating`; menambah entri = ADR); operand path setiap simple command di-resolve terhadap root dan `deny_paths` (cocok `deny_paths` → `denied_path`, tidak bisa di-approve); interpreter dengan program dari stdin/pipe/heredoc/`-c` non-literal dan `source` → `unknown`; sesi persisten yang mengubah `PATH`/alias/fungsi dll. → `tainted_session`; `git push`, publish, HTTP mutating/metadata, `ssh`, package manager sistem → `irreversible`; sudo → `privileged`, tanya per perintah di semua mode. `shell_allow` hanya menaikkan `mutating` → auto.
- Workspace trust (ADR-069): root dibuka pertama kali secara interaktif → prompt "trust workspace <path>?", disimpan di `security.trusted_workspaces`; headless `run` untrusted kecuali terdaftar. Workspace untrusted: AGENTS.md/context file = data `<untrusted source="workspace">`, konten file men-taint turn, verify-auto dan `shell_allow` mati, `git` read tidak `read_only`, hook proyek tidak berjalan.
- Baca di luar root (ADR-069): tool file dan shell `outside_read` → interaktif tanya "read outside workspace: <path>" (`allow_session` hanya path persis), headless → `outside_workspace`; `deny_paths` tetap error.
- Target: E10 (100 kasus) tingkat serangan berhasil ≤ 2% dan ≤ 0,3× hermes. Verifikasi: `bench/h2h` E10 (RC), `TestTaintForcesApproval` (wajib mencakup aksi di iterasi N+1 setelah `web_fetch` di iterasi N dan reset pada turn baru, ADR-070), `TestShellNetworkOutputUntrusted`, `TestClassifyCorpus` (EXC-13), `TestWorkspaceTrustGate`, `TestReadOutsideWorkspaceAsks` (PR).

### 1.2 Secret vault + redaksi
- Credential (API key provider, token platform, OAuth refresh token) disimpan di OS keychain: Secret Service (Linux, via D-Bus), Keychain (macOS), Credential Manager (Windows) melalui library pure Go ([`ENGINEERING.md`](ENGINEERING.md) §4). Fallback tanpa keychain (server headless): file `<config_dir>/secrets.enc` XChaCha20-Poly1305, kunci dari passphrase (scrypt N=2^17) atau `DAMNBOT_SECRETS_KEY`; tanpa keduanya → error eksplisit, tidak pernah plaintext.
- Config hanya menyimpan referensi: `api_key = "keychain:openrouter"` atau `api_key_env = "OPENROUTER_API_KEY"`.
- Redaksi (SPEC §14.3) diterapkan **sebelum** disimpan ke DB/blob/log, sebelum dikirim ke model, dan pada pesan keluar gateway. Pola tambahan (ADR-071): blok PEM `-----BEGIN … PRIVATE KEY-----` … `END` diganti seluruhnya (tanpa END → redaksi sampai akhir chunk/blob), Stripe, Google `AIza…`, GitLab `glpat-`, Hugging Face `hf_`, npm `npm_`, JWT, dan password userinfo URL (mis. `DATABASE_URL`).
- Redaksi stream (ADR-071): penyaring hanya menahan sufiks terpanjang yang masih bisa menjadi awal pola atau nilai secret yang dikenal (batas per pola); chunk tanpa kecocokan parsial dikirim segera (0 latensi tambahan, P17); `Bearer \S+` ditahan sampai terminator atau 4 KiB lalu diredaksi fail-closed; setelah `BEGIN … PRIVATE KEY` output ditahan sampai END atau akhir stream.
- Sentinel `[REDACTED:` di `old_string`/`new_string`/`new_text`/`write_file.content` → `invalid_args` (ADR-071). Log DEBUG request/response = metadata tanpa body; body hanya bila `log.message_content = true` (TTL P29) (ADR-071).
- Env anak (ADR-071): allowlist GAP-SEC-02 (deny-by-default) + `exec.env_allow` (nama persis var induk, tanpa glob) + `exec.env` (nilai yang di-set untuk anak); kunci provider hanya diwariskan bila tercantum eksplisit di `exec.env_allow`.
- Target: 0 credential plaintext di direktori data/config (scan pola + nilai keychain); 0 secret dari korpus 60 lolos ke 6 kanal. Verifikasi: `TestNoPlaintextCredentials`, `TestRedaction` (korpus turunan rule gitleaks ter-pin + 60 secret sintetis + korpus negatif ≥ 200, ADR-071), `TestStreamRedactSplit`, `TestEnvAllowlist`, `TestLogNoMessageContent` (PR).

### 1.3 Enkripsi data at-rest (opsional)
- `storage.encrypt = true`: kolom `events.content`, `events.payload`, `summaries.content`, `memory.content`, `reasoning_carriers.payload` (PROV-01), `gateway_inbound.payload` (antrean inbound, ADR-046), spool inbound `run/inbound_spool/` (per baris, kunci yang sama), dan isi blob dienkripsi XChaCha20-Poly1305 dengan DEK per DB; DEK dibungkus KEK dari keychain.
- FTS memakai blind index: token di-HMAC-SHA256 (kunci turunan), sehingga hanya pencarian token utuh (tanpa prefix/stemming). Default off (ADR-032 Q25); `doctor` merekomendasikan full-disk encryption bila off.
- Target: overhead `Store.Append` dengan enkripsi ≤ +2 ms p99; `recall` ≤ 80 ms p95 @ 1.000.000 event. Verifikasi: `BenchmarkAppendEncrypted`, `BenchmarkRecallBlindIndex` (nightly).

### 1.4 Supply chain
- Dependency langsung ≤ 28 (ADR-033), semua di-pin (`go.sum`), `go mod verify` di CI, allowlist `deps.allow`; penambahan butuh ADR (SPEC P21).
- `govulncheck` per PR + harian: 0 temuan reachable di semua severity (gate, [`ENGINEERING.md`](ENGINEERING.md) E-54); `go-licenses` hanya MIT/BSD/Apache-2.0/ISC; MPL-2.0 hanya untuk modul terisolasi tanpa modifikasi (`mautrix`, `go.mau.fi/util`; `whatsmeow`/`libsignal` GPL-3.0 tidak di-link, ADR-034); zlib/GPL/AGPL/LGPL ditolak ([`ENGINEERING.md`](ENGINEERING.md) E-40).
- SBOM CycloneDX per rilis (`cyclonedx-gomod`), dilampirkan di release.
- Build reproducible: `-trimpath`, `SOURCE_DATE_EPOCH`, versi Go di-pin; dua builder independen di CI harus menghasilkan SHA-256 identik.
- Verifikasi: job `supply-chain` (PR), `reproducible-build` (RC).

### 1.5 Rilis ditandatangani
- Setiap artefak: `SHA256SUMS` + tanda tangan ed25519 (minisign-compatible); kunci publik di-embed di binary untuk `damnbot update`.
- Kunci privat offline/HSM; rotasi lewat rilis bertanda kunci lama + baru.
- Signing OS (Apple notarization, Authenticode) bergantung Q27.
- Verifikasi: `TestUpdateRejectsBadSignature`, `TestUpdateRejectsChecksumMismatch` (PR).

---

### 1.6 Backup terenkripsi (GAP SEC-01)

`damnbot sessions backup`/`restore`: arsip selalu terenkripsi (XChaCha20-Poly1305; kunci dari passphrase scrypt atau recipient X25519, `golang.org/x/crypto`), dibuat `O_EXCL` mode 0600 di dir 0700, fsync + rename atomik, default `<data_dir>/backups/`; kunci vault tidak pernah satu arsip dengan ciphertext-nya tanpa enkripsi luar. Requirement & verifikasi: SPEC §22.1 baris SEC-01.

### 1.7 Support bundle tanpa pastebin (GAP SEC-17)

`damnbot doctor bundle [--out P]` default menulis **zip lokal ter-redaksi** (redaksi SPEC §14.3 + PII: ID, nama, isi pesan) dengan mode file `0600` (temp `0600` lalu `rename`; Windows: ACL hanya user), tidak terenkripsi; `--upload` mengenkripsi bundle yang sama ke kunci publik maintainer (X25519, embedded) dan menyimpan file terenkripsi untuk dilampirkan manual; tanpa `--no-redact`; 0 host upload/pastebin di kode (ADR-060). Requirement & verifikasi: SPEC §22.1 baris SEC-17.

### 1.8 Dokumentasi keamanan & privasi (GAP SEC-28)

Requirement & verifikasi: SPEC §22.1 baris SEC-28.

## 2. Multi-agent / subagent

- Mekanisme: SPEC §9.3 (`delegate_task`). Budget anak = slice eksplisit dari sisa budget **induk**; jumlah slice aktif ≤ sisa induk; usage anak dijumlahkan ke `v_turn_usage` induk sehingga cap turn/sesi/harian berlaku pada total.
- Hasil anak ke induk ≤ 2.000 char (`answer`) + fakta runtime; transcript anak penuh via `recall`.
- Isolasi opsional `git worktree` per anak; perubahan dikembalikan sebagai diff/branch, merge hanya atas permintaan.
- Maks 3 anak paralel, kedalaman 1, iterasi ≤ 40 per anak.
- Target: Σ usage anak ≤ budget induk pada 100% skenario; 0 anak yatim setelah cancel induk. Verifikasi: `TestDelegateBudgetSum`, `TestDelegateCancelPropagates` (PR).

## 3. Model routing & caching

- **Prompt caching**: OpenAI-compatible implicit prefix cache dijaga oleh P15; Anthropic `cache_control` di 3 batas (akhir stable+katalog, akhir context tier, akhir summaries); Gemini context caching untuk sesi ≥ 32K tok bila preset mendukung.
- Hit rate dihitung per call dari `cached_input_tokens/input_tokens`, ditampilkan di panel budget dan `damnbot usage --cache`.
- Target: hit rate ≥ 0,70 pada sesi ≥ 5 call (provider dengan cache). Verifikasi: SCORECARD; `TestCacheBreakpointsAnthropic` (PR, fixture).
- **Routing model ringan (opt-in)**: role `light` (`[provider.roles] light = "provider/model"`) untuk judul, klasifikasi smart approval, insights. Default kosong = fitur yang butuh role ini nonaktif; tidak pernah dipakai untuk turn utama tanpa perintah user. Setiap call tercatat `purpose=auto`, `feature`, model (P11).

## 4. Model lokal & offline

- Server lokal (Ollama, llama.cpp, vLLM, LM Studio, LiteLLM; [`PROVIDERS.md`](PROVIDERS.md) "Provider lokal") lewat adapter `openai-chat`; `/v1` ditambahkan otomatis bila tidak ada (pelajaran hermes #106010, PROVIDERS.md).
- Deteksi otomatis hanya saat `damnbot setup`/`doctor`: probe `127.0.0.1` port 11434, 8080, 8000, 1234, 4000 (timeout 150 ms, paralel, total ≤ 300 ms) + `GET /v1/models`.
- Mode offline `--offline` / `[network] offline = true`: semua dial non-loopback ditolak di `internal/netx` (error `offline_mode`), modul yang butuh jaringan berstatus `unavailable` dan tidak muncul di katalog deferred.
- Target: sesi penuh (coding E2 subset) berjalan dengan model lokal tanpa jaringan; 0 koneksi non-loopback. Verifikasi: `TestOfflineNoEgress` (PR, network namespace), smoke nightly dengan Ollama.

## 5. Observability & biaya

- Ledger `usage_ledger` per call (token, cache, biaya, sumber biaya, attempts, TTFT); agregasi per sesi/turn/tool/provider/fitur.
- Dashboard: panel TUI, halaman web UI (grafik harian, per model, per tool), `damnbot usage --by day|model|feature|tool --format table|jsonl`.
- Setiap stop/retry/fallback/compaction memiliki alasan terstruktur (event + log) yang dapat dicari: `damnbot sessions why <turn-id>` menampilkan rantai keputusan.
- Export: `damnbot sessions export --format jsonl` (event mentah), `damnbot usage --format jsonl`.
- Telemetri hanya opt-in: OTLP export ke endpoint milik user (`[telemetry] otlp_endpoint`), default kosong = 0 koneksi (Q21).
- Target: biaya turn di UI = Σ ledger (selisih 0); `damnbot sessions why` tersedia untuk 100% turn. Verifikasi: `TestUsageMatchesLedger`, `TestWhyExplainsAllStops` (PR).

## 6. Update & rilis

- Kanal `stable` / `beta` (`[update] channel`), cek manual atau harian di `serve` (tanpa auto-install kecuali `auto_install = true`).
- Self-update atomik: unduh → verifikasi ed25519 + SHA-256 → tulis `damnbot.new` → rename atomik (Windows: rename `damnbot.exe` → `damnbot.old.exe` lalu pasang) → simpan `damnbot.prev` → health check (start + `doctor --quick` ≤ 30 s) → gagal = rollback otomatis ([`SELF-HEALING.md`](SELF-HEALING.md)).
- Kompatibilitas data: migrasi maju bernomor (SPEC §11.1) dengan backup `VACUUM INTO` sebelum migrasi; binary lama menolak DB lebih baru dengan pesan "jalankan `damnbot update --rollback --data` untuk memulihkan backup pra-migrasi". `damnbot update --rollback [--data]` = binary `prev` + backup DB pasangannya.
- Versi semver; perubahan format config/DB/protokol hook = minor dengan migrasi otomatis; penghapusan = major.
- Target: 0 rilis gagal tanpa rollback otomatis pada suite update (20 skenario kegagalan). Verifikasi: `TestUpdateRollback*` (PR/nightly).

## 7. Lintas platform

Daftar target rilis kanonik = ENGINEERING E-39 (8 target, `CGO_ENABLED=0 -tags goolm`); tabel ini merinci CI per OS.

| Target | Arch | CI | Catatan |
|---|---|---|---|
| Linux | amd64, arm64, arm (armv7) | build + unit + DB + exec + perf (amd64); arm: build + unit di QEMU | static, musl tidak diperlukan |
| macOS | arm64, amd64 | unit + DB + exec | launchd |
| Windows | amd64, arm64 | unit + DB + exec (ConPTY, Job Object) | Windows Service / Task Scheduler |
| Android (Termux) | arm64 | build + unit di QEMU + smoke | parity OPS-031 |

Target perangkat lemah (Raspberry Pi 4, 4 GB): startup ≤ 300 ms, RSS idle TUI ≤ 40 MB, CPU idle `serve` (web + 3 adapter + cron) ≤ 2%, overhead iterasi p95 ≤ 60 ms. Verifikasi: job `perf-arm` (nightly, runner ARM fisik/QEMU dengan koreksi).

## 8. Stabilitas API ekstensi (hook/MCP)

- Kontrak versi: hook `damnbot.hook/v1` (field `version` di payload; v1 dipertahankan minimal 2 minor setelah v2 rilis); `modules/host` API internal tidak publik.
- MCP client & server penuh: versi protokol dinegosiasi, `tools/list` cache, OAuth, sampling opt-in (SPEC §9.6); ACP `protocolVersion` 1.
- Isolasi: hook & MCP stdio = proses terpisah dengan timeout (hook 5 s; MCP call default 60 s, `tools/list` 10 s), memori dibatasi (Linux `prlimit` RLIMIT_AS 1 GiB default), crash → event `module_error`, core tetap jalan.
- Target: 0 crash core pada suite 30 ekstensi rusak (crash, hang, output sampah, OOM). Verifikasi: `TestExtensionIsolation` (PR).

## 9. UX & onboarding

- First-run: `damnbot` tanpa config → wizard otomatis (≤ 3 langkah, ≤ 60 s): deteksi kredensial dari env, keychain, `~/.hermes`, `~/.omp/agent` (baca saja, dengan izin) → tampilkan provider yang siap pakai + rekomendasi ([`PROVIDERS.md`](PROVIDERS.md): auto-pilih dari kredensial, referensi `provider/model`, tanpa model hardcoded) → test call → simpan.
- Pesan error: katalog ber-ID (`DB-E012`), selalu memuat penyebab, perbaikan, dan perintah (`run: damnbot doctor --fix`); tidak ada stack trace di UI (ada di log).
- `damnbot doctor [--fix]` (SPEC §10, SELF-HEALING.md §8).
- Target: wizard ≤ 60 s (median 10 pengguna uji, skrip terstandar); error informatif ≥ 95% (suite 60). Verifikasi: SCORECARD §1.4, `TestErrorCatalogComplete` (setiap error yang mencapai UI punya ID katalog).

## 10. Disiplin engineering

Detail di [`ENGINEERING.md`](ENGINEERING.md). Ringkas:
- Ownership modul via `CODEOWNERS`; review wajib 1 approver; conventional commits; `CHANGELOG.md` diperbarui setiap PR yang mengubah perilaku.
- Budget LOC & ukuran per modul (SPEC §3.2.4) di-gate CI.
- Test berfokus perilaku; rasio test:source 0,8–1,2 (hermes 1,48×).
- Perubahan default perilaku atau invariant → ADR wajib dengan dampak token/latensi/LOC/ukuran binary (SPEC §20).
- Target: churn `fix` < 40% dari commit 90 hari setelah 1.0 (hermes 63%); diukur `git log` bulanan (laporan, bukan gate).

## 11. Lisensi

- Damn-Bot: MIT, "Copyright (c) 2026 Damn-Bot contributors" (`LICENSE`; ADR-021).
- Pemakaian ulang aset hermes (MIT, "Copyright (c) 2025 Nous Research"): skill, string i18n, pola threat, preset provider, prompt — wajib atribusi di `THIRD_PARTY_NOTICES.md` + header asal per file + daftar di `docs/ATTRIBUTION.md`; kode Python tidak diterjemahkan baris-per-baris (ADR-021).
- Dependency: allowlist lisensi [`ENGINEERING.md`](ENGINEERING.md) E-40 (MIT/BSD/Apache-2.0/ISC; MPL-2.0 hanya modul terisolasi).
- Verifikasi: `TestAttributionHeaders`, `go-licenses check` (PR).
