package config

import (
	"fmt"
	"maps"
	"slices"
	"time"
)

// Config is the immutable result of [Loader.Load]. All fields are values; maps
// and slices are never mutated by the package (E-13). Use [Config.Clone] to
// obtain an independent copy for tests or tooling.
type Config struct {
	Provider Provider `toml:"provider"`
	Loop     Loop     `toml:"loop"`
	Budget   Budget   `toml:"budget"`
	Approval Approval `toml:"approval"`
	Exec     Exec     `toml:"exec"`
	Agent    Agent    `toml:"agent"`
	Skills   Skills   `toml:"skills"`
	Sessions Sessions `toml:"sessions"`
	Log      Log      `toml:"log"`
}

// Skills is SPEC §4.8/§12 [skills]: the local skill sources (SKILL-009,
// SKILL-010; ADR-168). Project discovery reads only the roots of projects
// named in TrustedProjects or in <config>/trusted-projects, and every skill
// from a project or an external directory is quarantined like an import.
type Skills struct {
	ProjectDiscovery bool     `toml:"project_discovery"`
	TrustedProjects  []string `toml:"trusted_projects"`
	ExternalDirs     []string `toml:"external_dirs"`
	InlineShell      bool     `toml:"inline_shell"`
}

// Provider is SPEC §12 [provider].
type Provider struct {
	Model     string   `toml:"model"`
	Fallback  []string `toml:"fallback"`
	Light     string   `toml:"light"`
	MaxOutput int      `toml:"max_output"`
}

// Loop is SPEC §12 [loop] (P2, REL-07).
type Loop struct {
	MaxIterations    int `toml:"max_iterations"`
	MaxWallS         int `toml:"max_wall_s"`
	MaxIdentical     int `toml:"max_identical"`
	TurnMaxInputTok  int `toml:"turn_max_input_tok"`
	TurnMaxOutputTok int `toml:"turn_max_output_tok"`
	ParallelTools    int `toml:"parallel_tools"`
}

// Budget is SPEC §12 [budget] (P27).
type Budget struct {
	TurnUSD    float64            `toml:"turn_usd"`
	SessionUSD float64            `toml:"session_usd"`
	DailyUSD   float64            `toml:"daily_usd"`
	MonthlyUSD float64            `toml:"monthly_usd"`
	WarnAt     float64            `toml:"warn_at"`
	Providers  map[string]float64 `toml:"providers"`
}

// Approval is SPEC §12 [approval] (ADR-069, ADR-079).
type Approval struct {
	Mode            string     `toml:"mode"`
	ShellAllow      [][]string `toml:"shell_allow"`
	DenyPaths       []string   `toml:"deny_paths"`
	AddDirs         []string   `toml:"add_dirs"`
	PreviewTimeoutS int        `toml:"preview_timeout_s"`
}

// Exec is SPEC §12 [exec] (ADR-072, ADR-079, ADR-081) plus the SEC-02 keys
// exec.env_allow, exec.env and exec.pass_ssh_agent.
type Exec struct {
	DefaultTimeoutS  int               `toml:"default_timeout_s"`
	IdleTimeoutS     int               `toml:"idle_timeout_s"`
	AllowDestructive bool              `toml:"allow_destructive"`
	KillGraceS       int               `toml:"kill_grace_s"`
	EnvAllow         []string          `toml:"env_allow"`
	Env              map[string]string `toml:"env"`
	PassSSHAgent     bool              `toml:"pass_ssh_agent"`
}

// Agent is SPEC §12 [agent].
type Agent struct {
	VerifyOnStop string `toml:"verify_on_stop"`
}

// Log is SPEC §12 [log] plus the P29/GAP-SEC-11 content controls.
type Log struct {
	Level             string   `toml:"level"`
	MessageContent    bool     `toml:"message_content"`
	MessageContentTTL Duration `toml:"message_content_ttl"`
}

// Sessions is SPEC §12 [sessions] (ADR-142, MEM-015). Auto-prune is off by
// default because it deletes transcript data.
type Sessions struct {
	AutoPrune      bool `toml:"auto_prune"`
	PruneAfterDays int  `toml:"prune_after_days"`
}

// Duration is a TOML string duration such as "24h".
type Duration time.Duration

// UnmarshalText parses a Go duration string; a non-string TOML value is also
// routed here by the decoder, so reject anything time.ParseDuration rejects.
func (d *Duration) UnmarshalText(text []byte) error {
	v, err := time.ParseDuration(string(text))
	if err != nil {
		return fmt.Errorf("config: duration: %w", err)
	}
	*d = Duration(v)
	return nil
}

// Std returns the duration as time.Duration.
func (d Duration) Std() time.Duration { return time.Duration(d) }

// String returns the canonical duration form.
func (d Duration) String() string { return time.Duration(d).String() }

// Default returns the SPEC §12 defaults for every key exposed in F1.
func Default() Config {
	return Config{
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
			TurnUSD:    0.50,
			SessionUSD: 5.00,
			DailyUSD:   0,
			MonthlyUSD: 0,
			WarnAt:     0.80,
			Providers:  map[string]float64{},
		},
		Approval: Approval{
			Mode:            "ask",
			ShellAllow:      [][]string{},
			DenyPaths:       defaultDenyPaths(),
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
		Agent: Agent{VerifyOnStop: "run"},
		Skills: Skills{
			ProjectDiscovery: true,
			TrustedProjects:  []string{},
			ExternalDirs:     []string{},
			InlineShell:      false,
		},
		Sessions: Sessions{
			AutoPrune:      false,
			PruneAfterDays: 90,
		},
		Log: Log{
			Level:             "info",
			MessageContent:    false,
			MessageContentTTL: Duration(24 * time.Hour),
		},
	}
}

// defaultDenyPaths is SPEC §12 deny_paths in declaration order. The
// <config_dir> placeholder is expanded by the sandbox caller (ADR-097).
func defaultDenyPaths() []string {
	return []string{
		"~/.ssh",
		"~/.gnupg",
		"~/.aws",
		"**/.env",
		"**/*.pem",
		"~/.netrc",
		"~/.git-credentials",
		"~/.config/gh",
		"~/.docker/config.json",
		"~/.kube",
		"<config_dir>/secrets.enc",
		"/proc/*/environ",
		"/proc/*/task/*/environ",
		"/proc/*/mem",
	}
}

// Clone returns a deep copy: slices and maps are independent of c. The copy is
// the only supported way to derive a mutable value; the package never mutates
// c itself (E-13).
func (c *Config) Clone() Config {
	out := *c
	out.Provider.Fallback = slices.Clone(c.Provider.Fallback)
	out.Budget.Providers = maps.Clone(c.Budget.Providers)
	out.Approval.ShellAllow = cloneShellAllow(c.Approval.ShellAllow)
	out.Approval.DenyPaths = slices.Clone(c.Approval.DenyPaths)
	out.Approval.AddDirs = slices.Clone(c.Approval.AddDirs)
	out.Exec.EnvAllow = slices.Clone(c.Exec.EnvAllow)
	out.Exec.Env = maps.Clone(c.Exec.Env)
	return out
}

func cloneShellAllow(in [][]string) [][]string {
	if in == nil {
		return nil
	}
	out := make([][]string, len(in))
	for i, argv := range in {
		out[i] = slices.Clone(argv)
	}
	return out
}
