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

**Snapshot audit:** `origin/main` **`937fbddf`** (commit 2026-10-28 04:30 UTC /
11:30 +0700), tree `7951fa35`, `PHASE` = `F3`. Toolchain
`go1.26.10 linux/amd64`, workstation (bukan mesin referensi Q14).

**Verifikasi PR.** Pada snapshot ini satu-satunya PR terbuka adalah PR ledger
ini (`#391`). Seluruh gelombang penutup sudah MERGED: `#374` (D2b
install/rollback, `950e061a`), `#382` (baseline F3, `4d742996`), `#383`
(secrets, `7c377f20`), `#385` (ledger, `56a7dad6`), `#386` (P12, `ee282775`),
`#387` (ADR-225, `50c5883d`), `#388` (EC-CFG-002, `b1aca3fa`), `#389`
(EC-CFG-008/ADR-226, `b9247ef4`), `#390` (suite error, `937fbddf`), serta
`#372`/`#375` (EC-CFG-007/004). Final check PR `#390` lulus: `test` (3 OS;
Windows 13m39s), `test-timing`, `extra`, `static`, `supply`, `bench`, `fuzz`,
`ci-required`. Push CI di `main` adalah tier `smoke`, jadi bukan bukti suite
penuh.

**Aturan bukti.**

| status | arti |
| --- | --- |
| `LULUS` | Tes/perintah ada, dijalankan pada snapshot ini atau CI dengan tree identik, dan lulus; lokasi dikutip. |
| `TERBUKA` | Belum ada bukti lulus; sisa kerja konkret dicantumkan. |
| `DITUNDA` | Di luar F3 dengan ADR + fase tujuan eksplisit; bukan "nanti". |

Angka performa diukur di workstation ini dan **bukan** angka mesin referensi
Q14; angka itu bukti eksekusi, bukan klaim parity. Nama tes yang disebut
ROADMAP/SPEC tetapi tidak ada di kode dicatat sebagai temuan, bukan diganti.

## Ringkasan

| grup | baris | LULUS | TERBUKA | DITUNDA |
| --- | ---: | ---: | ---: | ---: |
| Deliverable F3 (`ROADMAP.md:145`) | 12 | 12 | 0 | 0 |
| Acceptance F3 (`ROADMAP.md:148-154`) | 12 | 10 | 2 | 0 |
| GAP gate F3 (`ROADMAP.md:234`, 20) | 20 | 3 | 16 | 1 |

## 1. Deliverable F3

| deliverable | status | bukti | sisa kerja |
| --- | --- | --- | --- |
| `tui` | LULUS | `internal/tui` (ADR-199); wiring `cmd/damnbot/tui_wire.go:24`; suite `TestTeatestInputRoundTrip` `internal/tui/input_e2e_test.go:83`, `TestTabCyclesRegistry` `model_test.go:395`, `TestNavMovesAndFolds` `nav_test.go:43`, `TestTabsRenderAndSwitch` `tabs_test.go:12`, `TestPaletteFiltersAndRunsSlash` `palette_test.go:13`, `TestBangRendersToolBlock` `bang_test.go:9`, `TestR7GoldenViews` `golden_test.go:58`, `TestRenderBudget1000Blocks` `render_perf_test.go:21` (p95 1,523277 ms ≤ 16 ms); P12/P17 §2 lulus | SPEC §13.1 masih kurang external editor `/prompt`, notify/bell, dan arti `ctrl+t`/`ctrl+w`; lihat §6 |
| `acp` | LULUS | `internal/acp` (ADR-201/205/219); server `cmd/damnbot/acp_wire.go:75`; golden `TestACPGoldenPrompt`/`TestACPGoldenProtocolErrors` `acp_wire_test.go:120,128`; fixture `internal/acp/testdata/golden.jsonl`, `golden-errors.jsonl`; e2e `TestACPStdio` `internal/e2e/acp_test.go:11` | smoke `tern` manual, `session/load`, `fs/*`, `terminal/*`, capability image/audio/mcp belum ada (ADR-219) |
| `config` | LULUS | `cmd/damnbot/config_cli.go:34`; `show/get/explain/schema/edit` + `profiles ls` (ADR-214), `profiles export\|import` + `modules` (ADR-228) `cmd/damnbot/config_profiles_archive.go:40`, `cmd/damnbot/config_modules_cli.go:19`; `config_version` (ADR-221) `internal/config/validate.go:18`; tes `TestConfigShowMasksSecrets` `config_cli_test.go:73`, `TestProfileArchiveRoundTrip` `config_profiles_archive_test.go:87`, `TestConfigVersionMarker` `internal/config/version_test.go:20` | `config hooks` di-re-fase F4 (ADR-228) dan full GAP-OPS-01 (generated docs/env override) belum |
| `damnbot setup` (rekomendasi provider) | LULUS | `cmd/damnbot/setup_cli.go:49`; tiga langkah (`:84` `1/3`), deteksi nama env kredensial tanpa nilai, tulis 2 baris `:148`; `setup secrets set\|rotate\|ls` (ADR-223) `cmd/damnbot/secrets_cli.go`; unit `TestSetupFreshYes` `setup_cli_test.go:34`, `TestSecretsSetListRotate` `internal/e2e/secrets_test.go:54`; e2e setup `internal/e2e/setup_test.go:30` | `auth` F5, `ssh add` F5, `--sudo-rule` F4, `--uninstall` F9; `secrets rotate --all-profiles` masih F3 (§4) |
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
| P12 first frame ≤ 100 ms | LULUS | `bench/f3/run.sh` pada `b9247ef4` (biner root identik dengan `937fbddf`; #390 hanya menambah modul bench): **p95 44,418 ms** (20 sampel; `GOMAXPROCS=1` p95 43,626 ms), gate 100 ms; trace `F3TRACE` membuktikan `config-done`/`app-built`/`run-start` sebelum frame dan `ensure-start` sesudahnya. Artefak PR `#386` `bench/f3/results/damnbot-6d01e7a1d5-F3.json`: p95 50,752 ms. `TestStartupBudget` in-process: p95 277,819 µs | harness ini manual (bukan CI gate); angka lokal bukan Q14. Kesiapan F4 tetap tidak bergantung pada baris ini |
| P13 build `minimal` ≤ 22 MB | LULUS | `make size-check` pada `937fbddf` (lokal, `TAGS=goolm`): full **19.271.842 B / 52.000.000 B**; minimal (`goolm,minimal`) **19.271.842 B / 22.000.000 B** | minimal byte-identik dengan full; plafon lulus, tag `minimal` belum memangkas (§5) |
| P17 first token TUI ≤ TTFT + 30 ms | LULUS | `TestFirstTokenBudget` `internal/tui/p17_perf_test.go:47` pada `937fbddf`: headless p95 25,994175 ms, TUI delta p95 **364,993 µs**; harness PTY first token p95 **17,473 ms** (b9247ef4, root identik) | — |
| ACP golden transcript | LULUS | `TestACPGoldenPrompt`/`TestACPGoldenProtocolErrors` `cmd/damnbot/acp_wire_test.go:120,128`; fixture `internal/acp/testdata/golden.jsonl`, `golden-errors.jsonl`; ADR-219 | — |
| `tern acp` manual smoke | TERBUKA | tidak ada transkrip smoke di repo; hanya target `ROADMAP.md:149` dan ADR-201 butir 8 yang menyatakan belum diklaim | jalankan `tern acp damnbot` di mesin dengan `tern`, simpan transkrip + versi |
| Wizard ≤ 3 langkah, ≤ 60 s, config 2 baris | LULUS | `runSetup` `setup_cli.go:49` mencetak `1/3` (`:84`), `3/3`; tulis `[provider]\nmodel = "…"\n` (`:148`); unit `TestSetupFreshYes` `setup_cli_test.go:34`; e2e `TestSetupFreshYes` `internal/e2e/setup_test.go:30` mengasersi ≤ 60 s, byte config persis, mode 0600, tanpa bocor secret | — |
| Import 5 fixture ≥ 99%, 0 silent loss, idempoten | LULUS | `TestImportFiveFixturesCoverage` `internal/importer/sources_test.go:141`; `TestImportAccounted` `importer_test.go:179`; `TestImportIdempotent` `:199`; e2e import | fixture sintetis, bukan 5 home nyata; pemetaan config sempit (5 key) |
| `TestPlanModeShell` | LULUS | `TestPlanModeShell` `internal/exec/classify/policy_test.go:227`; dispatch `TestPlanModeShellDispatch` `internal/tools/plan_mode_test.go:54`; dijalankan lokal: PASS | — |
| EC-CFG-001..008 | LULUS | 7/7 id F3 `covered` (001..007); 008 `DITUNDA` → F5 (ADR-226) | tidak ada sisa F3; 008 menunggu F5 |
| EC-FIO-007 | LULUS | `TestConfigBOM` `internal/config/bom_test.go:14`, `TestCompileBOM` `internal/schema/bom_test.go` | — |
| EC-FIO-018 | LULUS | `TestFrameSplitOnLFOnly` `internal/acp/frame_test.go:36`; `TestFrame100MBBounded` `:132` (100 MB → `-32600`, heap +1 KiB) | — |
| Baseline hermes F3 | TERBUKA | Artefak `bench/f3/results/damnbot-6d01e7a1d5-F3.json` (PR `#382`) mengukur sisi Damn-Bot: P12 50,752 ms; first token p95 18,371 ms; buildtime clean p50 29,140 s / incremental 1,698 s. Run ulang `b9247ef4`: P12 44,418 ms, first token 17,473 ms, buildtime 75,806/1,896 s (local, load-affected). `hermes: unavailable` (Python 3.14/pinned deps tidak ada; Node 22 ada); error informatif dirinci di §2.2 (#390 merged, belum ratifikasi manusia) | acceptance menuntut harness sama untuk **kedua** agent; sisi Hermes belum terukur. Error informatif: `TERBUKA` — bahan + skor proxy tersedia (3,84/5); #390 merged (`937fbddf`); ratifikasi 2 penilai manusia masih diperlukan |

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

**Bukan ratifikasi manusia, bukan gate, bukan `LULUS`.** Ini hanya sinyal
proxy dua rater LLM; keputusan `LULUS` menunggu dua penilai manusia independen
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
| GAP-REL-13 | F3 (`SPEC.md:2927`) | TERBUKA | `acp_wire.go:97` memakai `os.Stdin` langsung; 0 duplikasi fd `O_CLOEXEC`, 0 `TestACPChildStdinIsolation` |
| GAP-GW-07 | F3 importer/scan + F8 hub (`SPEC.md:2939`) | TERBUKA | 0 `tos_risk`/`tos_note`; pola scan dan flag importer belum ada |
| GAP-PROV-04 | F3 (`SPEC.md:2964`) | TERBUKA | importer memakai `go.yaml.in/yaml/v3` (`internal/importer/config.go:7`), tanpa resolver YAML 1.1 |
| GAP-PROV-05 | F3 importer + F8 memory-providers (`SPEC.md:2965`) | TERBUKA | 0 `byterover`/`retaindb`; mapping + warning belum ada |
| GAP-TOOL-01 | F3 requires importer + F6 `doc_*` (`SPEC.md:2966`) | TERBUKA | importer tidak menandai runtime eksternal dengan `requires:`; 0 `doc_read`/`doc_write` |
| GAP-TOOL-10 | F3 updater / F2 filter; residual F5/F8 (`SPEC.md:2975`, ADR-193) | DITUNDA | updater `TestNativeArchSelect` (`internal/update/update_test.go:223`) + filter katalog `internal/skills/requires.go`; retriever prompt masih `nil` (ADR-208 butir 5) → F5/F8 |
| GAP-UX-01 | F3 (`SPEC.md:2976`) | TERBUKA | 0 method `fs/read_text_file`/`fs/write_text_file` |
| GAP-UX-02 | F3 (`SPEC.md:2977`) | TERBUKA | 4 mode diiklankan (`acp_wire.go:178`), `set_mode` ada (`:245`), tetapi 0 tes memilih setiap mode; `set_model` belum ada |
| GAP-UX-03 | F3 (`SPEC.md:2978`) | TERBUKA | `session/load` belum ada; jatuh ke `-32601`, bukan `-32002` |
| GAP-UX-04 | F3 (`SPEC.md:2979`) | TERBUKA | `embeddedContext: true` (`acp_wire.go:138`), tetapi 0 tes parser inbound/resource |
| GAP-UX-05 | F3 (`SPEC.md:2980`) | TERBUKA | 0 tes keystroke→echo p99 dan SIGWINCH coalesce |
| GAP-UX-06 | F3 (`SPEC.md:2981`) | TERBUKA | 0 `NO_COLOR`/`TERM=dumb`/`--plain`; renderer tunggal belum ada |
| GAP-OPS-01 | F1 subset / F3, F4 penuh (`SPEC.md:2986`) | TERBUKA | subset F3 lengkap: `show/get/explain/schema/edit`, `profiles ls/export/import`, `modules` (ADR-214/ADR-228); `config hooks` di-re-fase F4 (ADR-228); sisa F4: generated docs/env override/allowlist |
| GAP-OPS-02 | F3 penuh (`SPEC.md:2987`) | TERBUKA | 0 mode `[network] background=false`/daftar egress |
| GAP-OPS-04 | F3 (`SPEC.md:2989`) | TERBUKA | `setup secrets set\|rotate\|ls` (ADR-223) + `rotate --all-profiles` (probe keyring sebelum tulis, rollback, profil tanpa entri dilaporkan) dan matriks `ls --refs` (nama × profil) diuji `TestNamedRotateAllProfiles`, `TestNamedRotateAllProfilesPartialFailure`, `TestSecretsListRefsMatrix`, `TestSecretsRotateAllProfilesRequiresKeyring`, e2e `TestSecretsRotateAllProfiles`/`TestSecretsSetListRotate`; hanya clone profil tanpa nilai (`TestProfileCloneNoSecretValues`) belum |
| GAP-OPS-06 | F3 (`SPEC.md:2991`) | LULUS | `internal/doctor` `DOC-NNN` + `damnbot.doctor/v1` (`doctor.go:21,34`); `TestRunHealthy` `doctor_test.go:57`, `TestReportJSON` `:125`; ADR-217 |
| GAP-OPS-07 | F3 (`SPEC.md:2992`) | LULUS | `TestCompletionScripts` `cmd/damnbot/completion_cli_test.go:8`, `TestCompleteCandidates` `:24`; ADR-222 |
| GAP-OPS-08 | F3 (`SPEC.md:2993`) | LULUS | 22 entri `surface()` `surface.go:54`; `TestSurfaceRegistry` `surface_test.go:65` |
| GAP-OPS-09 | F3 (`SPEC.md:2994`) | TERBUKA | 0 kebijakan notice/`[ui] notices`; 0 tes frekuensi |
| GAP-INF-04 | F3 (`SPEC.md:2998`) | TERBUKA | update --check menampilkan changelog + build reproducible/signed sudah ada, tetapi bagian `Breaking`/`Migrations`/`Security` per tag + `TestChangelogPerTag` belum |

## 4. Gap jujur

1. **Baseline hermes F3 belum dua sisi.** Artefak `bench/f3/results/damnbot-6d01e7a1d5-F3.json` mengukur sisi Damn-Bot (P12, first token TUI, buildtime); field `hermes` = `unavailable` karena checkout Hermes tidak punya runtime ter-pin (Python 3.14/deps absen; Node 22 **ada**). error informatif kini punya bahan suite `#390` yang sudah merged (`937fbddf`; 60/60 cek objektif; proxy 3,84/5) tetapi belum diratifikasi manusia (§2.2). Acceptance menuntut harness yang sama untuk kedua agent, jadi baris tetap `TERBUKA`.
2. **Error informatif.** PR `#390` sudah **MERGED** (`937fbddf`; final Windows rerun lulus). Cek objektif 60/60; skor proxy **3,84/5** (llm-proxy, dua rater; agreement a 78,3% b 71,7% c 68,3% d 98,3% e 81,7%; `net-tls` di-recapture, basis 59 kasus). Angka ini bukan ratifikasi manusia dan bukan gate; status ledger tetap `TERBUKA` menunggu 2 penilai manusia (§2.2).
3. **`setup` subperintah.** `secrets set|rotate|ls` (termasuk `rotate --all-profiles` dan matriks `ls --refs`) sudah ada dan diuji (ADR-223 + amandemennya), tetapi clone profil tanpa nilai (`TestProfileCloneNoSecretValues`) belum; `auth` → F5 (`GAP-SEC-18`, `SPEC.md:2904`), `ssh add` → F5 (`GAP-SEC-23`), `--sudo-rule` → F4 (`GAP-SEC-14`), `--uninstall` → F9 (`GAP-OPS-03`).
4. **Konflik fase CLI-064 `hermes uninstall`.** `FEATURE-PARITY.md:140` menulis F3, sedangkan `GAP-OPS-03`/`SPEC.md:2988` dan ADR-220 menempatkan `--uninstall` di F9. Ledger mengikuti SPEC + ADR.
5. **`ctrl+t`/`ctrl+w` tanpa arti normatif.** `SPEC.md:2556` hanya mendaftar tombol; `internal/tui` tidak mengimplementasikannya, sehingga tidak ada tes yang bisa dibuat tanpa keputusan perilaku.
6. **Queue/steer belum ada.** `SLASH-028`/`SLASH-029`/`SLASH-065` dan `AUTO-023` berfase F3; `internal/tui` belum punya antrean/steer (hanya `/plan`).
7. **`GAP-TOOL-10` residual.** Filter katalog + `skills.Store.Search` menegakkan `requires`, tetapi retriever prompt produksi masih `nil` (ADR-208 butir 5) → F5/F8.
8. **`GAP-SEC-16` residual.** `TestEgressDLP` menutup ingress; gerbang egress argumen tool 6 tool × 4 encoding di `internal/netx` belum ada. Fase penuh `SPEC.md:2902`: F2/F4/F5/F6/F7; bukan gate F3.
9. **ACP belum selebar SPEC §13.3.** `initialize` mengiklankan `loadSession:false`, `image:false`, `mcp http/sse:false` (ADR-219 butir 2), sedangkan `SPEC.md:2588` menulis true; `session/load`, `fs/*`, `terminal/*`, `set_model` belum ada.
10. **Profiles dan fixture.** `profiles` baru `ls` + `--profile`; export/import belum. Import memakai 5 fixture sintetis, bukan 5 home nyata teredaksi.

## 5. Divergensi dokumen (temuan, bukan tuduhan)

1. **ADR-194 butir 4 vs kode.** Wiring config sudah diperbaiki #372: `cmd/damnbot/limits_config.go` + `run.go:411` meneruskan `providers.custom` ke `registry.NewLimits`. Namun `provider.Route.ContextWindow` masih `cfg.Loop.TurnMaxInputTok` (`run.go:385`), sedangkan ADR-194 butir 4 (`DECISIONS.md:4145`) menyatakan `Limit.ContextWindow` **menggantikan** `loop.turn_max_input_tok`; komentar `compaction_wire.go` bahkan menyebut input cap tetap menjadi `Route.ContextWindow`. EC-CFG-007 tetap `LULUS` karena override-nya sampai ke resolver, tetapi klaim penggantian route belum benar.
2. **`SCORECARD.md` "diukur di F0" tanpa artefak.** Dimensi `SCORECARD.md:16-65` memakai sel itu, padahal artefak yang ada hanya codetools/budget/speed F0-F2 dan `bench/f3` (Damn-Bot side). Catatan `SCORECARD.md:3` hanya membaca ulang dimensi LLM sebagai RC F9; dimensi non-LLM berstatus F3 (startup prompt-ready, idle RSS, first token TUI, error informatif, build time) tidak punya angka F3 dua sisi.
3. **Kolom `status` FEATURE-PARITY tidak digate.** 854 baris berstatus `planned`/drop/keep; **0** `covered`, termasuk baris F1/F2 yang sudah punya tes. `TestParityPhaseColumn` (`internal/archtest/parity_phase_test.go:82`) hanya memeriksa kolom `fase`.
4. **Hitungan FEATURE-PARITY vs ADR-209.** Dokumen menulis F2 59 / F3 135 (`ROADMAP.md:31`); ADR-209 memindahkan CLI-040 + OPS-006 ke F3 dan CLI-038 ke F9 (F2 56 / F3 137), tetapi `FEATURE-PARITY.md:114,116,918` masih F2. Angka §6 adalah angka dokumen.
5. **P13 `minimal` tidak memangkas.** `make size-check` menghasilkan byte identik full dan minimal (19.271.842 B). Plafon lulus, tetapi gate tidak membedakan build.
6. **ADR-220 vs completion.** ADR-220 mencantumkan `completion` sebagai gap, tetapi #380 sudah mendarat dan tesnya lulus; ADR perlu amandemen.

## 6. Hitungan parity F3

Sumber: `docs/FEATURE-PARITY.md` pada `937fbddf`.

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

Catatan: beberapa baris punya kapabilitas lewat jalur lain (`/compress`→`/compact`,
`/branch`→`/fork`, `/skin`→`/theme`; `/btw` ada sebagai nama tetapi menolak;
`hermes chat`→bare `damnbot`). Baris itu tidak dihitung "punya implementasi
bernama sama"; hitungan tidak boleh dibaca sebagai "fitur hilang 91".

## 7. Rekomendasi

1. **F3 belum layak naik ke F4.** Gate katalog EC hijau (001..007 `covered`; 008 F5), tetapi baseline hermes F3, smoke `tern`, 16 GAP F3, dan deliverable profiles masih `TERBUKA`; `make phase-acceptance` dengan flip F4 belum lulus (lihat §8).
2. **EC-CFG selesai untuk F3.** 001..007 punya tes yang dijalankan; 008 di-re-fase ke F5 lewat ADR-226. Ini tidak boleh dipakai untuk mengklaim seluruh F3.
3. **Penghalang terbesar:** (a) baseline dua sisi + ratifikasi 2 penilai manusia untuk error informatif; (b) 16 GAP F3, terutama `GAP-REL-13` (fd ACP), `GAP-UX-01/03` (ACP fs/load), `GAP-OPS-04` (cross-profile), `GAP-UX-05/06` (TUI), `GAP-INF-04`; (c) smoke `tern` dan profiles.
4. **Syarat naik:** tutup atau re-fase 16 GAP lewat ADR (jangan memindahkan yang masih punya kewajiban F3), ratifikasi baseline/error, dokumentasikan smoke `tern`, lalu flip `PHASE=F4` dan jalankan `make phase-acceptance` sampai hijau; kembalikan `PHASE` ke F3 sampai semua bukti tersimpan.
5. **Parity F3 (91/135 tanpa permukaan)** adalah utang F3; jangan menandai `covered` tanpa tes. Paling tidak 50 baris SLASH dan 9 baris PLUG perlu keputusan re-fase ADR.

## 8. Kesiapan F4

**Kesimpulan: `PHASE` tidak boleh naik ke F4 pada snapshot ini.**

Syarat yang diminta: (a) tidak ada baris F3 `TERBUKA` yang wajib, (b) gate katalog hijau
saat flip `F4`, (c) `make phase-acceptance` lulus.

| syarat | hasil |
| --- | --- |
| Gate katalog flip F4 | **LULUS (terbatas)**: worktree sementara `b9247ef4` dengan `PHASE=F4`; `DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase ./internal/archtest` → ok. EC F3 001..007 covered; 008 F5. |
| `make phase-acceptance` penuh | **TIDAK LULUS di mesin ini**: rc=2; `internal/ctxdb` abort `runtime/cgo: pthread_create failed: Resource temporarily unavailable` di bawah `ulimit -v 4194304`. Ini batas sumber daya lokal, bukan kegagalan asersi; CI `test` pada tree identik lulus, tetapi job phase-acceptance tidak dijalankan di CI. |
| Tidak ada baris F3 wajib `TERBUKA` | **GAGAL**: baseline hermes F3, smoke `tern`, profiles, dan 16 GAP F3 masih `TERBUKA`. |
| PHASE | **Tetap `F3`.** |

Penghalang konkret: baseline hermes F3 (dua sisi + 2 penilai manusia), 15 GAP F3
(REL-13, GW-07, PROV-04, PROV-05, TOOL-01, UX-01..06, OPS-01, OPS-02, OPS-04,
OPS-09, INF-04), `tern acp` smoke, dan `config hooks` (F4, ADR-228). Setelah semuanya ditutup
atau di-re-fase lewat ADR, ulangi flip F4 + `make phase-acceptance` dan simpan
artefaknya sebelum menaikkan `PHASE`.

## 9. Audit ulang (perintah yang dijalankan)

Snapshot `937fbddf`, worktree `~/.cache/damnbot/wt-f3-ledger`. Pengukuran root/P12/buildtime dijalankan pada `b9247ef4` yang biner/tes root-nya identik (#390 hanya menambah modul `bench`). Perintah berat
memakai `nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1` dan `ulimit -v 4194304`;
lint memakai `GOMEMLIMIT=1500MiB`.

```bash
gh pr list --state open                        # 0
gh pr checks 390                               # final: ci-required pass; test 3 OS (Windows rerun lulus)
go test -tags "goolm,perf" -count=1 -run '^(TestStartupBudget|TestFirstTokenBudget|TestRenderBudget1000Blocks)$' ./internal/tui
# PASS; P12 in-process p95=309.043us; P17 delta p95=277.505us; R2 p95=1.523277ms
OUT=/tmp/f3-final.json RUNS=20 BUILD_RUNS=3 bench/f3/run.sh
# P12 p95=44.418ms; first token p95=17.473ms; buildtime 75.806/1.896s; error blocked
TAGS=goolm make size-check
# SIZE ok full 19271842 B / 52000000 B; minimal 19271842 B / 22000000 B
# worktree sementara b9247ef4 (root identik dengan 937fbddf) + PHASE=F4:
DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase ./internal/archtest
# ok (gate katalog)
make phase-acceptance
# rc=2; internal/ctxdb: pthread_create failed: Resource temporarily unavailable (batas ulimit lokal)
go test -tags goolm -count=1 ./internal/archtest
# ok
make loc
# rc=0, 0 GAGAL
```

**Tidak dijalankan:** smoke `tern` (tidak ada `tern` di mesin ini); update ke
GitHub Releases nyata (kontrak fake-server + aset bertanda tangan yang diuji);
soak/chaos; angka Q14. Angka performa lokal bukan bukti parity.
