package memoryproviders

import "errors"

// Closed errors of the provider contract. Callers match with errors.Is.
var (
	// ErrInvalid rejects a malformed setting, item or query.
	ErrInvalid = errors.New("memoryproviders: invalid configuration")
	// ErrUnknownProvider rejects a name outside the closed registry.
	ErrUnknownProvider = errors.New("memoryproviders: unknown provider")
	// ErrUnknownEngine rejects a context engine outside the closed slot.
	ErrUnknownEngine = errors.New("memoryproviders: unknown context engine")
	// ErrDropped reports ByteRover/RetainDB, removed upstream (NP-21).
	ErrDropped = errors.New("memoryproviders: provider dropped")
	// ErrUnavailable reports a provider that is not linked in this build or
	// is missing a required credential.
	ErrUnavailable = errors.New("memoryproviders: provider unavailable")
	// ErrTooLarge is an item above the 1 MiB upload cap.
	ErrTooLarge = errors.New("memoryproviders: item exceeds upload cap")
	// ErrTimeout is an operation that did not finish within 1.5 s.
	ErrTimeout = errors.New("memoryproviders: operation timed out")
	// ErrResponse is a malformed or non-2xx provider response.
	ErrResponse = errors.New("memoryproviders: provider response")
)
