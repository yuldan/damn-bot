package connectors

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Error is one typed connector failure.
type Error struct {
	Code       string
	Message    string
	Hint       string
	Status     int
	RetryAfter int64 // seconds; 0 when the server sent none
	err        error
}

// Error implements error.
func (e *Error) Error() string {
	if e.Message == "" {
		return "connectors: " + e.Code
	}
	return "connectors: " + e.Code + ": " + e.Message
}

func (e *Error) Unwrap() error { return e.err }

// Host converts the error to the module-facing host error. The code is
// already a member of the closed tool set.
func (e *Error) Host() *host.Error {
	hint := e.Hint
	if e.RetryAfter > 0 {
		hint = strings.TrimSpace(hint + fmt.Sprintf("; retry after %d s", e.RetryAfter))
	}
	return &host.Error{Code: e.Code, Message: e.Message, Hint: hint}
}

// AsHostError maps any error chain to a host error, keeping a typed
// connector code and turning everything else into network/internal.
func AsHostError(err error) *host.Error {
	if err == nil {
		return nil
	}
	ce, ok := errors.AsType[*Error](err)
	if !ok {
		return &host.Error{Code: "network", Message: err.Error()}
	}
	return ce.Host()
}

// Error codes used by the framework. They mirror internal/tools.Code values
// because the module boundary only speaks the closed set.
const (
	codeInvalidArgs       = "invalid_args"
	codeTimeout           = "timeout"
	codeTooLarge          = "too_large"
	codeNetwork           = "network"
	codeAuthRequired      = "auth_required"
	codeDenied            = "denied"
	codeNotFound          = "not_found"
	codeModuleUnavailable = "module_unavailable"
	codeInternal          = "internal"
)

// Redactor replaces known secret values in any text that could reach an
// error, an event or a log line. It is intentionally value-based: a token
// never has to be structurally recognisable to be removed.
type Redactor struct {
	values []string
}

// NewRedactor returns a redactor with the given known secret values.
func NewRedactor(values ...string) *Redactor {
	r := &Redactor{}
	for _, v := range values {
		r.Add(v)
	}
	return r
}

// Add registers one secret value.
func (r *Redactor) Add(value string) {
	if r == nil || len(value) < 4 {
		return
	}
	if slices.Contains(r.values, value) {
		return
	}
	r.values = append(r.values, value)
	// Longest first so a token that contains another one is removed whole.
	for i := len(r.values) - 1; i > 0 && len(r.values[i]) > len(r.values[i-1]); i-- {
		r.values[i], r.values[i-1] = r.values[i-1], r.values[i]
	}
}

// Redact removes every known secret from s.
// Redact removes every known secret from s.
func (r *Redactor) Redact(s string) string {
	if r == nil {
		return s
	}
	for _, v := range r.values {
		s = strings.ReplaceAll(s, v, "[redacted]")
	}
	return s
}
