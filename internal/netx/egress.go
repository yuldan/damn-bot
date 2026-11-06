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
		"media": {
			Purpose: Tool,
			Host:    "image_endpoint or video_create_url from [modules.media] (loopback endpoints use the local lane)",
			Trigger: "image_generate or video_generate after approval and the tool cap check",
		},
		"voice": {
			Purpose: Tool,
			Host:    "tts_endpoint from [modules.voice] (loopback endpoints use the local lane)",
			Trigger: "speak after approval; transcribe runs the local whisper binary only",
		},
		"voice-live": {
			Purpose: Inference,
			Host:    "live_endpoint from [modules.voice] when chat_mode = gpt_live",
			Trigger: "one gpt_live realtime answer after push-to-talk",
		},
		"browser": {
			Purpose: Tool,
			Host:    "cloud_url from [modules.browser] (Camoufox and loopback cloud gateways use the local lane)",
			Trigger: "browser_navigate and the other page tools after approval",
		},
		"doctor-tools-fetch": {
			Purpose: Tool,
			Host:    "pinned https release URL from the embedded tools lock (public addresses only)",
			Trigger: "damnbot doctor tools fetch <name> after the license confirmation",
		},
		"telegram": {
			Purpose: Gateway,
			Host:    "api.telegram.org",
			Trigger: "gateway long polling and message delivery for a user turn",
		},
		"peer-dm": {
			Purpose: Gateway,
			Host:    "peer url from the local peer book (<data-dir>/peer/peers.json)",
			Trigger: "damnbot peer dm to a registered peer gateway (GW-013)",
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
	case Exec:
		return "exec"
	default:
		return "unknown"
	}
}
