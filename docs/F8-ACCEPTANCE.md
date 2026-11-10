# F8-ACCEPTANCE — Ledger acceptance F8 (web UI, ekosistem, otomatisasi)

Status: **ledger hidup** untuk `PHASE=F8` pada `origin/main` `cb7755167`. Dari 167 baris data, **75 `LULUS`** (72 parity + 3 GAP), **90 `BELUM`**, dan 2 `N/A — … (carryover F9)`; ketujuh baris agregat §1 masih `BELUM`. Satu baris hanya boleh berubah menjadi `LULUS` (atau `N/A` dengan alasan tertulis) pada PR yang benar-benar menambahkan tes bernama di kolom "Tes yang harus ada" dan tes itu lulus; erratum ADR-326 mencatat pengecualian untuk baris yang tesnya sudah di-merge pada PR sebelumnya dan hanya disinkronkan oleh PR dokumen ini. Tidak ada kriteria yang diubah, dan setiap status `LULUS` menunjuk tes bernama yang sudah ada.

Sumber normatif: ROADMAP F8 (docs/ROADMAP.md:206-214), 148 baris parity berfase F8 (docs/FEATURE-PARITY.md; 72 `LULUS`, 76 `planned`), kontrak modul SPEC §3.2.4 (SPEC.md:302-307), 17 baris GAP F8 (SPEC.md:2927-2930,2938,2956,2965,2974,2979,3000,3017-3020,3035,3037-3038), katalog EC (tidak ada entri F8; docs/ROADMAP.md:58-67), dan carryover F7 (docs/F7-ACCEPTANCE.md:20,206). Nama paket, baris `MODULE`, dan plafon family mengikuti ADR-330 (docs/DECISIONS.md).

Aturan gate:

- **Penutupan (ADR-330, melanjutkan ADR-326/ADR-323).** Baris hanya `LULUS` bila PR yang sama menambah tes bernama, mengubah baris parity F8 menjadi `LULUS`, dan mengubah baris ledger `BELUM`→`LULUS`. Baris yang tidak dapat dibuktikan ditulis `N/A — <alasan teknis> (carryover F9)`, bukan `BELUM`. Baris agregat hanya `LULUS` setelah semua anggota tertutup.
- **Gate mesin.** Selama `PHASE=F8` gate tidak aktif (ledger hidup). Begitu `PHASE` > F8, `TestF8LedgerClosed` menolak `BELUM`, `N/A` telanjang (< 8 karakter alasan), dan baris parity F8 yang bukan `LULUS`/`N/A — …`; `TestParityPhaseColumn` memeriksa hal yang sama dari sisi parity. `TestF8LedgerGateRules` adalah kontrol negatifnya.
- **Budget.** `maxF8ModuleLOC` = 21.800 (ROADMAP F8) dan `maxModuleLOC` = 132.000 (ADR-330); baris `MODULE` F8 berada di luar Σ core (ADR-196). Naikkan angka hanya lewat ADR terukur.
- Tidak ada `t.Skip` runtime (REL-15). Web UI diuji dengan `httptest`/Chromium headless dan fake server; binary nyata hanya dipakai harness `internal/e2e`. Network keluar (petdex, skills hub, connector, upload debug) memakai fake server dan tidak pernah menjadi default.
- Nama tes bersifat target, bukan implementasi. Nama yang sudah diwajibkan SPEC dipertahankan apa adanya: `TestWebCSPHeaders`, `TestWebCSRF`, `TestWebSessionRevoke`, `TestContrastTokens`, `TestWebAssetsNoExternalURL`, `TestHelpOffline`, `TestSurfaceParity`, `TestSurfaceDocSync`, `TestRemoteLinkOneTime`, `TestI18nNoEmpty`, `TestNoUILiterals`, `TestCriticalKeysAllLocales`, `TestLocaleSetIdentical`, `TestReplyLanguageLine`, `TestLocaleNotEnglishCopy`, `TestThreatScanDotenv`, `TestExternalMemoryUploadCap`, `TestWebSessionRevoke`.

## 1. Acceptance ROADMAP F8

| Item ROADMAP F8 | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| UI 28/28 | 8 baris UI berfase F8 `LULUS` (§7) + 20 baris UI fase sebelumnya yang sudah berjalan; setiap permukaan frontend merender atau memakai fallback terdefinisi | §7 `LULUS` + `TestF8UIMatrix` | BELUM |
| AUTO 32/32 | 12 baris AUTO berfase F8 `LULUS` (§7) + 20 baris AUTO fase sebelumnya; semua fitur `auto` default off | §7 `LULUS` + `TestF8AUTOMatrix` | BELUM |
| P11 budget auto | config default → 0 call `purpose=auto`; setiap fitur berhenti tepat di budget dan menulis event `llm_call` dengan `feature=<id>` | `TestF8AutoDefaultZeroCalls`, `TestF8AutoBudgetStop` | BELUM |
| SKILL/SKCAT/PLUG | 8 baris SKILL + 35 SKCAT + 10 PLUG berfase F8 `LULUS`; katalog 58 bundled + 153 optional = 211 entri | §5/§6/§8 `LULUS` + `TestF8SkillCatalogCounts`, `TestF8PluginCatalog645` | BELUM |
| Web UI perf & aset | first token ≤ +50 ms; aset embedded ≤ 1,5 MB; 0 key i18n hilang di 18 locale | `BenchmarkWebFirstToken`, `TestF8WebAssetsBudget`, `TestI18nNoEmpty` | BELUM |
| Budget modul & binary | `make loc` 0 GAGAL dengan baris F8 (ADR-330); P13 full ≤ 52.000.000 B dan minimal ≤ 22.100.000 B (ADR-325) | `TestLocBudgetModuleRows`, `TestBudgetTableSum`, `make size-check` di PR penutup | BELUM |
| Ledger F8 | 148 baris parity F8 + 17 GAP tertutup; `PHASE` → F9 hanya setelahnya | `TestF8LedgerClosed`, `TestParityPhaseColumn`, `TestF8LedgerGateRules` | BELUM |

## 2. Ledger parity F8 — CLI (14) dan slash (13)

Kontrak yang sama untuk semua baris: perintah/slash terdaftar di `damnbot help` dan router (GAP-OPS-08), 0 nilai secret di output, error typed untuk argumen asing, output chat ≤ batas renderer, dan tidak ada network default (opt-in eksplisit).

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| CLI-012 | `damnbot proxy` | tiga sub `start`, `status`, `providers`; proxy OpenAI-compatible ke provider OAuth; 0 token di output/log | `TestF8CLI012Proxy`, `TestF8CLI012ProxySubcommands` | BELUM | surface UI-022 |
| CLI-030 | `damnbot project` | 11 sub (create, list, show, add-folder, remove-folder, rename, set-primary, use, archive, restore, bind-board); workspace multi-folder; path di luar workspace ditolak | `TestF8CLI030Project`, `TestF8CLI030ProjectSubcommands` | BELUM | OPS-038 |
| CLI-036 | `damnbot dump` | ringkasan setup untuk dukungan; redaksi §14.3 + PII; 0 secret mentah | `TestF8CLI036Dump` | LULUS | OPS-010 |
| CLI-037 | `damnbot debug` | dua sub `share`, `delete`; upload hanya opt-in dan terenkripsi; 0 host pastebin | `TestF8CLI037Debug` | LULUS | GAP-SEC-17 |
| CLI-049 | `damnbot curator` | 17 sub (status, usage, run, pause, resume, pin, unpin, list-unmanaged, adopt, restore, list-archived, archive, prune, backup, rollback, ledger, purge); mutasi idempoten | `TestF8CLI049Curator`, `TestF8CLI049CuratorSubcommands` | BELUM | SKILL-021 |
| CLI-050 | `damnbot pets` | 8 sub (list, install, select, show, off, scale, remove, doctor); fetch petdex opt-in fake server; 0 aset eksternal runtime | `TestF8CLI050Pets` | BELUM | UI-010 |
| CLI-051 | `damnbot journey` | alias `learning`, `memory-graph`; tiga sub list/delete/edit; timeline kronologis tanpa duplikat | `TestF8CLI051Journey` | BELUM | MEM-035 |
| CLI-052 | `damnbot memory` | empat sub setup/status/off/reset; provider tunggal aktif; config hanya referensi secret | `TestF8CLI052Memory` | BELUM | MEM-005/008 |
| CLI-056 | `damnbot connectors` | lima sub list/accounts/connect/rename/disconnect; akun hosted; 0 token di output | `TestF8CLI056Connectors` | BELUM | PLUG-035 |
| CLI-058 | `damnbot insights` | analitik pemakaian dari ledger; filter periode; 0 secret | `TestF8CLI058Insights` | LULUS | OPS-016 |
| CLI-060 | `damnbot monitoring` | satu sub `status`; health gateway + ekspor diagnostik ter-redaksi | `TestF8CLI060Monitoring` | BELUM | OPS-018 |
| CLI-068 | `damnbot dashboard` | satu sub `register`; menyajikan aset embedded; 0 URL eksternal | `TestF8CLI068Dashboard` | LULUS | UI-014/GAP-UX-08 |
| CLI-069 | `damnbot serve` | backend headless untuk desktop/remote; bind loopback default; auth wajib | `TestF8CLI069Serve` | LULUS | UI-016 |
| CLI-070 | `damnbot desktop` | alias `gui`; membuka web UI; 0 dependency Node/Electron saat runtime | `TestF8CLI070Desktop` | LULUS | UI-017 diganti web UI |
| SLASH-027 | `/journey` | alias `/learning`, `/memory-graph`; sub list/delete/edit; output ≤ batas renderer | `TestF8SLASH027Journey` | BELUM | CLI-051 |
| SLASH-030 | `/goal` | teks wajib; goal aktif lintas turn; dinilai judge (AUTO-014) | `TestF8SLASH030Goal` | BELUM | — |
| SLASH-031 | `/heartbeat` | alias `/hb`; sub status/pause/resume/clear; interval minimum dihormati | `TestF8SLASH031Heartbeat` | BELUM | AUTO-016 |
| SLASH-034 | `/loop` | alias `/proactive`; interval + prompt, `--times`, `--until`; batas jumlah | `TestF8SLASH034Loop` | BELUM | AUTO-015 |
| SLASH-037 | `/subgoal` | tambah/kelola kriteria goal aktif; hanya saat goal ada | `TestF8SLASH037Subgoal` | BELUM | — |
| SLASH-049 | `/personality` | nama persona divalidasi; preset dibaca dari katalog | `TestF8SLASH049Personality` | BELUM | UI-011 |
| SLASH-072 | `/pet` | sub toggle/list/scale/off; adopsi maskot opt-in | `TestF8SLASH072Pet` | BELUM | UI-010 |
| SLASH-073 | `/hatch` | alias `/generate-pet`; deskripsi → pet baru; network opt-in | `TestF8SLASH073Hatch` | BELUM | CLI-050 |
| SLASH-077 | `/suggestions` | alias `/suggest`; sub accept/dismiss/catalog/clear; saran default off | `TestF8SLASH077Suggestions` | LULUS | AUTO-013 |
| SLASH-078 | `/blueprint` | alias `/bp`; nama template + `slot=value` tervalidasi; slot asing ditolak | `TestF8SLASH078Blueprint` | LULUS | AUTO-012 |
| SLASH-079 | `/curator` | sub status/run/pause/resume/pin/unpin/restore/list-archived; sama dengan CLI-049 | `TestF8SLASH079Curator` | BELUM | — |
| SLASH-094 | `/insights` | argumen `days`; output analitik ≤ batas renderer | `TestF8SLASH094Insights` | LULUS | CLI-058 |
| SLASH-102 | `/debug` | upload laporan debug; tautan sekali pakai; konten ter-redaksi; 0 host pastebin | `TestF8SLASH102Debug` | BELUM | OPS-010/GAP-SEC-17 |

## 3. Ledger parity F8 — TOOL (23)

Kontrak: tool client-surface hanya tersedia di sesi yang mengiklankan surface (desktop/web); tool network `opt-in`; skema input divalidasi `internal/schema`; hasil network berlabel `<untrusted>`; 0 tool shell/write tambahan di toolset F8.

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| TOOL-020 | `manage_catalog` | kartu approval instalasi plugin/skill; hanya sesi GUI; ack diketik untuk entri `tos_risk=high` | `TestF8TOOL020ManageCatalog` | BELUM | TSET-011 |
| TOOL-024 | `manage_connections` | kelola akun connector hosted + link otorisasi; opt-in; token hanya referensi vault | `TestF8TOOL024ManageConnections` | BELUM | TSET-010 |
| TOOL-027 | `annotate_preview` | anotasi elemen pane preview; target harus pane aktif | `TestF8TOOL027AnnotatePreview` | BELUM | TSET-002 |
| TOOL-028 | `close_terminal` | tutup pane terminal desktop; pane asing ditolak | `TestF8TOOL028CloseTerminal` | BELUM | — |
| TOOL-029 | `desktop_preview` | buka file/URL di pane preview; path workspace atau URL https | `TestF8TOOL029DesktopPreview` | BELUM | — |
| TOOL-030 | `drive_preview` | klik/ketik/elemen di pane preview; aksi destruktif approval | `TestF8TOOL030DrivePreview` | BELUM | — |
| TOOL-031 | `focus_pane` | fokus pane GUI tertentu; nama pane divalidasi | `TestF8TOOL031FocusPane` | BELUM | — |
| TOOL-032 | `gui_tour` | alias legacy `tour`; tur terpandu; hanya bila tours aktif | `TestF8TOOL032GUITour` | BELUM | — |
| TOOL-033 | `react_to_message` | reaksi emoji pada pesan GUI; emoji dari allowlist | `TestF8TOOL033ReactToMessage` | BELUM | — |
| TOOL-034 | `read_terminal` | baca output pane terminal; output dibatasi + ter-redaksi | `TestF8TOOL034ReadTerminal` | BELUM | — |
| TOOL-035 | `read_window_below` | baca jendela/pane di bawah chat; pane hilang → kosong bukan panic | `TestF8TOOL035ReadWindowBelow` | BELUM | — |
| TOOL-036 | `set_theme` | ganti tema desktop; tema asing ditolak | `TestF8TOOL036SetTheme` | BELUM | — |
| TOOL-037 | `show_tip` | alias legacy `tip`; tip kontekstual; `notices=false` → 0 tip | `TestF8TOOL037ShowTip` | BELUM | GAP-OPS-09 |
| TOOL-038 | `discord` | baca/partisipasi Discord: pesan, cari member, buat thread; token dari seam | `TestF8TOOL038Discord` | LULUS | TSET-014 |
| TOOL-039 | `discord_admin` | admin server: channel, role, pin; butuh scope admin + approval | `TestF8TOOL039DiscordAdmin` | LULUS | TSET-015 |
| TOOL-040 | `feishu_doc_read` | baca dokumen Feishu/Lark; hasil `<untrusted>`; batas ukuran | `TestF8TOOL040FeishuDocRead` | LULUS | TSET-016 |
| TOOL-041 | `feishu_drive_add_comment` | tambah komentar dokumen Feishu; approval mutasi | `TestF8TOOL041FeishuAddComment` | LULUS | TSET-017 |
| TOOL-042 | `feishu_drive_list_comment_replies` | daftar balasan komentar; paginasi berbatas | `TestF8TOOL042FeishuListReplies` | LULUS | — |
| TOOL-043 | `feishu_drive_list_comments` | daftar komentar dokumen; tanpa akses → kosong | `TestF8TOOL043FeishuListComments` | LULUS | — |
| TOOL-044 | `feishu_drive_reply_comment` | balas komentar; id komentar divalidasi | `TestF8TOOL044FeishuReplyComment` | LULUS | — |
| TOOL-071 | `setup_beat` | kartu onboarding first-run; hanya sesi GUI; sekali per instalasi | `TestF8TOOL071SetupBeat` | BELUM | TSET-024 |
| TOOL-072 | `desktop_project` | buat/ganti Desktop Project (workspace bernama); path di luar workspace ditolak | `TestF8TOOL072DesktopProject` | BELUM | TSET-022 |
| TOOL-088 | `x_search` | cari post/thread X via xAI; opt-in; hasil `<untrusted>` + batas | `TestF8TOOL088XSearch` | LULUS | TSET-031 |

## 4. Ledger parity F8 — TSET (10) dan MEM (6)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| TSET-002 | toolset `desktop_ui` | 11 tool, default off, hanya surface desktop/web; 0 shell/write | `TestF8TSET002DesktopUI` | BELUM | TOOL-027..037,071,072 |
| TSET-010 | toolset `connections` | 1 tool `manage_connections`, default off | `TestF8TSET010Connections` | BELUM | — |
| TSET-011 | toolset `catalog` | 1 tool `manage_catalog`, default off | `TestF8TSET011Catalog` | BELUM | — |
| TSET-014 | toolset `discord` | 1 tool, default off, hanya bila adapter Discord aktif | `TestF8TSET014Discord` | LULUS | PLAT-005 |
| TSET-015 | toolset `discord_admin` | 1 tool, default off, scope admin eksplisit | `TestF8TSET015DiscordAdmin` | LULUS | — |
| TSET-016 | toolset `feishu_doc` | 1 tool, default off | `TestF8TSET016FeishuDoc` | LULUS | — |
| TSET-017 | toolset `feishu_drive` | 4 tool, default off; mutasi approval | `TestF8TSET017FeishuDrive` | LULUS | — |
| TSET-022 | toolset `project` | 1 tool, default off, hanya surface desktop | `TestF8TSET022Project` | BELUM | — |
| TSET-024 | toolset `onboarding` | 1 tool, default off, hanya first-run | `TestF8TSET024Onboarding` | BELUM | — |
| TSET-031 | toolset `x_search` | 1 tool, default off; opt-in eksplisit | `TestF8TSET031XSearch` | LULUS | — |
| MEM-005 | Plugin provider memori eksternal | satu provider aktif per `memory.provider`; provider asing → error; upload ≤ 1 MiB/item | `TestF8MEM005ExternalProvider`, `TestExternalMemoryUploadCap` | LULUS | GAP-PROV-05; ADR-338 |
| MEM-008 | Memory provider Honcho | config & profil Honcho; token dari secret; 0 upload default | `TestF8MEM008Honcho` | LULUS | PLUG-022; wiring `assemblerAdapter`, ADR-338 |
| MEM-009 | Memory query rewrite | rewrite query sebelum recall provider; hanya bila fitur aktif; prompt tetap Inggris | `TestF8MEM009QueryRewrite` | LULUS | default off, ADR-338 |
| MEM-010 | Memory OAuth / setup | wizard setup + OAuth provider memori; referensi secret saja; revoke didukung | `TestF8MEM010MemoryOAuth` | BELUM | GAP-SEC-18; lapisan OAuth/keychain lulus, surface CLI-052 belum |
| MEM-023 | Context engine plugin | ganti engine konteks via plugin `context_engine`; default off; engine asing ditolak | `TestF8MEM023ContextEngine` | LULUS | PLUG-014; slot read-only, ADR-338 |
| MEM-035 | Learning journey / graph | timeline skill & memori; entri tanpa sumber diredaksi; 0 duplikat | `TestF8MEM035LearningJourney` | BELUM | CLI-051/SLASH-027; builder lokal lulus, surface CLI belum |

## 5. Ledger parity F8 — SKILL (8)

Kontrak: setiap entri hub membawa `tos_risk`/`tos_note` (GAP-GW-07), signature ed25519 (GAP-SEC-20), provenance, dan hasil install melewati threat scan + karantina (P10). Baris SKILL-012..015/017/020 dibuktikan `internal/skills/hub` + CLI `damnbot skills hub` (`TestF8SKILL012Official`, `TestF8SKILL013GitHub`, `TestF8SKILL014ClawHub`, `TestF8SKILL015SkillsSh`, `TestF8SKILL017OptionalSync`, `TestF8SKILL020UsageTracking`, e2e `TestF8SkillsHubCLI`). SKILL-011 dan SKILL-021 ditutup oleh PR lanjutan: indeks `index.json.sig` ed25519 (kunci trust fail-closed, tanpa fallback) dan planner curator deterministik (idle ≥ 2 jam, stale/archive/retire/merge, budget, 0 LLM call, 0 tulis).

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| SKILL-011 | Skills hub (install/search) | cari & pasang dari sumber eksternal; indeks bertanda tangan; tanpa signature → tolak | `TestF8SKILL011Hub`, `TestCatalogSignature` | LULUS | index.json.sig ed25519; kunci operator fail-closed |
| SKILL-012 | Sumber resmi Nous | sumber official terdaftar; pinned SHA; 0 fallback diam | `TestF8SKILL012Official` | LULUS | — |
| SKILL-013 | Sumber GitHub | install dari repo GitHub; commit SHA + sha256 arsip diverifikasi | `TestF8SKILL013GitHub` | LULUS | sha256 konten SKILL.md (arsip tidak dipakai) |
| SKILL-014 | Sumber ClawHub | sumber ClawHub; entri tidak tepercaya tetap `pending` | `TestF8SKILL014ClawHub` | LULUS | — |
| SKILL-015 | Sumber skills.sh | sumber skills.sh; rate-limit + retry berbatas | `TestF8SKILL015SkillsSh` | LULUS | — |
| SKILL-017 | Optional skills sync | pasang dari `optional-skills/` sesuai permintaan; 153 skill opsional | `TestF8SKILL017OptionalSync` | LULUS | SKCAT-013..035 |
| SKILL-020 | Skill usage tracking | statistik pemakaian skill (input curator); agregat tanpa isi prompt | `TestF8SKILL020UsageTracking` | LULUS | — |
| SKILL-021 | Curator | pemeliharaan background stale/archive/consolidate; hanya saat idle ≥ 2 jam; budget | `TestF8SKILL021Curator`, `TestF8CuratorBudget` | LULUS | AUTO-003; bukti `internal/skills/hub/curator_test.go:20,43` (#828 @ b6c83b5b7) |
## 6. Ledger parity F8 — SKCAT (35)

Kontrak: kategori tersedia setelah sync idempoten; frontmatter setiap SKILL.md valid; referensi `requires`/`skill://` dan path script ada (0 menggantung); threat scan §14.4; 0 nilai kosong di katalog i18n terkait. Baris SKCAT-001..035 dibuktikan katalog embedded `internal/skills/catalog` (211 entri, digest per baris, kategori tertutup) dan tes bernama per kategori; sinkron opsional/hub memakai entri yang sama.

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| SKCAT-001 | Bundled `apple` (4) | 4 skill tersinkron; frontmatter valid; 0 referensi menggantung | `TestF8SKCAT001Apple` | LULUS | bundled |
| SKCAT-002 | Bundled `autonomous-ai-agents` (5) | 5 skill tersinkron; referensi valid | `TestF8SKCAT002AutonomousAIAgents` | LULUS | bundled |
| SKCAT-003 | Bundled `creative` (10) | 10 skill tersinkron; referensi valid | `TestF8SKCAT003Creative` | LULUS | bundled |
| SKCAT-004 | Bundled `devops` (1) | 1 skill tersinkron; referensi valid | `TestF8SKCAT004Devops` | LULUS | bundled |
| SKCAT-005 | Bundled `email` (2) | 2 skill tersinkron; referensi valid | `TestF8SKCAT005Email` | LULUS | bundled |
| SKCAT-006 | Bundled `media` (3) | 3 skill tersinkron; referensi valid | `TestF8SKCAT006Media` | LULUS | bundled |
| SKCAT-007 | Bundled `note-taking` (1) | 1 skill tersinkron; referensi valid | `TestF8SKCAT007NoteTaking` | LULUS | bundled |
| SKCAT-008 | Bundled `productivity` (14) | 14 skill tersinkron; referensi valid | `TestF8SKCAT008Productivity` | LULUS | bundled |
| SKCAT-009 | Bundled `research` (4) | 4 skill tersinkron; referensi valid | `TestF8SKCAT009Research` | LULUS | bundled |
| SKCAT-010 | Bundled `social-media` (1) | 1 skill tersinkron; referensi valid | `TestF8SKCAT010SocialMedia` | LULUS | bundled |
| SKCAT-011 | Bundled `software-development` (12) | 12 skill tersinkron; referensi valid | `TestF8SKCAT011SoftwareDevelopment` | LULUS | bundled |
| SKCAT-012 | Bundled `web` (1) | 1 skill tersinkron; referensi valid | `TestF8SKCAT012Web` | LULUS | bundled |
| SKCAT-013 | Optional `autonomous-ai-agents` (7) | 7 skill opsional terpasang saat diminta; 0 auto-install | `TestF8SKCAT013OptionalAutonomousAIAgents` | LULUS | opt-in |
| SKCAT-014 | Optional `blockchain` (3) | 3 skill opsional; default testnet/devnet (GAP-SEC-15) | `TestF8SKCAT014OptionalBlockchain` | LULUS | opt-in |
| SKCAT-015 | Optional `communication` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT015OptionalCommunication` | LULUS | opt-in |
| SKCAT-016 | Optional `creative` (31) | 31 skill opsional; referensi valid | `TestF8SKCAT016OptionalCreative` | LULUS | opt-in |
| SKCAT-017 | Optional `data-science` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT017OptionalDataScience` | LULUS | opt-in |
| SKCAT-018 | Optional `devops` (7) | 7 skill opsional; referensi valid | `TestF8SKCAT018OptionalDevops` | LULUS | opt-in |
| SKCAT-019 | Optional `dogfood` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT019OptionalDogfood` | LULUS | opt-in |
| SKCAT-020 | Optional `email` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT020OptionalEmail` | LULUS | opt-in |
| SKCAT-021 | Optional `finance` (9) | 9 skill opsional; skill pembayaran `monetary` (GAP-SEC-15) | `TestF8SKCAT021OptionalFinance` | LULUS | opt-in |
| SKCAT-022 | Optional `gaming` (2) | 2 skill opsional; referensi valid | `TestF8SKCAT022OptionalGaming` | LULUS | opt-in |
| SKCAT-023 | Optional `health` (2) | 2 skill opsional; referensi valid | `TestF8SKCAT023OptionalHealth` | LULUS | opt-in |
| SKCAT-024 | Optional `mcp` (3) | 3 skill opsional; referensi MCP valid | `TestF8SKCAT024OptionalMCP` | LULUS | opt-in |
| SKCAT-025 | Optional `migration` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT025OptionalMigration` | LULUS | opt-in |
| SKCAT-026 | Optional `mlops` (36) | 36 skill opsional; referensi valid | `TestF8SKCAT026OptionalMLOps` | LULUS | opt-in |
| SKCAT-027 | Optional `payments` (3) | 3 skill opsional; pola pembayaran `tos_risk` medium | `TestF8SKCAT027OptionalPayments` | LULUS | opt-in |
| SKCAT-028 | Optional `productivity` (10) | 10 skill opsional; referensi valid | `TestF8SKCAT028OptionalProductivity` | LULUS | opt-in |
| SKCAT-029 | Optional `research` (15) | 15 skill opsional; referensi valid | `TestF8SKCAT029OptionalResearch` | LULUS | opt-in |
| SKCAT-030 | Optional `security` (6) | 6 skill opsional; referensi valid | `TestF8SKCAT030OptionalSecurity` | LULUS | opt-in |
| SKCAT-031 | Optional `smart-home` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT031OptionalSmartHome` | LULUS | opt-in |
| SKCAT-032 | Optional `social-media` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT032OptionalSocialMedia` | LULUS | opt-in |
| SKCAT-033 | Optional `software-development` (6) | 6 skill opsional; referensi valid | `TestF8SKCAT033OptionalSoftwareDevelopment` | LULUS | opt-in |
| SKCAT-034 | Optional `web-development` (5) | 5 skill opsional; referensi valid | `TestF8SKCAT034OptionalWebDevelopment` | LULUS | opt-in |
| SKCAT-035 | Optional `yuanbao` (1) | 1 skill opsional; referensi valid | `TestF8SKCAT035OptionalYuanbao` | LULUS | opt-in |

## 7. Ledger parity F8 — AUTO (12) dan UI (8)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| AUTO-001 | Background review | review turn di background → usul memori/skill; default off; 0 call jalur kritis | `TestF8AUTO001BackgroundReview` | LULUS | P11; bukti `internal/auto/features_medium_test.go:21` (#821 @ 8e99ed7cf) |
| AUTO-002 | Title generation | judul sesi otomatis; default off; biaya per fitur | `TestF8AUTO002TitleGeneration` | LULUS | wiring serve: label setelah turn pertama; provider `purpose=auto`; `config` default-off |
| AUTO-003 | Curator scheduler | curator saat idle ≥ 2 jam; pause/resume; budget | `TestF8AUTO003CuratorScheduler` | BELUM | SKILL-021 |
| AUTO-004 | Idle/hygiene compaction | compaction sesi gateway saat idle/panjang; tidak memotong turn | `TestF8AUTO004IdleCompaction` | LULUS | bukti `internal/auto/features_test.go:103` (#820 @ 665b3aeba) |
| AUTO-012 | Blueprints | template otomasi siap pakai; slot divalidasi; default off | `TestF8AUTO012Blueprints` | LULUS | SLASH-078; bukti `internal/auto/features_medium_test.go:100` (#821 @ 8e99ed7cf) |
| AUTO-013 | Suggestions | saran otomasi accept/dismiss; default off; rate-limited | `TestF8AUTO013Suggestions` | LULUS | SLASH-077; bukti `internal/auto/features_test.go:76` (#820 @ 665b3aeba) |
| AUTO-014 | Goals + goal judge | tujuan berdiri lintas turn; judge terpisah; budget per fitur | `TestF8AUTO014GoalsJudge` | BELUM | SLASH-030 |
| AUTO-015 | Loops / proactive | ulangi prompt pada interval di sesi; `--times`/`--until`; batas | `TestF8AUTO015Loops` | LULUS | SLASH-034; bukti `internal/auto/features_medium_test.go:157` (#821 @ 8e99ed7cf) |
| AUTO-016 | Heartbeat | prompt berulang saat sesi idle; pause/resume/clear | `TestF8AUTO016Heartbeat` | LULUS | SLASH-031; bukti `internal/auto/features_medium_test.go:190` (#821 @ 8e99ed7cf) |
| AUTO-030 | Disk cleanup plugin | lacak & bersihkan file sementara sesi; dry-run default | `TestF8AUTO030DiskCleanup` | LULUS | PLUG-017 tetap BELUM; bukti `internal/auto/features_test.go:125` (#820 @ 665b3aeba) |
| AUTO-031 | Auto maintenance CLI | pemeliharaan otomatis saat start CLI; 0 mutasi tanpa config | `TestF8AUTO031AutoMaintenance` | BELUM | — |
| AUTO-032 | Outbound webhooks | kirim event ke webhook keluar; signature + retry berbatas | `TestF8AUTO032OutboundWebhooks` | LULUS | GW-011; bukti `internal/auto/features_medium_test.go:253` (#821 @ 8e99ed7cf) |
| UI-010 | Pets (petdex) | maskot animasi terminal; generate pet; default off | `TestF8UI010Pets` | BELUM | CLI-050 |
| UI-011 | Personalities | persona preset; opt-in; katalog tervalidasi | `TestF8UI011Personalities` | BELUM | SLASH-049 |
| UI-014 | Web dashboard | dashboard web dilayani binary; aset embedded; auth wajib | `TestF8UI014WebDashboard` | LULUS | GAP-SEC-06/07 |
| UI-015 | Dashboard auth providers | basic, drain, Nous OAuth, OIDC self-hosted; 0 token di HTML | `TestF8UI015AuthProviders`, `TestWebSessionRevoke` | BELUM | PLUG-016 |
| UI-016 | Headless backend | `damnbot serve` tanpa UI untuk desktop/remote; bind loopback default | `TestF8UI016Headless` | LULUS | CLI-069 |
| UI-017 | Desktop app (Electron) | diganti web UI: pane terminal/preview + projects tanpa runtime Node | `TestF8UI017DesktopWeb` | BELUM | ADR-330; 0 Electron |
| UI-022 | Local OAuth proxy | proxy OpenAI-compatible lokal ke provider OAuth; 0 token bocor | `TestF8UI022LocalProxy` | BELUM | CLI-012 |
| UI-023 | i18n (18 bahasa) | 18 locale identik; 0 key hilang/kosong; `lang`/`dir=rtl` benar | `TestF8UI023I18n`, `TestI18nNoEmpty`, `TestLocaleSetIdentical` | BELUM | GAP-INF-08/09 |

## 8. Ledger parity F8 — PLUG (10) dan OPS (9)

| ID | Fitur | Kriteria lulus | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|---|
| PLUG-009 | Plugin catalog (645 entri) | katalog pihak ketiga SHA-pinned; signature + anti-rollback; entri rusak ditolak | `TestF8PLUG009PluginCatalog`, `TestF8PluginCatalog645` | BELUM | GAP-SEC-20 |
| PLUG-014 | Plugin: context_engine | slot engine konteks alternatif; default off; engine asing ditolak | `TestF8PLUG014ContextEngine` | BELUM | MEM-023 |
| PLUG-016 | Plugin: dashboard_auth | 4 provider auth dashboard; token hanya cookie/session store | `TestF8PLUG016DashboardAuth` | BELUM | UI-015 |
| PLUG-017 | Plugin: disk-cleanup | bersihkan file ephemeral; dry-run + allowlist path | `TestF8PLUG017DiskCleanup` | BELUM | AUTO-030 |
| PLUG-018 | Plugin: google_meet | join Meet, transkripsi caption, bicara realtime; pengumuman consent ≤ 10 s | `TestF8PLUG018GoogleMeet`, `TestF8MeetConsent` | LULUS | GAP-GW-12 |
| PLUG-019 | Plugin: hermes-achievements | achievement/gamifikasi dashboard; event lokal 0 LLM call | `TestF8PLUG019Achievements` | BELUM | — |
| PLUG-022 | Plugin: memory | slot/plugin memory provider (Honcho); provider tunggal; ByteRover/RetainDB drop | `TestF8PLUG022MemoryProvider` | BELUM | MEM-005; NP-21 |
| PLUG-025 | Plugin: security-guidance | peringatan keamanan pada hasil write file; non-blokir | `TestF8PLUG025SecurityGuidance` | BELUM | — |
| PLUG-026 | Plugin: teams_pipeline | pipeline rapat MS Teams via Graph; subscription renew | `TestF8PLUG026TeamsPipeline` | LULUS | PLAT-030 |
| PLUG-035 | Connectors (hosted) | connector app hosted + akun via `manage_connections`; OAuth referensi saja | `TestF8PLUG035Connectors` | BELUM | CLI-056 |
| OPS-010 | Debug upload / dump | laporan debug ter-redaksi; tautan sekali pakai; 0 host pastebin | `TestF8OPS010DebugUpload` | BELUM | GAP-SEC-17 |
| OPS-016 | Insights | analitik pemakaian; agregat dari ledger; 0 secret | `TestF8OPS016Insights` | BELUM | CLI-058 |
| OPS-018 | Monitoring & OTLP export | health gateway + export OTLP; endpoint opt-in; 0 konten pesan | `TestF8OPS018OTLP`, `TestOPS018DisabledNoConnect` | LULUS | CLI-060 belum (surface monitoring) |
| OPS-020 | Batch runner | prompt massal paralel → trajectory; batas paralel + biaya | `TestF8OPS020BatchRunner`, `TestF8OPS020BatchRun` | LULUS | e2e `TestF8OPS020EvalBatch` |
| OPS-021 | Trajectory save & compressor | simpan & kompres trajectory; format OpenAI/ShareGPT valid | `TestF8OPS021Trajectory`, `TestF8OPS021BatchCompress` | LULUS | `TestF8OPS022Trajectory` |
| OPS-022 | Trace upload (HF) | upload trace ke Hugging Face; opt-in; 0 transcript user | `TestF8OPS022TraceUpload` | BELUM | — |
| OPS-023 | Mini SWE runner | runner tugas SWE format trajectory Hermes; sandbox | `TestF8OPS023MiniSWE` | LULUS | subset in-memory (tanpa Docker) |
| OPS-024 | Evals | suite evaluasi/probe perilaku deterministik; baseline ter-commit | `TestF8OPS024Evals`, `TestF8EvalDeterministic` | LULUS | GAP-INF-06 |
| OPS-038 | Projects | workspace multi-folder bernama; path divalidasi | `TestF8OPS038Projects` | BELUM | CLI-030 |

## 9. GAP F8 (17) — SPEC §22.1

| GAP | Kriteria verifikasi | Tes yang harus ada | Status | Catatan |
|---|---|---|---|---|
| GAP-SEC-06 | setiap respons HTML membawa CSP nonce tanpa `unsafe-inline`/`unsafe-eval`, nosniff, no-referrer, COOP/CORP, Permissions-Policy; output model/tool dirender DOM builder (0 sink HTML) | `TestWebCSPHeaders`, `TestF8CSPNoSink` | LULUS | — |
| GAP-SEC-07 | URL nonce sekali pakai (lokal 60 s, remote 10 menit) → cookie `__Host-` HttpOnly/Secure/SameSite=Strict; token tidak pernah di HTML/JS/localStorage/query; tiket WS/SSE/download 30 s; socket unix 0600 | `TestF8WebNonceExchange`, `TestF8WebTicketOneTime` | BELUM | — |
| GAP-SEC-08 | non-GET/HEAD wajib `Sec-Fetch-Site` same-origin/none atau Origin terkonfigurasi + CSRF double-submit; Host allowlist; DNS rebinding ditolak 421 | `TestWebCSRF` | LULUS | bukti `internal/web/web_test.go:342` (#822 @ 091ea1ff7) |
| GAP-SEC-09 | session server-side (`web_sessions`), idle 12 jam, absolut 7 hari; logout/ganti password/revoke ≤ 1 s; OIDC end_session | `TestWebSessionRevoke` | BELUM | — |
| GAP-SEC-17 | `doctor bundle` zip lokal 0600 ter-redaksi; `--upload` X25519 ciphertext; 0 host pastebin; 0 canary mentah | `TestF8DoctorBundle`, `TestF8BundleNoSecret` | BELUM | — |
| GAP-REL-07 | deadline jam monotonic; TTFB 25 s + 5 s/32K tok (maks 90 s), idle 60 s; status "menunggu provider" terlihat di web setelah 10 s; fake clock ±1 jam 0 kill palsu | `TestF8WebProviderStatus`, `TestF8MonotonicDeadlines` | BELUM | — |
| GAP-REL-16 | cron `after = [...]` DAG (siklus ditolak, kedalaman ≤ 5); job B menerima `execution_id` run A yang sama; `after_timeout_s` → `upstream_missing` | `TestF8CronAfterDAG` | BELUM | — |
| GAP-GW-07 | setiap entri skills-hub/MCP hub punya `tos_risk`/`tos_note`; `high` butuh ack diketik; entri jailbreak → `pending`; pola dotenv threat scan | `TestF8HubTOSRisk`, `TestThreatScanDotenv` | BELUM | `TestF8HubTOSRisk` belum ada; `TestThreatScanDotenv` (`cmd/damnbot/scan_test.go:11`) saja tidak memenuhi dua tes bernama |
| GAP-GW-12 | bot Google Meet mengirim pengumuman consent ≤ 10 s; teks tidak kosong; `consent_announcement=false` butuh ack; node audio non-TLS ditolak | `TestF8MeetConsent`, `TestF8MeetNodeTLS` | LULUS | — |
| GAP-PROV-05 | importer byterover/retaindb → memori native + 1 warning; upload provider eksternal ≤ 1 MiB/item | `TestExternalMemoryUploadCap` | BELUM | — |
| GAP-UX-07 | transkrip HTML semantik, keyboard-only, focus trap, WCAG 2.2 AA, `prefers-reduced-motion`, 18 bahasa + RTL; axe 0 serious/critical | `TestF8WebA11y`, `TestContrastTokens` | BELUM | — |
| GAP-UX-08 | 0 URL eksternal di aset/template/ekspor; font system stack; sesi export HTML self-contained; docs embedded ≤ 250 KB brotli | `TestWebAssetsNoExternalURL`, `TestHelpOffline` | BELUM | — |
| GAP-UX-09 | kontrak server-request tunggal × 5 frontend dirender atau fallback terdefinisi; docs tergenerasi sinkron | `TestSurfaceParity`, `TestSurfaceDocSync` | BELUM | — |
| GAP-UX-10 | web responsif ≥ 360 px, target ≥ 44 px, PWA installable, service worker cache-only aset; remote link sekali pakai | `TestRemoteLinkOneTime`, `TestF8WebResponsive` | BELUM | — |
| GAP-INF-06 | suite eval deterministik seed tetap; regresi > 2 pp absolut/3% relatif gagal; run live ≤ `max_cost_usd` | `TestF8EvalDeterministic`, `TestF8EvalCostCap` | LULUS |
| GAP-INF-08 | 0 nilai i18n kosong; 0 literal UI di luar katalog; key `critical` terisi di 18 locale; set locale identik | `TestI18nNoEmpty`, `TestNoUILiterals`, `TestCriticalKeysAllLocales`, `TestLocaleSetIdentical` | BELUM | — |
| GAP-INF-09 | `[ui] reply_language` auto/follow_ui/kode; instruksi stable ≤ 12 tok; copy locale identik-en < 2% | `TestReplyLanguageLine`, `TestLocaleNotEnglishCopy` | BELUM | — |
## 10. EC F8 (0)

Tidak ada EC F8: `docs/ROADMAP.md:58-67` hanya memetakan `EC-PROV`, `EC-TCALL`, `EC-CTX`, `EC-STATE`, `EC-FIO`, `EC-TERM`, `EC-WIN`, `EC-PLAT`, `EC-CFG`, dan `EC-SEC` ke F1–F7; katalog EC tidak memuat fase F8. Karena itu tabel ini sengaja kosong dan tidak boleh diisi dengan status F7; baris EC F7 tetap milik `docs/F7-ACCEPTANCE.md` §10. `TestEdgeCaseCatalogSync` tetap berlaku: tidak ada `pending:F8` baru tanpa ADR.

## 11. Carryover F7→F8 (2)

| Item | Kriteria | Tes yang harus ada | Status |
|---|---|---|---|
| Smoke nyata nightly | adapter tanpa akun uji nyata tetap `N/A` selama `nightly.yml` tidak memuat `secrets.`/`vars.`/`DAMNBOT_SMOKE`; fake server tetap wajib | job `nightly` + `TestF7NightlySmokeMatrix` (daftar adapter × status akun) | N/A — no test account for any adapter (nightly smoke) (carryover F9) |
| Q14 image preflight | `BenchmarkImagePreflight` 12 MP ≤ 150 ms hanya sah diukur di Q14; target dan `TestImagePreflightTargetNotLowered` tidak berubah | pengukuran Q14 + artefak `bench/speed/results/` + `TestF7ImagePreflightQ14Artifact` | N/A — hardware: target hanya sah di Q14 (carryover F9) |

## 12. Perintah bukti

```sh
# PHASE kini F8: acceptance mensertifikasi fase yang ditinggalkan, F8 (ADR-176, ADR-330).
# Jalankan di mesin dengan alamat virtual >= 8 GiB; internal/ctxdb chaos OOM di 4 GiB.
make phase-acceptance

# Katalog EC dibaca dari paket pemilik tes, bukan dari dokumen; F8 tidak menambah entri.
DAMNBOT_PHASE_ACCEPTANCE=1 go test -tags goolm -count=1 ./internal/testutil/phase

# Baris yang disentuh satu PR (contoh).
go test -count=1 -run 'TestF8' ./internal/web/... ./internal/connectors/... ./internal/auto/... ./cmd/damnbot

# Gate penutupan ADR-330: aktif sejak PHASE>F8; hijau hanya tanpa BELUM/planned.
go test -count=1 -run 'TestF8LedgerClosed|TestF7LedgerClosed|TestParityPhaseColumn|TestF8LedgerGateRules' ./internal/archtest

# Budget/arsitektur tetap hijau setelah paket F8 bertambah.
make loc
make size-check
```

## 13. Definisi `N/A` (ADR-330 butir 4, meneruskan ADR-326 butir 4)

`N/A` bukan `LULUS` dan bukan jalan pintas. Bentuk yang diterima gate: `N/A — <alasan teknis ≥ 8 karakter> (carryover F9)`; alasan wajib menyebut prasyarat yang hilang dan fase target. Pemakaian yang **sah**:

- adapter tanpa akun uji nyata → `N/A — no test account (nightly smoke) (carryover F9)` pada baris *smoke akun nyata* saja; baris fake server tetap wajib `LULUS`.
- pengukuran hardware Q14 → `N/A — hardware: target hanya sah di Q14 (carryover F9)`.
- surface yang menunggu modul fase lain (mis. desktop Electron yang diganti web UI) → hanya bila ADR mencatat penggantinya; bukan alasan untuk melewatkan tes web UI.

Pemakaian yang **ditolak**: `N/A` telanjang, `N/A` tanpa alasan (gate menolak < 8 karakter), `N/A` untuk menyembunyikan tes yang gagal, dan `N/A` pada baris agregat yang anggotanya belum tertutup.

## 14. Angka

- Baris parity F8: **148** (CLI 14, SLASH 13, TOOL 23, TSET 10, SKILL 8, SKCAT 35, MEM 6, AUTO 12, UI 8, PLUG 10, OPS 9). Dari 148: §2 27, §3 23, §4 16, §5 8, §6 35, §7 20, §8 19.
- GAP F8: **17**; EC F8: **0**; carryover F7→F8: **2** (keduanya `N/A — … (carryover F9)`).
- Baris agregat §1: **7**. Jumlah baris data ledger: **167** (148 parity + 17 GAP + 0 EC + 2 carryover); total baris ledger termasuk agregat: **174**.
- Katalog SKCAT: **58 bundled + 153 optional = 211** skill; PLUG-009 = **645** entri.
- Budget F8: **21.800 Go** (`maxF8ModuleLOC`) + **≈9.900 JS/CSS** (≤ 10.000, SPEC §3.2.4); `maxModuleLOC` **132.000**.
- Ledger F7 ditutup dengan 131 baris data (129 `LULUS`, 2 `N/A`). F8 berjalan pada `cb7755167`: 167 baris data dengan **75 `LULUS`** (72 parity + 3 GAP), **90 `BELUM`**, dan 2 `N/A`; ketujuh baris agregat §1 masih `BELUM`.
