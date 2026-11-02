# F5 acceptance ledger

Acceptance F5 didefinisikan di [ROADMAP.md](ROADMAP.md) §F5
(`ROADMAP.md:170-179`), [SPEC.md](../SPEC.md) §22.1,
[FEATURE-PARITY.md](FEATURE-PARITY.md), [EXEC-AND-CODING.md](EXEC-AND-CODING.md),
dan [CODE-TOOLS.md](CODE-TOOLS.md). Baris memakai bukti yang **ada di main**.

**Status final: `LULUS`.** Semua item acceptance F5 (17 GAP, 25 EC, backlog
ADR-078/ADR-086) berstatus `LULUS` atau `N/A` beralasan; gap yang tidak
dikerjakan di F5 dicatat `DITUNDA` dengan ADR + fase target. Tidak ada
`LULUS-SEBAGIAN`, `TERBUKA`, atau `BLOKIR` yang tersisa.

**Snapshot audit:** `origin/main` **`a5352334`** (#629), `PHASE` = `F5` → `F6`
di PR ini. Toolchain `go1.26.10 linux/amd64`, workstation (bukan mesin
referensi Q14).

## Verdict gate

| gate | perintah | hasil |
| --- | --- | --- |
| acceptance F5 | `PHASE=F5 make phase-acceptance` pada `365d6fae` | rc=0, 76/76 paket `ok`, 0 `PHASE GAGAL` |
| acceptance F6 (PR ini) | `PHASE=F6 make phase-acceptance` pada `a5352334` | rc=0, 76/76 paket `ok`, 0 `PHASE GAGAL` (lihat CHANGELOG Unreleased) |
| LOC | `make loc` | `TOTAL 101.267 / 190.000`, 0 `LOC GAGAL` |
| ukuran | `make size-check` | full **31.776.930 B** / 52.000.000; minimal **21.999.778 B** / 22.000.000 |

## EC F5 — semua `covered`

25 EC berfase F5 (ROADMAP §F5) semuanya punya `Gate.Item(..., "F5",
"covered")` dan lulus; `TestEdgeCaseCatalogSync` (PHASE=F6) lulus tanpa
"required at F5 but no package catalog declares it".

| EC | bukti (tes/berkas) |
| --- | --- |
| EC-PROV-002 | `internal/provider/gemini/gemini_test.go` |
| EC-PROV-003 | `TestRotatingItemIDKeepsCallID` (`internal/provider/responses/edgecases_f5_test.go`) |
| EC-PROV-014 | `internal/provider/refresh_test.go` |
| EC-PROV-017 | `TestPoolRotationKeepsRouteHost` (`internal/provider/pool_test.go`) |
| EC-PROV-018 | `TestInvalidGrantMarksRefresherDead` (`internal/provider/auth/edgecases_prov_f5_test.go`) |
| EC-PROV-019 | `TestRefreshTokenNeverReplayed` (`internal/provider/auth/edgecases_prov_f5_test.go`) |
| EC-PROV-020 | `TestPoolBillingAmbiguousShortCooldown` (`internal/provider/pool_test.go`) |
| EC-PROV-021 | `TestPoolManifestLoadIsNonDestructive`, `TestPoolManifestMissingKeychainFailsClosed` (`cmd/damnbot/edgecases_pool_manifest_test.go`) |
| EC-PROV-022 | `TestRebuiltClientKeepsCredentialCallback` (`internal/provider/edgecases_prov_f5_test.go`) |
| EC-PROV-023 | `TestLocalAliasGetsV1` (`cmd/damnbot/edgecases_prov_f5_test.go`) |
| EC-PROV-026 | `TestAuxRouteSharesRuntimeRegion` (`cmd/damnbot/edgecases_prov_region_f5_test.go`) |
| EC-PROV-027 | `internal/provider/edgecases_cache_f5_test.go` |
| EC-CFG-008 | `TestProviderSwitchLoadsProviderPool` (`cmd/damnbot/edgecases_cfg_f5_test.go`) |
| EC-FIO-003 | `internal/exec/backends/edgecases_f5_test.go` |
| EC-FIO-010 | `TestUTF16RangeConversion` (`internal/codeintel/lsp/edgecases_fio_test.go`) |
| EC-SEC-002/003 | `internal/provider/auth/credential_test.go` |
| EC-SEC-004 | `internal/provider/edgecases_sec_f5_test.go` |
| EC-TCALL-004 | `internal/provider/responses/edgecases_f5_test.go` |
| EC-TCALL-011 | `internal/provider/replay/parity_test.go` |
| EC-TERM-001 | `internal/exec/process_test.go` |
| EC-TERM-003 | `internal/exec/backends/edgecases_test.go` |
| EC-WIN-007 | `internal/exec/backends/edgecases_win_f5_test.go` (runner Windows) |
| EC-WIN-008 | `internal/exec/pipe_auth_test.go` (runner Windows) |
| EC-WIN-015 | `internal/exec/mux_test.go` (runner Windows) |

## 17 GAP fase F5

| GAP | status | tes | bukti | residual (di luar acceptance F5) |
| --- | --- | --- | --- | --- |
| GAP-SEC-03 | `LULUS` | `TestGAPF5_SEC03`; `TestUntrustedToolDeniedByDefault`, `TestToolEffectDefaultsToNonIdempotent`, `TestApprove*` | `internal/mcp/config.go:14-91` (trust default untrusted), `policy.go:12-43` (non-read selalu approval), `client.go:221-266` (deny sebelum dispatch), `config/modules_mcp.go:52` (read_only per tool); 10/10 prompt, headless 10/10 `denied`, 0 dispatch. | Wiring modul MCP→turn + matriks mode lewat core, dan konfirmasi `trust=full`/doctor: **N/A F5**, target F8 (permukaan modul/doctor; MCPX 65/65 sudah lulus #613). |
| GAP-SEC-04 | `LULUS` | `TestGAPF5_SEC04`, `TestPin*` (6 tes), `TestMCPServeCannotSelfApprove` | `internal/mcp/pin.go:120-235` (digest sha256, first-listing pin, modified/added/removed quarantine), `pinGate`, `ctxdb/mcpstore`, `mcp refresh/review`; 0 tool terlihat, 0 dispatch (`mcp_tool_changed`). | Diff di surface TUI/web: **N/A F5**, target F8 (CLI `mcp review` sudah menampilkan diff). |
| GAP-SEC-18 | `LULUS` | `TestGAPF5_SEC18`, `TestOAuthRefreshRevocation`, `TestAuthLoginStatusLogoutRevoke`, `TestAuthRevokeFailureKeepsToken`, `TestAuthOfflineFails` | `internal/provider/auth/oauth.go:227-252` (RFC 7009 refresh lalu access), `cmd/damnbot/auth_cli.go:125-160` (revoke dulu, state `revoked/unsupported/failed`), `mcp_oauth.go:139-162`; offline → `ErrOffline`. | Flag `--provider|--all` pada logout + `setup --uninstall --purge-data`: **N/A F5**, target F9 (installer/UX rilis). |
| GAP-SEC-20 | `LULUS` | `TestGAPF5_SEC20*`, `TestGAPF5_SEC20CLI`, `TestGAPF5_SEC20ConfigKey`, `TestGAPF5_SEC20SameHostRedirects`, `TestGAPF5_SEC20FetchAndSerial` | `internal/mcp/catalog` (manifest ed25519, tamper/unsigned/rollback fail-closed), `Fetch` 2xx+16 MiB, `WithSameHostRedirects(3)`, `FileSeqStore` serial lintas proses, `mcp catalog <file\|--url>`, `[update] catalog_public_key`. | Skills-hub/preset catalog fetch: **N/A F5**, target F8 (ADR-306 amandemen). |
| GAP-SEC-21 | `LULUS` | `TestGAPF5_SEC21`, `TestRespondRefusesSelfApprovalAndP26`, `TestRespondAndOpenApprovals`, `TestServe*`, `TestMCPServeCannotSelfApprove` | `ctxdb/mcpservestore` (scope read/send/approve, approval_response exactly-once, origin≠mcp_serve, P26), `internal/mcp/serve.go` (scope filter di `Respond`, rate limit, approval non-read), `cmd/damnbot/mcp_serve_cli.go` + token CLI. | — |
| GAP-SEC-22 | `DITUNDA` | `TestDockerRunContract`, `TestDockerRejectsEscapes`, `TestDockerBackendNoSocketMount` | `internal/exec/backends/docker.go:142-160,267-292` (hanya bind workspace; docker.sock ditolak). | Image `ghcr.io/damnbot/damnbot` distroless/user 65532/healthcheck/Trivy = artefak rilis **F9** (ROADMAP F9 "Docker 1 stage"); tidak ada kode backend yang berubah. |
| GAP-SEC-23 | `LULUS` | `TestGAPF5_SEC23DockerProbe`, `TestGAPF5_SEC23SingularityProbe`, `TestSSHHostKeyPin`, `TestDockerRunContract` | `docker.go:218-255` (`docker create` dengan limit sebelum run; gagal → `ErrCapability`), `singularity.go:140-174` (`cgroup status`), cache `sync.Once`, `allow_unlimited` satu-satunya opt-out; SSH host-key pin + egress allowlist. | — |
| GAP-SEC-24 | `LULUS` | `TestGAPF5_SEC24`, `TestOverrideWithoutFlag` | `cmd/damnbot/provider_resolve.go:115-135` (override=true wajib), `:143-161` (`--confirm-base-url`), `:272-300` (`providerDoctorWarnings`), `config/config.go:246-248`. | — |
| GAP-SEC-25 | `LULUS` | `TestGAPF5_SEC25`, `TestUserAgentDefault`, `TestNoAttributionHeaders` | `provider_resolve.go:305-410` (`send_attribution` default false), `provider/client.go:25-28,103-126,413-415` (UA `damnbot/<versi>`); 94 preset tanpa header atribusi. | — |
| GAP-REL-14 | `LULUS` | `TestGAPF5_REL14`, `internal/gateway/render/known_defects_test.go` (10 tes) | `internal/provider` (413 typed non-retryable, truncated stream `stream_broken` retry), `netx/retry/class.go:122`; render known-defects 10 skenario. | Gambar MCP sebagai `Attachments`: **N/A F5**, target F6 (media); job CI `known-defects`: target F9. |
| GAP-REL-18 | `LULUS` | `TestGAPF5_REL18`, `TestSessionToolIsolation`, `TestSessionViewFailClosed`, `TestSessionViewChildCannotEscalate` | `internal/tools/session_view.go`, `modules/host/session.go` (view eksplisit, zero fail-closed), `delegate` meneruskan `tools.SessionView`; 1.000 iterasi `-race`. | MCP `Client` session view + `execute_code` `Env.Session` seam: **N/A F5**, target F8 (seam modul host). |
| GAP-COST-03 | `LULUS` | `TestGAPF5_COST03`, `TestCacheTTLByOrigin`, `TestUsageCacheByOrigin` | `provider/cachettl.go` (1h origin interaktif, clamp `CacheTTLMax`), `provider/openai.go` encode ttl, `cmd/damnbot/usage_cli.go` (`--cache --by origin`); 9 origin × clamp. | Live replay Telegram `cached/input ≥70%`: **N/A** (butuh provider berbayar; bukan acceptance offline). |
| GAP-COST-10 | `LULUS` | `TestExecuteCodeApproval`, `TestExecuteCodeRun`, `TestExecuteCodeRPCNetworkDeny`, `TestGAPF5_COST10StarlarkChild` | `internal/exec/code` Python/JS/Go/Starlark, child `__starlark`, RPC allowlist 7 tool, `network=deny`, `DispatcherInvoker` wiring. | — |
| GAP-PROV-01 | `LULUS` | `TestGAPF5_PROV01`, `TestGAPF5_PROV01Kinds`, `TestCarrier*` (≥ 20 tes) | `provider/carriers.go` (capture byte-identik, project family+model asal), 4 adapter, `loop/turn.go:198-210` (`carrier_dropped`), `ctxdb/carrierstore`; 4 kind × 4 adapter, replay hanya route asal. | — |
| GAP-TOOL-04 | `LULUS` | `TestGAPF5_TOOL04` (Windows), `TestSSHPoolReuse`, `TestShellChoiceDrivesDialect`, `TestSSHContract` | `exec/backends/ssh.go:299-395` (pool per user/host/port, keepalive 30 s, idle 10 m), `shell_windows.go` (pwsh→powershell→cmd), `proc_windows.go`; 20 run → 1 koneksi TCP. | — |
| GAP-TOOL-05 | `LULUS` | `TestGAPF5_TOOL05`, `TestLeaseConflictDetected`, `TestWorktreeCleanup`, `TestCodetoolsLeaseGuard`, `TestWorktreeLeaseIsExclusive` | `delegate/lease.go` (lease per path, all-or-nothing, pemilik di error), `delegate/worktree.go` (bersih dihapus, kotor disimpan + `files_changed`), `codetools.LeaseGuard`; 3 task → 1 jalan, 2 `lease_conflict`. | — |
| GAP-TOOL-08 | `LULUS` | `TestProcessTools*`, `TestBackground*`, `TestProcessNotifyRateLimit` | `internal/exec/process.go` registry + `Background.SetOnExit`; `NoticeLimiter` 1/15 s proses, 4/menit chat, 15/10 s global, `+N`; cap 8/32, ring 8 MiB, kill TERM→KILL. | Hook `SetOnExit` ke emitter UI/gateway: **N/A F5**, target F8; watch 3-strike opsional → F9. |

## Backlog ADR-078/ADR-086 berfase F5

| item | status | bukti |
| --- | --- | --- |
| AUD-050 | `LULUS` | SPEC §9.6 `read\|send\|approve`; DDL `mcp_serve_tokens` + `mcpservestore` (`TestGAPF5_SEC21`). |
| NEW2-19 | `LULUS` | `tools.ApprovalRequest.Origin` + `Validate` (`internal/tools/approval.go:45-56`), diteruskan `approval_bridge.go:51,64`; `mcpservestore` mem-parse/menolak `origin` pada inbox. |
| NEW2-20 | `LULUS` | `run` adalah origin headless (ADR-202; `cron_wire.go:139`), delegate mewarisi origin induk lewat `SessionView.Child`; kanban/batch belum ada di F5 dan tidak ada jalur origin interaktif. |
| NEW3-02 | `LULUS` (residu eksplisit) | Auto headless `execute_code` hanya dengan `--approval auto` + `code_auto_languages` + trust/taint/tulis; tidak ada `--unsafe-auto-code` terpisah, dan bahasa shell tidak pernah lewat jalur ini (`TestExecuteCodeApproval`, TRUST-MODEL §3). |

## Gap yang ditunda / N/A (di luar F5)

| item | status | fase target | alasan |
| --- | --- | --- | --- |
| F5-CODEINTEL-LANG (`repo_map`/`code_intel` JS/TS/Python) | `DITUNDA` | F6 | ADR-294 amandemen PR 2b: aset npm `tree-sitter-*.wasm` adalah side-module `dylink.0` yang tidak dapat dimuat wazero; tanpa npm/emscripten di VPS. `internal/codeintel/ts` hanya `{c,cpp}`, bahasa lain `ErrUnsupportedLanguage`. |
| F5-BACKGROUND-WINDOWS (`shell background:true`, registry `process_*`) | `DITUNDA` | F6 | ADR-297/300: registry butuh Job Object agar `Close` membunuh seluruh pohon proses; permukaan fail-closed eksplisit (`exec.BackgroundSupported=false`, `module_unavailable` membungkus `exec.ErrCapability`; dikunci `process_windows_test.go` + `builtins_background_windows_test.go`). |
| GAP-SEC-22 image Docker rilis | `DITUNDA` | F9 | ROADMAP F9 "Docker 1 stage" (distroless, user 65532, healthcheck, Trivy); backend tidak berubah. |
| Skills-hub / preset catalog fetch | `N/A` | F8 | ADR-306 amandemen: jalur provider/MCP tertutup; katalog skills-hub milik F8. |
| Surface diff pin di TUI/web | `N/A` | F8 | Web UI adalah deliverable F8; CLI `mcp review` sudah menampilkan diff `modified`/`added`. |
| `auth logout --provider|--all` + `setup --uninstall --purge-data` | `N/A` | F9 | UX/installer rilis; requirement revoke RFC 7009 + `failed` offline sudah `LULUS` (GAP-SEC-18). |
| Konfirmasi `trust=full` + doctor trust | `N/A` | F8 | UX doctor; trust default untrusted + non-read approval sudah `LULUS` (GAP-SEC-03). |
| Gambar MCP sebagai lampiran (`Attachments`) | `N/A` | F6 | Media/attachment adalah F6; 413 + stream terputus sudah `LULUS` (GAP-REL-14). |
| MCP session view + `execute_code` `Env.Session` | `N/A` | F8 | Seam modul host; isolasi sesi tools sudah `LULUS` (GAP-REL-18). |
| Hook notice UI + watch 3-strike proses | `N/A` | F8/F9 | Emitter UI F8; watch 3-strike opsional di F9. |

## Baris parity F5

- `docs/FEATURE-PARITY.md` memuat **201 baris berfase F5** dari 854 baris.
  ROADMAP §"Pemetaan parity → fase" dan `TestParityPhaseColumn` mengunci angka
  itu (F1 46, F2 59, F3 135, F4 54, **F5 201**, F6 86, F7 103, F8 148, F9 5;
  Σ baris penyebut gate 837). Angka "200" bukan angka yang dipakai repo;
  sumbernya 201.
- Kolom `status` parity **bukan** status implementasi: nilai yang sah hanya
  `planned`, `dropped-with-approval`, dan `external-bridge`. Seluruh **201
  baris F5 berstatus `planned`** (0 dropped, 0 external-bridge). Tidak ada
  nilai `covered`/`n/a` di kolom ini, jadi tidak ada baris F5 yang dapat
  direklasifikasi tanpa bukti dan tidak ada baris yang diubah oleh PR ini.
- Gate parity **837/837** baru ditegakkan di **F9** (ROADMAP F9 acceptance;
  ADR-054), bukan di acceptance F5. Acceptance F5 menegakkan 25 EC F5 dan 17
  GAP seperti tabel di atas.
- Baris F5 yang fiturnya sudah punya tes di main tetap `planned` sampai gate
  F9; daftar per-baris (tes acceptance + benchmark speed parity) dilampirkan
  saat penutupan F9 sesuai ADR-064 (baris selesai bila test acceptance lulus
  **dan** benchmark speed parity memenuhi target).

## Perintah bukti

```sh
PHASE=F5 make phase-acceptance            # 365d6fae: rc=0, 76/76 paket ok
PHASE=F6 make phase-acceptance            # commit ini: rc=0, 76/76 paket ok
make loc                                  # TOTAL 101.267 / 190.000
make size-check                           # full 31.776.930 B; minimal 21.999.778 B
```
