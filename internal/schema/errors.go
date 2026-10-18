package schema

import (
	"errors"
	"strconv"
)

// ErrInvalid matches (errors.Is) every *Error returned by Validate: the
// document is not valid JSON or violates the schema. The tool pipeline maps
// it to the model-facing code invalid_args (SPEC P3).
var ErrInvalid = errors.New("schema: invalid document")

// Kind classifies a validation failure.
type Kind uint8

// Validation failure kinds.
const (
	KindSyntax    Kind = iota + 1 // not strict RFC 8259 JSON (incl. duplicate keys)
	KindDepth                     // nesting deeper than MaxDepth
	KindViolation                 // valid JSON that violates a keyword
)

// Error is a validation failure with a precise location for the model.
type Error struct {
	Kind    Kind
	Offset  int    // byte offset of the failure in the document
	Pointer string // RFC 6901 JSON Pointer of the failing value ("" = root)
	Keyword string // failing keyword for KindViolation ("type", "required", …)
	Msg     string
}

// Error renders a stable, model-facing message.
func (e *Error) Error() string {
	if e.Kind != KindViolation {
		return "invalid JSON at byte " + strconv.Itoa(e.Offset) + ": " + e.Msg
	}
	return orRoot(e.Pointer) + ": " + e.Msg
}

// Is reports ErrInvalid for every validation error.
func (e *Error) Is(target error) bool { return target == ErrInvalid }

func orRoot(pointer string) string {
	if pointer == "" {
		return "(root)"
	}
	return pointer
}
