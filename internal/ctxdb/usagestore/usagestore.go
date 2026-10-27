// Package usagestore is the read-only `damnbot usage` aggregation over
// usage_ledger (SPEC §10/§18.4). It never opens the writer: the DSN is
// mode=ro plus query_only, so a live `serve` keeps its lock.
package usagestore

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"path/filepath"

	_ "modernc.org/sqlite" // registers the "sqlite" driver for the read-only DSN

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// Row is one aggregate line of the ledger report.
type Row struct {
	Key          string
	Calls        int64
	Input        int64
	Cached       int64
	Output       int64
	CostMicros   int64
	UnknownCalls int64
}

// ValidBy reports the SPEC §10 grouping names accepted by Aggregate.
func ValidBy(by string) bool {
	switch by {
	case "day", "month", "model", "provider", "feature", "tool":
		return true
	default:
		return false
	}
}

// Aggregate runs the read-only GROUP BY for by. A missing DB is not its
// concern: the caller prints an empty report before calling.
func Aggregate(ctx context.Context, dir, by string) (out []Row, err error) {
	expr, where, err := group(by)
	if err != nil {
		return nil, err
	}
	u := &url.URL{Scheme: "file", Path: filepath.ToSlash(ctxdb.DBPath(dir))}
	u.RawQuery = "mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
	db, err := sql.Open("sqlite", u.String())
	if err != nil {
		return nil, fmt.Errorf("usage open: %w", err)
	}
	defer func() {
		if cerr := db.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("usage close: %w", cerr)
		}
	}()
	// #nosec G202 -- expr/where come from the closed group() switch
	q := `SELECT COALESCE(` + expr + `,''), COUNT(*), SUM(input_tokens), SUM(cached_input_tokens),
		SUM(output_tokens), COALESCE(SUM(cost_usd_micros),0),
		SUM(CASE WHEN cost_usd_micros IS NULL THEN 1 ELSE 0 END)
		FROM usage_ledger ` + where + ` GROUP BY ` + expr + ` ORDER BY ` + expr
	rows, err := db.QueryContext(ctx, q)
	if err != nil {
		return nil, fmt.Errorf("usage query: %w", err)
	}
	defer func() {
		if cerr := rows.Close(); cerr != nil && err == nil {
			err = fmt.Errorf("usage rows close: %w", cerr)
		}
	}()
	for rows.Next() {
		var r Row
		if err := rows.Scan(&r.Key, &r.Calls, &r.Input, &r.Cached, &r.Output, &r.CostMicros, &r.UnknownCalls); err != nil {
			return nil, fmt.Errorf("usage scan: %w", err)
		}
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("usage rows: %w", err)
	}
	return out, nil
}

func group(by string) (expr, where string, err error) {
	switch by {
	case "day":
		return "date(created_at/1000,'unixepoch')", "", nil
	case "month":
		return "strftime('%Y-%m', created_at/1000, 'unixepoch')", "", nil
	case "model":
		return "model", "", nil
	case "provider":
		return "provider", "", nil
	case "feature":
		return "feature", "", nil
	case "tool":
		return "feature", "WHERE purpose = 'tool'", nil
	default:
		return "", "", fmt.Errorf("--by must be day, month, model, provider, feature, or tool")
	}
}
