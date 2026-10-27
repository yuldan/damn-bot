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
	"strconv"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
)

type usageRow struct {
	By, Key                                           string
	Calls, Input, Cached, Output, CostMicros, Unknown int64
}

type usageJSON struct {
	By                string `json:"by"`
	Key               string `json:"key"`
	Calls             int64  `json:"calls"`
	InputTokens       int64  `json:"input_tokens"`
	CachedInputTokens int64  `json:"cached_input_tokens"`
	OutputTokens      int64  `json:"output_tokens"`
	CostUSDMicros     int64  `json:"cost_usd_micros"`
	UnknownCostCalls  int64  `json:"unknown_cost_calls"`
}

// runUsage implements the read-only F3 ledger report: `usage [--by ...]
// [--format table|jsonl]` (SPEC §10, §18.4). `budget`/`insights`/`--cache`
// need their own engines and are not accepted as no-ops.
func runUsageCommand(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("usage", flag.ContinueOnError)
	flags.SetOutput(stderr)
	by := flags.String("by", "day", "day|month|model|provider|feature|tool")
	format := flags.String("format", "table", "table|jsonl")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		say(stderr, "usage: %q is not available in F3\n", cleanArg(flags.Arg(0)))
		return 2
	}
	if *format != "table" && *format != "jsonl" {
		say(stderr, "usage: --format must be table or jsonl\n")
		return 2
	}
	if _, gerr := usageGroupFor(*by); gerr != nil {
		say(stderr, "usage: %s\n", gerr)
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
		return usageJSONLines(rows, stdout, stderr)
	}
	return usageTable(rows, stdout)
}

func usageJSONLines(rows []usageRow, stdout, stderr io.Writer) int {
	for _, r := range rows {
		b, err := json.Marshal(usageJSON{r.By, r.Key, r.Calls, r.Input, r.Cached, r.Output, r.CostMicros, r.Unknown})
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

func usageTable(rows []usageRow, stdout io.Writer) int {
	if code := say(stdout, "KEY\tCALLS\tINPUT\tCACHED\tOUTPUT\tCOST_USD\tUNKNOWN\n"); code != 0 {
		return code
	}
	for _, r := range rows {
		if code := say(stdout, "%s\t%d\t%d\t%d\t%d\t%.6f\t%d\n", r.Key, r.Calls, r.Input, r.Cached, r.Output, float64(r.CostMicros)/1e6, r.Unknown); code != 0 {
			return code
		}
	}
	return 0
}

func usageRows(ctx context.Context, dataDir, by string) ([]usageRow, error) {
	path := ctxdb.DBPath(dataDir)
	if _, err := os.Stat(path); errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	} else if err != nil {
		return nil, fmt.Errorf("stat %s: %w", path, err)
	}
	group, err := usageGroupFor(by)
	if err != nil {
		return nil, err
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "cli"})
	if err != nil {
		return nil, fmt.Errorf("open ctxdb: %w", err)
	}
	var raw []string
	q := `SELECT COALESCE(` + group.expr + `,'') || char(31) || COUNT(*) || char(31) ||
		SUM(input_tokens) || char(31) || SUM(cached_input_tokens) || char(31) ||
		SUM(output_tokens) || char(31) || COALESCE(SUM(cost_usd_micros),0) || char(31) ||
		SUM(CASE WHEN cost_usd_micros IS NULL THEN 1 ELSE 0 END)
		FROM usage_ledger ` + group.where + ` GROUP BY ` + group.expr + ` ORDER BY ` + group.expr
	werr := db.Write(ctx, func(tx *ctxdb.Tx) error {
		var e error
		raw, e = tx.Strings(q)
		if e != nil {
			return fmt.Errorf("aggregate usage: %w", e)
		}
		return nil
	})
	if cerr := db.CloseContext(ctx); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return nil, werr
	}
	rows := make([]usageRow, 0, len(raw))
	for _, line := range raw {
		parts := strings.Split(line, "\x1f")
		if len(parts) != 7 {
			return nil, fmt.Errorf("usage: malformed aggregate row")
		}
		nums := make([]int64, 6)
		for i := range nums {
			n, perr := strconv.ParseInt(parts[i+1], 10, 64)
			if perr != nil {
				return nil, fmt.Errorf("usage: parse %q: %w", parts[i+1], perr)
			}
			nums[i] = n
		}
		rows = append(rows, usageRow{
			By: by, Key: parts[0], Calls: nums[0], Input: nums[1],
			Cached: nums[2], Output: nums[3], CostMicros: nums[4], Unknown: nums[5],
		})
	}
	return rows, nil
}

type usageGroupSQL struct{ expr, where string }

func usageGroupFor(by string) (usageGroupSQL, error) {
	switch by {
	case "day":
		return usageGroupSQL{"date(created_at/1000,'unixepoch')", ""}, nil
	case "month":
		return usageGroupSQL{"strftime('%Y-%m', created_at/1000, 'unixepoch')", ""}, nil
	case "model":
		return usageGroupSQL{"model", ""}, nil
	case "provider":
		return usageGroupSQL{"provider", ""}, nil
	case "feature":
		return usageGroupSQL{"feature", ""}, nil
	case "tool":
		return usageGroupSQL{"feature", "WHERE purpose = 'tool'"}, nil
	default:
		return usageGroupSQL{}, fmt.Errorf("--by must be day, month, model, provider, feature, or tool")
	}
}

func usageUsage(w io.Writer) {
	say(w, "usage: damnbot usage [--by day|month|model|provider|feature|tool] [--format table|jsonl]\n")
}
