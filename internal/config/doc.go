// Package config loads, validates, and freezes the Damn-Bot TOML config
// (SPEC §4.9 and §12; ADR-105).
//
// The package is immutable after [Loader.Load]: [Config] is returned by value,
// every method has a value receiver, and callers that need to mutate a copy use
// [Config.Clone]. [Config.Validate] is the same check [Loader.Load] applies, so
// a hand-built config can be checked with the same error surface.
//
// F1 exposes only the SPEC §12 keys that core F1 packages read:
//
//	[provider]  model, fallback, light, max_output
//	[loop]      max_iterations, max_wall_s, max_identical,
//	            turn_max_input_tok, turn_max_output_tok, parallel_tools
//	[budget]    turn_usd, session_usd, daily_usd, monthly_usd, warn_at, providers
//	[approval]  mode, shell_allow, deny_paths, add_dirs, preview_timeout_s
//	[exec]      default_timeout_s, idle_timeout_s, allow_destructive,
//	            kill_grace_s, env_allow, env, pass_ssh_agent
//	[agent]     verify_on_stop
//	[log]       level, message_content, message_content_ttl
//	[toolsets]  active, disabled, platform.<os>.{active,disabled},
//	            custom.<name>.{description,tools,includes}
//
// Sections owned by later phases ([context], [memory], [recovery], [ui],
// gateway/cron/module sections, custom providers) are deliberately absent:
// unknown keys are rejected, so adding a section is an additive change that
// arrives with the phase that consumes it.
//
// Unknown keys are rejected with the full key path and, best effort, the TOML
// line of the first offending key. Values outside their SPEC range are rejected
// with the key path. Neither path panics: every error is a [*Error] that wraps
// a sentinel such as [ErrUnknownKey] or [ErrInvalidValue].
package config
