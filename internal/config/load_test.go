package config

import (
	"errors"
	"io/fs"
	"strings"
	"testing"
	"time"

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

// TestConfigDefaultsMatchSpec is the ROADMAP F1 acceptance hook (ADR-083).
func TestConfigDefaultsMatchSpec(t *testing.T) {
	t.Parallel()
	want := Config{
		Provider: Provider{
			Model: "", Fallback: []string{}, Light: "", MaxOutput: 8192,
		},
		Loop: Loop{
			MaxIterations: 40, MaxWallS: 900, MaxIdentical: 3,
			TurnMaxInputTok: 400000, TurnMaxOutputTok: 32000, ParallelTools: 4,
		},
		Budget: Budget{
			TurnUSD: 0.50, SessionUSD: 5.00, DailyUSD: 0, MonthlyUSD: 0,
			WarnAt: 0.80, Providers: map[string]float64{},
		},
		Approval: Approval{
			Mode:       "ask",
			ShellAllow: [][]string{},
			DenyPaths: []string{
				"~/.ssh", "~/.gnupg", "~/.aws", "**/.env", "**/*.pem",
				"~/.netrc", "~/.git-credentials", "~/.config/gh",
				"~/.docker/config.json", "~/.kube", "<config_dir>/secrets.enc",
				"/proc/*/environ", "/proc/*/task/*/environ", "/proc/*/mem",
			},
			AddDirs: []string{}, PreviewTimeoutS: 600,
		},
		Exec: Exec{
			DefaultTimeoutS: 120, IdleTimeoutS: 60, AllowDestructive: false,
			KillGraceS: 3, EnvAllow: []string{}, Env: map[string]string{}, PassSSHAgent: false,
		},
		Agent: Agent{VerifyOnStop: "run"},
		Log:   Log{Level: "info", MessageContent: false, MessageContentTTL: Duration(24 * time.Hour)},
	}
	if diff := cmp.Diff(want, Default()); diff != "" {
		t.Errorf("Default() differs from SPEC §12 (-want +got):\n%s", diff)
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
		t.Errorf("shell_allow (-want +got):\n%s", diff)
	}
	if len(cfg.Approval.DenyPaths) != 14 {
		t.Errorf("deny_paths default lost: %v", cfg.Approval.DenyPaths)
	}
}

func TestConfigUnknownKey(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[provider]\nmodel = \"p/m\"\nmodl = \"typo\"\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if ke.Key != "provider.modl" || ke.Line != 3 {
		t.Errorf("key/line = %q/%d, want provider.modl/3", ke.Key, ke.Line)
	}
}

func TestConfigUnknownSection(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[memory]\nentry_max_chars = 500\n")
	ke := keyErrorOf(t, err, ErrUnknownKey)
	if ke.Key != "memory" || ke.Line != 1 {
		t.Errorf("key/line = %q/%d, want memory/1", ke.Key, ke.Line)
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

// FuzzConfigTOML: arbitrary TOML must produce a config or a typed *Error;
// validation arrives with the validation PR.
func FuzzConfigTOML(f *testing.F) {
	for _, seed := range []string{"", "[provider]\nmodel = \"p/m\"\n", "[provider]\nmodel = [\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		if len(data) > MaxFileSize+1 {
			return
		}
		l, err := New(Deps{
			ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
			Getenv:   func(string) string { return "" },
			HomeDir:  func() (string, error) { return "/home/u", nil },
			GOOS:     "linux",
		})
		if err != nil {
			t.Fatalf("New: %v", err)
		}
		if _, err := l.Parse([]byte(data), "fuzz.toml"); err != nil {
			if _, ok := errors.AsType[*Error](err); !ok {
				t.Fatalf("non-typed error %T: %v", err, err)
			}
		}
	})
}
