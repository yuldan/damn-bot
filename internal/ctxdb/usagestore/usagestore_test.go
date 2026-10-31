package usagestore

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
)

func seed(t *testing.T, dir string) {
	t.Helper()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	sessions := []ctxdb.Session{
		{ID: "s", Origin: "headless", Cwd: "/w", Model: "openai/gpt-test"},
		{ID: "t", Origin: "tui", Cwd: "/w", Model: "openai/gpt-test"},
	}
	for i := range sessions {
		ss := sessions[i]
		if _, serr := db.CreateSession(ctx, ss); serr != nil {
			t.Fatal(serr)
		}
	}
	ids, err := db.Append(ctx, "s", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	ids2, err := db.Append(ctx, "t", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "u", Content: "y"})
	if err != nil {
		t.Fatal(err)
	}
	micros := int64(2500)
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		rows := []ledgerstore.Row{
			{SessionID: "s", TurnID: "t", EventID: ids[0], Purpose: "turn", Provider: "openai", Model: "gpt-test", Feature: "", InputTokens: 100, CachedInputTokens: 20, CacheCreationTokens: 5, OutputTokens: 30, CostMicros: &micros, CostSource: "pricing_table", PricingVersion: "v1", Attempts: 1, CreatedAt: 1_700_000_000_000},
			{SessionID: "s", TurnID: "t", EventID: ids[0], Purpose: "tool", Provider: "openai", Model: "gpt-test", Feature: "tool:image", InputTokens: 7, OutputTokens: 3, CostMicros: nil, CostSource: "unknown", PricingVersion: "none", Attempts: 1, CreatedAt: 1_700_000_000_000},
			{SessionID: "t", TurnID: "u", EventID: ids2[0], Purpose: "turn", Provider: "anthropic", Model: "claude", InputTokens: 30, CachedInputTokens: 5, CacheCreationTokens: 15, OutputTokens: 4, CostMicros: nil, CostSource: "unknown", PricingVersion: "none", Attempts: 1, CreatedAt: 1_700_000_100_000},
		}
		for i := range rows {
			if _, ierr := ledgerstore.Insert(tx, &rows[i]); ierr != nil {
				return fmt.Errorf("insert ledger: %w", ierr)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestAggregateGroups(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed(t, dir)
	for _, by := range []string{"day", "month", "model", "provider", "feature", "tool", "origin"} {
		rows, err := Aggregate(t.Context(), dir, by)
		if err != nil || len(rows) == 0 {
			t.Fatalf("%s: rows=%d err=%v", by, len(rows), err)
		}
	}
	rows, err := Aggregate(t.Context(), dir, "tool")
	if err != nil || len(rows) != 1 || rows[0].Key != "tool:image" || rows[0].UnknownCalls != 1 {
		t.Fatalf("tool rows = %+v, %v", rows, err)
	}
	if _, err := Aggregate(t.Context(), dir, "nope"); err == nil {
		t.Fatal("bad --by accepted")
	}
	if !ValidBy("day") || ValidBy("session") {
		t.Fatal("ValidBy mismatch")
	}
}

// TestAggregateOrigin: --by origin joins sessions and reports cache read and
// cache creation per origin.
func TestAggregateOrigin(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "data")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	seed(t, dir)
	rows, err := Aggregate(t.Context(), dir, "origin")
	if err != nil || len(rows) != 2 {
		t.Fatalf("origin rows = %+v, %v", rows, err)
	}
	if rows[0].Key != "headless" || rows[0].CacheWritten != 5 || rows[1].Key != "tui" || rows[1].CacheWritten != 15 {
		t.Fatalf("origin cache rows = %+v", rows)
	}
}
