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
| EC — required by ROADMAP F2 | 41 | 30 | 3 | — | 8 |
| GAP — DECISIONS.md L247-265 | 18 | 5 | 2 | 2 | 9 |
| Parity — F2 rows | 81 | 18 | 4 | 17 | 42 |

The eight missing EC rows are `EC-CTX-006`, `EC-CTX-007`, `EC-CTX-017`
(declared `pending:F2`), `EC-STATE-001`, `EC-STATE-007`, `EC-STATE-009`
(absent from `TestStateEdgeCases`), and `EC-PROV-008`, `EC-TCALL-010`.
`EC-WIN-002`, `EC-WIN-005`, `EC-WIN-011` moved from missing to in-flight in
this PR (windows-latest runner); the sixteen uncataloged F1 ids are tabulated
below with the sibling PRs that close them.

---

## 1. Edge cases required by ROADMAP F2

| EC | test | location (main) | status | owner |
| --- | --- | --- | --- | --- |
| EC-CTX-001 | `TestEdgeCases/EC-CTX-001` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-002 | `TestEdgeCases/EC-CTX-002` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-003 | `TestEdgeCases/EC-CTX-003` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-004 | `TestEdgeCases/EC-CTX-004` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-005 | `TestEdgeCases/EC-CTX-005` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-006 | `TestEdgeCases/EC-CTX-006` (`pending:F2`) | `internal/compaction/edgecases_test.go` | missing | compaction |
| EC-CTX-007 | `TestEdgeCases/EC-CTX-007` (`pending:F2`) | `internal/compaction/edgecases_test.go` | missing | compaction |
| EC-CTX-008 | `TestEdgeCases/EC-CTX-008` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-009 | `TestEdgeCases/EC-CTX-009` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-010 | `TestEdgeCases/EC-CTX-010` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-011 | `TestEdgeCases/EC-CTX-011` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-012 | `TestEdgeCases/EC-CTX-012` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-013 | `TestEdgeCases/EC-CTX-013` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-014 | `TestEdgeCases/EC-CTX-014` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-015 | `TestEdgeCases/EC-CTX-015` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-016 | `TestEdgeCases/EC-CTX-016` | `internal/compaction/edgecases_test.go` | covered | compaction |
| EC-CTX-017 | `TestEdgeCases/EC-CTX-017` (`pending:F2`) | `internal/compaction/edgecases_test.go` | missing | compaction |
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
| EC-PROV-008 | — | — | missing | provider |
| EC-TCALL-010 | — | — | missing | tools/loop |
| EC-WIN-002 | `TestWindowsF2EdgeCases/EC-WIN-002` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | in-flight (this PR) | ctxdb |
| EC-WIN-005 | `TestWindowsF2EdgeCases/EC-WIN-005` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | in-flight (this PR) | ctxdb |
| EC-WIN-011 | `TestWindowsF2EdgeCases/EC-WIN-011` | `internal/ctxdb/win_f2_edgecases_windows_test.go` | in-flight (this PR) | ctxdb |

Note: `EC-CTX-006`, `-007` and `-017` are declared `pending:F2` inside the
compaction gate but their IDs are still accepted by the completeness test
because the gate names them. The roadmap's "F2 = all EC-CTX ids" therefore
passes while three are in fact unimplemented. This is exactly the class of
silent miss the acceptance test must catch (§4, item 1).

### F1 ids with no catalog row

The same cross-check over the whole roadmap (not only F2) found 16 ids whose
roadmap phase is F1 but which no test named. The F1 acceptance run was
vacuous for them because the gate only saw declared rows. The cross-check gate
listed in §4 reports them; two PRs close them:

| EC | test | location | status |
| --- | --- | --- | --- |
| EC-FIO-002 | `TestEdgeCases/EC-FIO-002` | `internal/exec/edgecases_test.go` | in-flight (PR #255) |
| EC-FIO-005 | `TestSurrogateEdgeCases/EC-FIO-005` | `internal/provider/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-FIO-006 | `TestTurnAggregateScaled/EC-FIO-006` | `internal/tools/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-FIO-009 | `TestSurrogateEdgeCases/EC-FIO-009` | `internal/provider/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-FIO-012 | `TestEdgeCases/EC-FIO-012` | `internal/exec/edgecases_test.go` | in-flight (PR #255) |
| EC-FIO-016 | `TestSSEEncodingEdgeCases/EC-FIO-016` | `internal/netx/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-FIO-017 | `TestSSEEncodingEdgeCases/EC-FIO-017` | `internal/netx/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-FIO-019 | `TestTimeZoneEdgeCases/EC-FIO-019` | `internal/assemble/edgecases_fio_test.go` | in-flight (PR #255) |
| EC-TCALL-009 | `TestEdgeCases/EC-TCALL-009` + `TestRunAnswerSchema` | `internal/loop/edgecases_test.go`, `cmd/damnbot/run_inject_test.go` | in-flight (PR #255) |
| EC-TERM-002 | `TestEdgeCases/EC-TERM-002` | `internal/exec/edgecases_test.go` | in-flight (PR #255) |
| EC-TERM-007 | `TestProbeTimeout/EC-TERM-007` | `internal/ctxdb/edgecases_term_test.go` | in-flight (PR #255) |
| EC-WIN-001 | `TestWindowsF1EdgeCases/EC-WIN-001` | `internal/exec/win_f1_edgecases_windows_test.go` | in-flight (PR #254) |
| EC-WIN-009 | `TestWindowsF1EdgeCases/EC-WIN-009` | `internal/ctxdb/win_f1_edgecases_windows_test.go` | in-flight (PR #254) |
| EC-WIN-014 | `TestWindowsF1EdgeCases/EC-WIN-014` | `internal/codetools/win_f1_edgecases_windows_test.go` | in-flight (PR #254) |
| EC-WIN-016 | `TestWindowsF1EdgeCases/EC-WIN-016` | `internal/exec/win_f1_edgecases_windows_test.go` | in-flight (PR #254) |
| EC-WIN-017 | `TestWindowsF1EdgeCases/EC-WIN-017` | `cmd/damnbot/console_windows_test.go` | in-flight (PR #254) |

The two behaviours that needed code are EC-FIO-006 (the aggregate now scales
with the window) and EC-TCALL-009 (`--answer-schema` produces
`invalid_answer`, ADR-155).

## 2. GAP rows (DECISIONS.md L247-265)

| GAP | requirement | test(s) | status | owner |
| --- | --- | --- | --- | --- |
| GAP-SEC-01 | Backup secrets stay encrypted | `TestCreateVerify`, `TestRoundTrip`, `TestWrongKey`, `TestTamperEveryByte` (`internal/backup/create_test.go`; perms via `checkResult`: 0600 file / 0700 dir); no plaintext-canary test yet | in-flight (PR #241 tip of the 185–201 stack) | backup |
| GAP-SEC-15 | Payment always prompts | `TestMonetaryAlwaysPrompts`, `TestMonetaryNeverSession` | covered | tools |
| GAP-SEC-16 | egress DLP | `TestEgressDLP` + `TestEgressDLPBodyWarning` — F1 ingress subset only; the F2/F4–F7 portion (§SPEC half) is not tested | surface | netx |
| GAP-REL-01 | Failed turn persisted | `TestFailedTurnPersisted` + `TestFailedTurnEmptyContent` | covered | loop |
| GAP-REL-03 | Chaos: 50 concurrent clients, 30 min | `TestChaos50Clients`, nightly `chaos` | `internal/ctxdb/chaos_test.go`, `.github/workflows/nightly.yml` | covered | ctxdb |
| GAP-REL-04 | SQLite engine version | `TestSQLiteEngineVersion` exists; no `sqlite-engine` CI job | covered (no job) | provider/ctxdb |
| GAP-REL-05 | Migration fail-closed | `TestMigrationFailClosed` | `internal/ctxdb/edgecases_test.go` | covered | ctxdb |
| GAP-COST-01 | Pricing table not expired at release | `TestPricingTableNotExpired` (`internal/provider/registry/prices_test.go`) | in-flight (PR #188) | provider |
| GAP-COST-02 | No context probe | — (`TestNoContextProbe` nowhere; the analyzer does not exist) | missing | provider |
| GAP-COST-04 | Prefix stable across fallback | — (`TestPrefixStableAcrossFallback` nowhere) | missing | assemble/loop |
| GAP-PROV-01 | Cache key includes tier/system | `TestFixedOverheadGolden`, `TestAllModulesFixtureShape` compare the rendered prompt; the F2 part (table via migrations) is `TestMigrate*` | covered | assemble/ctxdb |
| GAP-PROV-02 | Per-tier budget asserted | — (`TestContextTierBudget` nowhere) | missing | assemble |
| GAP-INF-01 | CJK recall order | `TestRecallCJKOrder`, `BenchmarkRecallCJK` | `internal/ctxdb/acceptance_test.go`, `internal/ctxdb/recall_perf_test.go` | covered | ctxdb |
| GAP-INF-02 | FTS index auto-upgrade | `TestFTSAutoUpgrade`, `BenchmarkRecallToolOutput`, `TestRecallToolOutputBlob`, `TestEncryptedRecallToolOutputBlob`, `TestToolOutputSpillThroughBinary` (biner asli: spill terenkripsi + baris `blob_fts`) | `internal/ctxdb/acceptance_test.go`, `internal/ctxdb/recall_perf_test.go`, `internal/e2e/spill_test.go` | covered | ctxdb |
| GAP-INF-06 | Rerun deterministic after restart | — (no test; separate from EC-CTX-021's pure rerun) | missing | loop |
| GAP-TOOL-10 | Native arch select + skill filter | `TestToolSelection` exists; no `TestNativeArchSelect`; no `requires: gpu=nvidia` filter test | missing | tools/skills |
| GAP-SEC-26 | No dangling skill refs in builtin | `TestLintCorpus` lint only; no CI rule over `builtin/` (0 refs) | surface | skills/CI |
| GAP-TEST-01 | Test-only tree analysis | `TestTreeAnalysis`, `TestLanguageDetection` on `testdata` | covered | codetools |

## 3. F2 parity rows (FEATURE-PARITY.md, fase = F2)

The parity table marks these rows "planned". "surface" means the
capability exists and is tested but the row's CLI/slash surface is not
tested (and in most cases not built); it must not be counted as covered
by the acceptance test until the surface exists.

### CLI

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| CLI-038 | `hermes backup` | in-flight | PR #197: `internal/backup/create_test.go`, `restore_test.go` |
| CLI-039 | `hermes checkpoints` | missing | journal snapshots back undo only; no checkpoints command or test |
| CLI-040 | `hermes import` | in-flight | PR #197: `internal/backup/restore_test.go` |
| CLI-046 | `hermes skills` | surface | skills install/retrieve tests (`internal/skills/install_test.go`); no CLI test |
| CLI-047 | `hermes bundles` | missing | no bundle type or test |
| CLI-057 | `hermes sessions` | surface | `TestHistoryForkAware`, ctxdb history/fork; CLI list/rename/export/prune untested |

### Slash commands

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| SLASH-002 | `/new` | surface | fresh session = `TestForkAndRewind`, ctxdb Create |
| SLASH-006 | `/history` | surface | ctxdb history tests |
| SLASH-007 | `/save` | missing | no conversation export |
| SLASH-008 | `/retry` | missing | provider retries exist; last-message retry is not tested |
| SLASH-010 | `/undo` | surface | undo suite (`internal/journal`) |
| SLASH-011 | `/title` | missing | no session titles |
| SLASH-013 | `/branch` | surface | `TestForkAndRewind` (PR #227 adds the CLI) |
| SLASH-015 | `/compress` | surface | `internal/compaction` tests |
| SLASH-016 | `/rollback` | surface | journal undo tests |
| SLASH-017 | `/snapshot` | surface | PR #197 backup/restore |
| SLASH-044 | `/resume` | surface | fork/history tests |
| SLASH-045 | `/sessions` | surface | ctxdb history tests |
| SLASH-066 | `/autoprune` | missing | session purge exists; no auto-prune |
| SLASH-068 | `/toolsets` | missing | no toolsets |
| SLASH-069 | `/skills` | surface | `internal/skills` tests |
| SLASH-070 | `/memory` | surface | `TestQuarantine*`/`TestApprove*` |
| SLASH-071 | `/bundles` | missing | no bundles |
| SLASH-083 | `/reload-skills` | missing | no rescan-on-command test |

### Tools

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| TOOL-070 | `memory` tool | missing | no tool named `memory`; the exposed tools are `recall`/`propose_memory` |
| TOOL-073 | `session_search` tool | missing | `session_search` appears only as a tool-call parse fixture |
| TOOL-074 | `skill_manage` | missing | identifier absent |
| TOOL-075 | `skill_view` | missing | identifier absent (also the EC-CTX-006 blocker) |
| TOOL-076 | `skills_list` | missing | identifier absent |

### Toolsets

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| TSET-021 | toolset `memory` | missing | no toolset composition |
| TSET-023 | toolset `session_search` | missing | no toolset composition |
| TSET-025 | toolset `skills` | missing | no toolset composition |
| TSET-035 | toolset `context_engine` | missing | no toolset composition |
| TSET-038 | toolset `debugging` | missing | no toolset composition |
| TSET-039 | toolset `safe` | missing | no toolset composition |
| TSET-040 | toolset `coding` | missing | no toolset composition |
| TSET-041 | toolset `hermes-acp` | missing | no toolset composition |
| TSET-043 | toolset `hermes-cli` | missing | no toolset composition |

### Memory

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| MEM-001 | built-in memory | covered | `internal/memory` tests; memstore |
| MEM-002 | user profile | covered | memory scope tests |
| MEM-003 | memory nudge | missing | no nudge |
| MEM-004 | memory write approval | covered | `TestQuarantineCannotBeRetrieved`, `TestTransitions`, `TestUserWriteIsApproved` (`internal/memory`) |
| MEM-011 | session store SQLite | covered | `internal/ctxdb` (`TestStore*`) |
| MEM-012 | session search FTS | covered | `TestSearch*` in ctxdb |
| MEM-013 | recap/resume | surface | history/fork tests; no recap |
| MEM-014 | session export | missing | no export |
| MEM-015 | session prune/archive | missing | purge exists; no prune/archive/VACUUM test |
| MEM-016 | lineage & titles | surface | `TestHistoryForkAware` covers lineage; titles missing |
| MEM-018 | context compression | covered | compaction stage 2 tests |
| MEM-019 | manual compression | surface | compaction tests; no slash surface |
| MEM-020 | micro-compaction | covered | compaction stage 1 (prune) tests |
| MEM-021 | proactive prune | covered | tool-result stub tests |
| MEM-022 | native compaction provider | missing | no provider-native compaction |
| MEM-023 | context engine plugin | missing | see PLUG-014 |
| MEM-024 | @-ref | missing | no reference expansion |
| MEM-025 | context pin | missing | memory pins are storage, not prompt pins |
| MEM-026 | context files | covered | `TestContextTruncationOrder` and `TestContextEmptyAndMultipleFiles` exercise `AGENTS.md` in `internal/assemble` |
| MEM-028 | rewind | covered | `TestForkAndRewind` |
| MEM-029 | branch/fork | covered | `TestForkAndRewind`, `TestForkContextTierCopied` |
| MEM-030 | retry | missing | last-message retry not tested |
| MEM-031 | filesystem checkpoints | covered | journal undo suite |
| MEM-032 | state snapshot | in-flight | PR #197 backup/restore of config+state |
| MEM-033 | memory trim | missing | no RSS trim |
| MEM-034 | prompt caching | missing | no cache_control use |

### Skills

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| SKILL-001 | SKILL.md system | covered | `internal/skills` retrieval tests |
| SKILL-002 | skills_list / skill_view / skill_manage | missing | tools absent (see TOOL-074/075/076) |
| SKILL-004 | preprocessing | missing | no template/inline-shell tests |
| SKILL-007 | bundles | missing | no bundles |
| SKILL-008 | auto-load | covered | `TestRetrieveLimit`, retrieval tiers in `TestContextTierFrozen` |
| SKILL-009 | project discovery | missing | no trusted-project scan |
| SKILL-010 | external skill dirs | missing | no external dirs |
| SKILL-016 | bundled skills sync | covered | builtin install tests |
| SKILL-018 | guard/lint | covered | `TestLintCorpus`, `TestLintFailsClosed`, `TestQuarantine*` |
| SKILL-019 | ledger & provenance | surface | skillstore tracks sha; no provenance ledger test |
| SKILL-022 | write approval | covered | `TestQuarantine*` |
| SKILL-023 | skills index | missing | no index build/delta |

### Plugins and operations

| ID | fitur | status | evidence |
| --- | --- | --- | --- |
| PLUG-014 | context_engine plugin | missing | no engine slot |
| PLUG-036 | tool search / deferred tools | covered | `tool_search` tests, PR #174 wiring |
| OPS-006 | backup / import | in-flight | PR #197 |
| CORE-004 | toolsets & per-platform config | missing | no toolsets |
| CORE-005 | custom toolsets | missing | no toolsets |

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
7. **ctxdb/exec/Windows F1 (done, PR #254/#255):** EC-WIN-002/005/011 in
   this PR; EC-WIN-001/009/014/016/017 in PR #254; the F1 FIO/TCALL/TERM
   rows in PR #255.
8. **skills/CI:** builtin dangling-ref rule (GAP-SEC-26); native-arch
   skill filter (GAP-TOOL-10).
9. **backup:** land PR #241's stack; add the plaintext-canary test to
   GAP-SEC-01.
10. **docs:** the 81 F2 parity rows that are genuinely unimplemented
    (42 rows, listed above) need either an implementation PR or an
    explicit ADR-064 correction moving them to the phase that owns them.
    Silent "planned" rows are the hole this ledger closes.

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
  capability; the CLI/slash surface is called out separately.
