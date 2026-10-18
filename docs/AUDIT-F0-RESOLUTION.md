# AUDIT-F0 — status penutupan

Pelacak penutupan temuan [`AUDIT-F0.md`](AUDIT-F0.md) (file audit tidak diubah). Gate ROADMAP sebelum F1: semua BLOCKER/MAJOR ditutup lewat ADR. Keputusan didelegasikan pemilik proyek dengan kebijakan: opsi paling konservatif/aman, dalam budget SPEC §3.2.4/P1/P13/P21, tanpa dependency baru.

Status:
- **selesai**: ADR Accepted, teks diterapkan, dan kode/test F0 sudah ada dan lulus.
- **ditutup (ADR)**: ADR Accepted dan teksnya diterapkan ke dokumen sasaran. Test bernama menjadi acceptance fase yang disebut (kode belum ada di F0).
- **backlog Fx**: MINOR dengan pemilik dan fase di ADR-078. Wajib ditutup sebelum acceptance fase itu.

Commit ADR: `12de82e` (ADR-063..078), `fc1a4a9` (penanda ADR lama), `a88680e` (koreksi ADR-071).

## BLOCKER

| AUD | ADR | Commit penerapan | Test (fase) | Status |
|---|---|---|---|---|
| AUD-001 | ADR-069 | `df451c9` SPEC §4.3/§12/§14; `5547c27` EXEC §A.8; `d61b924` CODE-TOOLS §8; `20a67da` TRUST-MODEL; `878e0f0` CROSS-CUTTING | `TestShellDenyPaths`, `TestClassifyCorpus` (tabel 42 kasus ADR-069), `TestReadOutsideWorkspaceAsks` (F1) | ditutup (ADR) |
| AUD-002 | ADR-063 | `749b814` SPEC; `bb83757` CODE-TOOLS; `b1829b5` SCORECARD; `09660fe` FEATURE-PARITY; `fc1a4a9` ADR-036; `3d212ac` F0-REPORT (38/38) | `TestParityPhaseColumn` (F0, lulus); `TestCodeToolsIDsComplete` (F1); `TestModuleLifecycle/needs_attention` (F2); `TestCLITopLevelCount` (F3); `TestBudgetGuardGatewayUser` (F4); `TestBudgetGuardTools` (F6) | ditutup (ADR) |
| AUD-003 | ADR-064 | `09660fe` FEATURE-PARITY + `internal/archtest/parity_phase_test.go`; `12de82e` (EXEC-009 → F5); `020a08a` ROADMAP | `TestParityPhaseColumn` (837/837) | selesai |
| AUD-004 | ADR-065 | `020a08a` ROADMAP F0; `b1829b5` SCORECARD intro | per dimensi di tabel ADR-065 (F1–F4, F6, F9) | ditutup (ADR, re-scope) |
| AUD-005 | ADR-066 | `16e4483` SPEC P1; `020a08a` ROADMAP F1/F2 | `TestFixedOverheadGolden` (`internal/assemble`) + `TestFixedOverhead/f1-core` (`bench`) (F1); `all-modules` (F2) | ditutup (ADR) |
| AUD-006 | ADR-068 | `415c1e2` SPEC §22.1; `020a08a` ROADMAP adopsi GAP | cek subset F1 per GAP di ADR-068; cek penuh di fase pemilik | ditutup (ADR) |

## MAJOR

| AUD | ADR | Commit penerapan | Test (fase) | Status |
|---|---|---|---|---|
| AUD-007 | ADR-070 | `df451c9` SPEC §4.3 (6a)/§14.1/§14.5; `878e0f0` CROSS-CUTTING; `5547c27` EXEC; `b1829b5` SCORECARD E10 | `TestTaintForcesApproval` (iterasi N+1, reset turn), `TestShellNetworkOutputUntrusted` (F1) | ditutup (ADR) |
| AUD-008 | ADR-069 | `5547c27` EXEC §A.8; `df451c9` SPEC §14.1 | `TestClassifyCorpus`, `TestSessionTaintPersistent` (F1) | ditutup (ADR) |
| AUD-009 | ADR-069 | `5547c27`; `df451c9` | `TestShellIrreversibleAlwaysAsk`, `TestSudoAlwaysAsk` (F1) | ditutup (ADR) |
| AUD-010 | ADR-069 | `5547c27`; `df451c9` SPEC §12/§14.1; `20a67da` TRUST-MODEL §2 | `TestShellAllowMatching`, `TestWorkspaceTrustGate` (F1) | ditutup (ADR) |
| AUD-011 | ADR-071 | `6abe2cb` EXEC §A.6/§A.8/EXC-15; `df451c9` SPEC §12/§14.3; `ed49f0f` MITIGATION; `a88680e` (`exec.env` secret ref) | `TestEnvAllowlist` (F1) | ditutup (ADR) |
| AUD-012 | ADR-069 | `d61b924` CODE-TOOLS §8; `df451c9` SPEC §4.3 | `TestReadOutsideWorkspaceAsks` (F1) | ditutup (ADR) |
| AUD-013 | ADR-071 | `df451c9` SPEC §14.3; `cde994b` CODE-TOOLS; `878e0f0` CROSS-CUTTING | `TestRedaction` (korpus gitleaks ter-pin + PEM penuh) (F1) | ditutup (ADR) |
| AUD-014 | ADR-071 | `df451c9`; `6abe2cb` EXEC §A.3 | `TestStreamRedactSplit`, `TestFirstTokenLatency` (F1) | ditutup (ADR) |
| AUD-015 | ADR-071 | `cde994b` CODE-TOOLS §5/CT-20; `a88680e` (CT-20 ≥ 330 di semua dokumen) | CT-20 `TestEditAccuracySuite` +5 kasus (F1) | ditutup (ADR) |
| AUD-016 | ADR-071 | `df451c9` SPEC §15, P29 | `TestLogNoMessageContent` (DEBUG) (F1) | ditutup (ADR) |
| AUD-017 | ADR-069 | `20a67da` TRUST-MODEL §1/§3; `df451c9` SPEC P29/§14.2 | metrik EXC-13 = 0 (F1) | ditutup (ADR) |
| AUD-018 | ADR-072 | `922bfae` SPEC §3.5/§4.2/§13.3 | `TestContentFilterStop` (F1) | ditutup (ADR) |
| AUD-019 | ADR-072 | `922bfae` SPEC §4.3 | `TestErrorCodesClosed` (F1) | ditutup (ADR) |
| AUD-020 | ADR-072 | `922bfae` SPEC P24/§4.2/§12; `51e49b2` EXEC §A.3/§A.4/§A.10 | `TestCancelConfirmed`, `TestCancelConfirmBeforeRedispatch` (F1); `TestACPChildStdinIsolation` (F3) | ditutup (ADR) |
| AUD-021 | ADR-072 | `922bfae` SPEC §5.1; `51e49b2` EXEC §A.2/§A.5/§A.6; `020a08a` ROADMAP | background → `invalid_args` (F1); `process_*` + test P24/EXC-09/11 terkait (F5) | ditutup (ADR) |
| AUD-022 | ADR-067 | `16e4483` SPEC §6/P15 | `TestStableTierGolden` (3 varian), `TestPromptNoUnavailableTools` (F1) | ditutup (ADR) |
| AUD-023 | ADR-067 | `16e4483` SPEC P1/§4.5/§6 | `TestVolatileTierCaps`, `TestPrefixStableWithinTurn` (F1) | ditutup (ADR) |
| AUD-024 | ADR-067 | `16e4483` SPEC §4.5/§22.1; `fc1a4a9` ADR-058 | `TestTokenEstimatorCalibration` teks (F1), gambar (F2) | ditutup (ADR) |
| AUD-025 | ADR-077 | `020a08a` ROADMAP F1; `11626de` EXEC §A.10; `96e03a1` SCORECARD | daftar test acceptance F1 ADR-077 butir 1 | ditutup (ADR) |
| AUD-026 | ADR-077 | `020a08a` ROADMAP regression suite; `df61cd4` SPEC §16 | `TestEdgeCases/EC-*` per fase (tabel ADR-077) | ditutup (ADR) |
| AUD-027 | ADR-077 | `020a08a`; `df61cd4` | `TestEdgeCaseCatalogSync`, `TestCLIDocsMatchHelp` + file `PHASE` (F1 #0) | ditutup (ADR) |
| AUD-028 | ADR-077 | `df61cd4` SPEC §16/§17.1; `bb83757` CODE-TOOLS §10; `11626de` EXEC §A.9 | ×1,5 hanya waktu absolut (F1) | ditutup (ADR) |
| AUD-029 | ADR-077 | `df61cd4` SPEC §17.3; `020a08a` ROADMAP F0/F1; `b1829b5` SCORECARD §3 | `BenchmarkSpeedParity/<ID>` baris `fase=F1`, ≥ 20 run (F1) | ditutup (ADR) |
| AUD-030 | ADR-077 | `bb83757` CODE-TOOLS CT-10; `b1829b5` SCORECARD §1.2 | CT-10 ≥ 10× (70 operasi) (F1) | ditutup (ADR) |
| AUD-031 | ADR-073 | `2e247ec` ENGINEERING §5; `020a08a` ROADMAP F1 #0 | job matrix/build-matrix/fuzz/coverage/deadcode/pr-meta/CODEOWNERS + kasus meta-test (F1 #0) | ditutup (ADR) |
| AUD-032 | ADR-073 | `2e247ec`; `020a08a` | `TestImportGraph`, `nohttpclient`, `TestFileLength`, `TestGlobalAllowlist`, `TestGoleakMain`, `TestBudgetTableSum` (F1 #0) | ditutup (ADR) |
| AUD-033 | ADR-073 | `fcc0af6` ctxdb test lintas OS; `8288d53` `make vet-cross` + job CI + gate meta-test | `make vet-cross`; meta-test `vet-cross` DETECTED | selesai |
| AUD-034 | ADR-073 | `8288d53` `.github/workflows/meta.yml` (paths + nightly) | meta-test dipicu path gate + nightly; pohon minimal (F1 #0) | selesai (sisa F1 #0) |
| AUD-035 | ADR-073 | `8288d53` `make test-timing` + `-bench=. -benchtime=1x`; `a88680e` SPEC §16; `a09df65` SELF-HEALING; `fc1a4a9` ADR-048 | meta-test `timing` DETECTED; job `perf` + `internal/testutil/timing` (F1 #0) | selesai (sisa F1 #0) |
| AUD-036 | ADR-074 | `02a37c7` ENGINEERING §2.2/§2.4 + `loc.budget`; `fc1a4a9` ADR-049 | `make loc` (baris `ctxdb/<consumer>store`) | ditutup (ADR) |
| AUD-037 | ADR-075 | `4cf5b9e` `bench/h2h` ledger + cap | `TestLedgerCapAcrossRuns`, `TestReservationRejectsOverCap`, `TestPriceRequired`, `TestUsageMissing`, `TestUnknownPathRejected`, `TestProxyBudgetCap` | selesai |
| AUD-038 | ADR-075 | `020a08a` ROADMAP F1; `b1829b5` SCORECARD §2.3 | `TestOracleDamnbotToolNames`, `TestUnknownRuleKindFails`, `TestToolCallsFromFinalResponse` (F1, prasyarat smoke) | ditutup (ADR), kode F1 |
| AUD-039 | ADR-075 | `020a08a`; `b1829b5` | isolasi container/uid + scan rekaman (F1, prasyarat smoke) | ditutup (ADR), kode F1 |
| AUD-040 | ADR-076 | `675438a` SPEC §9.2/§22.1; `11626de` EXEC §B.7 | `TestExecuteCodeRPCNetworkDeny` (F5) | ditutup (ADR) |
| AUD-041 | ADR-076 | `675438a` SPEC §9.7/§14.2; `20a67da` TRUST-MODEL | `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope` (F4) | ditutup (ADR) |
| AUD-042 | ADR-076 | `675438a` SPEC §9.6/§14.1; `20a67da` TRUST-MODEL | `TestMCPServeCannotSelfApprove` (F5) | ditutup (ADR) |

## MINOR (ADR-078)

| AUD | Fase pemilik | Status |
|---|---|---|
| AUD-043, 044, 045, 046, 058, 068 | F1 (sebelum paket terkait) | backlog F1 |
| AUD-047, 075, 076, 077, 078 | F1 (sebelum smoke F1) | backlog F1 |
| AUD-048, 049, 055, 061, 062, 065, 066, 081, 082 | F1 | backlog F1 |
| AUD-056, 057, 059, 064, 067, 069, 070, 071, 072, 073, 074, 083 | F1 (Deliverable #0) | backlog F1 |
| AUD-079 | F1 (sebelum acceptance codetools) | backlog F1 |
| AUD-051, 053, 060, 063 | F4 | backlog F4 |
| AUD-050 | F5 | backlog F5 |
| AUD-052, 054 | F8 | backlog F8 |
| AUD-080 | — | ditutup ADR-077 (`fc1a4a9` ADR-059) |

Ringkasan: 6/6 BLOCKER dan 36/36 MAJOR punya ADR Accepted dengan teks terapan. 6 temuan selesai beserta kode/test F0 (AUD-003, 033, 034, 035, 037; AUD-002 sebagian lewat `TestParityPhaseColumn`). 40 MINOR menjadi backlog berfase; AUD-080 sudah ditutup.
