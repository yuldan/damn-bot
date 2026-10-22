package config

import (
	"maps"
	"math"
	"net/url"
	"slices"
	"strings"
	"time"
)

// Validate applies every SPEC §12 range/format check to c. It is pure: it
// never mutates c and returns a [*Error] naming the offending key.
func (c *Config) Validate() error {
	checks := []func() error{
		c.validateProvider,
		c.validateProviders,
		c.validateLoop,
		c.validateBudget,
		c.validateApproval,
		c.validateExec,
		c.validateAgent,
		c.validateLog,
	}
	for _, check := range checks {
		if err := check(); err != nil {
			return err
		}
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

// minContextWindow is registry.MinWindow (ADR-126): a model below it cannot
// run a turn, so a config override below it is refused at load.
const (
	minContextWindow = 8_192
	maxContextWindow = 10_000_000
)

func (c *Config) validateProviders() error {
	names := make(map[string]bool, len(c.Providers.Custom))
	for i, p := range c.Providers.Custom {
		if err := validateCustomProvider(i, p, names); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomProvider(i int, p CustomProvider, names map[string]bool) error {
	switch {
	case p.Name == "" || len(p.Name) > 128 || strings.ContainsAny(p.Name, " \t\r\n\x00/"):
		return invalid("providers.custom.name", "entry %d is not a provider id", i)
	case names[p.Name]:
		return invalid("providers.custom.name", "duplicate %q", p.Name)
	case !validBaseURL(p.BaseURL):
		return invalid("providers.custom.base_url", "entry %q is not a plain http(s) origin+path", p.Name)
	case len(p.Models) == 0:
		return invalid("providers.custom.models", "entry %q has no models", p.Name)
	}
	names[p.Name] = true
	ids := make(map[string]bool, len(p.Models))
	for j, m := range p.Models {
		if err := validateCustomModel(p.Name, j, m, ids); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomModel(provider string, j int, m CustomModel, ids map[string]bool) error {
	switch {
	case m.ID == "" || len(m.ID) > 256 || strings.ContainsAny(m.ID, " \t\r\n\x00"):
		return invalid("providers.custom.models", "%q model %d has an invalid id", provider, j)
	case ids[m.ID]:
		return invalid("providers.custom.models", "%q has duplicate model %q", provider, m.ID)
	case m.ContextLength != 0 && (m.ContextLength < minContextWindow || m.ContextLength > maxContextWindow):
		return invalid("providers.custom.models", "%q/%s context_length must be in %d..%d", provider, m.ID, minContextWindow, maxContextWindow)
	case m.MaxTokens < 0:
		return invalid("providers.custom.models", "%q/%s max_tokens must be >= 0", provider, m.ID)
	}
	ids[m.ID] = true
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
// validBaseURL matches the pricing normalisation: http(s), a host, no
// userinfo/query/fragment (the composition root normalises it again).
func validBaseURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		return true
	}
	return false
}

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
