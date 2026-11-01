//go:build minimal

package config

// ExecBackends is intentionally empty in the minimal build: the remote
// backend configuration is not linked (P13 minimal ≤ 22.000.000 B) and an
// unknown [exec.backends.*] key is rejected by the strict decoder.
type ExecBackends struct{}

// defaultExecBackends has no remote backend in the minimal build.
func defaultExecBackends() ExecBackends { return ExecBackends{} }

// validateExecBackends accepts only local; a remote id fails closed.
func (c *Config) validateExecBackends() error {
	switch c.Exec.Backend {
	case "", "local":
		return nil
	default:
		return invalid("exec.backend", "must be local in the minimal build")
	}
}

func validateExecCode(*Config) error { return nil }

// defaultExec is the SPEC §12 [exec] block without exec.code_auto_languages.
func defaultExec() Exec {
	return Exec{
		DefaultTimeoutS:  120,
		IdleTimeoutS:     60,
		AllowDestructive: false,
		KillGraceS:       3,
		EnvAllow:         []string{},
		Env:              map[string]string{},
		PassSSHAgent:     false,
		Backend:          "local",
		Backends:         defaultExecBackends(),
	}
}
