package assemble

import "errors"

// Sentinel errors of the package.
var (
	// ErrDeps reports a missing constructor dependency (E-11).
	ErrDeps = errors.New("assemble: missing dependency")
	// ErrPhase reports an unknown [Phase].
	ErrPhase = errors.New("assemble: unknown phase")
	// ErrInvalidTool reports a malformed tool schema (not valid JSON).
	ErrInvalidTool = errors.New("assemble: invalid tool schema")
	// ErrNoUserMessage reports a frozen turn without a user message to carry
	// the volatile tier.
	ErrNoUserMessage = errors.New("assemble: no user message")
	// ErrUnavailable marks a context source the sandbox refused to open
	// (outside root, denied_path, hard link). The source is omitted, never
	// read outside the sandbox.
	ErrUnavailable = errors.New("assemble: context source unavailable")
	// ErrContextPath reports an empty context file path.
	ErrContextPath = errors.New("assemble: invalid context path")
)
