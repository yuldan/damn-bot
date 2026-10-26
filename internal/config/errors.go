package config

import (
	"errors"
	"fmt"
)

// Sentinel errors. Every error returned by this package matches exactly one
// of these with errors.Is (SPEC §4.9).
var (
	// ErrUnknownKey reports a key that the config struct does not expose.
	ErrUnknownKey = errors.New("config: unknown key")
	// ErrModuleUnavailable reports a config section owned by a module that
	// this build does not compile in (SPEC §12 module sections). It is
	// distinct from ErrUnknownKey so users get an actionable error instead of
	// a typo message.
	ErrModuleUnavailable = errors.New("config: module section unavailable")
	// ErrInvalidValue reports a value outside its SPEC §12 range.
	ErrInvalidValue = errors.New("config: invalid value")
	// ErrSyntax reports malformed TOML.
	ErrSyntax = errors.New("config: invalid TOML")
	// ErrRead reports that the config file could not be read.
	ErrRead = errors.New("config: read failed")
	// ErrTooLarge reports a config file above [MaxFileSize].
	ErrTooLarge = errors.New("config: file too large")
	// ErrDeps reports a missing constructor dependency (E-11).
	ErrDeps = errors.New("config: missing dependency")
	// ErrProfile reports an invalid --profile name.
	ErrProfile = errors.New("config: invalid profile")
	// ErrHome reports that the home directory could not be resolved.
	ErrHome = errors.New("config: home directory unavailable")
)

// Error is the single error type of the package. Key is the dotted TOML key
// path (or the file path for read errors) and Line is the 1-based TOML line
// when it is known.
type Error struct {
	Kind  error // one of the sentinels above
	Cause error // optional underlying error (read/syntax failures)
	Key   string
	Line  int
	Msg   string
}

// Error renders "config: <key> (line N): <message>".
func (e *Error) Error() string {
	loc := e.Key
	if e.Line > 0 {
		loc = fmt.Sprintf("%s (line %d)", loc, e.Line)
	}
	if loc == "" {
		return "config: " + e.Msg
	}
	return "config: " + loc + ": " + e.Msg
}

// Unwrap exposes both the sentinel kind and the underlying cause so callers
// can use errors.Is(err, ErrRead) and errors.Is(err, fs.ErrNotExist).
func (e *Error) Unwrap() []error {
	if e.Cause != nil {
		return []error{e.Kind, e.Cause}
	}
	return []error{e.Kind}
}

func keyError(kind error, key, format string, args ...any) *Error {
	return &Error{Kind: kind, Key: key, Msg: fmt.Sprintf(format, args...)}
}
