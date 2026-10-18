# FEATURE-PARITY — inventaris fitur hermes-agent

Checklist parity resmi Damn-Bot 1.0 terhadap `hermes-agent` (snapshot `868b9f244f`, 2026-10-17). Setiap baris memiliki sumber yang bisa ditelusuri di repo hermes.

## Ringkasan jumlah per kategori

| Prefix ID | Kategori | Jumlah baris |
|---|---|---|
| `CLI` | 1a. CLI — subcommand top-level | 75 |
| `SLASH` | 1b. CLI — slash command interaktif (`CommandDef`) | 103 |
| `TOOL` | 2a. TOOL — tool terdaftar di registry | 88 |
| `TSET` | 2b. TOOL — toolset | 62 |
| `PLAT` | 3. PLAT — adapter platform chat | 30 |
| `PROV` | 4. PROV — provider/model backend | 80 |
| `MEM` | 5. MEM/CTX — memori, sesi, konteks | 37 |
| `SKILL` | 6a. SKILL — sistem skill | 23 |
| `SKCAT` | 6b. SKILL — katalog skill bawaan per kategori | 35 |
| `AUTO` | 7. AUTO — fitur otomatis/background | 32 |
| `UI` | 8. UI — antarmuka | 28 |
| `PLUG` | 9a. PLUG — sistem plugin, MCP, connectors | 36 |
| `MCPX` | 9b. PLUG — optional MCPs (65 manifest) | 65 |
| `MEDIA` | 10. MEDIA — suara, TTS/STT, vision, generasi media | 31 |
| `EXEC` | 11. EXEC — eksekusi, sandbox, approval, secret | 44 |
| `OPS` | 12. OPS — operasional | 38 |
| `GW` | 13. GW — fitur inti gateway (tambahan) | 26 |
| `CORE` | 14. CORE — agent loop & runtime (tambahan) | 21 |
| **Total** | | **854** |

Angka kunci terverifikasi: **88 tool** (= `get_all_tool_names()`), **33 toolset** registry, **27 adapter** `plugins/platforms/` (+3 surface `gateway/platforms/`), **50 profil provider** (41 direktori), **72 subcommand** top-level, **103 slash command**, **58** skill bawaan + **153** optional skill, **65** optional MCP, **645** entri plugin-catalog, **17** bahasa i18n.

## Metode inventaris

Snapshot sumber: `hermes-agent` commit `868b9f244f` (2026-10-17). Repo hermes hanya dibaca. Semua skrip dijalankan dengan
`cwd=/home/jelangkung/Dokumen/hermes-agent`, `HERMES_HOME=/tmp/hm_parity`, `PYTHONDONTWRITEBYTECODE=1`.

```bash
export HERMES_HOME=/tmp/hm_parity PYTHONDONTWRITEBYTECODE=1
cd /home/jelangkung/Dokumen/hermes-agent

# TOOL + toolset (88 tool / 33 toolset)
python3 -c 'import model_tools as m, json; n=m.get_all_tool_names(); print(len(n), len(m.get_available_toolsets())); print(json.dumps({t: m.get_toolset_for_tool(t) for t in n}))'
# Lokasi registrasi: tools.registry.registry._snapshot_entries() → inspect.getsourcefile(entry.handler),
# lalu cari baris `register(` / `"name": "<tool>"` di file tersebut.

# CLI subcommand: patch argparse._SubParsersAction.add_parser untuk mencatat (nama, help, alias, file:line),
# lalu jalankan hermes_cli.main.main() dengan sys.argv=["hermes","--help"] (SystemExit ditangkap).
# Dibandingkan dengan _BUILTIN_SUBCOMMANDS (hermes_cli/main.py:2778).

# Slash command: semua instance CommandDef di modul hermes_cli.commands (dataclasses.asdict).
python3 -c 'import hermes_cli.commands as c; print(sum(len(v) for v in vars(c).values() if isinstance(v,(list,tuple)) and v and isinstance(v[0], c.CommandDef)))'

# Provider: providers.list_providers() + providers.get_provider_profile(n) for n in providers.unlisted_provider_names()
# Config default: dict DEFAULT di hermes_cli/config_defaults.py (dict UPPERCASE terbesar)

# Hitungan file
ls -d plugins/platforms/*/ | wc -l          # 27
ls -d plugins/model-providers/*/ | wc -l    # 41
ls optional-mcps | wc -l                    # 65
ls plugin-catalog/*.yaml | wc -l            # 646 (termasuk removed.yaml → 645 entri)
find skills -name SKILL.md | wc -l          # 58
find optional-skills -name SKILL.md | wc -l # 153
ls locales/*.yaml | grep -v tui              # 17 bahasa
```

Konvensi kolom:
- **status** (ditambahkan integrasi Spec 1.0, ADR-022/033/034): `planned` = wajib ada di 1.0; `dropped-with-approval (NP-xx)` = di-drop dengan persetujuan user tercatat di ADR-022 (tabel NP di akhir dokumen); `external-bridge (ADR-034)` = dipenuhi lewat proses terpisah opsional di luar binary `damnbot`. Rekap: planned 836, dropped-with-approval 17, external-bridge 1 (total 854). Penyebut gate parity SCORECARD [I] = 854 − 17 = 837 (planned + external-bridge). Kolom `modul`/`fase` diturunkan dari prefix lewat SPEC §8 dan tabel "Pemetaan parity → fase" di ROADMAP; kolom `test`/`bench` diisi saat fase pembangunnya (aturan di SPEC §8).
- **Sumber**: `path:line` bila spesifik (baris dihitung otomatis dari pola definisi), path saja bila fitur berbasis modul.
- **Default**: dari `hermes_cli/config_defaults.py` (kunci config dikutip), `toolsets.py` (`_HERMES_CORE_TOOLS`), dan `hermes_cli/tools_config.py:110` (`_DEFAULT_OFF_TOOLSETS`). "opt-in" = harus dipanggil/diaktifkan pengguna; "tersedia" = command selalu ada.
- Penanda `[INFERENCE]` = disimpulkan dari nama/struktur modul, tidak diverifikasi dengan membaca implementasi.
- Deskripsi CLI/SLASH dikutip verbatim (Inggris) dari help hermes agar dapat dicocokkan; deskripsi lain ditulis ulang dalam Bahasa Indonesia.

## 1a. CLI — subcommand top-level
Deskripsi = teks `help` hermes (verbatim, dipotong 15 kata) agar dapat dicocokkan. Sumber = lokasi `add_parser` (dicatat lewat patch `argparse`). Jumlah top-level unik: 72 + `help`, `--version`, subcommand plugin.
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| CLI-001 | `hermes chat` | Interactive chat with the agent | `hermes_cli/_parser.py:228` | tersedia | — | juga default saat `hermes` tanpa argumen | planned |
| CLI-002 | `hermes model` | Select default model and provider | `hermes_cli/subcommands/model.py:10` | tersedia | — |  | planned |
| CLI-003 | `hermes moa` | Configure Mixture of Agents provider/model slots | `hermes_cli/subcommands/moa.py:10` | tersedia | — | 3 sub: list, configure, delete | planned |
| CLI-004 | `hermes fallback` | Manage fallback providers (tried when the primary model fails) | `hermes_cli/subcommands/fallback.py:10` | tersedia | — | 4 sub: list, add, remove, clear | planned |
| CLI-005 | `hermes worktree` | Audit and reclaim accumulated git worktrees and merged branches | `hermes_cli/subcommands/worktree.py:8` | tersedia | — | 2 sub: list, prune | planned |
| CLI-006 | `hermes browser` | Real-profile browsing helpers (close a browser locking its profile) | `hermes_cli/subcommands/browser.py:10` | tersedia | Chromium-family browser | 1 sub: close-profile | planned |
| CLI-007 | `hermes secrets` | Manage external secret sources (Bitwarden, 1Password) | `hermes_cli/subcommands/secrets.py:8` | tersedia | CLI bw/op | 2 sub: bitwarden, onepassword | planned |
| CLI-008 | `hermes egress` | Manage the iron-proxy egress credential-injection firewall | `hermes_cli/subcommands/egress.py:10` | tersedia | iron-proxy | 9 sub: install, setup, start, stop, restart, reload, status, disable, config | planned |
| CLI-009 | `hermes migrate` | Migrate configuration for retired models or deprecated settings | `hermes_cli/subcommands/migrate.py:10` | tersedia | — | 2 sub: xai, relay | dropped-with-approval (NP-15) |
| CLI-010 | `hermes codex-runtime` | Manage the optional codex app-server runtime (migrate MCP config) | `hermes_cli/subcommands/codex_runtime.py:33` | tersedia | binary codex | 1 sub: migrate | planned |
| CLI-011 | `hermes gateway` | Messaging gateway management | `hermes_cli/subcommands/gateway.py:59` | tersedia | SDK platform terkait | 13 sub: run, ensure, start, stop, restart, status, install, uninstall, list, setup, migrate-legacy, migrate, enroll | planned |
| CLI-012 | `hermes proxy` | Local OpenAI-compatible proxy to OAuth providers | `hermes_cli/subcommands/gateway.py:237` | tersedia | — | 3 sub: start, status, providers | planned |
| CLI-013 | `hermes lsp` | Language Server Protocol management | `agent/lsp/cli.py:33` | tersedia | language servers | 6 sub: status, list, install, install-all, restart, which | planned |
| CLI-014 | `hermes setup` | Interactive setup wizard | `hermes_cli/subcommands/setup.py:10` | tersedia | — |  | planned |
| CLI-015 | `hermes whatsapp` | Set up WhatsApp integration | `hermes_cli/subcommands/whatsapp.py:10` | tersedia | Node.js bridge |  | planned |
| CLI-016 | `hermes whatsapp-cloud` | Set up WhatsApp Business Cloud API integration | `hermes_cli/subcommands/whatsapp.py:18` | tersedia | Meta Cloud API |  | planned |
| CLI-017 | `hermes slack` | Slack integration helpers (manifest generation, etc.) | `hermes_cli/subcommands/slack.py:10` | tersedia | Slack app | 1 sub: manifest | planned |
| CLI-018 | `hermes send` | Send a message to a configured platform (scripts, cron jobs, CI). | `hermes_cli/send_cmd.py:269` | tersedia | platform terkonfigurasi |  | planned |
| CLI-019 | `hermes login` | Deprecated. Use `hermes auth` to manage credentials, `hermes model` to select a provider, or `hermes … | `hermes_cli/subcommands/login.py:19` | tersedia (deprecated; redirect) | — |  | dropped-with-approval (NP-02) |
| CLI-020 | `hermes logout` | Clear authentication for an inference provider | `hermes_cli/subcommands/logout.py:10` | tersedia | — |  | planned |
| CLI-021 | `hermes auth` | Manage pooled provider credentials | `hermes_cli/subcommands/auth.py:10` | tersedia | — | 9 sub: add, list, remove, reset, priority, refresh, status, logout, upgrade | planned |
| CLI-022 | `hermes status` | Show status of all components | `hermes_cli/subcommands/status.py:10` | tersedia | — |  | planned |
| CLI-023 | `hermes pause` | Emergency stop: pause cron/kanban dispatch and new gateway turns | `hermes_cli/subcommands/pause.py:44` | tersedia | — |  | planned |
| CLI-024 | `hermes resume` | Lift the emergency stop set by `hermes pause` | `hermes_cli/subcommands/pause.py:53` | tersedia | — |  | planned |
| CLI-025 | `hermes cron` | Cron job management | `hermes_cli/subcommands/cron.py:16` | tersedia | — | 13 sub: list, create, edit, pause, resume, run, remove, status, runs, incidents, notepad, doctor, tick | planned |
| CLI-026 | `hermes webhook` | Manage dynamic webhook subscriptions | `hermes_cli/subcommands/webhook.py:10` | tersedia | — | 4 sub: subscribe, list, remove, test | planned |
| CLI-027 | `hermes peer` | Bot-to-bot DMs across machines (peer Hermes gateways) | `hermes_cli/subcommands/peer.py:425` | tersedia | Network | 7 sub: add, list, remove, dm, run, status, stop | planned |
| CLI-028 | `hermes portal` | Set up Nous Portal (login, model pick, Tool Gateway); see also `portal info` | `hermes_cli/portal_cli.py:178` | tersedia | Nous Portal | 5 sub: login, info, status, open, tools | dropped-with-approval (NP-19) |
| CLI-029 | `hermes kanban` | Multi-profile collaboration board (tasks, links, comments) | `hermes_cli/kanban_parser.py:459` | tersedia | — | 45 sub: init, boards, create, swarm, list, show, assign, set-model, reclaim, reassign, diagnostics, link, unlink, claim, comment, attach, attachments, attach-rm, complete, edit, block, schedule, unblock, request-review, request-changes, reopen-review, promote, archive, tail, dispatch, daemon, watch, stats, notify-subscribe, notify-list, notify-unsubscribe, log, runs, heartbeat, assignees, context, specify, decompose, gc, repair | planned |
| CLI-030 | `hermes project` | Manage projects (named, multi-folder workspaces) | `hermes_cli/projects_cmd.py:14` | tersedia | — | 11 sub: create, list, show, add-folder, remove-folder, rename, set-primary, use, archive, restore, bind-board | planned |
| CLI-031 | `hermes hooks` | Inspect and manage shell-script hooks | `hermes_cli/subcommands/hooks.py:10` | tersedia | — | 4 sub: list, test, revoke, doctor | planned |
| CLI-032 | `hermes doctor` | Check configuration and dependencies | `hermes_cli/subcommands/doctor.py:10` | tersedia | — |  | planned |
| CLI-033 | `hermes verify` | Detect a project's run recipe and smoke-test it | `hermes_cli/subcommands/verify.py:21` | tersedia | — |  | planned |
| CLI-034 | `hermes security` | Supply-chain audit (OSV.dev) for venv, plugins, and MCP servers | `hermes_cli/subcommands/security.py:12` | tersedia | OSV.dev | 1 sub: audit | planned |
| CLI-035 | `hermes approvals` | Approval-prompt tools (mine history into allowlist proposals) | `hermes_cli/subcommands/approvals.py:13` | tersedia | — | 2 sub: suggest, test | planned |
| CLI-036 | `hermes dump` | Dump setup summary for support/debugging | `hermes_cli/subcommands/dump.py:10` | tersedia | — |  | planned |
| CLI-037 | `hermes debug` | Debug tools — upload logs and system info for support | `hermes_cli/subcommands/debug.py:11` | tersedia | Network (upload) | 2 sub: share, delete | planned |
| CLI-038 | `hermes backup` | Back up Hermes home directory to a zip file | `hermes_cli/subcommands/backup.py:18` | tersedia | — |  | planned |
| CLI-039 | `hermes checkpoints` | Inspect / prune / clear ~/.hermes/checkpoints/ | `hermes_cli/subcommands/checkpoints.py:8` | tersedia | — | 5 sub: status, list, prune, clear, clear-legacy | planned |
| CLI-040 | `hermes import` | Restore a Hermes backup from a zip file | `hermes_cli/subcommands/import_cmd.py:10` | tersedia | — |  | planned |
| CLI-041 | `hermes import-agent` | Import a Claude Code or Codex CLI setup into Hermes | `hermes_cli/subcommands/import_agent.py:12` | tersedia | Claude Code/Codex config |  | planned |
| CLI-042 | `hermes config` | View and edit configuration | `hermes_cli/subcommands/config.py:12` | tersedia | — | 9 sub: show, edit, get, set, unset, path, env-path, check, migrate | planned |
| CLI-043 | `hermes skin` | List, switch, and tweak skins | `hermes_cli/subcommands/skin.py:10` | tersedia | — | 3 sub: list, use, set | planned |
| CLI-044 | `hermes console` | Open the safe Hermes command console | `hermes_cli/subcommands/console.py:10` | tersedia | — |  | planned |
| CLI-045 | `hermes pairing` | Manage DM pairing codes for user authorization | `hermes_cli/subcommands/pairing.py:10` | tersedia | — | 4 sub: list, approve, revoke, clear-pending | planned |
| CLI-046 | `hermes skills` | Search, install, configure, and manage skills | `hermes_cli/subcommands/skills.py:22` | tersedia | Network (hub) | 21 sub: trust, untrust, browse, search, install, inspect, list, check, update, audit, uninstall, reset, list-modified, diff, opt-out, opt-in, repair-official, publish, snapshot, tap, config | planned |
| CLI-047 | `hermes bundles` | Create, list, and manage skill bundles (aliases for multiple skills) | `hermes_cli/subcommands/bundles.py:8` | tersedia | — | 5 sub: list, show, create, delete, reload | planned |
| CLI-048 | `hermes plugins` | Manage and validate plugins | `hermes_cli/subcommands/plugins.py:12` | tersedia | git | 16 sub: install, search, browse, validate, update, adopt, trust-update-url, check-updates, remove, list, enable, disable, capabilities, doctor, pack, show | planned |
| CLI-049 | `hermes curator` | Background skill maintenance (curator) — status, run, pause, pin | `hermes_cli/subcommands/curator.py:10` | tersedia | — | 17 sub: status, usage, run, pause, resume, pin, unpin, list-unmanaged, adopt, restore, list-archived, archive, prune, backup, rollback, ledger, purge | planned |
| CLI-050 | `hermes pets` | Browse, install, and select petdex animated pets | `hermes_cli/subcommands/pets.py:10` | tersedia | Network (petdex) | 8 sub: list, install, select, show, off, scale, remove, doctor | planned |
| CLI-051 | `hermes journey` | Timeline of learned skills + memories over time | `hermes_cli/subcommands/journey.py:10` | tersedia | — | alias: learning, memory-graph; 3 sub: list, delete, edit | planned |
| CLI-052 | `hermes memory` | Configure external memory provider | `hermes_cli/subcommands/memory.py:12` | tersedia | — | 4 sub: setup, status, off, reset | planned |
| CLI-053 | `hermes tools` | Configure which tools are enabled per platform | `hermes_cli/subcommands/tools.py:10` | tersedia | — | 4 sub: list, disable, enable, post-setup | planned |
| CLI-054 | `hermes computer-use` | Manage the Computer Use (cua-driver) backend (macOS/Windows/Linux) | `hermes_cli/subcommands/computer_use.py:127` | tersedia | cua-driver | 5 sub: install, status, doctor, permissions, screen | planned |
| CLI-055 | `hermes mcp` | Manage MCP servers and run Hermes as an MCP server | `hermes_cli/subcommands/mcp.py:13` | tersedia | extra `mcp` | 11 sub: serve, add, remove, list, test, configure, login, reauth, picker, catalog, install | planned |
| CLI-056 | `hermes connectors` | Manage hosted app connectors and their accounts | `hermes_cli/subcommands/connectors.py:10` | tersedia | Nous Portal | 5 sub: list, accounts, connect, rename, disconnect | planned |
| CLI-057 | `hermes sessions` | Manage session history (list, rename, export, prune, delete) | `hermes_cli/subcommands/sessions.py:33` | tersedia | — | 23 sub: list, export, delete, prune, archive, optimize, clean-markers, optimize-storage, repair, set-journal-mode, repair-routing, repair-profiles, repair-prompts, recover, stats, rename, discard, pin, unpin, pinned, retitle-skills, browse, import | planned |
| CLI-058 | `hermes insights` | Show usage insights and analytics | `hermes_cli/subcommands/insights.py:10` | tersedia | — |  | planned |
| CLI-059 | `hermes usage` | Show account rate-limit windows (the /usage block) without starting a session | `hermes_cli/subcommands/usage.py:63` | tersedia | — |  | planned |
| CLI-060 | `hermes monitoring` | Inspect gateway monitoring (health & diagnostics export) | `hermes_cli/subcommands/monitoring.py:10` | tersedia | — | 1 sub: status | planned |
| CLI-061 | `hermes claw` | OpenClaw migration tools | `hermes_cli/subcommands/claw.py:12` | tersedia | instalasi OpenClaw | 2 sub: migrate, cleanup | planned |
| CLI-062 | `hermes vault` | Manage the local encrypted autofill vault (add/list/rm credentials) | `hermes_cli/subcommands/vault.py:8` | tersedia | cryptography; op/bw opsional | 4 sub: add, list, rm, sources | planned |
| CLI-063 | `hermes update` | Update Hermes Agent to the latest version | `hermes_cli/subcommands/update.py:11` | tersedia | git/uv |  | planned |
| CLI-064 | `hermes uninstall` | Uninstall Hermes Agent | `hermes_cli/subcommands/uninstall.py:13` | tersedia | — |  | planned |
| CLI-065 | `hermes acp` | Run Hermes Agent as an ACP (Agent Client Protocol) server | `hermes_cli/subcommands/acp.py:12` | tersedia | extra `acp` |  | planned |
| CLI-066 | `hermes profile` | Manage profiles — multiple isolated Hermes instances | `hermes_cli/subcommands/profile.py:10` | tersedia | — | 15 sub: list, use, create, delete, describe, show, alias, rename, purge-identity, migrate-identity, export, import, install, update, info | planned |
| CLI-067 | `hermes completion` | Print shell completion script (bash, zsh, or fish) | `hermes_cli/subcommands/completion.py:10` | tersedia | — |  | planned |
| CLI-068 | `hermes dashboard` | Start the web UI dashboard | `hermes_cli/subcommands/dashboard.py:79` | tersedia | extra `web`, Node (build web) | 1 sub: register | planned |
| CLI-069 | `hermes serve` | Start the Hermes backend server (headless; powers the desktop app and remote backends) | `hermes_cli/subcommands/dashboard.py:95` | tersedia | extra `web` |  | planned |
| CLI-070 | `hermes desktop` | Build and launch the native desktop app | `hermes_cli/subcommands/gui.py:10` | tersedia | Node.js, Electron | alias: gui | planned |
| CLI-071 | `hermes logs` | View and filter Hermes log files | `hermes_cli/subcommands/logs.py:11` | tersedia | — |  | planned |
| CLI-072 | `hermes prompt-size` | Show a byte breakdown of the system prompt + tool schemas | `hermes_cli/subcommands/prompt_size.py:12` | tersedia | — |  | planned |
| CLI-073 | `hermes help` | Alias bantuan (ditangani khusus, bukan subparser) | `hermes_cli/main.py:2797` | tersedia | — | ada di `_BUILTIN_SUBCOMMANDS`, tidak via add_parser | planned |
| CLI-074 | `hermes --version` / `version` | Tampilkan versi | `hermes_cli/main.py:2217` | tersedia | — | cmd_version | planned |
| CLI-075 | Plugin-registered subcommands | Subcommand dari plugin (mis. `hermes photon`) | `hermes_cli/main.py:2825` | per plugin | — | deferred platform CLI (#54678) | planned |

## 1b. CLI — slash command interaktif (`CommandDef`)
Sumber: `hermes_cli/commands.py` (103 `CommandDef`). Deskripsi verbatim hermes, dipotong 15 kata. Selain ini setiap skill/bundle terpasang menjadi `/<nama>` dinamis (lihat SKILL) dan plugin dapat mendaftarkan command.
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| SLASH-001 | `/start` | Acknowledge platform start pings without a reply | `hermes_cli/commands.py:85` | tersedia (gateway-only); kategori Session | — |  | planned |
| SLASH-002 | `/new` | Start a new session (fresh session ID + history) | `hermes_cli/commands.py:87` | tersedia (CLI + gateway); kategori Session | — | alias: /reset; args `[name]` | planned |
| SLASH-003 | `/topic` | Enable or inspect Telegram DM topic sessions | `hermes_cli/commands.py:90` | tersedia (gateway-only); kategori Session | — | args `[off\\|help\\|session-id]` | planned |
| SLASH-004 | `/clear` | Clear screen and start a new session | `hermes_cli/commands.py:92` | tersedia (CLI-only); kategori Session | — |  | planned |
| SLASH-005 | `/redraw` | Force a full UI repaint (recovers from terminal drift) | `hermes_cli/commands.py:94` | tersedia (CLI-only); kategori Session | — |  | planned |
| SLASH-006 | `/history` | Show conversation history | `hermes_cli/commands.py:96` | tersedia (CLI-only); kategori Session | — |  | planned |
| SLASH-007 | `/save` | Export the current conversation (bare /save shows usage) | `hermes_cli/commands.py:98` | tersedia (CLI + gateway); kategori Session | — | args `<json\\|md\\|html> [filename] [redact]` | planned |
| SLASH-008 | `/retry` | Retry the last message (resend to agent) | `hermes_cli/commands.py:100` | tersedia (CLI + gateway); kategori Session | — |  | planned |
| SLASH-009 | `/prompt` | Compose your next prompt in $EDITOR (markdown), then send it | `hermes_cli/commands.py:101` | tersedia (CLI-only); kategori Session | — | alias: /compose; args `[initial text]` | planned |
| SLASH-010 | `/undo` | Back up N user turns and re-prompt (default 1) | `hermes_cli/commands.py:103` | tersedia (CLI + gateway); kategori Session | — | args `[N]` | planned |
| SLASH-011 | `/title` | Set a title for the current session | `hermes_cli/commands.py:105` | tersedia (CLI + gateway); kategori Session | — | args `[name]` | planned |
| SLASH-012 | `/handoff` | Hand off this session to a messaging platform (Telegram, Discord, etc.) | `hermes_cli/commands.py:106` | tersedia (CLI-only); kategori Session | — | args `<platform>` | planned |
| SLASH-013 | `/branch` | Branch the current session (new thread on Discord/Telegram/Slack/Matrix; --here stays here) | `hermes_cli/commands.py:108` | tersedia (CLI + gateway); kategori Session | — | alias: /fork; args `[--here] [name]` | planned |
| SLASH-014 | `/worktree` | Show, list, create, or prune isolated git worktrees | `hermes_cli/commands.py:110` | tersedia (CLI-only); kategori Session | — | sub: new, list, prune; args `[new [name]\\|list\\|prune [--dry-run]]` | planned |
| SLASH-015 | `/compress` | Compress conversation context (add 'here [N]' to keep recent N turns; --preview shows what would … | `hermes_cli/commands.py:113` | tersedia (CLI + gateway); kategori Session | — | alias: /compact; args `[here [N] \\| focus topic \\| --preview\\|--dry-run]` | planned |
| SLASH-016 | `/rollback` | List or restore filesystem checkpoints (restores keep your hand-edits; --all overrides) | `hermes_cli/commands.py:115` | tersedia (CLI + gateway); kategori Session | — | args `[number] [--all]` | planned |
| SLASH-017 | `/snapshot` | Create or restore state snapshots of Hermes config/state | `hermes_cli/commands.py:117` | tersedia (CLI-only); kategori Session | — | alias: /snap; args `[create\\|restore <id>\\|prune]` | planned |
| SLASH-018 | `/export` | Export a profile (config, skills, theme) to a shareable archive | `hermes_cli/commands.py:120` | tersedia (CLI-only); kategori Configuration | — | args `[profile] [-o output.tar.gz]` | planned |
| SLASH-019 | `/import` | Import a shared profile archive as a new profile | `hermes_cli/commands.py:122` | tersedia (CLI-only); kategori Configuration | — | args `<archive.tar.gz> [--name <name>]` | planned |
| SLASH-020 | `/stop` | Kill all running background processes | `hermes_cli/commands.py:124` | tersedia (CLI + gateway); kategori Session | — |  | planned |
| SLASH-021 | `/pause` | Pause new work globally (emergency stop); '/pause off' resumes | `hermes_cli/commands.py:126` | tersedia (gateway-only); kategori Session | — | args `[reason \\| off]` | planned |
| SLASH-022 | `/approve` | Approve a pending dangerous command | `hermes_cli/commands.py:128` | tersedia (gateway-only); kategori Session | — | args `[session\\|always]` | planned |
| SLASH-023 | `/deny` | Deny a pending dangerous command (optionally with a reason) | `hermes_cli/commands.py:130` | tersedia (gateway-only); kategori Session | — | args `[all] [reason]` | planned |
| SLASH-024 | `/bg` | Run a prompt in a separate background session | `hermes_cli/commands.py:132` | tersedia (CLI + gateway); kategori Session | — | args `<prompt>` | planned |
| SLASH-025 | `/btw` | Ask a side question about the current conversation without interrupting it | `hermes_cli/commands.py:134` | tersedia (CLI + gateway); kategori Session | — | args `<question>` | planned |
| SLASH-026 | `/agents` | Show active agents and running tasks | `hermes_cli/commands.py:136` | tersedia (CLI + gateway); kategori Session | — | alias: /tasks | planned |
| SLASH-027 | `/journey` | Open the learning journey timeline | `hermes_cli/commands.py:138` | tersedia (CLI-only); kategori Session | — | alias: /learning, /memory-graph; sub: list, delete, edit; args `[list\\|delete <id>\\|edit <id>]` | planned |
| SLASH-028 | `/queue` | Queue a prompt for the next turn, or list/edit/rm/move/clear queued prompts | `hermes_cli/commands.py:141` | tersedia (CLI + gateway); kategori Session | — | alias: /q; sub: list, edit, rm, move, clear, add; args `[<prompt>\\|list\\|edit N <prompt>\\|rm N\\|move A B\\|clear]` | planned |
| SLASH-029 | `/steer` | Inject a message after the next tool call without interrupting | `hermes_cli/commands.py:145` | tersedia (CLI + gateway); kategori Session | — | alias: /s; args `<prompt>` | planned |
| SLASH-030 | `/goal` | Set a standing goal Hermes works on across turns until achieved | `hermes_cli/commands.py:147` | tersedia (CLI + gateway); kategori Session | — | args `[text \\| -- <text> \\| draft <text> \\| show \\| gate add <cmd> \\| pause \\| resume \\| clear \\| status \\| wait <pid> \\| unwait]` | planned |
| SLASH-031 | `/heartbeat` | Set a recurring prompt that re-enters this session when idle | `hermes_cli/commands.py:150` | tersedia (CLI + gateway); kategori Session | — | alias: /hb; sub: status, pause, resume, clear; args `[every <interval> <prompt> \\| status \\| pause \\| resume \\| clear]` | planned |
| SLASH-032 | `/refine` | Review this conversation now and save lessons to memory/skills | `hermes_cli/commands.py:154` | tersedia (CLI + gateway); kategori Session | — | args `[focus instructions]` | planned |
| SLASH-033 | `/review` | Spawn an independent subagent to review the work just discussed (PR, code, docs) | `hermes_cli/commands.py:156` | tersedia (CLI + gateway); kategori Session | — | args `[review instructions]` | planned |
| SLASH-034 | `/loop` | Re-run a prompt on a recurring interval in this session | `hermes_cli/commands.py:158` | tersedia (CLI + gateway); kategori Session | — | alias: /proactive; args `[interval] <prompt> [--times N] [--until <condition>] \\| status \\| pause \\| resume \\| stop` | planned |
| SLASH-035 | `/plan` | Write a markdown implementation plan to .hermes/plans/ without executing anything | `hermes_cli/commands.py:162` | tersedia (CLI + gateway); kategori Session | — | args `[task]` | planned |
| SLASH-036 | `/moa` | Run one prompt through the default Mixture of Agents preset, then restore your model | `hermes_cli/commands.py:164` | tersedia (CLI + gateway); kategori Session | — | args `<prompt>` | planned |
| SLASH-037 | `/subgoal` | Add or manage extra criteria on the active goal | `hermes_cli/commands.py:166` | tersedia (CLI + gateway); kategori Session | — | args `[text \\| remove N \\| clear]` | planned |
| SLASH-038 | `/status` | Show session, model, token, and context info | `hermes_cli/commands.py:168` | tersedia (CLI + gateway); kategori Session | — |  | planned |
| SLASH-039 | `/egress` | Show Docker egress proxy status | `hermes_cli/commands.py:170` | tersedia (CLI + gateway); kategori Session | — | sub: status; args `[status]` | planned |
| SLASH-040 | `/context` | Show detailed context window view with usage gauge, category breakdown, compression stats, and throughput | `hermes_cli/commands.py:173` | tersedia (CLI + gateway); kategori Session | — | alias: /ctx; sub: all; args `[all]` | planned |
| SLASH-041 | `/whoami` | Show your slash command access (admin / user) | `hermes_cli/commands.py:175` | tersedia (CLI + gateway); kategori Info | — |  | planned |
| SLASH-042 | `/profile` | Show active profile name and home directory | `hermes_cli/commands.py:176` | tersedia (CLI + gateway); kategori Info | — |  | planned |
| SLASH-043 | `/sethome` | Set this chat as the home channel | `hermes_cli/commands.py:178` | tersedia (gateway-only); kategori Session | — | alias: /set-home | planned |
| SLASH-044 | `/resume` | Resume a previously-named session | `hermes_cli/commands.py:180` | tersedia (CLI + gateway); kategori Session | — | args `[name]` | planned |
| SLASH-045 | `/sessions` | Browse and resume previous sessions | `hermes_cli/commands.py:182` | tersedia (CLI + gateway); kategori Session | — |  | planned |
| SLASH-046 | `/config` | Show current configuration | `hermes_cli/commands.py:185` | tersedia (CLI-only); kategori Configuration | — |  | planned |
| SLASH-047 | `/model` | Switch model (session-scoped; --global to persist) | `hermes_cli/commands.py:187` | tersedia (CLI + gateway); kategori Configuration | — | args `[model] [--provider name] [--reasoning level] [--global\\|--session] [--refresh]` | planned |
| SLASH-048 | `/codex-runtime` | Toggle codex app-server runtime for OpenAI/Codex models | `hermes_cli/commands.py:190` | tersedia (CLI + gateway); kategori Configuration | — | alias: /codex_runtime; args `[auto\\|codex_app_server]` | planned |
| SLASH-049 | `/personality` | Set a predefined personality | `hermes_cli/commands.py:193` | tersedia (CLI + gateway); kategori Configuration | — | args `[name]` | planned |
| SLASH-050 | `/statusbar` | Toggle the context/model status bar | `hermes_cli/commands.py:195` | tersedia (CLI-only); kategori Configuration | — | alias: /sb | planned |
| SLASH-051 | `/battery` | Toggle a color-coded battery indicator in the status bar | `hermes_cli/commands.py:197` | tersedia (CLI-only); kategori Configuration | — | sub: on, off, status; args `[on\\|off\\|status]` | planned |
| SLASH-052 | `/timestamps` | Toggle [HH:MM] timestamps on messages and /history | `hermes_cli/commands.py:200` | tersedia (CLI-only); kategori Configuration | — | alias: /ts; sub: on, off, status; args `[on\\|off\\|status]` | planned |
| SLASH-053 | `/diff` | Show git changes in the working directory | `hermes_cli/commands.py:203` | tersedia (CLI + gateway); kategori Info | — | sub: staged, all, session; args `[staged\\|all\\|session] [--stat] [path...]` | planned |
| SLASH-054 | `/verbose` | Cycle tool progress display: off -> new -> all -> verbose | `hermes_cli/commands.py:206` | tersedia (CLI-only); kategori Configuration | — | gate: `display.tool_progress_command` | planned |
| SLASH-055 | `/focus` | Toggle focus view — show only your prompt and the final response | `hermes_cli/commands.py:209` | tersedia (CLI-only); kategori Configuration | — | sub: on, off, status; args `[on\\|off\\|status]` | planned |
| SLASH-056 | `/footer` | Toggle gateway runtime-metadata footer on final replies | `hermes_cli/commands.py:212` | tersedia (CLI + gateway); kategori Configuration | — | sub: on, off, status; args `[on\\|off\\|status]` | planned |
| SLASH-057 | `/yolo` | Toggle YOLO mode (skip all dangerous command approvals) | `hermes_cli/commands.py:215` | tersedia (CLI + gateway); kategori Configuration | — |  | planned |
| SLASH-058 | `/approvals` | Show or set the persistent dangerous-command approval mode | `hermes_cli/commands.py:217` | tersedia (CLI + gateway); kategori Configuration | — | sub: manual, smart, off; args `[manual\\|smart\\|off]` | planned |
| SLASH-059 | `/reasoning` | Manage reasoning effort and display | `hermes_cli/commands.py:220` | tersedia (CLI + gateway); kategori Configuration | — | sub: none, minimal, low, medium, high, xhigh, max, ultra, show, hide, on, off, full, clamp, --global; args `[level\\|show\\|hide\\|full\\|clamp] [--global]` | planned |
| SLASH-060 | `/fast` | Fast mode — OpenAI Priority Processing / Anthropic Fast Mode (normal/fast/auto/cold/ultrafast) | `hermes_cli/commands.py:223` | tersedia (CLI + gateway); kategori Configuration | — | sub: normal, fast, auto, cold, ultrafast, ultra, status, on, off, --global; args `[normal\\|fast\\|auto\\|cold\\|ultrafast\\|status] [--global]` | planned |
| SLASH-061 | `/skin` | Show or change the display skin/theme | `hermes_cli/commands.py:227` | tersedia (CLI-only); kategori Configuration | — | args `[name]` | planned |
| SLASH-062 | `/indicator` | Pick the TUI busy-indicator style | `hermes_cli/commands.py:229` | tersedia (CLI-only); kategori Configuration | — | sub: ascii, emoji, kaomoji, unicode; args `[ascii\\|emoji\\|kaomoji\\|unicode]` | planned |
| SLASH-063 | `/voice` | Toggle voice mode | `hermes_cli/commands.py:232` | tersedia (CLI + gateway); kategori Configuration | — | sub: on, off, tts, status; args `[on\\|off\\|tts\\|status]` | planned |
| SLASH-064 | `/wake` | Toggle the 'Hey Hermes' wake word listener | `hermes_cli/commands.py:235` | tersedia (CLI-only); kategori Configuration | — | sub: on, off, status; args `[on\\|off\\|status]` | planned |
| SLASH-065 | `/busy` | Control how messages behave while Hermes is working | `hermes_cli/commands.py:237` | tersedia (CLI + gateway); kategori Configuration | — | sub: queue, steer, interrupt, status; args `[queue\\|steer\\|interrupt\\|status]` | planned |
| SLASH-066 | `/autoprune` | Turn automatic pruning of old sessions on or off | `hermes_cli/commands.py:241` | tersedia (CLI + gateway); kategori Configuration | — | sub: on, off, later, status; args `[on\\|off\\|later\\|status]` | planned |
| SLASH-067 | `/tools` | Manage tools: /tools [list\|disable\|enable] [name...] | `hermes_cli/commands.py:246` | tersedia (CLI-only); kategori Tools & Skills | — | args `[list\\|disable\\|enable] [name...]` | planned |
| SLASH-068 | `/toolsets` | List available toolsets | `hermes_cli/commands.py:248` | tersedia (CLI-only); kategori Tools & Skills | — |  | planned |
| SLASH-069 | `/skills` | Search, install, inspect, or manage skills | `hermes_cli/commands.py:250` | tersedia (CLI-only); kategori Tools & Skills | — | sub: search, browse, inspect, install, audit, pending, approve, reject, diff, approval; gate: `skills.write_approval` | planned |
| SLASH-070 | `/memory` | Review pending memory writes / toggle the approval gate | `hermes_cli/commands.py:262` | tersedia (CLI + gateway); kategori Tools & Skills | — | sub: pending, approve, reject, approval; args `[pending\\|approve\\|reject\\|approval] [id\\|on\\|off]` | planned |
| SLASH-071 | `/bundles` | List skill bundles (aliases /<name> for multiple skills) | `hermes_cli/commands.py:265` | tersedia (CLI + gateway); kategori Tools & Skills | — |  | planned |
| SLASH-072 | `/pet` | Toggle or adopt a petdex mascot (/pet, /pet list, /pet <slug>) | `hermes_cli/commands.py:267` | tersedia (CLI-only); kategori Tools & Skills | — | sub: toggle, list, scale, off; args `[toggle\\|list\\|scale <n>\\|<slug>]` | planned |
| SLASH-073 | `/hatch` | Generate a new petdex pet from a description | `hermes_cli/commands.py:269` | tersedia (CLI-only); kategori Tools & Skills | — | alias: /generate-pet; args `[description]` | planned |
| SLASH-074 | `/learn` | Learn a reusable skill from anything you describe (dirs, URLs, this chat, notes) | `hermes_cli/commands.py:271` | tersedia (CLI + gateway); kategori Tools & Skills | — | args `<what to learn from>` | planned |
| SLASH-075 | `/init` | Generate or update AGENTS.md project instructions from a repo scan | `hermes_cli/commands.py:273` | tersedia (CLI + gateway); kategori Tools & Skills | — | args `[notes]` | planned |
| SLASH-076 | `/cron` | Manage scheduled tasks | `hermes_cli/commands.py:275` | tersedia (CLI-only); kategori Tools & Skills | — | sub: list, add, create, edit, pause, resume, run, remove; args `[subcommand]` | planned |
| SLASH-077 | `/suggestions` | Review suggested automations (accept/dismiss) | `hermes_cli/commands.py:279` | tersedia (CLI + gateway); kategori Tools & Skills | — | alias: /suggest; sub: accept, dismiss, catalog, clear; args `[accept\\|dismiss N \\| catalog]` | planned |
| SLASH-078 | `/blueprint` | Set up an automation from a blueprint template | `hermes_cli/commands.py:282` | tersedia (CLI + gateway); kategori Tools & Skills | — | alias: /bp; args `[name] [slot=value ...]` | planned |
| SLASH-079 | `/curator` | Background skill maintenance (status, run, pin, archive, list-archived) | `hermes_cli/commands.py:284` | tersedia (CLI + gateway); kategori Tools & Skills | — | sub: status, run, pause, resume, pin, unpin, restore, list-archived; args `[subcommand]` | planned |
| SLASH-080 | `/kanban` | Multi-profile collaboration board (tasks, links, comments) | `hermes_cli/commands.py:288` | tersedia (CLI + gateway); kategori Tools & Skills | — | sub: init, boards, create, list, ls, show, assign, reclaim, reassign, diagnostics, diag, link, unlink, claim, comment, complete, edit, block, unblock, archive, tail, dispatch, stats, notify-subscribe, notify-list, notify-unsubscribe, log, runs, heartbeat, assignees, context, specify, gc; args `[subcommand]` | planned |
| SLASH-081 | `/reload` | Reload .env variables into the running session | `hermes_cli/commands.py:297` | tersedia (CLI-only); kategori Tools & Skills | — |  | planned |
| SLASH-082 | `/reload-mcp` | Reload MCP servers from config | `hermes_cli/commands.py:299` | tersedia (CLI + gateway); kategori Tools & Skills | — | alias: /reload_mcp | planned |
| SLASH-083 | `/reload-skills` | Re-scan ~/.hermes/skills/ for newly installed or removed skills | `hermes_cli/commands.py:301` | tersedia (CLI + gateway); kategori Tools & Skills | — | alias: /reload_skills | planned |
| SLASH-084 | `/browser` | Connect browser tools to your live Chromium-family browser via CDP, or switch to Browser Use … | `hermes_cli/commands.py:303` | tersedia (CLI-only); kategori Tools & Skills | — | sub: connect, disconnect, status, use; args `[connect\\|disconnect\\|status\\|use]` | planned |
| SLASH-085 | `/plugins` | List installed plugins and their status | `hermes_cli/commands.py:306` | tersedia (CLI-only); kategori Tools & Skills | — |  | planned |
| SLASH-086 | `/commands` | Browse all commands and skills (paginated) | `hermes_cli/commands.py:310` | tersedia (gateway-only); kategori Info | — | args `[page]` | planned |
| SLASH-087 | `/help` | Show available commands (/help skills lists skill commands, /help <text> filters) | `hermes_cli/commands.py:313` | tersedia (CLI + gateway); kategori Info | — | args `[skills\\|<filter>]` | planned |
| SLASH-088 | `/palette` | Open the fuzzy command palette (also Ctrl+P) | `hermes_cli/commands.py:315` | tersedia (CLI-only); kategori Info | — |  | planned |
| SLASH-089 | `/restart` | Gracefully restart the gateway after draining active runs | `hermes_cli/commands.py:317` | tersedia (gateway-only); kategori Session | — |  | planned |
| SLASH-090 | `/usage` | Show token usage and rate limits; `reset` redeems a banked Codex limit reset | `hermes_cli/commands.py:319` | tersedia (CLI + gateway); kategori Info | — | args `[reset [--force]]` | planned |
| SLASH-091 | `/subscription` | View your Nous plan and change it in the browser | `hermes_cli/commands.py:321` | tersedia (CLI-only); kategori Info | — | alias: /upgrade | dropped-with-approval (NP-19) |
| SLASH-092 | `/login` | Sign in with a Nous account (keeps your connectors) | `hermes_cli/commands.py:323` | tersedia (CLI + gateway); kategori Info | — |  | dropped-with-approval (NP-19) |
| SLASH-093 | `/topup` | Show your Nous balance and manage billing on the portal | `hermes_cli/commands.py:325` | tersedia (CLI + gateway); kategori Info | — |  | dropped-with-approval (NP-19) |
| SLASH-094 | `/insights` | Show usage insights and analytics | `hermes_cli/commands.py:326` | tersedia (CLI + gateway); kategori Info | — | args `[days]` | planned |
| SLASH-095 | `/platforms` | Show gateway/messaging platform status | `hermes_cli/commands.py:328` | tersedia (CLI-only); kategori Info | — | alias: /gateway | planned |
| SLASH-096 | `/platform` | Pause, resume, or list a failing gateway platform | `hermes_cli/commands.py:330` | tersedia (gateway-only); kategori Info | — | args `<pause\\|resume\\|list> [name]` | planned |
| SLASH-097 | `/copy` | Copy the last assistant response to clipboard | `hermes_cli/commands.py:332` | tersedia (CLI-only); kategori Info | — | args `[number]` | planned |
| SLASH-098 | `/paste` | Attach clipboard image from your clipboard | `hermes_cli/commands.py:334` | tersedia (CLI-only); kategori Info | — |  | planned |
| SLASH-099 | `/image` | Attach a local image file for your next prompt | `hermes_cli/commands.py:336` | tersedia (CLI-only); kategori Info | — | args `<path>` | planned |
| SLASH-100 | `/update` | Update Hermes Agent to the latest version | `hermes_cli/commands.py:338` | tersedia (CLI + gateway); kategori Info | — |  | planned |
| SLASH-101 | `/version` | Show Hermes Agent version | `hermes_cli/commands.py:340` | tersedia (CLI + gateway); kategori Info | — | alias: /v | planned |
| SLASH-102 | `/debug` | Upload debug report (system info + logs) and get shareable links | `hermes_cli/commands.py:342` | tersedia (CLI + gateway); kategori Info | — | args `[nous\\|local]` | planned |
| SLASH-103 | `/quit` | Exit the CLI (use --delete to also remove session history) | `hermes_cli/commands.py:346` | tersedia (CLI-only); kategori Exit | — | alias: /exit; args `[--delete]` | planned |

## 2a. TOOL — tool terdaftar di registry
Diambil dari `model_tools.get_all_tool_names()` = **88 tool**; toolset dari `get_toolset_for_tool()`. Sumber = baris `registry.register`/schema `"name"`. Tool MCP dinamis dan tool plugin katalog TIDAK termasuk.
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| TOOL-001 | `a2a_call` (toolset `a2a`) | Kirim tugas ke agent remote via protokol A2A | `plugins/platforms/a2a/tools.py:172` | off (default-off toolset) | Platform A2A aktif (plugins/platforms/a2a) | check_fn `_a2a_tools_available` | planned |
| TOOL-002 | `a2a_discover` (toolset `a2a`) | Temukan agent A2A dan kapabilitasnya (agent card) | `plugins/platforms/a2a/tools.py:146` | off (default-off toolset) | Platform A2A aktif (plugins/platforms/a2a) | check_fn `_a2a_tools_available` | planned |
| TOOL-003 | `a2a_history` (toolset `a2a`) | Riwayat interaksi A2A dengan agent remote | `plugins/platforms/a2a/tools.py:223` | off (default-off toolset) | Platform A2A aktif (plugins/platforms/a2a) | check_fn `_a2a_tools_available` | planned |
| TOOL-004 | `a2a_list` (toolset `a2a`) | Daftar agent A2A yang dikenal/terkonfigurasi | `plugins/platforms/a2a/tools.py:199` | off (default-off toolset) | Platform A2A aktif (plugins/platforms/a2a) | check_fn `_a2a_tools_available` | planned |
| TOOL-005 | `a2a_orchestrate` (toolset `a2a`) | Orkestrasi multi-agent A2A untuk satu tugas | `plugins/platforms/a2a/tools.py:254` | off (default-off toolset) | Platform A2A aktif (plugins/platforms/a2a) | check_fn `_a2a_tools_available` | planned |
| TOOL-006 | `browser_back` (toolset `browser`) | Navigasi mundur di tab browser | `tools/browser_tool.py:554` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_back_requirements` | planned |
| TOOL-007 | `browser_click` (toolset `browser`) | Klik elemen halaman via ref snapshot | `tools/browser_tool.py:510` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_click_requirements` | planned |
| TOOL-008 | `browser_console` (toolset `browser`) | Baca log console / evaluasi JS di halaman | `tools/browser_tool.py:601` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_requirements` | planned |
| TOOL-009 | `browser_get_images` (toolset `browser`) | Daftar gambar pada halaman aktif | `tools/browser_tool.py:575` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_requirements` | planned |
| TOOL-010 | `browser_navigate` (toolset `browser`) | Buka URL di sesi browser terkelola | `tools/browser_tool.py:481` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_navigate_requirements` | planned |
| TOOL-011 | `browser_press` (toolset `browser`) | Tekan tombol keyboard di halaman | `tools/browser_tool.py:561` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_press_requirements` | planned |
| TOOL-012 | `browser_scroll` (toolset `browser`) | Scroll halaman atau elemen | `tools/browser_tool.py:541` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_scroll_requirements` | planned |
| TOOL-013 | `browser_snapshot` (toolset `browser`) | Snapshot accessibility tree halaman dengan ref elemen | `tools/browser_tool.py:495` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_snapshot_requirements` | planned |
| TOOL-014 | `browser_type` (toolset `browser`) | Ketik teks ke field input | `tools/browser_tool.py:524` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_type_requirements` | planned |
| TOOL-015 | `browser_vault` (toolset `browser`) | Autofill kredensial dari vault ke form login | `tools/browser_vault_tool.py:658` | opt-in; deferred via tool_search | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `_check_vault_available` | planned |
| TOOL-016 | `browser_vision` (toolset `browser`) | Screenshot halaman lalu analisis dengan model vision | `tools/browser_tool.py:582` | opt-in | agent-browser CLI + Chromium lokal, atau cloud (Browserbase/Browser Use/Firecrawl), Camofox/Lightpanda opsional | check_fn `check_browser_vision_requirements` | planned |
| TOOL-017 | `browser_cdp` (toolset `browser-cdp`) | Kirim perintah raw Chrome DevTools Protocol | `tools/browser_cdp_tool.py:397` | opt-in | Endpoint CDP Chrome/Brave/Edge (`/browser connect`) | check_fn `_browser_cdp_check` | planned |
| TOOL-018 | `browser_dialog` (toolset `browser-cdp`) | Tanggapi dialog JS native (alert/confirm/prompt) | `tools/browser_dialog_tool.py:102` | opt-in | Endpoint CDP Chrome/Brave/Edge (`/browser connect`) | check_fn `_browser_dialog_check` | planned |
| TOOL-019 | `browser_exec` (toolset `browser-use`) | Jalankan Python helper Browser Use CLI untuk mengendalikan browser | `tools/browser_use_cli.py:953` | opt-in | Browser Use CLI (`browser.backend: browser-use`) | check_fn `is_browser_use_cli_mode`; menggantikan tool browser lain bila `browser.backend: browser-use` | planned |
| TOOL-020 | `manage_catalog` (toolset `catalog`) | Minta instalasi plugin/skill katalog lewat kartu approval (desktop) | `tools/connectors/tool.py:172` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app | tanpa check_fn | planned |
| TOOL-021 | `clarify` (toolset `clarify`) | Tanya pengguna (pilihan ganda/terbuka) saat ambigu | `tools/clarify_tool.py:195` | opt-in | UI interaktif (CLI/TUI/gateway) | check_fn `check_clarify_requirements` | planned |
| TOOL-022 | `execute_code` (toolset `code_execution`) | Jalankan skrip Python yang memanggil tool secara programatik | `tools/code_execution_tool.py:966` | opt-in | Interpreter Python (kernel lokal/remote) | check_fn `check_sandbox_requirements` | planned |
| TOOL-023 | `computer_use` (toolset `computer_use`) | Kendali desktop: screenshot, mouse, keyboard via cua-driver | `tools/computer_use_tool.py:14` | opt-in; deferred via tool_search | binary cua-driver (macOS/Windows/Linux); extra `computer-use` | check_fn `check_computer_use_requirements` | planned |
| TOOL-024 | `manage_connections` (toolset `connections`) | Kelola akun connector hosted dan link otorisasi | `tools/connectors/tool.py:154` | opt-in | Hosted connectors service (Nous Portal) | check_fn `<lambda>` | planned |
| TOOL-025 | `cronjob_manage` (toolset `cronjob`) | Buat/list/ubah/pause/hapus/trigger cron job terjadwal | `tools/cronjob_tools.py:1193` | opt-in; deferred via tool_search | Scheduler cron (gateway atau `hermes cron tick`) | check_fn `check_cronjob_requirements`; alias legacy `cronjob` | planned |
| TOOL-026 | `delegate_task` (toolset `delegation`) | Spawn subagent dengan konteks terisolasi untuk subtugas | `tools/delegate_tool.py:800` | opt-in | Akses LLM (provider utama/delegation.provider) | check_fn `check_delegate_requirements`; diintersep agent loop (`model_tools.py:607`) | planned |
| TOOL-027 | `annotate_preview` (toolset `desktop_ui`) | Anotasi elemen di pane preview desktop | `tools/annotate_preview_tool.py:91` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-028 | `close_terminal` (toolset `desktop_ui`) | Tutup pane terminal desktop | `tools/close_terminal_tool.py:47` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-029 | `desktop_preview` (toolset `desktop_ui`) | Buka file/URL di pane preview desktop | `tools/preview_tool.py:73` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-030 | `drive_preview` (toolset `desktop_ui`) | Kendalikan halaman pane preview (klik, ketik, elemen) | `tools/drive_preview_tool.py:144` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-031 | `focus_pane` (toolset `desktop_ui`) | Fokuskan pane GUI tertentu | `tools/focus_pane_tool.py:25` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-032 | `gui_tour` (toolset `desktop_ui`) | Jalankan tur GUI terpandu | `tools/tour_tool.py:160` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | check_fn `check_tours_enabled`; alias legacy `tour` | planned |
| TOOL-033 | `react_to_message` (toolset `desktop_ui`) | Beri reaksi emoji pada pesan di GUI | `tools/react_to_message_tool.py:113` | client-surface (sesi GUI saja) | Desktop app (Electron) | check_fn `check_react_requirements` | planned |
| TOOL-034 | `read_terminal` (toolset `desktop_ui`) | Baca output pane terminal desktop | `tools/read_terminal_tool.py:76` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-035 | `read_window_below` (toolset `desktop_ui`) | Baca jendela/pane di bawah chat | `tools/read_window_tool.py:43` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-036 | `set_theme` (toolset `desktop_ui`) | Ganti tema tampilan desktop | `tools/set_theme_tool.py:91` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | tanpa check_fn | planned |
| TOOL-037 | `show_tip` (toolset `desktop_ui`) | Tampilkan tip UI kontekstual | `tools/tip_tool.py:78` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app (Electron) | check_fn `check_tips_enabled`; alias legacy `tip` | planned |
| TOOL-038 | `discord` (toolset `discord`) | Baca/partisipasi Discord: ambil pesan, cari member, buat thread | `tools/discord_tool.py:631` | off (default-off toolset) | DISCORD_BOT_TOKEN, discord.py | check_fn `check_discord_tool_requirements` | planned |
| TOOL-039 | `discord_admin` (toolset `discord_admin`) | Admin server Discord: channel, role, pin | `tools/discord_tool.py:631` | off (default-off toolset) | DISCORD_BOT_TOKEN, discord.py | check_fn `check_discord_tool_requirements` | planned |
| TOOL-040 | `feishu_doc_read` (toolset `feishu_doc`) | Baca isi dokumen Feishu/Lark | `tools/feishu_doc_tool.py:68` | on hanya di bundle platform terkait | lark-oapi + kredensial app Feishu | check_fn `_check_feishu` | planned |
| TOOL-041 | `feishu_drive_add_comment` (toolset `feishu_drive`) | Tambah komentar dokumen Feishu | `tools/feishu_drive_tool.py:145` | on hanya di bundle platform terkait | lark-oapi + kredensial app Feishu | check_fn `_check_feishu` | planned |
| TOOL-042 | `feishu_drive_list_comment_replies` (toolset `feishu_drive`) | Daftar balasan komentar Feishu | `tools/feishu_drive_tool.py:95` | on hanya di bundle platform terkait | lark-oapi + kredensial app Feishu | check_fn `_check_feishu` | planned |
| TOOL-043 | `feishu_drive_list_comments` (toolset `feishu_drive`) | Daftar komentar dokumen Feishu | `tools/feishu_drive_tool.py:67` | on hanya di bundle platform terkait | lark-oapi + kredensial app Feishu | check_fn `_check_feishu` | planned |
| TOOL-044 | `feishu_drive_reply_comment` (toolset `feishu_drive`) | Balas komentar dokumen Feishu | `tools/feishu_drive_tool.py:119` | on hanya di bundle platform terkait | lark-oapi + kredensial app Feishu | check_fn `_check_feishu` | planned |
| TOOL-045 | `patch` (toolset `file`) | Edit file via find/replace (fuzzy match) atau patch | `tools/file_tools.py:1429` | opt-in | — | check_fn `_check_file_reqs` | planned |
| TOOL-046 | `read_file` (toolset `file`) | Baca file teks dengan paginasi baris | `tools/file_tools.py:1405` | opt-in | — | check_fn `_check_file_reqs` | planned |
| TOOL-047 | `search_files` (toolset `file`) | Cari konten (regex) dan nama file | `tools/file_tools.py:1430` | opt-in | — | check_fn `_check_file_reqs` | planned |
| TOOL-048 | `write_file` (toolset `file`) | Tulis/timpa file utuh | `tools/file_tools.py:1406` | opt-in | — | check_fn `_check_file_reqs` | planned |
| TOOL-049 | `yb_query_group_info` (toolset `hermes-yuanbao`) | Info grup Yuanbao | `tools/yuanbao_tools.py:281` | on hanya di bundle platform terkait | Platform Yuanbao aktif | check_fn `_check_yuanbao`; nama toolset di TOOLSETS: `yuanbao`/`hermes-yuanbao` | planned |
| TOOL-050 | `yb_query_group_members` (toolset `hermes-yuanbao`) | Daftar member grup Yuanbao | `tools/yuanbao_tools.py:302` | on hanya di bundle platform terkait | Platform Yuanbao aktif | check_fn `_check_yuanbao`; nama toolset di TOOLSETS: `yuanbao`/`hermes-yuanbao` | planned |
| TOOL-051 | `yb_search_sticker` (toolset `hermes-yuanbao`) | Cari stiker Yuanbao | `tools/yuanbao_tools.py:417` | on hanya di bundle platform terkait | Platform Yuanbao aktif | check_fn `_check_yuanbao`; nama toolset di TOOLSETS: `yuanbao`/`hermes-yuanbao` | planned |
| TOOL-052 | `yb_send_dm` (toolset `hermes-yuanbao`) | Kirim DM Yuanbao | `tools/yuanbao_tools.py:349` | on hanya di bundle platform terkait | Platform Yuanbao aktif | check_fn `_check_yuanbao`; nama toolset di TOOLSETS: `yuanbao`/`hermes-yuanbao` | planned |
| TOOL-053 | `yb_send_sticker` (toolset `hermes-yuanbao`) | Kirim stiker Yuanbao | `tools/yuanbao_tools.py:448` | on hanya di bundle platform terkait | Platform Yuanbao aktif | check_fn `_check_yuanbao`; nama toolset di TOOLSETS: `yuanbao`/`hermes-yuanbao` | planned |
| TOOL-054 | `image_generate` (toolset `image_gen`) | Generate gambar teks-ke-gambar via backend terpilih | `tools/image_generation_tool.py:991` | opt-in; deferred via tool_search | Backend image_gen (FAL_KEY/OPENAI/XAI/OpenRouter/DeepInfra/Krea/Meta) | check_fn `check_image_generation_requirements` | planned |
| TOOL-055 | `kanban_attach` (toolset `kanban`) | Lampirkan file ke task kanban | `tools/kanban_tools.py:986` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-056 | `kanban_attach_url` (toolset `kanban`) | Lampirkan URL ke task kanban | `tools/kanban_tools.py:1042` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-057 | `kanban_attachments` (toolset `kanban`) | Daftar lampiran task kanban | `tools/kanban_tools.py:1064` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-058 | `kanban_block` (toolset `kanban`) | Tandai task terblokir menunggu input manusia | `tools/kanban_tools.py:811` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-059 | `kanban_comment` (toolset `kanban`) | Komentar pada thread task kanban | `tools/kanban_tools.py:949` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-060 | `kanban_complete` (toolset `kanban`) | Selesaikan task dengan handoff terstruktur | `tools/kanban_tools.py:725` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-061 | `kanban_create` (toolset `kanban`) | Buat task kanban (fan-out orkestrator) | `tools/kanban_tools.py:1094` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-062 | `kanban_heartbeat` (toolset `kanban`) | Heartbeat worker selama operasi panjang | `tools/kanban_tools.py:932` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-063 | `kanban_link` (toolset `kanban`) | Tautkan dependensi antar task | `tools/kanban_tools.py:1274` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-064 | `kanban_list` (toolset `kanban`) | Daftar task board (orkestrator) | `tools/kanban_tools.py:695` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_orchestrator_mode` | planned |
| TOOL-065 | `kanban_request_changes` (toolset `kanban`) | Kembalikan task review dengan permintaan perubahan | `tools/kanban_tools.py:919` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-066 | `kanban_request_review` (toolset `kanban`) | Ajukan task ke tahap review | `tools/kanban_tools.py:873` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-067 | `kanban_schedule` (toolset `kanban`) | Jadwalkan task kanban | `tools/kanban_tools.py:852` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode`; TIDAK ada di `_HERMES_CORE_TOOLS` | planned |
| TOOL-068 | `kanban_show` (toolset `kanban`) | Tampilkan detail task kanban | `tools/kanban_tools.py:662` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_mode` | planned |
| TOOL-069 | `kanban_unblock` (toolset `kanban`) | Buka blokir task (orkestrator) | `tools/kanban_tools.py:1260` | off (default-off toolset) | Dispatcher kanban (env HERMES_KANBAN_TASK) | check_fn `_check_kanban_orchestrator_mode` | planned |
| TOOL-070 | `memory` (toolset `memory`) | Tambah/ganti/hapus catatan memori persisten dan profil user | `tools/memory_tool.py:477` | opt-in | — | check_fn `check_memory_requirements`; diintersep agent loop (`model_tools.py:607`) | planned |
| TOOL-071 | `setup_beat` (toolset `onboarding`) | Kartu onboarding first-run (desktop) | `tools/setup_beat_tool.py:209` | client-surface (sesi GUI saja) | Desktop app | tanpa check_fn | planned |
| TOOL-072 | `desktop_project` (toolset `project`) | Buat/ganti Desktop Project (workspace bernama) | `tools/project_tools.py:165` | client-surface (sesi GUI saja); deferred via tool_search | Desktop app | tanpa check_fn | planned |
| TOOL-073 | `session_search` (toolset `session_search`) | Cari dan rangkum percakapan lampau (FTS) | `tools/session_search_tool.py:791` | opt-in; deferred via tool_search | SQLite FTS5 (state.db) | check_fn `check_session_search_requirements`; diintersep agent loop (`model_tools.py:607`) | planned |
| TOOL-074 | `skill_manage` (toolset `skills`) | Buat/edit/hapus skill (SKILL.md dan file pendukung) | `tools/skill_manager_tool.py:926` | opt-in | — | tanpa check_fn | planned |
| TOOL-075 | `skill_view` (toolset `skills`) | Muat isi skill beserta file terkait | `tools/skills_tool.py:742` | opt-in | — | check_fn `check_skills_requirements` | planned |
| TOOL-076 | `skills_list` (toolset `skills`) | Daftar skill tersedia (nama + deskripsi) | `tools/skills_tool.py:710` | opt-in | — | check_fn `check_skills_requirements` | planned |
| TOOL-077 | `process_manage` (toolset `terminal`) | Kelola proses background: list, poll, log, kill, wait | `tools/process_registry.py:2761` | opt-in; deferred via tool_search | Shell lokal; Docker/Singularity/Modal/Daytona/SSH/Vercel untuk backend lain | tanpa check_fn; alias legacy `process` | planned |
| TOOL-078 | `terminal` (toolset `terminal`) | Jalankan perintah shell di backend terminal terpilih | `tools/terminal_tool.py:1646` | opt-in | Shell lokal; Docker/Singularity/Modal/Daytona/SSH/Vercel untuk backend lain | check_fn `check_terminal_requirements` | planned |
| TOOL-079 | `todo_list` (toolset `todo`) | Daftar tugas/checklist untuk kerja multi-langkah | `tools/todo_tool.py:333` | opt-in; deferred via tool_search | — | check_fn `check_todo_requirements`; alias legacy `todo`; diintersep agent loop (`model_tools.py:607`) | planned |
| TOOL-080 | `text_to_speech` (toolset `tts`) | Ubah teks menjadi audio via provider TTS | `tools/tts_tool.py:674` | opt-in | edge-tts (default, gratis) / ElevenLabs / OpenAI / xAI / lokal (Piper, NeuTTS, KittenTTS) | check_fn `check_tts_requirements` | planned |
| TOOL-081 | `video_analyze` (toolset `video`) | Analisis/pahami video dengan model multimodal | `tools/vision_tools.py:1185` | off (default-off toolset) | Model multimodal video | check_fn `check_video_requirements` | planned |
| TOOL-082 | `video_generate` (toolset `video_gen`) | Generate video teks/gambar/referensi-ke-video | `tools/video_generation_tool.py:371` | off (default-off toolset) | Backend video_gen (FAL/xAI/OpenRouter/DeepInfra) API key | check_fn `check_video_generation_requirements` | planned |
| TOOL-083 | `xai_video_edit` (toolset `video_gen`) | Edit video via xAI Grok Imagine | `tools/xai_video_tools.py:110` | off (default-off toolset) | Backend video_gen (FAL/xAI/OpenRouter/DeepInfra) API key | check_fn `_check_xai_video_requirements` | planned |
| TOOL-084 | `xai_video_extend` (toolset `video_gen`) | Perpanjang video via xAI Grok Imagine | `tools/xai_video_tools.py:114` | off (default-off toolset) | Backend video_gen (FAL/xAI/OpenRouter/DeepInfra) API key | check_fn `_check_xai_video_requirements` | planned |
| TOOL-085 | `vision_analyze` (toolset `vision`) | Analisis gambar (URL/path) dengan model vision | `tools/vision_tools.py:1030` | opt-in | Model vision (auxiliary.vision) | check_fn `check_vision_requirements` | planned |
| TOOL-086 | `web_extract` (toolset `web`) | Ekstrak konten halaman web menjadi markdown | `tools/web_tools.py:566` | opt-in | API key backend search (Exa/Tavily/Parallel/Perplexity/Firecrawl/Keenable) atau backend keyless (ddgs/SearXNG/Brave free) | check_fn `check_web_api_key` | planned |
| TOOL-087 | `web_search` (toolset `web`) | Pencarian web via backend search terpilih | `tools/web_tools.py:560` | opt-in | API key backend search (Exa/Tavily/Parallel/Perplexity/Firecrawl/Keenable) atau backend keyless (ddgs/SearXNG/Brave free) | check_fn `check_web_api_key` | planned |
| TOOL-088 | `x_search` (toolset `x_search`) | Cari post/thread X (Twitter) via xAI x_search | `tools/x_search_tool.py:340` | off (default-off toolset) | XAI_API_KEY atau SuperGrok OAuth | check_fn `check_x_search_requirements` | planned |

## 2b. TOOL — toolset
33 toolset dari `get_available_toolsets()` + toolset komposit/bundle di `TOOLSETS` (`toolsets.py:74`).
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| TSET-001 | toolset `todo` | 1 tool | `toolsets.py:128` | on/sesuai tool | — | tools: todo_list; available saat inventaris=True | planned |
| TSET-002 | toolset `desktop_ui` | 11 tool | `toolsets.py:139` | client-surface (GUI) | — | tools: annotate_preview, close_terminal, drive_preview, focus_pane, desktop_preview, react_to_message, read_terminal, read_window_below, set_theme, show_tip, gui_tour; available saat inventaris=True | planned |
| TSET-003 | toolset `browser-cdp` | 2 tool | `tools/browser_cdp_tool.py` (registry) | on/sesuai tool | — | tools: browser_cdp, browser_dialog; available saat inventaris=False | planned |
| TSET-004 | toolset `browser-use` | 1 tool | `tools/browser_use_cli.py` (registry) | on/sesuai tool | — | tools: browser_exec; available saat inventaris=False | planned |
| TSET-005 | toolset `browser` | 11 tool | `toolsets.py:112` | on/sesuai tool | — | tools: browser_navigate, browser_snapshot, browser_click, browser_type, browser_scroll, browser_back, browser_press, browser_get_images, browser_vision, browser_console, browser_vault; available saat inventaris=False | planned |
| TSET-006 | toolset `clarify` | 1 tool | `toolsets.py:154` | on/sesuai tool | — | tools: clarify; available saat inventaris=True | planned |
| TSET-007 | toolset `terminal` | 2 tool | `toolsets.py:102` | on/sesuai tool | — | tools: process_manage, terminal; available saat inventaris=True | planned |
| TSET-008 | toolset `code_execution` | 1 tool | `toolsets.py:155` | on/sesuai tool | — | tools: execute_code; available saat inventaris=True | planned |
| TSET-009 | toolset `computer_use` | 1 tool | `toolsets.py:96` | on/sesuai tool | — | tools: computer_use; available saat inventaris=False | planned |
| TSET-010 | toolset `connections` | 1 tool | `toolsets.py:132` | on/sesuai tool | — | tools: manage_connections; available saat inventaris=True | planned |
| TSET-011 | toolset `catalog` | 1 tool | `toolsets.py:149` | client-surface (GUI) | — | tools: manage_catalog; available saat inventaris=True | planned |
| TSET-012 | toolset `cronjob` | 1 tool | `toolsets.py:117` | on/sesuai tool | — | tools: cronjob_manage; available saat inventaris=False | planned |
| TSET-013 | toolset `delegation` | 1 tool | `toolsets.py:156` | on/sesuai tool | — | tools: delegate_task; available saat inventaris=True | planned |
| TSET-014 | toolset `discord` | 1 tool | `toolsets.py:167` | off (default-off) | — | tools: discord; available saat inventaris=False | planned |
| TSET-015 | toolset `discord_admin` | 1 tool | `toolsets.py:168` | off (default-off) | — | tools: discord_admin; available saat inventaris=False | planned |
| TSET-016 | toolset `feishu_doc` | 1 tool | `toolsets.py:170` | on/sesuai tool | — | tools: feishu_doc_read; available saat inventaris=False | planned |
| TSET-017 | toolset `feishu_drive` | 4 tool | `toolsets.py:171` | on/sesuai tool | — | tools: feishu_drive_list_comments, feishu_drive_list_comment_replies, feishu_drive_reply_comment, feishu_drive_add_comment; available saat inventaris=False | planned |
| TSET-018 | toolset `file` | 4 tool | `toolsets.py:122` | on/sesuai tool | — | tools: read_file, write_file, patch, search_files; available saat inventaris=True | planned |
| TSET-019 | toolset `image_gen` | 1 tool | `toolsets.py:88` | on/sesuai tool | — | tools: image_generate; available saat inventaris=False | planned |
| TSET-020 | toolset `kanban` | 15 tool | `toolsets.py:157` | off (default-off) | — | tools: kanban_show, kanban_list, kanban_complete, kanban_block, kanban_schedule, kanban_request_review, kanban_request_changes, kanban_heartbeat, kanban_comment, kanban_attach, kanban_attach_url, kanban_attachments, kanban_create, kanban_unblock, kanban_link; available saat inventaris=False | planned |
| TSET-021 | toolset `memory` | 1 tool | `toolsets.py:129` | on/sesuai tool | — | tools: memory; available saat inventaris=True | planned |
| TSET-022 | toolset `project` | 1 tool | `toolsets.py:133` | client-surface (GUI) | — | tools: desktop_project; available saat inventaris=True | planned |
| TSET-023 | toolset `session_search` | 1 tool | `toolsets.py:131` | on/sesuai tool | — | tools: session_search; available saat inventaris=True | planned |
| TSET-024 | toolset `onboarding` | 1 tool | `toolsets.py:146` | client-surface (GUI) | — | tools: setup_beat; available saat inventaris=True | planned |
| TSET-025 | toolset `skills` | 3 tool | `toolsets.py:103` | on/sesuai tool | — | tools: skill_manage, skills_list, skill_view; available saat inventaris=True | planned |
| TSET-026 | toolset `tts` | 1 tool | `toolsets.py:127` | on/sesuai tool | — | tools: text_to_speech; available saat inventaris=True | planned |
| TSET-027 | toolset `video_gen` | 3 tool | `toolsets.py:89` | off (default-off) | — | tools: video_generate, xai_video_edit, xai_video_extend; available saat inventaris=False | planned |
| TSET-028 | toolset `vision` | 1 tool | `toolsets.py:86` | on/sesuai tool | — | tools: vision_analyze; available saat inventaris=False | planned |
| TSET-029 | toolset `video` | 1 tool | `toolsets.py:87` | off (default-off) | — | tools: video_analyze; available saat inventaris=False | planned |
| TSET-030 | toolset `web` | 2 tool | `toolsets.py:76` | on/sesuai tool | — | tools: web_search, web_extract; available saat inventaris=True | planned |
| TSET-031 | toolset `x_search` | 1 tool | `toolsets.py:78` | off (default-off) | — | tools: x_search; available saat inventaris=False | planned |
| TSET-032 | toolset `hermes-yuanbao` | 5 tool | `toolsets.py:225` | on/sesuai tool | — | tools: yb_query_group_info, yb_query_group_members, yb_send_dm, yb_search_sticker, yb_send_sticker; available saat inventaris=False | planned |
| TSET-033 | toolset `a2a` | 5 tool | `plugins/platforms/a2a/tools.py` (registry) | off (default-off) | — | tools: a2a_discover, a2a_call, a2a_list, a2a_history, a2a_orchestrate; available saat inventaris=False | planned |
| TSET-034 | toolset `search` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:77` | sesuai platform/posture | — |  | planned |
| TSET-035 | toolset `context_engine` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:130` | sesuai platform/posture | — | tanpa tool registry sendiri | planned |
| TSET-036 | toolset `bot_room` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:134` | sesuai platform/posture | — | tanpa tool registry sendiri | planned |
| TSET-037 | toolset `yuanbao` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:169` | sesuai platform/posture | — |  | dropped-with-approval (NP-07) |
| TSET-038 | toolset `debugging` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:174` | sesuai platform/posture | — |  | planned |
| TSET-039 | toolset `safe` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:175` | sesuai platform/posture | — |  | planned |
| TSET-040 | toolset `coding` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:181` | sesuai platform/posture | — |  | planned |
| TSET-041 | toolset `hermes-acp` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:191` | sesuai platform/posture | — |  | planned |
| TSET-042 | toolset `hermes-api-server` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:196` | sesuai platform/posture | — |  | planned |
| TSET-043 | toolset `hermes-cli` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:201` | sesuai platform/posture | — |  | planned |
| TSET-044 | toolset `hermes-cron` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:205` | sesuai platform/posture | — |  | planned |
| TSET-045 | toolset `hermes-telegram` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:206` | sesuai platform/posture | — |  | planned |
| TSET-046 | toolset `hermes-discord` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:207` | sesuai platform/posture | — |  | planned |
| TSET-047 | toolset `hermes-whatsapp` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:212` | sesuai platform/posture | — |  | planned |
| TSET-048 | toolset `hermes-slack` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:213` | sesuai platform/posture | — |  | planned |
| TSET-049 | toolset `hermes-signal` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:214` | sesuai platform/posture | — |  | planned |
| TSET-050 | toolset `hermes-bluebubbles` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:215` | sesuai platform/posture | — |  | planned |
| TSET-051 | toolset `hermes-email` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:216` | sesuai platform/posture | — |  | planned |
| TSET-052 | toolset `hermes-mattermost` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:217` | sesuai platform/posture | — |  | planned |
| TSET-053 | toolset `hermes-matrix` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:218` | sesuai platform/posture | — |  | planned |
| TSET-054 | toolset `hermes-dingtalk` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:219` | sesuai platform/posture | — |  | planned |
| TSET-055 | toolset `hermes-feishu` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:220` | sesuai platform/posture | — |  | planned |
| TSET-056 | toolset `hermes-weixin` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:221` | sesuai platform/posture | — |  | planned |
| TSET-057 | toolset `hermes-qqbot` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:222` | sesuai platform/posture | — |  | planned |
| TSET-058 | toolset `hermes-wecom` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:223` | sesuai platform/posture | — |  | planned |
| TSET-059 | toolset `hermes-wecom-callback` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:224` | sesuai platform/posture | — |  | planned |
| TSET-060 | toolset `hermes-sms` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:231` | sesuai platform/posture | — |  | planned |
| TSET-061 | toolset `hermes-webhook` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:232` | sesuai platform/posture | — |  | planned |
| TSET-062 | toolset `hermes-gateway` (komposit/bundle) | Toolset komposit/bundle platform (bukan toolset registry) | `toolsets.py:233` | sesuai platform/posture | — |  | planned |

## 3. PLAT — adapter platform chat
27 direktori di `plugins/platforms/` (semua direktori, tiap punya `plugin.yaml`) + 3 surface inti di `gateway/platforms/`. `wecom` mendaftarkan 2 platform (`wecom`, `wecom_callback`).
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| PLAT-001 | A2A (`a2a`) | Protokol Agent-to-Agent v1.0 (inbound server + outbound tools) | `plugins/platforms/a2a/plugin.yaml`, `plugins/platforms/a2a/adapter.py` | opt-in (aktif bila env wajib diisi) | — | provides_tools: a2a_discover, a2a_call, a2a_list, a2a_history, a2a_orchestrate | planned |
| PLAT-002 | BlueBubbles (iMessage) (`bluebubbles`) | iMessage via server BlueBubbles macOS lokal | `plugins/platforms/bluebubbles/plugin.yaml`, `plugins/platforms/bluebubbles/adapter.py` | opt-in (aktif bila env wajib diisi) | BLUEBUBBLES_SERVER_URL, BLUEBUBBLES_PASSWORD; server BlueBubbles (macOS) |  | planned |
| PLAT-003 | Buzz (`buzz`) | Relay komunitas Buzz (berbasis Nostr) | `plugins/platforms/buzz/plugin.yaml`, `plugins/platforms/buzz/adapter.py` | opt-in (aktif bila env wajib diisi) | BUZZ_RELAY_URL, BUZZ_PRIVATE_KEY; — |  | planned |
| PLAT-004 | DingTalk (`dingtalk`) | DingTalk via dingtalk-stream (Stream Mode) | `plugins/platforms/dingtalk/plugin.yaml`, `plugins/platforms/dingtalk/adapter.py` | opt-in (aktif bila env wajib diisi) | DINGTALK_CLIENT_ID, DINGTALK_CLIENT_SECRET; extra `dingtalk` |  | planned |
| PLAT-005 | Discord (`discord`) | Discord guild/DM, thread, voice, slash commands | `plugins/platforms/discord/plugin.yaml`, `plugins/platforms/discord/adapter.py` | opt-in (aktif bila env wajib diisi) | DISCORD_BOT_TOKEN; extra `discord` (discord.py[voice]) |  | planned |
| PLAT-006 | Email (`email`) | Email via IMAP polling + SMTP reply | `plugins/platforms/email/plugin.yaml`, `plugins/platforms/email/adapter.py` | opt-in (aktif bila env wajib diisi) | EMAIL_ADDRESS, EMAIL_PASSWORD, EMAIL_SMTP_HOST; — |  | planned |
| PLAT-007 | Feishu / Lark (`feishu`) | Feishu/Lark via lark-oapi WebSocket/webhook | `plugins/platforms/feishu/plugin.yaml`, `plugins/platforms/feishu/adapter.py` | opt-in (aktif bila env wajib diisi) | FEISHU_APP_ID, FEISHU_APP_SECRET; extra `feishu` (lark-oapi) |  | planned |
| PLAT-008 | Google Chat (`google_chat`) | Google Chat via HTTP callback atau Pub/Sub | `plugins/platforms/google_chat/plugin.yaml`, `plugins/platforms/google_chat/adapter.py` | opt-in (aktif bila env wajib diisi) | GOOGLE_CHAT_SERVICE_ACCOUNT_JSON; extra `google-chat` (google-cloud-pubsub) |  | planned |
| PLAT-009 | IRC (`irc`) | Channel/DM IRC tanpa dependensi eksternal | `plugins/platforms/irc/plugin.yaml`, `plugins/platforms/irc/adapter.py` | opt-in (aktif bila env wajib diisi) | IRC_SERVER, IRC_CHANNEL, IRC_NICKNAME; — |  | planned |
| PLAT-010 | LINE (`line`) | LINE Messaging API via webhook aiohttp (HMAC) | `plugins/platforms/line/plugin.yaml`, `plugins/platforms/line/adapter.py` | opt-in (aktif bila env wajib diisi) | LINE_CHANNEL_ACCESS_TOKEN, LINE_CHANNEL_SECRET; aiohttp |  | planned |
| PLAT-011 | Matrix (`matrix`) | Matrix homeserver via mautrix, E2EE opsional | `plugins/platforms/matrix/plugin.yaml`, `plugins/platforms/matrix/adapter.py` | opt-in (aktif bila env wajib diisi) | MATRIX_HOMESERVER, MATRIX_ACCESS_TOKEN; extra `matrix` (mautrix, Linux) |  | planned |
| PLAT-012 | Mattermost (`mattermost`) | Mattermost REST v4 + WebSocket | `plugins/platforms/mattermost/plugin.yaml`, `plugins/platforms/mattermost/adapter.py` | opt-in (aktif bila env wajib diisi) | MATTERMOST_URL, MATTERMOST_TOKEN; — |  | planned |
| PLAT-013 | ntfy (`ntfy`) | Push notification ntfy (topic subscribe/publish) | `plugins/platforms/ntfy/plugin.yaml`, `plugins/platforms/ntfy/adapter.py` | opt-in (aktif bila env wajib diisi) | NTFY_TOPIC; — |  | planned |
| PLAT-014 | iMessage via Photon (`photon`) | iMessage via platform Photon Spectrum (sidecar) | `plugins/platforms/photon/plugin.yaml`, `plugins/platforms/photon/adapter.py` | opt-in (aktif bila env wajib diisi) | PHOTON_PROJECT_ID, PHOTON_PROJECT_SECRET; sidecar Photon |  | planned |
| PLAT-015 | QQ Bot (`qqbot`) | QQ Official Bot API v2 (WebSocket + REST) | `plugins/platforms/qqbot/plugin.yaml`, `plugins/platforms/qqbot/adapter.py` | opt-in (aktif bila env wajib diisi) | QQ_APP_ID, QQ_CLIENT_SECRET; — |  | planned |
| PLAT-016 | Raft (`raft`) | Workspace Raft via wake-channel bridge lokal | `plugins/platforms/raft/plugin.yaml`, `plugins/platforms/raft/adapter.py` | opt-in (aktif bila env wajib diisi) | RAFT_PROFILE; bridge Raft lokal |  | planned |
| PLAT-017 | Signal (`signal`) | Signal via daemon signal-cli HTTP (SSE + JSON-RPC) | `plugins/platforms/signal/plugin.yaml`, `plugins/platforms/signal/adapter.py` | opt-in (aktif bila env wajib diisi) | SIGNAL_HTTP_URL, SIGNAL_ACCOUNT; binary signal-cli |  | planned |
| PLAT-018 | SimpleX Chat (`simplex`) | SimpleX Chat via daemon WebSocket lokal | `plugins/platforms/simplex/plugin.yaml`, `plugins/platforms/simplex/adapter.py` | opt-in (aktif bila env wajib diisi) | SIMPLEX_WS_URL; daemon simplex-chat |  | planned |
| PLAT-019 | Slack (`slack`) | Slack Socket Mode via slack-bolt | `plugins/platforms/slack/plugin.yaml`, `plugins/platforms/slack/adapter.py` | opt-in (aktif bila env wajib diisi) | SLACK_BOT_TOKEN, SLACK_APP_TOKEN; extra `slack` (slack-bolt) |  | planned |
| PLAT-020 | SMS (Twilio) (`sms`) | SMS via Twilio REST + webhook | `plugins/platforms/sms/plugin.yaml`, `plugins/platforms/sms/adapter.py` | opt-in (aktif bila env wajib diisi) | TWILIO_ACCOUNT_SID, TWILIO_AUTH_TOKEN, TWILIO_PHONE_NUMBER; extra `sms` (aiohttp) |  | planned |
| PLAT-021 | Microsoft Teams (`teams`) | Microsoft Teams via Bot Framework | `plugins/platforms/teams/plugin.yaml`, `plugins/platforms/teams/adapter.py` | opt-in (aktif bila env wajib diisi) | TEAMS_CLIENT_ID, TEAMS_CLIENT_SECRET, TEAMS_TENANT_ID; extra `teams` (microsoft-teams-apps) |  | planned |
| PLAT-022 | Telegram (`telegram`) | Telegram chat/grup/topik, streaming edit, inline keyboard | `plugins/platforms/telegram/plugin.yaml`, `plugins/platforms/telegram/adapter.py` | opt-in (aktif bila env wajib diisi) | TELEGRAM_BOT_TOKEN; extra `telegram`/`messaging` (python-telegram-bot) |  | planned |
| PLAT-023 | WeCom (Enterprise WeChat) (`wecom`) | WeCom Smart Robot (WS) + callback app (2 platform) | `plugins/platforms/wecom/plugin.yaml`, `plugins/platforms/wecom/adapter.py` | opt-in (aktif bila env wajib diisi) | WECOM_BOT_ID, WECOM_SECRET; extra `wecom` (defusedxml) |  | planned |
| PLAT-024 | Weixin / WeChat (`weixin`) | WeChat personal via iLink Bot API long-poll | `plugins/platforms/weixin/plugin.yaml`, `plugins/platforms/weixin/adapter.py` | opt-in (aktif bila env wajib diisi) | WEIXIN_ACCOUNT_ID, WEIXIN_TOKEN; — |  | planned |
| PLAT-025 | WhatsApp (`whatsapp`) | WhatsApp Web via bridge Node.js lokal | `plugins/platforms/whatsapp/plugin.yaml`, `plugins/platforms/whatsapp/adapter.py` | opt-in (aktif bila env wajib diisi) | WHATSAPP_ENABLED; Node.js bridge |  | external-bridge (ADR-034) |
| PLAT-026 | WhatsApp Business (Cloud) (`whatsapp_cloud`) | WhatsApp Business Cloud API resmi Meta | `plugins/platforms/whatsapp_cloud/plugin.yaml`, `plugins/platforms/whatsapp_cloud/adapter.py` | opt-in (aktif bila env wajib diisi) | WHATSAPP_CLOUD_PHONE_NUMBER_ID, WHATSAPP_CLOUD_ACCESS_TOKEN; URL webhook publik |  | planned |
| PLAT-027 | Yuanbao (`yuanbao`) | Tencent Yuanbao IM Bot via WebSocket | `plugins/platforms/yuanbao/plugin.yaml`, `plugins/platforms/yuanbao/adapter.py` | opt-in (aktif bila env wajib diisi) | YUANBAO_APP_ID, YUANBAO_APP_SECRET; — |  | planned |
| PLAT-028 | `api_server` (core gateway) | OpenAI-compatible HTTP API (chat completions, responses, runs) | `gateway/platforms/api_server.py` | opt-in | extra `web`/aiohttp [INFERENCE] | toolset `hermes-api-server` | planned |
| PLAT-029 | `webhook` (core gateway) | Penerima webhook generik (toolset aman) | `gateway/platforms/webhook.py` | opt-in | — | toolset `hermes-webhook` | planned |
| PLAT-030 | `msgraph_webhook` (core gateway) | Webhook Microsoft Graph (notifikasi Teams/Outlook) | `gateway/platforms/msgraph_webhook.py` | opt-in | Microsoft Graph | dipakai teams_pipeline [INFERENCE] | planned |

## 4. PROV — provider/model backend
50 profil provider unik dari `providers.list_providers()` + `unlisted_provider_names()` (41 direktori `plugins/model-providers/`; beberapa direktori mendaftarkan >1 profil). Diikuti adapter/transport, credential pool, OAuth. Default `model.provider` di config = kosong/`auto` (dipilih lewat `hermes setup`/`hermes model`).
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| PROV-001 | Actual Computer (`actual`) | Actual Computer - hosted inference via api.actual.inc, or local offline inference via model.base_url in config.yaml | `plugins/model-providers/actual/__init__.py:80` | opt-in (pilih via `hermes model`) | API key: ACTUAL_API_KEY | api_mode `chat_completions`; alias: actual-computer, actualcomputer, aci; base `https://api.actual.inc/v1` | planned |
| PROV-002 | ai-gateway (`ai-gateway`) | Profil provider ai-gateway | `plugins/model-providers/ai-gateway/__init__.py:22` | opt-in (pilih via `hermes model`) | API key: AI_GATEWAY_API_KEY | api_mode `chat_completions`; alias: vercel, vercel-ai-gateway, ai_gateway, aigateway; base `https://ai-gateway.vercel.sh/v1` | planned |
| PROV-003 | alibaba (`alibaba`) | Profil provider alibaba | `plugins/model-providers/alibaba/__init__.py:23` | opt-in (pilih via `hermes model`) | API key: DASHSCOPE_API_KEY | api_mode `chat_completions`; alias: dashscope, alibaba-cloud, qwen-dashscope, aliyun; base `https://dashscope-intl.aliyuncs.com/compatible-mode/v1` | planned |
| PROV-004 | Alibaba Cloud DashScope (China) (`alibaba-cn`) | Alibaba Cloud DashScope, mainland-China endpoint | `plugins/model-providers/alibaba/__init__.py:28` | opt-in (pilih via `hermes model`) | API key: DASHSCOPE_API_KEY, DASHSCOPE_CN_BASE_URL | api_mode `chat_completions`; alias: dashscope-cn, alibaba-cloud-cn; base `https://dashscope.aliyuncs.com/compatible-mode/v1` | planned |
| PROV-005 | Alibaba Cloud (Token Plan) (`alibaba-token-plan`) | Alibaba Cloud Model Studio Token Plan (flat-token tier) | `plugins/model-providers/alibaba/__init__.py:36` | opt-in (pilih via `hermes model`) | API key: ALIBABA_TOKEN_PLAN_API_KEY, ALIBABA_TOKEN_PLAN_BASE_URL | api_mode `chat_completions`; alias: dashscope-token-plan; base `https://token-plan.ap-southeast-1.maas.aliyuncs.com/compatible-mode/v1` | planned |
| PROV-006 | Alibaba Cloud (Token Plan, China) (`alibaba-token-plan-cn`) | Alibaba Cloud Model Studio Token Plan, mainland-China endpoint | `plugins/model-providers/alibaba/__init__.py:44` | opt-in (pilih via `hermes model`) | API key: ALIBABA_TOKEN_PLAN_CN_API_KEY, ALIBABA_TOKEN_PLAN_API_KEY, ALIBABA_TOKEN_PLAN_CN_BASE_URL | api_mode `chat_completions`; alias: dashscope-token-plan-cn; base `https://token-plan.cn-beijing.maas.aliyuncs.com/compatible-mode/v1` | planned |
| PROV-007 | Alibaba Cloud (Coding Plan) (`alibaba-coding-plan`) | Alibaba Cloud Coding Plan (Dedicated coding tier) | `plugins/model-providers/alibaba-coding-plan/__init__.py:23` | opt-in (pilih via `hermes model`) | API key: ALIBABA_CODING_PLAN_API_KEY, DASHSCOPE_API_KEY, ALIBABA_CODING_PLAN_BASE_URL | api_mode `chat_completions`; alias: alibaba_coding, alibaba-coding, dashscope-coding; base `https://coding-intl.dashscope.aliyuncs.com/v1` | planned |
| PROV-008 | Alibaba Cloud (Coding Plan, China) (`alibaba-coding-plan-cn`) | Alibaba Cloud Coding Plan, mainland-China endpoint | `plugins/model-providers/alibaba-coding-plan/__init__.py:32` | opt-in (pilih via `hermes model`) | API key: ALIBABA_CODING_PLAN_CN_API_KEY, ALIBABA_CODING_PLAN_API_KEY, DASHSCOPE_API_KEY, ALIBABA_CODING_PLAN_CN_BASE_URL | api_mode `chat_completions`; alias: alibaba-coding-cn, dashscope-coding-cn; base `https://coding.dashscope.aliyuncs.com/v1` | planned |
| PROV-009 | anthropic (`anthropic`) | Profil provider anthropic | `plugins/model-providers/anthropic/__init__.py:50` | opt-in (pilih via `hermes model`) | API key: ANTHROPIC_API_KEY, ANTHROPIC_TOKEN, CLAUDE_CODE_OAUTH_TOKEN | api_mode `anthropic_messages`; alias: claude, claude-oauth, claude-code; base `https://api.anthropic.com` | planned |
| PROV-010 | arcee (`arcee`) | Profil provider arcee | `plugins/model-providers/arcee/__init__.py:7` | opt-in (pilih via `hermes model`) | API key: ARCEEAI_API_KEY | api_mode `chat_completions`; alias: arcee-ai, arceeai; base `https://api.arcee.ai/api/v1` | planned |
| PROV-011 | Azure Foundry (`azure-foundry`) | Microsoft Foundry - OpenAI-compatible endpoint (user-supplied base URL) | `plugins/model-providers/azure-foundry/__init__.py:8` | opt-in (pilih via `hermes model`) | API key: AZURE_FOUNDRY_API_KEY, AZURE_FOUNDRY_BASE_URL | api_mode `chat_completions`; alias: azure, azure-ai-foundry, azure-ai | planned |
| PROV-012 | bedrock (`bedrock`) | Profil provider bedrock | `plugins/model-providers/bedrock/__init__.py:12` | opt-in (pilih via `hermes model`) | AWS IAM (SDK): — | api_mode `bedrock_converse`; alias: aws, aws-bedrock, amazon-bedrock, amazon; base `https://bedrock-runtime.us-east-1.amazonaws.com` | planned |
| PROV-013 | CommandCode (`commandcode`) | CommandCode — 20+ models via OpenAI-compatible API | `plugins/model-providers/commandcode/__init__.py:74` | opt-in (pilih via `hermes model`) | API key: COMMANDCODE_API_KEY, COMMANDCODE_BASE_URL | api_mode `chat_completions`; alias: commandcode-chat; base `https://api.commandcode.ai/provider/v1` | planned |
| PROV-014 | CommandCode (Anthropic) (`commandcode-anthropic`) | CommandCode — Claude models via Anthropic Messages API | `plugins/model-providers/commandcode/__init__.py:89` | opt-in (pilih via `hermes model`) | API key: COMMANDCODE_API_KEY, COMMANDCODE_ANTHROPIC_BASE_URL | api_mode `anthropic_messages`; alias: commandcode-claude; base `https://api.commandcode.ai/provider/v1` | planned |
| PROV-015 | copilot (`copilot`) | Profil provider copilot | `plugins/model-providers/copilot/__init__.py:39` | opt-in (pilih via `hermes model`) | GitHub token/OAuth Copilot: COPILOT_GITHUB_TOKEN, GH_TOKEN, GITHUB_TOKEN | api_mode `chat_completions`; alias: github-copilot, github-models, github-model, github; base `https://api.githubcopilot.com` | planned |
| PROV-016 | copilot-acp (`copilot-acp`) | Profil provider copilot-acp | `plugins/model-providers/copilot-acp/__init__.py:51` | opt-in (pilih via `hermes model`) | proses eksternal (ACP): — | api_mode `chat_completions`; alias: github-copilot-acp, copilot-acp-agent; base `acp://copilot` | planned |
| PROV-017 | custom (`custom`) | Profil provider custom | `plugins/model-providers/custom/__init__.py:96` | opt-in (pilih via `hermes model`) | API key: — | api_mode `chat_completions`; alias: ollama, local, vllm, llamacpp, llama.cpp, llama-cpp | planned |
| PROV-018 | DeepInfra (`deepinfra`) | DeepInfra — 100+ open models, pay-per-use | `plugins/model-providers/deepinfra/__init__.py:54` | opt-in (pilih via `hermes model`) | API key: DEEPINFRA_API_KEY, DEEPINFRA_BASE_URL | api_mode `chat_completions`; alias: deep-infra, deepinfra-ai; base `https://api.deepinfra.com/v1/openai` | planned |
| PROV-019 | DeepSeek (`deepseek`) | DeepSeek — native DeepSeek API | `plugins/model-providers/deepseek/__init__.py:55` | opt-in (pilih via `hermes model`) | API key: DEEPSEEK_API_KEY | api_mode `chat_completions`; alias: deepseek-chat, deep-seek; base `https://api.deepseek.com/v1` | planned |
| PROV-020 | Fireworks AI (`fireworks`) | Fireworks AI — OpenAI-compatible direct model API | `plugins/model-providers/fireworks/__init__.py:30` | opt-in (pilih via `hermes model`) | API key: FIREWORKS_API_KEY | api_mode `chat_completions`; alias: fireworks-ai, fw; base `https://api.fireworks.ai/inference/v1` | planned |
| PROV-021 | gemini (`gemini`) | Profil provider gemini | `plugins/model-providers/gemini/__init__.py:41` | opt-in (pilih via `hermes model`) | API key: GOOGLE_API_KEY, GEMINI_API_KEY | api_mode `chat_completions`; alias: google, google-gemini, google-ai-studio; base `https://generativelanguage.googleapis.com/v1beta` | planned |
| PROV-022 | GMI Cloud (`gmi`) | GMI Cloud — multi-model direct API (slash-form model IDs) | `plugins/model-providers/gmi/__init__.py:8` | opt-in (pilih via `hermes model`) | API key: GMI_API_KEY, GMI_BASE_URL | api_mode `chat_completions`; alias: gmi-cloud, gmicloud; base `https://api.gmi-serving.com/v1` | planned |
| PROV-023 | HuggingFace (`huggingface`) | HuggingFace Inference API | `plugins/model-providers/huggingface/__init__.py:7` | opt-in (pilih via `hermes model`) | API key: HF_TOKEN | api_mode `chat_completions`; alias: hf, hugging-face, huggingface-hub; base `https://router.huggingface.co/v1` | planned |
| PROV-024 | kilocode (`kilocode`) | Profil provider kilocode | `plugins/model-providers/kilocode/__init__.py:7` | opt-in (pilih via `hermes model`) | API key: KILOCODE_API_KEY | api_mode `chat_completions`; alias: kilo-code, kilo, kilo-gateway; base `https://api.kilo.ai/api/gateway` | planned |
| PROV-025 | kimi-coding (`kimi-coding`) | Profil provider kimi-coding | `plugins/model-providers/kimi-coding/__init__.py` | opt-in (pilih via `hermes model`) | API key: KIMI_API_KEY, KIMI_CODING_API_KEY | api_mode `chat_completions`; alias: kimi, moonshot, kimi-for-coding; base `https://api.moonshot.ai/v1` | planned |
| PROV-026 | kimi-coding-cn (`kimi-coding-cn`) | Profil provider kimi-coding-cn | `plugins/model-providers/kimi-coding/__init__.py` | opt-in (pilih via `hermes model`) | API key: KIMI_CN_API_KEY | api_mode `chat_completions`; alias: kimi-cn, moonshot-cn; base `https://api.moonshot.cn/v1` | planned |
| PROV-027 | LM Studio (`lmstudio`) | Profil provider lmstudio | `plugins/model-providers/lmstudio/__init__.py:41` | opt-in (pilih via `hermes model`) | API key: LM_API_KEY, LM_BASE_URL | api_mode `chat_completions` | planned |
| PROV-028 | Meta Model API (`meta-ai`) | Meta Muse Spark family (Meta Superintelligence Labs) | `plugins/model-providers/meta-ai/__init__.py:58` | opt-in (pilih via `hermes model`) | API key: MODEL_API_KEY, META_API_KEY, META_MODEL_API_KEY, META_BASE_URL | api_mode `codex_responses`; alias: meta, muse, muse-spark, model-api, msl; base `https://api.meta.ai/v1` | planned |
| PROV-029 | minimax (`minimax`) | Profil provider minimax | `plugins/model-providers/minimax/__init__.py:41` | opt-in (pilih via `hermes model`) | API key: MINIMAX_API_KEY | api_mode `anthropic_messages`; alias: mini-max; base `https://api.minimax.io/anthropic` | planned |
| PROV-030 | minimax-cn (`minimax-cn`) | Profil provider minimax-cn | `plugins/model-providers/minimax/__init__.py:46` | opt-in (pilih via `hermes model`) | API key: MINIMAX_CN_API_KEY | api_mode `anthropic_messages`; alias: minimax-china, minimax_cn; base `https://api.minimaxi.com/anthropic` | planned |
| PROV-031 | MiniMax (OAuth) (`minimax-oauth`) | MiniMax via OAuth browser flow — no API key required | `plugins/model-providers/minimax/__init__.py:52` | opt-in (pilih via `hermes model`) | OAuth eksternal: — | api_mode `anthropic_messages`; alias: minimax_oauth, minimax-portal, minimax-global, minimax-oauth-io; base `https://api.minimax.io/anthropic` | planned |
| PROV-032 | Nebius Token Factory (`nebius-token-factory`) | Nebius Token Factory — OpenAI-compatible inference | `plugins/model-providers/nebius-token-factory/__init__.py:35` | opt-in (pilih via `hermes model`) | API key: NEBIUS_API_KEY, NEBIUS_TOKEN_FACTORY_API_KEY, NEBIUS_BASE_URL | api_mode `chat_completions`; alias: nebius, nebius-tokenfactory, nebius-tf, token-factory, tokenfactory; base `https://api.tokenfactory.nebius.com/v1` | planned |
| PROV-033 | Nous Research (`nous`) | Nous Research — Hermes model family | `plugins/model-providers/nous/__init__.py:70` | opt-in (pilih via `hermes model`) | OAuth device code: NOUS_API_KEY | api_mode `chat_completions`; alias: nous-portal, nousresearch; base `https://inference-api.nousresearch.com/v1` | planned |
| PROV-034 | NovitaAI (`novita`) | NovitaAI — AI-native cloud for builders and agents | `plugins/model-providers/novita/__init__.py:8` | opt-in (pilih via `hermes model`) | API key: NOVITA_API_KEY, NOVITA_BASE_URL | api_mode `chat_completions`; alias: novita-ai, novitaai; base `https://api.novita.ai/openai/v1` | planned |
| PROV-035 | NVIDIA NIM (`nvidia`) | NVIDIA NIM — accelerated inference | `plugins/model-providers/nvidia/__init__.py:28` | opt-in (pilih via `hermes model`) | API key: NVIDIA_API_KEY | api_mode `chat_completions`; alias: nvidia-nim, nim, build-nvidia, nemotron; base `https://integrate.api.nvidia.com/v1` | planned |
| PROV-036 | ollama-cloud (`ollama-cloud`) | Profil provider ollama-cloud | `plugins/model-providers/ollama-cloud/__init__.py:39` | opt-in (pilih via `hermes model`) | API key: OLLAMA_API_KEY | api_mode `chat_completions`; alias: ollama_cloud; base `https://ollama.com/v1` | planned |
| PROV-037 | openai-codex (`openai-codex`) | Profil provider openai-codex | `plugins/model-providers/openai-codex/__init__.py:7` | opt-in (pilih via `hermes model`) | OAuth eksternal: — | api_mode `codex_responses`; alias: codex, openai_codex; base `https://chatgpt.com/backend-api/codex` | planned |
| PROV-038 | opencode-zen (`opencode-zen`) | Profil provider opencode-zen | `plugins/model-providers/opencode-zen/__init__.py:120` | opt-in (pilih via `hermes model`) | API key: OPENCODE_ZEN_API_KEY | api_mode `chat_completions`; alias: opencode, opencode_zen, zen; base `https://opencode.ai/zen/v1` | planned |
| PROV-039 | opencode-go (`opencode-go`) | Profil provider opencode-go | `plugins/model-providers/opencode-zen/__init__.py:126` | opt-in (pilih via `hermes model`) | API key: OPENCODE_GO_API_KEY | api_mode `chat_completions`; alias: opencode_go, go, opencode-go-sub; base `https://opencode.ai/zen/go/v1` | planned |
| PROV-040 | OpenRouter (`openrouter`) | OpenRouter — unified API for 200+ models | `plugins/model-providers/openrouter/__init__.py:208` | opt-in (pilih via `hermes model`) | API key: OPENROUTER_API_KEY | api_mode `chat_completions`; alias: or; base `https://openrouter.ai/api/v1` | planned |
| PROV-041 | qwen-oauth (`qwen-oauth`) | Profil provider qwen-oauth | `plugins/model-providers/qwen-oauth/__init__.py:68` | opt-in (pilih via `hermes model`) | OAuth eksternal: QWEN_API_KEY | api_mode `chat_completions`; alias: qwen, qwen-portal, qwen-cli; base `https://portal.qwen.ai/v1` | planned |
| PROV-042 | Ramp Router (`router`) | Ramp Router (router.com) — routes each request to the cheapest model that clears your quality … | `plugins/model-providers/router/__init__.py:290` | opt-in (pilih via `hermes model`) | API key: RAMP_ROUTER_API_KEY, ROUTER_API_KEY, RAMP_ROUTER_BASE_URL | api_mode `codex_responses`; alias: ramp-router, ramp, router.com; base `https://api.router.com/v1` | planned |
| PROV-043 | Solstice (`solstice`) | Solstice (your own per-user quota, OAuth sign-in) | `plugins/model-providers/solstice/__init__.py:59` | opt-in (pilih via `hermes model`) | OAuth eksternal: — | api_mode `chat_completions`; alias: solstice-oauth; base `https://generativelanguage.googleapis.com/v1alpha`; hidden (pra-rilis) | dropped-with-approval (NP-16) |
| PROV-044 | stepfun (`stepfun`) | Profil provider stepfun | `plugins/model-providers/stepfun/__init__.py:7` | opt-in (pilih via `hermes model`) | API key: STEPFUN_API_KEY | api_mode `chat_completions`; alias: step, stepfun-coding-plan; base `https://api.stepfun.ai/step_plan/v1` | planned |
| PROV-045 | Tencent TokenHub (`tencent-tokenhub`) | Profil provider tencent-tokenhub | `plugins/model-providers/tencent-tokenhub/__init__.py:38` | opt-in (pilih via `hermes model`) | API key: TOKENHUB_API_KEY, TOKENHUB_BASE_URL | api_mode `chat_completions` | planned |
| PROV-046 | Upstage Solar (`upstage`) | Upstage (Solar API) | `plugins/model-providers/upstage/__init__.py:51` | opt-in (pilih via `hermes model`) | API key: UPSTAGE_API_KEY, UPSTAGE_BASE_URL | api_mode `chat_completions`; alias: solar; base `https://api.upstage.ai/v1` | planned |
| PROV-047 | vertex (`vertex`) | Profil provider vertex | `plugins/model-providers/vertex/__init__.py:27` | opt-in (pilih via `hermes model`) | Google ADC/service account: — | api_mode `chat_completions`; alias: google-vertex, vertex-ai, gcp-vertex, vertexai; base `https://aiplatform.googleapis.com` | planned |
| PROV-048 | xai (`xai`) | Profil provider xai | `plugins/model-providers/xai/__init__.py:8` | opt-in (pilih via `hermes model`) | API key: XAI_API_KEY | api_mode `codex_responses`; alias: grok, x-ai, x.ai; base `https://api.x.ai/v1` | planned |
| PROV-049 | xiaomi (`xiaomi`) | Profil provider xiaomi | `plugins/model-providers/xiaomi/__init__.py:7` | opt-in (pilih via `hermes model`) | API key: XIAOMI_API_KEY | api_mode `chat_completions`; alias: mimo, xiaomi-mimo; base `https://api.xiaomimimo.com/v1` | planned |
| PROV-050 | Z.AI (GLM) (`zai`) | Z.AI / GLM — Zhipu AI models | `plugins/model-providers/zai/__init__.py:74` | opt-in (pilih via `hermes model`) | API key: GLM_API_KEY, ZAI_API_KEY, Z_AI_API_KEY | api_mode `chat_completions`; alias: glm, z-ai, z.ai, zhipu; base `https://api.z.ai/api/paas/v4` | planned |
| PROV-051 | Provider registry & ProviderProfile | Kontrak profil provider (api_mode, auth_type, env, hooks) | `providers/base.py:42`, `providers/__init__.py:122` | on | — |  | planned |
| PROV-052 | Transport: chat_completions | Wire OpenAI Chat Completions | `agent/transports/chat_completions.py` | on | openai SDK |  | planned |
| PROV-053 | Transport/adapter: Anthropic Messages | Wire Anthropic Messages + thinking policy/replay | `agent/anthropic_adapter.py`, `agent/transports/anthropic.py`, `agent/anthropic_thinking_policy.py` | on | anthropic SDK (extra `anthropic`) |  | planned |
| PROV-054 | Transport/adapter: Codex/Responses | Wire OpenAI Responses (Codex, xAI, Meta, Router) | `agent/codex_responses_adapter.py`, `agent/transports/codex.py` | on | openai SDK |  | planned |
| PROV-055 | Codex app-server runtime | Runtime opsional codex app-server + MCP tools bridge | `agent/transports/codex_app_server.py`, `agent/transports/hermes_tools_mcp_server.py`, `hermes_cli/codex_runtime_switch.py` | opt-in (`/codex-runtime`) | binary codex | `hermes codex-runtime` | planned |
| PROV-056 | Transport/adapter: Bedrock Converse | AWS Bedrock Converse API | `agent/bedrock_adapter.py`, `agent/transports/bedrock.py` | opt-in | boto3 (extra `bedrock`), IAM | discovery model on | planned |
| PROV-057 | Adapter: Gemini native | Gemini native API (long decode, schema) | `agent/gemini_native_adapter.py`, `agent/gemini_schema.py` | opt-in [INFERENCE] | GOOGLE_API_KEY |  | planned |
| PROV-058 | Adapter: Vertex | Google Vertex AI | `agent/vertex_adapter.py` | opt-in | google-auth (extra `vertex`) |  | planned |
| PROV-059 | Adapter: Azure identity | Auth Entra ID untuk Azure Foundry | `agent/azure_identity_adapter.py`, `hermes_cli/azure_detect.py` | opt-in | azure-identity (extra) |  | planned |
| PROV-060 | Adapter: Copilot ACP client | Copilot via ACP subprocess | `agent/copilot_acp_client.py` | opt-in | GitHub Copilot CLI |  | planned |
| PROV-061 | Moonshot schema shim | Sanitasi schema tool untuk Kimi | `agent/moonshot_schema.py` | auto | — |  | planned |
| PROV-062 | Credential pool | Pool kredensial multi-key, rotasi, cooldown per model | `agent/credential_pool.py`, `agent/credential_pool_model_cooldowns.py`, `agent/credential_pool_admin.py` | on | — | `credential_pool_strategies`; `hermes auth` | planned |
| PROV-063 | OAuth: Nous Portal (device code) | Login device-code Nous Portal | `hermes_cli/auth_nous.py`, `hermes_cli/auth_device_flow.py` | opt-in | browser | `hermes portal`, `/login` | planned |
| PROV-064 | OAuth: OpenAI Codex (ChatGPT) | Login ChatGPT untuk Codex | `hermes_cli/auth_codex.py`, `hermes_cli/auth_codex_browser.py` | opt-in | browser |  | planned |
| PROV-065 | OAuth: Anthropic (Claude Code token) | Pakai kredensial Claude Code/OAuth | `agent/anthropic_credentials.py` | opt-in | Claude Code credentials |  | planned |
| PROV-066 | OAuth: GitHub Copilot | Login Copilot | `hermes_cli/copilot_auth.py` | opt-in | GitHub |  | planned |
| PROV-067 | OAuth: Qwen | Login Qwen portal | `hermes_cli/auth_qwen.py` | opt-in | browser |  | planned |
| PROV-068 | OAuth: MiniMax | Login MiniMax OAuth | `hermes_cli/auth_minimax.py` | opt-in | browser |  | planned |
| PROV-069 | OAuth: xAI (SuperGrok) | Login xAI | `hermes_cli/auth_xai.py` | opt-in | browser |  | planned |
| PROV-070 | OAuth: OpenRouter | Login OpenRouter PKCE | `hermes_cli/auth_openrouter.py` | opt-in | browser |  | planned |
| PROV-071 | OAuth: Z.AI/Kimi | Alur auth Z.AI/Kimi | `hermes_cli/auth_zai_kimi.py` | opt-in | — |  | planned |
| PROV-072 | OAuth plugin PKCE / external process | Auth provider plugin (PKCE, proses eksternal) | `hermes_cli/auth_oauth_pkce_plugin.py`, `hermes_cli/auth_external_process.py` | opt-in | — |  | planned |
| PROV-073 | Anonymous/free tier sign-in | Sign-in anonim & free tier | `hermes_cli/anon_sign_in.py`, `hermes_cli/free_tier_offer.py` | opt-in | Nous Portal |  | dropped-with-approval (NP-19) |
| PROV-074 | Fallback providers | Provider cadangan saat primer gagal | `hermes_cli/fallback_config.py`, `agent/agent_init_fallback.py` | opt-in (`fallback_providers`) | — | `hermes fallback` | planned |
| PROV-075 | Auxiliary client | Klien LLM tugas sampingan (compression, vision, title, dll.) | `agent/auxiliary_client.py`, `agent/auxiliary_task_config.py` | on (provider `auto`) | — | ~15 tugas auxiliary | planned |
| PROV-076 | Model catalog & models.dev | Katalog model, metadata context length, harga | `hermes_cli/model_catalog.py`, `agent/models_dev.py`, `agent/model_metadata.py` | on (`model_catalog.enabled`) | Network (models.dev) |  | planned |
| PROV-077 | Local runtime (llama.cpp/Ollama) | Runtime model lokal terkelola | `agent/auxiliary_local_runtime.py`, `agent/model_metadata_llamacpp.py`, `agent/agent_init_ollama.py` | off (`local_runtime.enabled`) | llama.cpp/Ollama |  | planned |
| PROV-078 | Provider custom (OpenAI-compatible) | Endpoint kustom: Ollama, vLLM, llama.cpp | `plugins/model-providers/custom/__init__.py`, `hermes_cli/runtime_provider_custom.py` | opt-in | — | alias ollama/local/vllm/llamacpp | planned |
| PROV-079 | Model cost/data-policy guard | Peringatan model mahal / kebijakan data training | `hermes_cli/model_cost_guard.py`, `hermes_cli/model_data_policy_guard.py` | on | — |  | planned |
| PROV-080 | Rate-limit credits (Nous) | Kredit/guard rate limit Nous | `agent/nous_rate_guard.py`, `agent/rate_limit_credits.py` | on bila Nous | — |  | dropped-with-approval (NP-19) |

## 5. MEM/CTX — memori, sesi, konteks
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| MEM-001 | Memori built-in (MEMORY.md) | Catatan persisten agent lintas sesi, dibatasi karakter | `tools/memory_tool.py:38`, `tools/memory_tool_store.py` | on (`memory.memory_enabled: true`) | — | limit `memory_char_limit`=2200 | planned |
| MEM-002 | User profile (USER.md) | Profil pengguna terpisah dari catatan agent | `tools/memory_tool.py:2` | on (`memory.user_profile_enabled: true`) | — | limit `user_char_limit`=1375 | planned |
| MEM-003 | Memory nudge | Pengingat periodik agar agent menyimpan memori | `hermes_cli/config_defaults.py:1321` | on (interval 10) | — |  | planned |
| MEM-004 | Memory write approval | Gate approval untuk tulisan memori (/memory review) | `hermes_cli/write_approval_commands.py`, `tools/write_approval.py` | off (`memory.write_approval: false`) | — | slash `/memory` | planned |
| MEM-005 | External memory provider | Plugin provider memori eksternal (satu aktif per `memory.provider`) | `agent/memory_provider.py:84`, `agent/memory_manager.py`, `plugins/memory/__init__.py` | off (`memory.provider: ""`) | Tergantung provider | discovery bundled→user→project→entry point | planned |
| MEM-006 | Memory provider: ByteRover | Knowledge tree bertingkat via CLI brv | `plugins/memory/byterover/plugin.yaml` | opt-in | binary `brv` |  | dropped-with-approval (NP-21) |
| MEM-007 | Memory provider: RetainDB | Cloud memory hybrid search, 7 tipe memori | `plugins/memory/retaindb/plugin.yaml` | opt-in | RETAINDB_API_KEY |  | dropped-with-approval (NP-21) |
| MEM-008 | Memory provider: Honcho (config) | Integrasi Honcho untuk user modeling; config & profil | `hermes_cli/profiles_honcho.py`, `hermes_cli/config_defaults.py:1524` | opt-in | Honcho API | Plugin implementasi via katalog [INFERENCE] | planned |
| MEM-009 | Memory query rewrite | Rewrite query sebelum recall provider memori | `plugins/memory/query_rewrite.py` | on bila provider aktif (aux `memory_query_rewrite`) | LLM auxiliary |  | planned |
| MEM-010 | Memory OAuth / setup | Wizard setup & OAuth provider memori | `hermes_cli/memory_setup.py`, `hermes_cli/memory_oauth.py`, `hermes_cli/memory_provider_migration.py` | opt-in (`hermes memory`) | — |  | planned |
| MEM-011 | Session store SQLite | Penyimpanan sesi/pesan di state.db (WAL) | `hermes_state.py`, `hermes_state_schema.py`, `hermes_state_wal.py` | on | SQLite | ~60 modul hermes_state_* | planned |
| MEM-012 | Session search (FTS) | Pencarian full-text sesi lampau + ringkasan | `hermes_state_fts.py`, `hermes_state_search.py`, `tools/session_search_tool.py` | on | SQLite FTS5 | `sessions.cjk_fts: true` | planned |
| MEM-013 | Session recap/resume | Resume sesi bernama/terakhir dengan recap | `hermes_cli/session_recap.py`, `hermes_cli/main.py:1401` | on (`display.resume_last_session`) | — | `-c/--continue`, `/resume` | planned |
| MEM-014 | Session export (HTML/MD/JSON) | Ekspor percakapan ke file | `hermes_cli/session_export.py`, `hermes_cli/session_export_html.py`, `hermes_cli/session_export_md.py` | opt-in (`/save`, `hermes sessions export`) | — |  | planned |
| MEM-015 | Session prune/archive | Hapus/arsip sesi lama otomatis + VACUUM | `hermes_state_maintenance.py`, `hermes_cli/prune_advisor.py` | off (`sessions.auto_prune: false`) | — | `/autoprune` | planned |
| MEM-016 | Session lineage & titles | Lineage parent/child sesi dan judul | `hermes_state_local_lineage.py`, `hermes_state_titles.py` | on | — |  | planned |
| MEM-017 | Foreign sessions import | Impor/resume sesi Claude Code / Codex CLI | `hermes_cli/foreign_sessions.py`, `hermes_cli/agent_import.py` | opt-in (`hermes import-agent`) | — |  | planned |
| MEM-018 | Context compression (compressor) | Ringkas riwayat saat mendekati batas konteks | `agent/context_compressor.py`, `agent/conversation_compression.py` | on (`compression.enabled`, threshold 0.5) | LLM auxiliary (`auxiliary.compression`) | `context.engine: compressor` | planned |
| MEM-019 | Manual compression `/compress` | Kompres manual, opsi `here [N]` simpan ekor | `agent/conversation_compression_manual.py`, `hermes_cli/partial_compress.py` | opt-in | LLM auxiliary | alias `/compact` | planned |
| MEM-020 | Micro-compaction | Compaction ringan per-turn (defrag hasil tool) | `agent/micro_compaction.py` | off (`compression.micro_compact: false`) | — |  | planned |
| MEM-021 | Proactive prune hasil tool | Pangkas hasil tool besar sebelum overflow | `hermes_cli/config_defaults.py:609` | off (`proactive_prune_tokens: 0`) | — |  | planned |
| MEM-022 | Native compaction provider | Pakai compaction native provider (Codex/Anthropic) | `agent/native_compaction.py`, `agent/conversation_compression_codex.py` | auto per provider | Provider pendukung | [INFERENCE] dari nama modul | planned |
| MEM-023 | Context engine plugin | Ganti engine konteks via plugin (toolset `context_engine`) | `agent/context_engine.py`, `plugins/context_engine/__init__.py` | default `compressor` | — |  | planned |
| MEM-024 | Context references (@-ref) | Sisipkan @file/@url/@folder ke prompt | `agent/context_references.py`, `gateway/session_context_refs.py` | on | — |  | planned |
| MEM-025 | Context pin | Pin pesan/konteks agar tidak dikompres | `agent/context_pin.py`, `gateway/session_prompt_pin.py` | opt-in | — |  | planned |
| MEM-026 | Context files (AGENTS.md, SOUL.md, .cursorrules) | Muat instruksi proyek/persona ke system prompt | `agent/context_file_sources.py`, `agent/subdirectory_hints.py`, `SOUL.md` | on | — | `context_file_max_chars` | planned |
| MEM-027 | Context breakdown `/context` | Tampilan penggunaan context window per kategori | `agent/context_breakdown.py` | on | — | alias `/ctx` | planned |
| MEM-028 | Rewind/undo | Mundur N giliran user lalu prompt ulang | `hermes_state_rewind.py` | opt-in (`/undo`) | — |  | planned |
| MEM-029 | Branch/fork sesi | Cabangkan sesi jadi thread baru | `hermes_state_mutation_branch.py`, `gateway/slash_commands_branch_thread.py` | opt-in (`/branch`, alias `/fork`) | — |  | planned |
| MEM-030 | Retry | Kirim ulang pesan terakhir | `hermes_cli/commands.py:100` | opt-in (`/retry`) | — |  | planned |
| MEM-031 | Filesystem checkpoints | Snapshot file sebelum mutasi; `/rollback` | `tools/checkpoint_manager.py`, `tools/checkpoint_pruning.py` | off (`checkpoints.enabled: false`) | git (shadow repo) [INFERENCE] | `hermes checkpoints` | planned |
| MEM-032 | State snapshot `/snapshot` | Snapshot/restore config & state Hermes | `hermes_cli/commands.py:117` | opt-in | — | alias `/snap` | planned |
| MEM-033 | Memory trim proses | Trim memori proses (RSS) berkala | `hermes_cli/mem_trim.py` | on (`context.memory_trim.enabled`) | — |  | planned |
| MEM-034 | Prompt caching | Breakpoint cache prompt (Anthropic dll.) | `agent/prompt_caching.py`, `agent/prompt_cache_boundary.py` | on (auto per provider) | — |  | planned |
| MEM-035 | Learning journey / graph | Timeline skill & memori yang dipelajari | `agent/learning_graph.py`, `hermes_cli/journey.py` | opt-in (`hermes journey`, `/journey`) | — |  | planned |
| MEM-036 | Side question `/btw` | Pertanyaan sampingan tanpa mengganggu konteks utama | `agent/side_question.py` | opt-in | LLM |  | planned |
| MEM-037 | Image eviction/replay | Batasi gambar lama di konteks; replay N terakhir | `agent/image_eviction_policy.py`, `agent/image_replay.py` | on (`vision.replay_recent_images: 3`) | — |  | planned |

## 6a. SKILL — sistem skill
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| SKILL-001 | Skill system (SKILL.md) | Dokumen instruksi + file pendukung, dimuat progresif | `tools/skills_tool.py`, `agent/skill_utils.py` | on | — |  | planned |
| SKILL-002 | skills_list / skill_view / skill_manage | Tool agent untuk daftar, baca, kelola skill | `tools/skills_tool.py`, `tools/skill_manager_tool.py` | on (toolset `skills`) | — | lihat TOOL | planned |
| SKILL-003 | Skill slash commands | Setiap skill dipanggil sebagai `/<skill-name>` | `agent/skill_commands.py` | on | — |  | planned |
| SKILL-004 | Skill preprocessing (template vars, inline shell) | Substitusi variabel & shell inline di SKILL.md | `agent/skill_preprocessing.py` | template_vars on; inline_shell off | — | `skills.inline_shell: false` | planned |
| SKILL-005 | `/learn` | Pelajari skill dari deskripsi/dir/URL | `agent/learn_prompt.py` | opt-in | LLM |  | planned |
| SKILL-006 | `/refine` | Review percakapan → simpan pelajaran ke memori/skill | `hermes_cli/commands.py:154` | opt-in | LLM |  | planned |
| SKILL-007 | Skill bundles | Alias satu `/name` untuk beberapa skill | `agent/skill_bundles.py`, `hermes_cli/bundles.py` | opt-in (`hermes bundles`) | — |  | planned |
| SKILL-008 | Skills auto-load | Skill dimuat otomatis tiap sesi | `hermes_cli/config_defaults.py:1462` | off (`skills.auto_load: []`) | — |  | planned |
| SKILL-009 | Project skill discovery | Temukan skill di dir proyek (trusted) | `hermes_cli/config_defaults.py:1456` | on (`skills.project_discovery`) | — |  | planned |
| SKILL-010 | External skill dirs | Direktori skill tambahan | `hermes_cli/config_defaults.py:1448` | off (list kosong) | — |  | planned |
| SKILL-011 | Skills hub (install/search) | Cari & pasang skill dari sumber eksternal | `tools/skills_hub.py`, `tools/skills_hub_search.py`, `tools/skills_hub_install.py` | opt-in (`hermes skills`) | Network | sumber: official, GitHub, ClawHub, skills.sh | planned |
| SKILL-012 | Skills hub source: official | Sumber skill resmi Nous | `tools/skills_hub_official.py` | opt-in | Network |  | planned |
| SKILL-013 | Skills hub source: GitHub | Install skill dari repo GitHub | `tools/skills_hub_github.py` | opt-in | GitHub API |  | planned |
| SKILL-014 | Skills hub source: ClawHub | Install skill dari ClawHub | `tools/skills_hub_clawhub.py` | opt-in | Network |  | planned |
| SKILL-015 | Skills hub source: skills.sh | Install skill dari skills.sh | `tools/skills_hub_skillssh.py` | opt-in | Network |  | planned |
| SKILL-016 | Bundled skills sync | Sinkron skill bawaan ke HERMES_HOME saat start/update | `tools/skills_sync.py`, `tools/skills_sync_bundled_ops.py` | on | — |  | planned |
| SKILL-017 | Optional skills sync | Pasang skill dari `optional-skills/` sesuai permintaan | `tools/skills_sync_optional.py` | opt-in | — |  | planned |
| SKILL-018 | Skills guard / AST audit / linter | Pemindaian keamanan & lint skill | `tools/skills_guard.py`, `tools/skills_ast_audit.py`, `tools/skill_linter.py` | on (advisory) | — | `skills.guard_agent_created: false` | planned |
| SKILL-019 | Skill ledger & provenance | Catat perubahan & asal skill | `tools/skill_ledger.py`, `tools/skill_provenance.py` | on (`skills.ledger: true`) | — |  | planned |
| SKILL-020 | Skill usage tracking | Statistik pemakaian skill (input curator) | `tools/skill_usage.py` | on | — |  | planned |
| SKILL-021 | Curator | Pemeliharaan skill background: stale/archive/consolidate | `agent/curator.py`, `hermes_cli/curator.py`, `agent/curator_backup.py` | on (`curator.enabled`, interval 168 jam) | LLM auxiliary (`auxiliary.curator`) | `hermes curator`, `/curator` | planned |
| SKILL-022 | Skill write approval | Gate approval tulisan skill | `hermes_cli/config_defaults.py` | off (`skills.write_approval: false`) | — |  | planned |
| SKILL-023 | Skills index | Indeks skill (build script & delta) | `agent/skills_index_delta.py`, `scripts/build_skills_index.py` | on | — |  | planned |

## 6b. SKILL — katalog skill bawaan per kategori
`skills/`: **58** SKILL.md dalam 12 kategori; `optional-skills/`: **153** SKILL.md dalam 23 kategori (dihitung dengan `rglob("SKILL.md")`).
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| SKCAT-001 | Bundled skills: `apple` (4) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/apple/` | on (bundled) | per skill | skill: apple-notes, apple-reminders, findmy, imessage | planned |
| SKCAT-002 | Bundled skills: `autonomous-ai-agents` (5) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/autonomous-ai-agents/` | on (bundled) | per skill | skill: claude-code, codex, computer-use, hermes-agent, opencode | planned |
| SKCAT-003 | Bundled skills: `creative` (10) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/creative/` | on (bundled) | per skill | skill: architecture-diagram, ascii-video, baoyu-infographic, claude-design, design-md, humanizer, manim-video, p5js, popular-web-designs, songwriting-and-ai-music | planned |
| SKCAT-004 | Bundled skills: `devops` (1) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/devops/` | on (bundled) | per skill | skill: sdlc-review | planned |
| SKCAT-005 | Bundled skills: `email` (2) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/email/` | on (bundled) | per skill | skill: email-inbox-triage, himalaya | planned |
| SKCAT-006 | Bundled skills: `media` (3) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/media/` | on (bundled) | per skill | skill: gif-search, songsee, youtube-content | planned |
| SKCAT-007 | Bundled skills: `note-taking` (1) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/note-taking/` | on (bundled) | per skill | skill: obsidian | planned |
| SKCAT-008 | Bundled skills: `productivity` (14) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/productivity/` | on (bundled) | per skill | skill: airtable, box, document-to-action-items, docx, google-workspace, maps, meeting-action-items, notion, pdf, powerpoint, product-price-monitor, teams-meeting-pipeline, weekly-review-planning, xlsx | planned |
| SKCAT-009 | Bundled skills: `research` (4) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/research/` | on (bundled) | per skill | skill: arxiv, competitor-news-monitor, grounded-citations, llm-wiki | planned |
| SKCAT-010 | Bundled skills: `social-media` (1) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/social-media/` | on (bundled) | per skill | skill: xurl | planned |
| SKCAT-011 | Bundled skills: `software-development` (12) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/software-development/` | on (bundled) | per skill | skill: codebase-inspection, dogfood, github, hermes-agent-skill-authoring, inspecting-hermes-desktop-dom, node-inspect-debugger, python-debugpy, requesting-code-review, simplify-code, spike, systematic-debugging, test-driven-development | planned |
| SKCAT-012 | Bundled skills: `web` (1) | Skill bawaan kategori ini, disinkron ke HERMES_HOME | `skills/web/` | on (bundled) | per skill | skill: blocked-page-recovery | planned |
| SKCAT-013 | Optional skills: `autonomous-ai-agents` (7) | Skill opsional, dipasang sesuai permintaan | `optional-skills/autonomous-ai-agents/` | opt-in | per skill | skill: agent-merge-conflict-arbiter, antigravity-cli, blackbox, dynamic-workflow, grok, honcho, openhands | planned |
| SKCAT-014 | Optional skills: `blockchain` (3) | Skill opsional, dipasang sesuai permintaan | `optional-skills/blockchain/` | opt-in | per skill | skill: evm, hyperliquid, solana | planned |
| SKCAT-015 | Optional skills: `communication` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/communication/` | opt-in | per skill | skill: one-three-one-rule | planned |
| SKCAT-016 | Optional skills: `creative` (31) | Skill opsional, dipasang sesuai permintaan | `optional-skills/creative/` | opt-in | per skill | skill: ai-presenter-video, anatomy, archify, ascii-art, audiocraft-audio-generation, auteur, baoyu-article-illustrator, baoyu-comic, brag, brag-slim, comfyui, concept-diagrams, creative-ideation, draw-your-font, dream-loop, excalidraw, heartmula, hyperframes, impeccable, ip-as-logo, kanban-video-orchestrator, meme-generation, mono-color, pixel-art, pretext, simple-english, sketch, social-media-content-calendar, system-atlas, tldraw-offline, unreal-mcp | planned |
| SKCAT-017 | Optional skills: `data-science` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/data-science/` | opt-in | per skill | skill: jupyter-notebook | planned |
| SKCAT-018 | Optional skills: `devops` (7) | Skill opsional, dipasang sesuai permintaan | `optional-skills/devops/` | opt-in | per skill | skill: actual-setup, docker-management, hermes-s6-container-supervision, inference-sh-cli, pinggy-tunnel, setup-wizard-generator, watchers | planned |
| SKCAT-019 | Optional skills: `dogfood` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/dogfood/` | opt-in | per skill | skill: adversarial-ux-test | planned |
| SKCAT-020 | Optional skills: `email` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/email/` | opt-in | per skill | skill: agentmail | planned |
| SKCAT-021 | Optional skills: `finance` (9) | Skill opsional, dipasang sesuai permintaan | `optional-skills/finance/` | opt-in | per skill | skill: 3-statement-model, comps-analysis, dcf-model, excel-author, lbo-model, merger-model, polymarket, pptx-author, stocks | planned |
| SKCAT-022 | Optional skills: `gaming` (2) | Skill opsional, dipasang sesuai permintaan | `optional-skills/gaming/` | opt-in | per skill | skill: minecraft-modpack-server, pokemon-player | planned |
| SKCAT-023 | Optional skills: `health` (2) | Skill opsional, dipasang sesuai permintaan | `optional-skills/health/` | opt-in | per skill | skill: fitness-nutrition, neuroskill-bci | planned |
| SKCAT-024 | Optional skills: `mcp` (3) | Skill opsional, dipasang sesuai permintaan | `optional-skills/mcp/` | opt-in | per skill | skill: fastmcp, mcp-oauth-remote-gateway, mcporter | planned |
| SKCAT-025 | Optional skills: `migration` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/migration/` | opt-in | per skill | skill: openclaw-migration | planned |
| SKCAT-026 | Optional skills: `mlops` (36) | Skill opsional, dipasang sesuai permintaan | `optional-skills/mlops/` | opt-in | per skill | skill: accelerate, chroma, clip, evaluation/evaluating-llms-harness, evaluation/weights-and-biases, faiss, flash-attention, guidance, huggingface-tokenizers, inference/llama-cpp, inference/outlines, inference/serving-llms-vllm, instructor, lambda-labs, llava, modal, models/huggingface-hub, models/segment-anything-model, nemo-curator, obliteratus, peft, pinecone, pytorch-fsdp, pytorch-lightning, qdrant, research/dspy, saelens, simpo, slime, stable-diffusion, tensorrt-llm, torchtitan, training/axolotl, training/trl-fine-tuning, training/unsloth, whisper | planned |
| SKCAT-027 | Optional skills: `payments` (3) | Skill opsional, dipasang sesuai permintaan | `optional-skills/payments/` | opt-in | per skill | skill: mpp-agent, stripe-link-cli, stripe-projects | planned |
| SKCAT-028 | Optional skills: `productivity` (10) | Skill opsional, dipasang sesuai permintaan | `optional-skills/productivity/` | opt-in | per skill | skill: canvas, decision-questionnaire, here-now, live-dashboard, memento-flashcards, property-listings, shop, shopify, siyuan, telephony | planned |
| SKCAT-029 | Optional skills: `research` (15) | Skill opsional, dipasang sesuai permintaan | `optional-skills/research/` | opt-in | per skill | skill: bioinformatics, blogwatcher, darwinian-evolver, domain-intel, drug-discovery, duckduckgo-search, gitnexus-explorer, osint-investigation, parallel-cli, pinecone-research, qmd, research-paper-writing, rss-feeds, scrapling, searxng-search | planned |
| SKCAT-030 | Optional skills: `security` (6) | Skill opsional, dipasang sesuai permintaan | `optional-skills/security/` | opt-in | per skill | skill: 1password, godmode, oss-forensics, sherlock, unbroker, web-pentest | planned |
| SKCAT-031 | Optional skills: `smart-home` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/smart-home/` | opt-in | per skill | skill: openhue | planned |
| SKCAT-032 | Optional skills: `social-media` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/social-media/` | opt-in | per skill | skill: reddit-reading | planned |
| SKCAT-033 | Optional skills: `software-development` (6) | Skill opsional, dipasang sesuai permintaan | `optional-skills/software-development/` | opt-in | per skill | skill: ast-grep, code-wiki, grill-me, pr-lens, rest-graphql-debug, subagent-driven-development | planned |
| SKCAT-034 | Optional skills: `web-development` (5) | Skill opsional, dipasang sesuai permintaan | `optional-skills/web-development/` | opt-in | per skill | skill: cloudflare-temporary-deploy, har-derived-api-client, page-agent, publish-site, scrollcraft | planned |
| SKCAT-035 | Optional skills: `yuanbao` (1) | Skill opsional, dipasang sesuai permintaan | `optional-skills/yuanbao/` | opt-in | per skill | skill: yuanbao | planned |

## 7. AUTO — fitur otomatis/background
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| AUTO-001 | Background review | Review turn di background → usul memori/skill | `agent/background_review.py`, `agent/review_engine.py`, `agent/review_idle_queue.py` | on (`auxiliary.background_review.enabled`) | LLM auxiliary |  | planned |
| AUTO-002 | Title generation | Judul sesi otomatis | `agent/title_generator.py` | on (`auxiliary.title_generation.enabled`) | LLM auxiliary |  | planned |
| AUTO-003 | Curator scheduler | Jalankan curator saat idle (min 2 jam) | `agent/curator.py` | on | LLM auxiliary | lihat SKILL | planned |
| AUTO-004 | Idle/hygiene compaction | Compaction hygiene sesi gateway saat idle/terlalu panjang | `gateway/run_turn_hygiene.py`, `agent/compression_automation.py` | on | LLM auxiliary | `compression.hygiene_*` | planned |
| AUTO-005 | Cron scheduler | Penjadwal job cron (tick, delivery, catch-up) | `cron/scheduler.py`, `cron/jobs.py`, `cron/scheduler_tick.py` | on di gateway; job dibuat user | croniter | `hermes cron`, `/cron` | planned |
| AUTO-006 | Cron delivery ke platform | Kirim hasil job ke platform/home channel | `cron/scheduler_delivery.py`, `cron/delivery_queue.py` | on (`cron.delivery.notify`) | Platform terkait |  | planned |
| AUTO-007 | Cron script jobs | Job berbasis skrip (bukan prompt) | `cron/scheduler_script.py` | opt-in | — | timeout 3600 s | planned |
| AUTO-008 | Cron preflight & incidents | Cek pra-jalan, catat insiden, alert gagal berulang | `cron/scheduler_preflight.py`, `cron/incidents.py` | on (`cron.preflight`) | — |  | planned |
| AUTO-009 | Cron monitor | Monitor kesehatan job cron | `cron/monitor.py`, `agent/monitoring/cron_health.py` | on | LLM auxiliary (`auxiliary.monitor`) [INFERENCE] |  | planned |
| AUTO-010 | Cron provider: Chronos | Cron terkelola via NAS untuk agent scale-to-zero | `plugins/cron_providers/chronos/plugin.yaml` | off (`cron.provider: ""`) | Nous Portal/NAS |  | dropped-with-approval (NP-19) |
| AUTO-011 | Agent self-scheduling | Agent membuat cron sendiri | `hermes_cli/config_defaults.py:1805` | off | — |  | planned |
| AUTO-012 | Blueprints | Template otomasi siap pakai | `cron/blueprint_catalog.py`, `tools/blueprints.py`, `hermes_cli/blueprint_cmd.py` | opt-in (`/blueprint`) | — |  | planned |
| AUTO-013 | Suggestions | Saran otomasi (accept/dismiss) | `hermes_cli/suggestions_cmd.py` | opt-in (`/suggestions`) | LLM |  | planned |
| AUTO-014 | Goals + goal judge | Tujuan berdiri lintas turn, dinilai judge | `hermes_cli/goals.py`, `hermes_cli/goal_command.py`, `gateway/run_goals.py` | opt-in (`/goal`, max_turns 20) | LLM auxiliary (`auxiliary.goal_judge`) | `/subgoal` | planned |
| AUTO-015 | Loops / proactive | Ulangi prompt pada interval di sesi | `hermes_cli/loops.py` | opt-in (`/loop`, alias `/proactive`) | — | min interval 30 s | planned |
| AUTO-016 | Heartbeat | Prompt berulang saat sesi idle | `hermes_cli/heartbeat.py` | opt-in (`/heartbeat`) | — | alias `/hb` | planned |
| AUTO-017 | Kanban dispatcher | Dispatch task kanban ke worker profil | `hermes_cli/kanban_db_dispatch.py`, `gateway/kanban_watchers_dispatcher.py` | on di gateway (`kanban.dispatch_in_gateway`) | — |  | planned |
| AUTO-018 | Kanban auto-decompose | Pecah task besar otomatis | `hermes_cli/kanban_decompose.py` | on (`kanban.auto_decompose`) | LLM auxiliary |  | planned |
| AUTO-019 | Kanban notifier/watchers | Notifikasi perubahan task ke gateway | `gateway/kanban_watchers_notifier.py` | on (`kanban.notify_in_gateway`) | — |  | planned |
| AUTO-020 | Kanban swarm / workflow / PR acceptance | Alur kerja multi-agent & penerimaan PR | `hermes_cli/kanban_swarm.py`, `hermes_cli/kanban_workflow.py`, `hermes_cli/kanban_pr_acceptance.py` | opt-in | git/GitHub | [INFERENCE] dari nama modul | planned |
| AUTO-021 | Mixture of Agents (MoA) | Satu prompt ke beberapa model + aggregator | `agent/moa_loop.py`, `hermes_cli/moa_config.py` | opt-in (`/moa`; preset default enabled) | Beberapa provider (default aggregator OpenRouter) | `hermes moa` | planned |
| AUTO-022 | Background prompt `/bg` | Jalankan prompt di sesi background terpisah | `hermes_cli/commands.py:50` | opt-in | — |  | planned |
| AUTO-023 | Prompt queue `/queue` & steer `/steer` | Antre prompt / sisip pesan setelah tool call | `hermes_cli/commands.py:141`, `hermes_cli/commands.py:145` | opt-in | — | busy policy `display.busy_input_mode` | planned |
| AUTO-024 | Review subagent `/review` | Subagent independen me-review pekerjaan | `hermes_cli/commands.py:156` | opt-in | LLM |  | planned |
| AUTO-025 | Plan mode `/plan` | Tulis rencana markdown ke .hermes/plans/ | `agent/plan_prompt.py` | opt-in | LLM |  | planned |
| AUTO-026 | Auto-recovery turn | Pulihkan turn gagal (retry/fallback) otomatis | `agent/turn_recovery_autorecover.py` | on (`agent.auto_recovery_cycles: 5`) | — |  | planned |
| AUTO-027 | Empty response guard | Deteksi respons kosong & retry dengan batas biaya | `agent/empty_response_guard.py` | on | — | cost_threshold 0.25 USD | planned |
| AUTO-028 | Repetition/loop guardrails | Deteksi tool-loop berulang | `agent/repetition_guard.py`, `agent/tool_guardrails.py` | on (`tool_loop_guardrails`) | — |  | planned |
| AUTO-029 | Periodic scheduler | Penjadwal tugas periodik internal | `agent/periodic_scheduler.py` | on | — | internal | planned |
| AUTO-030 | Disk cleanup plugin | Lacak & bersihkan file sementara sesi | `plugins/disk-cleanup/plugin.yaml` | opt-in (plugin) | — |  | planned |
| AUTO-031 | Auto maintenance CLI | Pemeliharaan otomatis saat start CLI | `hermes_cli/cli_auto_maintenance.py` | on | — |  | planned |
| AUTO-032 | Outbound webhooks | Kirim event ke webhook keluar | `agent/outbound_webhooks.py` | opt-in | — |  | planned |

## 8. UI — antarmuka
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| UI-001 | CLI REPL (prompt_toolkit) | REPL interaktif klasik dengan status bar, dock proses | `cli.py`, `hermes_cli/cli_terminal_input.py`, `hermes_cli/cli_status_bar_mixin.py` | on (`display.interface: cli`) | prompt_toolkit |  | planned |
| UI-002 | Single-query / oneshot mode | `hermes chat -q` sekali jalan non-interaktif | `hermes_cli/cli_single_query.py`, `agent/oneshot.py` | opt-in | — |  | planned |
| UI-003 | Stream-JSON output | Keluaran event JSON untuk otomasi | `hermes_cli/stream_json.py` | opt-in | — | [INFERENCE] dari nama modul | planned |
| UI-004 | Skins/tema CLI | Skin warna & banner | `hermes_cli/skin_engine.py`, `hermes_cli/skin_cmd.py` | on (default skin) | — | `hermes skin`, `/skin` | planned |
| UI-005 | Status bar / battery / timestamps / focus view | Elemen tampilan REPL yang bisa di-toggle | `hermes_cli/cli_status_bar_mixin.py`, `agent/battery.py`, `hermes_cli/focus_view.py` | status bar on; lain off | — |  | planned |
| UI-006 | Bang shell `!cmd` | Eksekusi shell langsung dari prompt | `hermes_cli/bang_shell.py` | on | — |  | planned |
| UI-007 | Command palette | Palette fuzzy (Ctrl+P) | `hermes_cli/commands.py:315` | on | — |  | planned |
| UI-008 | Clipboard image / paste collapse | Tempel gambar clipboard; lipat paste panjang | `hermes_cli/clipboard.py`, `hermes_cli/pt_input_extras.py` | on | xclip/wl-paste/pbpaste [INFERENCE] | `/paste`, `/image` | planned |
| UI-009 | External editor `/prompt` | Tulis prompt di $EDITOR | `hermes_cli/commands.py:101` | opt-in | $EDITOR | alias `/compose` | planned |
| UI-010 | Pets (petdex) | Maskot animasi di terminal; generate pet | `agent/pet/__init__.py`, `hermes_cli/pets.py` | off (`display.pet.enabled`) | — | `/pet`, `/hatch` | planned |
| UI-011 | Personalities | Persona preset | `hermes_cli/personality.py` | opt-in (`/personality`) | — |  | planned |
| UI-012 | TUI (Ink/React) | TUI berbasis Node/Ink via tui_gateway JSON-RPC | `ui-tui/package.json`, `tui_gateway/entry.py`, `hermes_cli/main_tui_launch.py` | opt-in (`--tui` / `display.interface`) | Node.js | paket `ui-tui/packages/hermes-ink` | planned |
| UI-013 | tui_gateway (backend RPC) | Server RPC sesi untuk TUI/desktop | `tui_gateway/entry.py` | on bila TUI/desktop dipakai | — | ~94 modul | planned |
| UI-014 | Web dashboard | Dashboard web (React+Vite) dilayani FastAPI | `web/package.json`, `hermes_cli/web_server.py`, `hermes_cli/web_server_app.py` | opt-in (`hermes dashboard`) | extra `web` (fastapi, uvicorn), Node build |  | planned |
| UI-015 | Dashboard auth providers | Auth dashboard: basic, drain, Nous OAuth, OIDC self-hosted | `plugins/dashboard_auth/basic/plugin.yaml`, `plugins/dashboard_auth/nous/plugin.yaml`, `plugins/dashboard_auth/self_hosted/plugin.yaml`, `plugins/dashboard_auth/drain/plugin.yaml` | opt-in | env HERMES_DASHBOARD_* | 4 backend | planned |
| UI-016 | Headless backend `hermes serve` | Backend server tanpa UI untuk desktop/remote | `hermes_cli/subcommands/dashboard.py:59` | opt-in | extra `web` |  | planned |
| UI-017 | Desktop app (Electron) | Shell desktop native: pane terminal/preview, projects | `apps/desktop/package.json`, `hermes_cli/main_desktop.py` | opt-in (`hermes desktop`, alias `gui`) | Electron 40, Node.js | toolset desktop_ui/project/catalog | planned |
| UI-018 | Bootstrap installer (Tauri) | Installer GUI bootstrap | `apps/bootstrap-installer/src-tauri/tauri.conf.json` | opt-in | Rust/Tauri |  | planned |
| UI-019 | ACP adapter | Server Agent Client Protocol untuk editor (Zed, VS Code, JetBrains) | `acp_adapter/server.py:233`, `acp_adapter/entry.py` | opt-in (`hermes acp`, script `hermes-acp`) | extra `acp` (agent-client-protocol) | toolset `hermes-acp` | planned |
| UI-020 | MCP server (`hermes mcp serve`) | Ekspos percakapan gateway via MCP: 10 tool | `mcp_serve.py:727` | opt-in | extra `mcp` | tool: conversations_list, conversation_get, messages_read, attachments_fetch, events_poll, events_wait, messages_send, channels_list, permissions_list_open, permissions_respond | planned |
| UI-021 | OpenAI-compatible API server | Endpoint /v1 chat/responses untuk klien eksternal | `gateway/platforms/api_server.py`, `gateway/platforms/api_server_openai_routes.py` | opt-in (platform api_server) | aiohttp | lihat PLAT | planned |
| UI-022 | Local OAuth proxy `hermes proxy` | Proxy OpenAI-compatible lokal ke provider OAuth | `hermes_cli/proxy_cli.py` | off (`proxy.enabled: false`) | — |  | planned |
| UI-023 | i18n (17 bahasa) | Lokalisasi UI CLI/TUI/desktop | `locales/en.yaml`, `agent/i18n.py`, `agent/i18n_languages.py` | on (en default) | — | af, ar, de, en, es, fr, ga, hu, it, ja, ko, pt, ru, tr, uk, zh, zh-hant | planned |
| UI-024 | Shell completion | Skrip completion bash/zsh/fish | `hermes_cli/completion.py` | opt-in | — |  | planned |
| UI-025 | Onboarding first-run | Alur onboarding & first tasks | `agent/onboarding_flow.py`, `agent/onboarding_first_tasks.py` | on (first run) | — |  | planned |
| UI-026 | Tips | Tip kontekstual | `hermes_cli/tips.py` | on | — |  | planned |
| UI-027 | Safe command console | Konsol perintah aman `hermes console` | `hermes_cli/console_engine.py` | opt-in | — |  | planned |
| UI-028 | Terminal notify / bell | Notifikasi selesai/prompt | `hermes_cli/terminal_notify.py` | off (`display.bell_on_*`) | — |  | planned |

## 9a. PLUG — sistem plugin, MCP, connectors
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| PLUG-001 | Plugin manager & loader | Discovery, load, isolasi plugin (bundled/user/project/entry point) | `hermes_cli/plugins.py`, `plugins/plugin_loader.py`, `hermes_cli/plugins_discovery.py` | on (bundled) | — |  | planned |
| PLUG-002 | Plugin CLI `hermes plugins` | install/remove/toggle/update/validate/catalog/search | `hermes_cli/plugins_cmd.py`, `hermes_cli/subcommands/plugins.py` | opt-in | git | 16 sub-subcommand | planned |
| PLUG-003 | Plugin isolation & host process | Plugin di proses host terpisah + audit | `hermes_cli/plugin_isolation.py`, `hermes_cli/plugin_host.py`, `hermes_cli/plugin_isolation_audit.py` | on | — |  | planned |
| PLUG-004 | Plugin hooks (lifecycle, stream, API request) | Hook plugin pada turn/stream/request | `agent/plugin_stream_hooks.py`, `agent/api_request_hooks.py`, `hermes_cli/plugins_injection.py` | on | — |  | planned |
| PLUG-005 | Plugin storage | Penyimpanan data per plugin | `plugins/plugin_storage.py` | on | — |  | planned |
| PLUG-006 | Plugin validate (dev) | Validasi manifest, locale, import privat | `hermes_cli/plugin_validate.py`, `hermes_cli/plugin_dev.py` | opt-in | — |  | planned |
| PLUG-007 | Plugin auto update check | Cek update plugin tiap 24 jam | `hermes_cli/plugins_updates.py` | on (cek), auto_apply off | git |  | planned |
| PLUG-008 | Plugin packs | Paket beberapa plugin | `hermes_cli/plugin_packs.py` | opt-in | — |  | planned |
| PLUG-009 | Plugin catalog (645 entri) | Katalog plugin pihak-ketiga terkurasi (SHA-pinned) | `plugin-catalog/README.md` | opt-in (`hermes plugins install`) | git clone repo pihak ketiga | kategori: tools 198, desktop 144, memory 65, models 56, general 48, automation 42, platform 36, web 36, voice 18, tanpa kategori 2; `removed.yaml` tidak dihitung | planned |
| PLUG-010 | Shell hooks | Hook skrip shell pada event agent | `agent/shell_hooks.py`, `hermes_cli/hooks.py` | opt-in (`hermes hooks`) | shell | `hooks_auto_accept` | planned |
| PLUG-011 | Gateway hooks | Hook bawaan gateway | `gateway/hooks.py`, `gateway/builtin_hooks` | on | — |  | planned |
| PLUG-012 | Plugin: browser backends | Backend browser cloud: Browserbase, Browser Use, Firecrawl | `plugins/browser/browserbase/plugin.yaml`, `plugins/browser/browser_use/plugin.yaml`, `plugins/browser/firecrawl/plugin.yaml` | opt-in (`browser.backend`) | BROWSERBASE_API_KEY/PROJECT_ID, BROWSER_USE_API_KEY, FIRECRAWL_API_KEY |  | planned |
| PLUG-013 | Plugin: computer_use (cua) | Backend cua-driver untuk computer_use | `plugins/computer_use/cua/plugin.yaml` | opt-in | cua-driver |  | planned |
| PLUG-014 | Plugin: context_engine | Slot engine konteks alternatif | `plugins/context_engine/__init__.py` | default compressor | — |  | planned |
| PLUG-015 | Plugin: cron_providers (chronos) | Provider cron terkelola | `plugins/cron_providers/chronos/plugin.yaml` | off | Nous Portal |  | dropped-with-approval (NP-19) |
| PLUG-016 | Plugin: dashboard_auth | 4 provider auth dashboard | `plugins/dashboard_auth/_shared.py` | opt-in | — | lihat UI | planned |
| PLUG-017 | Plugin: disk-cleanup | Bersihkan file ephemeral | `plugins/disk-cleanup/disk_cleanup.py` | opt-in | — |  | planned |
| PLUG-018 | Plugin: google_meet | Join Google Meet, transkripsi caption, bicara realtime | `plugins/google_meet/plugin.yaml`, `plugins/google_meet/meet_bot.py` | opt-in | extra `google-meet` (playwright), Node |  | planned |
| PLUG-019 | Plugin: hermes-achievements | Achievement/gamifikasi di dashboard | `plugins/hermes-achievements/README.md` | opt-in | — |  | planned |
| PLUG-020 | Plugin: image_gen | 8 backend generate gambar | `plugins/image_gen/_common.py` | opt-in | lihat MEDIA |  | planned |
| PLUG-021 | Plugin: kanban (dashboard, systemd) | UI dashboard kanban & unit systemd | `plugins/kanban/dashboard`, `plugins/kanban/systemd` | opt-in | systemd |  | planned |
| PLUG-022 | Plugin: memory | Slot/plugin memory provider (Honcho) (V-30/ADR-054; byterover/retaindb = MEM-006/MEM-007, NP-21) | `plugins/memory/__init__.py` | off | lihat MEM |  | planned |
| PLUG-023 | Plugin: model-providers | 41 direktori profil provider model | `plugins/model-providers/README.md` | on (bundled) | lihat PROV |  | planned |
| PLUG-024 | Plugin: platforms | 27 adapter platform chat | `gateway/platforms/ADDING_A_PLATFORM.md` | per-platform | lihat PLAT |  | planned |
| PLUG-025 | Plugin: security-guidance | Peringatan keamanan pada hasil write file | `plugins/security-guidance/plugin.yaml`, `plugins/security-guidance/patterns.py` | opt-in | — |  | planned |
| PLUG-026 | Plugin: teams_pipeline | Pipeline rapat Microsoft Teams via Graph | `plugins/teams_pipeline/plugin.yaml`, `plugins/teams_pipeline/pipeline.py` | opt-in | Microsoft Graph creds |  | planned |
| PLUG-027 | Plugin: video_gen | 4 backend generate video | `plugins/video_gen/xai/plugin.yaml` | opt-in | lihat MEDIA |  | planned |
| PLUG-028 | Plugin: web | 11 backend web search/extract | `plugins/web/__init__.py` | auto (`web.backend: ""`) | lihat EXEC/TOOL |  | planned |
| PLUG-029 | MCP client | Koneksi server MCP stdio/HTTP/SSE → tool dinamis | `tools/mcp_tool.py`, `tools/mcp_tool_transport.py`, `tools/mcp_tool_registration.py` | on (bila `mcp` dikonfigurasi) | extra `mcp` | auto reload saat config berubah | planned |
| PLUG-030 | MCP OAuth (PKCE, device, DCR) | OAuth untuk server MCP remote | `tools/mcp_oauth_manager.py`, `tools/mcp_oauth_device.py` | on bila perlu | browser | `hermes mcp login` | planned |
| PLUG-031 | MCP sampling | Server MCP dapat meminta sampling LLM | `tools/mcp_tool_sampling.py` | opt-in [INFERENCE] | LLM |  | planned |
| PLUG-032 | MCP catalog/picker | Pilih & pasang MCP dari optional-mcps | `hermes_cli/mcp_catalog.py`, `hermes_cli/mcp_picker.py` | opt-in | — |  | planned |
| PLUG-033 | MCP security scan | Audit server MCP (OSV) | `hermes_cli/mcp_security.py` | opt-in | OSV.dev |  | planned |
| PLUG-034 | Optional MCPs (65) | Manifest MCP remote yang disetujui Nous | `optional-mcps/linear/manifest.yaml` | opt-in | semua transport http; oauth 55, none 10 | daftar di kategori MCPX | planned |
| PLUG-035 | Connectors (hosted) | Connector app hosted + akun, via tool manage_connections | `tools/connectors/tool.py`, `hermes_cli/connectors_cmd.py` | on (`tools.connectors.enabled`) | Nous Portal |  | planned |
| PLUG-036 | Tool search / deferred tools | Tool ditunda, ditemukan via pencarian katalog | `tools/tool_search.py`, `tools/tool_search_catalog.py` | auto (`tools.tool_search.enabled: auto`) | — | daftar defer di config | planned |

## 9b. PLUG — optional MCPs (65 manifest)
Sumber: `optional-mcps/*/manifest.yaml`. Semua transport `http`.
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| MCPX-001 | `airtable` | Bases, tables, and records from your Airtable workspace. | `optional-mcps/airtable/manifest.yaml` | opt-in | http `https://mcp.airtable.com/mcp`; auth oauth | connector_slug `airtable` | planned |
| MCPX-002 | `algolia` | Algolia search: indices, analytics, and settings (read-only). | `optional-mcps/algolia/manifest.yaml` | opt-in | http `https://mcp.algolia.com/mcp`; auth oauth |  | planned |
| MCPX-003 | `alltrails` | AllTrails: find hikes and trails with reviews and ratings. | `optional-mcps/alltrails/manifest.yaml` | opt-in | http `https://www.alltrails.com/mcp`; auth none |  | planned |
| MCPX-004 | `amplitude` | Amplitude analytics: charts, dashboards, experiments, flags. | `optional-mcps/amplitude/manifest.yaml` | opt-in | http `https://mcp.amplitude.com/mcp`; auth oauth |  | planned |
| MCPX-005 | `asana` | Tasks, projects, and goals from your Asana workspace. | `optional-mcps/asana/manifest.yaml` | opt-in | http `https://mcp.asana.com/v2/mcp`; auth oauth; env ASANA_CLIENT_ID, ASANA_CLIENT_SECRET | connector_slug `asana` | planned |
| MCPX-006 | `atlassian` | Jira issues and Confluence pages via Atlassian's hosted remote MCP. | `optional-mcps/atlassian/manifest.yaml` | opt-in | http `https://mcp.atlassian.com/v1/mcp/authv2`; auth oauth | connector_slug `jira` | planned |
| MCPX-007 | `attio` | CRM records, lists, and notes in Attio. | `optional-mcps/attio/manifest.yaml` | opt-in | http `https://mcp.attio.com/mcp`; auth oauth | connector_slug `attio` | planned |
| MCPX-008 | `aws-knowledge` | Authoritative AWS docs, API references, and best practices. | `optional-mcps/aws-knowledge/manifest.yaml` | opt-in | http `https://knowledge-mcp.global.api.aws`; auth none |  | planned |
| MCPX-009 | `betterstack` | Better Stack: logs, uptime monitors, incidents, and status pages. | `optional-mcps/betterstack/manifest.yaml` | opt-in | http `https://mcp.betterstack.com`; auth oauth | connector_slug `better_stack_mcp` | planned |
| MCPX-010 | `buildkite` | CI/CD pipelines, builds, and test results from Buildkite. | `optional-mcps/buildkite/manifest.yaml` | opt-in | http `https://mcp.buildkite.com/mcp`; auth oauth |  | planned |
| MCPX-011 | `calendly` | Scheduling links, events, and invitees from Calendly. | `optional-mcps/calendly/manifest.yaml` | opt-in | http `https://mcp.calendly.com`; auth oauth | connector_slug `calendly` | planned |
| MCPX-012 | `canva` | Create, search, and manage Canva designs. | `optional-mcps/canva/manifest.yaml` | opt-in | http `https://mcp.canva.com/mcp`; auth oauth | connector_slug `canva_mcp` | planned |
| MCPX-013 | `circleci` | CircleCI: diagnose build failures, read logs, rerun workflows. | `optional-mcps/circleci/manifest.yaml` | opt-in | http `https://mcp.circleci.com/v1/mcp`; auth oauth |  | planned |
| MCPX-014 | `clickup` | Tasks, docs, and workspaces in ClickUp. | `optional-mcps/clickup/manifest.yaml` | opt-in | http `https://mcp.clickup.com/mcp`; auth oauth |  | planned |
| MCPX-015 | `close` | Sales CRM: leads, opportunities, calls, and emails. | `optional-mcps/close/manifest.yaml` | opt-in | http `https://mcp.close.com/mcp`; auth oauth |  | planned |
| MCPX-016 | `cloudflare` | Full Cloudflare API access via the official remote MCP. | `optional-mcps/cloudflare/manifest.yaml` | opt-in | http `https://mcp.cloudflare.com/mcp?codemode=false`; auth oauth | connector_slug `cloudflare_mcp` | planned |
| MCPX-017 | `cloudinary` | Upload, search, and transform media assets in Cloudinary. | `optional-mcps/cloudinary/manifest.yaml` | opt-in | http `https://asset-management.mcp.cloudinary.com/mcp`; auth oauth |  | planned |
| MCPX-018 | `comfy-cloud` | Generate images, video, audio, and 3D on Comfy Cloud. | `optional-mcps/comfy-cloud/manifest.yaml` | opt-in | http `https://cloud.comfy.org/mcp`; auth oauth |  | planned |
| MCPX-019 | `context7` | Up-to-date, version-specific library docs and code examples. | `optional-mcps/context7/manifest.yaml` | opt-in | http `https://mcp.context7.com/mcp`; auth none |  | planned |
| MCPX-020 | `craft` | Craft: structured docs, tasks, and personal knowledge base. | `optional-mcps/craft/manifest.yaml` | opt-in | http `https://mcp.craft.do/my/mcp`; auth oauth |  | planned |
| MCPX-021 | `datadog` | Logs, monitors, dashboards, and incidents from Datadog. | `optional-mcps/datadog/manifest.yaml` | opt-in | http `https://mcp.datadoghq.com/api/unstable/mcp-server/mcp`; auth oauth | connector_slug `datadog_mcp` | planned |
| MCPX-022 | `deepwiki` | Ask questions about any public GitHub repo (Devin's DeepWiki). | `optional-mcps/deepwiki/manifest.yaml` | opt-in | http `https://mcp.deepwiki.com/mcp`; auth none |  | planned |
| MCPX-023 | `dropbox` | Search, read, and manage files in Dropbox. | `optional-mcps/dropbox/manifest.yaml` | opt-in | http `https://mcp.dropbox.com/mcp`; auth oauth | connector_slug `dropbox` | planned |
| MCPX-024 | `figma` | Official Figma remote MCP — design context, Code Connect, and write-to-canvas via https://mcp.figma.com/mcp (OAuth). | `optional-mcps/figma/manifest.yaml` | opt-in | http `https://mcp.figma.com/mcp`; auth oauth | connector_slug `figma` | planned |
| MCPX-025 | `fireflies` | Meeting transcripts, summaries, and action items. | `optional-mcps/fireflies/manifest.yaml` | opt-in | http `https://api.fireflies.ai/mcp`; auth oauth |  | planned |
| MCPX-026 | `gamma` | Gamma: generate and edit AI presentations, docs, and sites. | `optional-mcps/gamma/manifest.yaml` | opt-in | http `https://mcp.gamma.app/mcp`; auth oauth |  | planned |
| MCPX-027 | `gitlab` | GitLab: issues, merge requests, pipelines, and repo context. | `optional-mcps/gitlab/manifest.yaml` | opt-in | http `https://gitlab.com/api/v4/mcp`; auth oauth |  | planned |
| MCPX-028 | `globalping` | Ping, traceroute, DNS, and HTTP tests from global probes. | `optional-mcps/globalping/manifest.yaml` | opt-in | http `https://mcp.globalping.dev/mcp`; auth oauth |  | planned |
| MCPX-029 | `grafana` | Query metrics, logs, dashboards, alerts, and incidents from Grafana Cloud. | `optional-mcps/grafana/manifest.yaml` | opt-in | http `https://mcp.grafana.com/mcp`; auth oauth |  | planned |
| MCPX-030 | `hugging_face` | Models, datasets, Spaces, and papers from the Hugging Face Hub. | `optional-mcps/hugging_face/manifest.yaml` | opt-in | http `https://huggingface.co/mcp`; auth oauth |  | planned |
| MCPX-031 | `indeed` | Search jobs and listings on Indeed. | `optional-mcps/indeed/manifest.yaml` | opt-in | http `https://mcp.indeed.com/claude/mcp`; auth oauth |  | planned |
| MCPX-032 | `intercom` | Conversations, tickets, and customer data from Intercom. | `optional-mcps/intercom/manifest.yaml` | opt-in | http `https://mcp.intercom.com/mcp`; auth oauth | connector_slug `intercom_mcp` | planned |
| MCPX-033 | `kiwi` | Kiwi.com flight search: itineraries with direct booking links. | `optional-mcps/kiwi/manifest.yaml` | opt-in | http `https://mcp.kiwi.com`; auth none |  | planned |
| MCPX-034 | `klaviyo` | Klaviyo marketing: campaigns, flows, segments, and reporting. | `optional-mcps/klaviyo/manifest.yaml` | opt-in | http `https://mcp.klaviyo.com/mcp?core-tools-only=true&disable-tools-with-user-generated-content=true`; auth oauth |  | planned |
| MCPX-035 | `linear` | Find, create, and update Linear issues, projects, and comments. | `optional-mcps/linear/manifest.yaml` | opt-in | http `https://mcp.linear.app/mcp`; auth oauth | connector_slug `linear` | planned |
| MCPX-036 | `microsoft-learn` | Official Microsoft, Azure, and .NET docs and code samples. | `optional-mcps/microsoft-learn/manifest.yaml` | opt-in | http `https://learn.microsoft.com/api/mcp`; auth none |  | planned |
| MCPX-037 | `miro` | Read and edit Miro boards, diagrams, and frames. | `optional-mcps/miro/manifest.yaml` | opt-in | http `https://mcp.miro.com/`; auth oauth |  | planned |
| MCPX-038 | `mixpanel` | Mixpanel analytics: events, funnels, retention, dashboards. | `optional-mcps/mixpanel/manifest.yaml` | opt-in | http `https://mcp.mixpanel.com/mcp`; auth oauth |  | planned |
| MCPX-039 | `monday` | Boards, items, docs, and workflows in monday.com. | `optional-mcps/monday/manifest.yaml` | opt-in | http `https://mcp.monday.com/mcp`; auth oauth |  | planned |
| MCPX-040 | `motherduck` | MotherDuck: query DuckDB cloud warehouses with SQL. | `optional-mcps/motherduck/manifest.yaml` | opt-in | http `https://api.motherduck.com/mcp`; auth oauth |  | planned |
| MCPX-041 | `n8n-official` | Connect to your n8n instance's official MCP server with browser OAuth. | `optional-mcps/n8n-official/manifest.yaml` | opt-in | http `${N8N_MCP_SERVER_URL}`; auth oauth; env N8N_MCP_SERVER_URL |  | planned |
| MCPX-042 | `neon` | Neon serverless Postgres: projects, branches, and SQL. | `optional-mcps/neon/manifest.yaml` | opt-in | http `https://mcp.neon.tech/mcp`; auth oauth | connector_slug `neon_mcp` | planned |
| MCPX-043 | `netlify` | Sites, deploys, and env vars via Netlify's hosted MCP. | `optional-mcps/netlify/manifest.yaml` | opt-in | http `https://netlify-mcp.netlify.app/mcp`; auth oauth |  | planned |
| MCPX-044 | `notion` | Pages and databases from your Notion workspace. | `optional-mcps/notion/manifest.yaml` | opt-in | http `https://mcp.notion.com/mcp`; auth oauth | connector_slug `notion` | planned |
| MCPX-045 | `paypal` | Payments, invoices, and subscriptions via PayPal's hosted MCP. | `optional-mcps/paypal/manifest.yaml` | opt-in | http `https://mcp.paypal.com/sse`; auth oauth |  | planned |
| MCPX-046 | `plaid` | Plaid dashboard: integrations, Items, and usage debugging. | `optional-mcps/plaid/manifest.yaml` | opt-in | http `https://api.dashboard.plaid.com/mcp/`; auth oauth |  | planned |
| MCPX-047 | `postman` | Postman workspaces, collections, environments, and APIs. | `optional-mcps/postman/manifest.yaml` | opt-in | http `https://mcp.postman.com/minimal`; auth oauth |  | planned |
| MCPX-048 | `prisma-postgres` | Create and manage Prisma Postgres databases. | `optional-mcps/prisma-postgres/manifest.yaml` | opt-in | http `https://mcp.prisma.io/mcp`; auth oauth | connector_slug `prisma` | planned |
| MCPX-049 | `railway` | Railway: projects, services, deployments, and environments. | `optional-mcps/railway/manifest.yaml` | opt-in | http `https://mcp.railway.com`; auth oauth | connector_slug `railway` | planned |
| MCPX-050 | `robinhood` | Robinhood agentic trading: portfolio, balances, and orders. | `optional-mcps/robinhood/manifest.yaml` | opt-in | http `https://agent.robinhood.com/mcp/trading`; auth oauth |  | planned |
| MCPX-051 | `semgrep` | Scan code for security vulnerabilities with Semgrep. | `optional-mcps/semgrep/manifest.yaml` | opt-in | http `https://mcp.semgrep.ai/mcp`; auth oauth |  | planned |
| MCPX-052 | `sentry` | Issues, stack traces, and error context from Sentry. | `optional-mcps/sentry/manifest.yaml` | opt-in | http `https://mcp.sentry.dev/mcp`; auth oauth | connector_slug `sentry_mcp` | planned |
| MCPX-053 | `square` | Catalog, orders, and payments via Square's hosted MCP. | `optional-mcps/square/manifest.yaml` | opt-in | http `https://mcp.squareup.com/sse`; auth oauth |  | planned |
| MCPX-054 | `strava` | Strava: activities, fitness trends, training load (read-only). | `optional-mcps/strava/manifest.yaml` | opt-in | http `https://mcp.strava.com/mcp`; auth oauth |  | planned |
| MCPX-055 | `stripe` | Payments, customers, and invoices via Stripe's hosted MCP. | `optional-mcps/stripe/manifest.yaml` | opt-in | http `https://mcp.stripe.com`; auth oauth | connector_slug `stripe_mcp` | planned |
| MCPX-056 | `supabase` | Database, auth, and storage from your Supabase projects. | `optional-mcps/supabase/manifest.yaml` | opt-in | http `https://mcp.supabase.com/mcp`; auth oauth | connector_slug `supabase` | planned |
| MCPX-057 | `todoist` | Manage Todoist tasks and projects. | `optional-mcps/todoist/manifest.yaml` | opt-in | http `https://ai.todoist.net/mcp`; auth oauth | connector_slug `todoist` | planned |
| MCPX-058 | `trivago` | trivago hotel search: compare prices by city and dates. | `optional-mcps/trivago/manifest.yaml` | opt-in | http `https://mcp.trivago.com/mcp`; auth none |  | planned |
| MCPX-059 | `twelve-data` | Stocks, forex, and crypto market data from Twelve Data. | `optional-mcps/twelve-data/manifest.yaml` | opt-in | http `https://mcp.twelvedata.com/mcp`; auth oauth |  | planned |
| MCPX-060 | `twilio-docs` | Twilio developer docs search (public beta, read-only). | `optional-mcps/twilio-docs/manifest.yaml` | opt-in | http `https://mcp.twilio.com/docs`; auth none |  | planned |
| MCPX-061 | `unreal-engine` | Drive the Unreal Engine 5.8 editor over its local MCP server. | `optional-mcps/unreal-engine/manifest.yaml` | opt-in | http `http://127.0.0.1:8000/mcp`; auth none |  | planned |
| MCPX-062 | `vercel` | Deployments, logs, and projects via Vercel's hosted MCP. | `optional-mcps/vercel/manifest.yaml` | opt-in | http `https://mcp.vercel.com`; auth oauth | connector_slug `vercel_mcp` | planned |
| MCPX-063 | `webflow` | Sites, CMS collections, and pages via Webflow's hosted MCP. | `optional-mcps/webflow/manifest.yaml` | opt-in | http `https://mcp.webflow.com/mcp`; auth oauth |  | planned |
| MCPX-064 | `wolfram` | Wolfram\|Alpha computation, math, and curated knowledge. | `optional-mcps/wolfram/manifest.yaml` | opt-in | http `https://agenttools.wolfram.com/mcp`; auth none |  | planned |
| MCPX-065 | `wordpress-com` | WordPress.com: posts, pages, drafts, stats, and comments. | `optional-mcps/wordpress-com/manifest.yaml` | opt-in | http `https://public-api.wordpress.com/wpcom/v2/mcp/v1`; auth oauth |  | planned |

## 10. MEDIA — suara, TTS/STT, vision, generasi media
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| MEDIA-001 | Voice mode (CLI/TUI) | Rekam suara → STT → agent → TTS; push-to-talk | `tools/voice_mode.py`, `hermes_cli/cli_voice_mixin.py` | off (`/voice` toggle) | extra `voice` (sounddevice, numpy, faster-whisper) | record_key ctrl+b, barge-in | planned |
| MEDIA-002 | Voice live (GPT realtime) | Mode voice chat realtime `gpt_live` | `tools/voice_live.py` | opt-in (`voice.voice_chat_mode`) | OpenAI realtime API | default mode `chained` | planned |
| MEDIA-003 | Wake word | Listener "hey hermes" membuka sesi | `tools/wake_word.py`, `tools/wake_word_engines.py:40` | off (`wake_word.enabled`) | extra `wake` (openWakeWord / sherpa-onnx / Porcupine) | `/wake` | planned |
| MEDIA-004 | Wake engine: openWakeWord | Model wake word hey_hermes | `tools/wake_word_engines.py:78` | auto | pyopen-wakeword |  | planned |
| MEDIA-005 | Wake engine: sherpa-onnx KWS | Keyword spotting kosakata terbuka | `tools/wake_word_engines.py:177` | auto | sherpa-onnx + model unduhan |  | planned |
| MEDIA-006 | Wake engine: Porcupine | Picovoice Porcupine | `tools/wake_word_engines.py:257` | auto | pvporcupine + access key |  | planned |
| MEDIA-007 | TTS (11 provider built-in) | text_to_speech + auto TTS balasan | `agent/tts_registry.py:25`, `tools/tts_tool.py`, `tools/tts_tool_providers.py` | on, provider `edge` | per provider; lokal: piper/neutts/kittentts | provider: edge, elevenlabs, openai, minimax, xai, mistral, gemini, neutts, kittentts, piper, deepinfra | planned |
| MEDIA-008 | TTS streaming | Streaming audio TTS (gateway/voice) | `tools/tts_streaming.py`, `gateway/platforms/base_streaming_tts.py` | on bila didukung | — |  | planned |
| MEDIA-009 | TTS command provider | TTS via perintah eksternal | `tools/tts_command_provider.py` | opt-in | binary eksternal |  | planned |
| MEDIA-010 | STT (8 provider built-in) | Transkripsi pesan suara & voice mode | `agent/transcription_registry.py:23`, `tools/transcription_tools.py` | on (`stt.enabled`) | faster-whisper lokal / whisper.cpp / API cloud | provider: local, local_command, groq, openai, mistral, xai, elevenlabs, deepinfra | planned |
| MEDIA-011 | STT whisper.cpp & chunking | Backend whisper.cpp, chunk audio panjang | `tools/transcription_whisper_cpp.py`, `tools/transcription_chunking.py` | opt-in | whisper.cpp binary, ffmpeg [INFERENCE] |  | planned |
| MEDIA-012 | Vision (vision_analyze) | Analisis gambar via model vision auxiliary | `tools/vision_tools.py`, `agent/vision_message_prep.py` | on (core) | Model vision, Pillow |  | planned |
| MEDIA-013 | Image routing native | Kirim gambar native ke model multimodal | `agent/image_routing.py` | on | — |  | planned |
| MEDIA-014 | Video analyze | Analisis video | `tools/vision_tools.py:2` | off (toolset `video`) | Model multimodal |  | planned |
| MEDIA-015 | Image generation | image_generate dengan 8 backend plugin | `tools/image_generation_tool.py`, `agent/image_gen_registry.py` | on (core) bila backend tersedia | lihat backend |  | planned |
| MEDIA-016 | Image backend: FAL | flux, nano-banana, gpt-image dsb. | `plugins/image_gen/fal/plugin.yaml` | opt-in | FAL_KEY, extra `fal` |  | planned |
| MEDIA-017 | Image backend: OpenAI | GPT Image | `plugins/image_gen/openai/plugin.yaml` | opt-in | OPENAI_API_KEY |  | planned |
| MEDIA-018 | Image backend: OpenAI Codex OAuth | gpt-image via OAuth ChatGPT | `plugins/image_gen/openai-codex/plugin.yaml` | opt-in | Codex OAuth |  | planned |
| MEDIA-019 | Image backend: OpenRouter/Nous | Image via chat completions OpenRouter/Nous | `plugins/image_gen/openrouter/plugin.yaml` | opt-in | OPENROUTER_API_KEY |  | planned |
| MEDIA-020 | Image backend: xAI | grok-imagine-image | `plugins/image_gen/xai/plugin.yaml` | opt-in | XAI_API_KEY |  | planned |
| MEDIA-021 | Image backend: DeepInfra | FLUX, Qwen-Image | `plugins/image_gen/deepinfra/plugin.yaml` | opt-in | DEEPINFRA_API_KEY |  | planned |
| MEDIA-022 | Image backend: Krea | Krea 2 | `plugins/image_gen/krea/plugin.yaml` | opt-in | KREA_API_KEY |  | planned |
| MEDIA-023 | Image backend: Meta AI | muse-image | `plugins/image_gen/meta-ai/plugin.yaml` | opt-in | META_MODEL_API_KEY |  | planned |
| MEDIA-024 | Video generation | video_generate (+ xAI edit/extend) | `tools/video_generation_tool.py`, `agent/video_gen_registry.py` | off (toolset `video_gen`) | lihat backend |  | planned |
| MEDIA-025 | Video backend: FAL | Veo 3.1, Kling, Pixverse | `plugins/video_gen/fal/plugin.yaml` | opt-in | FAL_KEY |  | planned |
| MEDIA-026 | Video backend: xAI | Grok Imagine video (edit/extend) | `plugins/video_gen/xai/plugin.yaml` | opt-in | XAI_API_KEY |  | planned |
| MEDIA-027 | Video backend: OpenRouter | /api/v1/videos (Veo, Sora, Kling…) | `plugins/video_gen/openrouter/plugin.yaml` | opt-in | OPENROUTER_API_KEY |  | planned |
| MEDIA-028 | Video backend: DeepInfra | text/image-to-video | `plugins/video_gen/deepinfra/plugin.yaml` | opt-in | DEEPINFRA_API_KEY |  | planned |
| MEDIA-029 | Discord voice channel | Join voice channel Discord, STT/TTS, voice mixer | `plugins/platforms/discord/voice_mixer.py`, `gateway/run_voice.py` | opt-in | discord.py[voice], ffmpeg, opus |  | planned |
| MEDIA-030 | Media cache & repair gateway | Unduh/cache/perbaiki media inbound | `gateway/platforms/media_cache.py`, `gateway/media_repair.py` | on | ffmpeg [INFERENCE] |  | planned |
| MEDIA-031 | Silk audio (WeChat/QQ) | Dekode audio SILK | `pyproject.toml:342` | opt-in | extra `silk` (pilk) |  | planned |

## 11. EXEC — eksekusi, sandbox, approval, secret
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| EXEC-001 | Terminal backend: local | Eksekusi shell di host lokal | `tools/environments/local.py`, `tools/terminal_tool_backends.py:54` | on (`terminal.backend: local`) | shell (bash/zsh/PowerShell) |  | planned |
| EXEC-002 | Terminal backend: docker | Eksekusi di container Docker (+ egress proxy) | `tools/environments/docker.py`, `tools/environments/docker_egress.py` | opt-in | Docker daemon |  | planned |
| EXEC-003 | Terminal backend: singularity | Eksekusi di container Singularity/Apptainer | `tools/environments/singularity.py` | opt-in | singularity/apptainer |  | planned |
| EXEC-004 | Terminal backend: modal | Sandbox cloud Modal (+ managed modal) | `tools/environments/modal.py`, `tools/environments/managed_modal.py` | opt-in | extra `modal`, token Modal |  | planned |
| EXEC-005 | Terminal backend: daytona | Sandbox cloud Daytona | `tools/environments/daytona.py` | opt-in | extra `daytona`, API key |  | planned |
| EXEC-006 | Terminal backend: vercel_sandbox | Vercel Sandbox | `tools/environments/vercel_sandbox.py`, `hermes_cli/vercel_auth.py` | opt-in | extra `vercel`, token |  | planned |
| EXEC-007 | Terminal backend: ssh | Eksekusi remote via SSH (+ file sync) | `tools/environments/ssh.py`, `tools/environments/file_sync.py` | opt-in | ssh client |  | planned |
| EXEC-008 | Terminal env plugin registry | Backend terminal tambahan via plugin | `agent/terminal_env_registry.py`, `agent/terminal_env_provider.py` | opt-in | — |  | planned |
| EXEC-009 | Background processes | Proses background dengan registry, log, notifikasi | `tools/process_registry.py`, `tools/terminal_tool_background.py` | on | — | `/stop`, process dock | planned |
| EXEC-010 | PTY support | PTY untuk perintah interaktif | `hermes_cli/pty_session.py`, `hermes_cli/win_pty_bridge.py` | on | ptyprocess / pywinpty |  | planned |
| EXEC-011 | Sudo handling | Prompt sudo aman | `tools/terminal_tool_sudo.py` | on | — |  | planned |
| EXEC-012 | Code execution (execute_code) | Python yang memanggil tool via RPC; kernel persisten | `tools/code_execution_tool.py`, `tools/code_kernel.py`, `tools/code_execution_rpc.py` | on (`code_execution.mode: project`) | Python |  | planned |
| EXEC-013 | Approval modes (smart/manual/yolo) | Approval perintah berbahaya; smart via LLM | `tools/approval.py`, `tools/approval_smart.py`, `tools/approval_yolo.py` | on (`approvals.mode: smart`) | LLM auxiliary (`auxiliary.approval`) | `/yolo`, `/approvals`; cron/unattended deny | planned |
| EXEC-014 | Dangerous command detection | Pola perintah berbahaya & floor approval | `tools/approval_detection.py`, `tools/approval_floors.py`, `tools/threat_patterns.py` | on | — |  | planned |
| EXEC-015 | Command allowlist & approvals suggest | Allowlist + tambang riwayat jadi usulan | `hermes_cli/approvals_suggest.py` | on (allowlist kosong) | — | `hermes approvals` | planned |
| EXEC-016 | Approval transport eksternal | Kirim approval ke transport (builtin/eksternal) | `hermes_cli/approval_transport.py` | builtin | — | fallback deny | planned |
| EXEC-017 | Gateway approval `/approve` `/deny` | Approval perintah dari chat | `gateway/platforms/base_exec_approval.py` | on (gateway) | — |  | planned |
| EXEC-018 | Tool guardrails | Batas loop/ukuran output tool | `agent/tool_guardrails.py`, `tools/tool_output_limits.py` | on | — |  | planned |
| EXEC-019 | Path security / file safety | Cegah tulis ke path sensitif; protected instruction files | `tools/path_security.py`, `agent/file_safety.py`, `tools/file_tools_write_guards.py` | on (`security.protected_instruction_files`) | — |  | planned |
| EXEC-020 | URL safety / SSRF guard | Blok URL privat & blocklist website | `tools/url_safety.py`, `tools/website_policy.py` | on (private off; blocklist off) | — |  | planned |
| EXEC-021 | Secret redaction | Redaksi secret di output/log | `agent/redact.py` | on (`security.redact_secrets`) | — | PII redaction off | planned |
| EXEC-022 | Self-repo guard | Cegah agent merusak checkout Hermes sendiri | `tools/self_repo_guard.py`, `agent/runtime_self_protection.py` | on | — |  | planned |
| EXEC-023 | Plugin guard | Batasi akses plugin | `tools/plugin_guard.py` | on | — |  | planned |
| EXEC-024 | Env passthrough & credential files | Kontrol env/file kredensial ke sandbox | `tools/env_passthrough.py`, `tools/credential_files.py` | on | — |  | planned |
| EXEC-025 | Vault (autofill) backend: local | Store Fernet terenkripsi lokal | `agent/vault_backends/local.py`, `agent/vault_store.py` | on | cryptography | `hermes vault` | planned |
| EXEC-026 | Vault backend: 1Password | Ambil kredensial autofill dari 1Password | `agent/vault_backends/onepassword.py` | on bila tersedia (`vault.onepassword.enabled`) | CLI `op` |  | planned |
| EXEC-027 | Vault backend: Bitwarden | Ambil kredensial autofill dari Bitwarden | `agent/vault_backends/bitwarden.py` | on bila tersedia (`vault.bitwarden.enabled`) | CLI `bw` |  | planned |
| EXEC-028 | Vault unlock | Alur unlock vault | `agent/vault_backends/unlock.py` | on | — |  | planned |
| EXEC-029 | Secret source: Bitwarden | Sumber secret/env dari Bitwarden | `agent/secret_sources/bitwarden.py`, `agent/secret_sources/registry.py:40` | off (`secrets.bitwarden.enabled`) | CLI `bw` (auto install) | encrypted cache opsional | planned |
| EXEC-030 | Secret source: 1Password | Sumber secret dari 1Password | `agent/secret_sources/onepassword.py` | off | CLI `op` |  | planned |
| EXEC-031 | Secret source: command | Secret dari perintah arbitrer | `agent/secret_sources/command.py` | opt-in | — |  | planned |
| EXEC-032 | Egress firewall (iron-proxy) | Injeksi kredensial & firewall egress | `agent/proxy_sources/iron_proxy.py`, `hermes_cli/subcommands/egress.py` | off (`proxy.enabled`) | iron-proxy (auto install) | `/egress` | planned |
| EXEC-033 | Git worktree isolation | Worktree terisolasi untuk subagent/sesi | `tools/subagent_worktree.py`, `hermes_cli/worktree_cmd.py` | opt-in | git | `/worktree`, `hermes worktree` | planned |
| EXEC-034 | Delegation (subagent) | delegate_task: subagent paralel, kedalaman spawn, orkestrator | `tools/delegate_tool.py`, `tools/async_delegation.py` | on (max 10 children, depth 1) | LLM |  | planned |
| EXEC-035 | LSP integration | Diagnostik Language Server setelah edit | `agent/lsp/manager.py`, `agent/lsp/servers.py` | on (`lsp.enabled`) | Language server (npm install auto) | `hermes lsp` | planned |
| EXEC-036 | Computer use | Kendali desktop via cua-driver | `tools/computer_use_tool.py`, `tools/computer_use/cua_backend.py` | on core, gated cua-driver | cua-driver | `hermes computer-use` | planned |
| EXEC-037 | Browser engine: local (agent-browser) | Chromium lokal terkelola | `tools/browser_tool.py`, `tools/browser_tool_install.py` | auto (`browser.engine: auto`) | Chromium/agent-browser |  | planned |
| EXEC-038 | Browser engine: Camofox | Browser anti-deteksi Camofox | `tools/browser_camofox.py` | opt-in | Camofox |  | planned |
| EXEC-039 | Browser engine: Lightpanda | Browser headless ringan + fallback | `tools/browser_lightpanda.py`, `tools/browser_tool_lightpanda_fallback.py` | opt-in | Lightpanda binary |  | planned |
| EXEC-040 | Browser real profile / CDP connect | Pakai profil browser asli user via CDP | `tools/browser_tool_real_profile.py`, `hermes_cli/browser_connect.py` | opt-in (`/browser connect`) | Chrome/Brave/Edge |  | planned |
| EXEC-041 | Browser extension control | Kontrol via ekstensi browser | `tools/browser_extension_router.py` | off (`browser.extension_control.enabled`) | Ekstensi browser |  | planned |
| EXEC-042 | Web backends (11) | Backend web_search/extract plugin | `plugins/web/__init__.py` | auto | brave_free, ddgs, exa, firecrawl, keenable, openai_native, parallel, perplexity, searxng, tavily, xai |  | planned |
| EXEC-043 | Supply-chain audit (OSV) | Audit venv/plugin/MCP terhadap OSV.dev | `tools/osv_check.py`, `hermes_cli/security_audit.py` | opt-in (`hermes security`) | OSV.dev | advisori startup | planned |
| EXEC-044 | Website blocklist | Blok domain tertentu | `tools/website_policy.py` | off | — |  | planned |

## 12. OPS — operasional
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| OPS-001 | Profiles | Beberapa instance Hermes terisolasi (HERMES_HOME per profil) | `hermes_cli/profiles.py`, `hermes_cli/profile_cmd.py` | default profile | — | `hermes profile`, `-p/--profile`, `/profile` | planned |
| OPS-002 | Profile export/import | Ekspor/impor profil (config, skills, tema) | `hermes_cli/profile_distribution.py` | opt-in | — | `/export`, `/import` | planned |
| OPS-003 | Self-update (git/zip/Windows) | Update checkout + fleet, rollback, stash | `hermes_cli/update_cmd.py`, `hermes_cli/update_cmd_git.py`, `hermes_cli/update_cmd_zip.py` | cek update on (`updates.check`) | git, uv | pre-update backup quick | planned |
| OPS-004 | Release channels | Kanal rilis (stable/dll.) | `hermes_cli/release_channels.py`, `hermes_cli/update_channel.py` | stable [INFERENCE] | — |  | planned |
| OPS-005 | Uninstall | Hapus instalasi Hermes | `hermes_cli/uninstall.py` | opt-in | — |  | planned |
| OPS-006 | Backup / import | Backup HERMES_HOME ke zip & restore (SQLite aman) | `hermes_cli/backup.py`, `hermes_cli/backup_restore.py`, `hermes_cli/backup_sqlite.py` | opt-in | — | `hermes backup`, `hermes import` | planned |
| OPS-007 | Config management | config.yaml + .env, migrasi versi, backup config | `hermes_cli/config.py`, `hermes_cli/config_migrations.py`, `hermes_cli/config_backups.py` | on | — | `hermes config` | planned |
| OPS-008 | Setup wizard | Setup interaktif provider/platform/terminal/TTS | `hermes_cli/setup.py`, `hermes_cli/setup_quick.py` | first run | — | `hermes setup` | planned |
| OPS-009 | Logging | Log rotasi per komponen | `hermes_logging.py`, `hermes_cli/logs.py` | on (INFO, 5 MB × 3) | — | `hermes logs` | planned |
| OPS-010 | Debug upload / dump | Upload laporan debug & dump setup | `hermes_cli/debug.py`, `hermes_cli/diagnostics_upload.py`, `hermes_cli/dump.py` | opt-in | Network | redaksi debug | planned |
| OPS-011 | Doctor | Cek config, dependensi, konektivitas, platform | `hermes_cli/doctor.py`, `hermes_cli/doctor_connectivity.py` | opt-in | — |  | planned |
| OPS-012 | Status | Status semua komponen | `hermes_cli/status.py`, `hermes_cli/status_report.py` | opt-in | — | `/status` | planned |
| OPS-013 | Usage / rate limits | Jendela rate limit akun & token usage | `agent/account_usage.py`, `agent/rate_limit_tracker.py`, `hermes_state_usage.py` | on | — | `hermes usage`, `/usage` | planned |
| OPS-014 | Billing / Nous subscription | Saldo, plan, topup Nous | `agent/billing_usage.py`, `hermes_cli/nous_billing.py`, `hermes_cli/nous_subscription.py` | on bila Nous | Nous Portal | `/topup`, `/subscription` | dropped-with-approval (NP-19) |
| OPS-015 | Usage pricing / cost | Estimasi biaya per model | `agent/usage_pricing.py`, `hermes_cli/models_pricing.py` | on | models.dev data |  | planned |
| OPS-016 | Insights | Analitik penggunaan | `agent/insights.py` | opt-in | — | `hermes insights` | planned |
| OPS-017 | Prompt size | Rincian byte system prompt + schema tool | `hermes_cli/prompt_size.py` | opt-in | — |  | planned |
| OPS-018 | Monitoring & OTLP export | Health gateway, export OTLP | `agent/monitoring/otlp_exporter.py`, `agent/monitoring/gateway_health.py` | off (`monitoring.export.otlp.enabled`) | extra `otlp` |  | planned |
| OPS-019 | Telemetry shared metrics | Metrik bersama ke Nous | `hermes_cli/config_defaults.py:2328` | off | Network |  | dropped-with-approval (NP-19) |
| OPS-020 | Batch runner | Jalankan prompt massal paralel → trajectory | `batch_runner.py`, `toolset_distributions.py` | opt-in (skrip) | — | research/RL data | planned |
| OPS-021 | Trajectory save & compressor | Simpan & kompres trajectory untuk training | `agent/trajectory.py`, `trajectory_compressor.py` | opt-in | — |  | planned |
| OPS-022 | Trace upload (HF) | Upload trace ke Hugging Face | `agent/trace_upload.py` | opt-in | extra `trace-upload` |  | planned |
| OPS-023 | Mini SWE runner | Runner tugas SWE format trajectory Hermes | `mini_swe_runner.py` | opt-in (skrip) | Docker/Modal opsional |  | planned |
| OPS-024 | Evals | Suite evaluasi/probe perilaku | `evals/__init__.py` | dev only | — | 74 entri di `evals/` | planned |
| OPS-025 | Verify | Deteksi resep run proyek & smoke test | `hermes_cli/verify_cmd.py`, `agent/verify` | opt-in | — | `hermes verify` | planned |
| OPS-026 | Service manager (systemd/launchd/Windows) | Install gateway sebagai service | `hermes_cli/service_manager.py`, `hermes_cli/gateway_launchd.py`, `hermes_cli/gateway_windows.py` | opt-in | systemd/launchd/Task Scheduler |  | planned |
| OPS-027 | Startup watchdog & bootstrap | Watchdog startup, UTF-8 stdio Windows | `hermes_startup_watchdog.py`, `hermes_bootstrap.py` | on | — |  | planned |
| OPS-028 | Install repair / early recovery | Pulihkan update yang terputus | `hermes_cli/_early_recovery.py`, `hermes_cli/_install_repair.py` | on | — |  | planned |
| OPS-029 | Docker image & compose | Image Docker resmi | `Dockerfile`, `docker-compose.yml` | opt-in | Docker |  | planned |
| OPS-030 | Nix flake | Paket Nix | `flake.nix` | opt-in | Nix |  | planned |
| OPS-031 | Termux/Android | Dukungan Termux | `hermes_cli/psutil_android.py`, `pyproject.toml:439` | opt-in | Termux | extra `termux` | planned |
| OPS-032 | Installer scripts | setup-hermes.sh / .ps1 | `setup-hermes.sh`, `setup-hermes.ps1` | — | bash/PowerShell, uv |  | planned |
| OPS-033 | Data cleanup / disk status | Bersihkan data & laporan disk | `hermes_cli/data_cleanup.py`, `gateway/disk_status.py` | opt-in | — |  | planned |
| OPS-034 | OpenClaw migration | Migrasi dari OpenClaw | `hermes_cli/claw.py` | opt-in | — | `hermes claw` | planned |
| OPS-035 | Import Claude Code/Codex setup | Impor setup agent lain | `hermes_cli/agent_import.py` | opt-in | — | `hermes import-agent` | planned |
| OPS-036 | Model/config migration | Migrasi model pensiun/setting usang | `hermes_cli/migrate.py`, `hermes_cli/xai_retirement.py` | opt-in | — | `hermes migrate` | dropped-with-approval (NP-15) |
| OPS-037 | Emergency stop `pause` | Hentikan dispatch cron/kanban & turn baru | `hermes_cli/subcommands/pause.py`, `agent/estop.py` | opt-in | — | `hermes pause/resume`, `/pause` | planned |
| OPS-038 | Projects | Workspace multi-folder bernama | `hermes_cli/projects_cmd.py`, `hermes_cli/projects_db.py` | opt-in | — | `hermes project` | planned |

## 13. GW — fitur inti gateway (tambahan)
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| GW-001 | Gateway runtime | Proses gateway multi-platform (run, start/stop, service) | `gateway/run.py`, `hermes_cli/subcommands/gateway.py` | opt-in (`hermes gateway`) | — | 13 sub-subcommand | planned |
| GW-002 | Platform registry (deferred load) | Registrasi adapter platform tanpa impor SDK saat start | `gateway/platform_registry.py` | on | — |  | planned |
| GW-003 | DM pairing | Kode pairing untuk otorisasi user | `gateway/pairing.py`, `hermes_cli/pairing.py` | on (gateway) | — | `hermes pairing` | planned |
| GW-004 | Access policy / allowlist | Allowlist user/chat, admin vs user slash | `gateway/platforms/access_policy_mixin.py`, `gateway/slash_access.py` | on | — | `/whoami` | planned |
| GW-005 | Home channel | Set channel rumah untuk notifikasi | `hermes_cli/commands.py:178` | opt-in | — | `/sethome` | planned |
| GW-006 | Streaming edits ke chat | Edit pesan bertahap saat streaming | `gateway/stream_consumer.py` | off (`streaming.enabled: false`) | — |  | planned |
| GW-007 | Human delay | Jeda acak meniru manusia | `hermes_cli/config_defaults.py:1294` | off | — |  | planned |
| GW-008 | Bot loop guard | Cegah loop bot-ke-bot | `gateway/bot_loop_guard.py` | on | — |  | planned |
| GW-009 | Delivery ledger / queue | Pengiriman andal + dedupe | `gateway/delivery.py`, `gateway/delivery_ledger.py` | on | — |  | planned |
| GW-010 | Send CLI `hermes send` | Kirim pesan ke platform dari skrip/CI | `hermes_cli/send_cmd.py` | opt-in | Platform terkonfigurasi |  | planned |
| GW-011 | Webhook platform & subscriptions | Terima webhook (toolset aman) + subscription dinamis | `gateway/platforms/webhook.py`, `hermes_cli/webhook.py` | opt-in | — | toolset `hermes-webhook` | planned |
| GW-012 | MS Graph webhook | Webhook Microsoft Graph | `gateway/platforms/msgraph_webhook.py` | opt-in | MS Graph |  | planned |
| GW-013 | Peer gateways (bot-to-bot DM) | DM antar gateway Hermes lintas mesin | `gateway/hosted_room_peer.py`, `hermes_cli/subcommands/peer.py` | opt-in | Network | `hermes peer` | planned |
| GW-014 | Hosted rooms / group chat | Ruang multi-agent terhost | `gateway/hosted_rooms.py` | opt-in | — | toolset `bot_room` | planned |
| GW-015 | Bot mode / relay | Mode bot DM dan relay antar profil | `tools/bot_relay.py`, `tools/bot_mode_dm.py` | opt-in | — |  | planned |
| GW-016 | Profile routing / multiplex | Satu gateway melayani banyak profil | `gateway/profile_routing.py`, `hermes_cli/gateway_multiplex_mode.py` | on (`gateway.auto_multiplex_migration`) | s6 (opsional) |  | planned |
| GW-017 | Gateway restart/drain | Restart anggun setelah drain run aktif | `gateway/restart.py`, `gateway/drain_control.py` | opt-in | — | `/restart` | planned |
| GW-018 | Platform pause/resume | Pause platform yang gagal | `hermes_cli/commands.py:330` | opt-in | — | `/platform` | planned |
| GW-019 | Topics/threads | Sesi per topik Telegram/thread Discord | `gateway/run_topics.py` | opt-in | — | `/topic` | planned |
| GW-020 | Message timestamps | Prefix timestamp ke pesan | `gateway/message_timestamps.py` | off | — |  | planned |
| GW-021 | Runtime footer | Footer metadata runtime di balasan | `gateway/runtime_footer.py` | off | — | `/footer` | planned |
| GW-022 | Scale-to-zero | Gateway berhenti saat idle | `gateway/scale_to_zero.py` | opt-in | — |  | planned |
| GW-023 | Channel directory | Direktori channel lintas platform | `gateway/channel_directory.py` | on | — |  | planned |
| GW-024 | Gateway chat CLI | Chat ke gateway dari CLI | `hermes_cli/gateway_chat.py` | opt-in | — |  | planned |
| GW-025 | Telegram managed bot | Bot Telegram terkelola | `hermes_cli/telegram_managed_bot.py` | opt-in | Nous [INFERENCE] |  | planned |
| GW-026 | Handoff ke platform | Serahkan sesi CLI ke platform chat | `hermes_cli/commands.py:106` | opt-in | — | `/handoff` | planned |

## 14. CORE — agent loop & runtime (tambahan)
| ID | Fitur | Deskripsi singkat (≤15 kata) | Sumber hermes | Default di hermes | Dependensi eksternal | Catatan | status |
|---|---|---|---|---|---|---|---|
| CORE-001 | Agent loop (tool-calling) | Loop turn: request→tool calls→hasil→final | `run_agent.py`, `agent/conversation_loop.py`, `agent/turn_facade.py` | on | LLM |  | planned |
| CORE-002 | System prompt builder | Rakit system prompt (persona, konteks, tools, skills) | `agent/system_prompt.py`, `agent/prompt_builder.py` | on | — |  | planned |
| CORE-003 | Tool registry & dispatch | Registry tool, coercion arg, eksekusi paralel | `tools/registry.py:462`, `agent/tool_executor.py`, `tools/arg_coercion.py` | on | — |  | planned |
| CORE-004 | Toolsets & per-platform config | Komposisi toolset, aktif/nonaktif per platform | `toolsets.py:74`, `hermes_cli/tools_config.py:110` | on | — | `hermes tools`, `/tools` | planned |
| CORE-005 | Custom toolsets | Buat toolset kustom | `toolsets.py:479` | opt-in | — |  | planned |
| CORE-006 | Coding posture | Toolset coding otomatis di workspace kode | `agent/coding_context.py` | auto | — | toolset `coding` | planned |
| CORE-007 | Interrupt / busy input | Interupsi turn, mode busy (interrupt/queue/steer) | `agent/interrupt_control.py` | on (`busy_input_mode: interrupt`) | — | `/busy` | planned |
| CORE-008 | Iteration budget / max turns | Batas iterasi & budget waktu run | `agent/iteration_budget.py` | on | — |  | planned |
| CORE-009 | Reasoning effort & display | Atur effort reasoning, tampilkan reasoning | `agent/reasoning_effort.py`, `agent/reasoning_params.py` | on (show_reasoning) | Model reasoning | `/reasoning` | planned |
| CORE-010 | Fast mode | Priority processing OpenAI / Anthropic fast | `agent/fast_mode.py` | opt-in | Provider pendukung | `/fast` | planned |
| CORE-011 | Streaming output | Streaming token ke CLI | `agent/stream_delivery.py` | on [INFERENCE] | — |  | planned |
| CORE-012 | Model switch | Ganti model sesi/global | `hermes_cli/model_switch.py` | opt-in | — | `/model`, `hermes model` | planned |
| CORE-013 | Error classifier & retry | Klasifikasi error API, retry, failover | `agent/error_classifier.py`, `agent/retry_utils.py` | on (`api_max_retries: 3`) | — |  | planned |
| CORE-014 | Transcript repair / sanitization | Perbaiki riwayat (tool call yatim, dll.) | `agent/transcript_repair.py`, `agent/message_sanitization.py` | on | — |  | planned |
| CORE-015 | Think scrubber | Buang tag <think> dari output | `agent/think_scrubber.py` | on | — |  | planned |
| CORE-016 | Quick commands | Shortcut perintah dari config | `hermes_cli/config_defaults.py:1704` | opt-in | — |  | planned |
| CORE-017 | Relay runtime (NeMo) | Runtime relay LLM/tools | `agent/relay_runtime.py` | on di platform didukung [INFERENCE] | nemo-relay (core dep) |  | planned |
| CORE-018 | Todo tracking | Daftar tugas in-session (tool todo_list) | `tools/todo_tool.py` | on | — |  | planned |
| CORE-019 | Clarify | Pertanyaan klarifikasi interaktif | `tools/clarify_tool.py`, `tools/clarify_gateway.py` | on | — |  | planned |
| CORE-020 | Working diff `/diff` | Tampilkan perubahan git | `tools/working_diff.py` | opt-in | git |  | planned |
| CORE-021 | `/init` AGENTS.md | Generate/update AGENTS.md dari repo | `hermes_cli/init_command.py` | opt-in | LLM |  | planned |

## Kandidat non-parity (diputuskan)

Keputusan per kandidat: ADR-022 (Accepted 2026-10-17) dan ADR-033 (NP-21). Kolom "Baris parity" menunjukkan baris yang berstatus `dropped-with-approval`; "—" = kandidat tidak punya baris parity sendiri.

| # | Kandidat | Bukti | Baris parity | Keputusan (ADR-022, Accepted) |
|---|---|---|---|---|
| NP-01 | Entrypoint legacy `hermes-agent` | `pyproject.toml:561` (`hermes-agent = "agent.legacy_cli:main"`), `agent/legacy_cli.py:1` ("legacy hermes-agent runner") | — (bukan baris parity) | drop |
| NP-02 | `hermes login` deprecated (redirect ke `hermes auth`/`model`/`setup`) | `hermes_cli/subcommands/login.py:19` ("Deprecated…", #24756) | CLI-019 | drop |
| NP-03 | Extras kosong back-compat: `cron`, `vision`, `pty`, `nemo-relay` | `pyproject.toml:275` ("kept for back-compat"), `:377`, `:382`, `:405` ("no-op back-compat alias") | — (bukan baris) | drop |
| NP-04 | Alias nama tool legacy: `todo`→todo_list, `cronjob`, `process`, `tour`, `tip` | `model_tools.py:611` (`_LEGACY_TOOL_ALIASES`), `tools/todo_tool.py:287` | — (alias di catatan TOOL-079, TOOL-025, TOOL-077; baris tetap planned) | drop alias |
| NP-05 | Alias subcommand: `hermes learning`/`memory-graph` (=journey), `hermes gui` (=desktop) | `hermes_cli/subcommands/journey.py:10`, `hermes_cli/subcommands/gui.py:10` | — (alias di catatan CLI-051, CLI-070; baris tetap planned) | drop alias |
| NP-06 | Alias slash command (25): /new → /reset; /prompt → /compose; /branch → /fork; /compress → /compact; /snapshot → /snap; /agents → /tasks; /journey → /learning, /memory-graph; /queue → /q; /steer → /s; /heartbeat → /hb; /loop → /proactive; /context → /ctx; /sethome → /set-home; /codex-runtime → /codex_runtime; /statusbar → /sb; /timestamps → /ts; /hatch → /generate-pet; /suggestions → /suggest; /blueprint → /bp; /reload-mcp → /reload_mcp; /reload-skills → /reload_skills; /subscription → /upgrade; /platforms → /gateway; /version → /v; /quit → /exit | `hermes_cli/commands.py` (field `aliases`) | — (alias di catatan SLASH-*) | keep |
| NP-07 | Alias toolset `yuanbao` vs `hermes-yuanbao` (registry memakai `hermes-yuanbao`) | `toolsets.py:169`, `toolsets.py:225` | TSET-037 | drop |
| NP-08 | `rl` di `_AGENT_COMMANDS` tanpa subparser `rl` terdaftar | `hermes_cli/main.py:2852` (tidak ada di 72 add_parser top-level) | — (bukan baris) | drop |
| NP-09 | `spotify` di `_DEFAULT_OFF_TOOLSETS` padahal fitur sudah keluar dari core (plugin katalog) | `hermes_cli/tools_config.py:110`, `hermes_cli/left_core_migration.py:194` | — (bukan baris) | drop |
| NP-10 | Fitur "left core" → plugin katalog: Home Assistant, Spotify, Langfuse (hanya migrasi tersisa) | `hermes_cli/left_core_migration.py:186` | — (bukan baris; via MCP/OTLP) | keep via ekstensi |
| NP-11 | Updater lama / escape hatch: `_old_updater`, `old_updater_main`, `old_updater_deps` | `hermes_cli/_old_updater.py:1`, `hermes_cli/old_updater_main.py:1` | — (OPS-003 tetap planned) | drop |
| NP-12 | Launcher gateway Windows pra-suffix (legacy cleanup) | `hermes_cli/gateway_windows_legacy.py:1` (#116157) | — (bukan baris) | drop |
| NP-13 | Impor satu-kali hosted rooms legacy | `gateway/hosted_rooms_legacy_import.py:1` (#109775) | — (bukan baris) | drop |
| NP-14 | Bot chat pending dari lane CLI yang sudah dipensiunkan | `cron/bot_chat_legacy.py:1` | — (bukan baris) | drop |
| NP-15 | Migrasi satu-kali: xAI retirement, session_reset retirement, relay cutover, codex runtime plugin migration, gateway migrate multiplex, memory provider migration, setup migration | `hermes_cli/xai_retirement.py:1`, `hermes_cli/session_reset_retirement.py:1`, `hermes_cli/relay_plugin_cutover.py:1`, `hermes_cli/codex_runtime_plugin_migration.py`, `hermes_cli/gateway_migrate.py:1`, `hermes_cli/memory_provider_migration.py`, `hermes_cli/setup_migration.py` | CLI-009, OPS-036 | drop (diganti `damnbot import hermes`) |
| NP-16 | Provider `solstice` hidden (pra-rilis) | `plugins/model-providers/solstice/__init__.py` (`hidden=True`) | PROV-043 | drop |
| NP-17 | `kanban_schedule` terdaftar tetapi tidak ada di `_HERMES_CORE_TOOLS` | `tools/kanban_tools.py:852`, `toolsets.py:29-35` | TOOL-067 | keep |
| NP-18 | Internal/dev-only: `evals/`, `scripts/`, `hermes_constants_scratch.py`, `mini_swe_runner.py`, `toolset_distributions.py`, `tests*` | path terkait; tidak diekspos sebagai fitur pengguna | OPS-020, OPS-023, OPS-024 tetap planned | keep sebagian |
| NP-19 | Spesifik ekosistem Nous (Portal, free tier, anon sign-in, Nous billing, telemetry Nous, Tool Gateway) | `hermes_cli/portal_cli.py`, `hermes_cli/anon_sign_in.py`, `hermes_cli/nous_billing.py`, `tools/managed_tool_gateway.py` | CLI-028, SLASH-091, SLASH-092, SLASH-093, PROV-073, PROV-080, OPS-014, OPS-019, AUTO-010, PLUG-015 | drop |
| NP-20 | Flag `hermes --version` vs `/version` alias `/v` | `hermes_cli/main.py:2217`, `hermes_cli/commands.py:340` | — (alias) | keep |
| NP-21 | Memory provider ByteRover & RetainDB (dijadwalkan dihapus upstream 15 Okt 2026) | `plugins/AGENTS.md:19-20` | MEM-006, MEM-007 (PLUG-022 tetap planned untuk Honcho) | drop (ADR-033 Q31) |

## Hal yang tidak bisa ditentukan / batasan inventaris

- Kolom `available` toolset mencerminkan lingkungan inventaris (tanpa kredensial/ binary), bukan default produksi.
- Tool MCP dinamis, tool dari plugin katalog, dan `/<skill>` dinamis tidak dihitung sebagai baris tersendiri.
- 645 entri `plugin-catalog/` (pihak ketiga, termasuk 36 kategori `platform` dan 65 `memory`) hanya diringkas per kategori; bukan fitur core.
- Sub-subcommand (425 entri parser total) hanya dicantumkan namanya di kolom Catatan baris CLI induk.
- Fitur granular halaman web dashboard (`web/src`) dan desktop Electron (`apps/desktop/src`) tidak dienumerasi per layar.
- Lokasi implementasi provider memori Honcho (hanya config/profil di core) belum dipastikan.
- Baris bertanda `[INFERENCE]` perlu verifikasi perilaku sebelum dijadikan kriteria lulus parity.
- Default beberapa fitur bergantung kombinasi platform/posture (mis. toolset `coding` otomatis di workspace kode) sehingga "on/off" disederhanakan.
