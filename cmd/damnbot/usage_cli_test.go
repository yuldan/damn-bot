package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/ledgerstore"
)

func usageHome(t *testing.T) config.Paths {
	t.Helper()
	home := t.TempDir()
	for _, kv := range [][2]string{
		{"HOME", home},
		{"USERPROFILE", home},
		{"XDG_CONFIG_HOME", home},
		{"XDG_DATA_HOME", home},
		{"APPDATA", filepath.Join(home, "AppData", "Roaming")},
		{"LOCALAPPDATA", filepath.Join(home, "AppData", "Local")},
	} {
		t.Setenv(kv[0], kv[1])
	}
	setActiveProfile("")
	t.Cleanup(func() { setActiveProfile("") })
	loader, err := config.New(config.OS())
	if err != nil {
		t.Fatal(err)
	}
	paths, err := loader.Paths("")
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{paths.ConfigDir, paths.DataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return paths
}

func seedUsage(t *testing.T, dataDir string) {
	t.Helper()
	ctx := context.Background()
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	if _, serr := db.CreateSession(ctx, ctxdb.Session{ID: "s", Origin: "headless", Cwd: "/w", Model: "openai/gpt-test"}); serr != nil {
		t.Fatal(serr)
	}
	ids, err := db.Append(ctx, "s", nil, ctxdb.Event{Kind: "user_message", Role: "user", TurnID: "t", Content: "x"})
	if err != nil {
		t.Fatal(err)
	}
	micros := int64(2500)
	err = db.Write(ctx, func(tx *ctxdb.Tx) error {
		if _, ierr := ledgerstore.Insert(tx, &ledgerstore.Row{
			SessionID: "s", TurnID: "t", EventID: ids[0], Purpose: "turn", Provider: "openai",
			Model: "gpt-test", InputTokens: 100, CachedInputTokens: 20, OutputTokens: 30,
			CostMicros: &micros, CostSource: "pricing_table", PricingVersion: "v1",
			Attempts: 1, CreatedAt: time.Now().UnixMilli(),
		}); ierr != nil {
			return fmt.Errorf("insert ledger: %w", ierr)
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

func TestUsageEmpty(t *testing.T) {
	usageHome(t)
	code, out, errOut := runArgs(t, "usage")
	if code != 0 || out != "KEY\tCALLS\tINPUT\tCACHED\tOUTPUT\tCOST_USD\tUNKNOWN\n" {
		t.Fatalf("usage = (%d, %q, %s)", code, out, errOut)
	}
	code, out, _ = runArgs(t, "usage", "--format", "jsonl")
	if code != 0 || out != "" {
		t.Fatalf("usage jsonl = (%d, %q), want empty", code, out)
	}
}

func TestUsageAggregates(t *testing.T) {
	paths := usageHome(t)
	seedUsage(t, paths.DataDir)
	code, out, errOut := runArgs(t, "usage", "--by", "model", "--format", "jsonl")
	if code != 0 {
		t.Fatalf("usage = (%d, %s)", code, errOut)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 1 {
		t.Fatalf("lines = %d: %q", len(lines), out)
	}
	var row map[string]any
	if err := json.Unmarshal([]byte(lines[0]), &row); err != nil {
		t.Fatal(err)
	}
	for k, want := range map[string]float64{"key": 0, "calls": 1, "input_tokens": 100, "cached_input_tokens": 20, "output_tokens": 30, "cost_usd_micros": 2500, "unknown_cost_calls": 0} {
		got, ok := row[k].(float64)
		if k == "key" {
			if row[k] != "gpt-test" {
				t.Errorf("key = %v", row[k])
			}
			continue
		}
		if !ok || got != want {
			t.Errorf("%s = %v, want %v", k, row[k], want)
		}
	}
}

func TestUsageRejectsBadBy(t *testing.T) {
	usageHome(t)
	if code, _, errOut := runArgs(t, "usage", "--by", "nope"); code != 2 || !strings.Contains(errOut, "--by must") {
		t.Fatalf("bad --by = (%d, %s), want exit 2", code, errOut)
	}
}
