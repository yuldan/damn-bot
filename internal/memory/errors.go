package memory

import "errors"

// Closed errors of the consumer API.
var (
	// ErrInvalid is a rejected proposal (shape, scope, category, provenance).
	ErrInvalid = errors.New("invalid_memory")
	// ErrTooLarge is a content or per-scope cap violation (P10).
	ErrTooLarge = errors.New("too_large")
	// ErrScope rejects an id that does not belong to the caller's scope
	// (cross-scope supersede/decide/pin, JN3-2).
	ErrScope = errors.New("scope_mismatch")
)

// LimitError is implemented by storage errors that report the P10
// per-scope cap. memstore's error satisfies it, so the consumer can classify
// the violation without importing the storage package.
type LimitError interface {
	error
	TooLarge() bool
}
