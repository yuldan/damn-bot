# ROADMAP — Damn-Bot 1.0 (parity penuh)

Rilis 1.0 = **feature parity penuh** dengan hermes-agent ([`FEATURE-PARITY.md`](FEATURE-PARITY.md), 854 baris) + fitur pembeda (SPEC §18) + **menang di semua dimensi** [`SCORECARD.md`](SCORECARD.md). Tidak ada rilis 1.0 parsial; fase F0–F8 menghasilkan build internal/beta.

Prinsip urutan: ukur dulu (F0) → core yang benar (F1–F2) → permukaan (F3) → kanal utama user, Telegram, + ketahanan (F4) → kelompok fitur parity (F5–F8) → hardening & gate (F9).

## Ringkasan

| Fase | Fokus | LOC Go non-test | LOC lain | Dependensi fase |
|---|---|---:|---:|---|
| F0 | Harness: token, head-to-head, eval set, baseline hermes, speed parity, CI gate | 4.500 (tooling, di luar P21) | data eval | — |
| F1 | Core loop, provider openai-chat, schema gate, code tools, exec runner, spill, retry, keamanan dasar | 29.500 | — | F0 |
| F2 | Context DB, memori, skill, compaction, fork/rewind, modul lazy, journal/undo, budget guard | 12.000 | — | F1 |
| F3 | TUI, ACP, config, setup wizard, plan mode, preview, import hermes (config/skill/memory/MCP), rilis & update | 21.500 | — | F2 |
| F4 | Gateway core + **Telegram**, `serve`, service install, self-healing, cron, scorecard mingguan | 20.000 | — | F3 |
| F5 | Registry provider (88 + lokal + custom), MCP, delegate, exec backends, execute_code, codeintel, verifikasi coding | 30.500 | — | F4 |
| F6 | Web, browser, media, voice, computer use | 24.500 | — | F5 |
| F7 | 29 surface platform lain, api-server, webhook, kanban, fitur gateway lanjutan | 25.100 | — | F4, F5 |
| F8 | Web UI, connectors, skills hub, memory providers, `auto`, i18n, batch/evals, ops lanjutan | 21.800 | ≈ 9.900 JS/CSS (≤ 10.000, budget web UI SPEC §3.2.4) | F5–F7 |
| F9 | Hardening, edge-case suite penuh, OS matrix, soak/chaos, gate parity + scorecard, rilis 1.0 | 0 (perbaikan hardening diserap budget modul) | docs | F0–F8 |
| **Total** | | **184.900** = Σ budget modul SPEC §3.2.4 = Σ kolom fase di atas (pemetaan modul → fase = kolom Fase §3.2.4; ≤ 190.000, P21) | ≈ 9.900 (≤ 10.000) | |

LOC test non-generated: rasio 0,8–1,2× source ([`ENGINEERING.md`](ENGINEERING.md) §6), ≈ 150.000–225.000 (dari 184.900 source). Pembanding hermes: 972.645 LOC Python + 1.442.817 LOC test (BASELINE §1).

Milestone **Spec 1.0 freeze**: sebelum F0, setelah ADR-033..036 terintegrasi dan errata diverifikasi auditor (SPEC §20, ADR-030); terpenuhi oleh ADR-047 (V-22/ADR-054).

---

## Pemetaan parity → fase

Tabel ini memetakan prefix + kata kunci fitur ke fase. Kolom `fase` per baris di FEATURE-PARITY.md (deliverable F0) kini ada (ADR-064, commit `09660fe`): diisi dari tabel ini dan dicek `TestParityPhaseColumn` (837/837 baris penyebut gate punya `fase` ∈ F1..F9 yang konsisten dengan kolom prefix tabel ini; 17 baris dropped bernilai `—`). Jumlah baris per fase (ADR-064; dikoreksi ADR-139 untuk 22 baris F2): F1 46, F2 59, F3 135, F4 56, F5 201, F6 86, F7 101, F8 148, F9 5 (Σ 837). Baris dinyatakan selesai jika test acceptance-nya lulus **dan** benchmark speed parity-nya (SCORECARD §3) memenuhi target.

| Prefix (jumlah) | F1 | F2 | F3 | F4 | F5 | F6 | F7 | F8 | F9 |
|---|---|---|---|---|---|---|---|---|---|
| CORE (21) | loop, registry/dispatch, interrupt, iteration budget, streaming, error classifier, todo, clarify, think scrubber, reasoning | toolsets → grup modul | model switch, quick commands, `/diff`, `/init`, coding posture | | relay runtime (NeMo), fast mode | | | | |
| TOOL (88) / TSET (62) | file, terminal, patch, todo, clarify | memory, session_search, skills | | send_message (Telegram), cronjob | delegate, execute_code, MCP, LSP, process | web, browser, vision, image/video, TTS/STT, computer use | platform tools, kanban, a2a | connectors, X search, Feishu, Discord tool, misc | |
| CLI (75) / SLASH (103) | `run`/oneshot | sessions, memory, skills | inti TUI/REPL, config, setup, doctor, model, profiles, update | gateway, cron, service, `send` | mcp, delegate, tools | voice | platform-spesifik, kanban | dashboard, insights, batch, plugins | sisanya |
| PROV (80) | openai-chat generik + custom | | wizard rekomendasi | | registry penuh 88 + lokal, OAuth, credential pool | | | | |
| MEM (37) | | store, FTS, resume, export, lineage, compaction, fork/rewind, checkpoints, @-ref, pin, context files, prompt caching | `/context`, `/btw` | | native compaction provider (MEM-022) | image eviction | | external provider Honcho (ByteRover/RetainDB drop, NP-21), learning journey | |
| SKILL (23) / SKCAT (35) | | sistem skill, retrieval, karantina, ledger/provenance, guard | slash skill, `/learn`, `/refine` | | | | | hub (4 sumber), curator, katalog 58 + 153 skill | |
| AUTO (32) | empty-response guard, loop guardrails, auto-recovery | | plan mode, queue/steer, `/bg` | cron (scheduler, delivery, script jobs, monitor, preflight), periodic scheduler | review subagent, MoA | | kanban (dispatcher, decompose, watchers, swarm) | background review, title, curator scheduler, idle compaction, goals, loops, heartbeat, blueprints, suggestions, outbound webhooks, disk cleanup, maintenance | |
| UI (28) | stream-JSON, oneshot | | TUI, ACP, skins, status bar, palette, bang shell, editor, notify, completion, onboarding, tips | | MCP server | voice UI | API server | web dashboard + auth, desktop (web UI), i18n, pets, personalities, proxy | installer |
| PLUG (36) / MCPX (65) | | tool search/deferred | hooks (shell hooks), plugin CLI → `damnbot config hooks` | gateway hooks | MCP client/OAuth/sampling/catalog/scan, 65 MCPX | browser/image/video/computer-use backends | platforms | connectors, dashboard_auth, achievements, security-guidance, disk-cleanup, teams_pipeline, google_meet, plugin catalog mapping (Q18) | |
| MEDIA (31) | | | | | | semua kecuali Discord voice & silk | Discord voice (Q7), silk audio | | |
| EXEC (44) | local, PTY, sudo, approval modes, dangerous detection, allowlist, guardrails, path/URL safety, redaction, env passthrough | | | gateway approval | docker/ssh/modal/daytona/singularity/vercel, env registry, worktree, delegation, LSP, egress firewall, OSV, vault (local, 1Password, Bitwarden), secret sources | browser engines, computer use, web backends, website blocklist | | | |
| OPS (38) | logging | backup/import | profiles, config, setup, doctor, status, usage/pricing, prompt size, self-update, channels, uninstall, verify, Claude Code/Codex/OpenClaw import | service manager, startup watchdog, install repair, emergency stop, data cleanup | | | | insights, OTLP, batch, trajectory, evals, mini SWE, projects, debug dump | Docker, Nix, Termux, installer scripts |
| PLAT (30) | | | | Telegram (PLAT-022) | | | 26 adapter lain + api_server, webhook, msgraph_webhook | | |
| GW (26) | | | | runtime, registry, pairing, access, home channel, streaming edits, human delay, loop guard, delivery ledger, topics, timestamps, footer, restart/drain, pause/resume, send CLI, chat CLI, managed bot | | | webhook subscriptions, MS Graph, peer gateways, hosted rooms, relay, multiplex, scale-to-zero, channel directory, handoff | | |

NP-01..NP-21 diputuskan per ADR-022 (Accepted) dan ADR-033 (NP-21): sebagian `drop`, sebagian `keep` (NP-06, NP-10, NP-17, NP-20, sebagian NP-18). Yang berkeputusan drop **dan** punya baris parity = 17 baris `dropped-with-approval` (NP-02/07/15/16/19/21); NP drop lain tidak punya baris parity atau hanya menghapus alias (baris tetap `planned`). Pemetaan NP → ID baris di FEATURE-PARITY.md (tabel NP + kolom `status`).

## Regression suite dari EDGE-CASES

[`EDGE-CASES-hermes.md`](EDGE-CASES-hermes.md) (166 entri) dipetakan ke fase; modul tidak dinyatakan selesai sebelum entri area terkait berstatus `covered` (test lulus) atau `n/a` (dengan alasan tertulis). ADR-077: satu fase per entri, yaitu fase pemilik komponen yang dibutuhkan untuk mereproduksinya (entri yang butuh komponen F2+ bukan F1). `edgecases.json` mendukung status `pending:<Fx>`; item `pending` tidak menghasilkan subtest (tanpa `t.Skip`, REL-15). Semantik gate PR (`TestEdgeCaseCatalogSync`, `TestCLIDocsMatchHelp`, dan gate inkremental lain, ADR-080): item dengan fase **<** isi file `PHASE` wajib `covered` (subtest ada dan lulus); fase **=** `PHASE` boleh `pending:<PHASE>` atau `covered` (bila subtest ada, wajib lulus); fase **>** `PHASE` wajib `pending`. Job acceptance fase `make phase-acceptance` (dijalankan saat menaikkan `PHASE` dari Fx ke Fx+1 dan di setiap PR yang mengubah file `PHASE`): semua item fase ≤ Fx wajib `covered`, PR penaikan gagal bila tidak; himpunan wajibnya karena itu sama dengan gate PR, yaitu fase **<** isi `PHASE` (ADR-176); di `PHASE = F9` tidak boleh ada `pending`. Sinkronisasi katalog: himpunan ID katalog = ID subtest ∪ ID berstatus `pending`, keduanya saling lepas; `n/a` beralasan dihitung `covered` (ADR-080).

| Area | Fase |
|---|---|
| `EC-PROV` provider/streaming (30) | F1: 001, 004..007, 009..013, 015, 016, 025, 028..030 (16); F2: 008; F5: 002, 003, 014, 017..023, 026, 027 (12); F6: 024 (ADR-077) |
| `EC-TCALL` tool-call parsing (12) | F1: 001..003, 005..009, 012 (9); F2: 010; F5: 004, 011 (ADR-077) |
| `EC-CTX` context/compaction | F2 |
| `EC-STATE` state/SQLite (16) | F2: 001, 002, 003, 005..011, 013..016 (14); F4: 012; F7: 004 (ADR-080) |
| `EC-FIO` file I/O & encoding (19) | F1: 001, 002, 004..006, 008, 009, 011..017, 019 (15); F3: 007, 018; F5: 003, 010 (ADR-077) |
| `EC-TERM` terminal/proses (10) | F1: 002, 006, 007 (3); F4: 008..010; F5: 001 (butuh proses background, ADR-072), 003; F7: 004, 005 (ADR-077, ADR-080) |
| `EC-WIN` Windows (17) | F1: 001, 009, 010, 012, 013, 014, 016, 017 (8); F2: 002, 005, 011; F4: 003, 004, 006; F5: 007, 008, 015. Dijalankan di runner Windows matrix CI (F1 #0) dan masuk acceptance fasenya; F9 hanya untuk sisa yang belum `covered` karena bug, bukan fase default (ADR-080) |
| `EC-PLAT` platform chat (19 entri Telegram: EC-PLAT-001, 002, 003, 007, 009..023; ADR-061) | F4 (Telegram), F7 (lainnya: EC-PLAT-004, 005, 006, 008, 024; ADR-061) |
| `EC-CFG` config/startup | F3 |
| `EC-SEC` keamanan (8) | F1: 001, 005, 007, 008 (4); F5: 002..004; F7: 006 (ADR-077) |

---

## F0 — Harness & baseline

**Deliverable**
1. `bench/budget`: harness token P1 (o200k_base offline), dipakai CI.
2. `bench/h2h`: harness head-to-head dengan proxy rekam (SCORECARD §2.3) untuk hermes (`868b9f244f`) dan Damn-Bot. ADR-075: di F0/F1 run berurutan per agent; run berpasangan dengan urutan diacak dan container wajib untuk RC F9.
3. Eval set terkunci E1–E10 + manifest SHA-256 (SCORECARD §2.2), daftar ID SWE-bench Verified & Terminal-Bench di-pin.
4. Baseline hermes **tanpa biaya LLM** untuk dimensi SCORECARD yang dapat diukur tanpa model: token overhead & schema bytes, startup, RSS, code tools vs `rg`, speed parity tipe (a), ukuran/waktu install, baris config & langkah setup (ADR-032, eks-Q23).
5. `bench/speed`: kerangka speed parity per baris FEATURE-PARITY (SCORECARD §3), baseline hermes untuk semua operasi tipe (a). Smoke LLM harness: 10 task (subset E1–E7) × 1 run dengan model murah, hanya untuk membuktikan harness berjalan end-to-end; di F0 dijalankan untuk hermes, sisi Damn-Bot di F1 (ADR-062); angkanya tidak dipakai sebagai baseline gate.
6. Korpus code tools C1–C3 ([`CODE-TOOLS.md`](CODE-TOOLS.md) §10) + baseline `rg` dan tool hermes.
7. CI skeleton: lint, size-check, LOC (`scc`), deps allowlist, govulncheck, go-licenses, meta-test gate.
8. **Persistence spike** (ADR-047): prototipe Go `ctxdb` + lease/fencing/purge/gc + test skenario #1–#86 (SPEC §11.11.13 #1–#78, REVIEW-1.0 §"Verifikasi ADR-046" #79–#86) + chaos kill -9 + `TestMultiProcess`.
9. **Errata backlog ADR-047**: semua MINOR terbuka ditutup lewat ADR errata; AF-01 dan AF-02 wajib sebelum implementasi gateway (F4).

**Acceptance**
- Harness mereproduksi angka BASELINE-hermes.md (overhead per call, schema bytes) dalam ±5%.
- Setiap gate CI terbukti gagal pada pelanggaran sintetis (meta-test: 1 kasus per gate, 100% terdeteksi).
- Baseline tanpa LLM (ADR-065): **setiap** dimensi SCORECARD tanpa LLM punya status eksplisit di tabel ADR-065, yaitu `F0` (angka di artefak bernama) atau `Fx` (fase pengukuran + alasan; sisi hermes diukur di fase itu dengan harness yang sama untuk kedua agent, wajib ada sebelum acceptance fase tersebut). Dimensi berbasis LLM ditandai "diukur di RC F9" di artefak (ADR-032).
- Speed baseline hermes untuk 100% baris parity tipe (a) yang dapat dieksekusi tanpa kredensial berbayar, diproduksi `cd bench && go run ./speed/cmd/speed` (artefak `bench/speed/results/hermes-868b9f244f.json`, ADR-077); sisanya didaftar dengan alasan. `BenchmarkSpeedParity/<ID>` (V-20/ADR-054) dibuat di F1 (ADR-077).
- Smoke LLM 10 task selesai untuk hermes dengan rekaman proxy lengkap (0 error harness); terpenuhi oleh `bench/h2h` 2026-10-18. Sisi Damn-Bot pindah ke acceptance F1 (ADR-062).
- Persistence spike: 86/86 skenario adversarial lulus sebagai test otomatis; chaos kill -9 dan `TestMultiProcess` hijau.
- Errata backlog ADR-047: 100% item punya ADR penutup + test.
- **Gate sebelum F1:** review penuh area non-persistence oleh auditor selesai; temuan BLOCKER/MAJOR ditutup lewat ADR.

## F1 — Core

**Deliverable F1 #0 (CI hardening, ADR-073)** — wajib merge sebelum paket F1 pertama; setiap job baru punya ≥ 1 kasus di `scripts/ci-meta-test.sh`:
- matrix test ubuntu/macos/windows (E-55; 2 QEMU tetap di F9), build-matrix 8 target kanonik (E-39), fuzz per target yang ada (`FuzzSSEParse`, `FuzzToolArgs`, `FuzzEditApply`, …), coverage E-58, `deadcode` (3 GOOS), flake nightly `-count=20`;
- `pr-meta` + adrgate (PR yang mengubah `deps.allow`, `loc.budget`, default config §12, atau SPEC §2/§3.2.4/§17 wajib mengubah `docs/DECISIONS.md`), CHANGELOG, ukuran PR; `.github/CODEOWNERS` + `TestCodeownersComplete` (E-79);
- job `perf` (`-run '^$' -bench`, runner tetap, ambang ×1,5 hanya untuk waktu absolut, ADR-077) untuk `BenchmarkShellSpawn*`, `BenchmarkClassify`, `BenchmarkIterationOverhead`, dan benchmark codetools;
- helper budget waktu pindah dari `internal/ctxdb/timing_test.go` ke paket non-test `internal/testutil/timing` (build tag `!race`, baris `loc.budget` sendiri, ≤ 80 LOC) sebelum paket non-ctxdb pertama membutuhkan asersi waktu;
- meta-test gate test/race/timing diuji pada pohon minimal (`./internal/metaviolation/...`);
- gate arsitektur sebelum `loop`/`netx`/`provider` di-merge: `TestImportGraph` (E-01/E-02/E-06/E-08), analyzer `nohttpclient` (E-05), `TestFileLength` (E-14/E-71), `TestGlobalAllowlist` (E-10), `TestGoleakMain` (E-56), `TestBudgetTableSum` (P21);
- workflow `meta` (ADR-085) berjalan di **setiap** PR dan push `main`: job menghitung file berubah (`git diff --name-only` terhadap base) dan menjalankan `make meta-test` bila ada path gate yang berubah (`Makefile`, `scripts/**`, `.github/**`, `.golangci.yml`, `loc.budget`, `deps.allow`, `deps.mpl`, `go.mod`, `go.sum`, `tools/go.mod`, `tools/go.sum`, `internal/archtest/**`, `internal/testutil/**`, `internal/metaviolation/**`, `internal/ctxdb/timing_test.go`, `internal/ctxdb/race_*_test.go`, `internal/analyzers/**`), selain itu lulus dengan pesan "no gate paths changed"; status selalu terlapor dan menjadi required check (E-78); nightly menjalankan semua gate tanpa syarat;
- file `PHASE` di root repo berisi `F1` (gate inkremental ADR-077) dengan semantik ADR-080: item fase < `PHASE` wajib `covered`, fase = `PHASE` boleh `pending:F1` atau `covered`, fase > `PHASE` wajib `pending`, sehingga gate hijau sebelum paket F1 ditulis; job `make phase-acceptance` (semua item fase ≤ Fx `covered`) dijalankan saat menaikkan `PHASE` dan di PR yang mengubah file `PHASE`;
- item backlog berlabel "Deliverable #0" (ADR-083 butir 5): ADR-078 AUD-056, AUD-057, AUD-059, AUD-067, AUD-069, AUD-070, AUD-071, AUD-072, AUD-073, AUD-074, AUD-083 (AUD-064 sudah ditutup ADR-086); ADR-086 NEW-32, NEW-33, NEW-34, NEW-35, NEW-36, NEW-38.

Gate yang ditunda punya fase eksplisit (ADR-073): `TestNoCrossPackageEmbedding` F2 (bersama `modules`), `TestNolintDensity` acceptance F1.

**Deliverable**: `internal/{loop, provider (openai-chat), netx, schema, tools, codetools, exec (local, PTY, process group), sandbox, redact, budget (ledger dasar + estimator token COST-09, ADR-067), ctxdb (0001 core: events/blobs/ledger)}`, `assemble` minimal (tier stable/context/volatile; tanpa katalog, retrieval memori, dan skill) dan `config` minimal (struct + validasi + default SPEC §12 untuk kunci yang dipakai paket F1), keduanya di dalam core F1 18.000 (ADR-066), `damnbot run` headless, 8 tool inti + spill, approval dasar, stop reason lengkap termasuk `content_filtered` (ADR-072), driver `damnbot run` untuk `bench/h2h` (ADR-075). Proses background tidak ada di F1 (ADR-072): `shell{background:true}` → `invalid_args`; background, `process_*`, dan pemindahan foreground → background datang di F5 (TOOL-077/EXEC-009), bagian test P24/EXC-09/EXC-11 yang butuh `process_*` pindah ke acceptance F5; registry proses internal (untuk `still_running`) tetap F1.

Storage ledger `budget` F1 = subpaket `internal/ctxdb/ledgerstore` (ADR-074, baris `loc.budget` 300) dan termasuk deliverable F1; benchmark budget absolutnya ikut job `perf` (`-tags goolm,perf`, ADR-086 NEW-38).

**Acceptance**
- Deliverable F1 #0 hijau dan setiap job barunya terdeteksi meta-test; `TestNolintDensity` hijau (ADR-073). Gate bernama (NEW-32): `TestImportGraph`, `TestFileLength`, `TestGlobalAllowlist`, `TestGoleakMain` (= `goleak.VerifyTestMain` di `TestMain` setiap paket yang meluncurkan goroutine), `TestBudgetTableSum`, `TestCodeownersComplete`, `TestWorkflowsPinned`, `TestMPLIsolation`, analyzer `nohttpclient`; daftar job ↔ kasus meta-test di [`F1-REPORT.md`](F1-REPORT.md) (ADR-094).
- `make phase-acceptance` hijau saat `PHASE` dinaikkan ke fase berikutnya; item yang sudah `covered` lebih awal dari fasenya (fase > `PHASE`) diizinkan (ADR-091 NEW2-22). Bullet yang sama berlaku di acceptance setiap fase F2–F9.
- Matrix Windows Deliverable F1 #0 menjalankan `go test ./internal/ctxdb/` hijau, termasuk `TestFileLockSemantics`, S023, S044, dan `TestPlatformNAList`; kegagalan probe Windows di runner = perbaikan F1 #0, bukan skip (ADR-090).
- P1 fase F1 = fixture `f1-core` ≤ 4.000 tok o200k_base atas JSON request (bukan usage provider), lewat hook golden `TestFixedOverheadGolden` (`internal/assemble`) + `TestFixedOverhead/f1-core` (modul `bench`, job `bench-budget`), total dan tiap tier ≤ alokasi P1 (ADR-066); P2, P3, P4, P5, P6, P9 (events), P14, P17 hijau di CI. `TestTokenEstimatorCalibration` korpus teks en/id/ru/ja hijau (ADR-067).
- ADR-077: P7 (`TestFallbackVisible` untuk `run` headless); P26 bagian approval (`TestIrreversibleNeedsApproval`, `TestShellIrreversibleAlwaysAsk`, `TestAgentBypassFlagsAlwaysAsk`; subset SEC-14 F1 = sudo → prompt per perintah lewat approver palsu ber-preview di paket test, tanpa approver → `denied`, ADR-084); P29 subset ADR-068; `TestTaintForcesApproval`, `TestRedaction`, `TestStreamRedactSplit`, `TestFirstTokenLatency/redact` (penyaring aktif, P17, ADR-081); suite sandbox CODE-TOOLS §8 termasuk `TestSymlinkEscapeRace` (CT-26); `TestDestructiveAlwaysAsk` (EXC-14); `TestSpecDocs` (`TestSpecNoPendingAmendments`, `TestCodeToolsIDsComplete`) + `TestCLIDocsMatchHelp` + kasus meta-test gate dokumen.
- Model approval shell ADR-079 (menggantikan tabel dan test classifier ADR-069): `TestShellApprovalModel` (36 baris tabel ADR-079), `TestShellAllowExactMatch` (≥ 60 kasus; + CR, NUL, karakter kontrol, `.bat`/cwd Windows, ADR-088), `TestShellAllowCwdTracked`, `TestShellNoClassifierAuto` (EXC-13: ≥ 300 perintah korpus, mode `ask`/`auto-edit`/`auto`, `shell_allow` kosong → 0 dieksekusi tanpa approval), `TestUnsafeAutoShellFlag`, `TestVerifyNeedsShellAllow`, `TestShellOutputUntrustedWorkspace`, `TestCodeToolsSymlinkOutsideRoot`, `TestNoAutoClassReadonlyDecision` (`approvals.decision = 'auto_class_readonly'` dicadangkan, tidak pernah ditulis); `exec.allow_destructive`, `exec.destructive_allow`, dan `smart_approval` tidak berlaku untuk `shell` (ADR-079 butir 9–10); test ADR-069 yang tetap berlaku: `TestSudoAlwaysAsk`, `TestWorkspaceTrustGate`, `TestReadOutsideWorkspaceAsks`. ADR-088: `TestShellNoAllowSession` (`allow_session` tidak berlaku untuk shell; ACP `allow_always` → `allow_once`), `TestSecretRefScope` (secret ref `exec.env` hanya pada mode execve runner untuk argv0 ∈ `commands`) + kasus scope di EXC-15 `TestEnvAllowlist`.
- P15 subset ADR-067/082: `TestStableTierGolden` (varian), `TestPromptNoUnavailableTools`, `TestVolatileTierCaps`, `TestPrefixStableWithinTurn`, `TestRuntimeSuffixCaps`, `TestShellSchemaVariant`.
- [`CODE-TOOLS.md`](CODE-TOOLS.md): P23 = semua CT kecuali CT-08, CT-12, CT-13, CT-24, CT-28 (butuh `codeintel`/`codeindex`, F5) (ADR-077); grep literal ≤ 1,2× `rg`, regex ≤ 1,5× `rg` (rasio diukur apa adanya di runner CI dan di mesin referensi Q14, ADR-077), edit suite CT-20 ≥ 330 kasus (ADR-071) 100% / 0 salah lokasi. CT-10 (ADR-082): `search_files` dan `read_file` ≥ 10× p50; `patch`/tulis ≥ 2× p50 terhadap baseline hermes yang diukur ulang dengan durabilitas setara (driver hermes menambahkan `fsync(file)` + `fsync(dir)`) dan ≤ 6 ms p95 absolut di mesin referensi Q14; baseline durabel diukur di F1 sebelum acceptance codetools; tidak ada operasi yang lebih lambat dari hermes.
- [`EXEC-AND-CODING.md`](EXEC-AND-CODING.md) P24 (ADR-077): `TestNoOrphans` (200 iterasi), `TestInteractiveCorpus` (40 fixture × pipe/pty), `BenchmarkShellSpawn*` (spawn overhead p95 < 15 ms diukur di mesin referensi Q14; CI menegakkan ×1,5 di PR), `TestCancelConfirmed`, `TestCancelConfirmBeforeRedispatch` (REL-13 di F3; Windows di matrix).
- Entri EC berfase F1 di tabel regression suite covered (EC-PROV 16, EC-TCALL 9, EC-FIO 15, EC-TERM 3, EC-SEC 4; ADR-077, ADR-080) dan EC-WIN-001, 009, 010, 012, 013, 014, 016, 017 di runner Windows (ADR-080).
- Speed parity (a): `BenchmarkSpeedParity/<ID>` (`bench/speed`) untuk setiap baris FEATURE-PARITY berfase F1 (ADR-064) ≥ target SCORECARD §3.2 vs `bench/speed/results/hermes-868b9f244f.json`; sampel CLI ≥ 20 run per sisi, baseline hermes baris F1 diukur ulang dengan 20 run sebelum acceptance (ADR-077).
- Smoke LLM Damn-Bot (ADR-062): run pertama `damnbot run` pada 10 task smoke F0 yang sama (seleksi `h2h-smoke/v1`) lewat `bench/h2h`, rekaman proxy lengkap, 0 error harness; pass/fail informatif. Prasyarat (ADR-075, ADR-085): hardening `bench/h2h` ada di kode beserta test-nya sebelum run pertama, termasuk gate bernama `TestAgentIsolation` (uid agent dalam user namespace ≠ uid proxy, file kunci tidak ada setelah proxy membaca, env agent ⊆ allowlist) dan `TestRecordingsNoKey` (rekaman ter-commit dipindai pola token generik; bila `H2H_KEY_FILE` diset, juga literal/base64/base64url/hex/potongan 12 char kunci). Cap biaya LLM F1 total US$2,00, ditegakkan ledger tunggal `bench/h2h/results/f1-spend.json` (konstanta `h2h.F1LedgerPath`, di-commit, cap tersimpan di file) yang dipakai `cmd/h2h` dan `cmd/startup` tanpa flag path lain; setiap `Recorder` berkunci upstream wajib punya `Ledger`; pembaruan read-modify-write di bawah `flock` eksklusif, fail-closed tanpa `flock` (ADR-085).
- Test tertunda berfase F1 (ADR-083): `TestConfigDefaultsMatchSpec` (kunci F1); test ADR-075 `TestOracleDamnbotToolNames`, `TestUnknownRuleKindFails`, `TestToolCallsFromFinalResponse`, `TestAgentIsolation`, `TestRecordingsNoKey`.
- Baseline hermes untuk dimensi ADR-065/083 berstatus F1 diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).
- Backlog ADR-078/ADR-086 berfase F1 tertutup (ADR-083).

## F2 — Context DB, memori, modul lazy

**Deliverable**: `ctxdb` lengkap (migrasi 0001–0007, FTS5, leases, crash recovery), `assemble` (tier lengkap: katalog, retrieval memori, skill; minimal di F1, ADR-066), `compaction`, `memory` terstruktur + karantina, `skills` retrieval, `recall`, fork/rewind, `modules` (registry lazy + host API) + `tool_search`/`tool_invoke`, `journal` (action journal + undo, SPEC §18.1), budget guard harian/bulanan/provider (SPEC §18.4), formula token gambar estimator COST-09 + pemicu compaction P8 (estimator teks sudah F1, ADR-067), opsi enkripsi at-rest + keychain.

**Acceptance**
- P1 penuh ≤ 5.000 tok o200k_base atas JSON request untuk fixture `all-modules` (katalog, memori, dan skill nyata), lewat hook golden yang sama (`all-modules.request.json`, ADR-066); P8, P9, P10, P15, P16, P19 hijau; `TestTokenEstimatorCalibration` varian gambar hijau (ADR-067); recall E4 ≥ 90% (k=10) dengan model referensi.
- Undo: 100% aksi file pada suite undo (≥ 100 skenario) dapat dibatalkan dan menghasilkan byte identik.
- `recall` ≤ 50 ms p95 @ 1.000.000 event (di luar purge berjalan; selama purge + filter `purging` dan jendela pemeliharaan, SPEC §11.6/§17.1); `Store.Append` ≤ 10 ms p99 (synchronous=FULL; di luar jendela pemeliharaan gc/purge SPEC §11.6, selama batch +≤ 200 ms; `BenchmarkAppend`, `BenchmarkAppendDuringGC`).
- EC-CTX, EC-STATE-001, 002, 003, 005..011, 013..016 (14; ADR-080), EC-PROV-008, EC-TCALL-010 covered (ADR-077); EC-WIN-002, 005, 011 di runner Windows (ADR-080).
- Test tertunda berfase F2 (ADR-083): `TestModuleLifecycle/needs_attention`.
- Scope GAP F2 (ADR-129): acceptance menagih 13 gap P0/P1 yang tercantum di ADR-129; GAP-COST-04, GAP-TOOL-10, GAP-INF-02, dan GAP-INF-06 dijadwalkan ulang ke F5/F3+F5+F8/F9/F8 dan bukan gate F2. Requirement SPEC §22.1 tetap utuh untuk gate F9.
- Baseline hermes untuk dimensi ADR-065/083 berstatus F2 (termasuk Token skill per turn) diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).

## F3 — Permukaan & plug-and-play

**Deliverable**: `tui`, `acp`, `config`, `damnbot setup` (rekomendasi provider dari kredensial), plan mode (SPEC §18.3), preview aksi berbahaya di TUI (SPEC §18.2), slash/CLI inti, `importer` (`damnbot import hermes`: config, secrets → keychain, skills, memory, MCP, cron), profiles, release pipeline (SBOM, ed25519, reproducible), self-update + rollback, `LICENSE` MIT.

**Acceptance**
- P12 (≤ 100 ms), P13 (build `minimal` ≤ 22 MB), P17 first token TUI ≤ +30 ms.
- ACP conformance transcript golden lulus; `tern acp` manual smoke terdokumentasi (Q1/Q3).
- Wizard ≤ 3 langkah, ≤ 60 s; config minimum 2 baris.
- Import 5 fixture `~/.hermes`: ≥ 99% item, 0 kehilangan diam-diam, idempoten.
- Plan mode: `TestPlanModeShell` — shell hanya bila cocok persis `shell_allow` dan berlabel `non_mutating`, selain itu ditolak (ADR-079 butir 11).
- EC-CFG, EC-FIO-007, EC-FIO-018 covered (ADR-077).
- Baseline hermes untuk dimensi ADR-065/083 berstatus F3 diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).

## F4 — Telegram first + ketahanan

**Deliverable**: `gateway` core (pairing, allowlist default deny, peran owner/user, isolasi sesi & memori per user/chat, rate limit, outbox, inbound dedupe, approval & preview via inline keyboard), `gateway/telegram` kualitas tertinggi (long polling + webhook, rate-limit aware, media, inline keyboard, edit-stream, topic/thread, reconnect, offset persisten), `damnbot serve`, `service install` (systemd/launchd/Windows), `heal` ([`SELF-HEALING.md`](SELF-HEALING.md): supervisor, watchdog, turn journal, repair, safe mode), `cron` (catch-up policy), job **scorecard mingguan** (SPEC §18.5) dengan alarm Telegram.

**Acceptance**
- E8: orang asing → 0 tool call, 0 LLM call; approval inline keyboard default timeout = reject.
- Chaos suite (kill -9 × 1.000 turn, network cut, disk penuh, config/DB korup, reboot VM): 0 korupsi, 0 pesan Telegram masuk hilang/diproses ganda, 0 duplikat keluar pada suite, recovery ≤ 10 s, 0 tool non-idempoten terulang tanpa konfirmasi (P22).
- EC-PLAT Telegram (19 entri eksplisit: EC-PLAT-001, 002, 003, 007, 009..023; ADR-061) + EC-STATE-012 (ADR-080) + EC-TERM-008..010 covered (ADR-077); EC-WIN-003, 004, 006 di runner Windows (ADR-080).
- RSS `serve` + Telegram + cron ≤ 90 MB; CPU idle ≤ 0,5% (x86).
- Test tertunda berfase F4 (ADR-083): `TestBudgetGuardGatewayUser` (termasuk penolakan pra-turn untuk turn ke-201), `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope`.
- Baseline hermes untuk dimensi ADR-065/083 berstatus F4 diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).
- Backlog ADR-078/ADR-086 berfase F4 tertutup (ADR-083).

## F5 — Provider, MCP, delegasi, coding

**Deliverable**: `provider/registry` deklaratif ([`PROVIDERS.md`](PROVIDERS.md): 88 provider + 6 server lokal + custom 12 field) di atas 4 adapter (`openai-chat`, `openai-responses`, `anthropic-messages`, `gemini`) + transport/auth (SigV4, Google JWT, OAuth device/PKCE, `key_cmd`), credential pool, auto-discover `/models`; `mcp` (client + server + OAuth + sampling, 65 MCPX); `delegate` (budget dijumlah ke induk); `exec` backends (docker, ssh, modal, daytona, singularity, vercel_sandbox); `execute_code` (Python/JS/Go); `codeintel` (go/ast, tree-sitter wazero, LSP, repo map); loop verifikasi coding (`verify_on_stop`).

**Acceptance**
- ≥ 88 provider lulus fixture replay; smoke nyata untuk ≥ 10 provider utama; custom provider dari 12 field berjalan tanpa kode.
- 65/65 MCPX `tools/list` sukses; P1 tetap ≤ 5.000 dengan 65 server tercache.
- Smoke E1 10 task dengan model murah lulus harness end-to-end (tanpa gate vs hermes; perbandingan kualitas penuh baru di RC F9, ADR-032).
- Entri EC berfase F5 covered: EC-PROV 12, EC-TCALL-004/011, EC-FIO-003/010, EC-TERM-001 (ADR-080), EC-TERM-003, EC-SEC-002..004 (ADR-077); EC-WIN-007, 008, 015 di runner Windows (ADR-080); bagian test P24/EXC-09/EXC-11 yang butuh `process_*` hijau (ADR-072).
- Test tertunda berfase F5 (ADR-083): `TestMCPServeCannotSelfApprove`, `TestExecuteCodeRPCNetworkDeny`.
- ADR-089: `TestExecuteCodeApproval` (approval `execute_code` per mode × bahasa × `exec.code_auto_languages` × trust × taint; `bash` = model shell) dan `TestLSPWorkspaceTrustGate` (workspace untrusted → 0 proses LSP/build; trusted → setting aman build script/proc-macro diterapkan).
- Backlog ADR-078/ADR-086 berfase F5 tertutup (ADR-083).

## F6 — Web, browser, media, voice

**Deliverable**: `web` (11 backend), `browser` (CDP sendiri + engine lokal/Camofox/Lightpanda/cloud, real profile opt-in), `media` (vision dengan resize preventif, image gen 8 backend, video gen 4 backend, video analyze), `voice` (STT 8 provider + whisper.cpp, TTS 11 provider + streaming, voice mode, wake word via engine eksternal), `computer-use`.

**Acceptance**
- MEDIA 29/31 (Discord voice & silk di F7); 0 penolakan ukuran gambar pada suite 50 gambar; resize 12 MP ≤ 150 ms.
- Voice overhead end-to-end ≤ 200 ms; TTS streaming mulai ≤ 300 ms setelah kalimat pertama.
- Smoke E3 10 task dengan model murah lulus harness (perbandingan vs hermes di RC F9).
- EC-PROV-024 covered (ADR-077).
- Test tertunda berfase F6 (ADR-083): `TestBudgetGuardTools`.
- Baseline hermes untuk dimensi ADR-065/083 berstatus F6 diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).

## F7 — Platform lain, API server, kanban

**Deliverable**: 26 adapter (A2A … Yuanbao; WhatsApp Web via klien protokol `damnbot.bridge/v1` ke bridge eksternal `damnbot-wa-bridge` (ADR-034; bridge dirilis terpisah dengan lisensinya sendiri), `--accept-ban-risk`, inbound-only, cold outbound dilarang), `api_server` (OpenAI-compatible), webhook + subscriptions, MS Graph webhook, peer gateways, hosted rooms, relay, profile routing, scale-to-zero, Discord voice (Q7), silk audio, `kanban` (dispatcher, watchers, decompose, dashboard data).

**Acceptance**
- PLAT 30/30 lulus fake server; smoke nyata nightly untuk adapter yang punya akun uji.
- EC-PLAT (non-Telegram: EC-PLAT-004, 005, 006, 008, 024; ADR-061), EC-STATE-004 (ADR-080), EC-TERM-004/005, EC-SEC-006 covered (ADR-077).
- Kontribusi binary adapter ≤ budget SPEC §3.2.4; P13 full ≤ 52 MB.

## F8 — Web UI, ekosistem, otomatisasi

**Deliverable**: `web` UI (pengganti Electron + dashboard, auth, 18 bahasa), connectors (MS Graph, Feishu, Google Meet Q9, Teams pipeline, X search, Discord tool, OSV), skills hub (4 sumber) + katalog 211 skill, memory provider Honcho (ByteRover/RetainDB drop, NP-21), `auto` (semua fitur AUTO default off + budget), `i18n`, `batch`/evals/trajectory, OTLP, pemetaan plugin-catalog (Q18).

**Acceptance**
- UI 28/28, AUTO 32/32, SKILL/SKCAT, PLUG lulus test parity.
- P11: config default → 0 call `purpose=auto`; setiap fitur auto berhenti tepat di budget.
- Web UI first token ≤ +50 ms; aset ≤ 1,5 MB; 0 key i18n hilang.
- Backlog ADR-078/ADR-086 berfase F8 tertutup (ADR-083).

## F9 — Hardening & rilis 1.0

**Deliverable**: penutupan sisa EC-WIN yang belum `covered` karena bug (EC-WIN masuk acceptance fasenya, ADR-080) & sisa edge-case, OS matrix lengkap (CROSS-CUTTING §7), soak 24 jam + 1.000 turn, chaos penuh, **head-to-head penuh pertama** (SWE-bench Verified subset 100 × 3 run + E2–E10, model frontier dan anggaran ditetapkan user saat RC — Q23-RC), aktivasi scorecard mingguan, gate parity 837/837 baris penyebut (SPEC §8; V-28/ADR-054), import sesi hermes, Docker 1 stage, Nix flake, installer, Termux, dokumentasi migrasi.

**Acceptance (gate 1.0)**
1. SCORECARD §4 (`TestScorecardGate`, V-20/ADR-054): semua dimensi ≥ hermes, dimensi inti menang dengan margin, speed parity (a) (`BenchmarkSpeedParity/<ID>` di `bench/speed`) ≥ 5×/10× tanpa satu operasi pun lebih lambat, (b) median ≥ 2× tanpa task > ×1,10.
2. FEATURE-PARITY: 100% baris non-`dropped-with-approval` lulus test + benchmark speed.
3. EDGE-CASES: 166/166 `covered` atau `n/a` beralasan; GAP-SWEEP: 118/118 requirement SPEC §22.1 terverifikasi (dengan resolusi ADR-033).
4. Invariant P1–P29 hijau pada commit rilis.
5. Artefak scorecard RC dipublikasikan.
6. Baseline hermes untuk dimensi ADR-065/083 berstatus F9 diukur dengan harness yang sama untuk kedua agent (artefak `bench/…/results/hermes-868b9f244f-<fase>.json`) (ADR-083).


## Adopsi GAP-SWEEP (118 gap → scope 1.0)

Semua gap [`GAP-SWEEP.md`](GAP-SWEEP.md) menjadi requirement 1.0 (SPEC §22); gap yang menyentuh beberapa fase tercantum di tiap fase. Acceptance tiap fase bertambah: **semua gap berfase Fx lulus verifikasi di SPEC §22.1** (P0/P1 wajib di fase modulnya; P2 paling lambat F8/F9). Untuk gap bertanda `F1 (subset) / Fx (penuh)` di kolom Fase §22.1, acceptance F1 = cek subset dan cek penuh menjadi acceptance fase pemilik komponen (ADR-068). Gate F9 bertambah: 118/118 gap terverifikasi dengan resolusi ADR-033/ADR-034.

| Fase | Jumlah gap | ID (requirement SPEC §22.1) | Estimasi LOC Go tambahan |
|---|---:|---|---|
| F1 | 20 | GAP-SEC-02, GAP-SEC-11, GAP-SEC-12, GAP-SEC-14, GAP-SEC-16, GAP-REL-01, GAP-REL-06, GAP-REL-07, GAP-REL-15, GAP-COST-02, GAP-COST-07, GAP-COST-08, GAP-COST-15, GAP-PROV-02, GAP-TOOL-03, GAP-TOOL-04, GAP-OPS-01, GAP-OPS-02, GAP-INF-05, GAP-INF-07. Subset F1 dengan cek penuh di fase pemilik komponen (ADR-068): SEC-02, SEC-11, SEC-14, SEC-16, COST-02, PROV-02, REL-07, OPS-01, OPS-02, REL-01 | termasuk di budget modul SPEC §3.2.4 |
| F2 | 17 | GAP-SEC-01, GAP-SEC-15, GAP-SEC-26, GAP-REL-01, GAP-REL-03, GAP-REL-04, GAP-REL-05, GAP-COST-01, GAP-COST-02, GAP-COST-04, GAP-COST-09, GAP-PROV-01, GAP-PROV-02, GAP-TOOL-10, GAP-INF-01, GAP-INF-02, GAP-INF-06 | termasuk di budget modul SPEC §3.2.4 |
| F3 | 20 | GAP-REL-13, GAP-GW-07, GAP-PROV-04, GAP-PROV-05, GAP-TOOL-01, GAP-TOOL-10, GAP-UX-01, GAP-UX-02, GAP-UX-03, GAP-UX-04, GAP-UX-05, GAP-UX-06, GAP-OPS-01, GAP-OPS-02, GAP-OPS-04, GAP-OPS-06, GAP-OPS-07, GAP-OPS-08, GAP-OPS-09, GAP-INF-04 | termasuk di budget modul SPEC §3.2.4 |
| F4 | 20 | GAP-SEC-13, GAP-SEC-27, GAP-REL-02, GAP-REL-08, GAP-REL-09, GAP-REL-10, GAP-REL-11, GAP-REL-12, GAP-REL-14, GAP-REL-17, GAP-GW-02, GAP-GW-03, GAP-GW-04, GAP-GW-05, GAP-GW-06, GAP-COST-06, GAP-COST-13, GAP-COST-14, GAP-COST-16, GAP-PROV-03 | termasuk di budget modul SPEC §3.2.4 |
| F5 | 17 | GAP-SEC-03, GAP-SEC-04, GAP-SEC-18, GAP-SEC-20, GAP-SEC-21, GAP-SEC-22, GAP-SEC-23, GAP-SEC-24, GAP-SEC-25, GAP-REL-14, GAP-REL-18, GAP-COST-03, GAP-COST-10, GAP-PROV-01, GAP-TOOL-04, GAP-TOOL-05, GAP-TOOL-08 | termasuk di budget modul SPEC §3.2.4 |
| F6 | 9 | GAP-SEC-05, GAP-COST-05, GAP-COST-11, GAP-COST-12, GAP-TOOL-01, GAP-TOOL-02, GAP-TOOL-06, GAP-TOOL-07, GAP-TOOL-09 | termasuk di budget modul SPEC §3.2.4 |
| F7 | 10 | GAP-SEC-10, GAP-REL-14, GAP-GW-01, GAP-GW-03, GAP-GW-04, GAP-GW-05, GAP-GW-08, GAP-GW-09, GAP-GW-10, GAP-GW-11 | termasuk di budget modul SPEC §3.2.4 |
| F8 | 17 | GAP-SEC-06, GAP-SEC-07, GAP-SEC-08, GAP-SEC-09, GAP-SEC-17, GAP-REL-14, GAP-REL-16, GAP-GW-07, GAP-GW-12, GAP-PROV-05, GAP-UX-07, GAP-UX-08, GAP-UX-09, GAP-UX-10, GAP-INF-06, GAP-INF-08, GAP-INF-09 | termasuk di budget modul SPEC §3.2.4 |
| F9 | 7 | GAP-SEC-19, GAP-SEC-22, GAP-SEC-28, GAP-OPS-03, GAP-OPS-05, GAP-INF-03, GAP-INF-10 | termasuk di budget modul SPEC §3.2.4 |

Catatan scope (ADR-129, keputusan owner 2026-10-21): baris F2 di atas tetap peta historis 118 gap, tetapi acceptance F2 hanya menagih **13 gap P0/P1**. Empat gap P2 tidak menjadi gate F2: GAP-COST-04 → F5, GAP-TOOL-10 → F3 (updater) / F5 (MCP `requires`) / F8 (skills hub), GAP-INF-02 → F9, GAP-INF-06 → F8. Requirement SPEC §22.1 dan gate F9 118/118 tidak berubah.

Dampak LOC: rencana tunggal 184.900 = Σ budget modul SPEC §3.2.4 (≤ 190.000, P21); JS ≈ 9.900 (≤ 10.000); dependency langsung 26 ≤ 28 (ADR-033, ADR-034, koreksi ENGINEERING E-45).

---

## Pasca-1.0 (P1 backlog, bukan gate rilis)

| Item | Ringkas | Syarat masuk |
|---|---|---|
| Replay sesi langkah demi langkah | Putar ulang event transcript di TUI/web dengan kontrol langkah, diff, dan state konteks per iterasi | ADR + dampak LOC ≤ 2.000 |
| Model racing (opt-in) | Kirim turn ke 2 model paralel, pakai yang pertama valid; biaya ganda tercatat di ledger | Default off, budget guard wajib |
| Telegram Mini App | Web UI ringkas di dalam Telegram (sesi, approval, budget) via Web App API | Auth via `initData` HMAC |
| Sinkron antar perangkat terenkripsi E2E | Replikasi event append-only antar instance, kunci per user, server relay buta | Desain hash-chain kompatibel; ADR keamanan |
