package config

import (
	"errors"
	"runtime"
	"slices"
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
func (l *Loader) Load(path string) (Config, error) {
	data, err := l.deps.ReadFile(path)
	if err != nil {
		return Config{}, &Error{Kind: ErrRead, Cause: err, Key: path, Msg: "cannot read file"}
	}
	return l.Parse(data, path)
}

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
	if und := md.Undecoded(); len(und) > 0 {
		key := und[0]
		return Config{}, &Error{
			Kind: ErrUnknownKey,
			Key:  key.String(),
			Line: findKeyLine(data, key),
			Msg:  "key is not part of the F1 config",
		}
	}
	if err := cfg.Validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
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
	return &Error{Kind: ErrSyntax, Cause: err, Key: source, Msg: "invalid TOML"}
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
