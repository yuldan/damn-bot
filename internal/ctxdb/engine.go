package ctxdb

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
)

// MinSQLiteVersion is the oldest engine accepted by REL-04: it carries the
// WAL-reset fix (3.51.3) and contentless_delete (3.43).
const MinSQLiteVersion = "3.51.3"

// EngineInfo is the result of the REL-04 SQLite probe.
type EngineInfo struct {
	Version             string
	Major, Minor, Patch int
	FTS5                bool // CREATE VIRTUAL TABLE ... USING fts5
	ContentlessDelete   bool // content='', contentless_delete=1
	Trigram             bool // tokenize='trigram'
	JSON                bool // json_valid('{}') = 1
}

// Supported reports whether the engine satisfies every REL-04 requirement.
func (e EngineInfo) Supported() bool {
	return atLeast(e.Major, e.Minor, e.Patch, MinSQLiteVersion) &&
		e.FTS5 && e.ContentlessDelete && e.Trigram && e.JSON
}

// ProbeEngine opens an in-memory SQLite database with the same driver as the
// store and probes the REL-04 capabilities. It touches no data dir.
func ProbeEngine(ctx context.Context) (info EngineInfo, err error) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		return EngineInfo{}, fmt.Errorf("ctxdb: engine open: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("ctxdb: engine close: %w", cerr)
		}
	}()
	e := &info
	if err := db.QueryRowContext(ctx, "SELECT sqlite_version()").Scan(&e.Version); err != nil {
		return EngineInfo{}, fmt.Errorf("ctxdb: engine version: %w", err)
	}
	parsed, ok := parseVersion(e.Version)
	if !ok {
		return EngineInfo{}, fmt.Errorf("ctxdb: unparsable sqlite version %q", e.Version)
	}
	e.Major, e.Minor, e.Patch = parsed[0], parsed[1], parsed[2]
	e.FTS5 = probe(ctx, db, `CREATE VIRTUAL TABLE fts_probe USING fts5(x)`)
	e.ContentlessDelete = probe(ctx, db, `CREATE VIRTUAL TABLE fts_cd USING fts5(x, content='', contentless_delete=1)`)
	e.Trigram = probe(ctx, db, `CREATE VIRTUAL TABLE fts_tri USING fts5(x, tokenize='trigram')`)
	var one int
	e.JSON = db.QueryRowContext(ctx, `SELECT json_valid('{}')`).Scan(&one) == nil && one == 1
	return info, nil
}

func probe(ctx context.Context, db *sql.DB, ddl string) bool {
	_, err := db.ExecContext(ctx, ddl)
	return err == nil
}

// atLeast compares a semantic version against a dotted minimum.
func atLeast(major, minor, patch int, min string) bool {
	want, ok := parseVersion(min)
	if !ok {
		return false
	}
	for _, d := range [...][2]int{{major, want[0]}, {minor, want[1]}, {patch, want[2]}} {
		if d[0] != d[1] {
			return d[0] > d[1]
		}
	}
	return true
}

// parseVersion returns the first three numeric components of a version.
func parseVersion(v string) ([3]int, bool) {
	var out [3]int
	parts := strings.SplitN(v, ".", 4)
	if len(parts) < 3 {
		return out, false
	}
	for i := range out {
		n, err := strconv.Atoi(parts[i])
		if err != nil || n < 0 {
			return out, false
		}
		out[i] = n
	}
	return out, true
}
