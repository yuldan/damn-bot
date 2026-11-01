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
