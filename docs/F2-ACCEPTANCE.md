# F2 acceptance ledger

Acceptance for the F2 phase is defined in [ROADMAP.md](ROADMAP.md)
(F2 acceptance) and [EDGE-CASES-hermes.md](EDGE-CASES-hermes.md);
[FEATURE-PARITY.md](FEATURE-PARITY.md) assigns every parity row to a phase.
This ledger is the review artifact for that acceptance: every required
edge case (EC), every GAP row and every F2 parity row appears once, with
the test that proves it, where that test lives, and an owner.

The completeness test in `internal/phase/acceptance_test.go` checks that
each EC ID in the roadmap table is claimed by some test. It does not check
that the EC ID is *required* by the roadmap, nor that a parity row is
tested — missing rows are simply silent. Work items to close those holes
are listed in §4.

## Status legend

| status | meaning |
| --- | --- |
| covered | A test on `main` (sha `70ff325`) proves the row. |
| in-flight | An open PR head (fetched under `refs/remotes/pr/N`) provides the test; the PR number is cited. |
| missing | No test on `main` or in any open PR head proves the row. |
| surface | The capability is covered but the parity row is a CLI/slash surface that no test covers. |

Evidence was collected with `git grep` over `origin/main` and every open
PR head (47 heads at the time of writing), and with `git log -S` to
confirm that a test name exists nowhere else.

## Summary

| group | rows | covered | in-flight | surface | missing |
| --- | --- | --- | --- | --- | --- |
| EC — required by ROADMAP F2 | 41 | 41 | — | — | 0 |
| GAP — DECISIONS.md L247-265 | 18 | 13 | 2 | 1 | 2 |
| Parity — rows listed in §3 | 81 | 25 | 4 | 17 | 35 |

All 41 EC rows are covered or `n/a` now. The eight rows that were missing when
this ledger was written closed as: `EC-CTX-006` (#275), `EC-CTX-007`
(#249/#260), `EC-CTX-017` and `EC-PROV-008` (#245), `EC-STATE-001/007/009`
(#261), and `EC-TCALL-010` (`n/a` with a written reason, #174).
`EC-WIN-002`, `EC-WIN-005`, `EC-WIN-011` moved from missing to in-flight here
and are covered by #257 (windows-latest runner); the sixteen uncataloged F1
ids below are covered by #254/#255.

Ringkasan GAP di atas sebelumnya **tertukar** antara `covered` dan `missing` (tertulis 5/…/9 padahal baris §2
berisi 13 covered dan 2 missing); angka kini diselaraskan dengan barisnya. §2 diverifikasi ulang pada
`main ef8ec40d` (2026-10-24): `GAP-SEC-01` ditutup oleh canary `TestCryptoPlaintextCanaryVisible`,
`GAP-COST-02`/`GAP-COST-04`/`GAP-PROV-02` ternyata sudah punya tes di `main` (ledger mencari nama tes yang
berbeda), dan `GAP-SEC-26`/`GAP-REL-04` tinggal menunggu gate CI dari #258 (terbuka). Yang masih benar-benar
kosong: `GAP-INF-06` dan `GAP-TOOL-10`.

Audit §3 (parity) pada `main ef8ec40d` memetakan 42 baris berstatus `missing`: **8 baris ternyata sudah
`covered`** (statusnya dikoreksi di bawah, dengan berkas + nama tes), **22 baris sudah bukan milik F2**
(fasenya dikutip dari kolom `fase` `docs/FEATURE-PARITY.md` dan dicatat di barisnya; kepemilikan fase
mengikuti **ADR-139**), **22 baris masih F2 dan punya penutup PR terbuka** (nomornya dicatat di barisnya),
dan **1 baris tanpa penutup** (`CLI-039`; `MEM-026` **ditutup #310** pada 2026-10-25 dan `MEM-025`
**ditutup #318** pada 2026-10-26, jadi koreksi 2026-10-24 sudah lunas untuk keduanya - lihat barisnya dan
§4 item 11(d)/10). Karena 81 baris terdaftar = 59 baris yang kini masih
fase F2 + 22 baris yang pindah fase, utang implementasi F2 yang sebenarnya = **23 baris** (22 berpenutup +
1 tanpa). Baris parity **tidak** dulu maupun sekarang menjadi gate - lihat §3 bagian terakhir.

---

## 1. Edge cases required by ROADMAP F2

| EC | test | location (main) | status | owner |
| --- | --- | --- | --- | --- |
| EC-CTX-001 | `TestEdgeCases/EC-CTX-001` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-002 | `TestEdgeCases/EC-CTX-002` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-003 | `TestEdgeCases/EC-CTX-003` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-004 | `TestEdgeCases/EC-CTX-004` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-005 | `TestEdgeCases/EC-CTX-005` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-006 | `TestEdgeCases/EC-CTX-006` + `TestPruneSkillViewMarker` | `internal/compaction/edgecases_test.go`, `internal/compaction/prune_test.go` | covered (#275) | compaction |
| EC-CTX-007 | `TestEdgeCases/EC-CTX-007` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-008 | `TestEdgeCases/EC-CTX-008` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-009 | `TestEdgeCases/EC-CTX-009` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-010 | `TestEdgeCases/EC-CTX-010` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-011 | `TestEdgeCases/EC-CTX-011` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-012 | `TestEdgeCases/EC-CTX-012` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-013 | `TestEdgeCases/EC-CTX-013` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-014 | `TestEdgeCases/EC-CTX-014` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-015 | `TestEdgeCases/EC-CTX-015` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-016 | `TestEdgeCases/EC-CTX-016` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-017 | `TestEdgeCasesF2/EC-CTX-017` + `TestRunWindowReservation` | `internal/loop/edgecases_f2_test.go`, `cmd/damnbot/window_reservation_test.go` | covered (#245; the stale `pending:F2` row was removed from `internal/compaction/edgecases_test.go`) | loop |
| EC-CTX-018 | `TestEdgeCases/EC-CTX-018` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-019 | `TestEdgeCases/EC-CTX-019` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-020 | `TestEdgeCases/EC-CTX-020` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-021 | `TestEdgeCases/EC-CTX-021` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-022 | `TestEdgeCases/EC-CTX-022` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-STATE-001 | `TestStateEdgeCases/EC-STATE-001` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-002 | `TestStateEdgeCases/EC-STATE-002` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-003 | `TestStateEdgeCases/EC-STATE-003` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-005 | `TestStateEdgeCases/EC-STATE-005` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-006 | `TestStateEdgeCases/EC-STATE-006` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-007 | `TestStateEdgeCases/EC-STATE-007` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-008 | `TestStateEdgeCases/EC-STATE-008` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-009 | `TestStateEdgeCases/EC-STATE-009` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-010 | `TestStateEdgeCases/EC-STATE-010` + `edgecases_unix_test.go` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-011 | `TestStateEdgeCases/EC-STATE-011` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-013 | `TestStateEdgeCases/EC-STATE-013` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-014 | `TestStateEdgeCases/EC-STATE-014` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-015 | `TestStateEdgeCases/EC-STATE-015` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| EC-STATE-016 | `TestStateEdgeCases/platformStateCases` | `internal/ctxdb/edgecases_unix_test.go` | covered | ctxdb |
| EC-PROV-008 | `TestEdgeCasesF2/EC-PROV-008` | `internal/loop/edgecases_f2_test.go` | covered (#245) | loop |
| EC-TCALL-010 | `TestEdgeCasesF2/EC-TCALL-010` (`n/a`, reasoned) | `internal/tools/edgecases_f2_test.go` | n/a (#174: the declared-payload invariant is pinned in-process) | tools |
| EC-WIN-002 | `TestWindowsF2EdgeCases/EC-WIN-002` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | covered (#257) | ctxdb |
| EC-WIN-005 | `TestWindowsF2EdgeCases/EC-WIN-005` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | covered (#257) | ctxdb |
| EC-WIN-011 | `TestWindowsF2EdgeCases/EC-WIN-011` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | covered (#257) | ctxdb |

Note: `EC-CTX-006`, `-007` and `-017` were declared `pending:F2` inside the
compaction gate while this ledger was written; all three are covered now
(`-006` #275, `-007` #249/#260, `-017` #245, whose stale pending row was
removed from `internal/compaction/edgecases_test.go`). The class of silent
miss this section warned about is what §4 item 1 guards: a `pending` id at or
below `PHASE`−1 fails the phase acceptance (ADR-176).

### F1 ids with no catalog row

The same cross-check over the whole roadmap (not only F2) found 16 ids whose
roadmap phase is F1 but which no test named when this ledger was written. All
16 have a catalog row and a test now — EC-WIN-001/009/014/016/017 in #254, the
rest in #255 — and `DAMNBOT_PHASE_ACCEPTANCE=1` with `PHASE = F3` passes:

| EC | test | location | status |
| --- | --- | --- | --- |
| EC-FIO-002 | `TestEdgeCases/EC-FIO-002` | `internal/exec/edgecases_test.go` | covered (#255) |
| EC-FIO-005 | `TestSurrogateEdgeCases/EC-FIO-005` | `internal/provider/edgecases_fio_test.go` | covered (#255) |
| EC-FIO-006 | `TestTurnAggregateScaled/EC-FIO-006` | `internal/tools/edgecases_fio_test.go` | covered (#255) |
| EC-FIO-009 | `TestSurrogateEdgeCases/EC-FIO-009` | `internal/provider/edgecases_fio_test.go` | covered (#255) |
| EC-FIO-012 | `TestEdgeCases/EC-FIO-012` | `internal/exec/edgecases_test.go` | covered (#255) |
| EC-FIO-016 | `TestSSEEncodingEdgeCases/EC-FIO-016` | `internal/netx/edgecases_fio_test.go` | covered (#255) |
| EC-FIO-017 | `TestSSEEncodingEdgeCases/EC-FIO-017` | `internal/netx/edgecases_fio_test.go` | covered (#255) |
| EC-FIO-019 | `TestVolatileTimeIsUTC/EC-FIO-019` | `cmd/damnbot/edgecases_volatile_test.go` | covered (#255) |
| EC-TCALL-009 | `TestEdgeCases/EC-TCALL-009` + `TestRunAnswerSchema` | `internal/loop/edgecases_test.go`, `cmd/damnbot/run_inject_test.go` | covered (#255) |
| EC-TERM-002 | `TestEdgeCases/EC-TERM-002` | `internal/exec/edgecases_test.go` | covered (#255) |
| EC-TERM-007 | `TestProbeTimeout/EC-TERM-007` | `internal/ctxdb/edgecases_term_test.go` | covered (#255) |
| EC-WIN-001 | `TestWindowsF1EdgeCases/EC-WIN-001` | `internal/exec/win_f1_edgecases_windows_test.go` | covered (#254) |
| EC-WIN-009 | `TestWindowsF1EdgeCases/EC-WIN-009` | `internal/ctxdb/win_f1_edgecases_windows_test.go` | covered (#254) |
| EC-WIN-014 | `TestWindowsF1EdgeCases/EC-WIN-014` | `internal/codetools/win_f1_edgecases_windows_test.go` | covered (#254) |
| EC-WIN-016 | `TestWindowsF1EdgeCases/EC-WIN-016` | `internal/exec/win_f1_edgecases_windows_test.go` | covered (#254) |
| EC-WIN-017 | `TestWindowsF1EdgeCases/EC-WIN-017` | `cmd/damnbot/console_windows_test.go` | covered (#254) |

The two behaviours that needed code are EC-FIO-006 (the aggregate now scales
with the window) and EC-TCALL-009 (`--answer-schema` produces
`invalid_answer`, ADR-155).

## 2. GAP rows (DECISIONS.md L247-265)

| GAP | requirement | test(s) | status | owner |
| --- | --- | --- | --- | --- |
| GAP-SEC-01 | Backup secrets stay encrypted | `TestCreateVerify`, `TestRoundTrip`, `TestWrongKey`, `TestTamperEveryByte` (`internal/backup/create_test.go`; perms via `checkResult`: 0600 file / 0700 dir); canary: `TestCryptoPlaintextCanaryVisible` (`internal/e2e/crypto_test.go:168`) | covered | backup |
| GAP-SEC-15 | Payment always prompts | `TestMonetaryAlwaysPrompts`, `TestMonetaryNeverSession` | covered | tools |
| GAP-SEC-16 | egress DLP | `TestEgressDLP` + `TestEgressDLPBodyWarning` — F1 ingress subset only; the F2/F4–F7 portion (§SPEC half) is not tested | surface | netx |
| GAP-REL-01 | Failed turn persisted | `TestFailedTurnPersisted` + `TestFailedTurnEmptyContent` | covered | loop |
| GAP-REL-03 | Chaos: 50 concurrent clients, 30 min | `TestChaos50Clients`, nightly `chaos` (`internal/ctxdb/chaos_test.go`, `.github/workflows/nightly.yml`) | covered (retry busy: `BEGIN IMMEDIATE`/`COMMIT` <= 3x, PR retry busy; varian chaos 45 s hijau) | ctxdb (GAP-REL-03; pengecualian sah: `db_busy` typed untuk pemegang lock di-SIGSTOP, `SPEC.md:2362` baris 37) |
| GAP-REL-04 | SQLite engine version | `TestSQLiteEngineVersion` (`internal/ctxdb/engine_test.go`); gate CI `sqlite-engine` ada di #258 (terbuka) | covered | provider/ctxdb |
| GAP-REL-05 | Migration fail-closed | `TestMigrationFailClosed` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| GAP-COST-01 | Pricing table not expired at release | `TestPricingTableNotExpired` (`internal/provider/registry/prices_test.go`) | in-flight (PR #188) | provider |
| GAP-COST-02 | No context probe | `TestNoContextProbe` (`internal/provider/probe_test.go:16`) - analyzer + tesnya sudah ada di `main` | covered | provider |
| GAP-COST-04 | Prefix stable across fallback | `TestFallbackPrefixStable` (`cmd/damnbot/fallback_prefix_test.go:23`) - ledger dulu mencari nama `TestPrefixStableAcrossFallback` | covered | assemble/loop |
| GAP-PROV-01 | Cache key includes tier/system | `TestFixedOverheadGolden`, `TestAllModulesFixtureShape` compare the rendered prompt; the F2 part (table via migrations) is `TestMigrate*` | covered | assemble/ctxdb |
| GAP-PROV-02 | Per-tier budget asserted | `TestContextTierBudget` (`internal/assemble/tier_budget_test.go`) | covered | assemble |
| GAP-INF-01 | CJK recall order | `TestRecallCJKOrder`, `BenchmarkRecallCJK` | `internal/ctxdb/acceptance_test.go`, `internal/ctxdb/recall_perf_test.go` | covered | ctxdb |
| GAP-INF-02 | FTS index auto-upgrade | `TestFTSAutoUpgrade`, `BenchmarkRecallToolOutput`, `TestRecallToolOutputBlob`, `TestEncryptedRecallToolOutputBlob`, `TestToolOutputSpillThroughBinary` (biner asli: spill terenkripsi + baris `blob_fts`) | `internal/ctxdb/acceptance_test.go`, `internal/ctxdb/recall_perf_test.go`, `internal/e2e/spill_test.go` | covered | ctxdb |
| GAP-INF-06 | Rerun deterministic after restart | — (no test; separate from EC-CTX-021's pure rerun) - diverifikasi ulang `ef8ec40d`: tetap 0 tes (`RerunDeterministic`/`RerunAfterRestart` tidak ada) | missing | loop |
| GAP-TOOL-10 | Native arch select + skill filter | `TestToolSelection` exists; no `TestNativeArchSelect`; no `requires: gpu=nvidia` filter test - diverifikasi ulang `ef8ec40d`: tetap 0 tes pilih-arch/native filter | missing | tools/skills |
| GAP-SEC-26 | No dangling skill refs in builtin | `TestLintCorpus` lint only; gate `skill-refs` atas `builtin/` ada di #258 (`internal/skills/builtin_test.go`) | in-flight (PR #258) | skills/CI |
| GAP-TEST-01 | Test-only tree analysis | `TestTreeAnalysis`, `TestLanguageDetection` on `testdata` | covered | codetools |

### GAP tambahan hasil audit 2026-10-24

| GAP | requirement | bukti | status | owner / rencana fase |
| --- | --- | --- | --- | --- |
| GAP-LEASE-01 | SPEC §11.11: heartbeat lease 2 s + TTL 10 s dan `CheckLease` sebelum LLM call/tool dispatch | tidak ada pemanggil produksi `Heartbeat(`/`CheckLease(` (grep seluruh repo non-test = 0); token lease dibuang massal saat drain (`internal/ctxdb/store.go:511` di `detach`, dipicu `watch` `store.go:269-283` dan `preWrite` `tx.go:132`) sehingga `lease_fenced` fail-closed bisa muncul di alur nyata tanpa pre-flight; kelas kegagalan itu direproduksi deterministik oleh `internal/ctxdb/chaos_test.go` (`TestChaosClientRecoversFromFencing`) dan muncul di CI PR yang tidak menyentuh paket ini (`#302`, `#306`) | `missing` | core (`ctxdb`, `loop`, `cmd/damnbot`) - **rencana: F3 (inbound/ACP) atau F5 (gateway)**, lihat **ADR-185** |

Baris ini **di luar** 18 baris kanonik `DECISIONS.md L247-265` di atas; ia masuk ledger karena audit 2026-10-24 menemukan klaim §11.11 yang belum diimplementasikan, dan sengaja berstatus `missing` supaya gate `PHASE` tidak mengklaim kepatuhan yang tidak ada.

## 3. F2 parity rows (FEATURE-PARITY.md)

81 baris di bawah = **59 baris yang masih fase F2** + **22 baris yang pindah fase** (ADR-139: 19 permukaan
slash/MEM-019 -> F3; MEM-022 -> F5; MEM-023, PLUG-014 -> F8). Baris yang pindah **tidak dihapus**: supaya
ledger tetap memuat jejaknya, fasenya dicatat di sel bukti dengan kutipan kolom `fase` pada
`docs/FEATURE-PARITY.md` @ `ef8ec40d`.

Konvensi kolom: `status` = status **implementasi** (`covered`, `in-flight`, `surface`, `missing`); anotasi di
sel bukti memuat tiga hal yang berbeda - `fase **Fx**` = kepemilikan fase saat ini, `penutup #NNN` = PR
terbuka yang akan menutupnya, dan tanda `**tanpa penutup**` = belum ada PR/kode sama sekali. Anotasi ini
hasil audit pada `main ef8ec40d` (2026-10-24), bukan klaim gate.

The parity table marks these rows "planned". "surface" means the
capability exists and is tested but the row's CLI/slash surface is not
tested (and in most cases not built); it must not be counted as covered
by the acceptance test until the surface exists.

### CLI

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| CLI-038 | `hermes backup` | in-flight | PR #197: `internal/backup/create_test.go`, `restore_test.go` |
| CLI-039 | `hermes checkpoints` | missing | journal snapshots back undo only; no checkpoints command or test · **in-flight, PR port menyusul**: kode + tes sudah ada di `24d44b12` (#264, MERGED ke stack `f2-cp-api`, belum ada di `main`) |
| CLI-040 | `hermes import` | in-flight | PR #197: `internal/backup/restore_test.go` |
| CLI-046 | `hermes skills` | surface | skills install/retrieve tests (`internal/skills/install_test.go`); no CLI test |
| CLI-047 | `hermes bundles` | missing | no bundle type or test · penutup #270 |
| CLI-057 | `hermes sessions` | surface | `TestHistoryForkAware`, ctxdb history/fork; CLI list/rename/export/prune untested |

### Slash commands

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| SLASH-002 | `/new` | surface | fresh session = `TestForkAndRewind`, ctxdb Create · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-006 | `/history` | surface | ctxdb history tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-007 | `/save` | missing | no conversation export · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-008 | `/retry` | missing | provider retries exist; last-message retry is not tested · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-010 | `/undo` | surface | undo suite (`internal/journal`) · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-011 | `/title` | missing | no session titles · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-013 | `/branch` | surface | `TestForkAndRewind` (PR #227 adds the CLI) · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-015 | `/compress` | surface | `internal/compaction` tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-016 | `/rollback` | surface | journal undo tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-017 | `/snapshot` | surface | PR #197 backup/restore · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-044 | `/resume` | surface | fork/history tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-045 | `/sessions` | surface | ctxdb history tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-066 | `/autoprune` | missing | session purge exists; no auto-prune · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-068 | `/toolsets` | missing | no toolsets · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-069 | `/skills` | surface | `internal/skills` tests · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-070 | `/memory` | surface | `TestQuarantine*`/`TestApprove*` · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-071 | `/bundles` | missing | no bundles · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| SLASH-083 | `/reload-skills` | missing | no rescan-on-command test · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |

### Tools

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| TOOL-070 | `memory` tool | missing | no tool named `memory`; the exposed tools are `recall`/`propose_memory` · penutup #266 |
| TOOL-073 | `session_search` tool | missing | `session_search` appears only as a tool-call parse fixture · penutup #266 |
| TOOL-074 | `skill_manage` | covered | `internal/tools/skills.go` (`schemaSkillManage` + impl, tes `internal/tools/skills_test.go`), wiring `cmd/damnbot/skills_manager_wire.go` (#174 `3cc03815`), daftar payload `cmd/damnbot/modules_test.go:46`, e2e `internal/e2e/skills_test.go:97` `TestSkillManageAlwaysAsks` |
| TOOL-075 | `skill_view` | covered | `schemaSkillView` di berkas yang sama; e2e `internal/e2e/skills_test.go:19` `TestSkillsTools`, `:45` `TestSkillViewQuarantined` |
| TOOL-076 | `skills_list` | covered | `schemaSkillsList` (cap 50; `cmd/damnbot/skills_manager_wire_test.go:456`); e2e `TestSkillsTools` |

### Toolsets

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| TSET-021 | toolset `memory` | missing | no toolset composition · penutup #253 |
| TSET-023 | toolset `session_search` | missing | no toolset composition · penutup #253 |
| TSET-025 | toolset `skills` | missing | no toolset composition · penutup #253 |
| TSET-035 | toolset `context_engine` | missing | no toolset composition · penutup #253 |
| TSET-038 | toolset `debugging` | missing | no toolset composition · penutup #253 |
| TSET-039 | toolset `safe` | missing | no toolset composition · penutup #253 |
| TSET-040 | toolset `coding` | missing | no toolset composition · penutup #253 |
| TSET-041 | toolset `hermes-acp` | missing | no toolset composition · penutup #253 |
| TSET-043 | toolset `hermes-cli` | missing | no toolset composition · penutup #253 |

### Memory

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| MEM-001 | built-in memory | covered | `internal/memory` tests; memstore |
| MEM-002 | user profile | covered | memory scope tests |
| MEM-003 | memory nudge | missing | no nudge · penutup #268 (terblokir #259/#243) |
| MEM-004 | memory write approval | covered | `TestQuarantineCannotBeRetrieved`, `TestTransitions`, `TestUserWriteIsApproved` (`internal/memory`) |
| MEM-011 | session store SQLite | covered | `internal/ctxdb` (`TestStore*`) |
| MEM-012 | session search FTS | covered | `TestSearch*` in ctxdb |
| MEM-013 | recap/resume | surface | history/fork tests; no recap |
| MEM-014 | session export | covered | `cmd/damnbot/session_export.go` (ADR-141, `eda8a74b`); `cmd/damnbot/sessions_admin_test.go` `TestSessionsExport{Markdown,JSONL,HTML,File,RefusesExisting,RedactsMetadata}` |
| MEM-015 | session prune/archive | covered | `cmd/damnbot/sessions_admin.go` (ADR-142, `96c63944`); `TestSessionsPruneArchive`, `TestSessionsPrunePinned`; auto-prune lewat `[sessions] auto_prune` |
| MEM-016 | lineage & titles | surface | `TestHistoryForkAware` covers lineage; titles missing |
| MEM-018 | context compression | covered | compaction stage 2 tests |
| MEM-019 | manual compression | surface | compaction tests; no slash surface · fase **F3** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| MEM-020 | micro-compaction | covered | compaction stage 1 (prune) tests |
| MEM-021 | proactive prune | covered | tool-result stub tests |
| MEM-022 | native compaction provider | missing | no provider-native compaction · fase **F5** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| MEM-023 | context engine plugin | missing | see PLUG-014 · fase **F8** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| MEM-024 | @-ref | missing | no reference expansion · penutup #267 |
| MEM-025 | context pin | covered | **Ditutup #318** (squash `9a57a737`, 2026-10-26) di atas covernya **#304** (squash `f8ee2626`): pin sisi prompt hidup di tabel append-only `session_pins` (migrasi `internal/ctxdb/migrations/0012_session_pins.sql`, nilai terakhir menang) dan dibaca prompt lewat `internal/ctxdb/sessionpins.go:10` `pinSeqsQ` serta klausa `session_pins` di `internal/ctxdb/compactstore/compactstore.go:76-80` `eventsQ`; permukaan CLI-nya `damnbot sessions context-pin <id\|newest> [--at <seq\|turn:ID>] [--off]` (`cmd/damnbot/sessions.go:48`). Bukti **biner asli** (harness #239, squash `025ea0c4`): `internal/e2e/context_pin_compaction_test.go` (+141/-0, squash `9a57a737`), `TestContextPinSurvivesCompaction` — fakta yang di-pin tetap ada di request terakhir sesudah compaction (`:56`), dan request itu benar-benar dirakit dari view ter-compaction (`:53`). Prasyarat wiring compaction: **#219** (squash `bee69b31`) memasang `Deps.Compactor`. Tiga flip load-bearing (diverifikasi ulang pada pohon `b8d3d393`, log `~/.cache/damnbot/evidence-mem025/flips.log`): (1) `sessionpins.go` `pinSeqsQ` `p.pinned 1→2` ⇒ MERAH `context_pin_compaction_test.go:50` (`context-pin list = 0 "no pinned context"`); (2) klausa `session_pins` di `compactstore.go` `eventsQ` dibuang ⇒ MERAH `:56` (`pinned fact did not survive compaction (MEM-025)`); (3) `keepTail 8→1000` (`internal/compaction/summary.go:17`) ⇒ MERAH `:53` (`the retried turn was not assembled from the compacted view`), masing-masing hijau lagi sesudah dipulihkan. |
| MEM-026 | context files | covered | **Ditutup #310** (squash `2f42d75a`, 2026-10-25): tier berkas konteks kini diisi produksi - `cmd/damnbot/context_files.go:41` `Files: contextFiles()` (daftar `:14-17`: AGENTS.md, CLAUDE.md, SOUL.md, .cursorrules, .hermes.md), `:43` `UntrustedFiles: !model.trusted`, dan `:37-39` helper `contextTierInput` mengisi `Facts`/`Degraded` - sehingga wrapper `<untrusted source="workspace">` di `internal/assemble/context.go:109` hidup. Bukti e2e (`TestE2EContextFileWrappedUntrusted`): `internal/e2e/context_files_test.go:130` (B1), `:148` (B2), `:166` (B3 via `trustWorkspace` = `internal/e2e/harness_test.go:116`), `:183` (B4), nol `t.Skip`; hijau di CI `run 40149947287` head `ae2b2caf` (termasuk `test (windows-latest, -race, 1)`) dan berkas tesnya identik di pohon gabungan. Flip load-bearing di `2f42d75a` (mutasi 1-baris di worktree detached, lalu dibuang): C1 `UntrustedFiles: false` ⇒ **B1 merah**; C2a `trusted: false` ⇒ **B3 merah**; C2b `trusted: true` ⇒ **B1 merah**; C3 `Files: nil` ⇒ **B1+B3 merah**. **Caveat jujur**: flip C4 (bungkus teks prompt) **tidak terbukti** - dua mutasi sama-sama no-op (`in.Query` kosong tanpa retriever, teks user tidak lewat `run.go:627`/`:645`), jadi klaim lama "C4 ⇒ B2 merah" dicabut; bukti pengganti struktural (literal `workspace` hanya di `context.go:109`) dan itu pekerjaan lanjutan, bukan syarat penutupan. |
| MEM-028 | rewind | covered | `TestForkAndRewind` |
| MEM-029 | branch/fork | covered | `TestForkAndRewind`, `TestForkContextTierCopied` |
| MEM-030 | retry | missing | last-message retry not tested · penutup #259 |
| MEM-031 | filesystem checkpoints | covered | journal undo suite |
| MEM-032 | state snapshot | in-flight | PR #197 backup/restore of config+state |
| MEM-033 | memory trim | missing | no RSS trim · penutup #267 |
| MEM-034 | prompt caching | missing | no cache_control use · penutup #268 (terblokir #259/#243) |

### Skills

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| SKILL-001 | SKILL.md system | covered | `internal/skills` retrieval tests |
| SKILL-002 | skills_list / skill_view / skill_manage | covered | ketiga tool di atas: `internal/tools/skills.go` + e2e `internal/e2e/skills_test.go` (`TestSkillsTools`, `TestSkillViewQuarantined`, `TestSkillManageAlwaysAsks`) |
| SKILL-004 | preprocessing | missing | no template/inline-shell tests · penutup #269 |
| SKILL-007 | bundles | missing | no bundles · penutup #270 |
| SKILL-008 | auto-load | covered | `TestRetrieveLimit`, retrieval tiers in `TestContextTierFrozen` |
| SKILL-009 | project discovery | covered | `internal/config/config.go:31 ProjectDiscovery` + pemuat multi-root (`9cf1fa27`); e2e `TestImportedSkillQuarantined`, `TestSkillsTrustCLI` |
| SKILL-010 | external skill dirs | covered | `internal/config/config.go:33 ExternalDirs` + `validate.go:175`; e2e `internal/e2e/skills_test.go:160` |
| SKILL-016 | bundled skills sync | covered | builtin install tests |
| SKILL-018 | guard/lint | covered | `TestLintCorpus`, `TestLintFailsClosed`, `TestQuarantine*` |
| SKILL-019 | ledger & provenance | surface | skillstore tracks sha; no provenance ledger test |
| SKILL-022 | write approval | covered | `TestQuarantine*` |
| SKILL-023 | skills index | missing | no index build/delta · penutup #273 |

### Plugins and operations

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| PLUG-014 | context_engine plugin | missing | no engine slot · fase **F8** (ADR-139; kolom `fase` `docs/FEATURE-PARITY.md` @ `ef8ec40d`) |
| PLUG-036 | tool search / deferred tools | covered | `tool_search` tests, PR #174 wiring |
| OPS-006 | backup / import | in-flight | PR #197 |
| CORE-004 | toolsets & per-platform config | missing | no toolsets · penutup #253 |
| CORE-005 | custom toolsets | missing | no toolsets · penutup #253 |

### What the gate enforces vs what this ledger reports

`make phase-acceptance` (`Makefile:136-137`: `DAMNBOT_PHASE_ACCEPTANCE=1 go test -tags goolm -count=1 $(PKGS)`)
**tidak** memakai baris parity di dokumen ini sebagai gate. Tidak ada satu berkas `.go` yang membaca
`docs/F2-ACCEPTANCE.md` (grep = 0). Yang benar-benar digerbangi:

* `TestEdgeCaseCatalogSync` (`internal/testutil/phase`): mem-parse tabel EC ROADMAP + katalog setiap paket.
  Id dengan fase **< PHASE** wajib punya baris katalog dan berstatus `covered`/`n/a`; id fase **= PHASE**
  boleh `pending:<PHASE>` atau absen; id **> PHASE** hanya di-log; label fase katalog wajib cocok ROADMAP.
* `TestParityPhaseColumn` (`internal/archtest/parity_phase_test.go`; 854 baris, 837 baris bergerbang):
  hanya memeriksa **kolom `fase`** konsisten dengan tabel "Pemetaan parity -> fase" di ROADMAP - bukan
  status implementasi.

Akibatnya angka di ledger ini **tidak** memblokir atau meloloskan gate mana pun; ia laporan. `PHASE` di
`main` berisi `F1` (`F1\n`, 3 byte) dan belum pernah dinaikkan sejak `ad961ebc`. Bukti gate terakhir pada
`PHASE=F3` (pohon `ef8ec40d`, 2026-10-24): `ok internal/testutil/phase 0.155s` dan `ok internal/archtest
35.568s`, dengan satu baris **log** (bukan kegagalan) `acceptance_test.go:54: PHASE acceptance F3: 69 EC ids
of a later phase are still open`.

### F2 e2e suite: the nine scenarios (biner nyata)

**Sumber kanonik: body PR #239** (<https://github.com/yuldan/damn-bot/pull/239>) - bagian "Passing on main now"
(4a, 4b) dan "Pending scenarios" (1, 2, 3, 5, 6, 7, 8, 9; nomor 4 dipecah 4a/4b). **Tidak ada berkas repo yang
mengenumerasi kesembilan skenario ini**: `docs/ROADMAP.md` §F2 (baris 130-140) hanya memuat acceptance
P1/undo/recall/EC, dan dokumen ini sebelumnya tidak memuatnya - tabel di bawah adalah salinan pertama di repo.
Di kode, skenario dikenali dari komentar `scenario N of PR #239` (`internal/e2e/undo_review_test.go:23`,
`budget_guard_ledger_test.go:150`/`:226`, `sessions_fork_prefix_test.go:124`) dan dari komentar
`acceptance 4b`/`acceptance 9`.

| # | skenario | tes (`berkas:baris`) | kontrol negatif (load-bearing) | status |
| --- | --- | --- | --- | --- |
| 1 | compaction -> summary di SessionView + pembungkusan `<untrusted>` | `internal/e2e/summary_taint_test.go:47` `TestTaintedSummaryDeniesUnsafeShell` | `summary_taint_test.go:86` `TestUntaintedTurnControlShellRuns` - shell JALAN saat turn tidak taint, jadi yang memblokir adalah taint-nya, bukan penolakan menyeluruh | in-flight #219/#220 (W6/W7) |
| 2 | tier memory/recall + isolasi cross-project & cross-principal | `internal/e2e/recall_scope_test.go:159` `TestRecallIgnoresAnotherProject`, `:178` `TestRecallIgnoresAnotherPrincipal` | kedua tes itu sendiri: scope proyek/principal lain tidak boleh terlihat | in-flight #283 |
| 3 | failed-turn `context_exclude` pada request berikutnya + daftar side effect | `internal/e2e/resume_test.go:13` `TestRunResumeExcludesFailedTurn`, `:52` `TestRunContinueNewestSession` | belum ada yang eksplisit (turn yang BERHASIL tidak boleh dikecualikan) | in-flight #243/#259 |
| 4a | carrier di-replay byte-identik (PROV-01/SEC8) | `internal/e2e/carriers_test.go:12` `TestCarrierReplayedNextIteration` | - (pasangannya 4b) | ada di main (#239) |
| 4b | carrier yang ditolak tidak di-replay | `internal/e2e/carriers_test.go:35` `TestCarrierRejectedNotReplayed` | request retry tidak memuat byte carrier + jumlah provider call = 3 | ada di main (#239) |
| 5 | `undo` byte-identik + listing `review` | `internal/e2e/undo_review_test.go:29` `TestE2EUndoRestoresTheEditedFileAndListsTheAction`; jalur penolakan: `internal/e2e/undo_test.go:130` `TestUndoSymlinkedRoot`, `:168` `TestUndoSymlinkInsideRefused`, `:232` `TestUndoRefusalContentConflict` | berkas "bystander" tetap byte-identik dan tidak muncul di `undo ls`; `review` (karantina memori P10) tidak mengklaim perubahan berkas; penolakan symlink dibuktikan bersama kontrol `undo_test.go:213` `TestUndoWithoutSymlinkSucceeds` (yang menolak adalah aturan tanpa-symlink, bukan kegagalan umum) | ada di main (#289 + jalur penolakan #240) |
| 6a | budget guard menolak di cap ledger | `internal/e2e/budget_guard_ledger_test.go:154` `TestE2EBudgetGuardDeniesAtLedgerCap` (+ `:198` bawah cap, `:215` periode lain) | provider request = 0 saat ditolak; kontrol bawah-cap dan periode lain | ada di main (#289) |
| 6b | ledger mencatat usage persis | `internal/e2e/budget_guard_ledger_test.go:230` `TestE2EBudgetLedgerRecordsExactUsage` | call tanpa harga tidak dibebankan (`CostMicros=0`, `UnknownCost=1`) | ada di main (#289) |
| 7 | prefix anak `sessions fork`/`rewind` | `internal/e2e/sessions_fork_prefix_test.go:127`, `:170`, `:206` | induk tidak berubah (append-only), sesi lain tidak berubah, fork di head = transkrip utuh, cut 0 = kosong. **GAP:** belum ada penolakan id/`--at` salah (lihat §4 item 11) | ada di main (#289) |
| 8 | canary `storage.encrypt=true` + backup/restore | `internal/e2e/crypto_test.go:33` `TestCryptoBackupRestore` (sweep plaintext di data dir, arsip DMBK1, dan direktori hasil restore, lalu lanjut sesi) + `internal/e2e/spill_test.go:72` `TestToolOutputSpillThroughBinary` | `crypto_test.go:168` `TestCryptoPlaintextCanaryVisible` - tanpa enkripsi canary TERLIHAT, jadi sweep-nya terbukti mampu melihat plaintext; ditambah kasus `negative-control` (unencrypted) di `spill_test.go:80-81` | ada di main (#241, #290) |
| 9 | buka read-only DB skema lebih baru + kode exit SPEC | `internal/e2e/sessions_readonly_test.go:14` `TestSessionsReadOnlyNewerSchema` | penulis keluar exit 1 dengan `schema_newer` dan 0 provider call (read-only tetap exit 0) | ada di main (#248) |
| - | payment/exec gate (batas keamanan di luar 9 skenario, dari item 4 §4) | `internal/e2e/payment_gate_test.go:83` `TestPaymentSkillGateCLI` | belum terbukti - tes ini merah di Windows (`tool result denied = false, want true`) | in-flight #237 (GAP, §4 item 11) |

**Penyegaran.** Audit awal pada `b979bc83`; setelah #240 (`a1845da8`) mendarat, jalur penolakan `undo` sudah ada di `main` (`internal/e2e/undo_test.go`) sehingga GAP-nya tertutup - baris skenario 5 di atas sudah mencerminkannya.

**Ringkas.** 6 dari 9 skenario ada di `main` (4a, 4b, 5, 6a, 6b, 7, 8, 9); skenario **1, 2, 3** belum ada di
`main` tetapi tesnya sudah ditulis di cabang in-flight (#219/#220, #283, #243/#259). Ekstra di luar sembilan
skenario (mendukung baris acceptance lain, semua punya kontrol): `TestSkillsTools`, `TestSkillViewQuarantined`,
`TestSkillManageAlwaysAsks`, `TestImportedSkillQuarantined`, `TestSkillsTrustCLI` (`internal/e2e/skills_test.go`,
#281), `TestSessionsAdminExport`/`Prune` (#255), `TestSessionsLabelsEndToEnd`/`TestSessionsLabelsNegativeControls`
(#277), `TestRunAdvertisesNoDeferredTools` (#174), `TestHarnessSmoke` (#239).

## 4. Work items to close the ledger

These are the concrete PRs implied by the table above. Each item names
the owner prefix (branch names should use it) and the smallest test that
proves it.

1. **Acceptance completeness (PR #244, implemented).**
   `TestEdgeCaseCatalogSync` in `internal/testutil/phase` parses the
   ROADMAP EC table and every package catalog and, in acceptance mode,
   fails when an id at or below PHASE has no catalog row or is not
   `covered`/`n/a`; it also rejects catalog phases that disagree with the
   roadmap and ids the roadmap does not require. Normal `go test` keeps
   the run green and logs the uncataloged ids. The 16 F1 ids above and the
   three missing STATE ids are what it now reports; the GAP rows still
   need their own check (no phase column yet).
2. **tools:** implement `skill_view`/`skills_list`/`skill_manage`
   (TOOL-074/075/076, SKILL-002) — first unblocks EC-CTX-006.
3. **ctxdb:** migration fail-closed test (GAP-REL-05); FTS auto-upgrade
   and CJK order (GAP-INF-01/02); 50-client chaos (GAP-REL-03).
4. **provider:** no-context-probe analyzer + test (GAP-COST-02);
   EC-PROV-008 (finish_reason=length partial & not persisted).
5. **loop/tools:** EC-TCALL-010 (deferred schema INVALID_ARGUMENT);
   GAP-INF-06 rerun determinism; GAP-COST-04 prefix stability across
   fallback; MEM-030 retry.
6. **assemble:** GAP-PROV-02 tier budget assertion.
7. **ctxdb/exec/Windows F1/F2 (done):** EC-WIN-001/009/014/016/017 in #254;
   EC-WIN-002/005/011 in #257; the F1 FIO/TCALL/TERM rows in #255 (the
   EC-FIO-019 test moved to `cmd/damnbot/edgecases_volatile_test.go` as
   `TestVolatileTimeIsUTC`).
8. **skills/CI:** builtin dangling-ref rule (GAP-SEC-26); native-arch
   skill filter (GAP-TOOL-10).
9. **backup:** land PR #241's stack; add the plaintext-canary test to
   GAP-SEC-01.
10. **docs:** peta baris parity sudah diperbarui (audit `ef8ec40d`): **8 baris ternyata `covered`** di
    `main`, **22 baris pindah fase** (ADR-139) dan tetap tercatat dengan fase barunya, **22 baris masih F2
    dan punya penutup PR terbuka** (#253, #259, #266, #267, #268, #269, #270, #273), dan **1 baris tanpa
    penutup**: `CLI-039` (checkpoints CLI; kodenya menunggu PR port dari stack `f2-cp-api`). `MEM-025`
    (context pin) ditutup #318 di atas cover #304, dan `MEM-026` (berkas konteks) ditutup #310 — lihat §4
    item 11(d) untuk yang terakhir. Sisa pekerjaan F2 = mendaratkan penutup itu, atau memindahkan barisnya ke fase
    pemiliknya lewat koreksi ADR-064. Baris "planned" tanpa penutup adalah lubang yang ditutup ledger ini.

11. **e2e:** tutup celah suite biner nyata (§3 "F2 e2e suite"). (a) Skenario 1-3 menunggu pendaratan
    #219/#220, #283, #243/#259; saat mendarat, verifikasi tambahan: skenario 1 juga untuk summary yang dibangun
    dari BACA BERKAS (bukan hanya `shell`), skenario 2 butuh satu kasus positif dengan marker unik per scope
    supaya "0 hasil" tidak lolos karena recall-nya memang mati, dan skenario 3 butuh kontrol eksplisit bahwa
    turn yang **berhasil** tidak dikecualikan. (b) `TestE2EForkRefusesUnknownSessionAndBadCut` (`internal/e2e`): id tak
    dikenal exit 1 (`ctxdb.ErrNotFound`), `--at` salah exit 2; kontrol: transkrip induk tetap byte-identik dan
    tidak ada baris anak yang lahir. (c) #237 (`TestPaymentSkillGateCLI`, `internal/e2e/payment_gate_test.go`)
    harus mendarat bersama pasangan kontrolnya: perintah payment ditolak **dan** berkas efek sampingnya tidak
    ada, sedangkan perintah non-payment dengan `--approval auto` tetap jalan. (d)
    `TestE2EContextFileWrappedUntrusted` (`internal/e2e`): isi `AGENTS.md` ber-marker muncul di request di
    dalam `<untrusted source="workspace">` dengan role user; kontrol: teks instruksi di berkas itu tidak pernah
    muncul sebagai pesan system/developer, dan tanpa berkas wrapper-nya tidak ada. **Catatan 2026-10-24:**
    baris `MEM-026` kini berstatus `missing` (dulu salah `covered`) karena tier berkas konteks belum tersambung
    di produksi (`cmd/damnbot/run.go:212-221` tidak pernah mengisi `Files`, `UntrustedFiles` nol pemanggil) -
    jadi prasyarat tes ini adalah **wiring-nya lebih dulu**, bukan hanya tesnya. **Ditutup 2026-10-25 oleh
    #310** (squash `2f42d75a`): wiring mendarat di produksi (`cmd/damnbot/context_files.go:41` `Files`,
    `:43` `UntrustedFiles`) dan tes e2e di atas hijau di CI head `ae2b2caf` termasuk
    `test (windows-latest, -race, 1)`; di pohon gabungan flip C1/C2a/C2b/C3 menjatuhkan hanya subtes yang
    seharusnya, sementara flip **C4 tidak terbukti** (dicatat jujur di baris §3 dan di bawah). **Sudah tertutup:**
    kontrol penolakan `undo` (symlink ke luar/di dalam root, konflik isi) mendarat lewat #240 -
    `internal/e2e/undo_test.go:130`/`:168`/`:232` dengan kontrol positif `:213`.

## 5. Security-boundary evidence (e2e biner nyata + kontrol negatif)

Setiap permukaan batas keamanan butuh (a) minimal satu tes e2e lewat biner nyata (`internal/e2e`, dibangun di `harness_test.go:29`) dan (b) minimal satu kontrol negatif yang **load-bearing**: kontrol itu harus MERAH kalau perilaku yang dijaganya dimatikan. Kolom "flip" mencatat eksperimen pembuktiannya (satu baris patch di worktree sekali-pakai, lalu dibuang) dan kolom "baris MERAH" mengutip keluaran apa adanya. Semua eksperimen dijalankan 2026-10-24 pada pohon `main a1845da8` (kode tidak berubah setelah itu; `main 6d644a92` = docs-only).

| Permukaan | Tes positif (`berkas:baris`) | Kontrol negatif (`berkas:baris`) | Load-bearing | Flip yang dipakai | Baris MERAH (verbatim) |
|---|---|---|---|---|---|
| 1. undo/sandbox/journal | `internal/e2e/undo_review_test.go:29`; `internal/e2e/undo_test.go:130` | `undo_test.go:168` (symlink di dalam root) + pasangan positif `:213` | **terbukti** | `internal/journal/undo.go:311` → `false && !u.sb.SamePath(...)` | `undo_test.go:197: [undo --data-dir …] accepted a linked directory below the root` (FAIL `TestUndoSymlinkInsideRefused`) |
| 1. undo — gerbang konflik isi | `undo_test.go:232` (jalur `--force` L249) | `undo_test.go:232` sendiri (menolak + berkas tak berubah) | **terbukti** | `internal/journal/undo.go:162` → `case false && !match(cur, a.After) && !force:` | `undo_test.go:243: undo = 0, stderr ""; want a conflict refusal` (FAIL `TestUndoRefusalContentConflict`) |
| 1. undo — scoping listing | `undo_review_test.go:29` (L59-64) | `undo_review_test.go:65` | **terbukti** | `cmd/damnbot/undo.go:261 previewActions`: saat `list` juga `os.ReadDir(".")` | `undo_review_test.go:66: undo ls lists a file the turn never wrote:` (FAIL `TestE2EUndoRestoresTheEditedFileAndListsTheAction`) |
| 1. undo — witness isi | `undo_review_test.go:29` (L52-57, L78-86) | `undo_review_test.go:55` / `:84` (bystander byte-identik) | **belum dibuktikan** | tidak ada flip 1-baris yang wajar | mode regresi yang dijaga: undo/turn menulis berkas di luar jalur yang dijurnal (dengan isi berbeda); bukan kegagalan yang bisa dipicu dengan mematikan satu fitur |
| 2. exec/classifier/payment gate | **tidak ada** | **tidak ada** | — | — | **GAP**: belum ada tes e2e lewat biner sama sekali; PR tesnya sedang dikerjakan. Bukti in-process (`internal/exec/classify/payment_test.go`, `internal/tools/builtins_shell.go:185-194`, `cmd/damnbot/skills_wire_test.go:167`) tidak menggantikan e2e. |
| 3. crypto/at-rest | `internal/e2e/crypto_test.go:33`; `internal/e2e/spill_test.go:72` (subtes `encrypted`); `internal/e2e/sessions_readonly_test.go:14` | `crypto_test.go:168` (tanpa enkripsi canary harus terlihat); `spill_test.go:81` + sapuan yang sama di `:106` | **terbukti** | `internal/e2e/crypto_test.go` `scanCanaries` → `return false` (sapuan dilumpuhkan) | `crypto_test.go:173: canary [plaintext-canary-reply] absent from /tmp/…/data` (FAIL `TestCryptoPlaintextCanaryVisible`); tes positifnya justru tetap PASS — asersi canary-nya hanya sekuat sapuan |
| 3. crypto/at-rest — determinasi fitur | `crypto_test.go:33` | — | **terbukti** | `cmd/damnbot/atrest_wire.go:21` → `if true` (codec tidak pernah dipakai) | `crypto_test.go:157: plaintext canary "prompt-canary-crypto" in /tmp/…/data/damnbot.db` (FAIL `TestCryptoBackupRestore`) |
| 3. spill / blob | `spill_test.go:72` (subtes `encrypted`) | `spill_test.go:81` (`negative-control`) | **terbukti** | `internal/e2e/spill_test.go:61` → `return found, false` | `spill_test.go:107: canary in data dir = false, want true` (FAIL `TestToolOutputSpillThroughBinary/negative-control`) |
| 4. karantina skill | `internal/e2e/skills_test.go:19`, `:199` | `skills_test.go:45`; `skills_test.go:163` | **terbukti** | `internal/ctxdb/skillstore/skillstore.go:301` (filter `m.status = 'approved'` dilepas) | `skills_test.go:57: skills_list did not exclude the quarantined skill:` (FAIL `TestSkillViewQuarantined`); `:188` + `:191` (FAIL `TestImportedSkillQuarantined`) |
| 4. persetujuan skill (`skill_manage`) | `skills_test.go:19` (jalur tool) | `skills_test.go:97` (`:106` asersi terstruktur + `:113` efek samping) | **terbukti** | `internal/tools/skills.go:151` `RequireAsk` → `RequireAuto` | `skills_test.go:114: denied skill wrote /tmp/…/data/skills/agent/agent-note: <nil>` (FAIL `TestSkillManageAlwaysAsks`) |
| 4. catatan mekanisme approval | — | — | negatif (menjelaskan gerbang) | `internal/tools/skills.go:146` `Effect` → `EffectRead` **tidak mengubah apa pun** (tetap PASS) | gerbang approval berasal dari `Inspect` → `PolicyDecision{Requirement: RequireAsk}` (`internal/tools/skills.go:148-152`; enum `internal/tools/approval.go:36-45`), bukan dari `Effect` — penting untuk review PR yang menyentuh jalur ini |
| 5. budget guard — arah tolak | `budget_guard_ledger_test.go:154`, `:230` | `:198`, `:215` | **terbukti** | `internal/budget/guard.go:88` `Err()` → selalu `&DeniedError{}` | `budget_guard_ledger_test.go:205: call denied below the cap: …` (FAIL `TestE2EBudgetGuardAllowsUnderCap`) dan `:222: yesterday's spend denied today's call` (FAIL `TestE2EBudgetGuardIgnoresSpendOutsideThePeriod`) |
| 5. budget guard — arah boleh | `:198`, `:215` | `:154` | **terbukti** | `internal/budget/guard.go:88` → `if true { return nil }` | `budget_guard_ledger_test.go:180: stop = error/provider_error, want max_tokens/budget_daily` (FAIL `TestE2EBudgetGuardDeniesAtLedgerCap`) |
| 6. berkas/tool-output ke prompt | `internal/e2e/carriers_test.go:12`; `skills_test.go:37` (berkas → prompt lewat body skill); `spill_test.go:72` | `carriers_test.go:35`; `spill_test.go:81` | **terbukti** | `internal/provider/client.go:315` (jalur retry tanpa carrier dimatikan) | `carriers_test.go:44: exit = 2` (FAIL `TestCarrierRejectedNotReplayed`) |
| 6. catatan pemisahan kontrol | `carriers_test.go:12` | `carriers_test.go:35` | sebagian | `internal/loop/turn.go:220` → `reply.Carriers = nil` (replay dimatikan) | `carriers_test.go:28: second request did not replay the carrier "SIG-E2E-REPLAY"` (FAIL `TestCarrierReplayedNextIteration`) **dan** kontrolnya juga MERAH (`carriers_test.go:44: exit = 2`): pemisahan "positif merah, kontrol hijau" tidak bisa dicapai karena keduanya memakai jalur store→replay yang sama; arah yang bisa dipisah adalah sebaliknya (flip jalur retry → hanya kontrol yang merah) |
| 6b. rute URL/fetch | — | — | — | — | **bukan F2**: tidak ada tool web di biner (`TOOL-087`, `TSET-030`, `EXEC-042` = F6 `planned`, `docs/FEATURE-PARITY.md:351/387/906`); `SPEC.md:2644` sudah menetapkan wrap `<untrusted>` + penolakan IP privat/metadata + anti-DNS-rebinding untuk saat mendarat |
| 6c. berkas konteks workspace (MEM-026) | — | — | — | — | **GAP wiring**: `ContextInput.UntrustedFiles` tidak pernah diisi produksi (nol penugasan di `*.go` non-test) dan tier berkas konteks tidak dirender (`cmd/damnbot/run.go:213-216` hanya mengisi `Facts`/`Degraded`); dikoreksi di PR #300, perbaikan wiring terpisah → **ditutup #310** (squash `2f42d75a`): wiring di `cmd/damnbot/context_files.go:41`/`:43`, e2e `internal/e2e/context_files_test.go:130`/`:148`/`:166`/`:183` hijau (incl. Windows), flip C1/C2a/C2b/C3 load-bearing; **flip C4 tidak terbukti** (pekerjaan lanjutan) |

**Kontrol yang tidak boleh diterima.** (D1) kontrol yang hanya membuktikan "tanpa berkas ⇒ tidak ada wrapper"; (D2) tes positif yang marker-nya juga ada di prompt pengguna (asal marker ambigu); (D3) tes yang hanya menyuntik `assemble.ContextInput` di level unit, bukan lewat biner; (D4) tes yang membuat workspace harness selalu trusted demi menjaga `spill_test.go:98` tetap hijau; (D5) perbandingan trust yang menganggap path non-kanonik (cwd lewat symlink) sebagai trusted; (D6) menaikkan status baris ledger tanpa tes + bukti flip; (D7) `t.Skip` lingkungan tanpa `skipOrFail` (pola `undo_test.go:23`).

## Method

* EC: `git grep -n '<ID>' origin/main -- '*_test.go'`, plus the gate
  statuses in `internal/compaction/edgecases_test.go` and the
  `platformStateCases` split under `internal/ctxdb`.
* GAP: each row's test name searched with `git grep` on `origin/main`
  and `git log --all -S<name> -- '*.go'`; PR provenance from
  `git diff origin/main..pr/N -- '*_test.go'`.
* Parity: each F2 row matched against test names on `main` and in PR
  diffs, then reviewed by capability (one capability can have several
  parity rows). A row is `covered` only when a named test proves the
  capability; the CLI/slash surface is called out separately. Audit ulang 2026-10-24 pada `main
  ef8ec40d`: fase setiap baris dikutip dari kolom `fase` `docs/FEATURE-PARITY.md`, dan setiap baris
  `missing` dicocokkan ulang ke berkas + nama tes di `main` (`git grep`) - hasilnya anotasi
  `fase`/`penutup` di §3.
  capability; the CLI/slash surface is called out separately.
