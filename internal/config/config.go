package config

import (
	"fmt"
	"maps"
	"slices"
	"time"

	"github.com/damnbot/damnbot/internal/toolset"
	"github.com/damnbot/damnbot/internal/update"
)

// SchemaVersion is the config schema this binary writes and understands
// (SPEC §12, ADR-221). A file may declare a lower or equal config_version; a
// higher one is refused by [Config.Validate] so a stale binary never runs a
// newer file with silent defaults.
const SchemaVersion = 1

// Config is the immutable result of [Loader.Load]. All fields are values; maps
// and slices are never mutated by the package (E-13). Use [Config.Clone] to
// obtain an independent copy for tests or tooling.
type Config struct {
	// ConfigVersion is the schema version the file expects (SPEC §12). It
	// defaults to [SchemaVersion]; a file without the key is legacy and keeps
	// the default.
	ConfigVersion int      `toml:"config_version" json:"config_version"`
	Provider      Provider `toml:"provider"`
	Loop          Loop     `toml:"loop"`
	Budget        Budget   `toml:"budget"`
	Approval      Approval `toml:"approval"`
	Exec          Exec     `toml:"exec"`
	Agent         Agent    `toml:"agent"`
	Skills        Skills   `toml:"skills"`
	Sessions      Sessions `toml:"sessions"`
	Context       Context  `toml:"context"`
	Memory        Memory   `toml:"memory"`
	Recovery      Recovery `toml:"recovery"`
	UI            UI       `toml:"ui"`
	// Gateway and Providers are accepted and validated but have no runtime
	// consumer until F4/F5 (ADR-197): config is data-only for them.
	Gateway   Gateway   `toml:"gateway"`
	Modules   Modules   `toml:"modules"`
	Providers Providers `toml:"providers"`
	Log       Log       `toml:"log"`
	Storage   Storage   `toml:"storage"`
	Network   Network   `toml:"network"`
	// Toolsets is the F2 [toolsets] section (ADR-139). Empty Active keeps the
	// pre-toolset behaviour: every registered tool is exposed.
	Toolsets toolset.Config `toml:"toolsets"`
	Update   update.Config  `toml:"update"`
	ACP      ACP            `toml:"acp"`
}

// ACP is SPEC §12 [acp]: client-side capability use (GAP-UX-01).
type ACP struct {
	// UseClientFS lets file tools read the ACP client's editor buffer and
	// write through it (default true). False keeps every file tool local
	// and records no acp_fs_fallback event.
	UseClientFS bool `toml:"use_client_fs"`
	// UseClientTerminal runs one-shot shell commands on the ACP client
	// (terminal/*, GAP-UX-01 butir b); default false keeps the local runner.
	UseClientTerminal bool `toml:"use_client_terminal"`
}

// Skills is SPEC §4.8/§12 [skills]: the local skill sources (SKILL-009/010,
// ADR-175). Project discovery reads only the roots of projects named in
// TrustedProjects or in <config>/trusted-projects, and every skill from a
// project or an external directory is quarantined like an import.
type Skills struct {
	ProjectDiscovery bool     `toml:"project_discovery" json:"project_discovery"`
	TrustedProjects  []string `toml:"trusted_projects" json:"trusted_projects"`
	ExternalDirs     []string `toml:"external_dirs" json:"external_dirs"`
	// InlineShell enables the opt-in `!`cmd` expansion of SKILL-004 for trusted
	// skills only (ADR-168). Off by default: a skill body is data, not a script.
	InlineShell bool `toml:"inline_shell" json:"inline_shell"`
}

// Provider is SPEC §12 [provider].
type Provider struct {
	Model     string   `toml:"model"`
	Fallback  []string `toml:"fallback"`
	Light     string   `toml:"light"`
	MaxOutput int      `toml:"max_output"`
	// SendAttribution opts in to the attribution headers a provider preset
	// declares (SEC-25); default false sends none.
	SendAttribution bool `toml:"send_attribution" json:"send_attribution"`
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
	// ToolsSessionUSD and ToolsDailyUSD cap paid non-LLM tools (COST-11).
	// They are value-only until the F6 metering consumes them.
	ToolsSessionUSD float64 `toml:"tools_session_usd"`
	ToolsDailyUSD   float64 `toml:"tools_daily_usd"`
	// GatewayUserDailyUSD caps the daily spend of one role user gateway
	// identity in (0,100]; the owner is never capped (SPEC §18.4, GAP-GW-02).
	GatewayUserDailyUSD float64 `toml:"gateway_user_daily_usd"`
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
// exec.env_allow, exec.env and exec.pass_ssh_agent, and the F5 backend
// selection (ADR-275): exec.backend plus [exec.backends.<id>].
type Exec struct {
	DefaultTimeoutS  int               `toml:"default_timeout_s"`
	IdleTimeoutS     int               `toml:"idle_timeout_s"`
	AllowDestructive bool              `toml:"allow_destructive"`
	KillGraceS       int               `toml:"kill_grace_s"`
	EnvAllow         []string          `toml:"env_allow"`
	Env              map[string]string `toml:"env"`
	PassSSHAgent     bool              `toml:"pass_ssh_agent"`
	Backend          string            `toml:"backend"`
	Backends         ExecBackends      `toml:"backends"`
}

// ExecBackends is the per-backend configuration of [exec.backends]
// (EXEC-AND-CODING §A.7). Selecting a backend with missing required fields
// is a validation error, not a silent fallback to local.
type ExecBackends struct {
	Docker      DockerExec      `toml:"docker"`
	SSH         SSHExec         `toml:"ssh"`
	Singularity SingularityExec `toml:"singularity"`
	Modal       CloudExec       `toml:"modal"`
	Daytona     CloudExec       `toml:"daytona"`
	Vercel      CloudExec       `toml:"vercel_sandbox"`
}

// DockerExec configures the docker CLI backend.
type DockerExec struct {
	Binary         string   `toml:"binary"`
	Image          string   `toml:"image"`
	User           string   `toml:"user"`
	CPUMilli       int      `toml:"cpu_milli"`
	MemoryMiB      int      `toml:"memory_mib"`
	PIDs           int      `toml:"pids"`
	Network        string   `toml:"network"`      // none (default) | bridge | allowlist
	NetworkName    string   `toml:"network_name"` // isolated docker network used by allowlist
	EgressAllow    []string `toml:"egress_allow"`
	ProxyURL       string   `toml:"proxy_url"`
	AllowUnlimited bool     `toml:"allow_unlimited"`
}

// SSHExec configures the SSH backend.
type SSHExec struct {
	Host       string `toml:"host"`
	User       string `toml:"user"`
	Port       int    `toml:"port"`
	KeyFile    string `toml:"key_file"`
	KnownHosts string `toml:"known_hosts"`
	KeepAliveS int    `toml:"keepalive_s"`
	IdleCloseS int    `toml:"idle_close_s"`
	TimeoutS   int    `toml:"timeout_s"`
	MaxConns   int    `toml:"max_conns"`
}

// SingularityExec configures the singularity/apptainer CLI backend.
type SingularityExec struct {
	Binary         string `toml:"binary"`
	Image          string `toml:"image"`
	Network        string `toml:"network"` // none (default) | host
	Fakeroot       bool   `toml:"fakeroot"`
	AllowUnlimited bool   `toml:"allow_unlimited"`
}

// CloudExec configures one HTTP sandbox backend (modal, daytona, vercel
// sandbox). TokenSecret names an entry in the secret store; the token is
// read by the composition root and never enters a child environment.
type CloudExec struct {
	Endpoint    string `toml:"endpoint"`
	TokenSecret string `toml:"token_secret"`
	Image       string `toml:"image"`
	TimeoutS    int    `toml:"timeout_s"`
	ProjectID   string `toml:"project_id"`
	TeamID      string `toml:"team_id"`
}

// defaultExecBackends is the SPEC §12 [exec.backends] block (ADR-275): the
// docker caps are the conservative defaults, cloud backends stay inert until
// endpoint+token are supplied.
func defaultExecBackends() ExecBackends {
	return ExecBackends{
		Docker: DockerExec{
			Binary: "docker", User: "65534:65534", CPUMilli: 1000,
			MemoryMiB: 512, PIDs: 128, Network: "none",
		},
		SSH:         SSHExec{Port: 22, KeepAliveS: 30, IdleCloseS: 600, TimeoutS: 120, MaxConns: 4},
		Singularity: SingularityExec{Binary: "apptainer", Network: "none"},
		Modal:       CloudExec{TimeoutS: 120},
		Daytona:     CloudExec{TimeoutS: 120},
		Vercel:      CloudExec{TimeoutS: 120},
	}
}

// Agent is SPEC §12 [agent].
type Agent struct {
	VerifyOnStop string `toml:"verify_on_stop"`
}

// Storage is the optional at-rest encryption section (ADR-124).
type Storage struct {
	Encrypt bool `toml:"encrypt"`
}

// Network is CROSS-CUTTING §4 [network]: offline refuses every non-loopback
// dial, while background=false refuses non-inference egress (GAP-OPS-02).
type Network struct {
	Offline    bool `toml:"offline" json:"offline"`
	Background bool `toml:"background" json:"background"`
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

// Context is SPEC §12 [context]: compaction thresholds.
type Context struct {
	CompactAt       float64 `toml:"compact_at"`
	SummaryMaxTok   int     `toml:"summary_max_tok"`
	SummariesMaxTok int     `toml:"summaries_max_tok"`
	PinnedSchemaTok int     `toml:"pinned_schema_tok"`
}

// Memory is SPEC §12 [memory].
type Memory struct {
	EntryMaxChars int    `toml:"entry_max_chars"`
	TotalMaxChars int    `toml:"total_max_chars"`
	Embeddings    string `toml:"embeddings"`
}

// Recovery is SPEC §12 [recovery]: what a broken turn does by default.
type Recovery struct {
	Continue string `toml:"continue"`
}

// UI is SPEC §12 [ui]: interface language (§9.12) and theme (§13.1).
type UI struct {
	Language string `toml:"language"`
	Theme    string `toml:"theme"`
	// Notices enables non-critical notices (tips); critical notices are not
	// affected (GAP-OPS-09).
	Notices bool `toml:"notices" json:"notices"`
}

// Gateway is SPEC §12 gateway data (P25, GAP-GW-02). It is accepted and
// validated but has no runtime consumer until F4.
type Gateway struct {
	MaxParallelTurns int                    `toml:"max_parallel_turns"`
	BusyPolicy       string                 `toml:"busy_policy"`
	Users            map[string]GatewayUser `toml:"users"`
}

// ModulesGateway holds the gateway adapter configuration.
type ModulesGateway struct {
	Telegram ModulesTelegram `toml:"telegram"`
}

// ModulesTelegram is [modules.gateway.telegram]: thread_fallback is "fail"
// (default) or "parent".
type ModulesTelegram struct {
	ThreadFallback string `toml:"thread_fallback"`
}

// GatewayUser is one identity's gateway overrides; Workspace empty means the
// identity's derived sandbox root.
type GatewayUser struct {
	Workspace string `toml:"workspace"`
}

// Providers holds the SPEC §12 [[providers.custom]] array. It is accepted and
// validated but has no runtime consumer until F5 (GAP-SEC-24).
type Providers struct {
	Custom []CustomProvider `toml:"custom"`
}

// CustomProvider is one PROVIDERS.md custom provider (12 fields). APIKey may
// only be a keychain reference; Streaming nil means true.
type CustomProvider struct {
	Name                  string            `toml:"name"`
	BaseURL               string            `toml:"base_url"`
	API                   string            `toml:"api"`
	APIKeyEnv             string            `toml:"api_key_env"`
	APIKey                string            `toml:"api_key"`
	KeyCmd                []string          `toml:"key_cmd"`
	Models                []CustomModel     `toml:"models"`
	ExtraHeaders          map[string]string `toml:"extra_headers"`
	ExtraBody             map[string]any    `toml:"extra_body"`
	TLS                   *CustomTLS        `toml:"tls"`
	SessionAffinityHeader string            `toml:"session_affinity_header"`
	Streaming             *bool             `toml:"streaming"`
	// Override lets this entry replace a built-in catalog id (SEC-24); a
	// collision without it is a startup error.
	Override bool `toml:"override" json:"override"`
}

// CustomModel is one model of a custom provider.
type CustomModel struct {
	ID            string   `toml:"id"`
	ContextLength int      `toml:"context_length"`
	MaxTokens     int      `toml:"max_tokens"`
	Capabilities  []string `toml:"capabilities"`
}

// CustomTLS is the optional transport security block of a custom provider.
// SSLVerify nil means true.
type CustomTLS struct {
	SSLVerify *bool  `toml:"ssl_verify"`
	SSLCACert string `toml:"ssl_ca_cert"`
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

// MarshalText renders the same string form, so `config show` emits "24h"
// instead of the underlying nanosecond integer.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// DefaultGatewayUserDailyUSD is the SPEC §18.4 cap for one role user gateway
// identity: USD per UTC day.
const DefaultGatewayUserDailyUSD = 1.00

// Default returns the SPEC §12 defaults for every key exposed in F1.
func Default() Config {
	gateway, modules := gatewayDefaults()
	return Config{
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
			TurnUSD:    0.50,
			SessionUSD: 5.00,
			DailyUSD:   0,
			MonthlyUSD: 0,
			WarnAt:     0.80,
			Providers:  map[string]float64{},
			// SPEC §12 defers these to GAP-COST-11: 1,00 / 5,00.
			ToolsSessionUSD: 1.00, ToolsDailyUSD: 5.00,
			GatewayUserDailyUSD: DefaultGatewayUserDailyUSD,
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
			Backend:          "local",
			Backends:         defaultExecBackends(),
		},
		Agent:   Agent{VerifyOnStop: "run"},
		Storage: Storage{Encrypt: false},
		// The privacy-safe default: background egress is allowed (the daily
		// update check in serve), offline mode is off.
		Network: Network{Background: true},
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
		Context: Context{
			CompactAt:       0.75,
			SummaryMaxTok:   600,
			SummariesMaxTok: 3000,
			PinnedSchemaTok: 3000,
		},
		Memory: Memory{
			EntryMaxChars: 500,
			TotalMaxChars: 4000,
			Embeddings:    "",
		},
		Recovery:  Recovery{Continue: "safe_auto"},
		UI:        UI{Language: "auto", Theme: "tern-dark", Notices: true},
		Gateway:   gateway,
		Modules:   modules,
		Providers: Providers{Custom: []CustomProvider{}},
		Update:    update.DefaultConfig(), ACP: ACP{UseClientFS: true},
		Log: Log{
			Level:             "info",
			MessageContent:    false,
			MessageContentTTL: Duration(24 * time.Hour),
		},
	}
}

func gatewayDefaults() (Gateway, Modules) {
	return Gateway{MaxParallelTurns: 4, BusyPolicy: "queue", Users: map[string]GatewayUser{}},
		defaultModules()
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
		"<config_dir>/at-rest.*",
		"<config_dir>/backup.keys",
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
	out.Skills.TrustedProjects = slices.Clone(c.Skills.TrustedProjects)
	out.Skills.ExternalDirs = slices.Clone(c.Skills.ExternalDirs)
	out.Gateway.Users = maps.Clone(c.Gateway.Users)
	out.Providers.Custom = cloneCustomProviders(c.Providers.Custom)
	out.Toolsets = cloneToolsets(c.Toolsets)
	return out
}

func cloneCustomProviders(in []CustomProvider) []CustomProvider {
	if in == nil {
		return nil
	}
	out := make([]CustomProvider, len(in))
	for i := range in {
		out[i] = in[i]
		p := &out[i]
		p.KeyCmd = slices.Clone(in[i].KeyCmd)
		p.ExtraHeaders = maps.Clone(in[i].ExtraHeaders)
		p.ExtraBody = cloneAnyMap(in[i].ExtraBody)
		p.Models = slices.Clone(in[i].Models)
		for j := range p.Models {
			p.Models[j].Capabilities = slices.Clone(in[i].Models[j].Capabilities)
		}
		if p.TLS != nil {
			tls := *p.TLS
			if tls.SSLVerify != nil {
				v := *tls.SSLVerify
				tls.SSLVerify = &v
			}
			p.TLS = &tls
		}
		if p.Streaming != nil {
			v := *p.Streaming
			p.Streaming = &v
		}
	}
	return out
}

func cloneAnyMap(in map[string]any) map[string]any {
	if in == nil {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		out[k] = cloneAny(v)
	}
	return out
}

func cloneAny(v any) any {
	switch x := v.(type) {
	case map[string]any:
		return cloneAnyMap(x)
	case []any:
		out := make([]any, len(x))
		for i := range x {
			out[i] = cloneAny(x[i])
		}
		return out
	case []map[string]any:
		out := make([]map[string]any, len(x))
		for i := range x {
			out[i] = cloneAnyMap(x[i])
		}
		return out
	default:
		return v
	}
}

func cloneToolsets(in toolset.Config) toolset.Config {
	out := in
	out.Active = slices.Clone(in.Active)
	out.Disabled = slices.Clone(in.Disabled)
	if in.Platform != nil {
		out.Platform = make(map[string]toolset.Override, len(in.Platform))
		for os, ov := range in.Platform {
			ov.Active, ov.Disabled = slices.Clone(ov.Active), slices.Clone(ov.Disabled)
			out.Platform[os] = ov
		}
	}
	if in.Custom != nil {
		out.Custom = make(map[string]toolset.Definition, len(in.Custom))
		for name, d := range in.Custom {
			d.Tools, d.Includes = slices.Clone(d.Tools), slices.Clone(d.Includes)
			out.Custom[name] = d
		}
	}
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
