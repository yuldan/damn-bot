# F3 acceptance ledger

Acceptance F3 didefinisikan di [ROADMAP.md](ROADMAP.md) §F3 (`ROADMAP.md:143-154`),
[SPEC.md](SPEC.md) P12/P13/P17 (`SPEC.md:110-115`), §9.15 (`SPEC.md:982`),
§10 (`SPEC.md:1008`), §12 (`SPEC.md:2421`), §13.1 (`SPEC.md:2533`),
§13.3 (`SPEC.md:2567`), §18.2/§18.3 (`SPEC.md:2794-2817`), §22
(`SPEC.md:2881`), [EDGE-CASES-hermes.md](EDGE-CASES-hermes.md), dan
[FEATURE-PARITY.md](FEATURE-PARITY.md). Keputusan artefak: ADR-224
(`DECISIONS.md:4688`); audit GAP: ADR-227 (`DECISIONS.md:4739`). Setiap baris
memakai bukti yang **ada dan dijalankan**, bukan nama tes yang dikarang dari
judul PR.

**Snapshot audit:** `origin/main` **`041ead4f`** (commit 2026-10-29 02:59 UTC /
09:59 +0700), tree `75fc6a56`, `PHASE` = `F3`. Toolchain
`go1.26.10 linux/amd64`, workstation (bukan mesin referensi Q14).

**Verifikasi PR.** Pada snapshot ini **0 PR terbuka**. Gelombang penutup terakhir:
`#401` (OPS-02/ADR-230, `20157f3f`), `#403` (TUI design, `ae723eda`), `#402`
(ACP resource, `386222dc`), `#407` (client fs, `c5b65870`), `#413` (`_meta`/blob,
`4963eea2`), `#414` (terminal/*, ADR-241, `e4e86a02`), lalu dokumen review
`#419`-`#427` (`041ead4f`), di atas `#395`/`#396`/`#397`/`#390`/`#382`/`#383`/
`#374` dan baseline F3 sebelumnya. ADR terakhir di `main` = **241**; PR ini menambah
**ADR-242** (smoke Zed → F4). `PHASE` tetap `F3`.

**Aturan bukti.**

| status | arti |
| --- | --- |
| `LULUS` | Tes/perintah ada, dijalankan pada snapshot ini atau CI dengan tree identik, dan lulus; lokasi dikutip. |
| `TERBUKA` | Belum ada bukti lulus; sisa kerja konkret dicantumkan. |
| `DITUNDA` | Di luar F3 dengan ADR + fase tujuan eksplisit; bukan "nanti". |

Angka performa diukur di workstation ini dan **bukan** angka mesin referensi
Q14; angka itu bukti eksekusi, bukan klaim parity. Error informatif (§2.2)
tetap `TERBUKA` dan tidak dihitung sebagai baris acceptance terpisah. Nama tes yang disebut
ROADMAP/SPEC tetapi tidak ada di kode dicatat sebagai temuan, bukan diganti.

## Ringkasan

| grup | baris | LULUS | TERBUKA | DITUNDA |
| --- | ---: | ---: | ---: | ---: |
| Deliverable F3 (`ROADMAP.md:145`) | 12 | 12 | 0 | 0 |
| Acceptance F3 (`ROADMAP.md:148-154`) | 12 | 12 | 0 | 0 |
| GAP gate F3 (`ROADMAP.md:234`, 20) | 20 | 13 | 0 | 7 |

**Anggaran.** Σ core **47.511/47.600** (ADR-236/241); TOTAL **63.446/190.000** (`make loc`, 0 `GAGAL`).

## 1. Deliverable F3

| deliverable | status | bukti | sisa kerja |
| --- | --- | --- | --- |
| `tui` | LULUS | `internal/tui` (ADR-199); wiring `cmd/damnbot/tui_wire.go:24`; suite `TestTeatestInputRoundTrip` `internal/tui/input_e2e_test.go:83`, `TestTabCyclesRegistry` `model_test.go:395`, `TestNavMovesAndFolds` `nav_test.go:43`, `TestTabsRenderAndSwitch` `tabs_test.go:12`, `TestPaletteFiltersAndRunsSlash` `palette_test.go:13`, `TestBangRendersToolBlock` `bang_test.go:9`, `TestR7GoldenViews` `golden_test.go:58`, UX-05/06 §GAP (`TestKeystrokeEchoP99`, `TestResizeCoalesces`, `TestNoColorZeroEscape`), `TestFramePaletteBudget` `internal/tui/design_test.go:64`, `TestSpacingContract` `:144`, `TestRenderBudget1000Blocks` `render_perf_test.go:21`; P12/P17 §2 lulus | SPEC §13.1 masih kurang external editor `/prompt`, notify/bell, dan arti `ctrl+t`/`ctrl+w`; estetika sudah terkunci tes (palet/spacing/hierarki); lihat §6 |
| `acp` | LULUS | `internal/acp` (ADR-201/205/219); server `cmd/damnbot/acp_wire.go:75`; golden `TestACPGoldenPrompt`/`TestACPGoldenProtocolErrors` `acp_wire_test.go:120,128`; `TestACPInitializeProtocol` `:136`; fixture `internal/acp/testdata/golden.jsonl`, `golden-errors.jsonl`; e2e `TestACPStdio` `internal/e2e/acp_test.go:11` | `session/load`, `fs/*`, `terminal/*`, capability image/audio/mcp belum ada (ADR-219); smoke `tern` lulus §2 |
| `config` | LULUS | `cmd/damnbot/config_cli.go:34`; `show/get/explain/schema/edit` + `profiles ls` (ADR-214), `profiles export\|import` + `modules` (ADR-228) `cmd/damnbot/config_profiles_archive.go:40`, `cmd/damnbot/config_modules_cli.go:19`; `config_version` (ADR-221) `internal/config/validate.go:18`; tes `TestConfigShowMasksSecrets` `config_cli_test.go:73`, `TestProfileArchiveRoundTrip` `config_profiles_archive_test.go:87`, `TestConfigVersionMarker` `internal/config/version_test.go:20` | `config hooks` di-re-fase F4 (ADR-228) dan full GAP-OPS-01 (generated docs/env override) belum |
| `damnbot setup` (rekomendasi provider) | LULUS | `cmd/damnbot/setup_cli.go:49`; tiga langkah (`:84` `1/3`), deteksi nama env kredensial tanpa nilai, tulis 2 baris `:148`; `setup secrets set\|rotate\|ls` (ADR-223) `cmd/damnbot/secrets_cli.go`; unit `TestSetupFreshYes` `setup_cli_test.go:34`, `TestSecretsSetListRotate` `internal/e2e/secrets_test.go:54`; e2e setup `internal/e2e/setup_test.go:30` | `auth` F5, `ssh add` F5, `--sudo-rule` F4, `--uninstall` F9; clone profil tanpa nilai (`TestProfileCloneNoSecretValues`) belum (§4) |
| plan mode | LULUS | `TestPlanModeShell` `internal/exec/classify/policy_test.go:227`; enforcement `TestPlanModeShellDispatch` `internal/tools/plan_mode_test.go:54`, `TestPlanModeNoSideEffects`; wiring TUI `tui_wire.go:145`, ACP `acp_wire.go:210`; ADR-203 | flag `run --plan` (SPEC §10) belum ada di `cmd/damnbot/run.go:119-135`; plan mode hanya lewat TUI/ACP |
| preview aksi berbahaya | LULUS | `TestPreviewBeforeDangerous` `internal/tools/preview_test.go:101` (69 kasus, 0 proses), batas 3.500 rune (ADR-204); modal TUI `internal/tui/approval.go:16,133` + `TestApprovalPreviewThenAllowOnce` `approval_test.go:29`; ADR-202/204 | diff unified yang membaca workspace tetap di luar F3 (ADR-204) |
| slash/CLI inti | LULUS | registry CLI `cmd/damnbot/surface.go:54` (22 entri, `TestSurfaceRegistry` `surface_test.go:65`); 16 slash berfungsi + `/btw` yang menolak `internal/tui/slash.go:43-61`; tes `slash_test.go:89,125`, `config_slash_test.go:34`, `slash_a7_test.go:29,80,114,142` | 50/59 baris SLASH F3 tidak punya slash bernama sama; alias NP-06 dan queue/steer belum (§6) |
| `importer` | LULUS | `internal/importer/importer.go:260`; CLI `cmd/damnbot/import_cli.go:35`; 5 fixture `TestImportFiveFixturesCoverage` `sources_test.go:141`, `TestImportAccounted` `importer_test.go:179`, `TestImportIdempotent` `:199`; e2e `TestImportHermesWritesSkillsAndMemory` `internal/e2e/import_test.go:117` + `TestImportHermesIdempotent` `:172`; ADR-200 | fixture sintetis, bukan 5 home `~/.hermes` nyata teredaksi; YAML 1.1 (GAP-PROV-04) dan memory-provider mapping belum |
| profiles | LULUS | dasar: `--profile` + `config profiles ls` `cmd/damnbot/config_cli.go:314`, `TestConfigProfilesLs` `config_cli_test.go:188`; `config profiles export\|import` (ADR-228) `cmd/damnbot/config_profiles_archive.go`, e2e `TestConfigProfilesExportImportRoundTrip` `internal/e2e/profiles_archive_test.go:131`; ADR-214 butir 6 | permukaan `damnbot profile` (15 sub, parity CLI-066) tetap di luar SPEC §10/§12 |
| release pipeline | LULUS | `.github/workflows/release.yml:53` (dua build byte-identik + SBOM), `:141` ed25519; `scripts/release-test.sh:1`; `make release-test` `Makefile:224`; unit `TestSignVerifyRoundTrip` `tools/release/main_test.go:82`; dieksekusi lokal: `RELEASE TEST ok` | workflow tag `v*` belum pernah dijalankan (0 run); bukti reproducible baru pada perintah workflow, bukan artefak rilis |
| self-update + rollback | LULUS | `internal/update/install.go`; CLI `cmd/damnbot/update.go:26`; `TestInstallSuccess` `internal/update/install_test.go:163`, `TestRollbackRestoresBinaryAndDB` `:251`, `TestInstallSerializesConcurrentUpdates` `:289`; e2e `TestE2EUpdateInstallAndRollback` `internal/e2e/update_install_test.go:179`, `TestE2EUpdateInstallRejectsCorruptAsset` `:251`; ADR-216 | update nyata ke GitHub Releases belum diuji end-to-end; kontrak fake-server + aset bertanda tangan diuji |
| `LICENSE` MIT | LULUS | `LICENSE:1` "MIT License" | — |

## 2. Acceptance F3

| acceptance | status | bukti / angka | sisa kerja |
| --- | --- | --- | --- |
| P12 first frame ≤ 100 ms | LULUS | `bench/f3/run.sh` pada `e4e86a02` (kode root identik dengan `041ead4f`; delta hanya dokumen): **p95 47,349 ms** (20 sampel; run sebelumnya `20157f3f` 48,732 ms), gate 100 ms; trace `F3TRACE` membuktikan `config-done`/`app-built`/`run-start` sebelum frame dan `ensure-start` sesudahnya. Artefak PR `#386` `bench/f3/results/damnbot-6d01e7a1d5-F3.json`: p95 50,752 ms. `TestStartupBudget` in-process: p95 145,765 µs | harness ini manual (bukan CI gate); angka lokal bukan Q14. Kesiapan F4 tetap tidak bergantung pada baris ini |
| P13 build `minimal` ≤ 22 MB | LULUS | `make size-check` pada `e4e86a02` (lokal, `TAGS=goolm`): full **19.763.362 B / 52.000.000 B**; minimal (`goolm,minimal`) **19.763.362 B / 22.000.000 B** | minimal byte-identik dengan full; plafon lulus, tag `minimal` belum memangkas (§5) |
| P17 first token TUI ≤ TTFT + 30 ms | LULUS | `TestFirstTokenBudget` `internal/tui/p17_perf_test.go:47` pada `e4e86a02`: headless p95 26,043664 ms, TUI delta p95 **343,869 µs**; harness PTY first token p95 **16,6 ms** (e4e86a02) | — |
| ACP golden transcript | LULUS | `TestACPGoldenPrompt`/`TestACPGoldenProtocolErrors` `cmd/damnbot/acp_wire_test.go:120,128`; fixture `internal/acp/testdata/golden.jsonl`, `golden-errors.jsonl`; ADR-219 | — |
| `tern acp` manual smoke | LULUS | `~/.cache/damnbot/evidence-tern/acp-init/` (`acp-init-verification.txt`, `TURN.txt`): tern 0.9.5, head `ff2c8b84`, initialize + session/new + session/prompt + session/update dengan fake TLS provider; satu turn penuh `Hello from the fake local provider.`, tanpa banner `missing field`/`answered initialize` | batas: satu delta kandidat, tanpa tool call/approval; kredensial nyata tidak dipakai |
| Wizard ≤ 3 langkah, ≤ 60 s, config 2 baris | LULUS | `runSetup` `setup_cli.go:49` mencetak `1/3` (`:84`), `3/3`; tulis `[provider]\nmodel = "…"\n` (`:148`); unit `TestSetupFreshYes` `setup_cli_test.go:34`; e2e `TestSetupFreshYes` `internal/e2e/setup_test.go:30` mengasersi ≤ 60 s, byte config persis, mode 0600, tanpa bocor secret | — |
| Import 5 fixture ≥ 99%, 0 silent loss, idempoten | LULUS | `TestImportFiveFixturesCoverage` `internal/importer/sources_test.go:141`; `TestImportAccounted` `importer_test.go:179`; `TestImportIdempotent` `:199`; e2e import | fixture sintetis, bukan 5 home nyata; pemetaan config sempit (5 key) |
| `TestPlanModeShell` | LULUS | `TestPlanModeShell` `internal/exec/classify/policy_test.go:227`; dispatch `TestPlanModeShellDispatch` `internal/tools/plan_mode_test.go:54`; dijalankan lokal: PASS | — |
| EC-CFG-001..008 | LULUS | 7/7 id F3 `covered` (001..007); 008 `DITUNDA` → F5 (ADR-226) | tidak ada sisa F3; 008 menunggu F5 |
| EC-FIO-007 | LULUS | `TestConfigBOM` `internal/config/bom_test.go:14`, `TestCompileBOM` `internal/schema/bom_test.go` | — |
| EC-FIO-018 | LULUS | `TestFrameSplitOnLFOnly` `internal/acp/frame_test.go:36`; `TestFrame100MBBounded` `:132` (100 MB → `-32600`, heap +1 KiB) | — |
| Baseline hermes F3 | LULUS | `bench/f3/results/hermes-868b9f244f-F3.json` (`#397`): CLI REPL first token p50 **148,671**/p95 192,938 ms, prompt-ready p50 6.767,787 ms; Ink TUI first token p50 **39,407**/p95 46,948 ms, first frame p50 **1.368,273**/p95 1.518,447 ms, prompt-ready p50 1.834,405 ms; buildtime clean p50 **66,276 s**/p95 245,950 s, incremental p50 4,756/p95 4,841 s. Damn-Bot: P12 p95 47,349 ms, first token p95 16,6 ms; buildtime artifact 29,140/1,698 s | workstation Xeon 6152, gateway hangat, Ink TUI dibangun, bukan Q14; error informatif tetap `TERBUKA` (§2.2) menunggu ratifikasi manusia |

### 2.1 EC-CFG

| EC | status | bukti | catatan |
| --- | --- | --- | --- |
| EC-CFG-001 | LULUS | `TestConfigStaleEnvDoesNotOverrideFile` `internal/config/edgecases_cfg_test.go:23` | `.env` basi tidak mengalahkan `config.toml` |
| EC-CFG-002 | LULUS | `TestConfigSHA` `cmd/damnbot/config_sha_test.go:22` | `sessions.config_sha` = hash config efektif; reload invalid mempertahankan hash |
| EC-CFG-003 | LULUS | `TestRunStartupNoNetworkProbe` `cmd/damnbot/edgecases_cfg_test.go:62` | 0 request sebelum turn |
| EC-CFG-004 | LULUS | `TestDualStackFallback` `internal/netx/dial_dualstack_test.go:26` | race IPv4/IPv6 + fallback |
| EC-CFG-005 | LULUS | `TestConfigVersionMarker` `internal/config/version_test.go:20`; `validate.go:18`; ADR-221 | legacy/0/1/2/99/-1 |
| EC-CFG-006 | LULUS | `TestCompactorConstructionNoProbe` `cmd/damnbot/edgecases_cfg_test.go:96` | 0 request, fallback window |
| EC-CFG-007 | LULUS | `TestRunConfigContextLength` `cmd/damnbot/edgecases_cfg007_test.go:21`; `cmd/damnbot/limits_config.go`; `registry.NewLimits(configWindows, …)` `run.go:411` | override `providers.custom` sampai ke resolver |
| EC-CFG-008 | DITUNDA → F5 | ADR-226; `EDGE-CASES-hermes.md:251`; 0 simbol credential pool | pool/provider switch milik F5; tidak ada tes F3 yang dikarang |

### 2.2 Error informatif (bahan suite `#390`)

Sub-baris ini merinci dimensi error informatif di baseline; ia **bukan**
baris acceptance terpisah dan tidak menambah hitungan §Ringkasan.

Sumber: `bench/f3/errors/**` di `main` (PR `#390` MERGED, `937fbddf`; final
Windows rerun lulus 24 pass/1 skip). Status: **`TERBUKA` — bahan + skor proxy
tersedia (3,84/5); ratifikasi 2 penilai manusia masih diperlukan.** Rincian:
60 kasus, cek objektif 60/60; proxy 3,84/5 llm-proxy; agreement exact
a 78,3% b 71,7% c 68,3% d 98,3% e 81,7%.

| cek | hasil | sumber |
| --- | --- | --- |
| exit code ≠ 0 | **60/60** | `bench/f3/errors/summary.json`; README §5 |
| kebocoran secret di output tersanitasi | **0/60** | idem |
| `panic:` / `goroutine ` | **0/60** | idem |
| pesan tidak kosong (≥ 1 baris) | **60/60** | idem |
| skor proxy llm-proxy (dua rater A/B, rubrik v1 SHA-256 `ff193826…`) | mean **3,84/5**; a `what_failed` 4,34, b `why` 3,89, c `what_to_do` 2,25, d `no_secret_leak` 4,99, e `no_stack_for_usage` 3,71 | `bench/f3/errors/scores-proxy.json`; README §6 |
| agreement exact-match per dimensi | a 78,3% · b 71,7% · c 68,3% · d 98,3% · e 81,7% (MAD 0,22/0,28/0,33/0,02/0,18) | `scores-proxy.json`; README §6 |
| validitas | `net-tls` di-recapture; basis proxy **59 kasus** (mean 4,35/3,89/2,25/4,99/3,72) | README §6 |

Ratifikasi manusia tidak menggating F3: `make phase-acceptance` hanya menjalankan
`DAMNBOT_PHASE_ACCEPTANCE=1 go test` (`Makefile:199-200`), `TestEdgeCaseCatalogSync`
hanya memeriksa katalog EC (`internal/testutil/phase/acceptance_test.go:33-60`),
dan `TestParityPhaseColumn` hanya fase/jumlah. Tidak ada tes/archtest/ADR yang
membuat skor manusia menjadi pass/fail; `TERBUKA` di sini adalah utang kualitas.

**Bukan ratifikasi manusia, bukan gate, bukan `LULUS`.** Ini hanya sinyal
proxy dua rater LLM; sisi Hermes juga punya 8 skenario tambahan
(`bench/f3/results/hermes-errors-868b9f244f.json`) yang bersifat suplementer,
bukan pengganti penilai manusia; keputusan `LULUS` menunggu dua penilai manusia independen
dan adjudikasi target SCORECARD. Temuan yang dicatat: (i) `run --output text`
mencetak kosong saat provider gagal; (ii) pesan identik untuk penyebab berbeda
(4× config-unreadable, 3× db-corrupt, 3× stream); (iii) tidak satu pun pesan
provider/network/stream memberi langkah perbaikan (dimensi `c` terlemah).
Rujukan: `bench/f3/errors/README.md` §5-§6, `cases.json`, `rubric.json`,
`scores-proxy.json`; PR `#390`.

## 3. GAP gate F3 (ROADMAP:234, 20 gap)

ROADMAP menyatakan acceptance tiap fase bertambah dengan **semua** gap berfase
itu (`ROADMAP.md:228`). Kolom fase mengutip `SPEC.md` §22.1; baris dengan
beberapa fase hanya boleh pindah bila **tidak ada** kewajiban F3 tersisa.

| GAP | fase otoritatif | status | bukti / sisa |
| --- | --- | --- | --- |
| GAP-REL-13 | F3 (`SPEC.md:2927`) | LULUS | `TestACPChildStdinIsolation` `cmd/damnbot/acp_stdio_test.go:29`; ADR-237 (dup privat + `O_CLOEXEC`/non-inheritable, fd0→null, write deadline, frame limit) |
| GAP-GW-07 | F3 importer/scan + F8 hub (`SPEC.md:2939`) | DITUNDA → F8 | ADR-234 menutup kewajiban F3 (`ToSMeta`, `RequireToSAck`, `ClassifyToS`, importer flag, dotenv scan; `TestClassifyToS`/`TestToSMetaRequired`/`TestImportFlagsGodmodeToS`); tampilan/ack CLI hub tetap F8 |
| GAP-PROV-04 | F3 (`SPEC.md:2964`) | LULUS | `TestImportYAML11` `internal/importer/yaml11_test.go:26`, `TestImportYAML11QuotedList` `:70`, `TestImportYAML11DuplicateKey` `:79`, `TestImportYAML11Fixture` `:123`; `internal/importer/yaml11.go` |
| GAP-PROV-05 | F3 importer + F8 memory-providers (`SPEC.md:2965`) | DITUNDA → F8 | F3 importer selesai: `TestImportExternalMemoryProviderWarns` `internal/importer/memory_provider_test.go:11`, `TestImportHermesWarnsExternalMemoryProvider` `internal/e2e/import_memory_provider_test.go:10`; provider memory eksternal F8 |
| GAP-TOOL-01 | F3 requires importer + F6 `doc_*` (`SPEC.md:2966`) | DITUNDA → F6 | F3 importer selesai: `TestImportSkillRuntimeMetadata` `internal/importer/runtime_test.go:24`, `TestImportHermesRuntimeRequires` `internal/e2e/import_runtime_test.go:25`; tool `doc_read`/`doc_write` tetap F6 |
| GAP-TOOL-10 | F3 updater / F2 filter; residual F5/F8 (`SPEC.md:2975`, ADR-193) | DITUNDA | updater `TestNativeArchSelect` (`internal/update/update_test.go:223`) + filter katalog `internal/skills/requires.go`; retriever prompt masih `nil` (ADR-208 butir 5) → F5/F8 |
| GAP-UX-01 | F3 (`SPEC.md:2976`) | DITUNDA → F4 | Client fs (#407: buffer/tag/`stale_tag`/fallback; `TestACPClientFSReadBuffer` `internal/e2e/acp_clientfs_test.go:214`) + `terminal/*` (#414/ADR-241; `TestACPClientTerminalRunsOnClient` `acp_terminal_test.go:267`). Smoke tern `~/.cache/damnbot/evidence-tern/acp-matrix-e0f2b3e4`: 13 interaksi = **10 PASS**, 1 client-limit, 1 partial, 1 not-testable. Sisa eksplisit: **smoke Zed** (ADR-242; host headless, Zed absen) → F4 |
| GAP-UX-02 | F3 (`SPEC.md:2977`) | LULUS | ADR-235; `TestACPModeMatrix` `cmd/damnbot/acp_conformance_test.go:355`, `TestACPSetModel` `:395`, `TestACPSetModelBusy` `:425` |
| GAP-UX-03 | F3 (`SPEC.md:2978`) | LULUS | ADR-233; `TestACPLoadReplaysHistory` `cmd/damnbot/acp_conformance_test.go:152`, `TestACPLoadFailureCodes` `:188`, `TestACPLoadDoesNotCreateSession` `:316` |
| GAP-UX-04 | F3 (`SPEC.md:2979`) | DITUNDA → F4 | #402 parser resource + #413/ADR-240 `_meta` mentah/spill blob (`TestACPResourceSpillAndMetaPersistence` `internal/e2e/acp_resource_test.go:47`, `TestACPResourceBlobTooLarge` `:167`, `TestACPResourceBlocks` `cmd/damnbot/acp_wire_test.go:449`). Sisa eksplisit: **smoke Zed** (ADR-242) → F4 |
| GAP-UX-05 | F3 (`SPEC.md:2980`) | LULUS | `TestKeystrokeEchoP99` `internal/tui/keystroke_perf_test.go:20` (p99 885,963 µs lokal; CHANGELOG ≈101 µs; ambang 10 ms); `TestResizeCoalesces` `ux56_test.go:62` (1 commit) + `TestResizeNegativeControl` `:100` (50) |
| GAP-UX-06 | F3 (`SPEC.md:2981`) | LULUS | `TestNoColorZeroEscape` `internal/tui/ux56_test.go:12` (0 byte ESC di semua state), `TestNoColorWinsOverTheme` `:49`; `TestNoColorMode` `cmd/damnbot/render_mode_test.go:6`, `TestRootPlainFlagReachesTUI` `:29` |
| GAP-OPS-01 | F1 subset / F3, F4 penuh (`SPEC.md:2986`) | DITUNDA → F4 | subset F3 lengkap: `show/get/explain/schema/edit`, `profiles ls/export/import`, `modules` (ADR-214/ADR-228); `config hooks` di-re-fase F4 (ADR-228); sisa F4: generated docs/env override/allowlist |
| GAP-OPS-02 | F3 penuh (`SPEC.md:2987`) | LULUS | `TestE2EBackgroundOffBlocksUpdate` `internal/e2e/ops02_test.go:16`, `TestE2EBackgroundOffKeepsInference` `:44`, `TestE2EDoctorNetwork` `:59`, `TestEgressRegistryComplete` `internal/netx/egress_test.go:36`; ADR-230 |
| GAP-OPS-04 | F3 (`SPEC.md:2989`) | LULUS | ADR-223 + amendemen; `TestNamedRotateAllProfiles`, `TestNamedRotateAllProfilesPartialFailure`, `TestSecretsListRefsMatrix`, `TestSecretsRotateAllProfilesRequiresKeyring`, e2e `TestSecretsRotateAllProfiles`/`TestSecretsSetListRotate`; clone profil tanpa nilai `TestProfileCloneNoSecretValues` `internal/e2e/profiles_clone_test.go:17` |
| GAP-OPS-06 | F3 (`SPEC.md:2991`) | LULUS | `internal/doctor` `DOC-NNN` + `damnbot.doctor/v1` (`doctor.go:21,34`); `TestRunHealthy` `doctor_test.go:57`, `TestReportJSON` `:125`; ADR-217 |
| GAP-OPS-07 | F3 (`SPEC.md:2992`) | LULUS | `TestCompletionScripts` `cmd/damnbot/completion_cli_test.go:8`, `TestCompleteCandidates` `:24`; ADR-222 |
| GAP-OPS-08 | F3 (`SPEC.md:2993`) | LULUS | 22 entri `surface()` `surface.go:54`; `TestSurfaceRegistry` `surface_test.go:65` |
| GAP-OPS-09 | F3 (`SPEC.md:2994`) | LULUS | ADR-238; `TestE2ENoticePolicyConfig` `internal/e2e/ops09_test.go:13`, `TestE2EJSONModeHasNoNotice` `:38`, `TestNoticeGateOnce` `cmd/damnbot/notice_gate_test.go:14`, `TestNoNag` `internal/tui/notice_test.go:64` |
| GAP-INF-04 | F3 (`SPEC.md:2998`) | LULUS | ADR-239; `TestChangelogPerTag` `internal/update/changelog_test.go:144`, `TestUpdateCheckShowsChangelog` `:282`, `TestChangelogPerTagSyntheticHistory` `:308` |

## 4. Gap jujur

1. **Baseline hermes F3 kini dua sisi.** `bench/f3/results/hermes-868b9f244f-F3.json` (#397) mengukur CLI REPL dan Ink TUI Hermes; artefak Damn-Bot tetap ada. Angka: CLI REPL first token p50 148,671/p95 192,938 ms, prompt-ready 6.767,787 ms; Ink TUI first token p50 39,407/p95 46,948 ms, first frame p50 1.368,273/p95 1.518,447 ms, prompt-ready 1.834,405 ms; buildtime clean p50 66,276 s/p95 245,950 s, incremental 4,756/4,841 s. Damn-Bot: P12 p95 47,349 ms, first token p95 16,6 ms; buildtime artifact 29,140/1,698 s. Caveat: Xeon 6152, gateway hangat, Ink TUI dibangun, bukan Q14. Baris acceptance `LULUS`; error informatif tetap `TERBUKA` menunggu ratifikasi manusia (§2.2).
2. **Error informatif.** PR `#390` sudah **MERGED** (`937fbddf`; final Windows rerun lulus). Cek objektif 60/60; skor proxy **3,84/5** (llm-proxy, dua rater; agreement a 78,3% b 71,7% c 68,3% d 98,3% e 81,7%; `net-tls` di-recapture, basis 59 kasus). Angka ini bukan ratifikasi manusia dan bukan gate; status ledger tetap `TERBUKA` menunggu 2 penilai manusia (§2.2).
3. **`setup` subperintah.** `secrets set|rotate|ls` (termasuk `rotate --all-profiles`, matriks `ls --refs`, dan clone profil tanpa nilai) sudah ada dan diuji (ADR-223 + amendemen); `auth` → F5 (`GAP-SEC-18`, `SPEC.md:2904`), `ssh add` → F5 (`GAP-SEC-23`), `--sudo-rule` → F4 (`GAP-SEC-14`), `--uninstall` → F9 (`GAP-OPS-03`).
4. **Konflik fase CLI-064 `hermes uninstall`.** `FEATURE-PARITY.md:140` menulis F3, sedangkan `GAP-OPS-03`/`SPEC.md:2988` dan ADR-220 menempatkan `--uninstall` di F9. Ledger mengikuti SPEC + ADR.
5. **`ctrl+t`/`ctrl+w` tanpa arti normatif.** `SPEC.md:2556` hanya mendaftar tombol; `internal/tui` tidak mengimplementasikannya, sehingga tidak ada tes yang bisa dibuat tanpa keputusan perilaku.
6. **Queue/steer belum ada.** `SLASH-028`/`SLASH-029`/`SLASH-065` dan `AUTO-023` berfase F3; `internal/tui` belum punya antrean/steer (hanya `/plan`).
7. **`GAP-TOOL-10` residual.** Filter katalog + `skills.Store.Search` menegakkan `requires`, tetapi retriever prompt produksi masih `nil` (ADR-208 butir 5) → F5/F8.
8. **`GAP-SEC-16` residual.** `TestEgressDLP` menutup ingress; gerbang egress argumen tool 6 tool × 4 encoding di `internal/netx` belum ada. Fase penuh `SPEC.md:2902`: F2/F4/F5/F6/F7; bukan gate F3.
9. **ACP: smoke Zed ditunda ke F4.** `session/load`, `set_model`, mode, client fs, `_meta`/blob, dan `terminal/*` sudah ada (ADR-232/233/235/240/241); smoke tern 13 interaksi = 10 PASS/1 client-limit/1 partial/1 not-testable. Client kedua (Zed) tidak dapat dijalankan di host headless ini, sehingga `GAP-UX-01`/`GAP-UX-04` **DITUNDA → F4** (ADR-242), bukan `LULUS`; requirement 1.0 tetap wajib sebelum RC/F9. `image/audio/mcp` capabilities tetap false (`SPEC.md:2588` menulis true).
10. **Profiles dan fixture.** `config profiles ls/export/import` sudah ada (ADR-228) dan deliverable profiles `LULUS`; permukaan `damnbot profile` (15 sub, parity CLI-066) tetap di luar SPEC §10/§12. Import memakai 5 fixture sintetis, bukan 5 home nyata teredaksi.

## 5. Divergensi dokumen (temuan, bukan tuduhan)

1. **ADR-194 butir 4 vs kode.** Wiring config sudah diperbaiki #372: `cmd/damnbot/limits_config.go` + `run.go:411` meneruskan `providers.custom` ke `registry.NewLimits`. Namun `provider.Route.ContextWindow` masih `cfg.Loop.TurnMaxInputTok` (`run.go:385`), sedangkan ADR-194 butir 4 (`DECISIONS.md:4145`) menyatakan `Limit.ContextWindow` **menggantikan** `loop.turn_max_input_tok`; komentar `compaction_wire.go` bahkan menyebut input cap tetap menjadi `Route.ContextWindow`. EC-CFG-007 tetap `LULUS` karena override-nya sampai ke resolver, tetapi klaim penggantian route belum benar.
2. **`SCORECARD.md` "diukur di F0" kini bertentangan dengan artefak F3.** Sejak #397, `bench/f3/results/hermes-868b9f244f-F3.json` berisi angka F3 dua sisi untuk first token TUI, build time, dan startup; sel `SCORECARD.md:16-65` masih berlabel "diukur di F0" dan catatan `SCORECARD.md:3` hanya membaca ulang dimensi LLM sebagai RC F9. Label SCORECARD perlu diselaraskan; error informatif tetap proxy, bukan angka manusia.
3. **Kolom `status` FEATURE-PARITY tidak digate.** 854 baris berstatus `planned`/drop/keep; **0** `covered`, termasuk baris F1/F2 yang sudah punya tes. `TestParityPhaseColumn` (`internal/archtest/parity_phase_test.go:82`) hanya memeriksa kolom `fase`.
4. **Hitungan FEATURE-PARITY vs ADR-209.** Dokumen menulis F2 59 / F3 135 (`ROADMAP.md:31`); ADR-209 memindahkan CLI-040 + OPS-006 ke F3 dan CLI-038 ke F9 (F2 56 / F3 137), tetapi `FEATURE-PARITY.md:114,116,918` masih F2. Angka §6 adalah angka dokumen.
5. **P13 `minimal` tidak memangkas.** `make size-check` pada `e4e86a02` menghasilkan byte identik full dan minimal (19.763.362 B). Plafon lulus, tetapi gate tidak membedakan build.
6. **ADR-220 vs completion.** ADR-220 mencantumkan `completion` sebagai gap, tetapi #380 sudah mendarat dan tesnya lulus; ADR perlu amandemen.

## 6. Hitungan parity F3

Sumber: `docs/FEATURE-PARITY.md` pada `041ead4f`.

- Baris berfase **F3 = 135** (SLASH 59, CLI 23, OPS 15, UI 14, PLUG 9, CORE 5, MEM 4, SKILL 3, AUTO 3).
- Menurut dokumen: **0 `covered`, 135 `planned`.**
- Menurut kode: **44 baris punya permukaan bernama sama** (9 SLASH, 10 CLI, 10 OPS termasuk 2 sebagian, 8 UI, 3 CORE, 3 MEM, 1 AUTO); **91 baris tidak punya implementasi bernama sama.** Ini inventaris nama/permukaan, bukan klaim parity selesai.
- Jika ADR-209 diterapkan, F3 menjadi 137 (tambah CLI-040 + OPS-006); CLI-040 sudah punya CLI, OPS-006 sebagian (`sessions backup` F2 + import).

Baris F3 yang **belum** punya implementasi bernama sama (91):

- **SLASH (50):** 004, 005, 006, 007, 008, 009, 011, 013, 015, 016, 017, 019, 020, 024, 025, 028, 029, 032, 038, 042, 044, 045, 046, 050, 051, 052, 054, 055, 057, 058, 059, 061, 062, 065, 066, 068, 069, 070, 071, 074, 083, 085, 086, 087, 088, 090, 097, 100, 101, 103.
- **CLI (13):** 002, 022, 031, 033, 035, 043, 044, 048, 064, 066, 071, 072, 075.
- **OPS (5):** 002, 005, 012, 017, 025.
- **UI (6):** 009, 013, 025, 026, 027, 028.
- **PLUG (9):** 001, 002, 003, 004, 005, 006, 007, 008, 010.
- **CORE (2):** 006, 016.
- **MEM (1):** 036 (`/btw` ditolak: `internal/tui/actions.go:85`).
- **SKILL (3):** 003, 005, 006.
- **AUTO (2):** 022, 023.

Catatan: `OPS-002` (profile export/import) kini punya kapabilitas lewat `config profiles export|import` (ADR-228), tetapi bukan permukaan `damnbot profile` (CLI-066) yang tetap absen; hitungan "bernama sama" tidak berubah.

Status parity **bukan gate kenaikan fase**: `TestParityPhaseColumn` hanya
memeriksa kolom `fase` dan jumlah per fase (`internal/archtest/parity_phase_test.go:84-113`);
ADR-139 butir 2/6 (`DECISIONS.md:3409,3413`) menetapkan fase = fase selesainya
baris, bukan status `covered`;
`make phase-acceptance` hanya katalog EC (ADR-176/ADR-080). Jadi 91 baris adalah
**utang parity**, bukan penghalang F3. Usul ADR re-fase (belum dikerjakan):
`PLUG-001..008` → F8 (plugin system; `PLUG-010` hooks → F4 lewat ADR-228) dan
`UI-013` tui_gateway → drop-with-approval karena backend RPC Hermes tidak punya
padanan arsitektur; 50 baris SLASH tetap F3 per ADR-139 sampai ada ADR per baris.

Catatan: beberapa baris punya kapabilitas lewat jalur lain (`/compress`→`/compact`,
`/branch`→`/fork`, `/skin`→`/theme`; `/btw` ada sebagai nama tetapi menolak;
`hermes chat`→bare `damnbot`). Baris itu tidak dihitung "punya implementasi
bernama sama"; hitungan tidak boleh dibaca sebagai "fitur hilang 91".

## 7. Rekomendasi

1. **F3 siap dinaikkan setelah PR ini.** Gate katalog EC hijau (001..007 `covered`; 008 F5), baseline/smoke/profiles `LULUS`, dan `make phase-acceptance` flip F4 lulus di 8 GiB; tidak ada baris F3 `TERBUKA` (UX-01/UX-04 `DITUNDA → F4` lewat ADR-242).
2. **EC-CFG selesai untuk F3.** 001..007 punya tes yang dijalankan; 008 di-re-fase ke F5 lewat ADR-226. Ini tidak boleh dipakai untuk mengklaim seluruh F3.
3. **Tidak ada penghalang F3 yang tersisa.** 13 GAP `LULUS`; 7 `DITUNDA` dengan fase eksplisit (GW-07/PROV-05 → F8, TOOL-01 → F6, OPS-01/UX-01/UX-04 → F4, TOOL-10 → F5/F8). Error informatif dan status parity bukan gate.
4. **Syarat naik terpenuhi:** tidak ada `TERBUKA`, katalog hijau saat flip F4, dan `make phase-acceptance` lulus 8 GiB. `PHASE` tetap `F3` di PR dokumen ini; owner menjalankan gate fase final lalu bump pada PR terpisah. Ratifikasi error informatif bukan gate, hanya utang kualitas.
5. **Parity F3 (91/135 tanpa permukaan)** adalah utang F3; jangan menandai `covered` tanpa tes. Paling tidak 50 baris SLASH dan 9 baris PLUG perlu keputusan re-fase ADR.

## 8. Kesiapan F4

**Kesimpulan: syarat `PHASE=F4` terpenuhi; PR dokumen ini tidak mengubah `PHASE`.**

Syarat yang diminta: (a) tidak ada baris F3 `TERBUKA` yang wajib, (b) gate katalog hijau
saat flip `F4`, (c) `make phase-acceptance` lulus. Normatif: `make phase-acceptance`
hanya menjalankan `DAMNBOT_PHASE_ACCEPTANCE=1 go test` (`Makefile:199-200`) dan
`TestEdgeCaseCatalogSync` hanya katalog EC; `TestParityPhaseColumn` hanya fase/jumlah.
Tidak ada gate yang mensyaratkan ratifikasi manusia atau status parity `covered`.

| syarat | hasil |
| --- | --- |
| Gate katalog flip F4 | **LULUS (terbatas)**: worktree sementara `e4e86a02` dengan `PHASE=F4`; `DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase ./internal/archtest` → ok. EC F3 001..007 covered; 008 F5. |
| `make phase-acceptance` penuh | **LULUS**: worktree sementara `e4e86a02` + `PHASE=F4`, `ulimit -v 8388608`, `make phase-acceptance` → rc=0, 451,27 s. Tail: `ok internal/testutil/timing`, `ok internal/tools`, `ok internal/toolset`, `ok internal/tui`, `ok internal/update`. |
| Tidak ada baris F3 wajib `TERBUKA` | **LULUS**: 0 `TERBUKA`; UX-01/UX-04 `DITUNDA → F4` (ADR-242). Error informatif **tidak** menggating F3; ia dicatat sebagai utang kualitas. |
| PHASE | **Tetap `F3` di PR ini**; owner menjalankan gate fase final lalu bump. |

Tidak ada penghalang F3 yang tersisa. Baseline/smoke/profiles `LULUS`; UX-01/UX-04
di-re-fase ke F4 lewat ADR-242; error informatif adalah utang kualitas, bukan gate.
Flip F4 + `make phase-acceptance` sudah hijau; simpan artefaknya saat bump.

## 9. Audit ulang (perintah yang dijalankan)

Snapshot `041ead4f`, worktree `~/.cache/damnbot/wt-f3-final`. Perintah berat
memakai `nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1` dan `ulimit -v 4194304`;
lint memakai `GOMEMLIMIT=1500MiB`.

```bash
gh pr list --state open                        # 0
gh pr checks 390                               # #390 merged; final checks lulus
go test -tags "goolm,perf" -count=1 -run '^(TestStartupBudget|TestFirstTokenBudget|TestKeystrokeEchoP99|TestRenderBudget1000Blocks)$' ./internal/tui
# PASS; P12 in-process 145.765us; P17 delta 343.869us; keystroke p99 885.963us; R2 p95 1.523277ms
OUT=/tmp/f3-hermes-final.json RUNS=20 BUILD_RUNS=1 bench/f3/run.sh
# P12 p95=47.349ms; first token p95=16.6ms
TAGS=goolm make size-check
# SIZE ok full 19763362 B / 52000000 B; minimal 19763362 B / 22000000 B
go test -tags goolm -count=1 -run '^(TestImportYAML11|TestImportYAML11Fixture)$' ./internal/importer
# ok
go test -tags goolm -count=1 -run '^(TestResizeCoalesces|TestResizeNegativeControl|TestNoColorZeroEscape)$' ./internal/tui
# ok
go test -tags goolm -count=1 -run '^(TestNoColorMode|TestRootPlainFlagReachesTUI|TestACPInitializeProtocol)$' ./cmd/damnbot
# ok
# worktree sementara e4e86a02 + PHASE=F4:
DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase ./internal/archtest
# ok (gate katalog)
# make phase-acceptance 8 GiB
ulimit -v 8388608; make phase-acceptance
# rc=0 (451.27s); tail: ok timing/tools/toolset/tui/update
go test -tags goolm -count=1 ./internal/archtest
# ok
make loc
# rc=0, 0 GAGAL, TOTAL 63446/190000
```

**Tidak dijalankan ulang di sesi ini:** smoke `tern` (memakai evidence
`~/.cache/damnbot/evidence-tern/acp-matrix-e0f2b3e4` dan `acp-init/` dari mesin
ber-tern); update ke GitHub
Releases nyata (kontrak fake-server + aset bertanda tangan yang diuji); soak/chaos;
angka Q14. Angka performa lokal bukan bukti parity.
