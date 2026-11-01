# F5 acceptance — matriks final

Status `covered` hanya bila tes bernama **ada di main**. PR terbuka = `pending merge`
(tes/pemilik ada, tetapi belum di main). Tidak ada `covered` tanpa nama tes.

## Matriks

| Item | Status | Bukti (tes + PR + head) | Pemilik |
|---|---|---|---|
| Provider catalog replay 88 | covered | `TestProviderCatalog88`, `TestProvidersMDParity`; PR #588 `aa18f6af` (merged; 85 provider replay) | provider |
| Custom 12-field + real smoke | pending merge | PR #602 `17cb94b1` | provider |
| MCP client/server/OAuth/pin | pending merge | #574 `96fb9e82` merged; #581 `cf136d02` merged (manage CLI); #589 `518e713a` (serve) open | mcp-oauth-b |
| `TestMCPServeCannotSelfApprove` | pending merge | #589 `518e713a` | mcp-oauth-b |
| Exec backends docker/ssh | covered | #568/#570 merged; `TestBackendContract/*`, `TestSSHContract` | exec |
| Exec backends singularity/modal/daytona/vercel | pending merge | #571 `3b41e09c`; `TestBackendContract/{singularity,vercel_sandbox}`, `TestSandboxProviders`, e2e `TestExecBackendAcrossBackends` | exec |
| execute_code contract/approval | pending merge | #573 `db029513` | exec |
| execute_code tool + wiring | pending merge | #576 `de10898a` | exec |
| process registry + process_* + shell background | pending merge | #575 `13e7a3d4`, #577 `715882e4`, #578 `d63b21de`; `TestExecuteCode*`/process e2e #579 `d0daa647` | exec |
| codeintel repo map/Golden index | covered | #580 `bada3b16` merged; `TestRepoMap*`/`TestGoSymbolIndex` | codeintel |
| codeintel tree-sitter wazero | pending merge | #582 `435cd564` | codeintel |
| LSP + trust gate | pending merge | #583 `4e4faed1`; `TestLSPWorkspaceTrustGate` | codeintel |
| `repo_map`/`code_intel` tools + e2e | pending merge | #593 `b6ee739f` | codeintel |
| Credential pool (core/rotation/config/wiring) | pending merge | #597 `56fb0849`, #599 `8b614447`, #601 `454c5286`, #607 `85959107` | pool worker |
| EC-PROV-014 (401 refresh, 403/429) | pending merge | #609 `e86e5eac` (explicit credential sources + 401 refresh) | provider |
| EC-PROV-017/020/021/022/026 | pending merge | #597/#599/#601/#607 | pool worker |
| EC-PROV-003/023 | pending | tidak ada tes bernama; PROV-023 `n/a` (SPEC §4.1/§12 tidak mengatur bare-host `/v1`) | provider |
| EC-PROV-027 | pending merge | #592 `10ee4646`; `TestCacheMarkerCapability` | provider |
| EC-TCALL-004 | pending merge | #586 `d4696986`; `TestToolCallIDAliases` | provider |
| EC-TCALL-011 | pending merge | #596 `b9a96760` (shared chat→Responses converter) | provider |
| EC-FIO-003 | pending merge | #585 `d2ee8b96`; `TestContainerCwdValidated` | exec |
| EC-FIO-010 | pending merge | #583 `4e4faed1` (LSP UTF-16) | codeintel |
| EC-TERM-001/003 | pending merge | #575/#578 (background registry); warning sandbox belum ada tes bernama | exec |
| EC-SEC-004 | pending merge | #590 `07ae8e00`; `TestOAuthBearerHostPinned` | provider |
| EC-SEC-002/003 | pending | tidak ada tes bernama | provider |
| EC-WIN-007 | pending merge | #591 `64642d7a`; `TestDockerVolumeDriveLetter` (windows) | exec |
| EC-WIN-008/015 | pending merge | #595 `63873be7`, #598 `ed6c9e0f` | win-final |
| `TestExecuteCodeRPCNetworkDeny` / `TestExecuteCodeApproval` | pending merge | #573/#576/#579 | exec |
| E1 smoke 10 task | pending merge | #594 `979fd3fd` | h2h |
| e2e F5 approval/budget | pending merge | #603 `51211677`; `TestF5ApprovalBounds`, `TestF5BudgetPreTurn` | me |
| e2e F5 taint/skill | pending merge | #604 `f991cbcb`; `TestF5ContextTaintBounds`, `TestF5SkillQuarantineBounds` | me |
| Audit doc | pending merge | #587 `cde7ee18` (dokumen ini) | me |
| 201 baris FEATURE-PARITY fase F5 | pending | belum diverifikasi per baris dengan tes/PR | Main |

## Gate menaikkan PHASE ke F6

Harus hijau pada main yang memuat **semua** PR di atas:

1. `make phase-acceptance` dengan `PHASE=F5` (`DAMNBOT_PHASE_ACCEPTANCE=1 go test ./...`) — **belum bisa**: item `pending/pending merge` di atas, khususnya MCP 65, pool, execute_code/process, LSP, WIN, parity per baris.
2. `make loc` — 0 `GAGAL`; saat ini hijau pada head E (`TOTAL 95.071/190.000`).
3. `make size-check` — full ≤ 52 MB, minimal ≤ 22.000.000 B; head E minimal `21.999.778` B (headroom 222 B).
4. Full e2e biner — `go test -tags goolm ./internal/e2e`; head E PASS `758.187s` pada kode produksi identik; suite baru #603/#604 PASS.

Tidak ada ambang gate yang dilonggarkan.
