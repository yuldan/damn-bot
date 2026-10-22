package ctxdb

// ADR-115: the schema template that TestMain builds is only sound if a store
// opened in a directory copied from it is indistinguishable from one opened on
// an empty directory, where the migration ladder runs.

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
)

// TestSchemaTemplateCopyMatchesFreshOpen pins user_version, the schema dump,
// the connection PRAGMAs and every table's row count between a fresh open and
// a store opened from the template copy.
func TestSchemaTemplateCopyMatchesFreshOpen(t *testing.T) {
	t.Parallel()
	if os.Getenv(templateEnv) == "" {
		t.Fatal("schema template was not built (see the TestMain warning); newEnv would run migrations")
	}
	fresh := newRawEnv(t).open(1, "serve")
	copied := newEnv(t).open(2, "serve")
	ctx := context.Background()

	for _, q := range []string{
		"PRAGMA user_version",
		"PRAGMA journal_mode",
		"PRAGMA synchronous",
		"PRAGMA foreign_keys",
		"PRAGMA busy_timeout",
		"PRAGMA journal_size_limit",
		"PRAGMA wal_autocheckpoint",
		"PRAGMA temp_store",
		"PRAGMA trusted_schema",
		"PRAGMA cache_size",
	} {
		var a, b any
		if err := fresh.get(ctx, q, nil, &a); err != nil {
			t.Fatalf("%s (fresh): %v", q, err)
		}
		if err := copied.get(ctx, q, nil, &b); err != nil {
			t.Fatalf("%s (copy): %v", q, err)
		}
		if got, want := fmt.Sprint(b), fmt.Sprint(a); got != want {
			t.Errorf("%s: template copy %q, fresh %q", q, got, want)
		}
	}

	const dump = `SELECT group_concat(sql, ';') FROM (SELECT sql FROM sqlite_master WHERE sql IS NOT NULL ORDER BY type, name, sql)`
	var a, b any
	if err := fresh.get(ctx, dump, nil, &a); err != nil {
		t.Fatal(err)
	}
	if err := copied.get(ctx, dump, nil, &b); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(a) != fmt.Sprint(b) {
		t.Errorf("schema dump differs:\nfresh:   %v\ntemplate: %v", a, b)
	}

	var tables []string
	if err := fresh.each(ctx, `SELECT name FROM sqlite_master WHERE type = 'table' ORDER BY name`, nil,
		func(rows *sql.Rows) error {
			var name string
			if err := rows.Scan(&name); err != nil {
				return fmt.Errorf("scan table name: %w", err)
			}
			tables = append(tables, name)
			return nil
		}); err != nil {
		t.Fatal(err)
	}
	if len(tables) == 0 {
		t.Fatal("no tables in the fresh schema")
	}
	for _, table := range tables {
		q := `SELECT count(*) FROM "` + table + `"`
		if n, m := count(t, fresh, q), count(t, copied, q); n != m {
			t.Errorf("%s: %d rows fresh, %d from the template", table, n, m)
		}
	}
}
