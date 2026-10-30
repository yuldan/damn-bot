package config

import (
	"fmt"
	"maps"
	"math"
	"net/url"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Validate applies every SPEC §12 range/format check to c. It is pure: it
// never mutates c and returns a [*Error] naming the offending key.
func (c *Config) Validate() error {
	if c.ConfigVersion < 0 || c.ConfigVersion > SchemaVersion {
		return &Error{Kind: ErrVersion, Key: "config_version", Msg: fmt.Sprintf("config_version %d is outside the supported range 0..%d", c.ConfigVersion, SchemaVersion)}
	}
	checks := []func() error{
		c.validateProvider,
		c.validateLoop,
		c.validateBudget,
		c.validateApproval,
		c.validateExec,
		c.validateAgent,
		c.validateSkills,
		c.validateSessions,
		c.validateContext,
		c.validateMemory,
		c.validateRecovery,
		c.validateUI,
		c.validateGateway,
		c.validateProviders,
		c.validateLog,
		c.validateToolsets,
		c.Update.Validate,
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
		{"budget.tools_session_usd", c.Budget.ToolsSessionUSD},
		{"budget.tools_daily_usd", c.Budget.ToolsDailyUSD},
	}
	for _, cap := range caps {
		if !validUSD(cap.v) {
			return invalid(cap.key, "must be a finite value >= 0")
		}
	}
	if !validUSD(c.Budget.WarnAt) || c.Budget.WarnAt <= 0 || c.Budget.WarnAt > 1 {
		return invalid("budget.warn_at", "must be in (0, 1]")
	}
	if v := c.Budget.GatewayUserDailyUSD; !validUSD(v) || v <= 0 || v > 100 {
		return invalid("budget.gateway_user_daily_usd", "must be a finite value in (0, 100]")
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

func (c *Config) validateContext() error {
	if !(c.Context.CompactAt > 0 && c.Context.CompactAt < 1) {
		return invalid("context.compact_at", "must be in (0, 1)")
	}
	if c.Context.SummaryMaxTok <= 0 {
		return invalid("context.summary_max_tok", "must be > 0")
	}
	if c.Context.SummariesMaxTok < c.Context.SummaryMaxTok {
		return invalid("context.summaries_max_tok", "must be >= context.summary_max_tok")
	}
	if c.Context.PinnedSchemaTok < 0 {
		return invalid("context.pinned_schema_tok", "must be >= 0")
	}
	return nil
}

func (c *Config) validateMemory() error {
	if c.Memory.TotalMaxChars <= 0 {
		return invalid("memory.total_max_chars", "must be > 0")
	}
	if c.Memory.EntryMaxChars <= 0 || c.Memory.EntryMaxChars > c.Memory.TotalMaxChars {
		return invalid("memory.entry_max_chars", "must be in 1..memory.total_max_chars")
	}
	if c.Memory.Embeddings != "" && !validModelRef(c.Memory.Embeddings) {
		return invalid("memory.embeddings", "not a provider/model reference")
	}
	return nil
}

func (c *Config) validateRecovery() error {
	switch c.Recovery.Continue {
	case "off", "ask", "safe_auto":
		return nil
	default:
		return invalid("recovery.continue", "must be off, ask or safe_auto")
	}
}

func (c *Config) validateUI() error {
	if !validLanguage(c.UI.Language) {
		return invalid("ui.language", "%q is not auto or a §9.12 language code", c.UI.Language)
	}
	if !validTheme(c.UI.Theme) {
		return invalid("ui.theme", "must be tern-dark, tern-light, mono or high-contrast")
	}
	return nil
}

func (c *Config) validateGateway() error {
	if c.Gateway.MaxParallelTurns < 1 || c.Gateway.MaxParallelTurns > 16 {
		return invalid("gateway.max_parallel_turns", "must be in 1..16")
	}
	for _, id := range slices.Sorted(maps.Keys(c.Gateway.Users)) {
		if !validIdentity(id) {
			return invalid("gateway.users", "identity %q must match ^[A-Za-z0-9][A-Za-z0-9_.:@-]{0,127}$", id)
		}
		if !validWorkspace(c.Gateway.Users[id].Workspace) {
			return invalid("gateway.users."+id+".workspace", "must be empty or an absolute path")
		}
	}
	return nil
}

func (c *Config) validateProviders() error {
	names := make(map[string]struct{}, len(c.Providers.Custom))
	for i := range c.Providers.Custom {
		if err := validateCustomProvider(&c.Providers.Custom[i], i, names); err != nil {
			return err
		}
	}
	return nil
}

func validateCustomProvider(p *CustomProvider, i int, names map[string]struct{}) error {
	key := "providers.custom[" + strconv.Itoa(i) + "]"
	if !validProviderName(p.Name) {
		return invalid(key+".name", "must match ^[a-z0-9][a-z0-9_-]{0,63}$")
	}
	if _, dup := names[p.Name]; dup {
		return invalid(key+".name", "duplicate provider name %q", p.Name)
	}
	names[p.Name] = struct{}{}
	if !validBaseURL(p.BaseURL) {
		return invalid(key+".base_url", "must be an http(s) URL with a host and no userinfo, query or fragment")
	}
	if !validCustomAPI(p.API) {
		return invalid(key+".api", "must be openai-chat, openai-responses, anthropic-messages or gemini")
	}
	if err := validateProviderAuth(p, key); err != nil {
		return err
	}
	if err := validateCustomModels(p.Models, key); err != nil {
		return err
	}
	if err := validateCustomHeaders(p.ExtraHeaders, key); err != nil {
		return err
	}
	return validateCustomTLS(p.TLS, key)
}

func validCustomAPI(api string) bool {
	return api == "" || slices.Contains([]string{"openai-chat", "openai-responses", "anthropic-messages", "gemini"}, api)
}

func validateProviderAuth(p *CustomProvider, key string) error {
	sources := 0
	if p.APIKeyEnv != "" {
		sources++
		if !validKeyEnvName(p.APIKeyEnv) {
			return invalid(key+".api_key_env", "must match ^[A-Z_][A-Z0-9_]*$")
		}
	}
	if p.APIKey != "" {
		sources++
		if !validKeyRef(p.APIKey) {
			return invalid(key+".api_key", "must be a keychain:<id> reference, never a plaintext key")
		}
	}
	if len(p.KeyCmd) > 0 {
		sources++
		if slices.Contains(p.KeyCmd, "") {
			return invalid(key+".key_cmd", "entries must be non-empty")
		}
	}
	if sources > 1 {
		return invalid(key+".api_key", "at most one of api_key_env, api_key and key_cmd may be set")
	}
	return nil
}

func validateCustomModels(models []CustomModel, key string) error {
	if len(models) == 0 {
		return invalid(key+".models", "must list at least one model")
	}
	seen := make(map[string]struct{}, len(models))
	for j, m := range models {
		mk := key + ".models[" + strconv.Itoa(j) + "]"
		if m.ID == "" {
			return invalid(mk+".id", "must not be empty")
		}
		if _, dup := seen[m.ID]; dup {
			return invalid(mk+".id", "duplicate model id %q", m.ID)
		}
		seen[m.ID] = struct{}{}
		if m.ContextLength < 0 || m.MaxTokens < 0 {
			return invalid(mk, "context_length and max_tokens must be >= 0")
		}
		for _, capability := range m.Capabilities {
			if !validCapability(capability) {
				return invalid(mk+".capabilities", "%q is not tools, vision or reasoning", capability)
			}
		}
	}
	return nil
}

func validateCustomHeaders(headers map[string]string, key string) error {
	for _, name := range slices.Sorted(maps.Keys(headers)) {
		if !validHeaderName(name) {
			return invalid(key+".extra_headers", "%q is not an RFC 7230 header name", name)
		}
		if strings.ContainsAny(headers[name], "\r\n") {
			return invalid(key+".extra_headers."+name, "value contains a line break")
		}
	}
	return nil
}

func validateCustomTLS(tls *CustomTLS, key string) error {
	if tls == nil {
		return nil
	}
	if tls.SSLVerify == nil && tls.SSLCACert == "" {
		return invalid(key+".tls", "must set ssl_verify or ssl_ca_cert")
	}
	if strings.ContainsAny(tls.SSLCACert, "\x00\r\n") {
		return invalid(key+".tls.ssl_ca_cert", "contains NUL or a line break")
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

// validLanguage accepts "auto" and the 18 SPEC §9.12 language codes.
func validLanguage(s string) bool {
	codes := [...]string{"af", "ar", "de", "en", "es", "fr", "ga", "hu", "it", "ja", "ko", "pt", "ru", "tr", "uk", "zh", "zh-hant", "id"}
	return s == "auto" || slices.Contains(codes[:], s)
}

// validTheme accepts the four SPEC §13.1 themes.
func validTheme(s string) bool {
	return slices.Contains([]string{"tern-dark", "tern-light", "mono", "high-contrast"}, s)
}

// validIdentity is the gateway user id grammar of SPEC §12.
func validIdentity(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := range len(id) {
		c := id[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '.' || c == ':' || c == '@' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// validWorkspace accepts an empty override, an absolute POSIX/Windows path, or
// a "~/" / "~\" path. Relative paths are rejected: the root must not depend on
// the process cwd (SPEC §9.7, §14.2).
func validWorkspace(p string) bool {
	switch {
	case p == "":
		return true
	case strings.ContainsAny(p, "\x00\r\n"):
		return false
	case strings.HasPrefix(p, "~/"), strings.HasPrefix(p, `~\`),
		strings.HasPrefix(p, "/"), strings.HasPrefix(p, `\\`):
		return true
	}
	letter := p[0] >= 'A' && p[0] <= 'Z' || p[0] >= 'a' && p[0] <= 'z'
	return len(p) >= 3 && letter && p[1] == ':' && (p[2] == '/' || p[2] == '\\')
}

// validProviderName is the custom provider id grammar of PROVIDERS.md.
func validProviderName(s string) bool {
	if s == "" || len(s) > 64 {
		return false
	}
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case i > 0 && (c == '_' || c == '-'):
		default:
			return false
		}
	}
	return true
}

// validBaseURL is an http(s) origin with a host; credentials in the URL, a
// query and a fragment are rejected (ADR-194 amandemen SEC6-17).
func validBaseURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != "" && u.User == nil && u.RawQuery == "" && u.Fragment == ""
}

// validKeyEnvName is the api_key_env grammar of SPEC §12.
func validKeyEnvName(s string) bool {
	return validEnvName(s) && s == strings.ToUpper(s)
}

// validKeyRef accepts only a keychain reference, never a plaintext secret.
func validKeyRef(s string) bool {
	id, ok := strings.CutPrefix(s, "keychain:")
	return ok && id != "" && !strings.ContainsAny(id, " \t\r\n\x00")
}

// validCapability is the CustomModel capability enum of PROVIDERS.md.
func validCapability(s string) bool {
	return slices.Contains([]string{"tools", "vision", "reasoning"}, s)
}

// validHeaderName is the RFC 7230 token grammar.
func validHeaderName(s string) bool {
	if s == "" {
		return false
	}
	const punctuation = "!#$%&'*+-.^_`|~"
	for i := range len(s) {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9':
		case strings.ContainsRune(punctuation, rune(c)):
		default:
			return false
		}
	}
	return true
}
