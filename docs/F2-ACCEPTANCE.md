# F2 acceptance ledger

Acceptance F2 didefinisikan di [ROADMAP.md](ROADMAP.md) §F2,
[EDGE-CASES-hermes.md](EDGE-CASES-hermes.md), dan scope GAP di
[ADR-193](DECISIONS.md) + SPEC §22.1. Dokumen ini adalah artefak review:
setiap baris EC/GAP/parity F2 muncul sekali, dengan bukti yang **ada dan
dijalankan**, bukan nama tes yang dikarang.

**Snapshot audit:** `origin/main` `f3c0af68` (2026-10-27). Semua angka di
bawah adalah hasil eksekusi pada snapshot itu (atau job CI yang tree-nya
identik, disebut per baris), bukan target. Setelah snapshot, #345/ADR-208
mendarat (`7a196a28`); baris `GAP-TOOL-10` diperbarui ke keadaan itu tanpa
audit ulang penuh. Status hanya tiga nilai:

| status | arti |
| --- | --- |
| `LULUS` | Tes ada, dijalankan pada snapshot (atau CI dengan tree identik), dan lulus; berkas/nama tes dikutip. |
| `TERBUKA` | Belum ada bukti yang lulus; sisa kerja eksplisit dicantumkan. |
| `DITUNDA` | Di luar F2 dengan ADR + batas fase eksplisit; bukan "nanti". |

Aturan bukti: satu baris tidak boleh `LULUS` hanya karena katalog
mendeklarasikannya. Nama tes yang tidak ada adalah cacat ledger; §7
mencantumkan perintah audit ulang. Status CI tidak dipakai sebagai bukti:
push `e727ee1a` merah di `lint` (`cmd/damnbot/sessions.go:312`, direktif
`//nolint:gosec` tidak terpakai) dan perbaikannya ada di PR #346 yang belum
merge saat snapshot ini diambil. Ledger ini tidak mengklaim CI hijau.

## Ringkasan

| grup | baris | LULUS | TERBUKA | DITUNDA |
| --- | --- | --- | --- | --- |
| EC wajib ROADMAP F2 | 41 | 41 | 0 | 0 |
| GAP gate F2 (ADR-193, 13) | 13 | 12 | 1 (`GAP-COST-01`) | 0 |
| GAP di luar gate F2 | 7 | 0 | 3 | 4 |
| Perf F2 (2 metrik, §2b) | 2 | 0 | 2 | 0 |
| Parity F2 setelah re-fase ADR-209 | 56 | 55 | 1 (`SKILL-016`) | 0 |
| Parity re-fase ADR-209 | 3 | 0 | 2 (F3) | 1 (F9) |
| Parity pindah fase ADR-139 (bukan F2) | 22 | — | — | — |

Jadi parity F2 = **55/56 LULUS**; satu-satunya baris F2 yang `TERBUKA`
adalah `SKILL-016`. Tiga baris lain (CLI-040/OPS-006/CLI-038) sudah bukan F2
menurut ADR-209.

`EC-TCALL-010` dihitung `LULUS` karena katalognya `n/a` beralasan dan
ADR-080 butir 3 menghitung `n/a` beralasan sebagai `covered`; bukti
run-level-nya tetap wajib saat modul pertama ter-wire (F5).

## 1. Edge cases wajib ROADMAP F2

| EC | tes | lokasi | status | owner |
| --- | --- | --- | --- | --- |
| EC-CTX-001 | `TestEdgeCases/EC-CTX-001` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-002 | `TestEdgeCases/EC-CTX-002` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-003 | `TestEdgeCases/EC-CTX-003` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-004 | `TestEdgeCases/EC-CTX-004` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-005 | `TestEdgeCases/EC-CTX-005` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-006 | `TestEdgeCases/EC-CTX-006` | `internal/compaction/edgecases_test.go:18`; `ecCtx006` `prune_test.go:303` | LULUS | compaction |
| EC-CTX-007 | `TestEdgeCases/EC-CTX-007` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-008 | `TestEdgeCases/EC-CTX-008` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-009 | `TestEdgeCases/EC-CTX-009` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-010 | `TestEdgeCases/EC-CTX-010` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-011 | `TestEdgeCases/EC-CTX-011` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-012 | `TestEdgeCases/EC-CTX-012` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-013 | `TestEdgeCases/EC-CTX-013` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-014 | `TestEdgeCases/EC-CTX-014` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-015 | `TestEdgeCases/EC-CTX-015` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-016 | `TestEdgeCases/EC-CTX-016` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-017 | `TestEdgeCasesF2/EC-CTX-017` + `TestRunWindowReservation` | `internal/loop/edgecases_f2_test.go:22`, `cmd/damnbot/window_reservation_test.go:16` | LULUS | loop |
| EC-CTX-018 | `TestEdgeCases/EC-CTX-018` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-019 | `TestEdgeCases/EC-CTX-019` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-020 | `TestEdgeCases/EC-CTX-020` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-021 | `TestEdgeCases/EC-CTX-021` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-CTX-022 | `TestEdgeCases/EC-CTX-022` | `internal/compaction/edgecases_test.go:18` | LULUS | compaction |
| EC-STATE-001 | `TestStateEdgeCases/EC-STATE-001` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-002 | `TestStateEdgeCases/EC-STATE-002` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-003 | `TestStateEdgeCases/EC-STATE-003` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-005 | `TestStateEdgeCases/EC-STATE-005` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-006 | `TestStateEdgeCases/EC-STATE-006` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-007 | `TestStateEdgeCases/EC-STATE-007` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-008 | `TestStateEdgeCases/EC-STATE-008` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-009 | `TestStateEdgeCases/EC-STATE-009` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-010 | `TestStateEdgeCases/EC-STATE-010` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-011 | `TestStateEdgeCases/EC-STATE-011` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-013 | `TestStateEdgeCases/EC-STATE-013` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-014 | `TestStateEdgeCases/EC-STATE-014` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-015 | `TestStateEdgeCases/EC-STATE-015` | `internal/ctxdb/edgecases_test.go:44` | LULUS | ctxdb |
| EC-STATE-016 | `TestStateEdgeCases/EC-STATE-016` | `internal/ctxdb/edgecases_unix_test.go:17` | LULUS | ctxdb |
| EC-PROV-008 | `TestEdgeCasesF2/EC-PROV-008` | `internal/loop/edgecases_f2_test.go:21` | LULUS | loop |
| EC-TCALL-010 | `TestEdgeCasesF2/EC-TCALL-010` (n/a) + `TestDeferredSchemaNotInPayload` | `internal/tools/edgecases_f2_test.go:27`, `:34`; e2e kontrak `internal/e2e/deferred_test.go:19` | LULUS (n/a; ADR-080 butir 3 + amandemen 2026-10-22; run-level F5) | tools |
| EC-WIN-002 | `TestWindowsF2EdgeCases/EC-WIN-002` | `internal/ctxdb/win_f2_edgecases_windows_test.go:19`; CI Windows PR #344 (tree `f3c0af68`) | LULUS | ctxdb |
| EC-WIN-005 | `TestWindowsF2EdgeCases/EC-WIN-005` | `internal/ctxdb/win_f2_edgecases_windows_test.go:19`; CI Windows PR #344 | LULUS | ctxdb |
| EC-WIN-011 | `TestWindowsF2EdgeCases/EC-WIN-011` | `internal/ctxdb/win_f2_edgecases_windows_test.go:19`; CI Windows PR #344 | LULUS | ctxdb |

## 2. GAP rows

Sumber: ADR-193 (scope gate F2 = 13 GAP) + SPEC §22.1. Baris di luar gate
dicantumkan supaya statusnya tidak hilang, bukan sebagai tagihan F2.

| GAP | gate F2 | bukti | status | pemilik / basis |
| --- | --- | --- | --- | --- |
| GAP-SEC-01 | ya | `TestCreateVerify`, `TestRoundTrip`, `TestWrongKey`, `TestTamperEveryByte` (`internal/backup`); canary `TestCryptoPlaintextCanaryVisible` `internal/e2e/crypto_test.go:168` | LULUS | backup |
| GAP-SEC-15 | ya | `TestMonetaryAlwaysPrompts` `internal/tools/payment_test.go:146`; e2e `TestShellGateMonetaryThroughBinary` `internal/e2e/shell_gate_classify_test.go:79` | LULUS | tools |
| GAP-SEC-16 | — | `TestEgressDLP` `internal/netx/dlp_test.go:111` (ingress). Tes body/egress tidak ada | TERBUKA | netx; sisa body/egress (bukan gate F2) |
| GAP-REL-01 | ya | `TestFailedTurnPersisted` `cmd/damnbot/rel01_test.go:20`; e2e `TestRunResumeExcludesFailedTurn` `internal/e2e/resume_test.go:16` | LULUS | loop |
| GAP-REL-03 | ya | nightly `chaos` 30 m: `DAMNBOT_CHAOS_DURATION=30m ... ok 1807.990s` (run 40290080911); varian 45 s `ok 45.449s` | LULUS | ctxdb |
| GAP-REL-04 | ya | `TestSQLiteEngineVersion` `internal/ctxdb/engine_test.go:7`; job `sqlite-engine` success | LULUS | provider/ctxdb |
| GAP-REL-05 | ya | `TestMigrationFailClosed` `internal/ctxdb/edgecases_test.go:351` | LULUS | ctxdb |
| GAP-COST-01 | ya | `TestDefaultPriceTableTeamRouter` `internal/provider/registry/prices_test.go:29` (tanpa `until`), `TestPricingExpiredFailsClosed` `internal/budget/pricing_test.go:66`. **Tidak ada gate release** yang gagal bila entri lewat `until`; data saat ini 0 `until` | TERBUKA | provider; sisa: gate release |
| GAP-COST-02 | ya | `TestNoContextProbe` `internal/provider/probe_test.go:16` | LULUS | provider |
| GAP-COST-04 | — | `TestFallbackPrefixStable` `cmd/damnbot/fallback_prefix_test.go:23`; `run.go:432` belum meneruskan `Provider.Fallback` | DITUNDA → F5 | ADR-193 |
| GAP-COST-09 | ya | `TestTokenEstimatorCalibration` `internal/budget/budget_test.go:132`; `TestImageTokens` `internal/budget/image_test.go:9`, `TestEstimatorImages` `:120` | LULUS | budget |
| GAP-PROV-01 | ya | e2e `TestCarrierReplayedNextIteration` `internal/e2e/carriers_test.go:12` + `TestCarrierRejectedNotReplayed` `:35` | LULUS | loop/provider |
| GAP-PROV-02 | ya | `TestContextTierBudget` `internal/assemble/tier_budget_test.go:15` | LULUS | assemble |
| GAP-INF-01 | ya | `TestRecallCJKOrder` `internal/ctxdb/acceptance_test.go:11` | LULUS | ctxdb |
| GAP-INF-02 | — | `TestFTSAutoUpgrade` `internal/ctxdb/acceptance_test.go:39`, `TestRecallToolOutputBlob` `:101`; spill e2e `internal/e2e/spill_test.go:72` | DITUNDA → F9 | ADR-193 |
| GAP-INF-06 | — | 0 tes `RerunDeterministic`/`RerunAfterRestart` | DITUNDA → F8 | ADR-193 |
| GAP-TOOL-10 | — | `internal/skills/requires.go` + `requires_test.go`; `cmd/damnbot/skills_host.go` + `skills_unavailable_test.go` (ADR-207); #345/ADR-208 menambah `skills.requires_host` + filter `skillstore.Search`/`skills_list` + e2e `internal/e2e/skills_index_filter_test.go:18`. Residual: retriever prompt masih nil (ADR-208 butir 5), native-arch select/MCP `requires`/hub | TERBUKA | ADR-193 (F3/F5/F8) + ADR-207/ADR-208 |
| GAP-SEC-26 | ya | `TestBuiltinSkillRefs` `internal/skills/builtin_test.go:24`; job `skill-refs` success | LULUS | skills/CI |
| GAP-TEST-01 | — | `TestOutline` `internal/codetools/read_test.go:277` (tree analysis); tes language detection tidak ada | TERBUKA | codetools; nama tes ledger lama fiktif |
| GAP-LEASE-01 | — | 0 pemanggil produksi `Heartbeat`/`CheckLease` | DITUNDA → F3/F5 | ADR-185 |

## 2b. Performa F2 (ambang acceptance, bukan target)

Ambang di bawah adalah ambang acceptance apa adanya; tidak ada margin.
`timing.Check` ×1,5 (ADR-077) adalah kebijakan flake **runner untuk tes**,
bukan ambang gate, dan tidak dipakai untuk menilai baris ini.

| metrik | ambang (SPEC) | bukti saat ini | status | sisa kerja |
| --- | --- | --- | --- | --- |
| `Store.Append` | p99 ≤ **10 ms** Linux/Windows, ≤ **30 ms** macOS; `synchronous=FULL`, kasus 3 blob ref/event (SPEC.md:585, ADR-039) | `BenchmarkAppend` `internal/ctxdb/bench_test.go:99` hanya melaporkan p50/p95/p99, **tanpa asersi**; `TestAppendP99` belum ada; angka CI belum ada | TERBUKA (menunggu angka CI) | PR `ci/perf-acceptance`: job yang menjalankan `TestAppendP99` + angka p50/p95/p99 dan nama runner |
| `recall` | p95 ≤ **50 ms** @ **1.000.000 event**, di luar purge berjalan (SPEC.md:1613, GAP-INF-01) | `TestRecallP95` `internal/ctxdb/recall_perf_test.go:86` ada, tetapi **tidak dipanggil job CI mana pun**; tidak dijalankan pada snapshot ini | TERBUKA (menunggu angka CI) | job CI yang menjalankan `TestRecallP95` + angka p50/p95 dan nama runner |

Angka performa bergantung pada runner: Q14 NVMe vs mesin bersama. Angka
lokal (bukan Q14) **bukan** bukti acceptance; hanya angka dari job CI pada
head final, beserta nama runner, yang dapat menutup baris ini.

## 3. Parity F2 (FEATURE-PARITY, setelah re-fase ADR-209)

### 3.1 56 baris F2

| ID | fitur | status | bukti |
| --- | --- | --- | --- |
| CLI-039 | `hermes checkpoints` | LULUS | `cmd/damnbot/checkpoints.go`; e2e `TestCheckpointsCLI` `internal/e2e/checkpoints_test.go:191` (+`:81`, `:147`) |
| CLI-046 | `hermes skills` | LULUS | `cmd/damnbot/skills_cli.go:255`; e2e `TestSkillsCLI` `internal/e2e/skills_cli_test.go:25` |
| CLI-047 | `hermes bundles` | LULUS | `cmd/damnbot/bundles.go`; e2e `TestBundlesCLI` `internal/e2e/bundles_test.go:12` |
| CLI-057 | `hermes sessions` | LULUS | `cmd/damnbot/sessions.go`; e2e `sessions_admin_test.go:27,64`, `sessions_prune_search_test.go:25,64,113`, `sessions_labels_test.go:28,59`, `sessions_why_test.go:70,118,138` |
| TOOL-070 | `memory` tool | LULUS | `internal/tools/memory.go:43`; wiring `cmd/damnbot/tools_wire.go:333`; e2e `TestMemoryToolEndToEnd` `internal/e2e/context_tools_test.go:15` |
| TOOL-073 | `session_search` tool | LULUS | `internal/tools/session_search.go:26`; e2e `TestSessionSearchEndToEnd` `internal/e2e/context_tools_test.go:57` |
| TOOL-074 | `skill_manage` | LULUS | `internal/tools/skills.go:62`; e2e `TestSkillManageAlwaysAsks` `internal/e2e/skills_test.go:98` |
| TOOL-075 | `skill_view` | LULUS | `internal/tools/skills.go:61`; e2e `TestSkillViewQuarantined` `internal/e2e/skills_test.go:46` |
| TOOL-076 | `skills_list` | LULUS | `internal/tools/skills.go:60`; e2e `TestSkillsTools` `internal/e2e/skills_test.go:20` |
| TSET-021 | toolset `memory` | LULUS | `internal/toolset/toolset.go:82`; `TestResolveAll` `toolset_test.go:173`; wiring `cmd/damnbot/run.go:457` |
| TSET-023 | toolset `session_search` | LULUS | idem; `TestResolveAll` |
| TSET-025 | toolset `skills` | LULUS | idem; `TestResolveAll` |
| TSET-035 | toolset `context_engine` | LULUS | `TestContextEngineIsDataOnly` `toolset_test.go:188` |
| TSET-038 | toolset `debugging` | LULUS | `TestResolveAll` `toolset_test.go:173` |
| TSET-039 | toolset `safe` | LULUS | `TestResolveAll` `toolset_test.go:173` |
| TSET-040 | toolset `coding` | LULUS | `TestResolveAll` `toolset_test.go:173` |
| TSET-041 | toolset `hermes-acp` | LULUS | `TestResolveAll` `toolset_test.go:173` |
| TSET-043 | toolset `hermes-cli` | LULUS | `TestResolveAll` `toolset_test.go:173` |
| MEM-001 | built-in memory | LULUS | `internal/memory` suite; e2e memory tool `context_tools_test.go:15` |
| MEM-002 | user profile | LULUS | `TestUserWriteIsApproved` `internal/memory/memory_test.go:231` |
| MEM-003 | memory nudge | LULUS | `cmd/damnbot/nudge.go`; e2e `TestMemoryNudge` `internal/e2e/nudge_test.go:18` |
| MEM-004 | memory write approval | LULUS | `TestQuarantineCannotBeRetrieved` `memory_test.go:163`, `TestTransitions` `:332` |
| MEM-011 | session store SQLite | LULUS | `internal/ctxdb` suite (`TestStore*`) |
| MEM-012 | session search FTS | LULUS | `TestSearchFilters` `internal/ctxdb/memstore/memstore_test.go:165` |
| MEM-013 | recap/resume | LULUS | `cmd/damnbot/recap.go`; e2e `TestRunRecapOnResume` `internal/e2e/recap_test.go:25` |
| MEM-014 | session export | LULUS | `cmd/damnbot/session_export.go`; e2e `TestSessionsAdminExport` `sessions_admin_test.go:27` |
| MEM-015 | session prune/archive | LULUS | `cmd/damnbot/sessions_admin.go`; e2e `sessions_admin_test.go:64`, `sessions_prune_search_test.go:64,113` |
| MEM-016 | lineage & titles | LULUS | e2e `TestSessionsLabelsEndToEnd` `sessions_labels_test.go:28`, kontrol `:59`; lineage `TestHistoryForkAware` |
| MEM-018 | context compression | LULUS | `TestCompaction` `internal/compaction/compaction_test.go:55` |
| MEM-020 | micro-compaction | LULUS | `TestPruneStaleRead` `prune_test.go:86`, `TestPruneOldResults` `:108` |
| MEM-021 | proactive prune | LULUS | `TestResultFlags` `prune_test.go:278` |
| MEM-024 | @-ref | LULUS | `cmd/damnbot/refs.go`; e2e `refs_test.go:16,30,84,115,131,139,151,165,186,202` |
| MEM-025 | context pin | LULUS | e2e `TestContextPinSurvivesCompaction` `internal/e2e/context_pin_compaction_test.go:35` |
| MEM-026 | context files | LULUS | `cmd/damnbot/context_files.go:41,43`; e2e `TestE2EContextFileWrappedUntrusted` `context_files_test.go:121` |
| MEM-028 | rewind | LULUS | `internal/journal` suite; e2e `undo_review_test.go:29`, `undo_test.go:130,168,213,232` |
| MEM-029 | branch/fork | LULUS | `TestForkAndRewind` `internal/ctxdb/read_test.go:290`; e2e `sessions_fork_prefix_test.go:127,170,206,319` |
| MEM-030 | retry | LULUS | `run --retry`; e2e `recap_test.go:48,67,90` |
| MEM-031 | filesystem checkpoints | LULUS | `TestUndoSuite` `internal/ctxdb/journalstore/undo_suite_test.go:188` (≥100 skenario); e2e checkpoints |
| MEM-032 | state snapshot | LULUS | `internal/backup` suite (config+DB); e2e `TestCryptoBackupRestore` `crypto_test.go:33` |
| MEM-033 | memory trim | LULUS | `trimRSS` `cmd/damnbot/run_resume.go:32`; `TestRunTrimsMemory` `cmd/damnbot/run_inject_test.go:134` |
| MEM-034 | prompt caching | LULUS | `internal/provider/openai.go:183`; `TestCacheBreakpointAnthropic` `internal/provider/cache_test.go:70`; e2e `cache_test.go:15` |
| SKILL-001 | SKILL.md system | LULUS | `internal/skills` suite (`TestParse`, `TestRetrieveLimit`) |
| SKILL-002 | skills_list / skill_view / skill_manage | LULUS | e2e `skills_test.go:20,46,98` |
| SKILL-004 | preprocessing | LULUS | `internal/skills/preprocess.go`; `preprocess_test.go:23,44,64` |
| SKILL-007 | bundles | LULUS | `internal/skills/bundle.go`; `bundle_test.go:13`; e2e `bundles_test.go:12` |
| SKILL-008 | auto-load | LULUS | `TestRetrieveLimit` `internal/skills/skills_test.go:262` |
| SKILL-009 | project discovery | LULUS | `cmd/damnbot/skills_wire.go:57`; e2e `TestImportedSkillQuarantined` `skills_test.go:214` |
| SKILL-010 | external skill dirs | LULUS | `cmd/damnbot/skills_wire.go:62`; e2e `TestSkillsTrustCLI` `skills_test.go:250` |
| SKILL-016 | bundled skills sync | TERBUKA | 0 kode sync; korpus builtin kosong (`skills/.gitkeep`); `TestBuiltinSkillRefs` `internal/skills/builtin_test.go:24` hanya lint |
| SKILL-018 | guard/lint | LULUS | `TestLintCorpus` `internal/skills/lint_test.go:56`, `TestLintFailsClosed` `:133` |
| SKILL-019 | ledger & provenance | LULUS | `internal/skills/ledger.go`; `TestLedgerRoundTrip` `ledger_test.go:13` |
| SKILL-022 | write approval | LULUS | `TestInstallQuarantine` `skills_test.go:187`; e2e `skills_test.go:98` |
| SKILL-023 | skills index | LULUS | `internal/skills/index.go`; `TestIndexDiff` `index_test.go:11`; `skills index` `cmd/damnbot/skills_cli.go:268` |
| PLUG-036 | tool search / deferred | LULUS | `internal/tools/deferred.go`; `deferred_test.go`; kontrak e2e `internal/e2e/deferred_test.go:19` |
| CORE-004 | toolsets & per-platform config | LULUS | `TestResolvePlatformOverride` `toolset_test.go:150`; `TestConfigToolsets` `internal/config/toolsets_test.go:11` |
| CORE-005 | custom toolsets | LULUS | `TestResolveDisabledAndCustom` `toolset_test.go:107`; `TestConfigToolsetsFailClosed` |

### 3.2 Tiga baris yang di-re-fase ADR-209 (bukan F2)

| ID | fitur | fase baru | status | bukti / prasyarat |
| --- | --- | --- | --- | --- |
| CLI-040 | `hermes import` | F3 | TERBUKA | Bentuk normatif SPEC §9.15 (`SPEC.md:981`) + §10 (`SPEC.md:1013`); `internal/importer` hanya sisi baca (`importer.go:186`,`:210`). Prasyarat: config writer + merge atomik, secret setter terekspor, cron store F4, sessions collector + `Sink.PutSessions`. PR pengimplementasi belum ada di daftar terbuka 2026-10-27 |
| OPS-006 | backup / import | F3 | TERBUKA | Backup: `sessions backup` (ADR-124/ADR-198). Import: prasyarat yang sama dengan CLI-040; `--only sessions` tidak dapat dipenuhi karena `Source` tidak punya sessions dan `Sink` tidak punya `PutSessions` (gap SPEC §9.15) |
| CLI-038 | `hermes backup` | F9 | DITUNDA | SPEC §10 tidak punya top-level `backup`; backup = `sessions backup` (ADR-124/ADR-198). Arsip portabel/impor = GAP-OPS-05 `SPEC.md:2978`, pasca-1.0 `SPEC.md:2823` (§19). Menambah top-level `backup` melanggar §10 satu-nama-satu-tujuan |

#345/ADR-208 sudah mendarat: katalog (`skills_list`) + `skillStore.Search`
menegakkan `requires`, tetapi retriever prompt masih **nil** (ADR-208 butir 5),
jadi `GAP-TOOL-10` tetap `TERBUKA` dan tetap di luar gate F2 (ADR-193).

### 3.3 22 baris pindah fase ADR-139 (bukan F2)

`SLASH-002/006/007/008/010/011/013/015/016/017/044/045/066/068/069/070/071/083`
dan `MEM-019` → F3; `MEM-022` → F5; `MEM-023` + `PLUG-014` → F8. Baris-baris
ini tidak dihitung sebagai utang F2 dan tidak diberi status acceptance F2;
inventarisnya tetap di `docs/FEATURE-PARITY.md`.

## 4. Suite e2e (biner asli)

`go test -tags goolm -count=1 ./internal/e2e` pada `f3c0af68`: **rc=0,
79 top-level PASS, 0 SKIP, 0 FAIL**.

| # | skenario | tes | status |
| --- | --- | --- | --- |
| 1 | compaction→summary + `<untrusted>`, termasuk baca berkas | `internal/e2e/summary_taint_test.go:47`; kontrol `:86` | LULUS |
| 2 | tier recall + isolasi project/principal/sesi | `internal/e2e/recall_scope_test.go:29,255,270,289`; kontrol vacuity | LULUS |
| 3 | failed-turn `context_exclude` + side effect | `internal/e2e/resume_test.go:16`; kontrol turn sukses di tes yang sama | LULUS |
| 4a/4b | carrier replay/reject | `internal/e2e/carriers_test.go:12,35` | LULUS |
| 5 | undo byte-identik + review + penolakan symlink/konflik | `internal/e2e/undo_review_test.go:29`; `undo_test.go:130,168,213,232` | LULUS |
| 6a/6b | budget guard cap + ledger usage | `internal/e2e/budget_guard_ledger_test.go:154,198,215,230` | LULUS |
| 7 | prefix fork/rewind + penolakan id/`--at` | `internal/e2e/sessions_fork_prefix_test.go:127,170,206,319` | LULUS |
| 8 | crypto at-rest + backup/restore + canary | `internal/e2e/crypto_test.go:33,168`; `spill_test.go:72` | LULUS |
| 9 | buka DB skema lebih baru read-only | `internal/e2e/sessions_readonly_test.go:14` | LULUS |

Ekstra yang menutup baris: `skills_test.go` (TOOL-074/075/076, SKILL-002/009/010),
`skills_cli_test.go:25` (CLI-046), `bundles_test.go:12` (CLI-047),
`checkpoints_test.go:191` (CLI-039), `context_files_test.go:121` (MEM-026),
`context_pin_compaction_test.go:35` (MEM-025), `context_tools_test.go:15,57`
(TOOL-070/073), `sessions_*` (CLI-057/MEM-013..016), `recap_test.go` (MEM-030),
`refs_test.go` (MEM-024), `nudge_test.go:18` (MEM-003), `cache_test.go:15`
(MEM-034), `shell_gate_classify_test.go:79` (SEC-15), `deferred_test.go:19`
(PLUG-036/EC-TCALL-010 kontrak).

**Belum ada e2e:** `import` CLI (CLI-040/OPS-006), komposisi toolset
(TSET/CORE), memory trim (MEM-033 hanya unit), skills index (SKILL-023 hanya
unit), deferred tools dengan modul benar-benar ter-wire (EC-TCALL-010).

## 5. Sisa kerja (PR-sized)

1. **`GAP-COST-01`** — gate release yang gagal bila ada entri `providers.toml`
   lewat `until`; saat ini 0 tes dan 0 entri `until` (±40 baris).
2. **`SKILL-016`** — sync korpus skill bawaan (embedded corpus + install saat
   start/update) + tes, atau ADR yang memindahkannya dari F2 (±200–400 baris).
3. **`GAP-TOOL-10` residual** — filter requirement host di index/prompt
   (`ctxdb/skillstore` + `SearchAvailable`), ditutup PR #345 bila mendarat
   (±80–120 baris, ADR-207).
4. **`GAP-TEST-01`** — tes language detection yang berdiri sendiri, atau
   koreksi baris; `TestOutline` sudah menutup tree analysis (±80 baris).
5. **`GAP-SEC-16`** — tes body/egress DLP di luar ingress (±300–500 baris).
6. **`import` (CLI-040/OPS-006, F3)** — prasyarat ADR-209: config writer +
   merge atomik, secret setter, cron store F4, sessions collector; `--only
   sessions` butuh keputusan owner (implementasi atau amandemen SPEC §9.15).
7. **Gate performa F2** — lihat §2b. `TestAppendP99` belum ada dan
   `BenchmarkAppend` hanya melaporkan p99 tanpa asersi; `TestRecallP95`
   (1.000.000 event) tidak dipanggil job CI mana pun. Tambahkan job CI
   (PR `ci/perf-acceptance`) dan angka p50/p95/p99 + nama runner di head
   final sebelum menulis `LULUS`. Ambang tidak berubah: Append p99 ≤ 10 ms
   (Linux/Windows) / ≤ 30 ms (macOS); recall p95 ≤ 50 ms @1M.

## 6. Bukti batas keamanan (ringkas)

Detail verbatim flip ada di PR #302 dan commit `5434a915`; audit ini tidak
mengulang flip, hanya memastikan tesnya ada dan lulus pada snapshot.

| permukaan | positif | kontrol negatif | load-bearing |
| --- | --- | --- | --- |
| undo/sandbox/journal | `internal/e2e/undo_review_test.go:29`; `undo_test.go:130` | `undo_test.go:168` + positif `:213`; konflik `:232` | terbukti (flip 2026-10-24) |
| exec/classifier/payment | `internal/e2e/shell_gate_classify_test.go:79` | refusal leg di tes yang sama (efek samping tidak ada) | terbukti |
| crypto/at-rest | `internal/e2e/crypto_test.go:33`; `spill_test.go:72` | `crypto_test.go:168`; `spill_test.go:81` | terbukti |
| karantina skill | `internal/e2e/skills_test.go:19,199` | `skills_test.go:45,163` | terbukti |
| persetujuan skill | `skills_test.go:19` | `skills_test.go:97` | terbukti |
| budget guard | `budget_guard_ledger_test.go:154,230` | `:198,215` | terbukti |
| berkas/tool-output ke prompt | `carriers_test.go:12`; `skills_test.go:37`; `spill_test.go:72` | `carriers_test.go:35`; `spill_test.go:81` | terbukti |

## 7. Audit ulang (perintah yang dijalankan)

Snapshot `f3c0af68`; perintah berat memakai `GOMAXPROCS=2 GOFLAGS=-p=1
nice -n 19` dan `ulimit -v 4194304`.

```bash
go test -tags goolm -count=1 ./internal/e2e
# rc=0; 79 PASS, 0 SKIP, 0 FAIL
go test -tags goolm -count=1 ./internal/skills ./cmd/damnbot
# ok (f3c0af68)
go test -tags goolm -count=1 ./internal/ctxdb ./internal/loop ./internal/tools \
  ./internal/compaction ./internal/assemble ./internal/provider ./internal/backup ./cmd/damnbot
# ok (e727ee1a; f3c0af68 hanya menambah skills/cmd/docs)
go test ./internal/ctxdb/journalstore -run TestUndoSuite -count=1
# PASS, >=100 skenario
go test ./internal/archtest -run 'TestBudgetTableSum|TestEngineeringTotalCoreMatchesLocBudget|TestParityPhaseColumn' -count=1
# PASS; Sigma core f3c0af68=46.703, 7a196a28 setelah #345=46.733 = docs/ENGINEERING.md
DAMNBOT_PHASE_ACCEPTANCE=1 go test ./internal/testutil/phase -run TestEdgeCaseCatalogSync -count=1
# PASS (PHASE=F3; 0 id F1/F2 pending)
make loc
# f3c0af68: rc=0, 37 LOC ok, 0 GAGAL, TOTAL 51358/190000
# 7a196a28 setelah #345: rc=0, 37 LOC ok, 0 GAGAL, TOTAL 51433/190000
cd bench && go test -count=1 -run TestFixedOverhead ./budget
# PASS (all-modules 5000, f1-core 4000)
```

**Tidak dijalankan pada snapshot ini:** `make phase-acceptance` penuh
(run penuh terakhir yang hijau: PR #328, `b46253e2`); `TestRecallP95` @1M;
race/fuzz/coverage/bench-parity; chaos 30 m lokal (memakai nightly);
lint lokal (tidak ada binari; status CI tidak dipakai sebagai bukti);
Windows lokal (memakai job CI PR #344 pada tree `f3c0af68`). **Tidak ada
angka performa CI pada snapshot ini**, sehingga kedua baris §2b tetap
`TERBUKA`. `BenchmarkAppend` pernah diukur lokal (p99 25,23 ms di bawah
`GOMAXPROCS=2`/`nice 19`), tetapi benchmark itu **tidak berasersi** dan
mesin ini bukan runner referensi Q14; angka performa bergantung pada runner
(Q14 NVMe vs mesin bersama), jadi angka lokal **bukan** bukti acceptance.
