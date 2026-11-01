# F5 acceptance — status EC dan named test

Dokumen ini mencatat item acceptance F5 yang belum `covered` agar keputusan tidak
diam-diam. Status `covered` ditegakkan lewat `phase.Gate.Item(t, id, "F5", "covered")`
di paket test (tidak ada `edgecases.json` di repo).

| ID | Status | Komponen yang kurang | Pemilik / PR | Catatan |
|---|---|---|---|---|
| EC-FIO-003 | covered | — | #585 | cwd host di luar mount ditolak backend container sebelum CLI |
| EC-TCALL-004 | covered | — | #586 | alias `call_id`/`id`/`item_id`, prioritas `call_id` > `id` > `item_id` |
| EC-PROV-003 | open | korelasi item ID yang berotasi di `provider/responses` | provider owner | perlu tabel event stream |
| EC-PROV-014 | open — audit | tidak ada jalur refresh token di layer provider: `retry.ClassifyStatus` memetakan 401/403 ke `Auth` (non-retry), jadi 403 berhenti (sesuai EC) dan 401 juga tidak me-refresh. Refresh-401 hanya ada di MCP (`TestOAuthRefreshOn401AndBearerHeader`). Bila SPEC menuntut refresh provider, komponen baru diperlukan. | provider owner | jangan tambah retry diam-diam; keputusan spec |
| EC-PROV-023 | n/a | `provider.NormalizeBaseURL` sengaja tidak menambah `/v1` untuk host loopback bare. SPEC §4.1 ("adapter wire") dan §12 mendefinisikan `base_url` sebagai URL penuh; tidak ada satu pun kalimat yang menyatakan bare loopback host diberi `/v1`, jadi aturan itu tidak boleh dikarang. | Main | n/a dengan alasan tertulis; bila SPEC kelak menambah aturan, item dibuka kembali |
| EC-PROV-027 | covered | — | #592 | kapabilitas katalog `cache` → `Route.CacheMarkers`; marker hanya ke route yang mendukung, provider lain tidak menerima field asing |
| EC-SEC-004 | open — audit | tidak ada host-pin bearer OAuth di layer provider API; token OAuth dipakai oleh issuer/login dan MCP server terkonfigurasi. Belum ada mekanisme menolak bearer saat redirect/host asing untuk provider API. | provider owner | perlu komponen host-pin |
| EC-PROV-002 | blocked | checkpoint/resume stream Gemini | provider owner | |
| EC-PROV-017/020/021/022/026 | blocked | credential pool SPEC §4.1 | worker baru | |
| EC-FIO-010 | blocked | codeintel/LSP UTF-16 | exec-backends | |
| EC-TERM-001 | blocked | background process registry | exec-backends | |
| EC-TERM-003 | blocked | peringatan sandbox diganti | exec-backends | |
| EC-TCALL-011 | blocked | konverter chat→Responses bersama | menyusul | |
| EC-SEC-002/003 | blocked | kebijakan kredensial eksternal | provider owner | |
| EC-WIN-007/008/015 | blocked | translator path docker Windows / peer token named pipe / pipe multiplexer | saya, setelah E merge | implementasi + tes Windows nyata |
| TestExecuteCodeRPCNetworkDeny | blocked | tool `execute_code` | F5 execute_code | |
| TestExecuteCodeApproval | blocked | tool `execute_code` | F5 execute_code | |
| TestLSPWorkspaceTrustGate | blocked | klien LSP | exec-backends | |
| TestMCPServeCannotSelfApprove | owned | — | mcp-oauth-b | |

Gate acceptance: `make phase-acceptance` = `DAMNBOT_PHASE_ACCEPTANCE=1 go test $(GOTAGS) ./...`.
Tidak boleh hijau selama item `blocked/open` di atas belum ditutup; ambang gate tidak diubah.

## Audit acceptance F5 (status per item, bukti harus ada di main)

| Item | Status | Bukti konkret (tes + PR + head) | Sisa kerja | Pemilik |
|---|---|---|---|---|
| Provider registry 88 + custom 12 field | partial | `TestProviderCatalog88`, `TestProvidersMDParity`, `TestCatalogRejects*` (registry); PR #558/#531 | replay fixture 88 provider dan smoke 10 provider nyata belum ada test/artefak | provider owner |
| MCP 65/65 `tools/list`, P1 ≤ 5.000 | pending | hanya tes klien/server MCP umum (`internal/mcp/*_test.go`); tidak ada katalog 65 server | harness 65 MCPX + hitung P1 | mcp-oauth-b |
| Smoke E1 10 task model murah | pending | tidak ada artefak bench F5 | jalankan harness E1 | Main |
| Exec backends docker/ssh/singularity/modal/daytona/vercel_sandbox | pending merge | #571 head 65266bb5: `TestBackendContract/{local,docker,singularity,vercel_sandbox}`, `TestSandboxProviders`, e2e PASS 758.187s | merge #571 | exec owner |
| EC-PROV 12 (002,003,014,017..023,026,027) | partial | PROV-027 #592; PROV-018 ada di main; PROV-003/014/023/017/020/021/022/026 tidak ada tes bernama | lihat tabel EC di atas | provider owner |
| EC-TCALL-004/011 | partial | TCALL-004 #586; TCALL-011 pending | converter chat→Responses | provider owner |
| EC-FIO-003/010 | partial | FIO-003 #585; FIO-010 blocked LSP | #585 merge; LSP | exec-backends |
| EC-TERM-001/003 | pending | tidak ada (registry background belum ada) | proses background + warning sandbox | exec-backends |
| EC-SEC-002..004 | partial | SEC-004 #590; SEC-002/003 pending | kebijakan kredensial eksternal | provider owner |
| EC-WIN-007/008/015 | partial | WIN-007 #591; WIN-008/015 dialihkan `win-final` | tes Windows CI | win-final |
| `TestMCPServeCannotSelfApprove` | pending | tidak ada di main | tulis tes | mcp-oauth-b |
| `TestExecuteCodeRPCNetworkDeny` / `TestExecuteCodeApproval` | pending | tool `execute_code` belum ada | komponen execute_code + tes | F5 execute_code owner |
| `TestLSPWorkspaceTrustGate` | pending | klien LSP belum ada | komponen LSP + tes | exec-backends |
| Backlog ADR-078/ADR-086 fase F5 | pending | belum ada bukti penutupan per item | audit per ADR | Main |
| GAP-SEC-03/04/18/20/21/23/24/25 | pending | tidak ada tes yang menyebut ID ini; sebagian perilaku ada (approval/secret/backends) | petakan ke tes bernama atau tulis tes | security owner |
| GAP-REL-18 | evidence | `GAP-REL-18` dirujuk di tes delegate/session (`grep` menemukan 1 file tes) | verifikasi acceptance penuh | delegate owner |
| GAP-COST-03 | evidence | 5 file tes menyebut GAP-COST-03 (cache TTL) | — | provider owner |
| GAP-TOOL-05 | evidence | 2 file tes menyebut GAP-TOOL-05 (lease) | — | delegate owner |
| GAP-COST-10 / GAP-TOOL-08 | pending | tidak ada tes bernama | Starlark RPC / repo map | F5 coding owner |
| 201 baris FEATURE-PARITY fase F5 | pending | tidak diverifikasi per baris; hanya kelompok modul yang punya tes | tabel per-baris dengan tes/PR | Main |

**Kesimpulan:** `make phase-acceptance` (PHASE=F5) belum bisa hijau. Minimal yang memblokir: merge #571/#585/#586/#590/#591/#592, katalog MCP 65, smoke provider/E1, `execute_code` + named tests, LSP, process_* + TERM ECs, EC-WIN-008/015, dan pemetaan GAP/parity per baris. Ambang gate tidak diubah.
