package ctxdb

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"modernc.org/sqlite"
)

// TestRaiseCodeExact pins the single E-21 exception (ADR-093, TS-P05): only
// SQLITE_CONSTRAINT_TRIGGER whose whole message is an exact closed RAISE token
// maps to that sentinel; near misses and other trigger texts are ErrInternal,
// and non-trigger constraint errors are not reclassified.
func TestRaiseCodeExact(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	db := sql.OpenDB(connector{d: &sqlite.Driver{}, dsn: "file:" + filepath.Join(t.TempDir(), "raise.db")})
	defer func() { must(t, db.Close()) }()
	cases := []struct {
		raise string
		want  error
	}{
		{"session_purging", ErrSessionPurging},
		{"lease_fenced", ErrLeaseFenced},
		{"approval_not_pending", ErrApprovalNotPending},
		{"session_purging ", ErrInternal},
		{"x session_purging", ErrInternal},
		{"Lease_fenced", ErrInternal},
		{"events is append-only", ErrInternal},
		{"internal: cross-session event_id", ErrInternal},
	}
	_, err := db.ExecContext(ctx, "CREATE TABLE t(n INTEGER CHECK (n >= 0))")
	must(t, err)
	for i, c := range cases {
		_, err = db.ExecContext(ctx, fmt.Sprintf(
			"CREATE TRIGGER t%d BEFORE INSERT ON t WHEN NEW.n = %d BEGIN SELECT RAISE(ABORT, '%s'); END", i, i, c.raise))
		must(t, err)
	}
	closed := []error{ErrSessionPurging, ErrLeaseFenced, ErrApprovalNotPending, ErrInternal}
	for i, c := range cases {
		_, err = db.ExecContext(ctx, "INSERT INTO t(n) VALUES (?)", i)
		got := classify(err)
		for _, e := range closed {
			if errors.Is(got, e) != errors.Is(c.want, e) {
				t.Errorf("RAISE %q: got %v, want %v", c.raise, got, c.want)
			}
		}
	}
	_, err = db.ExecContext(ctx, "INSERT INTO t(n) VALUES (-1)") // CHECK, not a trigger
	if err == nil || errors.Is(classify(err), ErrInternal) {
		t.Fatalf("CHECK violation: %v, want an unclassified constraint error", err)
	}
}

// TestTrustedSchemaExplicit (ADR-093, TS-P03): trusted_schema is set on every
// Store connection instead of relying on the SQLite compile-time default,
// because the trigger functions cannot be flagged SQLITE_INNOCUOUS.
func TestTrustedSchemaExplicit(t *testing.T) {
	t.Parallel()
	s := newEnv(t).open(1, "tui")
	for _, db := range []*sql.DB{s.w, s.r} {
		var v int
		must(t, getOne(context.Background(), db, "PRAGMA trusted_schema", nil, &v))
		if v != 1 {
			t.Fatalf("trusted_schema = %d, want 1", v)
		}
	}
}

// TestNonRefShaClosedList (ADR-093, TS-P04, SPEC §4.1): only preview_sha,
// args_sha, payload_sha, and the carrier ref's sha may hold a 64-hex value
// without being a blob ref; any other non-ref key is rejected.
func TestNonRefShaClosedList(t *testing.T) {
	t.Parallel()
	const a = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const b = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	doc := `{"preview_sha":"` + a + `","args_sha":"` + a + `","payload_sha":"` + a + `","carriers":[{"ord":0,"sha":"` + a + `"}],"blob_sha":"` + b + `"}`
	refs, err := BlobRefsOf("approval", "1", nil, map[string]string{"payload": doc})
	must(t, err)
	if len(refs) != 1 || refs[0].Sha != b {
		t.Fatalf("refs = %+v, want only blob_sha", refs)
	}
	for _, k := range []string{"other_sha", "sha256", "preview"} {
		if _, err := BlobRefsOf("approval", "1", nil, map[string]string{"payload": `{"` + k + `":"` + a + `"}`}); !errors.Is(err, ErrInternal) {
			t.Errorf("key %q: %v, want rejected", k, err)
		}
	}
}
