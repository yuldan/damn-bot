package netx

// Egress is one statically registered outbound endpoint (GAP-OPS-02). The
// registry is descriptive: the policy itself is enforced per [Purpose] in
// [Network.Client], and `damnbot doctor --network` renders this list with the
// status the current [Config] gives each entry.
type Egress struct {
	Purpose Purpose
	Host    string // the host (or the config key that supplies it)
	Trigger string // the action that causes the request
}

// Registry returns the static egress registry keyed by the client name used
// at the call site. A new call site must add its entry here; the package test
// fails when a production client is missing from (or stale in) this table.
func Registry() map[string]Egress {
	reg := map[string]Egress{
		"inference": {
			Purpose: Inference,
			Host:    "provider base URL from [provider].model, [[providers.custom]].base_url, or --base-url",
			Trigger: "every turn request to the configured LLM",
		},
		"refs": {
			Purpose: Tool,
			Host:    "host of the referenced URL",
			Trigger: "an @url reference in the user prompt",
		},
		"update": {
			Purpose: Background,
			Host:    "[update].feed_url (default https://api.github.com/repos/yuldan/damn-bot/releases)",
			Trigger: "damnbot update; the daily check in serve (F4)",
		},
		"cron-monitor": {
			Purpose: Tool,
			Host:    "monitor.url in cron_jobs.gate",
			Trigger: "a due cron monitor gate before the prompt runs (COST-13)",
		},
		"mcp": {
			Purpose: Tool,
			Host:    "server url from [modules.mcp.servers.<name>].url (public addresses only)",
			Trigger: "damnbot mcp refresh/review and a turn's MCP list/call",
		},
		"mcp-local": {
			Purpose: Inference,
			Host:    "loopback server url from [modules.mcp.servers.<name>].url",
			Trigger: "the same calls to an explicitly local MCP server",
		},
		"telegram": {
			Purpose: Gateway,
			Host:    "api.telegram.org",
			Trigger: "gateway long polling and message delivery for a user turn",
		},
	}
	addAuthEgress(reg)
	return reg
}

// String names the purpose in diagnostics.
func (p Purpose) String() string {
	switch p {
	case Inference:
		return "inference"
	case Tool:
		return "tool"
	case Background:
		return "background"
	case Gateway:
		return "gateway"
	default:
		return "unknown"
	}
}
