//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
)

// TestF8CLI051JourneyMemory proves the CLI journey merges locally stored
// memories with the skills ledger through the shared builder (CLI-051,
// MEM-035): chronological, deduplicated, and redacted.
func TestF8CLI051JourneyMemory(t *testing.T) {
	data := t.TempDir()
	seedJourney(t, data)
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: data, Kind: "cli"})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		_, xerr := tx.Exec(`INSERT INTO memory(kind, scope, content, origin, created_at)
			VALUES ('memory', 'user', ?1, ?2, ?3)`, "remembered context", "user", 1)
		if xerr != nil {
			return fmt.Errorf("insert memory: %w", xerr)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.CloseContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	code, out, errs := runF8CLI(t, runJourney, "--data-dir", data, "list", "--json")
	var rows []f8JourneyEntry
	if code != f8ExitOK || json.Unmarshal([]byte(out), &rows) != nil {
		t.Fatalf("journey list = %d %q %q", code, out, errs)
	}
	found := false
	for _, row := range rows {
		if row.Action == "remembered" && strings.Contains(row.Name, "remembered context") && row.Source == "user" {
			found = true
		}
	}
	if !found {
		t.Fatalf("memory row missing: %+v", rows)
	}
}
