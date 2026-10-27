package config

import (
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

// TestConfigAllSpec12Keys sets every accepted SPEC §12 key so a struct field
// that loses its tag is caught here, not by a user.
func TestConfigAllSpec12Keys(t *testing.T) { //nolint:gocyclo // one assertion per accepted key
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[provider]", `model = "openrouter/anthropic/claude-sonnet-4.5"`, `fallback = ["a/b"]`,
		`light = "a/l"`, "max_output = 4096",
		"[loop]", "max_iterations = 12", "max_wall_s = 300", "max_identical = 4",
		"turn_max_input_tok = 100000", "turn_max_output_tok = 8000", "parallel_tools = 2",
		"[budget]", "turn_usd = 1.25", "session_usd = 10", "daily_usd = 2", "monthly_usd = 20",
		"warn_at = 0.5", "providers = { openrouter = 5.0 }",
		"tools_session_usd = 2.5", "tools_daily_usd = 3.5",
		"[approval]", `mode = "auto-edit"`, `shell_allow = [["go", "test"]]`,
		`deny_paths = ["~/.ssh"]`, `add_dirs = ["/extra"]`, "preview_timeout_s = 30",
		"[exec]", "default_timeout_s = 30", "idle_timeout_s = 10", "allow_destructive = true",
		"kill_grace_s = 1", `env_allow = ["PATH"]`, `env = { FOO = "bar" }`, "pass_ssh_agent = true",
		"[agent]", `verify_on_stop = "repair"`,
		"[context]", "compact_at = 0.5", "summary_max_tok = 700", "summaries_max_tok = 4000",
		"pinned_schema_tok = 1000",
		"[memory]", "entry_max_chars = 300", "total_max_chars = 2000", `embeddings = "openai/text-embedding-3-small"`,
		"[skills]", "project_discovery = false", "trusted_projects = [" + tomlAbsPath(t) + "]",
		"external_dirs = [" + tomlAbsPath(t) + "]", "inline_shell = true",
		"[recovery]", `continue = "ask"`,
		"[ui]", `language = "id"`, `theme = "mono"`,
		"[log]", `level = "debug"`, "message_content = true", `message_content_ttl = "48h"`,
		"[sessions]", "auto_prune = true", "prune_after_days = 30",
		"[storage]", "encrypt = true",
		"[toolsets]", `active = ["coding"]`,
		"[update]", `feed_url = "https://example.test/releases"`, `channel = "beta"`, "auto_install = true",
		"[gateway]", "max_parallel_turns = 8",
		"[gateway.users.\"telegram:123\"]", `workspace = "/srv/ws"`,
		"[[providers.custom]]", `name = "kantor"`, `base_url = "http://10.0.0.5:8000/v1"`,
		`api = "openai-chat"`, `api_key_env = "KANTOR_KEY"`,
		`models = [{ id = "qwen3-coder", context_length = 131072, capabilities = ["tools"] }]`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Provider.MaxOutput != 4096 || cfg.Loop.ParallelTools != 2 ||
		cfg.Budget.ToolsSessionUSD != 2.5 || cfg.Budget.ToolsDailyUSD != 3.5 {
		t.Errorf("provider/loop/budget = %+v", cfg)
	}
	if cfg.Approval.Mode != "auto-edit" || cfg.Exec.PassSSHAgent != true {
		t.Errorf("approval/exec = %+v", cfg)
	}
	if cfg.Context.CompactAt != 0.5 || cfg.Context.SummariesMaxTok != 4000 ||
		cfg.Context.PinnedSchemaTok != 1000 {
		t.Errorf("context = %+v", cfg.Context)
	}
	if cfg.Memory.EntryMaxChars != 300 || cfg.Memory.Embeddings != "openai/text-embedding-3-small" {
		t.Errorf("memory = %+v", cfg.Memory)
	}
	if cfg.Skills.InlineShell != true || cfg.Skills.ProjectDiscovery != false {
		t.Errorf("skills = %+v", cfg.Skills)
	}
	if cfg.Recovery.Continue != "ask" || cfg.UI.Language != "id" || cfg.UI.Theme != "mono" {
		t.Errorf("recovery/ui = %+v/%+v", cfg.Recovery, cfg.UI)
	}
	if !cfg.Sessions.AutoPrune || cfg.Sessions.PruneAfterDays != 30 || !cfg.Storage.Encrypt {
		t.Errorf("sessions/storage = %+v/%+v", cfg.Sessions, cfg.Storage)
	}
	if cfg.Gateway.MaxParallelTurns != 8 || cfg.Gateway.Users["telegram:123"].Workspace != "/srv/ws" {
		t.Errorf("gateway = %+v", cfg.Gateway)
	}
	if len(cfg.Providers.Custom) != 1 || cfg.Providers.Custom[0].Name != "kantor" ||
		cfg.Providers.Custom[0].Models[0].ContextLength != 131072 {
		t.Errorf("providers = %+v", cfg.Providers)
	}
}

// TestConfigSpec12Example parses the literal SPEC §12 core block and checks
// that its values are the defaults.
func TestConfigSpec12Example(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"# <config_dir>/config.toml — nilai di bawah adalah default",
		"[provider]", `model = ""`, "fallback = []", `light = ""`, "max_output = 8192",
		"[loop]", "max_iterations = 40", "max_wall_s = 900", "max_identical = 3",
		"turn_max_input_tok = 400000", "turn_max_output_tok = 32000", "parallel_tools = 4",
		"[budget]", "turn_usd = 0.50", "session_usd = 5.00", "daily_usd = 0", "monthly_usd = 0",
		"warn_at = 0.80", "providers = {}",
		"[approval]", `mode = "ask"`, "shell_allow = []",
		`deny_paths = ["~/.ssh", "~/.gnupg", "~/.aws", "**/.env", "**/*.pem", "~/.netrc", "~/.git-credentials", "~/.config/gh", "~/.docker/config.json", "~/.kube", "<config_dir>/secrets.enc", "<config_dir>/at-rest.*", "<config_dir>/backup.keys", "/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem"]`,
		"add_dirs = []", "preview_timeout_s = 600",
		"[exec]", "default_timeout_s = 120", "idle_timeout_s = 60", "allow_destructive = false",
		"kill_grace_s = 3",
		"[agent]", `verify_on_stop = "run"`,
		"[context]", "compact_at = 0.75", "summary_max_tok = 600", "summaries_max_tok = 3000",
		"pinned_schema_tok = 3000",
		"[memory]", "entry_max_chars = 500", "total_max_chars = 4000", `embeddings = ""`,
		"[skills]", "project_discovery = true", "trusted_projects = []", "external_dirs = []",
		"[recovery]", `continue = "safe_auto"`,
		"[ui]", `language = "auto"`, `theme = "tern-dark"`,
		"[log]", `level = "info"`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := Default()
	if diff := cmp.Diff(want, cfg); diff != "" {
		t.Errorf("SPEC §12 example differs from Default() (-want +got):\n%s", diff)
	}
}

// TestConfigModuleSectionsUnavailable: compiled-out module sections get their
// own sentinel, never the generic unknown-key message.
func TestConfigModuleSectionsUnavailable(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, toml, prefix string }{
		{"mcp", "[modules.mcp.servers.github]\ncommand = [\"github-mcp-server\", \"stdio\"]\n", "modules.mcp.servers.github"},
		{"gateway", "[modules.gateway.telegram]\nenabled = true\n", "modules.gateway.telegram"},
		{"obs", "[obs]\nlisten = \"127.0.0.1:7879\"\n", "obs"},
		{"cron", "[cron]\ndefault_model = \"p/m\"\n", "cron"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			ke := keyErrorOf(t, err, ErrModuleUnavailable)
			if errors.Is(err, ErrUnknownKey) {
				t.Errorf("module error must not match ErrUnknownKey: %v", err)
			}
			if !strings.HasPrefix(ke.Key, tc.prefix) {
				t.Errorf("key = %q, want prefix %q", ke.Key, tc.prefix)
			}
			if ke.Line == 0 {
				t.Errorf("line = 0")
			}
			if !strings.Contains(ke.Msg, "not compiled") {
				t.Errorf("msg = %q", ke.Msg)
			}
		})
	}
}

func TestConfigContextRanges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, toml string }{
		{"defaults", ""},
		{"low", "[context]\ncompact_at = 0.001\n"},
		{"high", "[context]\ncompact_at = 0.999\n"},
		{"summary_one", "[context]\nsummary_max_tok = 1\n"},
		{"equal", "[context]\nsummaries_max_tok = 600\n"},
		{"pinned_zero", "[context]\npinned_schema_tok = 0\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(t, tc.toml); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, toml, key string }{
		{"compact_zero", "[context]\ncompact_at = 0\n", "context.compact_at"},
		{"compact_one", "[context]\ncompact_at = 1\n", "context.compact_at"},
		{"compact_negative", "[context]\ncompact_at = -0.1\n", "context.compact_at"},
		{"compact_high", "[context]\ncompact_at = 1.1\n", "context.compact_at"},
		{"summary_zero", "[context]\nsummary_max_tok = 0\n", "context.summary_max_tok"},
		{"summaries_low", "[context]\nsummaries_max_tok = 599\n", "context.summaries_max_tok"},
		{"pinned_negative", "[context]\npinned_schema_tok = -1\n", "context.pinned_schema_tok"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			ke := keyErrorOf(t, err, ErrInvalidValue)
			if ke.Key != tc.key {
				t.Errorf("key = %q, want %q", ke.Key, tc.key)
			}
		})
	}
}

func TestConfigMemoryRanges(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, toml string }{
		{"defaults", ""},
		{"equal", "[memory]\nentry_max_chars = 4000\ntotal_max_chars = 4000\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parse(t, tc.toml); err != nil {
				t.Fatalf("Parse: %v", err)
			}
		})
	}
	for _, tc := range []struct{ name, toml, key string }{
		{"entry_zero", "[memory]\nentry_max_chars = 0\n", "memory.entry_max_chars"},
		{"entry_above_total", "[memory]\nentry_max_chars = 4001\n", "memory.entry_max_chars"},
		{"total_zero", "[memory]\ntotal_max_chars = 0\n", "memory.total_max_chars"},
		{"embeddings", "[memory]\nembeddings = \"bukan-ref\"\n", "memory.embeddings"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			ke := keyErrorOf(t, err, ErrInvalidValue)
			if ke.Key != tc.key {
				t.Errorf("key = %q, want %q", ke.Key, tc.key)
			}
		})
	}
}

func TestConfigRecoveryValues(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"off", "ask", "safe_auto"} {
		if _, err := parse(t, "[recovery]\ncontinue = \""+value+"\"\n"); err != nil {
			t.Errorf("%s: %v", value, err)
		}
	}
	for _, value := range []string{"auto", ""} {
		_, err := parse(t, "[recovery]\ncontinue = \""+value+"\"\n")
		ke := keyErrorOf(t, err, ErrInvalidValue)
		if ke.Key != "recovery.continue" {
			t.Errorf("%q: key = %q", value, ke.Key)
		}
	}
}

func TestConfigUILanguageTheme(t *testing.T) {
	t.Parallel()
	for _, code := range []string{
		"auto", "af", "ar", "de", "en", "es", "fr", "ga", "hu", "it",
		"ja", "ko", "pt", "ru", "tr", "uk", "zh", "zh-hant", "id",
	} {
		if _, err := parse(t, "[ui]\nlanguage = \""+code+"\"\n"); err != nil {
			t.Errorf("language %q: %v", code, err)
		}
	}
	for _, theme := range []string{"tern-dark", "tern-light", "mono", "high-contrast"} {
		if _, err := parse(t, "[ui]\ntheme = \""+theme+"\"\n"); err != nil {
			t.Errorf("theme %q: %v", theme, err)
		}
	}
	if _, err := parse(t, "[ui]\nlanguage = \"xx\"\n"); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("language xx = %v", err)
	}
	if _, err := parse(t, "[ui]\ntheme = \"solarized\"\n"); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("theme solarized = %v", err)
	}
}

func TestConfigGateway(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil || cfg.Gateway.MaxParallelTurns != 4 || len(cfg.Gateway.Users) != 0 {
		t.Fatalf("defaults = %+v (%v)", cfg.Gateway, err)
	}
	for _, n := range []string{"1", "16"} {
		if _, err := parse(t, "[gateway]\nmax_parallel_turns = "+n+"\n"); err != nil {
			t.Errorf("max_parallel_turns %s: %v", n, err)
		}
	}
	for _, n := range []string{"0", "17"} {
		if _, err := parse(t, "[gateway]\nmax_parallel_turns = "+n+"\n"); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("max_parallel_turns %s = %v", n, err)
		}
	}
	for _, id := range []string{"telegram:123", "irc_budi", "a", strings.Repeat("a", 128)} {
		if _, err := parse(t, "[gateway.users.\""+id+"\"]\nworkspace = \"\"\n"); err != nil {
			t.Errorf("id %q: %v", id, err)
		}
	}
	for _, id := range []string{"", "has space", strings.Repeat("a", 129)} {
		if _, err := parse(t, "[gateway.users.\""+id+"\"]\nworkspace = \"\"\n"); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("id %q = %v", id, err)
		}
	}
	for _, ws := range []string{`""`, `"/srv/ws"`, `'C:\ws'`, "'~/ws'", `'~\ws'`} {
		toml := "[gateway.users.\"irc\"]\nworkspace = " + ws + "\n"
		if _, err := parse(t, toml); err != nil {
			t.Errorf("workspace %s: %v", ws, err)
		}
	}
	if _, err := parse(t, "[gateway.users.\"irc\"]\nworkspace = \"rel/ws\"\n"); !errors.Is(err, ErrInvalidValue) {
		t.Errorf("relative workspace = %v", err)
	}
}

func TestConfigCustomProviders(t *testing.T) { //nolint:gocyclo // one assertion per decoded field
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[[providers.custom]]",
		`name = "kantor"`,
		`base_url = "http://10.0.0.5:8000/v1"`,
		`api = "openai-responses"`,
		`api_key = "keychain:kantor"`,
		`session_affinity_header = "X-Session"`,
		`extra_headers = { "X-Tenant" = "a" }`,
		`extra_body = { temperature = 0.2, nested = { k = "v" }, list = [1, 2] }`,
		`streaming = false`,
		`tls = { ssl_verify = false, ssl_ca_cert = "/etc/ca.pem" }`,
		`models = [{ id = "qwen3-coder", context_length = 131072, max_tokens = 8192, capabilities = ["tools", "vision"] }]`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := cfg.Providers.Custom[0]
	if p.Name != "kantor" || p.BaseURL != "http://10.0.0.5:8000/v1" || p.API != "openai-responses" {
		t.Fatalf("provider = %+v", p)
	}
	if p.Streaming == nil || *p.Streaming || p.TLS == nil || p.TLS.SSLVerify == nil || *p.TLS.SSLVerify {
		t.Fatalf("pointers not decoded: %+v", p)
	}
	if p.ExtraHeaders["X-Tenant"] != "a" || p.ExtraBody["temperature"] != 0.2 {
		t.Fatalf("extra = %+v/%+v", p.ExtraHeaders, p.ExtraBody)
	}
	if p.Models[0].Capabilities[1] != "vision" || p.Models[0].MaxTokens != 8192 {
		t.Fatalf("model = %+v", p.Models[0])
	}

	minimal, err := parse(t, "[[providers.custom]]\nname = \"local\"\nbase_url = \"https://local.example/v1\"\nmodels = [{ id = \"m\" }]\n")
	if err != nil {
		t.Fatalf("minimal: %v", err)
	}
	if got := minimal.Providers.Custom[0].API; got != "openai-chat" {
		t.Errorf("empty api normalized to %q", got)
	}
	if minimal.Providers.Custom[0].Streaming != nil || minimal.Providers.Custom[0].TLS != nil {
		t.Errorf("unset pointers must stay nil: %+v", minimal.Providers.Custom[0])
	}

	keyCmd, err := parse(t, "[[providers.custom]]\nname = \"cmd\"\nbase_url = \"https://cmd.example/v1\"\nkey_cmd = [\"pass\", \"show\", \"cmd\"]\nmodels = [{ id = \"m\" }]\n")
	if err != nil {
		t.Fatalf("key_cmd: %v", err)
	}
	if len(keyCmd.Providers.Custom[0].KeyCmd) != 3 {
		t.Errorf("key_cmd = %v", keyCmd.Providers.Custom[0].KeyCmd)
	}
}

func TestConfigCustomProviderRejects(t *testing.T) {
	t.Parallel()
	const valid = `name = "k"
base_url = "https://k.example/v1"
models = [{ id = "m" }]
`
	tests := []struct{ name, toml string }{
		{"missing_base_url", "[[providers.custom]]\nname = \"k\"\nmodels = [{ id = \"m\" }]\n"},
		{"missing_models", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\n"},
		{"duplicate_name", "[[providers.custom]]\n" + valid + "[[providers.custom]]\n" + valid},
		{"name_case", "[[providers.custom]]\nname = \"K\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ id = \"m\" }]\n"},
		{"base_url_relative", "[[providers.custom]]\nname = \"k\"\nbase_url = \"k.example/v1\"\nmodels = [{ id = \"m\" }]\n"},
		{"base_url_userinfo", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://u:p@k.example/v1\"\nmodels = [{ id = \"m\" }]\n"},
		{"api", "[[providers.custom]]\n" + valid + "api = \"grpc\"\n"},
		{"two_key_sources", "[[providers.custom]]\n" + valid + "api_key_env = \"K_KEY\"\napi_key = \"keychain:k\"\n"},
		{"plaintext_api_key", "[[providers.custom]]\n" + valid + "api_key = \"sk-123\"\n"},
		{"api_key_env_case", "[[providers.custom]]\n" + valid + "api_key_env = \"k_key\"\n"},
		{"header_crlf", "[[providers.custom]]\n" + valid + "extra_headers = { \"X-Bad\" = \"a\\nb\" }\n"},
		{"header_name", "[[providers.custom]]\n" + valid + "extra_headers = { \"Bad Header\" = \"a\" }\n"},
		{"capability", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ id = \"m\", capabilities = [\"video\"] }]\n"},
		{"model_id", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ context_length = 1 }]\n"},
		{"duplicate_model", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ id = \"m\" }, { id = \"m\" }]\n"},
		{"negative_context", "[[providers.custom]]\nname = \"k\"\nbase_url = \"https://k.example/v1\"\nmodels = [{ id = \"m\", context_length = -1 }]\n"},
		{"empty_tls", "[[providers.custom]]\n" + valid + "tls = {}\n"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			if !errors.Is(err, ErrInvalidValue) {
				t.Errorf("err = %v, want ErrInvalidValue", err)
			}
		})
	}

	_, err := parse(t, "[[providers.custom]]\n"+valid+"foo = 1\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if ke.Key != "providers.custom.foo" || ke.Line != 5 {
		t.Errorf("unknown field key/line = %q/%d", ke.Key, ke.Line)
	}
}

func TestConfigBudgetToolCaps(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// SPEC §12 defers these to GAP-COST-11: 1,00 / 5,00.
	if cfg.Budget.ToolsSessionUSD != 1.00 || cfg.Budget.ToolsDailyUSD != 5.00 {
		t.Errorf("tools caps = %v/%v", cfg.Budget.ToolsSessionUSD, cfg.Budget.ToolsDailyUSD)
	}
	cfg, err = parse(t, "[budget]\ntools_session_usd = 2.5\ntools_daily_usd = 0\n")
	if err != nil || cfg.Budget.ToolsSessionUSD != 2.5 || cfg.Budget.ToolsDailyUSD != 0 {
		t.Fatalf("override = %v (%v)", cfg.Budget, err)
	}
	for _, value := range []string{"-1", "nan", "inf"} {
		if _, err := parse(t, "[budget]\ntools_session_usd = "+value+"\n"); !errors.Is(err, ErrInvalidValue) {
			t.Errorf("tools_session_usd %s = %v", value, err)
		}
	}
}
