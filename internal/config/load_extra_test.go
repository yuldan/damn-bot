package config

import (
	"errors"
	"strings"
	"testing"
)

// TestConfigAllF1Keys sets every exposed key and checks the decode.
func TestConfigAllF1Keys(t *testing.T) { //nolint:gocyclo // one assertion per exposed F1 key
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[provider]",
		`model = "openrouter/anthropic/claude-sonnet-4.5"`,
		`fallback = ["openai/gpt-4.1", "local/qwen"]`,
		`light = "openai/gpt-4.1-mini"`,
		"max_output = 4096",
		"[loop]",
		"max_iterations = 12", "max_wall_s = 300", "max_identical = 4",
		"turn_max_input_tok = 100000", "turn_max_output_tok = 8000", "parallel_tools = 2",
		"[budget]",
		"turn_usd = 1.25", "session_usd = 10", "daily_usd = 2", "monthly_usd = 20",
		"warn_at = 0.5", "providers = { openrouter = 5.0 }",
		"[approval]",
		`mode = "auto-edit"`, `shell_allow = [["go", "test", "./..."]]`,
		`deny_paths = ["~/.ssh", "**/.env"]`, `add_dirs = ["/work/extra"]`, "preview_timeout_s = 30",
		"[exec]",
		"default_timeout_s = 30", "idle_timeout_s = 10", "allow_destructive = true",
		"kill_grace_s = 1", `env_allow = ["PATH", "GOPATH"]`, `env = { FOO = "bar" }`,
		"pass_ssh_agent = true",
		"[agent]",
		`verify_on_stop = "repair"`,
		"[log]",
		`level = "debug"`, "message_content = true", `message_content_ttl = "48h"`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Provider.Model != "openrouter/anthropic/claude-sonnet-4.5" || len(cfg.Provider.Fallback) != 2 || cfg.Provider.MaxOutput != 4096 {
		t.Errorf("provider = %+v", cfg.Provider)
	}
	if cfg.Loop.MaxIterations != 12 || cfg.Loop.ParallelTools != 2 || cfg.Loop.MaxWallS != 300 {
		t.Errorf("loop = %+v", cfg.Loop)
	}
	if cfg.Budget.TurnUSD != 1.25 || cfg.Budget.WarnAt != 0.5 || cfg.Budget.Providers["openrouter"] != 5.0 {
		t.Errorf("budget = %+v", cfg.Budget)
	}
	if cfg.Approval.Mode != "auto-edit" || len(cfg.Approval.ShellAllow) != 1 || len(cfg.Approval.DenyPaths) != 2 || cfg.Approval.PreviewTimeoutS != 30 {
		t.Errorf("approval = %+v", cfg.Approval)
	}
	if cfg.Exec.DefaultTimeoutS != 30 || cfg.Exec.KillGraceS != 1 || cfg.Exec.Env["FOO"] != "bar" || !cfg.Exec.PassSSHAgent {
		t.Errorf("exec = %+v", cfg.Exec)
	}
	if cfg.Agent.VerifyOnStop != "repair" || cfg.Log.Level != "debug" || !cfg.Log.MessageContent {
		t.Errorf("agent/log = %+v/%+v", cfg.Agent, cfg.Log)
	}
	if cfg.Log.MessageContentTTL.String() != "48h0m0s" {
		t.Errorf("ttl = %s", cfg.Log.MessageContentTTL)
	}
}

// TestConfigUnknownQuotedKey keeps the dotted path and line for quoted keys.
func TestConfigUnknownQuotedKey(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[provider]\n\"modl\" = \"x\"\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if ke.Key != "provider.modl" || ke.Line != 2 {
		t.Fatalf("key/line = %q/%d", ke.Key, ke.Line)
	}
}

func TestConfigUnknownArrayTable(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[[context.foo]]\nname = \"x\"\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if !strings.HasPrefix(ke.Key, "context.foo") {
		t.Fatalf("key = %q", ke.Key)
	}
	if ke.Line == 0 {
		t.Fatal("line = 0")
	}
}

func TestConfigCommentOnlyUsesDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "# only a comment\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Loop.MaxIterations != 40 || cfg.Exec.KillGraceS != 3 {
		t.Errorf("defaults not applied: %+v", cfg)
	}
}

func TestConfigDurationRejectsGarbage(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[log]\nmessage_content_ttl = \"soon\"\n")
	requireKeyError(t, err, ErrSyntax)
}

// TestConfigCloneIsDeep covers every slice/map field, not only the ones the
// E-13 scenario mutates.
func TestConfigCloneIsDeep(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[provider]", `fallback = ["a/b"]`,
		"[budget]", "providers = { p = 1 }",
		"[approval]", `shell_allow = [["go", "test"]]`, `deny_paths = ["x"]`, `add_dirs = ["/d"]`,
		"[exec]", `env_allow = ["PATH"]`, `env = { A = "1" }`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	c := cfg.Clone()
	c.Provider.Fallback[0] = "z/z"
	c.Budget.Providers["p"] = 9
	c.Approval.ShellAllow[0][0] = "rm"
	c.Approval.DenyPaths[0] = "y"
	c.Approval.AddDirs[0] = "/e"
	c.Exec.EnvAllow[0] = "HOME"
	c.Exec.Env["A"] = "2"
	if cfg.Provider.Fallback[0] != "a/b" || cfg.Budget.Providers["p"] != 1 ||
		cfg.Approval.ShellAllow[0][0] != "go" || cfg.Approval.DenyPaths[0] != "x" ||
		cfg.Approval.AddDirs[0] != "/d" || cfg.Exec.EnvAllow[0] != "PATH" || cfg.Exec.Env["A"] != "1" {
		t.Fatal("Clone shares storage with the original")
	}
}

func TestConfigReadCausePreserved(t *testing.T) {
	t.Parallel()
	_, err := testLoader(t, nil).Load("missing.toml")
	if !errors.Is(err, ErrRead) {
		t.Fatalf("err = %v", err)
	}
}
