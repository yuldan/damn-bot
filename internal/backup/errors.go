// Package backup implements the encrypted DMBK1 backup container (ADR-124,
// GAP-SEC-01). It is a library: the CLI and the ctxdb snapshot/restore wiring
// live in the composition root and later PRs. A container is always
// encrypted; there is no plaintext mode.
package backup

import "errors"

// Typed container errors (E-21).
var (
	// ErrAuth means the archive was not encrypted to the supplied key
	// (wrong key) or the authenticated header was modified.
	ErrAuth = errors.New("backup: authentication failed")
	// ErrCorrupt means the archive framing or a payload chunk is invalid.
	ErrCorrupt = errors.New("backup: corrupt archive")
	// ErrTruncated means the archive ended before its final chunk.
	ErrTruncated = errors.New("backup: truncated archive")
	// ErrIncompatible means the magic, version, wrap or algorithm is unknown.
	ErrIncompatible = errors.New("backup: unsupported archive")
	// ErrKey means the supplied key material is malformed.
	ErrKey = errors.New("backup: invalid key")
)
