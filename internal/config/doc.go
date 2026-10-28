// Package config loads, validates, and freezes the Damn-Bot TOML config
// (SPEC §4.9 and §12; ADR-105, ADR-197).
//
// The package is immutable after [Loader.Load]: [Config] is returned by value,
// every method has a value receiver, and callers that need to mutate a copy use
// [Config.Clone]. [Config.Validate] is the same check [Loader.Load] applies, so
// a hand-built config can be checked with the same error surface.
//
// The complete SPEC §12 core section is accepted:
//
//	[provider]  model, fallback, light, max_output
//	[loop]      max_iterations, max_wall_s, max_identical,
//	            turn_max_input_tok, turn_max_output_tok, parallel_tools
//	[budget]    turn_usd, session_usd, daily_usd, monthly_usd, warn_at,
//	            providers, tools_session_usd, tools_daily_usd
//	[approval]  mode, shell_allow, deny_paths, add_dirs, preview_timeout_s
//	[exec]      default_timeout_s, idle_timeout_s, allow_destructive,
//	            kill_grace_s, env_allow, env, pass_ssh_agent
//	[agent]     verify_on_stop
//	[context]   compact_at, summary_max_tok, summaries_max_tok, pinned_schema_tok
//	[memory]    entry_max_chars, total_max_chars, embeddings
//	[skills]    project_discovery, trusted_projects, external_dirs, inline_shell
//	[recovery]  continue
//	[ui]        language, theme
//	[log]       level, message_content, message_content_ttl
//	[sessions]  auto_prune, prune_after_days
//	[storage]   encrypt
//	[network]   offline, background (DAMNBOT_NETWORK_BACKGROUND override)
//	[toolsets]  active, disabled, platform.<os>.{active,disabled},
//	            custom.<name>.{description,tools,includes}
//
// [[providers.custom]] and [gateway] are accepted and validated but are
// data-only in F3: no package consumes them until F5 and F4 respectively.
//
// Sections owned by compiled-out modules ([modules], [obs], [cron]) are
// rejected with [ErrModuleUnavailable]; every other unknown key is
// [ErrUnknownKey]. Values outside their SPEC range are [ErrInvalidValue].
// Neither path panics: every error is a [*Error] that wraps a sentinel.
package config
