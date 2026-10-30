# F4 acceptance ledger

Acceptance F4 didefinisikan di [ROADMAP.md](ROADMAP.md) §F4
(`ROADMAP.md:156-168`), [SPEC.md](SPEC.md) §22.1 (`SPEC.md:2907-3030`),
[EDGE-CASES-hermes.md](EDGE-CASES-hermes.md), dan
[FEATURE-PARITY.md](FEATURE-PARITY.md). Keputusan artefak: ADR-244/ADR-245
(`loc.budget`), ADR-250/ADR-251/ADR-256 (`service`), ADR-252 (cap USD
gateway), ADR-253 (`gwstore`), ADR-254 (emergency stop), ADR-255
(`internal/hooks`), ADR-259/ADR-260 (re-fase parity). Setiap baris memakai
bukti yang **ada di main** dan dijalankan; PR yang masih terbuka tidak
dihitung `LULUS`.

**Snapshot audit:** `origin/main` **`cc0d1239`** (#515, 2026-10-31), tree
`778f8428`, `PHASE` = `F4`. Toolchain `go1.26.10 linux/amd64`,
workstation (bukan mesin referensi Q14).

**Status PR saat snapshot.** Merge di main: `#486`, `#491`-`#504`,
`#506`-`#525` (kecuali `#518` = PR ini). Tidak ada PR bukti tersisa; `#505`
**closed tanpa merge** (digantikan `#523` yang sudah merge). Ledger ini hanya
menyatakan `LULUS` untuk bukti di main.

**Aturan bukti.**

| status | arti |
| --- | --- |
| `LULUS` | Tes/perintah ada **di main**, dijalankan pada snapshot ini atau CI PR yang sudah merge, dan lulus; lokasi dikutip. |
| `TERBUKA` | Belum ada bukti lulus di main; sisa kerja/PR konkret dicantumkan. |
| `DITUNDA` | Di luar F4 dengan ADR + fase tujuan eksplisit; bukan "nanti". |

Angka performa diukur di workstation ini dan **bukan** angka Q14. Nama tes yang
disebut ROADMAP/SPEC tetapi tidak ada di kode dicatat sebagai temuan, bukan
diganti.

## Ringkasan

| grup | baris | LULUS | TERBUKA | DITUNDA |
| --- | ---: | ---: | ---: | ---: |
| Deliverable F4 (`ROADMAP.md:158`) | 7 | 7 | 0 | 0 |
| Acceptance F4 (`ROADMAP.md:161-168`) | 8 | 7 | 1 | 0 |
| GAP fase F4 (`SPEC.md` §22.1) | 25 | 25 | 0 | 0 |

**Anggaran.** Σ core `loc.budget` tidak berubah; `make loc` @ `cc0d1239`
rc=0, 0 `GAGAL`, TOTAL **82.955/190.000**. Baris F4: `MODULE gateway`
**5.189/9.500**, `MODULE gwstore` **996/1.000**, `MODULE cron`
**1.723/3.500**, `MODULE cronstore` **186/200**, `MODULE obs` **461/4.000**,
`MODULE scorecard` **342/500**, `heal` **565/1.500**. PR ini tidak mengubah
`loc.budget`.

## 1. Deliverable F4

| deliverable | status | bukti | sisa kerja |
| --- | --- | --- | --- |
| `gateway` core: pairing, allowlist default deny, role owner/user, isolasi sesi/memori, rate limit, outbox, inbound dedupe, approval inline keyboard | LULUS | `internal/gateway/gate.go:61` (`New`), `:147` (`Admit`), `:262` (authorize), `:312` (budget); pairing `pairing.go:31`; allowlist `allowlist.go:23`; kuota `quota.go:10`; outbox `outbox.go:80`; approval `approval.go:16-48`. Tes main: `TestAdmitUnknownIdentityNoSideEffects` `gate_test.go:113`, `TestAdmitDeniesNonAllowlistedStatuses` `:137`, `TestAdmitAllowlistedUserScope` `:203`, `TestDedupeSingleAction` `:278`, `TestAdmitRecordedSkipsInboxWrite` `:404`, `TestGatewayQuota` `quota_test.go:125`, `TestGatewayUserRootIsolation` `isolation_test.go:15`, `TestGatewayUserBlobScope` `:88`, `TestOutboxEnqueueIdempotent` `outbox_test.go:86`, `TestApprovalTimeoutDenies` `approval_test.go:48`, `TestPairBruteForce` `pairing_test.go:92`. `02-packages.log`: PASS. | — |
| `gateway/telegram`: long polling, rate-limit aware, media, inline keyboard, edit-stream, topic/thread, reconnect, offset persisten | LULUS | Main: `telegram.go:119` (`New`), `poll.go:31` (`Run`, offset persisten), `send.go:48` (chunk + pacing), `media.go`, `approval.go:24` (inline keyboard), `render_adopt_test.go:198`; #493 caps/409/thread (`TestLimiterCapsGroupBucket` `ratelimit_test.go:119`, `TestSendCapsChunksPerReply` `telegram/send_test.go:201`, `TestRunRetries409WithoutDropping` `poll_test.go:181`); #494 DLP (`TestSendScansTextBeforeEgress` `send_test.go:248`); #504 webhook: `webhook.go`, `TestWebhookRejectsMissingOrWrongSecret`, `TestWebhookWaitsForCommitBefore200`, `TestWebhookDuplicateUpdateDeliveredOnce`, `TestServeTelegramWebhookEndToEnd` `internal/e2e/telegram_webhook_test.go`, `TestServeTelegramWebhookCommitBeforeAck`, `TestSetDeleteWebhook` (`go test -tags goolm ./internal/gateway/telegram -run 'TestWebhook|TestSetDeleteWebhook'` ok; `TestServeTelegramWebhook` e2e ok). | — |
| `damnbot serve` | LULUS | `cmd/damnbot/serve.go:64` (`runServe`), `:137-166`, `:193` (`serveLoop`); lock `serve_lock.go`; drain `serve_drain.go`; estop `serve_estop.go`. Tes: `TestServeRejectsNonLoopbackBind` `serve_test.go:14`, `TestServeLockSingleInstance` `:33`, `TestDrainWaitsForActiveTurn` `:71`, `TestServeObservabilityAndCleanStop` `internal/e2e/serve_test.go:216`, `TestServeSecondInstanceRefused` `:266`, `TestServeDrainCompletesActiveTurn` `:282`, `TestServeTelegramSigtermDrainsTurn` `serve_gateway_test.go:318`. | Web UI/API server tetap F8. |
| `service install` (systemd/launchd/Windows) | LULUS | systemd: `service_cli.go:192`, `TestRenderUnitByteExact` `service_cli_test.go:62`, `TestInstallIdempotent` `:112`, `TestUninstallKeepsData` `:198`. Windows: `TestWinInstallExactArgs` `service_schtasks_test.go:240`, `TestWinInstallEnableAndRetiresLegacy` `:295`, `TestWinUninstallRetiresAllNamesKeepsData` `:356`. macOS #486/ADR-256: `TestRenderLaunchdPlistByteExact` `service_launchd_test.go:85`, `TestLaunchdInstallIdempotent` `:155`, `TestLaunchdUninstallKeepsData` `:227`, `TestLaunchdStatusClassification` `:288`; darwin-only `TestLaunchdRoundTrip` `service_launchd_darwin_test.go:20`. `02-packages.log`: PASS. | `--system`/LaunchDaemon dan restart launchd tetap di luar F4 (ADR-256). |
| `heal`: supervisor, watchdog, turn journal, repair, safe mode | LULUS | `internal/heal/supervisor.go:92`, `watchdog.go:62`, `repair.go:27,53`, `breaker.go:31`; wiring `serve_gateway.go:96-97`, `serve.go:154`. Tes: `TestSupervisorTransitions` `supervisor_test.go:10`, `TestWatchdogFreshBeatNoAction` `watchdog_test.go:13`, `TestWatchdogCrashLoopBreaker` `:73`, `TestWatchdogActionFailureTripsSafeMode` `:122`, `TestRecoverTurns` `repair_test.go:13`, `TestRepairIdempotent` `:56`, `TestBreakerResume` `breaker_test.go:50`, `TestECTERM010WatchdogDisarmVersusFire` `watchdog_test.go:186`. | — |
| `cron` (catch-up policy) | LULUS | `internal/cron/cron.go:136,152,241-254`, `script.go`, `preflight.go`, `incident.go`; wiring `serve.go:143` + `serve_gateway.go:149` + `cron_wire.go:288-362`. Tes: `TestCatchUpRestartRunsOnce` `cron_test.go:212`, `TestCatchUpPolicies` `:238`, `TestOverlapSkip` `:322`, `TestPreflightSkip` `:446`, `TestScriptTimeoutKillsAndNegativeControl` `script_unix_test.go:39`, `TestTickDeliveryOnceAcrossRestart` `incident_test.go:179`, `TestServeCronTicksDueJobOnce` `cmd/damnbot/cron_test.go:46`. | — |
| Job **scorecard mingguan** (SPEC §18.5) + alarm Telegram | LULUS | #492: `internal/scorecard/job.go:121,138`; wiring `cmd/damnbot/cron_wire.go:307,430-440`; CLI `cmd/damnbot/scorecard_cli.go:19`. Tes: `TestServeScorecardOncePerISOWeek` `scorecard_wire_test.go:90`, `TestServeScorecardWithoutRunnerIsSkipped` `:136`, `TestServeScorecardFailureAlarmsOnce` `:163`, `TestServeScorecardAlarmFallsBackToHomeChannel` `:202`, `TestScorecardCLIRunsOnceAndShowsStatus` `:228`, `TestWeekKey` `scorecard_test.go:113`, `TestTickOncePerWeek` `:165`, `TestTickDimensionRegressionAlarms` `:303`. `02-packages.log`: PASS. | — |

## 2. Acceptance F4

| acceptance | status | bukti / angka | sisa kerja |
| --- | --- | --- | --- |
| E8: orang asing → 0 tool/0 LLM; approval timeout = reject | LULUS | E2E: `TestServeTelegramUnknownSenderSilent` `internal/e2e/serve_gateway_test.go:95`; unit `TestAdmitUnknownIdentityNoSideEffects` `internal/gateway/gate_test.go:113`; `TestGatewayChatCLIAdmissionStillApplies` `internal/e2e/gateway_chat_test.go:57`. Approval default **300 s** (`telegram.go:139`), timeout `DecisionDeny` (`approval.go:11-48`); `TestApprovalTimeoutDenies` `telegram/approval_test.go:116` + `gateway/approval_test.go:48`; E2E `TestServeTelegramApproval` `serve_gateway_test.go:249`. `03-e2e.log`: PASS. | — |
| Chaos suite: 1.000 turn kill -9, network cut, disk penuh, config/DB korup, reboot VM; 0 korupsi/duplikat, recovery ≤ 10 s, 0 tool non-idempoten terulang | LULUS | `TestChaosF4` `internal/e2e/chaos_f4_test.go:43`; skenario `:174,377,464,572,599,640,744,874`. **200 ronde di main `8fa97231`** (`04-chaos-200.log`): PASS 72,72 s; kill -9 200 turn, recovery mean 54,2 ms / max 241,6 ms, reboot 51,1 ms; 8/8 subtes PASS. **1.000 ronde** (PR #470, `19fd48d9`): PASS 488,2 s. | 1.000 ronde tidak dijalankan ulang; artefak PR #470 dikutip. |
| EC F4: 19 EC-PLAT + EC-STATE-012 + EC-TERM-008..010 + EC-WIN-003/004/006 | LULUS | #491 (main) melengkapi katalog: `internal/gateway/edgecases_plat_test.go`, `internal/gateway/render/edgecases_plat_test.go`, `internal/gateway/telegram/edgecases_plat_test.go`, `cmd/damnbot/service_edgecases_windows_test.go`. Gate katalog `PHASE=F5`: `TestEdgeCaseCatalogSync` **rc=0**. Perilaku: 23 tes lulus `02-packages.log`; 3 EC-WIN di runner Windows CI. | — |
| RSS `serve` + Telegram + cron ≤ 90 MB; CPU idle ≤ 0,5 % (x86) | LULUS | `make perf-f4` @ `8fa97231` (`01-perf.log`): RSS **25,2 MB**, CPU idle **0,317 %**, 20 goroutine, 2 poll Telegram, 1 run cron; gate `true`. Negative control ditolak (153,9 MB, 201,2 %). #500 memperbaiki cron ganda; #508 (main) menambah gate wake-up ≤60/menit: `TestPerfF4` dan `TestPerfF4NegativeControl` `internal/e2e/perf_f4_test.go:105,120` (tick watchdog adaptif 1 s/10 s, satu timer idle gateway; `TestWatchdogRunStops` `internal/heal/watchdog_test.go:141`). | — |
| `TestBudgetGuardGatewayUser` (cap USD/hari role user, pra-turn, 201 turn) | LULUS | `internal/gateway/budget_test.go:100`; subtes under/at/over cap, owner, UTC reset, akumulasi, **turn 201 ditolak pra-kerja**. `02-packages.log`: PASS. Default 1,00 USD/hari (ADR-252). | — |
| Test tertunda F4: `TestGatewayUserRootIsolation`, `TestGatewayUserBlobScope` | LULUS | `internal/gateway/isolation_test.go:15,88`; `02-packages.log`: PASS. | — |
| Baseline hermes F4 (ADR-065/083) | LULUS | `bench/f4/results/hermes-868b9f244f-F4.json`: CPU idle **1,4 %**, RSS **210,4 MB** (peak 211,5), startup **34,759 s**, 0 LLM call. Damn-Bot: RSS 25,2 MB, CPU 0,317 %. | Dimensi soak/chaos/E8 hermes `n/a`; angka Damn-Bot ada di suite F4. |
| Backlog ADR-078/ADR-086 berfase F4 | TERBUKA | AUD-053 closed (`f3302ac`); AUD-063 LULUS (`loc.budget:207-222`); **AUD-051** LULUS (#508: gate wake-up ≤60/menit, lihat baris RSS/CPU). | **AUD-060** (claim `event_id` produksi; antrean #495 memakai pointer `module_state`), **NEW2-10** (`shell_allow` gateway/cron + trust root; `serve_gateway.go:421` masih `approvalOrigin: "run"`). |

### 2.1 EC F4

Sumber: [EDGE-CASES-hermes.md](EDGE-CASES-hermes.md) baris 144, 185-187,
195-198, 215-237. Tes dijalankan `02-packages.log`; EC-WIN di runner Windows
CI.

| EC | status | bukti | catatan |
| --- | --- | --- | --- |
| EC-PLAT-001 | LULUS | `TestRegistryFailureDoesNotStopSiblings` `internal/gateway/registry_test.go:70`; katalog #491 | — |
| EC-PLAT-002 | LULUS | `TestEdgeCases` `internal/gateway/telegram/edgecases_plat_test.go:14` | — |
| EC-PLAT-003 | LULUS | `TestEdgeCases` `internal/e2e/edgecases_plat_test.go:13` | — |
| EC-PLAT-007 | LULUS | `TestRouterLocksProfileToBot` `internal/gateway/router_test.go:14`; katalog #491 | — |
| EC-PLAT-009 | LULUS | `TestFenceClosingCRLF` `internal/gateway/render/render_test.go:85`; katalog #491 | — |
| EC-PLAT-010 | LULUS | `TestInlineBackticksNotPre` `render_test.go:115`; katalog #491 | — |
| EC-PLAT-011 | LULUS | `TestChunkUTF16` `render_test.go:136`; `TestSendEntitiesUTF16` `telegram/render_adopt_test.go:77`; katalog #491 | — |
| EC-PLAT-012 | LULUS | `TestMeasureCardAfterEscape` `render_test.go:172`; `TestApprovalCardTruncatesAfterEscape` `render_adopt_test.go:134`; katalog #491 | — |
| EC-PLAT-013 | LULUS | `TestECPLAT013ChunkRetryResumes` `telegram/edgecases_plat_test.go:63` | — |
| EC-PLAT-014 | LULUS | `TestECPLAT014Send429PenalizesRetryAfter` `:116`, `TestECPLAT014Edit429WaitsRetryAfter` `:162` | — |
| EC-PLAT-015 | LULUS | `TestECPLAT015ChatQueueOrdering` `internal/gateway/edgecases_plat_test.go:24` | — |
| EC-PLAT-016 | LULUS | `TestECPLAT016PollStallRebuildsAdapter` `telegram/edgecases_plat_transport_test.go:33` | — |
| EC-PLAT-017 | LULUS | `TestECPLAT017SendDeadlineRetriedViaOutbox` `:139`, `TestECPLAT017EditDeadline` `:174`, `TestECPLAT017MediaUsesLongerDeadline` `:187` | — |
| EC-PLAT-018 | LULUS | `TestAnchorInvalid` `render_test.go:222`; `TestAnchorValidation` `render_adopt_test.go:170`; katalog #491 | — |
| EC-PLAT-019 | LULUS | `TestRunDeliversPersistsOffsetAndResumes` `poll_test.go:31` | — |
| EC-PLAT-020 | LULUS | `TestApprovalForeignChatNeverDecides` `telegram/approval_test.go:136`; `TestCallbackUnknownAnsweredOnce` `:161` | — |
| EC-PLAT-021 | LULUS | `TestParseAllowlistJSONAndCSV` `gateway/allowlist_test.go:14`; `TestGatewayCLIEnvBootstrapJSON` `cmd/damnbot/gateway_cli_test.go:162`; katalog #491 | — |
| EC-PLAT-022 | LULUS | `TestGroupMentionPolicy` `gateway/policy_test.go:30`; `TestInboundMentionFacts` `telegram/mention_test.go:86`; katalog #491 | — |
| EC-PLAT-023 | LULUS | `TestECPLAT023LargeImageCompressedToPhoto` `telegram/media_test.go:47` | — |
| EC-STATE-012 | LULUS | `TestECSTATE012FlushCursorResetsPerTurn` `cmd/damnbot/serve_gateway_state_test.go:17` | — |
| EC-TERM-008 | LULUS | `TestECTERM008ShutdownBackstop` `internal/e2e/serve_test.go:323` | — |
| EC-TERM-009 | LULUS | `TestECTERM009TurnSoakKeepsFDsFlat` `internal/loop/edgecases_term_fd_test.go:28` + kontrol `:59` | — |
| EC-TERM-010 | LULUS | `TestECTERM010WatchdogDisarmVersusFire` `internal/heal/watchdog_test.go:186` | — |
| EC-WIN-003 | LULUS | `TestServiceWindowsEdgeCases` `cmd/damnbot/service_edgecases_windows_test.go:18`; `TestWinStatusSupervision` `service_schtasks_test.go:422` | runner Windows CI |
| EC-WIN-004 | LULUS | `TestServiceWindowsEdgeCases`; `TestWinTaskNameIsProfileScoped` `:117`, `TestWinUninstallRetiresAllNamesKeepsData` `:356` | runner Windows CI |
| EC-WIN-006 | LULUS | `TestServiceWindowsEdgeCases`; `TestDecodeTaskOutputUTF16LE` `:132`, `TestWinLogsDecodesUTF16Query` `:488` | runner Windows CI |

## 3. GAP fase F4 (SPEC §22.1, 25 baris)

ROADMAP menyatakan acceptance tiap fase bertambah dengan **semua** gap berfase
itu (`ROADMAP.md:228`). Status `LULUS` hanya untuk bukti di main; PR terbuka
disebut di kolom sisa.

| GAP | fase otoritatif | status | bukti / sisa |
| --- | --- | --- | --- |
| GAP-SEC-11 | F3, F4, F7 penuh (`SPEC.md:2923`) | LULUS | #507 (main): canary e2e `TestServeLogCanary` `internal/e2e/log_canary_test.go:83` + `TestServeLogCanaryNegativeControl` `:124`; redaksi `TestLogContentFreeAtInfo` `internal/obs/log_test.go:46`, `TestLogPlatformIDsHMAC` `:71`, `TestLogContentTTL` `:107`, `TestLogValueNeverWritten` `:133`; coverage `internal/obs` 89% pada CI #507 `b4af6ce8`. |
| GAP-SEC-13 | F4 (`SPEC.md:2925`) | LULUS | #506 (main): nonce approval sekali lintas restart `TestApprovalNonceConsumedOnceAcrossRestart` `internal/gateway/telegram/approval_replay_test.go:11`, `TestApprovalNonceCrossSessionReplayRejected` `:62`, `TestServeTelegramApprovalReplayRejected` `internal/e2e/approval_replay_test.go:20`. #515 (main): replay keputusan durable `TestDurableApproveReplayNoSecondPrompt` `cmd/damnbot/approval_durable_test.go:46`, `TestDurableDenyReplayStaysDenied` `:62`, `TestDurableExpiredCannotBeApproved` `:78`, `TestDurableParallelSessionsIndependent` `:96`; e2e `TestServeTelegramApprovalDurableApprove` `internal/e2e/approval_durable_test.go:29`, `TestServeTelegramApprovalDurableDeny` `:77`. |
| GAP-SEC-14 | F1 subset / F4 penuh (`SPEC.md:2926`) | LULUS | #523 (main): `TestServeTelegramSudoNeedsApproval`, `TestServeTelegramNoSudoPassword` `internal/e2e/sudo_elevation_test.go` (approval pra-eksekusi, tanpa `SUDO_PASSWORD`); importer tetap menolak `SUDO_PASSWORD` (`internal/importer/env.go:34`). |
| GAP-SEC-16 | F1 subset / F2, F4, F5, F6, F7 penuh (`SPEC.md:2928`) | LULUS | #494 (main): `TestSendScansTextBeforeEgress` `telegram/send_test.go:248`, `TestSendScansCaptionBeforeEgress` `:276`, `TestScanTextGateway` `netx/dlp_test.go:657`; `internal/netx/dlp.go`. |
| GAP-SEC-27 | F4 (`SPEC.md:2939`) | LULUS | #494/#495 (main): `TestPairingPlatformCap` `gateway_pairing_cap_test.go:22`, `TestPairingSourceCap` `:55`, `TestPairingOwnerNoticeOnce` `:75`. |
| GAP-REL-02 | F4 (`SPEC.md:2942`) | LULUS | #493 (main): `TestRunRetries409WithoutDropping` `poll_test.go:181`, `TestRunNotifiesOwnerAfter409Window` `:210`; offset `poll.go:31-74`. |
| GAP-REL-07 | F1 subset / F3, F4, F8 penuh (`SPEC.md:2947`) | LULUS | #494 (main): `TestProviderWaitTracksFirstUpdate` `serve_gateway_wait_test.go:11`; `internal/netx/stream.go`. |
| GAP-REL-08 | F4 (`SPEC.md:2948`) | LULUS | `TestOverlapSkip` `cron_test.go:322`, `TestOverlapQueueOne` `:361`, `TestOverlapParallel` `:406`. |
| GAP-REL-09 | F4 (`SPEC.md:2949`) | LULUS | #494 (main): `TestRecoverCrashedRunsClosesSlotOnce` `cron_recovery_test.go:60`, `TestRecoverPausesAfterConsecutiveCrashes` `:106`; #503 (main): `TestRecoverRetriesOnlyOptIn`, `TestStopAfterFinishIsNoActive`, `TestConcurrentStopsClaimOnce`, `TestStopVsFinishDeterministic`. |
| GAP-REL-10 | F4 (`SPEC.md:2950`) | LULUS | `telegram/send.go:48-120`, `TestECPLAT013ChunkRetryResumes`, `TestDeliveryIdempotent` `cron_test.go:269`, `TestTickDeliveryOnceAcrossRestart` `incident_test.go:179`. |
| GAP-REL-11 | F4 (`SPEC.md:2951`) | LULUS | #495 (main): `TestInboundQueueRoundTrip` `serve_gateway_queue_test.go:19`, `TestInboundQueueOverflowReplies` `:58`, `TestChatLeaseBlocksLiveOwnerAndExpires` `:103`, `TestResumeDrainsQueuedUpdate` `:128`; #503 (main): `TestBusyPolicyQueueAndSteer`, `TestBusyPolicyRejectNotify`. |
| GAP-REL-12 | F4 (`SPEC.md:2952`) | LULUS | #499 (main): `TestSoakF4Resources` `internal/e2e/soak_f4_test.go:17`; `boundary.Scope`. |
| GAP-REL-14 | F4/F5/F7/F8 (`SPEC.md:2954`) | LULUS | #499 (main): 10 `TestKnownDefect*` `internal/gateway/render/known_defects_test.go:12-`; skenario 413/kanban/MCP tetap fase modulnya. |
| GAP-REL-17 | F4 (`SPEC.md:2957`) | LULUS | #498 (main): `TestCircuitStoreShared` `circuit_store_test.go:16`, `TestOldestQueued` `:48`, readyz `internal/obs`; `/readyz` memeriksa DB, lease, adapter, outbox, circuit. #522 (main): probe `/readyz` memakai DSN privat minimal + deadline 250 ms (`TestProbeDSNIsMinimal` `internal/ctxdb/probe_test.go:14`, `TestProbeWriteBlocked` `:42`) dan pacing Telegram memakai clock injeksi (`TestPacingDeadlineUsesInjectedClock` `telegram/send_test.go:160`). |
| GAP-GW-02 | F4 (`SPEC.md:2960`) | LULUS | `TestGatewayQuota` `quota_test.go:125`, `TestBudgetGuardGatewayUser` `budget_test.go:100`, `TestGatewayUserRootIsolation` `isolation_test.go:15`, `TestGatewayUserBlobScope` `:88`. |
| GAP-GW-03 | F4 core+Telegram (`SPEC.md:2961`) | LULUS | #493 (main): `TestLimiterCapsGroupBucket` `ratelimit_test.go:119`, `TestLimiterReplay200GroupReplies` `:148`, `TestSendCapsChunksPerReply` `telegram/send_test.go:201`. |
| GAP-GW-04 | F4 AST+Telegram (`SPEC.md:2962`) | LULUS | `internal/gateway/render/parse.go`, `render.go:138`; `TestFenceClosingCRLF` `render_test.go:85`, `TestInlineBackticksNotPre` `:115`, `TestChunkUTF16` `:136`, `TestMeasureCardAfterEscape` `:172`, `TestAnchorInvalid` `:222`, `FuzzRenderTelegram`. |
| GAP-GW-05 | F4 core+Telegram (`SPEC.md:2963`) | LULUS | `TestGroupMentionPolicy` `policy_test.go:30`, `TestMentionPolicyStoreFailureDenies` `:81`, `TestMentionPolicyMalformedOverrideDenies` `:98`; `mention_test.go:86`. |
| GAP-GW-06 | F4 (`SPEC.md:2964`) | LULUS | #493 (main): `TestThreadNotFoundIsTyped` `telegram/send_test.go:228`, `TestThreadNotFoundFailsAndNotifiesOwner` `cmd/damnbot/serve_gateway_delivery_test.go:160`; #503 (main): `TestGatewayBusyPolicyAndThreadFallback`, `TestThreadFallbackParentIsOptIn`. |
| GAP-COST-06 | F4 (`SPEC.md:2976`) | LULUS | #498 (main): `TestSharedCircuitCrossProcess` `internal/netx/retry/circuit_test.go:28`, `TestSharedCircuitWindowAndNonRetryable` `:62`; `provider_circuits`. |
| GAP-COST-13 | F4 (`SPEC.md:2983`) | LULUS | #496/#499 (main): `TestParseGate` `internal/cron/gate_test.go:28`, `TestCronGateScript` `:136`, `TestNormalizeMonitor` `:52`, `TestCronGateNormalized` `:86`. |
| GAP-COST-14 | F4 (`SPEC.md:2984`) | LULUS | #496 (main): `TestCronMinIntervalGuard` `internal/cron/admin_test.go:41`, `TestCronJobQuota` `:64`; dedup `cron_store.go:54`. |
| GAP-COST-16 | F4 (`SPEC.md:2986`) | LULUS | CPU idle main **0,317 %** lulus (`make perf-f4`); #508 (main): gate wake-up ≤60/menit via counter `damnbot_idle_wakeups_total`, `TestPerfF4` `internal/e2e/perf_f4_test.go:105` + kontrol negatif `TestPerfF4NegativeControl` `:120`, watchdog adaptif `TestWatchdogRunStops` `internal/heal/watchdog_test.go:141`. |
| GAP-PROV-03 | F4 (`SPEC.md:2989`) | LULUS | #499 (main): `TestTimezoneCommand` `commands_tz_test.go:13`, `TestVolatileTimeIdentityZone` `internal/assemble/volatile_test.go:85`, `TestScheduleDSTNewYork` `internal/cron/schedule_test.go:79`; `time/tzdata`. |
| GAP-OPS-01 | F1 subset / F3, F4 penuh (`SPEC.md:3012`) | LULUS | #501 (main, ADR-257): override generik `DAMNBOT_<SECTION>_<KEY>`, `docs/CONFIG-REFERENCE.md` tergenerasi, provenance `config explain`, allowlist `os.Getenv`; `TestConfigGeneratedNoDrift`, `TestConfigExplainEnvSource`, `TestEnvOverrides`, `TestEnvOverrideInvalid`, `TestEnvNameHasNoAliases`, `TestE2EConfigEnvOverride`, `TestEnvAllowlist`; `config hooks` #484. |

**ADR per baris.** ADR-020 (Telegram), ADR-044/046/052/084 (approval, inbound
durable, outbox, workspace role `user`), ADR-061 (EC-PLAT F4), ADR-071
(redaksi/log), ADR-076/084 (isolasi role `user`), ADR-080/176 (gate fase),
ADR-083 (baseline/test tertunda), ADR-244/245 (`loc.budget`),
ADR-250/251/256 (service + disk), ADR-252 (cap USD), ADR-253 (`gwstore`),
ADR-254 (emergency stop), ADR-255 (hooks), ADR-258 (busy policy/thread
fallback), ADR-259/260 (re-fase EXEC-016/GW-025).

## 4. Parity F4 (54 baris setelah re-fase)

`TestParityPhaseColumn` (`internal/archtest/parity_phase_test.go:107-110`)
mengharapkan **F4 54, F7 103**. Kolom `status` FEATURE-PARITY tetap `planned`
(bukan gate; ADR-139); hitungan adalah inventaris permukaan + tes di main.

- **54 baris penuh di main**: CLI-011, CLI-018, CLI-023, CLI-024, CLI-045,
  SLASH-001, SLASH-003, SLASH-021, SLASH-023, SLASH-041, SLASH-043,
  SLASH-056, SLASH-076, SLASH-089, SLASH-095, SLASH-096, TOOL-025,
  TSET-012, TSET-044, TSET-045, TSET-062, PLAT-022, AUTO-005, AUTO-006,
  AUTO-007, AUTO-008, AUTO-009, AUTO-011, AUTO-029, PLUG-011, EXEC-017,
  OPS-026, OPS-027, OPS-028, OPS-033, OPS-037, GW-001, GW-002, GW-003,
  GW-004, GW-005, GW-006, GW-007, GW-008, GW-009, GW-010, GW-017, GW-018,
  GW-019, GW-020, GW-021, GW-024.
- **0 baris parsial**.
- **2 baris DITUNDA → F7**: EXEC-016 (ADR-259), GW-025 (ADR-260).

| prefix | baris F4 | penuh | parsial | pending PR |
| --- | ---: | ---: | ---: | ---: |
| CLI | 6 | 6 | 0 | 0 |
| SLASH | 12 | 12 | 0 | 0 |
| TOOL | 1 | 1 | 0 | 0 |
| TSET | 4 | 4 | 0 | 0 |
| PLAT | 1 | 1 | 0 | 0 |
| AUTO | 7 | 7 | 0 | 0 |
| PLUG | 1 | 1 | 0 | 0 |
| EXEC | 1 | 1 | 0 | 0 |
| OPS | 5 | 5 | 0 | 0 |
| GW | 16 | 16 | 0 | 0 |
| **Σ** | **54** | **54** | **0** | **0** |

### 4.1 Baris parsial

Tidak ada; permukaan F4 yang tersisa hanya dua baris DITUNDA→F7 (§4.2).

### 4.2 Baris DITUNDA → F7

| ID | ADR | alasan |
| --- | --- | --- |
| EXEC-016 | ADR-259 | External approval transport butuh connector/platform transport F7; F4 hanya transport bawaan. |
| GW-025 | ADR-260 | Telegram managed bot butuh partner/managed-bot API di luar Bot API F4. |

### 4.3 Bukti baris yang ditutup di main

| ID | PR merge | bukti tes main |
| --- | --- | --- |
| CLI-018 | #512 | `TestSurfaceRegistry` (25 entri; `send` di `damnbot help` + `send_spool_test.go:12`) |
| CLI-011 | #519/#521 | `TestGatewayEnrollAndMigrate` `gateway_cli_test.go`, `TestGatewayPairingSurface` (`enroll`, `migrate status/plan/apply`, alias `pairing list/approve/revoke/clear-pending`, `migrate-legacy`; idempoten) |
| CLI-025 | #517 | `TestCronNotepadRoundTrip`, `TestCronRunsQueryLimitAndOrder`, `TestCronDoctorFindsBrokenConfig`, `TestCronTickParallelClaimsOnce` `cmd/damnbot/cron_inspect_test.go` (+ `cron edit`) |
| CLI-045 | #514 + #521 | `TestGatewayPairingApproveClear` `gateway_cli_test.go:216` (`pairing list` dari #495 + `approve`/`clear-pending`: approve sekali, replay, retensi identitas, clear hanya kode unconsumed); #521 menambah alias `revoke`/`clear-pending` dan `migrate-legacy` (`TestGatewayPairingSurface`) |
| SLASH-022 | #509 | `TestServeTelegramApproveCommand` `internal/e2e/gateway_approve_test.go:20`, `TestApproveWithoutPending` `:66` (deny/allow teks, replay idempoten, id unknown tidak menjalankan tool) |
| AUTO-011 | #510 | `TestCronJobSelfScheduleLimits` `internal/tools/cronjob_test.go` + suite `SelfSchedule` (depth/quota/idempotensi; penolakan `denied` tanpa tulis) |
| AUTO-029 | #513 | `internal/cron/periodic`: `TestFirstRun`, `TestNoOverlap`, `TestBudget`, `TestRestartNoDuplicate`, `TestDeliveryOncePerSlot`, `TestJitterStable`; `TestServePeriodicRunsOnceAcrossRestart` `internal/e2e/periodic_test.go` |
| OPS-028 | #520 | `TestServeEarlyRecoveryRollback` `internal/e2e/recovery_test.go:61` (binary e2e: target checksum salah → `.prev` restored, pending dihapus, DB tidak di-rollback), `TestRepairLocalChecksum` `internal/update/repair_local_test.go:51` |
| AUTO-009 | #516 | `TestCronHealthConditions` `cron_health_test.go:64`, `TestCronHealthNoticesOnceAndRecovery` `:95` (stalled/failing/stale_success, satu notice per transisi, satu notice recovery), `TestServeShutdownDuringCronHealth` `internal/e2e/serve_health_shutdown_test.go:18` |
| SLASH-003, GW-019 | #511 | `TestServeTopicBindAndOff`, `TestTopicOffAndBind` (topic `off`/`session-id`, bind, unknown id) |
| SLASH-076, GW-007, GW-020 | #502 | `TestGatewayCronAddEdit`, `TestTimestampsAndDelayToggles`, `TestGatewayTimestampsPrefixRepliesAndOutbox` |
| PLAT-022 | #504 | `TestWebhookRejectsMissingOrWrongSecret`, `TestWebhookWaitsForCommitBefore200`, `TestWebhookDuplicateUpdateDeliveredOnce`, `TestServeTelegramWebhookEndToEnd`, `TestServeTelegramWebhookCommitBeforeAck`, `TestSetDeleteWebhook` |
| OPS-026 | #486 | `TestRenderLaunchdPlistByteExact` `service_launchd_test.go:85`, `TestLaunchdInstallIdempotent` `:155`, `TestLaunchdUninstallKeepsData` `:227`, `TestLaunchdStatusClassification` `:288`, `TestLaunchdRoundTrip` `service_launchd_darwin_test.go:20` |
| OPS-033 | #486 | `TestCheckDiskStatus` `internal/doctor/doctor_test.go:284`; prune `TestSessionsAdminPrune` `internal/e2e/sessions_admin_test.go:64`, `TestSessionsPruneID` `sessions_prune_search_test.go:64` |

## 5. Sisa/gap jujur (daftar yang masih TERBUKA/parsial)

**Deliverable TERBUKA (0):** — (webhook + secret token sudah di main lewat
#504).

**Acceptance TERBUKA (1):** backlog F4 — AUD-060, NEW2-10 (AUD-051 LULUS
via #508).

**GAP TERBUKA (0):** —.

**Parity parsial (0):** —.

**Parity pending PR (0):** — (AUTO-029 #513 sudah merge).

**Catatan.**
1. `#503` (ADR-258/gateway residual) sudah merge; ADR-259/260 kini berada
   setelah ADR-258 dengan urutan 257, 258, 259, 260 dan satu salinan per
   nomor. `#501`/ADR-257, `#502`, `#504`, `#506`-`#525` juga sudah merge.
2. Tidak ada PR bukti yang tersisa; `#505` closed tanpa merge dan digantikan
   `#523` (sudah merge, SEC-14 LULUS).
3. SEC-13 ditutup oleh #506 (nonce sekali lintas restart) + #515 (replay
   keputusan durable setelah restart), keduanya di main.
4. Kanban tetap F7; cron F4 tidak mengklaim dispatch/board kanban.
5. EC-WIN-003/004/006 bergantung pada runner Windows CI, bukan host Linux.
6. REL-14 adalah subset F4 (10 skenario render/pengiriman); skenario
   413/kanban/MCP tetap fase modulnya.
7. #524 hanya menyesuaikan deteksi goroutine baru di `TestSoakF4Resources`
   (tanpa perubahan status); #525 menutup stream setelah panic recovery.

## 6. Kesiapan

**Belum siap bump `PHASE=F5`.** Gate katalog EC `PHASE=F5` rc=0; `make loc`
rc=0 (0 `GAGAL`, TOTAL 82.955/190.000); paket F4 fokus rc=0;
`TestADRUnique`/`TestADRMonotonic`/`TestParityPhaseColumn` rc=0. Angka
`make perf-f4` (RSS 25,2 MB, CPU 0,317 %) dan chaos 200 tetap dari artefak
snapshot sebelumnya. Satu-satunya yang menahan: backlog AUD-060/NEW2-10.
ADR-259/260 tidak menahan: keduanya sudah berurutan setelah ADR-258.

## 7. Audit ulang (perintah yang dijalankan)

Snapshot `cc0d1239` (rebase #518 ke main yang memuat #506-#525). Artefak:
`~/.cache/damnbot/evidence-f4-ledger-pr/`.

```bash
DAMNBOT_PHASE_ACCEPTANCE=1 go test -tags goolm -count=1 -run '^TestEdgeCaseCatalogSync$' ./internal/testutil/phase   # rc=0 (PHASE=F5 sementara)
go test -tags goolm -count=1 ./internal/archtest -run 'TestADR|TestParity|TestEdgeCaseCatalogSync|TestBudget|TestEngineering'  # rc=0; ADR/parity/budget/engineering PASS
go test -tags goolm -count=1 ./internal/archtest                                                                       # rc=0
go test -tags goolm -count=1 -run '^(TestServeTelegramApproveCommand|TestApproveWithoutPending|TestCronJobSelfScheduleLimits|TestServePeriodicRunsOnceAcrossRestart|TestFirstRun|TestIntervalAndDrift|TestNoOverlap|TestBudget|TestPreflightSkip|TestPauseResume|TestRestartNoDuplicate|TestDeliveryOncePerSlot|TestDeliveryFailureOncePerSlot|TestRegisterValidation|TestJitterStable|TestCronNotepadRoundTrip|TestCronRunsQueryLimitAndOrder|TestCronDoctorFindsBrokenConfig|TestCronTickParallelClaimsOnce|TestGatewayEnrollAndMigrate|TestGatewayPairingSurface|TestServeTelegramSudoNeedsApproval|TestServeTelegramNoSudoPassword|TestGatewayBusyPolicyAndThreadFallback|TestThreadFallbackParentIsOptIn|TestBusyPolicyQueueAndSteer|TestBusyPolicyRejectNotify|TestRecoverRetriesOnlyOptIn|TestStopAfterFinishIsNoActive|TestConcurrentStopsClaimOnce|TestStopVsFinishDeterministic|TestGatewayPairingApproveClear|TestCronHealthConditions|TestCronHealthNoticesOnceAndRecovery|TestServeShutdownDuringCronHealth|TestProbeDSNIsMinimal|TestPacingDeadlineUsesInjectedClock|TestServeTopicBindAndOff|TestTopicOffAndBind)$' ./internal/e2e ./internal/gateway ./internal/tools ./internal/cron/periodic ./cmd/damnbot ./internal/config ./internal/gateway/telegram ./internal/ctxdb  # rc=0 (bukti #503/#509-#523)
go test -tags goolm -count=1 -run '^(TestServeLogCanary|TestServeLogCanaryNegativeControl|TestLogContentFreeAtInfo|TestLogPlatformIDsHMAC|TestLogContentTTL|TestLogValueNeverWritten|TestApprovalNonceConsumedOnceAcrossRestart|TestApprovalNonceCrossSessionReplayRejected|TestServeTelegramApprovalReplayRejected|TestDurableApproveReplayNoSecondPrompt|TestDurableDenyReplayStaysDenied|TestDurableExpiredCannotBeApproved|TestDurableParallelSessionsIndependent|TestServeTelegramApprovalDurableApprove|TestServeTelegramApprovalDurableDeny|TestWatchdogRunStops|TestServeEarlyRecoveryRollback|TestRepairLocalChecksum)$' ./internal/e2e ./internal/obs ./internal/gateway/telegram ./cmd/damnbot ./internal/heal ./internal/update  # rc=0 (bukti #506/#507/#515/#520)
make loc                                                                                                               # rc=0; 0 GAGAL; TOTAL 82.955/190.000
BASE=origin/main HEAD=HEAD PR_TITLE=... scripts/pr-meta.sh                                                             # rc=0
```

**Tidak dijalankan ulang:** `make perf-f4` (angka RSS 25,2 MB / CPU 0,317 %
tetap dari `01-perf.log`), chaos 200/1.000 ronde, `make phase-acceptance`
penuh, runner Windows, angka Q14, dan tes PR terbuka.
