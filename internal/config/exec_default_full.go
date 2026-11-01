//go:build !minimal

package config

// defaultExec is the SPEC §12 [exec] block plus the build-specific
// exec.code_auto_languages key (ADR-296/298).
func defaultExec() Exec {
	return Exec{
		DefaultTimeoutS:   120,
		IdleTimeoutS:      60,
		AllowDestructive:  false,
		KillGraceS:        3,
		EnvAllow:          []string{},
		Env:               map[string]string{},
		PassSSHAgent:      false,
		Backend:           "local",
		Backends:          defaultExecBackends(),
		CodeAutoLanguages: []string{},
	}
}
