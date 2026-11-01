# F5 acceptance ledger

Acceptance F5 didefinisikan di [ROADMAP.md](ROADMAP.md) §F5
(`ROADMAP.md:170-179`) dan [SPEC.md](../SPEC.md) §22.1. Baris yang diminta
adalah **17 GAP berfase F5**: GAP-SEC-03, SEC-04, SEC-18, SEC-20, SEC-21,
SEC-22, SEC-23, SEC-24, SEC-25, REL-14, REL-18, COST-03, COST-10, PROV-01,
TOOL-04, TOOL-05, TOOL-08.

**Snapshot audit:** `origin/main` **`81d305f4`**, `PHASE` = `F5`.
Toolchain `go1.26.10 linux/amd64`, workstation (bukan mesin referensi Q14).
Hanya bukti yang ada di main yang dihitung `LULUS`; PR terbuka tidak dihitung.

**Aturan status.**

| status | arti |
| --- | --- |
| `LULUS` | Perilaku requirement dan tes acceptance ada di main, dijalankan pada snapshot ini, lulus. |
| `LULUS-SEBAGIAN` | Bagian perilaku yang dapat diuji offline lulus; sisa requirement belum ada di main dan dicantumkan. |
| `TERBUKA` | Requirement belum ada/belum lengkap di main; PR konkret dicantumkan. |
| `DITUNDA` | Di luar F5 dengan ADR + fase tujuan eksplisit. |

**PR verifikasi.** SEC: `test/f5-gap-sec`; REL+COST: `test/f5-gap-rel-cost`;
PROV+TOOL: `test/f5-gap-prov-tool`. Semua tes bernama `TestGAPF5_<ID>`.

## Ringkasan GAP fase F5

| GAP | status | tes | bukti | sisa kerja / PR |
| --- | --- | --- | --- | --- |
| GAP-SEC-03 | LULUS-SEBAGIAN | `TestGAPF5_SEC03`; `TestUntrustedToolDeniedByDefault`, `TestToolEffectDefaultsToNonIdempotent`, `TestApprove*` | `internal/mcp/config.go:14-91` (trust default untrusted), `policy.go:12-43` (non-read selalu approval, hint server tidak menurunkan gate), `client.go:221-266` (deny sebelum dispatch), `config/modules_mcp.go:52` (read_only per tool). Tes baru: 10 tool ber-`readOnlyHint:true` → 10/10 prompt, headless 10/10 `denied`, 0 dispatch. | MCP tool belum di-wire ke turn (`modules.Register` 0 di produksi), jadi matriks mode ask/auto-edit/auto belum diuji lewat core; `trust=full` belum butuh konfirmasi eksplisit dan belum tampil di `doctor`. PR: wire modul MCP + mode matrix + trust confirmation/doctor. |
| GAP-SEC-04 | LULUS-SEBAGIAN | `TestGAPF5_SEC04`, `TestPin*` (6 tes), `TestMCPRugPull` belum ada dengan nama itu | `internal/mcp/pin.go:120-235` (digest sha256, first-listing pin, modified/added/removed quarantine), `pinGate`, `ctxdb/mcpstore`, `mcp refresh/review`. Tes baru: deskripsi berubah + tool baru di tengah sesi → 0 tool terlihat, 0 dispatch (`mcp_tool_changed`), diff `modified`/`added`. | Diff hanya di `damnbot mcp review`; TUI/web belum menampilkan diff. PR: surface diff TUI/web. |
| GAP-SEC-18 | LULUS-SEBAGIAN | `TestGAPF5_SEC18`, `TestOAuthRefreshRevocation`, `TestAuthLoginStatusLogoutRevoke`, `TestAuthRevokeFailureKeepsToken`, `TestAuthOfflineFails` | `internal/provider/auth/oauth.go:227-252` (RFC 7009 refresh lalu access), `cmd/damnbot/auth_cli.go:125-160` (revoke dulu, state `revoked/unsupported/failed`), `mcp_oauth.go:139-162`. Tes baru: keduanya dicabut, refresh → `invalid_grant`, offline → `ErrOffline`. | CLI hanya positional satu provider: `--provider P|--all` tidak ada; `damnbot setup --uninstall --purge-data` belum ada sehingga laporan `failed` per token tanpa jaringan belum bisa diuji. PR: flag logout + uninstall purge. |
| GAP-SEC-20 | LULUS-SEBAGIAN | `TestGAPF5_SEC20*` (verifier), `TestGAPF5_SEC20CLI`, `TestGAPF5_SEC20ConfigKey` | `internal/mcp/catalog` (manifest `{kind,name,version,serial,entries,signature}`, ed25519 wajib, tamper/unsigned/rollback fail-closed, entry commit+sha256), `cmd/damnbot/mcp_catalog_cli.go` (`mcp catalog <file>`), `[update] catalog_public_key` divalidasi. | Fetch live + redirect max 3 same-host + penyimpanan serial lintas proses belum di-wire; verifier dan CLI sudah fail-closed. |
| GAP-SEC-21 | LULUS-SEBAGIAN | `TestGAPF5_SEC21`, `TestRespondRefusesSelfApprovalAndP26`, `TestRespondAndOpenApprovals`, `TestServe*` (9 tes) | `ctxdb/mcpservestore` (token scope read/send/approve, approval_response exactly-once, origin≠mcp_serve, P26), `internal/mcp/serve.go` (scope filter, rate limit, approval non-read). Tes baru: 3 scope terikat profil, respond kedua `ErrNotPending`, self-origin/P26 `ErrForbidden`. | `damnbot mcp serve` belum ada (tidak ada ServeTool, token create/ls/revoke, `messages_send`, `permissions_list_open/respond`); scope token tidak diperiksa saat `Respond` (caller harus). Test tertunda `TestMCPServeCannotSelfApprove` belum ada dengan nama itu. PR: serve command + tools + scope check. |
| GAP-SEC-22 | DITUNDA-F9 | `TestDockerRunContract`, `TestDockerRejectsEscapes`, `TestDockerBackendNoSocketMount` (nama acceptance; perilaku ada) | `internal/exec/backends/docker.go:142-160,267-292` (hanya bind workspace; docker.sock/workspace socket ditolak), `cli_test.go:108,119`. | Image `ghcr.io/damnbot/damnbot` distroless/user 65532/healthcheck/Trivy adalah artefak rilis F9 (ROADMAP F9 “Docker 1 stage”); tidak ada kode backend yang perlu diubah. |
| GAP-SEC-23 | LULUS | `TestGAPF5_SEC23DockerProbe`, `TestGAPF5_SEC23SingularityProbe`, `TestSSHHostKeyPin`, `TestDockerRunContract` | `docker.go:218-255` (`docker create` dengan limit sebelum run; gagal → `ErrCapability` “no_resource_limits”), `singularity.go:140-174` (`cgroup status` sebelum run), cache `sync.Once`, `allow_unlimited` satu-satunya opt-out; SSH host-key pin dan egress allowlist sudah ada. | — |
| GAP-SEC-24 | LULUS | `TestGAPF5_SEC24`, `TestOverrideWithoutFlag` | `cmd/damnbot/provider_resolve.go:115-135` (override=true wajib), `:143-161` (`--confirm-base-url`), `:272-300` (`providerDoctorWarnings`), `config/config.go:246-248`. Tes baru: 2/2 override + non-default muncul di doctor; shadow tanpa flag gagal; base_url non-default butuh konfirmasi. | — |
| GAP-SEC-25 | LULUS | `TestGAPF5_SEC25`, `TestUserAgentDefault`, `TestNoAttributionHeaders` | `provider_resolve.go:305-410` (`send_attribution` default false, `userAgent()`), `provider/client.go:25-28,103-126,413-415` (UA `damnbot/<versi>`). Tes baru: 94 preset (88+6 lokal) tanpa header atribusi; UA tanpa OS/hostname; opt-in hanya header deklarasi. | — |
| GAP-REL-14 | LULUS-SEBAGIAN | `TestGAPF5_REL14`, `internal/gateway/render/known_defects_test.go` (10 tes) | `internal/provider` (413 typed, truncated stream `stream_broken` retry), `netx/retry/class.go:122`; render known-defects 10 skenario. | Skenario MCP gambar sebagai lampiran belum ada (`mcp.Client.mapResult` menaruh non-text di `Meta["content"]`, `host.Result` tidak punya `Attachments`); streaming-once/kanban bukan F5. Job `known-defects` belum ada. PR: attachments MCP + job. |
| GAP-REL-18 | LULUS-SEBAGIAN | `TestGAPF5_REL18`, `TestSessionToolIsolation`, `TestSessionViewFailClosed`, `TestSessionViewChildCannotEscalate` | `internal/tools/session_view.go`, `modules/host/session.go` (view eksplisit, zero fail-closed), `delegate` meneruskan `tools.SessionView`, `TestSessionToolIsolation` 1.000 iterasi `-race`. | `internal/mcp.Client` tidak menerima/memeriksa `SessionView`/`CapMCP`; `execute_code` belum ada. PR: MCP session view + execute_code. |
| GAP-COST-03 | LULUS | `TestGAPF5_COST03`, `TestCacheTTLByOrigin`, `TestUsageCacheByOrigin` | `provider/cachettl.go` (1h tui/acp/web/gateway, clamp `CacheTTLMax`), `provider/openai.go` encode ttl, `catalog.go:99,143`, `cmd/damnbot/usage_cli.go` (`--cache --by origin`). Tes baru: 9 origin × clamp. | Live replay Telegram `cached/input ≥70%` belum diukur (butuh provider nyata). |
| GAP-COST-10 | TERBUKA | — | Tidak ada `execute_code`, tidak ada `go.starlark.net` di kode (hanya `deps.allow:26`). | Tool `execute_code` + interpreter Starlark/RPC allowlist 7 tool + `network=deny` + approval ADR-089. PR: execute_code (G2-02). |
| GAP-PROV-01 | LULUS | `TestGAPF5_PROV01`, `TestCarrier*` (≥ 20 tes), `TestCarrierSignatureReplay`, `cmd/damnbot/carriers_wire_test.go` | `provider/carriers.go` (capture byte-identik, project hanya family+model asal), 4 adapter (`openai-chat`, `responses`, `anthropic`, `gemini`), `loop/turn.go:198-210` (event `carrier_dropped`), `ctxdb/carrierstore`. Tes baru: 4 kind (thought_signature/thinking/redacted_thinking/encrypted) di 4 adapter, replay hanya route asal, drop `route_mismatch`. | — |
| GAP-TOOL-04 | LULUS | `TestGAPF5_TOOL04` (Windows), `TestSSHPoolReuse`, `TestShellChoiceDrivesDialect`, `TestSSHContract` | `exec/backends/ssh.go:299-395` (pool per user/host/port, keepalive, idle close), `shell_windows.go` (pwsh→powershell→cmd, tanpa bash), `proc_windows.go` (Job Object). Test baru (runner Windows): PATH berisi bash.exe tetapi tanpa pwsh/powershell → cmd, bukan bash. | — |
| GAP-TOOL-05 | LULUS | `TestGAPF5_TOOL05`, `TestLeaseConflictDetected`, `TestWorktreeCleanup`, `TestCodetoolsLeaseGuard`, `TestWorktreeLeaseIsExclusive` | `delegate/lease.go` (lease per path, all-or-nothing, pemilik di error), `delegate/worktree.go` + `taskWorktree` (clean dihapus, kotor disimpan + `files_changed`), `codetools.LeaseGuard`. Tes baru: 3 task file sama → 1 jalan, 2 `lease_conflict` dengan pemilik; worktree kotor dilaporkan. | — |
| GAP-TOOL-08 | TERBUKA | — | Tidak ada background process/`process_*`; `shell{background}` masih F1 `invalid_args`; tidak ada limiter notice `≤1/15s`, `≤4/menit/chat`, global `≤15/10s`/`+N`. | Registry proses + `process_*` + limiter notice + 3-strike watch. PR: process background (G2-02). |

## Backlog ADR-078/ADR-086 berfase F5

| item | isi | status di main |
| --- | --- | --- |
| AUD-050 | SPEC §9.6 scope token `read\|send` → `read\|send\|approve` | **LULUS**: `SPEC.md:894` kini `read|send|approve`; DDL `mcp_serve_tokens` dan `mcpservestore` sudah `approve` (`TestGAPF5_SEC21`). |
| NEW2-19 | field `origin` di payload `approval_request` | **LULUS-SEBAGIAN / BLOKIR**: `tools.ApprovalRequest.Origin` ada (`internal/tools/approval.go:133`), `approval_bridge.go:51,64` meneruskannya; nilai `mcp_serve` belum bisa diuji karena `damnbot mcp serve` belum ada (GAP-SEC-21). |
| NEW2-20 | mapping origin cron/batch/kanban → `headless`, delegate → origin induk | **BLOKIR**: `cron_wire.go:139` masih `approvalOrigin: "run"`, dan `tools.ValidOrigin` hanya `run|tui|acp|plan`; perlu perluasan origin + update semua pembuat approval (bukan sekadar test). |
| NEW3-02 | auto headless `execute_code` butuh `--unsafe-auto-shell`/`--unsafe-auto-code` atau residu eksplisit | **BLOKIR**: menunggu `execute_code` (#573–#579 belum merge); belum ada flag/residu di TRUST-MODEL. |

## 1. GAP SEC

| GAP | requirement (ringkas) | status | tes | bukti |
| --- | --- | --- | --- | --- |
| SEC-03 | trust default untrusted; tool non-read selalu approval; read_only satu-satunya pengecualian; headless deny | LULUS-SEBAGIAN | `TestGAPF5_SEC03` | `internal/mcp/policy.go`, `config.go`, `client.go:221`; 10/10 prompt, 10/10 denied |
| SEC-04 | pin sha256; ubah/tambah tool → quarantine, 0 dispatch, diff | LULUS-SEBAGIAN | `TestGAPF5_SEC04` | `internal/mcp/pin.go:120`; `modified`/`added`, `mcp_tool_changed` |
| SEC-18 | revoke RFC 7009 refresh lalu access; `failed` tanpa jaringan | LULUS-SEBAGIAN | `TestGAPF5_SEC18` | `auth/oauth.go:227`; `invalid_grant`, `ErrOffline` |
| SEC-20 | katalog bertanda tangan ed25519 + anti-rollback | LULUS-SEBAGIAN | `TestGAPF5_SEC20*`, `TestGAPF5_SEC20CLI`, `TestGAPF5_SEC20ConfigKey` | `internal/mcp/catalog`; signature wajib/tamper/rollback fail-closed; `mcp catalog <file>`; key `[update] catalog_public_key` |
| SEC-21 | token scope + approval lintas proses; tidak menyetujui diri sendiri/P26 | LULUS-SEBAGIAN | `TestGAPF5_SEC21` | `ctxdb/mcpservestore`; exactly-once, origin, P26 |
| SEC-22 | image distroless non-root; backend docker tidak mount socket | DITUNDA-F9 | `TestDockerRunContract`/`TestDockerRejectsEscapes` | `backends/docker.go:142,267`; image F9 |
| SEC-23 | limit container fail-closed; host key pin; egress allowlist | LULUS | `TestGAPF5_SEC23DockerProbe`, `TestGAPF5_SEC23SingularityProbe`, `TestSSHHostKeyPin` | `docker.go:218`, `singularity.go:140`; probe sebelum run, `no_resource_limits` |
| SEC-24 | override built-in butuh `override=true`; base_url butuh konfirmasi; doctor 100% | LULUS | `TestGAPF5_SEC24` | `provider_resolve.go:115,143,272` |
| SEC-25 | atribusi opt-in; UA `damnbot/<versi>` | LULUS | `TestGAPF5_SEC25` | `provider_resolve.go:305,390`; 94 preset |

## 2. GAP REL + COST

| GAP | requirement (ringkas) | status | tes | bukti |
| --- | --- | --- | --- | --- |
| REL-14 | 413 eksplisit, stream terputus bukan jawaban, gambar MCP lampiran | LULUS-SEBAGIAN | `TestGAPF5_REL14` + `internal/gateway/render/known_defects_test.go` | `provider/client.go` menolak 413 non-retryable sebagai `provider.Error`; cut stream → `stream_broken` × `MaxAttempts`; gambar MCP tetap di `Meta["content"]`, belum `Attachments`; job `known-defects` belum ada |
| REL-18 | SessionView eksplisit, nil fail-closed, 2 sesi paralel terisolasi | LULUS-SEBAGIAN | `TestGAPF5_REL18`, `TestSessionToolIsolation`, `TestSessionViewFailClosed` | `modules/host/session.go`, `tools/session_view.go`; zero view → `internal`, `CapUserMemory` hilang → `denied`; MCP/`execute_code` belum memakai view |
| COST-03 | 1h untuk origin interaktif, 5m batch, clamp `cache_ttl_max`, usage per origin | LULUS | `TestGAPF5_COST03`, `TestCacheTTLByOrigin`, `TestUsageCacheByOrigin` | `provider/cachettl.go` 9 origin × clamp; `usage --cache --by origin`; hit-rate live ≥70% belum diukur |

Tes COST-10 tidak dibuat: `execute_code`/Starlark tidak ada di main (hanya
`go.starlark.net` di `deps.allow:26`), jadi tidak ada perilaku yang bisa diuji
tanpa tes palsu. Nama tes acceptance yang belum ada sampai #573–#579 merge:
`TestExecuteCodeApproval`, `TestExecuteCodeRPCNetworkDeny`, dan tes
`tool(name, **args)` allowlist 7 tool. Status tetap `TERBUKA`; PR
`execute_code` (G2-02) di atas.

## 3. GAP PROV + TOOL

| GAP | requirement (ringkas) | status | tes | bukti |
| --- | --- | --- | --- | --- |
| PROV-01 | carrier opaque 4 keluarga, byte-identik, replay hanya route asal, drop + notice | LULUS | `TestGAPF5_PROV01`, `TestGAPF5_PROV01Kinds`, `TestCarrier*` | `adapter.Encode` memuat carrier persis untuk family+model asal; model lain → `route_mismatch`, 0 byte carrier; `loop/turn.go:198-210` menulis `carrier_dropped` |
| TOOL-04 | Windows tanpa bash; pool SSH per user/host/port | LULUS | `TestGAPF5_TOOL04` (Windows), `TestSSHPoolReuse`, `TestSSHContract` | `shell_windows.go` hanya pwsh→powershell→cmd; `ssh.go:299-395` 20 run → 1 koneksi TCP, keepalive 30 s, idle 10 m |
| TOOL-05 | lease `tasks[].files`, worktree bersih dihapus/kotor dilaporkan | LULUS | `TestGAPF5_TOOL05`, `TestLeaseConflictDetected`, `TestWorktreeCleanup` | 3 child file sama → 1 jalan, 2 `lease_conflict` pemilik `P-d1`; worktree kotor + `files_changed` dipertahankan |
| TOOL-08 | rate-limit notice proses ≤1/15 s, ≤4/menit/chat, global ≤15/10 s, `+N` | TERBUKA | — | Tidak ada background process/`process_*`; `shell{background}` masih `invalid_args` F1; tidak ada limiter |

Tes TOOL-08 tidak dibuat karena perilakunya belum ada (bukan sekadar belum
diuji). Nama tes acceptance yang belum ada: `TestProcessNotifyRateLimit`.
PR: registry proses + `process_*` + limiter notice pada gelombang
`execute_code` (#573–#579 belum merge ke a8258e8a).
