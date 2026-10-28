package config

import (
	"errors"
	"io/fs"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/update"
	"github.com/google/go-cmp/cmp"
)

func testLoader(t *testing.T, files map[string][]byte) *Loader {
	t.Helper()
	l, err := New(Deps{
		ReadFile: func(name string) ([]byte, error) {
			if data, ok := files[name]; ok {
				return data, nil
			}
			return nil, fs.ErrNotExist
		},
		Getenv:  func(string) string { return "" },
		HomeDir: func() (string, error) { return "/home/u", nil },
		GOOS:    "linux",
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return l
}

func parse(t *testing.T, toml string) (Config, error) {
	t.Helper()
	return testLoader(t, nil).Parse([]byte(toml), "config.toml")
}

// hostPath is an absolute path for the host GOOS; filepath.IsAbs drives the
// skills check, so a literal "/p" is rejected on Windows.
func hostPath(t *testing.T) string {
	t.Helper()
	return filepath.ToSlash(t.TempDir())
}

func tomlQuote(p string) string { return strconv.Quote(p) }

func tomlAbsPath(t *testing.T) string {
	t.Helper()
	return tomlQuote(hostPath(t))
}

// TestConfigDefaultsMatchSpec is the ROADMAP F1 acceptance hook (ADR-083):
// every default exposed in F1 equals SPEC §12 (and, for log content controls,
// the P29/GAP-SEC-11 line).
func TestConfigDefaultsMatchSpec(t *testing.T) {
	t.Parallel()
	want := Config{
		ConfigVersion: SchemaVersion,
		Provider: Provider{
			Model:     "",
			Fallback:  []string{},
			Light:     "",
			MaxOutput: 8192,
		},
		Loop: Loop{
			MaxIterations:    40,
			MaxWallS:         900,
			MaxIdentical:     3,
			TurnMaxInputTok:  400000,
			TurnMaxOutputTok: 32000,
			ParallelTools:    4,
		},
		Budget: Budget{
			TurnUSD:         0.50,
			SessionUSD:      5.00,
			DailyUSD:        0,
			MonthlyUSD:      0,
			WarnAt:          0.80,
			Providers:       map[string]float64{},
			ToolsSessionUSD: 1.00,
			ToolsDailyUSD:   5.00,
		},
		Approval: Approval{
			Mode:       "ask",
			ShellAllow: [][]string{},
			DenyPaths: []string{
				"~/.ssh", "~/.gnupg", "~/.aws", "**/.env", "**/*.pem",
				"~/.netrc", "~/.git-credentials", "~/.config/gh",
				"~/.docker/config.json", "~/.kube", "<config_dir>/secrets.enc",
				"<config_dir>/at-rest.*", "<config_dir>/backup.keys",
				"/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem",
			},
			AddDirs:         []string{},
			PreviewTimeoutS: 600,
		},
		Exec: Exec{
			DefaultTimeoutS:  120,
			IdleTimeoutS:     60,
			AllowDestructive: false,
			KillGraceS:       3,
			EnvAllow:         []string{},
			Env:              map[string]string{},
			PassSSHAgent:     false,
		},
		Agent:   Agent{VerifyOnStop: "run"},
		Storage: Storage{Encrypt: false},
		Network: Network{Offline: false, Background: true},
		Skills: Skills{
			ProjectDiscovery: true,
			TrustedProjects:  []string{},
			ExternalDirs:     []string{},
			InlineShell:      false,
		},
		Sessions: Sessions{AutoPrune: false, PruneAfterDays: 90},
		Context: Context{
			CompactAt:       0.75,
			SummaryMaxTok:   600,
			SummariesMaxTok: 3000,
			PinnedSchemaTok: 3000,
		},
		Memory:    Memory{EntryMaxChars: 500, TotalMaxChars: 4000, Embeddings: ""},
		Recovery:  Recovery{Continue: "safe_auto"},
		UI:        UI{Language: "auto", Theme: "tern-dark"},
		Gateway:   Gateway{MaxParallelTurns: 4, Users: map[string]GatewayUser{}},
		Providers: Providers{Custom: []CustomProvider{}},
		Log:       Log{Level: "info", MessageContent: false, MessageContentTTL: Duration(24 * time.Hour)},
		Update:    update.DefaultConfig(),
		ACP:       ACP{UseClientFS: true},
	}
	if diff := cmp.Diff(want, Default()); diff != "" {
		t.Errorf("Default() differs from SPEC §12 (-want +got):\n%s", diff)
	}
}

func TestConfigDefaultsValidate(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Default().Validate(): %v", err)
	}
}

// TestConfigMinimum accepts the two-line minimum config of SPEC §4.9.
func TestConfigMinimum(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[provider]\nmodel = \"openrouter/anthropic/claude-sonnet-4.5\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Provider.Model != "openrouter/anthropic/claude-sonnet-4.5" {
		t.Errorf("model = %q", cfg.Provider.Model)
	}
	if cfg.Loop.MaxIterations != 40 || cfg.Approval.Mode != "ask" {
		t.Errorf("defaults not applied: %+v", cfg)
	}
	if cfg.Context.CompactAt != 0.75 || cfg.UI.Theme != "tern-dark" ||
		cfg.Gateway.MaxParallelTurns != 4 || cfg.Budget.ToolsSessionUSD != 1.00 ||
		cfg.Budget.ToolsDailyUSD != 5.00 {
		t.Errorf("new §12 defaults not applied: %+v", cfg)
	}
}

func TestConfigOverridesKeepDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[loop]\nmax_iterations = 7\n\n[approval]\nshell_allow = [[\"go\", \"test\", \"./...\"]]\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Loop.MaxIterations != 7 || cfg.Loop.MaxWallS != 900 {
		t.Errorf("loop = %+v", cfg.Loop)
	}
	if diff := cmp.Diff([][]string{{"go", "test", "./..."}}, cfg.Approval.ShellAllow); diff != "" {
		t.Errorf("shell_allow replaced, not appended (-want +got):\n%s", diff)
	}
	if len(cfg.Approval.DenyPaths) != 16 {
		t.Errorf("deny_paths default lost: %v", cfg.Approval.DenyPaths)
	}
	cfg2, err := parse(t, "[ui]\ntheme = \"mono\"\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg2.UI.Theme != "mono" {
		t.Errorf("theme = %q", cfg2.UI.Theme)
	}
	if cfg2.Context.CompactAt != 0.75 || cfg2.Gateway.MaxParallelTurns != 4 ||
		cfg2.Memory.TotalMaxChars != 4000 || cfg2.Recovery.Continue != "safe_auto" {
		t.Errorf("other defaults lost: %+v", cfg2)
	}
}

func TestConfigUnknownKey(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, toml, key string }{
		{"provider", "[provider]\nmodel = \"p/m\"\nmodl = \"typo\"\n", "provider.modl"},
		{"memory", "[memory]\nentry_max_char = 500\n", "memory.entry_max_char"},
		{"context", "[context]\ncompact = 0.75\n", "context.compact"},
		{"ui", "[ui]\nthemes = \"mono\"\n", "ui.themes"},
		{"gateway", "[gateway]\nmax_parallel = 4\n", "gateway.max_parallel"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, tc.toml)
			ke := keyErrorOf(t, err, ErrUnknownKey)
			if ke.Key != tc.key {
				t.Errorf("key = %q, want %q", ke.Key, tc.key)
			}
			if ke.Line != 3 && tc.name == "provider" {
				t.Errorf("line = %d, want 3", ke.Line)
			}
		})
	}
}

func TestConfigUnknownSection(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct{ name, section, body string }{
		{"typo_memo", "memo", "entry_max_chars = 500"},
		{"typo_providers", "provders", `model = "p/m"`},
		{"plural_gateway", "gateways", "max_parallel_turns = 4"},
		{"memory_typo", "memoryx", "entry_max_chars = 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			_, err := parse(t, "["+tc.section+"]\n"+tc.body+"\n")
			ke := keyErrorOf(t, err, ErrUnknownKey)
			if ke.Key != tc.section {
				t.Errorf("key = %q, want %q", ke.Key, tc.section)
			}
			if ke.Line != 1 {
				t.Errorf("line = %d, want 1", ke.Line)
			}
		})
	}
}

func TestConfigInvalidValues(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		toml string
		key  string
	}{
		{"iterations_high", "[loop]\nmax_iterations = 41\n", "loop.max_iterations"},
		{"iterations_zero", "[loop]\nmax_iterations = 0\n", "loop.max_iterations"},
		{"identical", "[loop]\nmax_identical = 6\n", "loop.max_identical"},
		{"input_tokens", "[loop]\nturn_max_input_tok = 400001\n", "loop.turn_max_input_tok"},
		{"mode", "[approval]\nmode = \"yolo\"\n", "approval.mode"},
		{"mode_plan", "[approval]\nmode = \"plan\"\n", "approval.mode"},
		{"shell_allow_empty", "[approval]\nshell_allow = [[]]\n", "approval.shell_allow"},
		{"shell_allow_space", "[approval]\nshell_allow = [[\"go test\"]]\n", "approval.shell_allow"},
		{"deny_path_nul", "[approval]\ndeny_paths = [\"a\\u0000b\"]\n", "approval.deny_paths"},
		{"kill_grace", "[exec]\nkill_grace_s = 4\n", "exec.kill_grace_s"},
		{"idle_timeout", "[exec]\nidle_timeout_s = 0\n", "exec.idle_timeout_s"},
		{"env_name", "[exec]\nenv_allow = [\"1BAD\"]\n", "exec.env_allow"},
		{"env_value_nul", "[exec]\nenv = { OK = \"a\\u0000b\" }\n", "exec.env.OK"},
		{"warn_at", "[budget]\nwarn_at = 0\n", "budget.warn_at"},
		{"negative_usd", "[budget]\nturn_usd = -1\n", "budget.turn_usd"},
		{"verify", "[agent]\nverify_on_stop = \"always\"\n", "agent.verify_on_stop"},
		{"log_level", "[log]\nlevel = \"verbose\"\n", "log.level"},
		{"ttl", "[log]\nmessage_content_ttl = \"73h\"\n", "log.message_content_ttl"},
		{"max_output", "[provider]\nmax_output = 0\n", "provider.max_output"},
		{"fallback_many", "[provider]\nfallback = [\"a/b\", \"c/d\", \"e/f\"]\n", "provider.fallback"},
		{"fallback_ref", "[provider]\nfallback = [\"nodash\"]\n", "provider.fallback"},
		{"light_ref", "[provider]\nlight = \"nodash\"\n", "provider.light"},
	}
	for _, tc := range tests {
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

func TestConfigSyntaxError(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[provider\nmodel = \"p/m\"\n")
	ke := keyErrorOf(t, err, ErrSyntax)
	if ke.Line == 0 {
		t.Errorf("syntax error without line: %v", err)
	}
}

func TestConfigTooLarge(t *testing.T) {
	t.Parallel()
	_, err := parse(t, strings.Repeat("#", MaxFileSize+1))
	requireKeyError(t, err, ErrTooLarge)
}

func TestConfigReadError(t *testing.T) {
	t.Parallel()
	_, err := testLoader(t, nil).Load("missing.toml")
	requireKeyError(t, err, ErrRead)
	if !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("cause not preserved: %v", err)
	}
}

func TestConfigNilDeps(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("New(Deps{}) = %v, want ErrDeps", err)
	}
}

// TestConfigImmutable is E-13: 30 scenarios, then the original snapshot must
// be byte-identical. Scenarios only read, clone, or mutate clones.
func TestConfigImmutable(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		"[provider]",
		"model = \"p/m\"",
		"fallback = [\"a/b\"]",
		"[budget]",
		"providers = { a = 20.0 }",
		"[approval]",
		"shell_allow = [[\"go\", \"test\"]]",
		"deny_paths = [\"~/.ssh\"]",
		"[exec]",
		"env_allow = [\"PATH\"]",
		"env = { A = \"1\" }",
		"[context]",
		"compact_at = 0.5",
		"[gateway.users.\"telegram:1\"]",
		"workspace = \"/srv/ws\"",
		"[[providers.custom]]",
		"name = \"k\"",
		"base_url = \"https://k.example/v1\"",
		"key_cmd = [\"k\"]",
		"extra_headers = { A = \"1\" }",
		"extra_body = { k = \"v\" }",
		"models = [{ id = \"m\", capabilities = [\"tools\"] }]",
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	before := cfg.Clone()
	for range 30 {
		if err := cfg.Validate(); err != nil {
			t.Fatalf("Validate: %v", err)
		}
		_ = cfg.DenyPaths("/tmp/cfg")
		clone := cfg.Clone()
		clone.Provider.Fallback[0] = "changed/x"
		clone.Budget.Providers["a"] = 1
		clone.Approval.ShellAllow[0][0] = "changed"
		clone.Approval.DenyPaths[0] = "/tmp"
		clone.Exec.EnvAllow[0] = "HOME"
		clone.Exec.Env["A"] = "2"
		delete(clone.Gateway.Users, "telegram:1")
		clone.Providers.Custom[0].KeyCmd[0] = "x"
		clone.Providers.Custom[0].ExtraHeaders["A"] = "2"
		clone.Providers.Custom[0].ExtraBody["k"] = "w"
		clone.Providers.Custom[0].Models[0].Capabilities[0] = "video"
	}
	if diff := cmp.Diff(before, cfg); diff != "" {
		t.Errorf("config mutated (-before +after):\n%s", diff)
	}
}

func TestConfigPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		goos     string
		env      map[string]string
		profile  string
		wantCfg  string
		wantData string
	}{
		{
			name: "linux_xdg", goos: "linux",
			env:     map[string]string{"XDG_CONFIG_HOME": "/x/cfg", "XDG_DATA_HOME": "/x/data"},
			wantCfg: "/x/cfg/damnbot", wantData: "/x/data/damnbot",
		},
		{
			name: "linux_default", goos: "linux",
			wantCfg: "/home/u/.config/damnbot", wantData: "/home/u/.local/share/damnbot",
		},
		{
			name: "linux_relative_xdg_ignored", goos: "linux",
			env:     map[string]string{"XDG_CONFIG_HOME": "relative", "XDG_DATA_HOME": "../x"},
			wantCfg: "/home/u/.config/damnbot", wantData: "/home/u/.local/share/damnbot",
		},
		{
			name: "darwin", goos: "darwin",
			wantCfg:  "/home/u/Library/Application Support/damnbot",
			wantData: "/home/u/Library/Application Support/damnbot",
		},
		{
			name: "windows", goos: "windows",
			env:     map[string]string{"APPDATA": "/roaming", "LOCALAPPDATA": "/local"},
			wantCfg: "/roaming/damnbot", wantData: "/local/damnbot",
		},
		{
			name: "profile", goos: "linux", profile: "work",
			wantCfg:  "/home/u/.config/damnbot/profiles/work",
			wantData: "/home/u/.local/share/damnbot/profiles/work",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			l, err := New(Deps{
				ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
				Getenv:   func(k string) string { return tc.env[k] },
				HomeDir:  func() (string, error) { return "/home/u", nil },
				GOOS:     tc.goos,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			got, err := l.Paths(tc.profile)
			if err != nil {
				t.Fatalf("Paths: %v", err)
			}
			if filepath.ToSlash(got.ConfigDir) != tc.wantCfg || filepath.ToSlash(got.DataDir) != tc.wantData {
				t.Errorf("Paths = %+v, want cfg %s data %s", got, tc.wantCfg, tc.wantData)
			}
			if want := path.Join(tc.wantCfg, "config.toml"); filepath.ToSlash(got.ConfigFile()) != want {
				t.Errorf("ConfigFile = %q, want %q", got.ConfigFile(), want)
			}
		})
	}
}

func TestConfigProfileRejectsTraversal(t *testing.T) {
	t.Parallel()
	l := testLoader(t, nil)
	for _, profile := range []string{"../x", "Work", "a/b", "-lead", strings.Repeat("a", 65), ""} {
		if profile == "" {
			continue // empty is the default profile
		}
		if _, err := l.Paths(profile); !errors.Is(err, ErrProfile) {
			t.Errorf("Paths(%q) = %v, want ErrProfile", profile, err)
		}
	}
}

func TestConfigDenyPathsExpansion(t *testing.T) {
	t.Parallel()
	cfg := Default()
	got := cfg.DenyPaths("/cfg/damnbot")
	want := append([]string{}, cfg.Approval.DenyPaths...)
	want[10] = "/cfg/damnbot/secrets.enc"
	want[11] = "/cfg/damnbot/at-rest.*"
	want[12] = "/cfg/damnbot/backup.keys"
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("DenyPaths (-want +got):\n%s", diff)
	}
	if cfg.Approval.DenyPaths[10] != "<config_dir>/secrets.enc" {
		t.Errorf("DenyPaths mutated the config: %q", cfg.Approval.DenyPaths[10])
	}
}

func TestConfigLoadProfile(t *testing.T) {
	t.Parallel()
	p, err := testLoader(t, nil).Paths("work")
	if err != nil {
		t.Fatalf("Paths: %v", err)
	}
	files := map[string][]byte{p.ConfigFile(): []byte("[provider]\nmodel = \"p/m\"\n")}
	cfg, err := testLoader(t, files).LoadProfile("work")
	if err != nil {
		t.Fatalf("LoadProfile: %v", err)
	}
	if cfg.Provider.Model != "p/m" {
		t.Errorf("model = %q", cfg.Provider.Model)
	}
}

func requireKeyError(t *testing.T, err, kind error) {
	t.Helper()
	if err == nil {
		t.Fatalf("error = nil, want %v", kind)
	}
	if _, ok := errors.AsType[*Error](err); !ok {
		t.Fatalf("error %v is not *Error", err)
	}
	if !errors.Is(err, kind) {
		t.Fatalf("error %v does not match %v", err, kind)
	}
}

func keyErrorOf(t *testing.T, err, kind error) *Error {
	t.Helper()
	requireKeyError(t, err, kind)
	ke, _ := errors.AsType[*Error](err)
	return ke
}

func TestConfigUpdateValidation(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[update]\nfeed_url = \"https://example.test/releases\"\nchannel = \"beta\"\nauto_install = true\n")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cfg.Update.FeedURL != "https://example.test/releases" || cfg.Update.Channel != "beta" || !cfg.Update.AutoInstall {
		t.Fatalf("update = %+v", cfg.Update)
	}
	for name, toml := range map[string]string{
		"channel": "[update]\nchannel = \"staging\"\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parse(t, toml); err == nil || !strings.Contains(err.Error(), "update.") {
				t.Fatalf("Parse(%q) = %v, want an update.* validation error", toml, err)
			}
		})
	}
}
