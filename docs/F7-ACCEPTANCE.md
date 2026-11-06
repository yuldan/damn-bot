# F7-ACCEPTANCE — Ledger acceptance F7 (platform lain, api-server, kanban)

Status: **ledger hidup** untuk `PHASE=F7` pada `origin/main` `8d482d4f`. Belum ada baris yang ditutup: seluruh 103 baris parity F7, 10 GAP, 9 EC, dan baris agregat masih `BELUM`. Satu baris hanya boleh berubah menjadi `LULUS` (atau `N/A` dengan alasan tertulis) pada PR yang benar-benar menambahkan tes bernama di kolom "Tes yang harus ada" dan tes itu lulus; tidak ada status yang diubah oleh dokumen saja.

Sumber normatif: ROADMAP F7 (docs/ROADMAP.md:196-204), 103 baris parity berfase F7 (docs/FEATURE-PARITY.md; semuanya `planned` kecuali `PLAT-025` bertanda `external-bridge (ADR-034)`), kontrak modul SPEC §3.2.4 (SPEC.md:299-302), 10 baris GAP F7 (SPEC.md:2929,2961,2966-2976; docs/ROADMAP.md:240), EC F7 (docs/ROADMAP.md:61,63,65,67; docs/EDGE-CASES-hermes.md:136,181-182,218-238,262), dan carryover F6 (docs/F6-ACCEPTANCE.md:40,46,48; ADR-323 §12). Nama paket dan baris budget mengikuti ADR-326 (docs/DECISIONS.md).

Aturan gate:

- **Penutupan (ADR-326, ADR-323).** Baris hanya `LULUS` bila PR yang sama menambah tes bernama, mengubah baris parity F7 menjadi `LULUS`, dan mengubah baris ledger `BELUM`→`LULUS`. Baris yang tidak dapat dibuktikan ditulis `N/A — <alasan teknis> (carryover F8)`, bukan `BELUM`. Baris agregat hanya `LULUS` setelah semua anggota tertutup.
- **Gate mesin.** Selama `PHASE=F7` gate tidak aktif (ledger hidup). Begitu `PHASE` > F7, `TestF7LedgerClosed` menolak `BELUM`, `N/A` telanjang (< 8 karakter alasan), dan baris parity F7 yang bukan `LULUS`/`N/A — …`; `TestParityPhaseColumn` memeriksa hal yang sama dari sisi parity. `TestF7LedgerGateRules` adalah kontrol negatifnya.
- **Status parity `external-bridge (ADR-034)` (PLAT-025) bukan penutupan.** Gate F7 memperlakukannya sebagai belum tertutup; PR yang membuktikan adapter bridge (fake bridge server, inbound-only, cold outbound ditolak) me-flip baris itu menjadi `LULUS` dengan catatan ADR-034 tetap.
- Tidak ada `t.Skip` runtime (REL-15). Setiap adapter/HTTP surface diuji dengan **fake server** (HTTP/WS/IMAP/SMTP) di `internal/gwplat/platformtest`; binary nyata hanya dipakai harness `internal/e2e` yang sudah ada. Smoke akun nyata berjalan nightly dan tidak menggantikan fake server.
- Nama tes bersifat target, bukan implementasi. Nama yang sudah diwajibkan SPEC dipertahankan apa adanya: `TestIRCAccountAuth`, `TestIdentityStrengthTable`, `TestWhatsAppWebGuards`, `TestSetupNoWebWithoutAck`, `TestBridgeProtocolConformance`, `TestGroupMentionPolicy`, `FuzzRenderTelegram`, `TestKnownDefect*` (internal/gateway/render/known_defects_test.go:12-83).

## 1. Acceptance ROADMAP F7

| Item ROADMAP F7 | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| PLAT 30/30 fake server | 29 baris PLAT berfase F7 `LULUS` (§2 dan §3) + PLAT-022 (Telegram, F4) yang sudah berjalan; setiap adapter menjawab fake server, 0 network tanpa config | §2/§3 `LULUS` + `TestPlatformAdapterContract` + `TestF7PlatformIdentityTable` | BELUM |
| Smoke nyata nightly | adapter yang punya akun uji lulus smoke nightly; adapter tanpa akun → `N/A — no test account (nightly smoke)` | job `nightly` + `TestF7NightlySmokeMatrix` (daftar adapter × status akun) | BELUM |
| EC-PLAT non-Telegram (5) | EC-PLAT-004/005/006/008/024 `covered` di katalog EC | §10 baris EC-PLAT-* | BELUM |
| EC-STATE/TERM/SEC F7 | EC-STATE-004, EC-TERM-004, EC-TERM-005, EC-SEC-006 `covered` | §10 baris EC-STATE-004/EC-TERM-004/EC-TERM-005/EC-SEC-006 | BELUM |
| Budget modul & binary | `make loc` 0 GAGAL dengan baris F7 (ADR-326); P13 full ≤ 52.000.000 B dan minimal ≤ 22.100.000 B (ADR-325) | `TestLocBudgetModuleRows`, `TestBudgetTableSum`, `make size-check` di PR penutup | BELUM |
| Ledger F7 | 103 baris parity F7 + 10 GAP + 9 EC tertutup; `PHASE` → F8 hanya setelahnya | `TestF7LedgerClosed`, `TestParityPhaseColumn`, `TestF7LedgerGateRules` | BELUM |

Catatan **PLAT 30/30**: `docs/FEATURE-PARITY.md` memuat 30 baris PLAT; `PLAT-022` (Telegram) berfase F4 dan sudah berjalan (ADR-020), sehingga ledger F7 menagih 29 baris (26 adapter — termasuk `PLAT-025` WhatsApp Web bridge — ditambah tiga surface core gateway `PLAT-028/029/030`). `PLUG-024` (plugin platforms, 27 adapter) dihitung di §2.

## 2. Ledger parity F7 — PLAT adapter (26) + PLUG-024

Kontrak yang sama untuk semua baris: descriptor adapter di registry tertutup, `default-off`, aktif hanya dari config+secret eksplisit, tanpa autodetect env; identity strength dari `Caps.IdentityStrength` (SEC-10); outbound lewat `gateway/outq` (GW-03); renderer markdown platform (GW-04); mention policy core (GW-05); tidak ada `time.Sleep` inline. Tes kontrak bersama: `TestPlatformAdapterContract` (satu subtest per adapter) + `TestF7PLAT<nnn><Nama>` per baris.

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| PLAT-001 | A2A | server inbound A2A v1.0 (task/send, task/get) + agent card; adapter default-off; card absen → error typed | `TestF7PLAT001A2A`, `TestF7A2AAgentCard` | BELUM | §8 tools |
| PLAT-002 | BlueBubbles | kirim REST + terima webhook; password hanya dari secret; URL wajib https atau loopback | `TestF7PLAT002BlueBubbles` | BELUM | server macOS, fake HTTP |
| PLAT-003 | Buzz (Nostr relay) | subscribe/publish relay WS; private key hanya dari secret; 0 key di log/error | `TestF7PLAT003Buzz` | LULUS | — |
| PLAT-004 | DingTalk Stream | WS client id/secret; ack ≤ 3 s; dedupe `msgId` (replay 2× → 1 turn) | `TestF7PLAT004DingTalk`, `TestF7DingTalkDedupe` | BELUM | — |
| PLAT-005 | Discord | Gateway WS + REST; intents minimal; thread/DM mapping; slash command; 4004 → needs_attention (GW-11) | `TestF7PLAT005Discord`, `TestF7DiscordIntents` | BELUM | voice MEDIA-029 |
| PLAT-006 | Email | IMAP polling + SMTP reply; MIME multipart; dedupe `Message-ID`; BODY.PEEK (GW-08) | `TestF7PLAT006Email` | BELUM | GAP-GW-08 |
| PLAT-007 | Feishu/Lark | WS long connection + webhook fallback; tenant token cache; dedupe `event_id` | `TestF7PLAT007Feishu` | BELUM | — |
| PLAT-008 | Google Chat | callback HTTP terverifikasi (JWT Google) atau Pub/Sub pull; payload tak terverifikasi → 0 turn | `TestF7PLAT008GoogleChat`, `TestF7GoogleChatVerify` | BELUM | — |
| PLAT-009 | IRC | wajib SASL PLAIN/EXTERNAL + CAP `account-notify`/`account-tag`; tanpa itu `module_unavailable{irc_no_account_auth}` | `TestF7PLAT009IRC`, `TestIRCAccountAuth` | LULUS | GAP-SEC-10 |
| PLAT-010 | LINE | webhook HMAC-SHA256 constant-time; reply token sekali pakai | `TestF7PLAT010LINE`, `TestF7LINEHMAC` | BELUM | — |
| PLAT-011 | Matrix | homeserver + access token; E2EE pure Go (`goolm`); edit-stream default mati (GW-10) | `TestF7PLAT011Matrix`, `TestF7MatrixNoEditStream` | BELUM | budget `gwmatrix` |
| PLAT-012 | Mattermost | REST v4 + WebSocket; token dari secret; reconnect backoff netx | `TestF7PLAT012Mattermost` | BELUM | — |
| PLAT-013 | ntfy | subscribe topic + publish; topic dari config; isi pesan tidak masuk log INFO | `TestF7PLAT013Ntfy` | LULUS | — |
| PLAT-014 | Photon (iMessage) | sidecar HTTP; project id/secret; sidecar absen → `module_unavailable` 0 network | `TestF7PLAT014Photon` | BELUM | — |
| PLAT-015 | QQ Bot | API v2 WS + REST; refresh token; dedupe event | `TestF7PLAT015QQBot` | BELUM | — |
| PLAT-016 | Raft | wake-channel bridge lokal; `RAFT_PROFILE`; bridge absen → pesan instalasi | `TestF7PLAT016Raft` | LULUS | — |
| PLAT-017 | Signal | daemon signal-cli (SSE + JSON-RPC); account → session key; plaintext tidak dipersist | `TestF7PLAT017Signal` | LULUS | — |
| PLAT-018 | SimpleX | daemon WS lokal; reconnect; 0 tulis plaintext | `TestF7PLAT018Simplex` | LULUS | — |
| PLAT-019 | Slack | Socket Mode; ack < 3 s; retry envelope didedupe; mrkdwn renderer | `TestF7PLAT019Slack`, `TestF7SlackAck` | BELUM | renderer GW-04 |
| PLAT-020 | SMS (Twilio) | REST send + webhook signature; segmen GSM-7/UCS-2 (GW-09) | `TestF7PLAT020SMS`, `TestF7GAPGW09SMSSegments` | BELUM | GAP-GW-09 |
| PLAT-021 | Microsoft Teams | Bot Framework JWT (issuer/audience/exp) divalidasi; activity dedupe; tenant allowlist | `TestF7PLAT021Teams`, `TestF7TeamsJWT` | BELUM | — |
| PLAT-023 | WeCom | smart robot WS + callback app; XML parser hardened (tanpa entity eksternal) | `TestF7PLAT023WeCom`, `TestF7WeComXML` | BELUM | dua surface |
| PLAT-024 | Weixin/WeChat | iLink long-poll; kursor persist; audio SILK → MEDIA-031 | `TestF7PLAT024Weixin` | BELUM | — |
| PLAT-025 | WhatsApp Web (bridge) | klien `damnbot.bridge/v1`; inbound-only; `send_message` cold ke JID tanpa inbound → `cold_outbound_denied` 0 request; ack ban risk tunggal; `health.reason` dipetakan ke breaker | `TestWhatsAppWebGuards`, `TestBridgeProtocolConformance`, `TestF7PLAT025WABridge` | BELUM | parity `external-bridge (ADR-034)` wajib di-flip `LULUS`; GAP-GW-01/11 |
| PLAT-026 | WhatsApp Cloud | API resmi Meta; verify token + HMAC `X-Hub-Signature-256`; jendela 24 jam dihormati | `TestF7PLAT026WhatsAppCloud`, `TestF7WACloudSignature` | BELUM | satu-satunya mode wizard |
| PLAT-027 | Yuanbao | WS bot app id/secret; grup/DM mapping | `TestF7PLAT027Yuanbao` | BELUM | §8 tools |
| PLUG-024 | Plugin: platforms | 27 descriptor adapter terdaftar, semuanya `default-off`, aktivasi eksplisit, 0 autodetect env | `TestF7PLUG024PlatformPlugins`, `TestF7PlatformRegistryClosed` | BELUM | kontrol: nama asing ditolak |

## 3. Ledger parity F7 — api_server, webhook, subscriptions, MS Graph

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| PLAT-028 | `api_server` core gateway | `/v1/chat/completions`, `/v1/responses`, `/v1/runs` OpenAI-compatible; SSE stream; bearer token per klien; payload > batas → 413 eksplisit (bukan potong diam); 0 turn tanpa token | `TestF7PLAT028APIServer`, `TestF7APIStream413`, `TestF7PLAT028Responses`, `TestF7PLAT028Runs` | LULUS | REL-14; responses/runs #741 |
| UI-021 | OpenAI-compatible API server | klien eksternal (SDK openai) bicara ke `/v1`; `model` dipetakan ke profil/route; usage/ledger tercatat | `TestF7UI021OpenAICompat`, `TestF7UIOpenAISDK` | LULUS | surface sama dengan PLAT-028 (#741) |
| PLAT-029 | `webhook` core gateway | penerima webhook generik; secret bersama; dedupe `delivery_id`; toolset webhook hanya web/vision/clarify | `TestF7PLAT029Webhook`, `TestF7WebhookToolsetSafe` | BELUM | EC-SEC-006 |
| GW-011 | Webhook platform & subscriptions | subscription dinamis (subscribe/list/remove/test) persisten; signature keluar; retry berbatas; test kirim 1 event bertanda | `TestF7GW011Subscriptions`, `TestF7GW011SubscriptionRetry` | BELUM | CLI-026 |
| GW-012 | MS Graph webhook | handshake `validationToken` (echo ≤ 10 s); `clientState` dicocokkan; dedupe `resource`+`changeType`; notifikasi asing ditolak | `TestF7GW012MSGraph`, `TestF7MSGraphClientState` | BELUM | PLAT-030 |
| PLAT-030 | `msgraph_webhook` core gateway | subscription Graph dibuat/diperbarui (renew sebelum expiry) dan notifikasi diteruskan ke Teams pipeline | `TestF7PLAT030MSGraphRenew` | BELUM | — |
| CLI-026 | `damnbot webhook` | empat sub: `subscribe`, `list`, `remove`, `test`; `test` mengirim tepat 1 event; 0 kredensial di output | `TestF7CLI026Webhook` | BELUM | — |
| TSET-042 | bundle `hermes-api-server` | komposit tanpa tool registry sendiri; komposisi toolset aman untuk klien API | `TestF7TSET042APIServerBundle` | LULUS | — |
| TSET-061 | bundle `hermes-webhook` | komposit; hanya toolset aman (web/vision/clarify), 0 shell/write | `TestF7TSET061WebhookBundle`, `TestF7WebhookToolsetSafe` | BELUM | EC-SEC-006 |

## 4. Ledger parity F7 — peer, hosted rooms, relay, profile routing, scale-to-zero

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| CLI-027 | `damnbot peer` | tujuh sub: `add`, `list`, `remove`, `dm`, `run`, `status`, `stop`; `dm` mengirim ke peer terdaftar; peer asing ditolak | `TestF7CLI027Peer` | LULUS | — |
| GW-013 | Peer gateways | DM bot-ke-bot lintas mesin; envelope bertanda tangan; replay ditolak; identitas peer `cryptographic` | `TestF7GW013PeerDM`, `TestF7PeerReplay` | LULUS | SEC-10 |
| GW-014 | Hosted rooms | ruang multi-agent; urutan turn per room; biaya dipisah per agen; anggota asing tidak bisa join | `TestF7GW014HostedRooms` | LULUS | TSET-036 |
| GW-015 | Bot mode / relay | mode bot DM + relay antar profil; loop dicegah (maks 2 hop); relay ke profil non-allowlist ditolak | `TestF7GW015BotRelay`, `TestF7RelayLoopGuard` | LULUS | — |
| GW-016 | Profile routing / multiplex | satu gateway melayani banyak profil; routing per bot/identitas; EC-STATE-004 (koneksi read-pool berbatas); EC-TERM-004/005 (sandbox & policy per profil, fail-closed) | `TestF7GW016ProfileRouting`, `TestEdgeCases/EC-STATE-004`, `TestEdgeCases/EC-TERM-004`, `TestEdgeCases/EC-TERM-005` | LULUS | EC §10 |
| GW-022 | Scale-to-zero | idle > ambang → proses gateway berhenti bersih; pesan masuk pertama membangunkan tanpa kehilangan/duplikasi; 0 pesan diparkir hilang | `TestF7GW022ScaleToZero`, `TestF7ScaleToZeroWakeNoLoss` | LULUS | — |
| GW-023 | Channel directory | direktori channel lintas platform; 0 token/secret; entri hilang → baris kosong bukan panic | `TestF7GW023ChannelDirectory` | LULUS | — |
| GW-025 | Telegram managed bot | provisioning bot terkelola opt-in; tanpa kredensial → `module_unavailable`; 0 panggilan jaringan saat default | `TestF7GW025ManagedBot` | LULUS | default off |
| GW-026 | Handoff ke platform | serahkan sesi CLI ke platform chat; transcript ringkas; konfirmasi idempoten (retry 1× tidak menggandakan) | `TestF7GW026Handoff` | LULUS | SLASH-012 |
| SLASH-012 | `/handoff <platform>` | argumen platform divalidasi; tanpa adapter aktif → pesan, 0 turn; handoff tercatat di sesi | `TestF7SLASH012Handoff` | LULUS | — |
| EXEC-016 | Approval transport eksternal | approval dikirim ke transport (builtin/eksternal); transport gagal → `deny` (fail-closed); kelas P26 tetap butuh kanal interaktif | `TestF7EXEC016ApprovalTransport`, `TestF7ApprovalTransportFailClosed` | LULUS | TRUST-MODEL |
| TSET-036 | bundle `bot_room` | komposit; hanya tool room + tool inti read-only; 0 shell | `TestF7TSET036BotRoomBundle` | LULUS | — |

## 5. Ledger parity F7 — CLI adapter dan bundle toolset platform (18)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| CLI-015 | `damnbot whatsapp` | setup hanya menawarkan Cloud API; mode Web ditolak tanpa `--accept-ban-risk`; ack ditulis sekali + `system_notice{wa_ban_risk_ack}` | `TestSetupNoWebWithoutAck`, `TestF7CLI015WhatsApp` | BELUM | GAP-GW-01 |
| CLI-016 | `damnbot whatsapp-cloud` | setup phone-number id + access token dari secret; verifikasi webhook ditampilkan | `TestF7CLI016WhatsAppCloud` | BELUM | — |
| CLI-017 | `damnbot slack` | helper manifest (scopes/events) dihasilkan; manifest tidak memuat token | `TestF7CLI017SlackManifest` | BELUM | — |
| TSET-046 | `hermes-discord` | komposit platform Discord; 0 tool registry sendiri; hanya aktif bila adapter aktif | `TestF7TSET046DiscordBundle` | BELUM | — |
| TSET-047 | `hermes-whatsapp` | komposit; memuat toolset aman; tidak mengaktifkan cold outbound | `TestF7TSET047WhatsAppBundle` | BELUM | — |
| TSET-048 | `hermes-slack` | komposit; renderer mrkdwn aktif | `TestF7TSET048SlackBundle` | BELUM | — |
| TSET-049 | `hermes-signal` | komposit; adapter Signal wajib aktif | `TestF7TSET049SignalBundle` | BELUM | — |
| TSET-050 | `hermes-bluebubbles` | komposit; adapter BlueBubbles wajib aktif | `TestF7TSET050BlueBubblesBundle` | BELUM | — |
| TSET-051 | `hermes-email` | komposit; toolset aman; 0 tool shell | `TestF7TSET051EmailBundle` | BELUM | — |
| TSET-052 | `hermes-mattermost` | komposit; adapter Mattermost wajib aktif | `TestF7TSET052MattermostBundle` | BELUM | — |
| TSET-053 | `hermes-matrix` | komposit; edit-stream mati (GW-10) | `TestF7TSET053MatrixBundle` | BELUM | — |
| TSET-054 | `hermes-dingtalk` | komposit; adapter DingTalk wajib aktif | `TestF7TSET054DingTalkBundle` | BELUM | — |
| TSET-055 | `hermes-feishu` | komposit; adapter Feishu wajib aktif | `TestF7TSET055FeishuBundle` | BELUM | — |
| TSET-056 | `hermes-weixin` | komposit; SILK decode tersedia lewat MEDIA-031 | `TestF7TSET056WeixinBundle` | BELUM | — |
| TSET-057 | `hermes-qqbot` | komposit; adapter QQ wajib aktif | `TestF7TSET057QQBotBundle` | BELUM | — |
| TSET-058 | `hermes-wecom` | komposit; dua surface (robot WS + callback) | `TestF7TSET058WeComBundle` | BELUM | — |
| TSET-059 | `hermes-wecom-callback` | komposit callback; XML parser hardened | `TestF7TSET059WeComCallbackBundle` | BELUM | — |
| TSET-060 | `hermes-sms` | komposit; cap segmen (GW-09) berlaku | `TestF7TSET060SMSBundle` | BELUM | — |

## 6. Ledger parity F7 — media (2)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| MEDIA-029 | Discord voice channel | join/leave voice channel; PCM 48 kHz mixer; STT/TTS lewat pipeline F6; tanpa opus/ffmpeg → `module_unavailable` 0 join; barge-in menutup stream | `TestF7MEDIA029DiscordVoice`, `TestF7DiscordVoiceNoOpus` | BELUM | carryover F6 (F6-ACCEPTANCE §6) |
| MEDIA-031 | Silk audio (WeChat/QQ) | decode SILK/SILKv3 → PCM16 untuk STT; payload rusak → error typed, bukan crash; 0 dependency CGO | `TestF7MEDIA031Silk`, `TestF7SilkCorruptPayload` | BELUM | carryover F6 |

## 7. Ledger parity F7 — kanban (23)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| CLI-029 | `damnbot kanban` (45 sub) | sub inti (init, boards, create, list, show, assign, set-model, reclaim, reassign, diagnostics, link, unlink, claim, comment, attach, attachments, attach-rm, complete, edit, block, schedule, unblock, request-review, request-changes, reopen-review, promote, archive, tail, dispatch, daemon, watch, stats, notify-*, log, runs, heartbeat, assignees, context, specify, decompose, gc, repair, swarm) berjalan; sub tak dikenal → exit 2 | `TestF7CLI029Kanban`, `TestF7CLI029KanbanSubcommands` | LULUS | board SQLite |
| SLASH-080 | `/kanban` | subset sub gateway berjalan di chat; otorisasi owner untuk mutasi; output ≤ batas renderer | `TestF7SLASH080Kanban` | LULUS | — |
| TOOL-055 | `kanban_attach` | lampirkan file workspace; path di luar workspace ditolak; ukuran dibatasi | `TestF7TOOL055KanbanAttach` | LULUS | — |
| TOOL-056 | `kanban_attach_url` | lampirkan URL https; URL privat/loopback ditolak sebelum fetch | `TestF7TOOL056KanbanAttachURL` | LULUS | SSRF |
| TOOL-057 | `kanban_attachments` | daftar lampiran + ukuran; tanpa akses → kosong, bukan error bocor | `TestF7TOOL057KanbanAttachments` | LULUS | — |
| TOOL-058 | `kanban_block` | tandai terblokir + alasan wajib; state machine menolak transisi ilegal | `TestF7TOOL058KanbanBlock` | LULUS | — |
| TOOL-059 | `kanban_comment` | komentar append-only; 0 edit riwayat | `TestF7TOOL059KanbanComment` | LULUS | — |
| TOOL-060 | `kanban_complete` | selesai dengan handoff terstruktur (file/commit/verify); biaya run ditagih 1× (REL-14) | `TestF7TOOL060KanbanComplete`, `TestF7KanbanCostOnce` | LULUS | — |
| TOOL-061 | `kanban_create` | buat task anak dengan dependency; idempoten pada kunci `(board, slug)` | `TestF7TOOL061KanbanCreate` | LULUS | — |
| TOOL-062 | `kanban_heartbeat` | heartbeat worker tercatat; timeout memicu `needs_attention` | `TestF7TOOL062KanbanHeartbeat` | LULUS | — |
| TOOL-063 | `kanban_link` | link dependensi; siklus ditolak | `TestF7TOOL063KanbanLink` | LULUS | — |
| TOOL-064 | `kanban_list` | daftar task board; hanya mode orkestrator | `TestF7TOOL064KanbanList` | LULUS | — |
| TOOL-065 | `kanban_request_changes` | kembalikan task review dengan alasan; transisi valid | `TestF7TOOL065KanbanRequestChanges` | LULUS | — |
| TOOL-066 | `kanban_request_review` | ajukan ke review; tidak bisa dari state tertutup | `TestF7TOOL066KanbanRequestReview` | LULUS | — |
| TOOL-067 | `kanban_schedule` | jadwalkan task (RFC 3339); jadwal lampau ditolak | `TestF7TOOL067KanbanSchedule` | LULUS | — |
| TOOL-068 | `kanban_show` | detail task + komentar + lampiran; field sensitif diredaksi | `TestF7TOOL068KanbanShow` | LULUS | — |
| TOOL-069 | `kanban_unblock` | buka blokir; hanya orkestrator; transisi valid | `TestF7TOOL069KanbanUnblock` | LULUS | — |
| TSET-020 | toolset `kanban` | 15 tool, default off, tersedia saat inventaris=false | `TestF7TSET020Kanban` | LULUS | — |
| AUTO-017 | Kanban dispatcher | dispatch task ke worker profil; `kanban.dispatch_in_gateway` default off; satu task satu worker (lease) | `TestF7AUTO017KanbanDispatch`, `TestF7KanbanDispatchLease` | LULUS | gateway tick; e2e `TestF7KanbanGatewayDispatchE2E` |
| AUTO-018 | Kanban auto-decompose | task besar dipecah via LLM auxiliary; `kanban.auto_decompose` default off; hasil decompose divalidasi schema | `TestF7AUTO018KanbanDecompose` | LULUS | plan tervalidasi; seam produksi `TestF7AUTO018KanbanDecomposeProd` |
| AUTO-019 | Kanban notifier/watchers | perubahan task → notifikasi gateway; `kanban.notify_in_gateway`; rate-limited 1/menit/task | `TestF7AUTO019KanbanNotify` | LULUS | outbox + renderer; e2e `TestF7KanbanGatewayNotifyE2E` |
| AUTO-020 | Kanban swarm/workflow/PR acceptance | alur multi-agent + penerimaan PR; opt-in; merge hanya setelah bukti verify | `TestF7AUTO020KanbanSwarm` | LULUS | — |
| PLUG-021 | Plugin kanban (dashboard, systemd) | data dashboard (board/task/run) tersedia read-only; unit systemd dihasilkan tanpa path rahasia | `TestF7PLUG021KanbanDashboard` | LULUS | data saja, UI di F8 |

## 8. Ledger parity F7 — A2A dan Yuanbao (12)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-001 | `a2a_call` | kirim tugas ke agent remote; timeout + retry berbatas; hasil `<untrusted>` | `TestF7TOOL001A2ACall` | BELUM | — |
| TOOL-002 | `a2a_discover` | temukan agent + agent card; card invalid ditolak | `TestF7TOOL002A2ADiscover` | BELUM | — |
| TOOL-003 | `a2a_history` | riwayat interaksi A2A; tanpa entri → kosong | `TestF7TOOL003A2AHistory` | BELUM | — |
| TOOL-004 | `a2a_list` | daftar agent dikenal/terkonfigurasi; 0 secret di output | `TestF7TOOL004A2AList` | BELUM | — |
| TOOL-005 | `a2a_orchestrate` | orkestrasi multi-agent; fan-out dibatasi; 0 rekursi tak berujung | `TestF7TOOL005A2AOrchestrate`, `TestF7A2AOrchestrateDepth` | BELUM | — |
| TOOL-049 | `yb_query_group_info` | info grup Yuanbao; adapter nonaktif → `module_unavailable` | `TestF7TOOL049YuanbaoGroupInfo` | BELUM | — |
| TOOL-050 | `yb_query_group_members` | daftar member; paginasi berbatas | `TestF7TOOL050YuanbaoGroupMembers` | BELUM | — |
| TOOL-051 | `yb_search_sticker` | cari stiker; hasil ≤ batas | `TestF7TOOL051YuanbaoSearchSticker` | BELUM | — |
| TOOL-052 | `yb_send_dm` | kirim DM; identitas non-allowlist ditolak | `TestF7TOOL052YuanbaoSendDM` | BELUM | — |
| TOOL-053 | `yb_send_sticker` | kirim stiker valid; stiker asing ditolak | `TestF7TOOL053YuanbaoSendSticker` | BELUM | — |
| TSET-032 | toolset `hermes-yuanbao` | 5 tool, available saat inventaris=false | `TestF7TSET032Yuanbao` | BELUM | — |
| TSET-033 | toolset `a2a` | 5 tool, default off, available saat inventaris=false | `TestF7TSET033A2A` | BELUM | — |

## 9. GAP F7 (10) — SPEC §22.1

| GAP | Kriteria verifikasi | Tes yang harus ada | Status |
|---|---|---|---|
| GAP-SEC-10 | tabel identity strength lengkap untuk 30 adapter; identitas `weak` tidak bisa allowlist/pair (fail-closed); IRC wajib SASL + `account-tag`; PRIVMSG tanpa tag `account` → 0 LLM/tool call | `TestIdentityStrengthTable`, `TestIRCAccountAuth` | LULUS |
| GAP-REL-14 | suite `known-defects` ≥ 10 skenario lulus 100% per PR: payload > batas → 413 eksplisit, streaming 1× per pesan, biaya kartu kanban 1× per run, stream terputus retry/error, gambar MCP tetap lampiran | `TestKnownDefectsSuite` (agregat 10 `TestKnownDefect*` + `TestF7KanbanCostOnce` + `TestF7APIStream413`) | BELUM |
| GAP-GW-01 | wizard hanya Cloud API; Web hanya lewat `--accept-ban-risk` dengan satu ack + event audit; guard cold outbound `cold_outbound_denied` 0 request; pacing 100 pesan ≥ 297 s fake clock | `TestWhatsAppWebGuards`, `TestSetupNoWebWithoutAck` | BELUM |
| GAP-GW-03 | `gateway/outq` token bucket per chat/grup/platform dari `Caps.Rate`; replay 200 balasan → 0×429; `MaxChunksPerReply` dihormati semua adapter; 0 `time.Sleep` inline (forbidigo) | `TestF7GAPGW03OutqReplay`, `TestPlatformAdapterContract`, `TestF7NoInlineSleep` | LULUS |
| GAP-GW-04 | satu parse AST → renderer Telegram entities/Slack mrkdwn/WhatsApp/Discord/Matrix HTML/plain; fence terpotong dibuka ulang; `FuzzRenderTelegram` 10.000 input 0 penolakan; golden per renderer | `FuzzRenderTelegram`, `TestF7RenderGoldens` | LULUS |
| GAP-GW-05 | matriks `TestGroupMentionPolicy` semua adapter: pesan grup tanpa mention → 0 turn (default); `stt_echo` default `dm` | `TestGroupMentionPolicy` | LULUS |
| GAP-GW-08 | Email XOAUTH2; kursor `UIDVALIDITY`+`UID` di `gateway_cursors`; `BODY.PEEK[]` tanpa `\Seen`; email ditolak tetap UNSEEN; IDLE dipakai ulang; login/hari < 10 | `TestF7GAPGW08EmailIMAP`, `TestF7EmailCursorPersist` | BELUM |
| GAP-GW-09 | segmen GSM-7 160/153 dan UCS-2 70/67; cap 3 segmen + "balas MORE"; biaya per segmen masuk ledger `tool:sms`; `damnbot usage --by feature` menampilkannya | `TestF7GAPGW09SMSSegments`, `TestF7SMSLedger` | BELUM |
| GAP-GW-10 | `Caps.MinEditInterval`/`MaxEditsPerMessage` dideklarasikan dan dipatuhi; Telegram 1,5 s; Matrix tanpa edit-stream | `TestF7GAPGW10EditStreamCaps`, `TestF7MatrixNoEditStream` | LULUS |
| GAP-GW-11 | breaker per kelas disconnect (`logged_out`/`banned`/401/Discord 4004) → berhenti reconnect + `needs_attention` + notice owner; disconnect 10 menit ≤ 15 percobaan; `TestBridgeProtocolConformance` memetakan `health.reason`; 0 fetch versi protokol WA saat runtime | `TestF7GAPGW11Breaker`, `TestBridgeProtocolConformance` | BELUM |

## 10. EC F7 (9)

| Item | Kriteria | Tes / artefak | Status |
|---|---|---|---|
| EC-PLAT-004 | turn dengan `api_calls>0` tanpa teks final tetap mengirim notifikasi (bukan senyap) | `TestEdgeCases/EC-PLAT-004` (`TestF7ECPLAT004EmptyReplyNotice`) | LULUS |
| EC-PLAT-005 | pesan baru setelah `/stop` selalu diproses (generasi baru), tidak dibuang | `TestEdgeCases/EC-PLAT-005` (`TestF7ECPLAT005AfterStop`) | LULUS |
| EC-PLAT-006 | media hanya dari turn saat ini; gambar turn lama tidak menempel | `TestEdgeCases/EC-PLAT-006` (`TestF7ECPLAT006MediaIsolation`) | LULUS |
| EC-PLAT-008 | edit pairing store diserialisasi lintas proses (dua proses, 0 lost update) | `TestEdgeCases/EC-PLAT-008` (`TestF7ECPLAT008PairingLock`) | LULUS |
| EC-PLAT-024 | timeout baca body bridge tidak melepas ownership sesi; request lambat tetap milik satu owner | `TestEdgeCases/EC-PLAT-024` (`TestF7ECPLAT024BridgeOwnership`) | BELUM |
| EC-STATE-004 | multiplex banyak profil tidak menembus batas koneksi; EMFILE → fallback ke writer, bukan 500 | `TestEdgeCases/EC-STATE-004` (`TestF7ECSTATE004ReadPool`) | LULUS |
| EC-TERM-004 | task id sama lintas profil tidak berbagi sandbox/cwd; kunci dikualifikasi per profil/sesi | `TestEdgeCases/EC-TERM-004` (`TestF7ECTERM004SandboxKey`) | LULUS |
| EC-TERM-005 | policy terminal yang gagal resolve → fail closed, bukan nilai ambient | `TestEdgeCases/EC-TERM-005` (`TestF7ECTERM005PolicyResolve`) | LULUS |
| EC-SEC-006 | payload webhook tidak pernah mengeksekusi file/terminal; toolset webhook = web/vision/clarify | `TestEdgeCases/EC-SEC-006` (`TestF7ECSEC006WebhookToolset`) | BELUM |

Katalog EC dibaca lewat `internal/testutil/phase` (`g.Item(t, "<EC-ID>", "F7", "covered")`); `make phase-acceptance` pada `PHASE=F7` mensertifikasi F6, sedangkan baris F7 menjadi gate saat `PHASE`→F8.

## 11. Carryover F6→F7 (3)

| Item | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| MEDIA-029/031 | dua baris media F6 yang ditandai `N/A — fase F7` ditutup di §6 | `TestF7MEDIA029DiscordVoice`, `TestF7MEDIA031Silk` | BELUM |
| Q14 image preflight | `BenchmarkImagePreflight` 12 MP ≤ 150 ms diukur di mesin Q14; angka target dan `TestImagePreflightTargetNotLowered` tidak berubah | pengukuran Q14 + artefak `bench/speed/results/` + `TestF7ImagePreflightQ14Artifact` | BELUM |
| GAP-REL-14 F7 slice | suite `known-defects` diperluas ke permukaan F7 (api-server 413, kanban biaya 1×, stream diskon) | `TestKnownDefectsSuite` | BELUM |

## 12. Perintah bukti

```sh
# Gate fase saat ini mensertifikasi F6 (ADR-176); F7 menjadi gate saat PHASE->F8.
PHASE=F7 make phase-acceptance

# Baris yang disentuh satu PR (contoh).
go test -race -count=1 -run 'TestF7|TestPlatformAdapterContract|TestIRCAccountAuth|TestIdentityStrengthTable' ./internal/gateway/... ./cmd/damnbot

# Gate penutupan ADR-326: hijau selama PHASE=F7; menolak sisa BELUM/planned saat PHASE>F7.
go test -count=1 -run 'TestF7LedgerClosed|TestF6LedgerClosed|TestParityPhaseColumn|TestF7LedgerGateRules' ./internal/archtest

# Budget/arsitektur tetap hijau setelah paket F7 bertambah.
make loc
make size-check
```

## 13. Definisi `N/A` (ADR-326 butir 4)

`N/A` bukan `LULUS` dan bukan jalan pintas. Bentuk yang diterima gate: `N/A — <alasan teknis ≥ 8 karakter> (carryover F8)`; alasan wajib menyebut prasyarat yang hilang dan fase target. Pemakaian yang **sah**:

- adapter tanpa akun uji nyata → `N/A — no test account (nightly smoke) (carryover F8)` pada baris *smoke akun nyata* saja; baris fake server tetap wajib `LULUS`.
- pengukuran hardware Q14 → `N/A — hardware: target hanya sah di Q14 (carryover F8)`.
- surface yang menunggu modul fase lain (mis. dashboard kanban UI penuh) → `N/A — UI web F8 (carryover F8)`.

Pemakaian yang **ditolak**: `N/A` telanjang, `N/A` tanpa alasan (gate menolak < 8 karakter), `N/A` untuk menyembunyikan tes yang gagal, dan `N/A` pada baris agregat yang anggotanya belum tertutup.

## 14. Angka

- Baris parity F7: **103** (PLAT 29, TSET 21, TOOL 25, GW 10, CLI 6, AUTO 4, MEDIA 2, PLUG 2, SLASH 2, UI 1, EXEC 1). Dari 103: §2 27, §3 9, §4 12, §5 18, §6 2, §7 23, §8 12.
- GAP F7: **10**; EC F7: **9** (`EC-PLAT-004/005/006/008/024`, `EC-STATE-004`, `EC-TERM-004/005`, `EC-SEC-006`); carryover F6→F7: **3**.
- Baris agregat §1: **6**. Jumlah baris data ledger: **131**.
- Ledger F6 ditutup dengan 111 baris data (110 `LULUS`, 1 `N/A — hardware`); F7 dimulai dari 0 `LULUS`.
