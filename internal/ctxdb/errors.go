package ctxdb

import (
	"errors"
	"fmt"
	"strconv"

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
	ErrReadOnly            = errors.New("read_only")
	ErrDBBusy              = errors.New("db_busy")
	ErrDiskFull            = errors.New("disk_full")
	ErrNotFound            = errors.New("not_found")
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	ErrInboundInvalid      = errors.New("inbound_invalid")
	ErrMigrationRefused    = errors.New("migration_refused")
	ErrInternal            = errors.New("internal")
	ErrInvalidSeq          = errors.New("invalid_args") // fork seq outside [0, head]
)

// SQLite result codes used for classification (sqlite3.h).
const (
	codeBusy              = 5
	codeFull              = 13
	codeConstraintTrigger = 1811
)

// raiseMsg is the exact driver text of a trigger RAISE(ABORT, '<token>'):
// sqlite3_errstr(SQLITE_CONSTRAINT_TRIGGER) + ": " + token + " (1811)", the
// "%s: %s (%v)" form of modernc errstrForDB.
func raiseMsg(token string) string {
	return "constraint failed: " + token + " (" + strconv.Itoa(codeConstraintTrigger) + ")"
}

// raiseCode is the single E-21 exception (ADR-093, TS-P05): a trigger's RAISE
// text is the only channel carrying its reason (SPEC §11.10), so for
// SQLITE_CONSTRAINT_TRIGGER the whole message is compared for equality with
// each closed token — no substring, prefix or parsing. Any other text,
// including a changed driver format, is ErrInternal (fail-closed);
// TestRaiseCodeExact pins the format.
func raiseCode(se *sqlite.Error) error {
	for _, code := range [...]error{ErrSessionPurging, ErrLeaseFenced, ErrApprovalNotPending} {
		if se.Error() == raiseMsg(code.Error()) {
			return code
		}
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
		return fmt.Errorf("%w: %w", raiseCode(se), err)
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
