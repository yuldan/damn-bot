package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/usagestore"
)

type usageJSON struct {
	By                string `json:"by"`
	Key               string `json:"key"`
	Calls             int64  `json:"calls"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	CacheCreatedInput int64  `json:"cache_creation_input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CostUSDMicros     int64  `json:"cost_usd_micros"`
	UnknownCostCalls  int64  `json:"unknown_cost_calls"`
}

// runUsageCommand is the read-only F3 ledger report: `usage [--by ...]
// [--format table|jsonl]` (SPEC §10, §18.4). `budget`/`insights`/`--cache`
// need their own engines and are not accepted as no-ops.
func runUsageCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("usage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	by := flags.String("by", "day", "day|month|model|provider|feature|tool|origin")
	format := flags.String("format", "table", "table|jsonl")
	cache := flags.Bool("cache", false, "prompt-cache report; defaults to --by origin")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *cache {
		seen := false
		flags.Visit(func(f *flag.Flag) { seen = seen || f.Name == "by" })
		if !seen {
			*by = "origin"
		}
	}
	if flags.NArg() != 0 {
		say(stderr, "usage: %q is not available in F3\n", cleanArg(flags.Arg(0)))
		return 2
	}
	if *format != "table" && *format != "jsonl" {
		say(stderr, "usage: --format must be table or jsonl\n")
		return 2
	}
	if !usagestore.ValidBy(*by) {
		say(stderr, "usage: --by must be day, month, model, provider, feature, tool, or origin\n")
		return 2
	}
	loader, err := config.New(config.OS())
	if err != nil {
		say(stderr, "usage: %s\n", cleanLine(err.Error()))
		return 1
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		say(stderr, "usage: %s\n", cleanLine(err.Error()))
		return 1
	}
	rows, err := usageRows(context.Background(), paths.DataDir, *by)
	if err != nil {
		say(stderr, "usage: %s\n", cleanLine(err.Error()))
		return 1
	}
	if *format == "jsonl" {
		return usageJSONLines(*by, rows, stdout, stderr)
	}
	return usageTable(rows, *cache, stdout)
}

func usageJSONLines(by string, rows []usagestore.Row, stdout, stderr io.Writer) int {
	for _, r := range rows {
		b, err := json.Marshal(usageJSON{By: by, Key: r.Key, Calls: r.Calls, InputTokens: r.Input, CachedInputTokens: r.Cached, CacheCreatedInput: r.CacheWritten, OutputTokens: r.Output, CostUSDMicros: r.CostMicros, UnknownCostCalls: r.UnknownCalls})
		if err != nil {
			say(stderr, "usage: json: %s\n", cleanLine(err.Error()))
			return 1
		}
		if code := say(stdout, "%s\n", b); code != 0 {
			return code
		}
	}
	return 0
}

func usageTable(rows []usagestore.Row, cache bool, stdout io.Writer) int {
	if cache {
		if code := say(stdout, "ORIGIN\tCALLS\tINPUT\tCACHE_READ\tCACHE_CREATION\tHIT_PCT\tOUTPUT\tCOST_USD\n"); code != 0 {
			return code
		}
		for _, r := range rows {
			hit := int64(0)
			if r.Input > 0 {
				hit = 100 * r.Cached / r.Input
			}
			if code := say(stdout, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%.6f\n", r.Key, r.Calls, r.Input, r.Cached, r.CacheWritten, hit, r.Output, float64(r.CostMicros)/1e6); code != 0 {
				return code
			}
		}
		return 0
	}
	if code := say(stdout, "KEY\tCALLS\tINPUT\tCACHED\tOUTPUT\tCOST_USD\tUNKNOWN\n"); code != 0 {
		return code
	}
	for _, r := range rows {
		if code := say(stdout, "%s\t%d\t%d\t%d\t%d\t%.6f\t%d\n", r.Key, r.Calls, r.Input, r.Cached, r.Output, float64(r.CostMicros)/1e6, r.UnknownCalls); code != 0 {
			return code
		}
	}
	return 0
}

func usageRows(ctx context.Context, dataDir, by string) ([]usagestore.Row, error) {
	path := ctxdb.DBPath(dataDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	rows, err := usagestore.Aggregate(ctx, dataDir, by)
	if err != nil {
		return nil, fmt.Errorf("aggregate usage: %w", err)
	}
	return rows, nil
}

func usageUsage(w io.Writer) {
	say(w, "usage: damnbot usage [--by day|month|model|provider|feature|tool|origin] [--cache] [--format table|jsonl]\n")
	say(w, "  --cache: HIT_PCT = cached_input / (input + cache_read + cache_creation); cache-write premium is not priced (cost stays unknown unless the provider reports it)\n")
}
