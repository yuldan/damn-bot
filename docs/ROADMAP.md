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

Milestone **Spec 1.0 freeze**: sebelum F0 dimulai, setelah GAP-SWEEP diintegrasikan (SPEC §20).

---

## Pemetaan parity → fase

Tabel ini memetakan prefix + kata kunci fitur ke fase; karena pemetaan per kata kunci tidak bisa diturunkan mekanis untuk setiap baris, kolom `fase` per baris di FEATURE-PARITY.md adalah **deliverable F0** (diisi dari tabel ini, dicek `TestParityPhaseColumn`: 837/837 baris penyebut gate punya `fase` ∈ F1..F9 yang konsisten dengan kolom prefix tabel ini). Baris dinyatakan selesai jika test acceptance-nya lulus **dan** benchmark speed parity-nya (SCORECARD §3) memenuhi target.

| Prefix (jumlah) | F1 | F2 | F3 | F4 | F5 | F6 | F7 | F8 | F9 |
|---|---|---|---|---|---|---|---|---|---|
| CORE (21) | loop, registry/dispatch, interrupt, iteration budget, streaming, error classifier, todo, clarify, think scrubber, reasoning | toolsets → grup modul | model switch, quick commands, `/diff`, `/init`, coding posture | | relay runtime (NeMo), fast mode | | | | |
| TOOL (88) / TSET (62) | file, terminal, patch, todo, clarify | memory, session_search, skills | | send_message (Telegram), cronjob | delegate, execute_code, MCP, LSP, process | web, browser, vision, image/video, TTS/STT, computer use | platform tools, kanban, a2a | connectors, X search, Feishu, Discord tool, misc | |
| CLI (75) / SLASH (103) | `run`/oneshot | sessions, memory, skills | inti TUI/REPL, config, setup, doctor, model, profiles, update | gateway, cron, service, `send` | mcp, delegate, tools | voice | platform-spesifik, kanban | dashboard, insights, batch, plugins | sisanya |
| PROV (80) | openai-chat generik + custom | | wizard rekomendasi | | registry penuh 88 + lokal, OAuth, credential pool | | | | |
| MEM (37) | | store, FTS, resume, export, lineage, compaction, fork/rewind, checkpoints, @-ref, pin, context files, prompt caching | `/context`, `/btw` | | | image eviction | | external provider Honcho (ByteRover/RetainDB drop, NP-21), learning journey | |
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

[`EDGE-CASES-hermes.md`](EDGE-CASES-hermes.md) (166 entri) dipetakan ke fase; modul tidak dinyatakan selesai sebelum entri area terkait berstatus `covered` (test lulus) atau `n/a` (dengan alasan tertulis).

| Area | Fase |
|---|---|
| `EC-PROV` provider/streaming | F1 (openai-chat), F5 (protokol & provider lain) |
| `EC-TCALL` tool-call parsing | F1 |
| `EC-CTX` context/compaction | F2 |
| `EC-STATE` state/SQLite | F2 (multi-proses, WAL, network FS), F4 (chaos) |
| `EC-FIO` file I/O & encoding | F1 |
| `EC-TERM` terminal/proses | F1 (local), F5 (backend) |
| `EC-WIN` Windows | setiap fase di matrix CI Windows; F9 penutupan |
| `EC-PLAT` platform chat (termasuk 15 entri Telegram) | F4 (Telegram), F7 (lainnya) |
| `EC-CFG` config/startup | F3 |
| `EC-SEC` keamanan | F1 (sandbox, redaksi), F3 (import), F4 (gateway) |

---

## F0 — Harness & baseline

**Deliverable**
1. `bench/budget`: harness token P1 (o200k_base offline), dipakai CI.
2. `bench/h2h`: harness head-to-head menjalankan hermes (`868b9f244f`, container) dan Damn-Bot berdampingan dengan proxy rekam (SCORECARD §2.3).
3. Eval set terkunci E1–E10 + manifest SHA-256 (SCORECARD §2.2), daftar ID SWE-bench Verified & Terminal-Bench di-pin.
4. Baseline hermes **tanpa biaya LLM** untuk dimensi SCORECARD yang dapat diukur tanpa model: token overhead & schema bytes, startup, RSS, code tools vs `rg`, speed parity tipe (a), ukuran/waktu install, baris config & langkah setup (ADR-032, eks-Q23).
5. `bench/speed`: kerangka speed parity per baris FEATURE-PARITY (SCORECARD §3), baseline hermes untuk semua operasi tipe (a). Smoke LLM harness: 10 task (subset E1–E7) × 1 run dengan model murah, hanya untuk membuktikan harness berjalan end-to-end pada kedua agent; angkanya tidak dipakai sebagai baseline gate.
6. Korpus code tools C1–C3 ([`CODE-TOOLS.md`](CODE-TOOLS.md) §10) + baseline `rg` dan tool hermes.
7. CI skeleton: lint, size-check, LOC (`scc`), deps allowlist, govulncheck, go-licenses, meta-test gate.
8. **Persistence spike** (ADR-047): prototipe Go `ctxdb` + lease/fencing/purge/gc + test skenario #1–#86 (SPEC §11.11.13 #1–#78, REVIEW-1.0 §"Verifikasi ADR-046" #79–#86) + chaos kill -9 + `TestMultiProcess`.
9. **Errata backlog ADR-047**: semua MINOR terbuka ditutup lewat ADR errata; AF-01 dan AF-02 wajib sebelum implementasi gateway (F4).

**Acceptance**
- Harness mereproduksi angka BASELINE-hermes.md (overhead per call, schema bytes) dalam ±5%.
- Setiap gate CI terbukti gagal pada pelanggaran sintetis (meta-test: 1 kasus per gate, 100% terdeteksi).
- Baseline hermes tersedia untuk 100% dimensi SCORECARD tipe tanpa LLM; dimensi berbasis LLM ditandai "diukur di RC F9" di artefak.
- Speed baseline hermes untuk 100% baris parity tipe (a) yang dapat dieksekusi tanpa kredensial berbayar; sisanya didaftar dengan alasan.
- Smoke LLM 10 task selesai untuk kedua agent dengan rekaman proxy lengkap (0 error harness).
- Persistence spike: 86/86 skenario adversarial lulus sebagai test otomatis; chaos kill -9 dan `TestMultiProcess` hijau.
- Errata backlog ADR-047: 100% item punya ADR penutup + test.
- **Gate sebelum F1:** review penuh area non-persistence oleh auditor selesai; temuan BLOCKER/MAJOR ditutup lewat ADR.

## F1 — Core

**Deliverable**: `internal/{loop, provider (openai-chat), netx, schema, tools, codetools, exec (local, PTY, process group), sandbox, redact, budget (ledger dasar), ctxdb (0001 core: events/blobs/ledger)}`, `damnbot run` headless, 8 tool inti + spill, approval dasar, stop reason lengkap.

**Acceptance**
- P1 ≤ 5.000 tok (dengan tool inti saja: ≤ 4.000), P2, P3, P4, P5, P6, P9 (events), P14, P17 hijau di CI.
- [`CODE-TOOLS.md`](CODE-TOOLS.md): grep literal ≤ 1,2× `rg`, regex ≤ 1,5× `rg`, edit suite CT-20 ≥ 325 kasus 100% / 0 salah lokasi.
- [`EXEC-AND-CODING.md`](EXEC-AND-CODING.md): spawn overhead p95 < 15 ms; 0 orphan & 0 hang pada suite 200 skenario.
- EC-TCALL, EC-FIO, EC-TERM (local), EC-PROV (openai-chat) covered.
- Speed parity (a) untuk baris F1 ≥ target SCORECARD §3.2.

## F2 — Context DB, memori, modul lazy

**Deliverable**: `ctxdb` lengkap (migrasi 0001–0007, FTS5, leases, crash recovery), `assemble` (tier), `compaction`, `memory` terstruktur + karantina, `skills` retrieval, `recall`, fork/rewind, `modules` (registry lazy + host API) + `tool_search`/`tool_invoke`, `journal` (action journal + undo, SPEC §18.1), budget guard harian/bulanan/provider (SPEC §18.4), opsi enkripsi at-rest + keychain.

**Acceptance**
- P8, P9, P10, P15, P16, P19 hijau; recall E4 ≥ 90% (k=10) dengan model referensi.
- Undo: 100% aksi file pada suite undo (≥ 100 skenario) dapat dibatalkan dan menghasilkan byte identik.
- `recall` ≤ 50 ms p95 @ 1.000.000 event (di luar purge berjalan; selama purge + filter `purging` dan jendela pemeliharaan, SPEC §11.6/§17.1); `Store.Append` ≤ 10 ms p99 (synchronous=FULL; di luar jendela pemeliharaan gc/purge SPEC §11.6, selama batch +≤ 200 ms; `BenchmarkAppend`, `BenchmarkAppendDuringGC`).
- EC-CTX, EC-STATE (non-chaos) covered.

## F3 — Permukaan & plug-and-play

**Deliverable**: `tui`, `acp`, `config`, `damnbot setup` (rekomendasi provider dari kredensial), plan mode (SPEC §18.3), preview aksi berbahaya di TUI (SPEC §18.2), slash/CLI inti, `importer` (`damnbot import hermes`: config, secrets → keychain, skills, memory, MCP, cron), profiles, release pipeline (SBOM, ed25519, reproducible), self-update + rollback, `LICENSE` MIT.

**Acceptance**
- P12 (≤ 100 ms), P13 (build `minimal` ≤ 22 MB), P17 first token TUI ≤ +30 ms.
- ACP conformance transcript golden lulus; `tern acp` manual smoke terdokumentasi (Q1/Q3).
- Wizard ≤ 3 langkah, ≤ 60 s; config minimum 2 baris.
- Import 5 fixture `~/.hermes`: ≥ 99% item, 0 kehilangan diam-diam, idempoten.
- EC-CFG covered.

## F4 — Telegram first + ketahanan

**Deliverable**: `gateway` core (pairing, allowlist default deny, peran owner/user, isolasi sesi & memori per user/chat, rate limit, outbox, inbound dedupe, approval & preview via inline keyboard), `gateway/telegram` kualitas tertinggi (long polling + webhook, rate-limit aware, media, inline keyboard, edit-stream, topic/thread, reconnect, offset persisten), `damnbot serve`, `service install` (systemd/launchd/Windows), `heal` ([`SELF-HEALING.md`](SELF-HEALING.md): supervisor, watchdog, turn journal, repair, safe mode), `cron` (catch-up policy), job **scorecard mingguan** (SPEC §18.5) dengan alarm Telegram.

**Acceptance**
- E8: orang asing → 0 tool call, 0 LLM call; approval inline keyboard default timeout = reject.
- Chaos suite (kill -9 × 1.000 turn, network cut, disk penuh, config/DB korup, reboot VM): 0 korupsi, 0 pesan Telegram masuk hilang/diproses ganda, 0 duplikat keluar pada suite, recovery ≤ 10 s, 0 tool non-idempoten terulang tanpa konfirmasi (P22).
- EC-PLAT Telegram (15 entri) + EC-STATE chaos covered.
- RSS `serve` + Telegram + cron ≤ 90 MB; CPU idle ≤ 0,5% (x86).

## F5 — Provider, MCP, delegasi, coding

**Deliverable**: `provider/registry` deklaratif ([`PROVIDERS.md`](PROVIDERS.md): 88 provider + 6 server lokal + custom 12 field) di atas 4 adapter (`openai-chat`, `openai-responses`, `anthropic-messages`, `gemini`) + transport/auth (SigV4, Google JWT, OAuth device/PKCE, `key_cmd`), credential pool, auto-discover `/models`; `mcp` (client + server + OAuth + sampling, 65 MCPX); `delegate` (budget dijumlah ke induk); `exec` backends (docker, ssh, modal, daytona, singularity, vercel_sandbox); `execute_code` (Python/JS/Go); `codeintel` (go/ast, tree-sitter wazero, LSP, repo map); loop verifikasi coding (`verify_on_stop`).

**Acceptance**
- ≥ 88 provider lulus fixture replay; smoke nyata untuk ≥ 10 provider utama; custom provider dari 12 field berjalan tanpa kode.
- 65/65 MCPX `tools/list` sukses; P1 tetap ≤ 5.000 dengan 65 server tercache.
- Smoke E1 10 task dengan model murah lulus harness end-to-end (tanpa gate vs hermes; perbandingan kualitas penuh baru di RC F9, ADR-032).
- EC-PROV (semua), EC-TERM (backend) covered.

## F6 — Web, browser, media, voice

**Deliverable**: `web` (11 backend), `browser` (CDP sendiri + engine lokal/Camofox/Lightpanda/cloud, real profile opt-in), `media` (vision dengan resize preventif, image gen 8 backend, video gen 4 backend, video analyze), `voice` (STT 8 provider + whisper.cpp, TTS 11 provider + streaming, voice mode, wake word via engine eksternal), `computer-use`.

**Acceptance**
- MEDIA 29/31 (Discord voice & silk di F7); 0 penolakan ukuran gambar pada suite 50 gambar; resize 12 MP ≤ 150 ms.
- Voice overhead end-to-end ≤ 200 ms; TTS streaming mulai ≤ 300 ms setelah kalimat pertama.
- Smoke E3 10 task dengan model murah lulus harness (perbandingan vs hermes di RC F9).

## F7 — Platform lain, API server, kanban

**Deliverable**: 26 adapter (A2A … Yuanbao; WhatsApp Web via klien protokol `damnbot.bridge/v1` ke bridge eksternal `damnbot-wa-bridge` (ADR-034; bridge dirilis terpisah dengan lisensinya sendiri), `--accept-ban-risk`, inbound-only, cold outbound dilarang), `api_server` (OpenAI-compatible), webhook + subscriptions, MS Graph webhook, peer gateways, hosted rooms, relay, profile routing, scale-to-zero, Discord voice (Q7), silk audio, `kanban` (dispatcher, watchers, decompose, dashboard data).

**Acceptance**
- PLAT 30/30 lulus fake server; smoke nyata nightly untuk adapter yang punya akun uji.
- Kontribusi binary adapter ≤ budget SPEC §3.2.4; P13 full ≤ 52 MB.
- EC-PLAT (non-Telegram) covered.

## F8 — Web UI, ekosistem, otomatisasi

**Deliverable**: `web` UI (pengganti Electron + dashboard, auth, 18 bahasa), connectors (MS Graph, Feishu, Google Meet Q9, Teams pipeline, X search, Discord tool, OSV), skills hub (4 sumber) + katalog 211 skill, memory provider Honcho (ByteRover/RetainDB drop, NP-21), `auto` (semua fitur AUTO default off + budget), `i18n`, `batch`/evals/trajectory, OTLP, pemetaan plugin-catalog (Q18).

**Acceptance**
- UI 28/28, AUTO 32/32, SKILL/SKCAT, PLUG lulus test parity.
- P11: config default → 0 call `purpose=auto`; setiap fitur auto berhenti tepat di budget.
- Web UI first token ≤ +50 ms; aset ≤ 1,5 MB; 0 key i18n hilang.

## F9 — Hardening & rilis 1.0

**Deliverable**: penutupan EC-WIN & sisa edge-case, OS matrix lengkap (CROSS-CUTTING §7), soak 24 jam + 1.000 turn, chaos penuh, **head-to-head penuh pertama** (SWE-bench Verified subset 100 × 3 run + E2–E10, model frontier dan anggaran ditetapkan user saat RC — Q23-RC), aktivasi scorecard mingguan, gate parity 854 baris, import sesi hermes, Docker 1 stage, Nix flake, installer, Termux, dokumentasi migrasi.

**Acceptance (gate 1.0)**
1. SCORECARD §4: semua dimensi ≥ hermes, dimensi inti menang dengan margin, speed parity (a) ≥ 5×/10× tanpa satu operasi pun lebih lambat, (b) median ≥ 2× tanpa task > ×1,10.
2. FEATURE-PARITY: 100% baris non-`dropped-with-approval` lulus test + benchmark speed.
3. EDGE-CASES: 166/166 `covered` atau `n/a` beralasan; GAP-SWEEP: 118/118 requirement SPEC §22.1 terverifikasi (dengan resolusi ADR-033).
4. Invariant P1–P29 hijau pada commit rilis.
5. Artefak scorecard RC dipublikasikan.


## Adopsi GAP-SWEEP (118 gap → scope 1.0)

Semua gap [`GAP-SWEEP.md`](GAP-SWEEP.md) menjadi requirement 1.0 (SPEC §22); gap yang menyentuh beberapa fase tercantum di tiap fase. Acceptance tiap fase bertambah: **semua gap berfase Fx lulus verifikasi di SPEC §22.1** (P0/P1 wajib di fase modulnya; P2 paling lambat F8/F9). Gate F9 bertambah: 118/118 gap terverifikasi dengan resolusi ADR-033/ADR-034.

| Fase | Jumlah gap | ID (requirement SPEC §22.1) | Estimasi LOC Go tambahan |
|---|---:|---|---|
| F1 | 20 | GAP-SEC-02, GAP-SEC-11, GAP-SEC-12, GAP-SEC-14, GAP-SEC-16, GAP-REL-01, GAP-REL-06, GAP-REL-07, GAP-REL-15, GAP-COST-02, GAP-COST-07, GAP-COST-08, GAP-COST-15, GAP-PROV-02, GAP-TOOL-03, GAP-TOOL-04, GAP-OPS-01, GAP-OPS-02, GAP-INF-05, GAP-INF-07 | termasuk di budget modul SPEC §3.2.4 |
| F2 | 17 | GAP-SEC-01, GAP-SEC-15, GAP-SEC-26, GAP-REL-01, GAP-REL-03, GAP-REL-04, GAP-REL-05, GAP-COST-01, GAP-COST-02, GAP-COST-04, GAP-COST-09, GAP-PROV-01, GAP-PROV-02, GAP-TOOL-10, GAP-INF-01, GAP-INF-02, GAP-INF-06 | termasuk di budget modul SPEC §3.2.4 |
| F3 | 20 | GAP-REL-13, GAP-GW-07, GAP-PROV-04, GAP-PROV-05, GAP-TOOL-01, GAP-TOOL-10, GAP-UX-01, GAP-UX-02, GAP-UX-03, GAP-UX-04, GAP-UX-05, GAP-UX-06, GAP-OPS-01, GAP-OPS-02, GAP-OPS-04, GAP-OPS-06, GAP-OPS-07, GAP-OPS-08, GAP-OPS-09, GAP-INF-04 | termasuk di budget modul SPEC §3.2.4 |
| F4 | 20 | GAP-SEC-13, GAP-SEC-27, GAP-REL-02, GAP-REL-08, GAP-REL-09, GAP-REL-10, GAP-REL-11, GAP-REL-12, GAP-REL-14, GAP-REL-17, GAP-GW-02, GAP-GW-03, GAP-GW-04, GAP-GW-05, GAP-GW-06, GAP-COST-06, GAP-COST-13, GAP-COST-14, GAP-COST-16, GAP-PROV-03 | termasuk di budget modul SPEC §3.2.4 |
| F5 | 17 | GAP-SEC-03, GAP-SEC-04, GAP-SEC-18, GAP-SEC-20, GAP-SEC-21, GAP-SEC-22, GAP-SEC-23, GAP-SEC-24, GAP-SEC-25, GAP-REL-14, GAP-REL-18, GAP-COST-03, GAP-COST-10, GAP-PROV-01, GAP-TOOL-04, GAP-TOOL-05, GAP-TOOL-08 | termasuk di budget modul SPEC §3.2.4 |
| F6 | 9 | GAP-SEC-05, GAP-COST-05, GAP-COST-11, GAP-COST-12, GAP-TOOL-01, GAP-TOOL-02, GAP-TOOL-06, GAP-TOOL-07, GAP-TOOL-09 | termasuk di budget modul SPEC §3.2.4 |
| F7 | 10 | GAP-SEC-10, GAP-REL-14, GAP-GW-01, GAP-GW-03, GAP-GW-04, GAP-GW-05, GAP-GW-08, GAP-GW-09, GAP-GW-10, GAP-GW-11 | termasuk di budget modul SPEC §3.2.4 |
| F8 | 17 | GAP-SEC-06, GAP-SEC-07, GAP-SEC-08, GAP-SEC-09, GAP-SEC-17, GAP-REL-14, GAP-REL-16, GAP-GW-07, GAP-GW-12, GAP-PROV-05, GAP-UX-07, GAP-UX-08, GAP-UX-09, GAP-UX-10, GAP-INF-06, GAP-INF-08, GAP-INF-09 | termasuk di budget modul SPEC §3.2.4 |
| F9 | 7 | GAP-SEC-19, GAP-SEC-22, GAP-SEC-28, GAP-OPS-03, GAP-OPS-05, GAP-INF-03, GAP-INF-10 | termasuk di budget modul SPEC §3.2.4 |

Dampak LOC: rencana tunggal 184.900 = Σ budget modul SPEC §3.2.4 (≤ 190.000, P21); JS ≈ 9.900 (≤ 10.000); dependency langsung 26 ≤ 28 (ADR-033, ADR-034, koreksi ENGINEERING E-45).

---

## Pasca-1.0 (P1 backlog, bukan gate rilis)

| Item | Ringkas | Syarat masuk |
|---|---|---|
| Replay sesi langkah demi langkah | Putar ulang event transcript di TUI/web dengan kontrol langkah, diff, dan state konteks per iterasi | ADR + dampak LOC ≤ 2.000 |
| Model racing (opt-in) | Kirim turn ke 2 model paralel, pakai yang pertama valid; biaya ganda tercatat di ledger | Default off, budget guard wajib |
| Telegram Mini App | Web UI ringkas di dalam Telegram (sesi, approval, budget) via Web App API | Auth via `initData` HMAC |
| Sinkron antar perangkat terenkripsi E2E | Replikasi event append-only antar instance, kunci per user, server relay buta | Desain hash-chain kompatibel; ADR keamanan |
