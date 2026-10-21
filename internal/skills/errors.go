package skills

import "errors"

// Closed errors of the consumer API.
var (
	// ErrInvalid is a rejected skill shape or argument.
	ErrInvalid = errors.New("invalid_skill")
	// ErrTooLarge is the 8.192 B file cap violation.
	ErrTooLarge = errors.New("too_large")
	// ErrFrontMatter is a missing or unsupported front matter block.
	ErrFrontMatter = errors.New("invalid_front_matter")
	// ErrScan means an untrusted install had no threat scanner (fail closed).
	ErrScan = errors.New("scan_required")
)
