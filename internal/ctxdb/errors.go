package ctxdb

import (
	"errors"
	"fmt"
	"strconv"
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
	ErrReadOnly            = errors.New("read_only")
	ErrDBBusy              = errors.New("db_busy")
	ErrDiskFull            = errors.New("disk_full")
	ErrNotFound            = errors.New("not_found")
	ErrIdempotencyConflict = errors.New("idempotency_conflict")
	ErrInboundInvalid      = errors.New("inbound_invalid")
	ErrMigrationRefused    = errors.New("migration_refused")
	ErrInternal            = errors.New("internal")
	ErrInvalidSeq          = errors.New("invalid_args") // fork seq outside [0, head]
	ErrEncrypted           = errors.New("encrypted_db")
	ErrAtRestPlaintext     = errors.New("at_rest_existing_plaintext")
)

// SQLite result codes used for classification (sqlite3.h).
const (
	codeBusy              = 5
	codeReadOnly          = 8
	codeFull              = 13
	codeCantOpen          = 14
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
// readOnlyQuery accepts a single read statement (SELECT/WITH/VALUES) and
// rejects DML, PRAGMA, ATTACH, VACUUM, and multi-statement SQL. It tokenizes
// outside strings, quoted identifiers, and comments, so a keyword inside a
// literal or a name such as created_at/pragma_table_info is not a match.
func readOnlyQuery(q string) bool {
	toks, ok := scanSQL(q)
	if !ok || len(toks) == 0 {
		return false
	}
	for _, t := range toks {
		switch t {
		case "ATTACH", "DETACH", "VACUUM", "INSERT", "UPDATE", "DELETE", "REPLACE", "CREATE", "DROP", "ALTER", "PRAGMA":
			return false
		}
	}
	switch toks[0] {
	case "SELECT", "WITH", "VALUES":
		return true
	}
	return false
}

func scanSQL(q string) ([]string, bool) {
	var toks []string
	terminated := false
	for i := 0; i < len(q); {
		tok, next, ok := nextSQLToken(q, i)
		if !ok {
			return nil, false
		}
		i = next
		switch tok {
		case ";":
			terminated = true
		case "":
		default:
			if terminated {
				return nil, false
			}
			toks = append(toks, tok)
		}
	}
	return toks, true
}

// nextSQLToken skips whitespace, comments, strings, and quoted identifiers;
// it returns ";" for a separator and "" for skipped input.
func nextSQLToken(q string, i int) (string, int, bool) {
	c := q[i]
	switch {
	case isSpace(c):
		return "", i + 1, true
	case c == '-' && i+1 < len(q) && q[i+1] == '-':
		return "", lineEnd(q, i), true
	case c == '/' && i+1 < len(q) && q[i+1] == '*':
		n, ok := blockEnd(q, i)
		return "", n, ok
	case isQuote(c):
		n, ok := quotedEnd(q, i, c)
		return "", n, ok
	case c == '[':
		n, ok := bracketEnd(q, i)
		return "", n, ok
	case c == ';':
		return ";", i + 1, true
	case isWordStart(c):
		j := i + 1
		for j < len(q) && isWordByte(q[j]) {
			j++
		}
		return strings.ToUpper(q[i:j]), j, true
	}
	return "", i + 1, true
}

func lineEnd(q string, i int) int {
	for i < len(q) && q[i] != '\n' {
		i++
	}
	return i
}

func blockEnd(q string, i int) (int, bool) {
	j := strings.Index(q[i+2:], "*/")
	if j < 0 {
		return 0, false
	}
	return i + j + 4, true
}

func quotedEnd(q string, i int, quote byte) (int, bool) {
	i++
	for i < len(q) {
		if q[i] != quote {
			i++
			continue
		}
		if i+1 < len(q) && q[i+1] == quote {
			i += 2
			continue
		}
		return i + 1, true
	}
	return 0, false
}

func bracketEnd(q string, i int) (int, bool) {
	j := strings.IndexByte(q[i+1:], ']')
	if j < 0 {
		return 0, false
	}
	return i + j + 2, true
}

func isSpace(c byte) bool { return strings.IndexByte(" \t\n\r\f\v", c) >= 0 }

func isQuote(c byte) bool { return strings.IndexByte("'\"`", c) >= 0 }

func isWordStart(c byte) bool {
	return c == '_' || c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'
}

func isWordByte(c byte) bool {
	return isWordStart(c) || c >= '0' && c <= '9'
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("ctxdb: %w", classify(err))
}
