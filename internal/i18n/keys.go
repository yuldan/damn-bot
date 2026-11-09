package i18n

// Catalog keys used by the CLI surface (SPEC §9.12, §10). A key is part of the
// embedded contract: TestCLIUsedKeysExist fails when a wired key is absent
// from any locale.
const (
	// KeyHelpUsage is the root usage line: "usage: damnbot <command> [flags]".
	KeyHelpUsage = "help.usage"
	// KeyHelpCommands is the command-table heading.
	KeyHelpCommands = "help.commands"
	// KeyHelpGlobalFlags is the global-flags heading.
	KeyHelpGlobalFlags = "help.global_flags"
	// KeyHelpProfile describes the global --profile flag.
	KeyHelpProfile = "help.profile"
	// KeyHelpHelp describes the help flag.
	KeyHelpHelp = "help.help"
	// KeyHelpVersion describes the --version alias.
	KeyHelpVersion = "help.version"
	// KeyHelpDetails is the pointer to `damnbot help <command>`.
	KeyHelpDetails = "help.details"
	// KeyHelpUnknownTopic reports an unknown help topic (%q).
	KeyHelpUnknownTopic = "help.unknown_topic"
	// KeyHelpUnknownHint points at the root help after an unknown topic.
	KeyHelpUnknownHint = "help.unknown_topic_hint"
	// KeyHelpTopicHeader is the per-topic header (name, summary).
	KeyHelpTopicHeader = "help.topic_header"
	// KeyConfigLoad prefixes a config load failure.
	KeyConfigLoad = "config.load"
	// KeyAPIAuthRequired rejects an unauthenticated API server start.
	KeyAPIAuthRequired = "api.token_required"
	// KeyWebAuthUnauthorized is the seam for the F8 web UI bearer check; the
	// web package is not in this tree yet, so only the catalog entry exists.
	KeyWebAuthUnauthorized = "web.auth_unauthorized"
)
