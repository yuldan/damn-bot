# AUDIT-F0 — status penutupan

Pelacak penutupan temuan [`AUDIT-F0.md`](AUDIT-F0.md) (putaran 1) dan [`AUDIT-F0-REVERIFY.md`](AUDIT-F0-REVERIFY.md) (putaran 2; kedua file audit tidak diubah). Gate ROADMAP sebelum F1: semua BLOCKER/MAJOR ditutup lewat ADR. Kebijakan keputusan: opsi paling konservatif/aman, dalam budget SPEC §3.2.4/P1/P13/P21, tanpa dependency baru; model shell ADR-079 = keputusan direktur proyek.

Status:
- **selesai**: ADR Accepted, teks diterapkan, dan kode/test F0 ada dan lulus.
- **ditutup (ADR)**: ADR Accepted dan teks diterapkan ke dokumen sasaran; test bernama = acceptance fase yang disebut (kode belum ada di F0).
- **backlog Fx**: MINOR dengan pemilik dan fase di ADR-078/ADR-086; wajib tertutup sebelum acceptance fase itu.

Commit ADR: putaran 1 `12de82e`, `fc1a4a9`, `a88680e`; putaran 2 `139958d` (ADR-079..086), `31e9cc0` (ADR-079 butir 9–11), `8a212e6` (ADR-087 + amandemen ADR-079).

## BLOCKER (putaran 1)

| AUD | ADR | Commit penerapan | Test (fase) | Status |
|---|---|---|---|---|
| AUD-001 | ADR-069 → **ADR-079** | putaran 1: `df451c9`, `5547c27`, `d61b924`, `20a67da`, `878e0f0`; putaran 2: `1a67894` SPEC, `123ccda` EXEC/CODE-TOOLS, `b785b95` TRUST-MODEL, `176dd54`/`eabecaf` CROSS-CUTTING, `ac5f3f9` ROADMAP | `TestShellApprovalModel` (36 baris ADR-079), `TestShellNoClassifierAuto`, `TestShellAllowExactMatch`, `TestCodeToolsSymlinkOutsideRoot` (F1) | ditutup (ADR) |
| AUD-002 | ADR-063 | `749b814`, `bb83757`, `b1829b5`, `09660fe`, `fc1a4a9`, `3d212ac` | `TestParityPhaseColumn` (F0, lulus); test F1–F6 di ADR-063, kini di acceptance fasenya (ADR-083, `ac5f3f9`) | ditutup (ADR) |
| AUD-003 | ADR-064 | `09660fe`, `12de82e`, `020a08a`; `7e3ee69` (jumlah per fase) | `TestParityPhaseColumn` (837/837 + jumlah per fase) | selesai |
| AUD-004 | ADR-065 + **ADR-083** | `020a08a`, `b1829b5`; `ade0e2f` artefak `not_measured`, `b925653` F0-REPORT #4, `8e6bf9b` SCORECARD, `ac5f3f9` ROADMAP | `TestNoLLMBaselineCoverage` (F0, lulus); baseline hermes per fase di acceptance F1–F4, F6, F9 | selesai (re-scope berfase) |
| AUD-005 | ADR-066 | `16e4483`, `020a08a` | `TestFixedOverheadGolden` + `TestFixedOverhead/f1-core` (F1) | ditutup (ADR) |
| AUD-006 | ADR-068 | `415c1e2`, `020a08a` | subset F1 per GAP; cek penuh di fase pemilik | ditutup (ADR) |

## MAJOR (putaran 1)

Verdict putaran 2: closed kecuali yang ditandai *partial → …* (ditutup putaran 2).

| AUD | ADR | Commit penerapan | Test (fase) | Status |
|---|---|---|---|---|
| AUD-007 | ADR-070 + ADR-079 butir 6 | `df451c9`, `878e0f0`, `5547c27`; `1a67894`, `123ccda`, `176dd54` | `TestTaintForcesApproval`, `TestShellOutputUntrustedWorkspace` (F1) | partial → ditutup (ADR) |
| AUD-008 | ADR-069 → ADR-079 | lihat AUD-001 | `TestShellNoClassifierAuto`, `TestShellAllowCwdTracked` (F1) | partial → ditutup (ADR) |
| AUD-009 | ADR-069 (butir 5 tetap) | `5547c27`, `df451c9` | `TestShellApprovalModel` baris 21–29 (F1) | ditutup (ADR) |
| AUD-010 | ADR-069 → ADR-079 butir 3–5 | lihat AUD-001 | `TestShellAllowExactMatch`, `TestVerifyNeedsShellAllow`, `TestWorkspaceTrustGate` (F1) | partial → ditutup (ADR) |
| AUD-011 | ADR-071 + ADR-081 butir 1 | `6abe2cb`, `df451c9`, `ed49f0f`, `a88680e`; `39494af`, `c9634eb` | `TestEnvAllowlist` (F1) | ditutup (ADR) |
| AUD-012 | ADR-069 butir 8 + ADR-079 butir 7 | `d61b924`, `df451c9`; `123ccda` | `TestReadOutsideWorkspaceAsks`, `TestCodeToolsSymlinkOutsideRoot` (F1) | ditutup (ADR) |
| AUD-013 | ADR-071 | `df451c9`, `cde994b`, `878e0f0` | `TestRedaction` (F1) | ditutup (ADR) |
| AUD-014 | ADR-071 + ADR-081 butir 2 | `df451c9`, `6abe2cb`; `39494af`, `c9634eb`, `176dd54` | `TestStreamRedactSplit`, `TestFirstTokenLatency/redact` ≤ 1 ms p95 (F1) | partial → ditutup (ADR) |
| AUD-015 | ADR-071 | `cde994b`, `a88680e` | CT-20 ≥ 330 (F1) | ditutup (ADR) |
| AUD-016 | ADR-071 | `df451c9` | `TestLogNoMessageContent` termasuk DEBUG (F1) | ditutup (ADR) |
| AUD-017 | ADR-069 → ADR-079 butir 8 + ADR-081 butir 3 | `20a67da`; `b785b95`, `1a67894` (GAP-SEC-28) | definisi vulnerability TRUST-MODEL §1 | partial → ditutup (ADR) |
| AUD-018 | ADR-072 + ADR-082 butir 1 | `922bfae`; `27c01d6` | `TestContentFilterStop` (+ `tool_calls`) (F1) | partial → ditutup (ADR) |
| AUD-019 | ADR-072 | `922bfae` | `TestErrorCodesClosed` (F1) | ditutup (ADR) |
| AUD-020 | ADR-072 + ADR-082 butir 2 | `922bfae`, `51e49b2`; `27c01d6`, `fc8b543`, `b0ebceb` | `TestToolResultStatusClosed`, `TestCancelConfirmed`, `TestACPChildStdinIsolation` (F1/F3) | partial → ditutup (ADR) |
| AUD-021 | ADR-072 | `922bfae`, `51e49b2`, `020a08a` | background → `invalid_args` (F1); `process_*` (F5) | ditutup (ADR) |
| AUD-022 | ADR-067 | `16e4483` | `TestStableTierGolden`, `TestPromptNoUnavailableTools` (F1, acceptance via ADR-082 butir 4) | ditutup (ADR) |
| AUD-023 | ADR-067 + ADR-082 butir 3 | `16e4483`; `27c01d6`, `fc8b543`, `40947c0` | `TestVolatileTierCaps`, `TestRuntimeSuffixCaps`, `TestPrefixStableWithinTurn` (F1) | partial → ditutup (ADR) |
| AUD-024 | ADR-067 | `16e4483`, `fc1a4a9` | `TestTokenEstimatorCalibration` (F1/F2) | ditutup (ADR) |
| AUD-025 | ADR-077 | `020a08a`, `11626de`, `96e03a1`; `ac5f3f9` | daftar acceptance F1 | ditutup (ADR) |
| AUD-026 | ADR-077 + ADR-080 | `020a08a`, `df61cd4`; `ae17f8c`, `ac5f3f9` | EC per ID per fase | ditutup (ADR) |
| AUD-027 | ADR-077 → ADR-080 | `ae17f8c`, `ac5f3f9` | `TestEdgeCaseCatalogSync`, `TestCLIDocsMatchHelp` (fase < `PHASE`), `make phase-acceptance` | partial → ditutup (ADR) |
| AUD-028 | ADR-077 | `df61cd4`, `bb83757`, `11626de` | ×1,5 hanya waktu absolut | ditutup (ADR) |
| AUD-029 | ADR-077 | `df61cd4`, `020a08a`, `b1829b5` | `BenchmarkSpeedParity/<ID>` fase F1 (F1) | ditutup (ADR) |
| AUD-030 | ADR-077 → ADR-082 butir 6 | `bb83757`; `fc8b543`, `27c01d6` | CT-10: baca ≥ 10×, `patch` ≥ 2× setara durabilitas (F1) | ditutup (ADR) |
| AUD-031 | ADR-073 | `2e247ec`, `020a08a` | F1 #0 | ditutup (ADR) |
| AUD-032 | ADR-073 | `2e247ec`, `020a08a` | F1 #0 | ditutup (ADR) |
| AUD-033 | ADR-073 + ADR-085 butir 4 | `fcc0af6`, `8288d53`; `f19076c` | `make vet-cross`, `TestPlatformNAList` | selesai |
| AUD-034 | ADR-073 → ADR-085 butir 1 | `8288d53`; `47f9599` meta.yml | workflow `meta` di setiap PR (status selalu terlapor) | partial → selesai (pohon minimal F1 #0) |
| AUD-035 | ADR-073 | `8288d53`, `a88680e`, `a09df65`, `fc1a4a9` | meta-test `timing`; job `perf` (F1 #0) | selesai (sisa F1 #0) |
| AUD-036 | ADR-074 | `02a37c7`, `fc1a4a9` | `make loc` | ditutup (ADR) |
| AUD-037 | ADR-075 → ADR-085 butir 2 | `4cf5b9e`; `4992cdb` | `TestLedgerCapAcrossRuns`, `TestReservationRejectsOverCap`, `TestPriceRequired`, `TestUsageMissing`, `TestUnknownPathRejected`, `TestLedgerRequiredWithKey`, `TestLedgerSharedAcrossInstances` | partial → selesai |
| AUD-038 | ADR-075 | `020a08a`, `b1829b5`; `ac5f3f9` | test oracle (F1, prasyarat smoke, di acceptance F1) | ditutup (ADR), kode F1 |
| AUD-039 | ADR-075 → ADR-085 butir 3 | `ac5f3f9`, `8e6bf9b` | `TestAgentIsolation`, `TestRecordingsNoKey` (F1, prasyarat smoke) | partial → ditutup (ADR), kode F1 |
| AUD-040 | ADR-076 | `675438a`, `11626de` | `TestExecuteCodeRPCNetworkDeny` (F5) | ditutup (ADR) |
| AUD-041 | ADR-076 + ADR-084 butir 2 | `675438a`, `20a67da`; `f8c9733`, `b785b95` | `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope` (F4, GAP-GW-02) | partial → ditutup (ADR) |
| AUD-042 | ADR-076 + ADR-084 butir 3 | `675438a`, `20a67da`; `f8c9733` | `TestMCPServeCannotSelfApprove` (F5, GAP-SEC-21) | partial → ditutup (ADR) |

## Temuan baru putaran 2 — BLOCKER/MAJOR

| NEW | Sev | ADR | Commit | Test (fase) | Status |
|---|---|---|---|---|---|
| NEW-01 | BLOCKER | ADR-079 | `1a67894`, `123ccda`, `b785b95`, `176dd54` | `TestShellNoClassifierAuto`, `TestShellApprovalModel` baris 4–8 (F1) | ditutup (ADR) |
| NEW-02 | BLOCKER | ADR-080 | `ae17f8c`, `ac5f3f9` | gate fase < `PHASE`; `make phase-acceptance` (F1 #0) | ditutup (ADR) |
| NEW-03 | MAJOR | ADR-079 | sama dengan NEW-01 | baris 30 (sisa risiko yang di-approve) | ditutup (ADR) |
| NEW-04 | MAJOR | ADR-079 butir 3 | sama | `TestShellAllowCwdTracked` (F1) | ditutup (ADR) |
| NEW-05 | MAJOR | ADR-081 butir 1 | `39494af`, `c9634eb`, `b785b95` | `TestEnvAllowlist` + kasus secret ref (F1) | ditutup (ADR) |
| NEW-06 | MAJOR | ADR-079 butir 5 | `1a67894`, `123ccda` | `TestVerifyNeedsShellAllow` (F1) | ditutup (ADR) |
| NEW-07 | MAJOR | ADR-079 butir 8 | `b785b95` | definisi vulnerability | ditutup (ADR) |
| NEW-08 | MAJOR | ADR-081 butir 2 | `39494af`, `176dd54`, `c9634eb` | `TestFirstTokenLatency/redact` (F1) | ditutup (ADR) |
| NEW-09 | MAJOR | ADR-079 butir 6 | `1a67894`, `176dd54` | `TestShellOutputUntrustedWorkspace` (F1) | ditutup (ADR) |
| NEW-10 | MAJOR | ADR-082 butir 3 | `27c01d6`, `fc8b543`, `40947c0` | `TestRuntimeSuffixCaps` (F1) | ditutup (ADR) |
| NEW-11 | MAJOR | ADR-082 butir 4 | `ac5f3f9` | P15 subset di acceptance F1 | ditutup (ADR) |
| NEW-12 | MAJOR | ADR-083 | `ac5f3f9` | bullet baseline per fase | ditutup (ADR) |
| NEW-13 | MAJOR | ADR-085 butir 1 | `47f9599`, `696f24b` | workflow `meta` | selesai |
| NEW-14 | MAJOR | ADR-085 butir 2 | `4992cdb` | `TestLedgerRequiredWithKey`, `TestLedgerSharedAcrossInstances` | selesai |
| NEW-15 | MAJOR | ADR-085 butir 4 | `f19076c` | `TestPlatformNAList`; vet/test-compile darwin+windows | selesai (eksekusi di runner macOS/Windows F1 #0) |
| NEW-16 | MAJOR | ADR-084 butir 1 | `f8c9733`, `b785b95`, `ac5f3f9` | approver palsu ber-preview (F1), ACP (F3) | ditutup (ADR) |
| NEW-17 | MAJOR | ADR-082 butir 6 | `27c01d6`, `fc8b543`, `8e6bf9b` | CT-10 `patch` setara durabilitas (F1) | ditutup (ADR) |
| NEW-18 | MAJOR | ADR-082 butir 5 | `27c01d6`, `fc8b543` | `TestShellSchemaVariant` (F1) | ditutup (ADR) |
| NEW-19 | MAJOR | ADR-080 butir 4 | `ae17f8c`, `ac5f3f9` | EC-STATE/EC-WIN per ID | ditutup (ADR) |
| NEW-20 | MAJOR | ADR-081 butir 4 | `696f24b` | `make loc` (`exec` 3.500 + `exec/classify` 1.500) | selesai |

**Flake CI `TestMultiProcess`** (run 39117760522): ADR-087, commit `70b386d`/`e344612` (diagnostik) + `8a212e6` (fairness writer, budget `ctxdb` 3.100) — selesai.

**Platform-n/a ctxdb (ADR-085 butir 4, `TestPlatformNAList`)**: Windows = S037, S063 (tanpa SIGSTOP/SIGCONT), S062, S074, S085 (tanpa process group POSIX; grup tool Windows = Job Object, F1). OS lain (bukan target CI) = S023, S044, S037, S063, S062, S074, S085. Linux dan darwin: tidak ada.

## MINOR

| ID | ADR | Fase / status |
|---|---|---|
| AUD-043, 044, 045, 046, 058, 068 | ADR-078 (068: keputusan ADR-086) | backlog F1 (sebelum paket terkait) |
| AUD-047, 075, 076, 077, 078 | ADR-078 (047: keputusan ADR-086) | backlog F1 (sebelum smoke F1) |
| AUD-048, 055, 061, 062, 065, 066 | ADR-078 (048: keputusan ADR-086) | backlog F1 |
| AUD-049 | ADR-078 | (a) closed (EXEC §A.8); (b), (c) backlog F1 |
| AUD-056, 057, 059, 067, 069, 070, 071, 072, 073, 074 | ADR-078 | backlog F1 (Deliverable #0) |
| AUD-083 | ADR-078 + ADR-086 NEW-51 | `.gitignore` ada (`4992cdb`); sisa run TS-11 terpisah: backlog F1 #0 |
| AUD-079 | ADR-078 | backlog F1 (sebelum acceptance codetools) |
| AUD-051, 060, 063 | ADR-078 | backlog F4 |
| AUD-053 | ADR-086 | closed (US$5,00, `f3302ac`) |
| AUD-064 | ADR-086 | closed (definisi P21, `f3302ac`) |
| AUD-050 | ADR-078 | backlog F5 |
| AUD-052, 054 | ADR-078 | backlog F8 |
| AUD-080 | ADR-077 | closed |
| AUD-081, AUD-082 | — | closed (SCORECARD §2.5 / ROADMAP F0; NEW-47) |
| NEW-21, 29, 40, 45, 50 | ADR-086 | backlog F1 |
| NEW-31, 41, 52 | ADR-086 | backlog F1 (sebelum `codetools`/`assemble`) |
| NEW-32, 33, 34, 35, 36, 38 | ADR-086 | backlog F1 (Deliverable #0) |
| NEW-44 | ADR-086 | backlog F2 |
| NEW-22, 23, 24, 25, 26, 27 | ADR-079 | closed |
| NEW-28 | ADR-079/081 | closed |
| NEW-30 | ADR-080 | closed |
| NEW-37 | ADR-085 | closed (flock, cached = 0); test biaya abort: backlog F1 sebelum smoke |
| NEW-39, 42, 46 | ADR-083 | closed |
| NEW-43 | ADR-086 | selesai (`7e3ee69`) |
| NEW-47, 48, 49 | ADR-086 | closed (pelacak ini; keputusan 047/048/053/064/068; penanda amandemen `139958d`) |
| NEW-51 | ADR-086 | selesai (`.gitignore`; `sisasisa` milik user tidak disentuh) |

Ringkasan: 6/6 BLOCKER asli, 36/36 MAJOR asli, 2/2 BLOCKER baru, dan 18/18 MAJOR baru punya ADR Accepted dengan teks terapan. Yang selesai beserta kode/test F0: AUD-003, 004, 033, 034, 035, 037; NEW-13, 14, 15, 20, 43, 51; flake `TestMultiProcess`. Sisa MINOR = backlog berfase ADR-078/ADR-086.
