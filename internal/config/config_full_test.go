//go:build !minimal

package config

import (
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/update"
	"github.com/google/go-cmp/cmp"
)

// specExecDefaults mirrors the [exec] block of SPEC §12 field by field;
// duplicating the production literal here is the point of the test.
func specExecDefaults() Exec {
	e := Exec{
		DefaultTimeoutS:  120,
		IdleTimeoutS:     60,
		AllowDestructive: false,
		KillGraceS:       3,
		EnvAllow:         []string{},
		Env:              map[string]string{},
		PassSSHAgent:     false,
		Backend:          "local",
		Backends: ExecBackends{
			Docker: DockerExec{
				Binary: "docker", User: "65534:65534", CPUMilli: 1000,
				MemoryMiB: 512, PIDs: 128, Network: "none",
			},
			SSH: SSHExec{Port: 22, KeepAliveS: 30, IdleCloseS: 600, TimeoutS: 120, MaxConns: 4},
			Singularity: SingularityExec{
				Binary: "apptainer", CPUMilli: 1000, MemoryMiB: 512, PIDs: 128, Network: "none",
			},
			Modal:         SandboxExec{TimeoutS: 300},
			Daytona:       SandboxExec{TimeoutS: 300},
			VercelSandbox: SandboxExec{TimeoutS: 300},
		},
	}
	fillExecCode(&e)
	return e
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
			TurnUSD:             0.50,
			SessionUSD:          5.00,
			DailyUSD:            0,
			MonthlyUSD:          0,
			WarnAt:              0.80,
			Providers:           map[string]float64{},
			ToolsSessionUSD:     1.00,
			ToolsDailyUSD:       5.00,
			GatewayUserDailyUSD: 1.00,
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
		Exec:    specExecDefaults(),
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
		UI:        UI{Language: "auto", Theme: "tern-dark", Notices: true},
		Gateway:   Gateway{MaxParallelTurns: 4, BusyPolicy: "queue", Users: map[string]GatewayUser{}},
		Modules:   Modules{Gateway: ModulesGateway{Telegram: ModulesTelegram{ThreadFallback: "fail"}}},
		Providers: Providers{Custom: []CustomProvider{}},
		Log:       Log{Level: "info", MessageContent: false, MessageContentTTL: Duration(24 * time.Hour)},
		Update:    update.DefaultConfig(),
		ACP:       ACP{UseClientFS: true},
	}
	if diff := cmp.Diff(want, Default()); diff != "" {
		t.Errorf("Default() differs from SPEC §12 (-want +got):\n%s", diff)
	}
}

// TestExecBackendSelection: a selected backend with missing or unsafe
// configuration is rejected; local stays the default and needs nothing.
func TestExecBackendSelection(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		edit func(*Config)
		key  string
		ok   bool
	}{
		{"default local", func(*Config) {}, "", true},
		{"explicit local", func(c *Config) { c.Exec.Backend = "local" }, "", true},

		{"unknown backend", func(c *Config) { c.Exec.Backend = "firecracker" }, "exec.backend", false},
		{"docker without image", func(c *Config) { c.Exec.Backend = "docker" }, "exec.backends.docker.image", false},
		{"docker no network", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Docker.Image = "docker", "img"
			c.Exec.Backends.Docker.Network = "host"
		}, "exec.backends.docker.network", false},
		{"docker allowlist without network", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Docker.Image = "docker", "img"
			c.Exec.Backends.Docker.Network = "allowlist"
			c.Exec.Backends.Docker.EgressAllow = []string{"proxy.golang.org:443"}
			c.Exec.Backends.Docker.ProxyURL = "http://127.0.0.1:8080"
		}, "exec.backends.docker.network_name", false},
		{"ssh without known_hosts", func(c *Config) {
			c.Exec.Backend = "ssh"
			c.Exec.Backends.SSH.Host, c.Exec.Backends.SSH.User = "example.test", "dev"
		}, "exec.backends.ssh.known_hosts", false},
		{"ssh ok", func(c *Config) {
			c.Exec.Backend = "ssh"
			c.Exec.Backends.SSH.Host, c.Exec.Backends.SSH.User = "example.test", "dev"
			c.Exec.Backends.SSH.KnownHosts = "/home/dev/.ssh/known_hosts"
		}, "", true},
		{"docker ok", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Docker.Image = "docker", "img"
		}, "", true},
		{"singularity without image", func(c *Config) { c.Exec.Backend = "singularity" }, "exec.backends.singularity.image", false},
		{"singularity ok", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Singularity.Image = "singularity", "img.sif"
		}, "", true},
		{"modal without endpoint", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Modal.Token = "modal", "modal_key"
		}, "exec.backends.modal.endpoint", false},
		{"modal without token", func(c *Config) { c.Exec.Backend = "modal" }, "exec.backends.modal.token", false},
		{"modal bad token name", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Modal.Token = "modal", "not a name"
		}, "exec.backends.modal.token", false},
		{"modal ok", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Modal.Token = "modal", "modal_key"
			c.Exec.Backends.Modal.Endpoint = "https://sandbox.example.test"
		}, "", true},
		{"daytona ok default endpoint", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.Daytona.Token = "daytona", "daytona_key"
		}, "", true},
		{"vercel without runtime", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.VercelSandbox.Token = "vercel_sandbox", "vercel_key"
		}, "exec.backends.vercel_sandbox.runtime", false},
		{"vercel http remote endpoint", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.VercelSandbox.Token = "vercel_sandbox", "vercel_key"
			c.Exec.Backends.VercelSandbox.Runtime = "node24"
			c.Exec.Backends.VercelSandbox.Endpoint = "http://sandbox.example.test"
		}, "exec.backends.vercel_sandbox.endpoint", false},
		{"vercel ok loopback http", func(c *Config) {
			c.Exec.Backend, c.Exec.Backends.VercelSandbox.Token = "vercel_sandbox", "vercel_key"
			c.Exec.Backends.VercelSandbox.Runtime = "node24"
			c.Exec.Backends.VercelSandbox.Endpoint = "http://127.0.0.1:8080"
		}, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := Default()
			tc.edit(&cfg)
			err := cfg.Validate()
			switch {
			case tc.ok && err != nil:
				t.Fatalf("Validate = %v, want nil", err)
			case !tc.ok && (err == nil || !strings.Contains(err.Error(), tc.key)):
				t.Fatalf("Validate = %v, want %s", err, tc.key)
			}
		})
	}
}
