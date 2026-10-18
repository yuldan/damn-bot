package ctxdb

import (
	"errors"
	"fmt"
	"strings"

	"modernc.org/sqlite"
)

// Closed error codes (SPEC §4.3, §11.10). Callers classify with errors.Is only.
var (
	ErrSessionPurging      = errors.New("session_purging")
	ErrLeaseFenced         = errors.New("lease_fenced")
	ErrSessionBusy         = errors.New("session_busy")
	ErrPurgeInProgress     = errors.New("purge_in_progress")
	ErrPurgePending        = errors.New("purge_pending")
	ErrHasForks            = errors.New("has_forks")
	ErrBlobExpired         = errors.New("blob_expired")
	ErrBlobMissing         = errors.New("blob_missing")
	ErrApprovalNotPending  = errors.New("approval_not_pending")
	ErrSchemaNewer         = errors.New("schema_newer")
	ErrDBBusy              = errors.New("db_busy")
	ErrDiskFull            = errors.New("disk_full")
	ErrNotFound            = errors.New("not_found")
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	ErrInboundInvalid      = errors.New("inbound_invalid")
	ErrMigrationRefused    = errors.New("migration_refused")
	ErrInternal            = errors.New("internal")
)

// SQLite result codes used for classification (sqlite3.h).
const (
	codeBusy              = 5
	codeFull              = 13
	codeConstraintTrigger = 1811
)

// raiseCode maps the RAISE(ABORT, '<msg>') texts of the §11 DDL to closed
// codes. This is the single place where an SQLite message is inspected: the
// RAISE text is the only channel a trigger has to carry its reason (SPEC
// §11.10); matching is exact, not substring (report TS-P05).
func raiseCode(msg string) error {
	switch msg {
	case "session_purging":
		return ErrSessionPurging
	case "lease_fenced":
		return ErrLeaseFenced
	case "approval_not_pending":
		return ErrApprovalNotPending
	}
	return ErrInternal
}

// classify wraps a driver error with its closed code, keeping the cause.
func classify(err error) error {
	var se *sqlite.Error
	if err == nil || !errors.As(err, &se) {
		return err
	}
	switch code := se.Code(); {
	case code&0xff == codeBusy:
		return fmt.Errorf("%w: %w", ErrDBBusy, err)
	case code&0xff == codeFull:
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	case code == codeConstraintTrigger:
		return fmt.Errorf("%w: %w", raiseCode(raiseText(se.Error())), err)
	}
	return err
}

// wrap classifies and wraps any error crossing the package boundary.
func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("ctxdb: %w", classify(err))
}

// raiseText extracts "<msg>" from the driver format "<errstr>: <msg> (<rc>)".
func raiseText(s string) string {
	_, rest, ok := strings.Cut(s, ": ")
	if i := strings.LastIndex(rest, " ("); ok && i >= 0 {
		return rest[:i]
	}
	return rest
}
