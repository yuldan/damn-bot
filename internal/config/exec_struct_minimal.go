//go:build minimal

package config

// Exec is SPEC §12 [exec] (ADR-072, ADR-079, ADR-081) plus the SEC-02 keys
// exec.env_allow, exec.env and exec.pass_ssh_agent, and the F5 backend
// selection (ADR-277): exec.backend plus [exec.backends.<id>]. The minimal
// build has no exec.code_auto_languages key.
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
