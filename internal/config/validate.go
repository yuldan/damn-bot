package config

import (
	"maps"
	"math"
	"path/filepath"
	"slices"
	"strings"
	"time"
)

// Validate applies every SPEC §12 range/format check to c. It is pure: it
// never mutates c and returns a [*Error] naming the offending key.
func (c *Config) Validate() error {
	checks := []func() error{
		c.validateProvider,
		c.validateLoop,
		c.validateBudget,
		c.validateApproval,
		c.validateExec,
		c.validateAgent,
		c.validateSkills,
		c.validateSessions,
		c.validateLog,
		c.validateToolsets,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
	}
	return nil
}

func (c *Config) validateToolsets() error {
	if err := c.Toolsets.Validate(); err != nil {
		return invalid("toolsets", "%v", err)
	}
	return nil
}

func (c *Config) validateProvider() error {
	if c.Provider.MaxOutput <= 0 {
		return invalid("provider.max_output", "must be > 0")
	}
	if len(c.Provider.Fallback) > 2 {
		return invalid("provider.fallback", "must contain at most 2 models")
	}
	for i, ref := range c.Provider.Fallback {
		if !validModelRef(ref) {
			return invalid("provider.fallback", "entry %d is not a provider/model reference", i)
		}
		if slices.Contains(c.Provider.Fallback[:i], ref) {
			return invalid("provider.fallback", "duplicate entry %q", ref)
		}
	}
	if c.Provider.Light != "" && !validModelRef(c.Provider.Light) {
		return invalid("provider.light", "not a provider/model reference")
	}
	return nil
}

func (c *Config) validateLoop() error {
	switch {
	case c.Loop.MaxIterations < 1 || c.Loop.MaxIterations > 40:
		return invalid("loop.max_iterations", "must be in 1..40")
	case c.Loop.MaxWallS <= 0:
		return invalid("loop.max_wall_s", "must be > 0")
	case c.Loop.MaxIdentical < 2 || c.Loop.MaxIdentical > 5:
		return invalid("loop.max_identical", "must be in 2..5")
	case c.Loop.TurnMaxInputTok <= 0 || c.Loop.TurnMaxInputTok > 400000:
		return invalid("loop.turn_max_input_tok", "must be in 1..400000")
	case c.Loop.TurnMaxOutputTok <= 0 || c.Loop.TurnMaxOutputTok > 32000:
		return invalid("loop.turn_max_output_tok", "must be in 1..32000")
	case c.Loop.ParallelTools <= 0:
		return invalid("loop.parallel_tools", "must be > 0")
	}
	return nil
}

func (c *Config) validateBudget() error {
	caps := []struct {
		key string
		v   float64
	}{
		{"budget.turn_usd", c.Budget.TurnUSD},
		{"budget.session_usd", c.Budget.SessionUSD},
		{"budget.daily_usd", c.Budget.DailyUSD},
		{"budget.monthly_usd", c.Budget.MonthlyUSD},
	}
	for _, cap := range caps {
		if !validUSD(cap.v) {
			return invalid(cap.key, "must be a finite value >= 0")
		}
	}
	if !validUSD(c.Budget.WarnAt) || c.Budget.WarnAt <= 0 || c.Budget.WarnAt > 1 {
		return invalid("budget.warn_at", "must be in (0, 1]")
	}
	for _, name := range slices.Sorted(maps.Keys(c.Budget.Providers)) {
		if name == "" || strings.ContainsAny(name, " \t\r\n") {
			return invalid("budget.providers", "provider name %q is empty or contains whitespace", name)
		}
		if !validUSD(c.Budget.Providers[name]) {
			return invalid("budget.providers."+name, "must be a finite value >= 0")
		}
	}
	return nil
}

func (c *Config) validateApproval() error {
	switch c.Approval.Mode {
	case "ask", "auto-edit", "auto":
	default:
		return invalid("approval.mode", "must be ask, auto-edit or auto")
	}
	for i, argv := range c.Approval.ShellAllow {
		if len(argv) == 0 {
			return invalid("approval.shell_allow", "entry %d is empty", i)
		}
		for j, token := range argv {
			if token == "" || strings.ContainsAny(token, " \t\r\n\x00") {
				return invalid("approval.shell_allow", "entry %d token %d is empty or contains whitespace/NUL", i, j)
			}
		}
	}
	for _, p := range c.Approval.DenyPaths {
		if !validPathPattern(p) {
			return invalid("approval.deny_paths", "path %q is empty or contains NUL", p)
		}
	}
	for _, p := range c.Approval.AddDirs {
		if p == "" || strings.ContainsRune(p, '\x00') {
			return invalid("approval.add_dirs", "path is empty or contains NUL")
		}
	}
	if c.Approval.PreviewTimeoutS <= 0 {
		return invalid("approval.preview_timeout_s", "must be > 0")
	}
	return nil
}

func (c *Config) validateExec() error {
	switch {
	case c.Exec.DefaultTimeoutS < 1 || c.Exec.DefaultTimeoutS > 600:
		return invalid("exec.default_timeout_s", "must be in 1..600")
	case c.Exec.IdleTimeoutS < 1 || c.Exec.IdleTimeoutS > 600:
		return invalid("exec.idle_timeout_s", "must be in 1..600")
	case c.Exec.KillGraceS < 1 || c.Exec.KillGraceS > 3:
		return invalid("exec.kill_grace_s", "must be in 1..3")
	}
	for _, name := range c.Exec.EnvAllow {
		if !validEnvName(name) {
			return invalid("exec.env_allow", "%q is not an environment variable name", name)
		}
	}
	for name, value := range c.Exec.Env {
		if !validEnvName(name) {
			return invalid("exec.env", "%q is not an environment variable name", name)
		}
		if strings.ContainsRune(value, '\x00') {
			return invalid("exec.env."+name, "value contains NUL")
		}
	}
	return nil
}

func (c *Config) validateAgent() error {
	switch c.Agent.VerifyOnStop {
	case "off", "run", "repair":
		return nil
	default:
		return invalid("agent.verify_on_stop", "must be off, run or repair")
	}
}

func (c *Config) validateSkills() error {
	for i, p := range c.Skills.TrustedProjects {
		if err := validDirPath("skills.trusted_projects", i, p); err != nil {
			return err
		}
	}
	for i, p := range c.Skills.ExternalDirs {
		if err := validDirPath("skills.external_dirs", i, p); err != nil {
			return err
		}
	}
	return nil
}

// validDirPath requires an absolute path (or "~" form) so trust and external
// roots never depend on the process cwd.
func validDirPath(key string, i int, p string) error {
	if p == "" || len(p) > 4096 {
		return invalid(key, "entry %d must be a non-empty path", i)
	}
	if strings.ContainsAny(p, "\x00\n\r") || (!filepath.IsAbs(p) && p[0] != '~') {
		return invalid(key, "entry %d must be absolute", i)
	}
	return nil
}

func (c *Config) validateSessions() error {
	if c.Sessions.PruneAfterDays < 1 || c.Sessions.PruneAfterDays > 3650 {
		return invalid("sessions.prune_after_days", "must be in 1..3650")
	}
	return nil
}

func (c *Config) validateLog() error {
	switch c.Log.Level {
	case "debug", "info", "warn", "error":
	default:
		return invalid("log.level", "must be debug, info, warn or error")
	}
	ttl := c.Log.MessageContentTTL.Std()
	if ttl <= 0 || ttl > 72*time.Hour {
		return invalid("log.message_content_ttl", "must be in (0, 72h]")
	}
	return nil
}

func invalid(key, format string, args ...any) *Error {
	return keyError(ErrInvalidValue, key, format, args...)
}

// validModelRef accepts "provider/model" (openrouter-style multi-segment
// models are allowed) without whitespace or control bytes.
func validModelRef(s string) bool {
	if s == "" || len(s) > 512 || !strings.Contains(s, "/") {
		return false
	}
	return !strings.ContainsAny(s, " \t\r\n\x00")
}

func validUSD(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0) && v >= 0
}

func validPathPattern(p string) bool {
	return p != "" && !strings.ContainsRune(p, '\x00')
}

// validEnvName is the POSIX name grammar (portable to cmd.exe).
func validEnvName(name string) bool {
	if name == "" || name[0] >= '0' && name[0] <= '9' {
		return false
	}
	for i := range len(name) {
		c := name[i]
		switch {
		case c == '_', c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z':
		case i > 0 && c >= '0' && c <= '9':
		default:
			return false
		}
	}
	return true
}
