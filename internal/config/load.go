package config

import (
	"errors"
	"os"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
)

// MaxFileSize bounds config.toml at 1 MiB. The complete core config is under
// 60 lines; a larger file is a mistake or an attack, not a profile.
const MaxFileSize = 1 << 20

// Deps is the explicit dependency set of a [Loader] (E-11). Tests inject
// in-memory readers and fake environments; the composition root that first
// consumes config supplies the OS functions (the production constructor is
// added with that consumer so no exported symbol is dead).
type Deps struct {
	// ReadFile reads one file. Required.
	ReadFile func(name string) ([]byte, error)
	// Getenv reads an environment variable. Required; this package is the
	// only internal package allowed to call os.Getenv (OPS-01).
	Getenv func(key string) string
	// HomeDir resolves the user's home directory. Required for [Loader.Paths].
	HomeDir func() (string, error)
	// GOOS selects the path convention; empty means runtime.GOOS.
	GOOS string
}

// OS returns the production dependencies. It is added with its first
// consumer, the composition root's headless run command.
func OS() Deps {
	return Deps{
		ReadFile: os.ReadFile,
		Getenv:   os.Getenv,
		HomeDir:  os.UserHomeDir,
		GOOS:     runtime.GOOS,
	}
}

// Loader loads config files with injected I/O. It is immutable and safe for
// concurrent use.
type Loader struct {
	deps Deps
}

// New validates deps and returns a Loader. A missing dependency is
// [ErrDeps]; it never falls back to production I/O silently (E-11).
func New(deps Deps) (*Loader, error) {
	if deps.ReadFile == nil {
		return nil, keyError(ErrDeps, "", "Deps.ReadFile is nil")
	}
	if deps.Getenv == nil {
		return nil, keyError(ErrDeps, "", "Deps.Getenv is nil")
	}
	if deps.HomeDir == nil {
		return nil, keyError(ErrDeps, "", "Deps.HomeDir is nil")
	}
	if deps.GOOS == "" {
		deps.GOOS = runtime.GOOS
	}
	return &Loader{deps: deps}, nil
}

// Load reads and parses one TOML file. Read failures wrap [ErrRead]; parse,
// unknown-key, too-large and validation failures keep their own sentinels.
// The environment override is applied separately by [Loader.Effective], so a
// stale variable never silently beats the file (EC-CFG-001).
func (l *Loader) Load(path string) (Config, error) {
	data, err := l.deps.ReadFile(path)
	if err != nil {
		return Config{}, &Error{Kind: ErrRead, Cause: err, Key: path, Msg: "cannot read file"}
	}
	return l.Parse(data, path)
}

// Effective applies the generated DAMNBOT_<SECTION>_<KEY> overrides to cfg
// (GAP-OPS-01). It is separate from [Loader.Parse] so `config edit` validates
// the file, not the environment.
func (l *Loader) Effective(cfg *Config) error { return l.applyEnv(cfg) }

// Env exposes one overridden variable for `config explain` provenance
// without letting callers read the environment directly (GAP-OPS-01).
func (l *Loader) Env(key string) string { return l.deps.Getenv(key) }

// Parse decodes data over the SPEC §12 defaults, rejects unknown keys and
// values outside their range, and returns the frozen config.
func (l *Loader) Parse(data []byte, source string) (Config, error) {
	if len(data) > MaxFileSize {
		return Config{}, keyError(ErrTooLarge, source, "file is larger than %d bytes", MaxFileSize)
	}
	cfg := Default()
	md, err := toml.Decode(string(data), &cfg)
	if err != nil {
		return Config{}, syntaxError(source, err)
	}
	if key, ok := firstUnknown(&md); ok {
		kind, msg := ErrUnknownKey, "key is not part of the config"
		if isModuleKey(key) {
			kind = ErrModuleUnavailable
			msg = "module is not compiled in this build (modules arrive in F4/F5)"
		}
		return Config{}, &Error{
			Kind: kind,
			Key:  key.String(),
			Line: findKeyLine(data, key),
			Msg:  msg,
		}
	}
	cfg.normalize()
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// normalize applies the SPEC §12 defaults that TOML decoding cannot express
// without a pointer or a second pass. It runs on the local copy only, before
// [Config.Validate], which stays pure (E-13).
func (c *Config) normalize() {
	for i := range c.Providers.Custom {
		if c.Providers.Custom[i].API == "" {
			c.Providers.Custom[i].API = "openai-chat"
		}
	}
}

// isModuleKey reports a section owned by a module that this build does not
// compile in. Those get [ErrModuleUnavailable], not a typo message.
func isModuleKey(key toml.Key) bool {
	return len(key) > 0 && (key[0] == "modules" || key[0] == "obs" || key[0] == "cron")
}

// firstUnknown returns the first undecoded key that is not part of a free-form
// [[providers.custom]] extra_body table. BurntSushi does not mark nested keys
// inside map[string]any as decoded, so those are filtered explicitly.
func firstUnknown(md *toml.MetaData) (toml.Key, bool) {
	for _, key := range md.Undecoded() {
		if isExtraBodyKey(key) {
			continue
		}
		return key, true
	}
	return nil, false
}

func isExtraBodyKey(key toml.Key) bool {
	for i := 0; i+1 < len(key); i++ {
		if key[i] == "custom" && key[i+1] == "extra_body" {
			return true
		}
	}
	return false
}

// LoadProfile loads <config_dir>/profiles/<profile>/config.toml.
func (l *Loader) LoadProfile(profile string) (Config, error) {
	p, err := l.Paths(profile)
	if err != nil {
		return Config{}, err
	}
	return l.Load(p.ConfigFile())
}

func syntaxError(source string, err error) *Error {
	if pe, ok := errors.AsType[toml.ParseError](err); ok {
		key := pe.LastKey
		if key == "" {
			key = source
		}
		return &Error{Kind: ErrSyntax, Cause: err, Key: key, Line: pe.Position.Line, Msg: pe.Message}
	}
	// BurntSushi reports decode/type mismatches as plain errors whose text
	// still carries the key and line ("toml: line 2 (last key \"x\"): …").
	if key, line, msg, ok := decodeLocation(err.Error()); ok {
		return &Error{Kind: ErrInvalidValue, Cause: err, Key: key, Line: line, Msg: msg}
	}
	return &Error{Kind: ErrSyntax, Cause: err, Key: source, Msg: "invalid TOML"}
}

// decodeLocation extracts the key, line and message from a BurntSushi decode
// error. It never invents a key: an unrecognised shape returns ok=false.
func decodeLocation(text string) (key string, line int, msg string, ok bool) {
	m := regexp.MustCompile(`^toml: (?:line (\d+) )?\(last key "([^"]+)"\): (.+)$`).FindStringSubmatch(text)
	if m == nil {
		return "", 0, "", false
	}
	if n, err := strconv.Atoi(m[1]); err == nil {
		line = n
	}
	return m[2], line, m[3], true
}

// findKeyLine returns the 1-based line of the first occurrence of key, or 0
// when the key is a computed table and no single line can be named. The scan
// is intentionally small: it only understands section headers and
// "dotted.key = value" lines, which is what an unknown key otherwise is.
func findKeyLine(data []byte, want toml.Key) int {
	var section []string
	for i, raw := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(stripComment(raw))
		switch {
		case line == "":
			continue
		case strings.HasPrefix(line, "[["):
			section = splitKeyPath(strings.TrimSuffix(strings.TrimPrefix(line, "[["), "]]"))
			if slices.Equal(section, want) {
				return i + 1
			}
		case strings.HasPrefix(line, "["):
			section = splitKeyPath(strings.TrimSuffix(strings.TrimPrefix(line, "["), "]"))
			if slices.Equal(section, want) {
				return i + 1
			}
		default:
			left, _, ok := strings.Cut(line, "=")
			if !ok {
				continue
			}
			full := append(append([]string{}, section...), splitKeyPath(left)...)
			if slices.Equal(full, want) {
				return i + 1
			}
		}
	}
	return 0
}

// stripComment removes a trailing '#' that is not inside a basic or literal
// string. TOML escapes are irrelevant for key lines and are not interpreted.
func stripComment(line string) string {
	var (
		quote byte
		skip  bool
	)
	for i := range len(line) {
		c := line[i]
		switch {
		case skip:
			skip = false
		case quote != 0 && c == '\\' && quote == '"':
			skip = true
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			// inside a string, keep scanning
		case c == '"' || c == '\'':
			quote = c
		case c == '#':
			return line[:i]
		}
	}
	return line
}

// splitKeyPath splits "a.b.c" on dots outside quotes and removes quotes.
func splitKeyPath(s string) []string {
	var (
		parts []string
		cur   strings.Builder
		quote byte
		skip  bool
	)
	for i := range len(s) {
		c := s[i]
		switch {
		case skip:
			cur.WriteByte(c)
			skip = false
		case quote != 0 && c == '\\' && quote == '"':
			skip = true
		case quote != 0 && c == quote:
			quote = 0
		case quote != 0:
			cur.WriteByte(c)
		case c == '"' || c == '\'':
			quote = c
		case c == '.':
			parts = append(parts, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	parts = append(parts, strings.TrimSpace(cur.String()))
	return parts
}
