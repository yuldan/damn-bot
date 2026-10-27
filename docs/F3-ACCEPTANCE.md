# F3 acceptance ledger

Acceptance F3 didefinisikan di [ROADMAP.md](ROADMAP.md) §F3 (`ROADMAP.md:143-154`),
[SPEC.md](SPEC.md) P12/P13/P17 (`SPEC.md:110-115`), §9.15 (`SPEC.md:982`),
§10 (`SPEC.md:1008`), §12 (`SPEC.md:2421`), §13.1 (`SPEC.md:2533`),
§13.3 (`SPEC.md:2567`), §18.2/§18.3 (`SPEC.md:2794-2817`), §22
(`SPEC.md:2881`), [EDGE-CASES-hermes.md](EDGE-CASES-hermes.md), dan
[FEATURE-PARITY.md](FEATURE-PARITY.md). Keputusan artefak: ADR-224
(`DECISIONS.md:4667`). Dokumen ini adalah artefak audit:
setiap baris memakai bukti yang **ada dan dijalankan**, bukan nama tes yang
dikarang dari judul PR.

**Snapshot audit:** `origin/main` **`5a851596`** (commit 2026-10-27 23:02 UTC /
2026-10-28 06:02 +0700), tree `cc064fb9`, `PHASE` = `F3`. Toolchain
`go1.26.10 linux/amd64`, workstation (bukan mesin referensi Q14).

**Verifikasi PR.** Daftar PR di konteks tugas tidak dipercaya apa adanya.
`gh pr view` pada 22 nomor: **20 MERGED** (`358-371`, `376-381`), **2 OPEN**
(`#374` D2b install/rollback, `#382` baseline F3). PR relevan lain juga
terbuka: `#372` (EC-CFG-007), `#375` (EC-CFG-004), `#383` (setup secrets).
PR `#384` (A8 keybindings, head `43ae4058`, tree `cc064fb9` — sama dengan
tree `5a851596`) sudah MERGED dan lulus CI run `40597498235`: `test` (3 OS),
`test-timing`, `extra`, `static`, `supply`, `bench`, `fuzz`, `ci-required`.
Push CI di `main` adalah tier `smoke`: `test`, `test-timing`, dan `extra`
**dilewati**, jadi run itu bukan bukti suite penuh.
Refresh terakhir (2026-10-27 UTC): `origin/main` = `5a851596`; `#374`,
`#382`, dan `#383` masih OPEN.

**Aturan bukti.**

| status | arti |
| --- | --- |
| `LULUS` | Tes/perintah ada, dijalankan pada snapshot ini atau CI dengan tree identik, dan lulus; lokasi dikutip. |
| `TERBUKA` | Belum ada bukti lulus; sisa kerja konkret dicantumkan. |
| `DITUNDA` | Di luar F3 dengan ADR + fase tujuan eksplisit; bukan "nanti". |

Angka performa di bawah diukur di workstation ini dan **bukan** angka mesin
referensi Q14; angka itu bukti eksekusi, bukan klaim parity. Nama tes yang
disebut ROADMAP/SPEC tetapi tidak ada di kode dicatat sebagai temuan, bukan
diam-diam diganti.

## Ringkasan

| grup | baris | LULUS | TERBUKA | DITUNDA |
| --- | ---: | ---: | ---: | ---: |
| Deliverable F3 (`ROADMAP.md:145`) | 12 | 10 | 2 | 0 |
| Acceptance F3 (`ROADMAP.md:148-154`) | 12 | 8 | 4 | 0 |
| GAP gate F3 (`ROADMAP.md:234`, 20) | 20 | 3 | 16 | 1 |

## 1. Deliverable F3

| deliverable | status | bukti | sisa kerja |
| --- | --- | --- | --- |
| `tui` | LULUS | `internal/tui` (ADR-199); wiring `cmd/damnbot/tui_wire.go:24`; suite `TestTeatestInputRoundTrip` `internal/tui/input_e2e_test.go:83`, `TestTabCyclesRegistry` `model_test.go:395`, `TestNavMovesAndFolds` `nav_test.go:43`, `TestTabsRenderAndSwitch` `tabs_test.go:12`, `TestPaletteFiltersAndRunsSlash` `palette_test.go:13`, `TestBangRendersToolBlock` `bang_test.go:9`, `TestR7GoldenViews` `golden_test.go:58`, `TestRenderBudget1000Blocks` `render_perf_test.go:21` (p95 1,48277 ms ≤ 16 ms); P17 §2 lulus, P12 §2 `TERBUKA` | SPEC §13.1 masih kurang external editor `/prompt`, notify/bell, dan arti `ctrl+t`/`ctrl+w`; lihat §6 |
| `acp` | LULUS | `internal/acp` (ADR-201/205/219); server `cmd/damnbot/acp_wire.go:75`; golden `TestACPGoldenPrompt` `acp_wire_test.go:121` + fixture `internal/acp/testdata/golden.jsonl`; e2e `TestACPStdio` `internal/e2e/acp_test.go:11` | smoke `tern` manual, `session/load`, `fs/*`, `terminal/*`, capability image/audio/mcp belum ada (ADR-219 konsekuensi) |
| `config` | LULUS | `cmd/damnbot/config_cli.go:34`; `show/get/explain/schema/edit` + `profiles ls` (ADR-214); `config_version` (ADR-221) `internal/config/validate.go:18`; tes `TestConfigShowMasksSecrets` `config_cli_test.go:73`, `TestConfigVersionMarker` `internal/config/version_test.go:20` | `config modules`, `config hooks`, `profiles export|import` (ADR-214 butir 7) |
| `damnbot setup` (rekomendasi provider) | LULUS | `cmd/damnbot/setup_cli.go:36`; tiga langkah (`:80` `1/3`), deteksi nama env kredensial tanpa nilai, tulis 2 baris `:134`; tes `TestSetupFreshYes` `cmd/damnbot/setup_cli_test.go:34` + e2e `internal/e2e/setup_test.go:30` | rekomendasi model otomatis tidak ada karena SPEC §4.1 melarang model hardcoded (ADR-220); `auth`/`secrets`/`ssh`/`--sudo-rule`/`--uninstall` gap (§4) |
| plan mode | LULUS | `TestPlanModeShell` `internal/exec/classify/policy_test.go:227`; enforcement `TestPlanModeShellDispatch` `internal/tools/plan_mode_test.go:54`, `TestPlanModeNoSideEffects`; wiring TUI `tui_wire.go:145`, ACP `acp_wire.go:210`; ADR-203 | flag `run --plan` (SPEC §10) belum ada di `cmd/damnbot/run.go:119-135`; plan mode hanya tersedia lewat TUI/ACP, bukan `run` |
| preview aksi berbahaya | LULUS | `TestPreviewBeforeDangerous` `internal/tools/preview_test.go:101` (69 kasus, 0 proses), batas 3.500 rune (ADR-204); modal TUI `internal/tui/approval.go:16,133` + `TestApprovalPreviewThenAllowOnce` `approval_test.go:29`; ADR-202/204 | diff unified yang membaca workspace tetap di luar F3 (ADR-204 konsekuensi) |
| slash/CLI inti | LULUS | registry CLI `cmd/damnbot/surface.go:52` (22 entri, `TestSurfaceRegistry` `surface_test.go:65`); 16 slash berfungsi + `/btw` yang menolak `internal/tui/slash.go:43-61`; tes `slash_test.go:89,125`, `config_slash_test.go:34`, `slash_a7_test.go:29,80,114,142` | 50/59 baris SLASH F3 tidak punya slash bernama sama; alias NP-06 dan queue/steer belum (§6) |
| `importer` | LULUS | `internal/importer/importer.go:260`; CLI `cmd/damnbot/import_cli.go:35`; 5 fixture `TestImportFiveFixturesCoverage` `sources_test.go:141`, `TestImportAccounted` `importer_test.go:179`, `TestImportIdempotent` `:199`; e2e `TestImportHermesWritesSkillsAndMemory` `internal/e2e/import_test.go:117` + `TestImportHermesIdempotent` `:172`; ADR-200 | fixture adalah testdata sintetis, bukan 5 home `~/.hermes` nyata yang diredaksi (SPEC §9.15) |
| profiles | TERBUKA | dasar: `--profile` + `config profiles ls` `cmd/damnbot/config_cli.go:321`, `TestConfigProfilesLs` `config_cli_test.go:188`; ADR-214 butir 6 | `profiles export|import` dan permukaan `damnbot profile` (15 sub, parity CLI-066) tidak ada; fase tetap F3, ADR-214 menyebut menunggu format arsip |
| release pipeline | LULUS | `.github/workflows/release.yml:53` (dua build byte-identik + SBOM), `:141` ed25519; `scripts/release-test.sh:1`; `make release-test` `Makefile:224`; unit `TestSignVerifyRoundTrip` `tools/release/main_test.go:82`; dieksekusi lokal: `RELEASE TEST ok` | workflow tag `v*` belum pernah dijalankan (0 run); bukti reproducible baru pada perintah workflow, bukan artefak rilis |
| self-update + rollback | TERBUKA | check bertanda tangan + kanal: `internal/update/update.go:49`, `TestCheckAvailable` `update_test.go:102`, `TestCheckRejectsBadSignature` `:129`; CLI `cmd/damnbot/update.go:22` | install/rollback = D2b, belum di `main` (`update.go:21,36`); PR `#374` OPEN |
| `LICENSE` MIT | LULUS | `LICENSE:1` "MIT License" | — |

## 2. Acceptance F3

| acceptance | status | bukti / angka | sisa kerja |
| --- | --- | --- | --- |
| P12 first frame ≤ 100 ms | TERBUKA | `TestStartupBudget` `internal/tui/startup_perf_test.go:18` (tag `perf`, 20 run): p50 **239,492 µs**, p95 **710,36 µs** — lulus untuk jalur milik TUI (`NewApp`+`View`) | komentar tes sendiri mengecualikan exec proses dan `init()` ("covered by the binary startup job"), tetapi **tidak ada job/tes itu** (`TestInitTrace`/benchmark startup: 0 hasil); `damnbot version` ≤ 20 ms juga belum diukur. Sisa kerja: harness **spawn→frame pertama** lewat PTY (pola `bench/f3`), p95 ≤ 100 ms, tanpa `t.Skip` |
| P13 build `minimal` ≤ 22 MB | LULUS | `make size-check` (lokal, `TAGS=goolm`): full **19.189.922 B / 52.000.000 B**; minimal (`goolm,minimal`) **19.189.922 B / 22.000.000 B** | minimal menghasilkan byte identik dengan full; plafon lulus, tetapi tag `minimal` belum memangkas apa pun — dicatat sebagai observasi §5 |
| P17 first token TUI ≤ TTFT + 30 ms | LULUS | `TestFirstTokenBudget` `internal/tui/p17_perf_test.go:47` (20 run, fake TTFT 25 ms): headless p95 26,195112 ms, TUI p95 **237,31 µs**, delta **237,31 µs** ≤ 30 ms | — |
| ACP golden transcript | LULUS | `TestACPGoldenPrompt`/`TestACPGoldenProtocolErrors` `cmd/damnbot/acp_wire_test.go:120,128`; fixture `internal/acp/testdata/golden.jsonl`, `golden-errors.jsonl`; ADR-219 | — |
| `tern acp` manual smoke | TERBUKA | tidak ada transkrip smoke di repo; satu-satunya penyebutan `tern acp` adalah target (`ROADMAP.md:149`) dan ADR-201 butir 8 yang menyatakan belum diklaim | jalankan `tern acp damnbot` di mesin dengan `tern`, simpan transkrip + versi |
| Wizard ≤ 3 langkah, ≤ 60 s, config 2 baris | LULUS | `runSetup` `setup_cli.go:36` mencetak `1/3`, `3/3`; tulis `[provider]\nmodel = "…"\n` `:134`; unit `TestSetupFreshYes` `setup_cli_test.go:34`; e2e `TestSetupFreshYes` `internal/e2e/setup_test.go:30` mengasersi ≤ 60 s, byte config persis, mode 0600, tanpa bocor secret | — |
| Import 5 fixture ≥ 99%, 0 silent loss, idempoten | LULUS | `TestImportFiveFixturesCoverage` `internal/importer/sources_test.go:141` (5 fixture, tiap `Coverage() ≥ 0.99`, `Imported+Skipped == Total`, total ≥ 0.99); `TestImportAccounted` `importer_test.go:179` (setiap skip punya source/key/reason); `TestImportIdempotent` `:199` (run kedua `Changed()==0`) | cakupan pemetaan config sempit (5 key) dan fixture sintetis; bukan 5 home nyata |
| `TestPlanModeShell` | LULUS | `TestPlanModeShell` `internal/exec/classify/policy_test.go:227`; dispatch `TestPlanModeShellDispatch` `internal/tools/plan_mode_test.go:54`; dijalankan lokal: PASS | — |
| EC-CFG-001..008 | TERBUKA | 4/8 covered (sub-tabel) | EC-CFG-002/004/007/008 belum covered |
| EC-FIO-007 | LULUS | `TestConfigBOM` `internal/config/bom_test.go:14` (`g.Item(... "covered")`), `TestCompileBOM` `internal/schema/bom_test.go` | — |
| EC-FIO-018 | LULUS | `TestFrameSplitOnLFOnly` `internal/acp/frame_test.go:36`; kontrol negatif splitter; `TestFrame100MBBounded` `:132` (100 MB → `-32600`, heap +1 KiB) | — |
| Baseline hermes F3 | TERBUKA | tidak ada `bench/f3/` dan tidak ada `bench/**/results/hermes-868b9f244f-F3.json`; artefak yang ada berhenti di F2 (`bench/f2/results/hermes-868b9f244f-F2.json`) | harness + artefak F3 (PR `#382` OPEN); dimensi F3 ADR-065: startup prompt-ready/idle RSS (F0 masih `null`), first token TUI, error informatif, build time; error informatif butuh **2 penilai manusia** (`SCORECARD.md:68`) |

### 2.1 EC-CFG

| EC | status | bukti | catatan |
| --- | --- | --- | --- |
| EC-CFG-001 | LULUS | `TestConfigStaleEnvDoesNotOverrideFile` `internal/config/edgecases_cfg_test.go:23` | `.env` basi tidak mengalahkan `config.toml` |
| EC-CFG-002 | TERBUKA | `/reload` TUI ada (`internal/tui/slash_a7_test.go:29`) | belum ada item katalog/tes untuk reload setting gateway; komponen gateway F4 |
| EC-CFG-003 | LULUS | `TestRunStartupNoNetworkProbe` `cmd/damnbot/edgecases_cfg_test.go:62` | 0 request sebelum turn |
| EC-CFG-004 | TERBUKA | 0 tes netx happy-eyeballs; PR `#375` OPEN | dual-stack race belum ada |
| EC-CFG-005 | LULUS | `TestConfigVersionMarker` `internal/config/version_test.go:20`; `validate.go:18`; ADR-221 | legacy/0/1/2/99/-1 |
| EC-CFG-006 | LULUS | `TestCompactorConstructionNoProbe` `cmd/damnbot/edgecases_cfg_test.go:96` | 0 request, fallback window |
| EC-CFG-007 | TERBUKA | `context_length` diparse (`internal/config/config.go:206`) tetapi `registry.NewLimits(nil, nil, …)` di `cmd/damnbot/run.go:397`; PR `#372` OPEN | override belum sampai ke `Limits`; lihat §5 |
| EC-CFG-008 | TERBUKA | 0 tes pool kredensial; pool/provider switch F5 | belum ada ADR re-fase, jadi bukan `DITUNDA` |

## 3. GAP gate F3 (ROADMAP:234, 20 gap)

ROADMAP menyatakan acceptance tiap fase bertambah dengan **semua** gap berfase
itu (`ROADMAP.md:228`). Baris di bawah adalah audit permukaan, bukan klaim
bahwa requirement penuh sudah diverifikasi.

| GAP | status | bukti / kekurangan |
| --- | --- | --- |
| GAP-REL-13 | TERBUKA | `cmd/damnbot/acp_wire.go:97` memakai `os.Stdin` langsung; tidak ada duplikasi fd `O_CLOEXEC`, tidak ada `TestACPChildStdinIsolation` di kode |
| GAP-GW-07 | TERBUKA | 0 simbol `tos_risk`/`tos_note`; hub skill/MCP belum ada |
| GAP-PROV-04 | TERBUKA | importer memakai `go.yaml.in/yaml/v3` (`internal/importer/config.go:7`), tanpa resolver YAML 1.1 |
| GAP-PROV-05 | TERBUKA | 0 simbol `byterover`/`retaindb`; pemetaan + warning belum ada |
| GAP-TOOL-01 | TERBUKA | 0 tool `doc_read`/`doc_write` |
| GAP-TOOL-10 | DITUNDA | updater: `TestNativeArchSelect` `internal/update/update_test.go:223`; filter katalog: `internal/skills/requires.go`; residual retriever prompt **nil** (ADR-208 butir 5) → F5 (MCP `requires`) / F8 (hub) per ADR-193 |
| GAP-UX-01 | TERBUKA | tidak ada method `fs/read_text_file`/`fs/write_text_file` (ADR-219) |
| GAP-UX-02 | TERBUKA | `session/new` mengiklankan 4 mode (`acp_wire.go:178`), `set_mode` ada (`:245`), tetapi tidak ada tes yang memilih **setiap** mode yang diiklankan; `set_model` belum ada |
| GAP-UX-03 | TERBUKA | `session/load` belum ada; permintaan itu jatuh ke `-32601`, bukan `-32002` (ADR-219) |
| GAP-UX-04 | TERBUKA | `embeddedContext: true` ada (`acp_wire.go:138`), tetapi tidak ada tes parser inbound toleran / resource |
| GAP-UX-05 | TERBUKA | 0 tes keystroke→echo p99 dan SIGWINCH coalesce |
| GAP-UX-06 | TERBUKA | 0 penanganan `NO_COLOR`/`TERM=dumb`/`--plain` di `internal/tui`; renderer tunggal belum ada |
| GAP-OPS-01 | TERBUKA | `show/get/explain/schema/edit` lulus (ADR-214), tetapi `config modules`/`hooks` belum ada (ADR-214 butir 7) |
| GAP-OPS-02 | TERBUKA | 0 mode privasi `[network] background`/notice background |
| GAP-OPS-04 | TERBUKA | `setup secrets` tidak ada di `main` (ADR-220); PR `#383` OPEN |
| GAP-OPS-06 | LULUS | `internal/doctor` ID `DOC-NNN` (`doctor.go:34,81`) + `damnbot.doctor/v1` (`:21`); `TestRunHealthy` `doctor_test.go:57`, `TestReportJSON` `:125`; ADR-217 |
| GAP-OPS-07 | LULUS | `TestCompletionScripts` `cmd/damnbot/completion_cli_test.go:8` (4 shell + `__complete`), `TestCompleteCandidates` `:24`; ADR-222 |
| GAP-OPS-08 | LULUS | 22 entri `surface()` `cmd/damnbot/surface.go:52`; `TestSurfaceRegistry` `surface_test.go:65` (satu entri per baris SPEC §10, nama unik, fase) |
| GAP-OPS-09 | TERBUKA | 0 kebijakan notice/`[ui] notices`; 0 tes frekuensi notice |
| GAP-INF-04 | TERBUKA | `CHANGELOG.md` belum punya bagian `Breaking`/`Migrasi` yang diwajibkan; 0 gate |

## 4. Gap jujur

1. **Baseline hermes F3 tidak ada.** `bench/f3/` tidak ada; artefak terakhir
   adalah F2 (`bench/f2/results/hermes-868b9f244f-F2.json`). PR `#382`
   (harness baseline F3) OPEN. Dimensi F3 ADR-065 yang masih kosong:
   `startup_prompt_ready` dan `idle_rss` bernilai `null` di
   `bench/speed/results/hermes-868b9f244f-nollm.json` (butuh fixture PTY TUI
   2.000 event), first token TUI, build time, dan **error informatif**.
   Error informatif memakai rubrik biner **2 penilai manusia** (`SCORECARD.md:68`);
   belum ada penilai, jadi baris itu `TERBUKA` dan tidak boleh diisi angka.
2. **Reproduksi sisi hermes di mesin ini tidak identik dengan F0.** Artefak
   F0 mencatat `Python 3.14.7` (`docs/F0-REPORT.md:3`); mesin ini hanya punya
   `python3.12.3` (tidak ada `python3.14`) dan Node 22. Sumber hermes
   `~/.cache/damnbot/f2-inputs/hermes-src` ada, dan artefak F2 terukur dengan
   Python 3.12.3, tetapi angka F0 (startup/RSS) tidak dapat direproduksi
   apa adanya tanpa fixture/harness F3. Klaim "mesin ini tidak bisa mengukur
   hermes karena Node absen" **tidak terbukti**: Node 22 ada.
3. **`setup` subperintah.** `auth` → F5 (`GAP-SEC-18`, `SPEC.md:2904`; ADR-220
   mencatat sebagai gap); `ssh add` → F5 (`GAP-SEC-23`); `--sudo-rule` → F4
   (`GAP-SEC-14`, subset F1/penuh F4); `--uninstall` → F9 (`GAP-OPS-03`;
   ADR-220). Semuanya `DITUNDA` ke fase itu, kecuali `secrets` yang tetap F3
   (`GAP-OPS-04`, `ROADMAP.md:234`) dan kini `TERBUKA` sampai `#383` mendarat.
   ADR-220 menyebut `completion` sebagai gap; itu sudah usang karena `#380`
   mendarat dan `TestSetupCompletion`/`TestCompletionScripts` lulus.
4. **Konflik fase CLI-064 `hermes uninstall`.** `FEATURE-PARITY.md:140`
   menulis fase **F3**, sedangkan `GAP-OPS-03`/`SPEC.md:2988` dan ADR-220
   menempatkan `--uninstall` di **F9**. Ledger ini mengikuti SPEC + ADR;
   baris parity itu perlu dikoreksi pemiliknya.
5. **`ctrl+t`/`ctrl+w` tidak punya arti normatif.** `SPEC.md:2556` hanya
   mendaftar tombol tanpa semantik; kode `internal/tui` tidak
   mengimplementasikannya. Tidak ada tes yang bisa dibuat tanpa keputusan
   perilaku. Item ini tidak dihitung LULUS.
6. **Queue/steer belum ada.** `SLASH-028` (`/queue`), `SLASH-029` (`/steer`),
   `SLASH-065` (`/busy`), dan `AUTO-023` berfase F3, tetapi `internal/tui`
   tidak punya antrean/steer; hanya `/plan` yang ada. Ini juga berarti R6
   (queue/steer) belum tertutup.
7. **`GAP-TOOL-10` residual.** Filter katalog + `skills.Store.Search` sudah
   menegakkan `requires`, tetapi retriever prompt produksi masih `nil`
   (ADR-208 butir 5). Target fase residual: F5 (MCP `requires`) dan F8 (hub).
8. **`GAP-SEC-16` residual.** `TestEgressDLP` menutup ingress; gerbang
   egress argumen tool 6 tool × 4 encoding di `internal/netx` belum ada.
   Fase penuh menurut `SPEC.md:2902`: F2/F4/F5/F6/F7; ini bukan gate F3,
   tetapi tetap P1 untuk rilis.
9. **ACP belum selebar SPEC §13.3.** `initialize` mengiklankan
   `loadSession:false`, `image:false`, `mcp http/sse:false`
   (ADR-219 butir 2), sedangkan `SPEC.md:2574-2576` menulis `loadSession:true`,
   image true, mcp true; `session/load`, `fs/*`, `terminal/*`, dan `set_model`
   belum ada. Golden transcript hanya menutup 4 frame sukses + 3 error.
10. **Profiles dan fixtures.** `profiles` baru `ls` + `--profile`; export/import
    belum. Import memakai 5 fixture sintetis (bukan 5 home nyata teredaksi).
11. **P12 dan self-update.** P12 hanya terukur di jalur in-process; tidak ada
    job startup biner/`TestInitTrace`. Sisa kerja konkret: harness spawn→frame
    pertama lewat PTY (pola `bench/f3`) dengan p95 ≤ 100 ms dan tanpa `t.Skip`.
    Self-update baru `--check`; install dan rollback ada di PR `#374` yang
    belum merge.

## 5. Divergensi dokumen (temuan, bukan tuduhan)

1. **ADR-194 butir 4 vs kode.** ADR-194 butir 4 (`DECISIONS.md:4145`) menyatakan
   `newRunLimits` membangun `registry.Limits` dari `providers.custom` +
   registry + storage, dan `Limit.ContextWindow` **menggantikan**
   `loop.turn_max_input_tok`. Di `main`, composition root memanggil
   `registry.NewLimits(nil, nil, limitstore.Store{...}, nil)`
   (`cmd/damnbot/run.go:397`; sama di `compaction_wire.go:286`), dan
   `provider.Route.ContextWindow` masih `cfg.Loop.TurnMaxInputTok`
   (`run.go:381`). `context_length` diparse (`internal/config/config.go:206`)
   tetapi tidak diteruskan. Ini konsisten dengan EC-CFG-007 yang masih
   `TERBUKA` dan PR `#372` yang belum merge.
2. **`SCORECARD.md` "diukur di F0" tanpa artefak.** Dimensi di
   `SCORECARD.md:16-65` diberi sel "diukur di F0", padahal artefak yang ada
   hanya `bench/codetools/results/f0-baseline.json`,
   `bench/budget/results/hermes-868b9f244f.json`,
   `bench/speed/results/hermes-868b9f244f-nollm.json`, dan artefak F1/F2.
   Catatan di `SCORECARD.md:3` hanya membaca ulang dimensi LLM sebagai "RC F9"
   dan dimensi berstatus `Fx` menurut ADR-065; dimensi tanpa LLM yang masih
   F3 (startup prompt-ready, idle RSS, first token TUI, error informatif,
   build time) tidak punya angka F3. Tidak ada artefak F3 di repo.
3. **Kolom `status` FEATURE-PARITY tidak dipakai gate.** Seluruh 854 baris
   berstatus `planned` atau drop/keep; **0** baris `covered` — termasuk baris
   F1/F2 yang sudah punya tes. `TestParityPhaseColumn`
   (`internal/archtest/parity_phase_test.go:82`) hanya memeriksa kolom `fase`,
   bukan `status`. Jadi "135 planned" bukan ukuran kemajuan.
4. **Hitungan F3 di FEATURE-PARITY vs ADR-209.** Dokumen menulis F2 59 / F3
   135 (`ROADMAP.md:31`), sedangkan ADR-209 memindahkan CLI-040 + OPS-006 ke
   F3 dan CLI-038 ke F9 (F2 56 / F3 137). Di `FEATURE-PARITY.md:114,116,918`
   ketiga baris masih F2; F2-ACCEPTANCE §3.2 sudah memakai F3. Angka 135 di
   §6 adalah angka dokumen, bukan angka ADR.
5. **P13 `minimal` tidak memangkas.** `make size-check` menghasilkan byte
   identik untuk full dan `goolm,minimal` (19.189.922 B). Plafon lulus, tetapi
   gate tidak membedakan build; indikasi tag `minimal` belum mengeluarkan
   modul apa pun. Bukan kegagalan acceptance, tetapi metriknya tidak
   diskriminatif.
6. **ADR-220 vs completion.** ADR-220 (2026-10-27) mencantumkan `completion`
   sebagai gap, tetapi `#380` (`d9efd901`) sudah mendarat dan tesnya lulus.
   ADR itu perlu amandemen, bukan dianggap status sekarang.

## 6. Hitungan parity F3

Sumber: `docs/FEATURE-PARITY.md` pada `5a851596`.

- Baris berfase **F3 = 135** (SLASH 59, CLI 23, OPS 15, UI 14, PLUG 9,
  CORE 5, MEM 4, SKILL 3, AUTO 3), cocok dengan `ROADMAP.md:31`.
- Menurut dokumen: **0 `covered`, 135 `planned`.** Kolom status tidak
  di-update dan tidak digate (§5.3).
- Menurut kode: **44 baris punya permukaan bernama sama** (9 SLASH, 10 CLI,
  10 OPS termasuk 2 sebagian, 8 UI, 3 CORE, 3 MEM, 1 AUTO); **91 baris tidak
  punya implementasi bernama sama.** Angka ini inventaris nama/permukaan,
  **bukan** klaim parity selesai; tidak ada satu pun baris F3 yang punya tes
  parity khusus.
- Jika ADR-209 diterapkan, F3 menjadi 137 (tambah CLI-040 + OPS-006); CLI-040
  (`import hermes`) sudah punya CLI, OPS-006 baru sebagian (`sessions backup`
  F2 + import).

Baris F3 yang **belum** punya implementasi bernama sama (91):

- **SLASH (50):** 004, 005, 006, 007, 008, 009, 011, 013, 015, 016, 017, 019,
  020, 024, 025, 028, 029, 032, 038, 042, 044, 045, 046, 050, 051, 052, 054,
  055, 057, 058, 059, 061, 062, 065, 066, 068, 069, 070, 071, 074, 083, 085,
  086, 087, 088, 090, 097, 100, 101, 103.
- **CLI (13):** 002, 022, 031, 033, 035, 043, 044, 048, 064, 066, 071, 072,
  075.
- **OPS (5):** 002, 005, 012, 017, 025.
- **UI (6):** 009, 013, 025, 026, 027, 028.
- **PLUG (9):** 001, 002, 003, 004, 005, 006, 007, 008, 010.
- **CORE (2):** 006, 016.
- **MEM (1):** 036 (`/btw` sengaja ditolak: `internal/tui/actions.go:85`).
- **SKILL (3):** 003, 005, 006.
- **AUTO (2):** 022, 023.

Catatan: beberapa baris di atas punya kapabilitas lewat jalur lain (mis.
`/compress` → `/compact`, `/branch` → `/fork`, `/skin` → `/theme`; `/btw` ada
sebagai nama tetapi menolak (`internal/tui/actions.go:85`); `hermes chat` →
bare `damnbot`). Baris-baris itu tidak dihitung "punya implementasi bernama
sama" karena nama/wire parity-nya tidak ada; hitungan tidak boleh dibaca
sebagai "fitur hilang 91".

## 7. Rekomendasi

1. **F3 belum layak naik ke F4.** `PHASE=F4` akan menuntut 10 item EC F3
   `covered` (ADR-195 butir 3/ADR-176); EC-CFG-002/004/007/008 belum, jadi
   `make phase-acceptance` akan gagal secara desain, bukan karena flake.
2. Penghalang terbesar: (a) baseline hermes F3 + artefak `-F3.json` belum ada
   (`#382`) dan error informatif butuh 2 penilai manusia; (b) empat EC-CFG
   terbuka, khususnya EC-CFG-007 karena wiring `Limits` belum memakai config
   (`#372`); (c) GAP gate F3 16/20 terbuka, termasuk P1 `GAP-REL-13` (fd
   ACP), `GAP-OPS-04` (secrets, `#383`), dan `GAP-UX-01/03` (ACP fs/load).
3. Syarat minimum naik: tutup EC-CFG-002/004/007/008 + `#383` + D2b
   (`#374`), hasilkan artefak baseline F3 (dengan dua penilai untuk error
   informatif), tambahkan harness P12 spawn→frame PTY (p95 ≤ 100 ms, tanpa
   `t.Skip`), lalu jalankan flip `PHASE=F4` untuk membuktikan gate tidak
   vakum, dan kembalikan ke F3.
4. Jangan menaikkan `PHASE` dengan menandai `covered` tanpa tes, atau dengan
   menghapus id EC; itu melanggar ADR-176/195 dan aturan ledger ini.
5. Parity F3 (91/135 tanpa permukaan) adalah utang F3 yang harus dicatat,
   bukan diubah menjadi `covered`; paling tidak 50 baris SLASH dan 9 baris
   PLUG perlu keputusan re-fase ADR bila tidak dikerjakan di F3.

## 8. Audit ulang (perintah yang dijalankan)

Snapshot `5a851596`, worktree `~/.cache/damnbot/wt-f3-ledger`. Perintah berat
memakai `nice -n 19 env GOMAXPROCS=2 GOFLAGS=-p=1` dan `ulimit -v 4194304`;
lint memakai `GOMEMLIMIT=1500MiB`.

```bash
gh pr view <n> --json number,state,mergeCommit   # 22 nomor: 20 MERGED, #374/#382 OPEN
gh run view 40597498235 --json jobs              # PR #384, tree cc064fb9 == 5a851596
go test -tags "goolm,perf" -count=1 -run '^(TestStartupBudget|TestFirstTokenBudget|TestRenderBudget1000Blocks)$' ./internal/tui
# PASS; P12 p95=710.36µs; P17 delta p95=237.31µs; R2 p95=1.48277ms
TAGS=goolm make size-check
# SIZE ok full 19189922 B / 52000000 B; minimal 19189922 B / 22000000 B
go test -tags goolm -count=1 -run '^(TestPlanModeShell|TestShellAllowExactMatch)$' ./internal/exec/classify
# PASS
go test -tags goolm -count=1 -run '^(TestConfigStaleEnvDoesNotOverrideFile|TestConfigVersionMarker|TestConfigBOM)$' ./internal/config
# PASS
go test -tags goolm -count=1 -run '^(TestFrameSplitOnLFOnly|TestFrame100MBBounded)$' ./internal/acp
# PASS; 100 MB -> -32600, heap +1 KiB
go test -tags goolm -count=1 -run '^(TestImportFiveFixturesCoverage|TestImportIdempotent|TestImportAccounted)$' ./internal/importer
# PASS
go test -tags goolm -count=1 -run '^(TestPreviewBeforeDangerous|TestPlanModeShellDispatch)$' ./internal/tools
# PASS; dangerous corpus 69 cases
go test -tags goolm -count=1 -run '^(TestSetupFreshYes|TestACPGoldenPrompt|TestACPGoldenProtocolErrors|TestRunStartupNoNetworkProbe|TestCompactorConstructionNoProbe)$' ./cmd/damnbot
# PASS
go test -tags goolm -count=1 -run '^(TestACPStdio|TestSetupFreshYes|TestImportHermesIdempotent|TestImportHermesWritesSkillsAndMemory)$' ./internal/e2e
# PASS
TAGS=goolm make release-test
# RELEASE TEST ok
go test -tags goolm -count=1 -run '^(TestBangRendersToolBlock|TestBangFailureAndPlan|TestBangRefusals|TestPaletteFiltersAndRunsSlash|TestPaletteEscAndUnknown|TestPaletteUsesSlashPath|TestTabsRenderAndSwitch|TestTabsRefusals|TestR7GoldenViews)$' ./internal/tui
# PASS
go test -tags goolm -count=1 -run '^(TestBangNoApproverDenies|TestBangApprovedRuns|TestBangPlanModeDenies|TestBangApprovalTimeoutDenies)$' ./cmd/damnbot
# PASS
```

**Tidak dijalankan:** suite `./...` penuh lokal (dipakai CI PR `#384` pada tree
identik); `make phase-acceptance` dengan `PHASE=F4` (sengaja, karena akan
merah); smoke `tern`; build tag rilis; soak/chaos. Angka performa lokal bukan
angka Q14 dan tidak dipakai untuk mengklaim parity.
