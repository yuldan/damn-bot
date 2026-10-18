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
	codeConstraint        = 19
	codeConstraintTrigger = 1811
)

// raiseCodes maps RAISE(ABORT, '<msg>') texts of the §11 DDL to closed codes.
// This is the single place where an SQLite message is inspected: the RAISE
// text is the only channel a trigger has to carry its reason (SPEC §11.10
// "Pesan RAISE dipetakan ke kode tertutup"); matching is exact, not substring.
var raiseCodes = map[string]error{
	"session_purging":      ErrSessionPurging,
	"lease_fenced":         ErrLeaseFenced,
	"approval_not_pending": ErrApprovalNotPending,
}

// classify wraps a driver error with its closed code, keeping the cause.
func classify(err error) error {
	if err == nil {
		return nil
	}
	var se *sqlite.Error
	if !errors.As(err, &se) {
		return err
	}
	switch code := se.Code(); {
	case code&0xff == codeBusy:
		return fmt.Errorf("%w: %w", ErrDBBusy, err)
	case code&0xff == codeFull:
		return fmt.Errorf("%w: %w", ErrDiskFull, err)
	case code == codeConstraintTrigger:
		if c, ok := raiseCodes[raiseText(se.Error())]; ok {
			return fmt.Errorf("%w: %w", c, err)
		}
		return fmt.Errorf("%w: %w", ErrInternal, err)
	}
	return err
}

// raiseText extracts "<msg>" from the driver format "<errstr>: <msg> (<rc>)".
func raiseText(s string) string {
	_, rest, ok := strings.Cut(s, ": ")
	if !ok {
		return ""
	}
	if i := strings.LastIndex(rest, " ("); i >= 0 {
		rest = rest[:i]
	}
	return rest
}
