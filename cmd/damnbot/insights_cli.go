//go:build !minimal

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"time"

	"github.com/damnbot/damnbot/internal/ctxdb/usagestore"
)

type f8Insight struct {
	Day          string `json:"day"`
	Calls        int64  `json:"calls"`
	Input        int64  `json:"input_tokens"`
	Cached       int64  `json:"cached_input_tokens"`
	CacheWritten int64  `json:"cache_creation_input_tokens"`
	Output       int64  `json:"output_tokens"`
	CostMicros   int64  `json:"cost_usd_micros"`
	UnknownCalls int64  `json:"unknown_cost_calls"`
}

type f8Insights struct {
	Since string      `json:"since"`
	Rows  []f8Insight `json:"days"`
	Total f8Insight   `json:"total"`
}

func runInsights(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("insights", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dataDir := fs.String("data-dir", "", "data directory (default: profile data dir)")
	days := fs.Int("days", 7, "period length in days; 0 means the whole ledger")
	since := fs.String("since", "", "first day (YYYY-MM-DD); overrides --days")
	format := fs.String("format", "table", "table|json")
	if err := fs.Parse(args); err != nil {
		return f8ExitUsage
	}
	if fs.NArg() != 0 || *days < 0 || (*format != "table" && *format != "json") {
		say(stderr, "usage: damnbot insights [--days N|--since YYYY-MM-DD] [--format table|json] [--data-dir DIR]\n")
		return f8ExitUsage
	}
	cutoff, err := insightCutoff(*days, *since)
	if err != nil {
		say(stderr, "insights: %s\n", cleanLine(err.Error()))
		return f8ExitUsage
	}
	dir, err := f8Dir(*dataDir)
	if err != nil {
		return f8CmdFail(stderr, "insights", err)
	}
	rows, err := usageRows(context.Background(), dir, "day")
	if err != nil {
		return f8CmdFail(stderr, "insights", err)
	}
	out := buildInsights(rows, cutoff)
	if *format == "json" {
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			return f8CmdFail(stderr, "insights", fmt.Errorf("encode: %w", err))
		}
		if say(stdout, "%s\n", b) != 0 {
			return f8ExitFail
		}
		return f8ExitOK
	}
	return insightTable(&out, stdout)
}

func insightCutoff(days int, since string) (string, error) {
	if since != "" {
		if _, err := time.Parse("2006-01-02", since); err != nil {
			return "", fmt.Errorf("--since must be YYYY-MM-DD")
		}
		return since, nil
	}
	if days == 0 {
		return "", nil
	}
	return time.Now().UTC().AddDate(0, 0, -(days - 1)).Format("2006-01-02"), nil
}

func buildInsights(rows []usagestore.Row, since string) f8Insights {
	out := f8Insights{Since: since, Rows: []f8Insight{}, Total: f8Insight{Day: "total"}}
	for _, r := range rows {
		if since != "" && r.Key < since {
			continue
		}
		row := f8Insight{
			Day: r.Key, Calls: r.Calls, Input: r.Input, Cached: r.Cached,
			CacheWritten: r.CacheWritten, Output: r.Output, CostMicros: r.CostMicros,
			UnknownCalls: r.UnknownCalls,
		}
		out.Rows = append(out.Rows, row)
		out.Total.Calls += row.Calls
		out.Total.Input += row.Input
		out.Total.Cached += row.Cached
		out.Total.CacheWritten += row.CacheWritten
		out.Total.Output += row.Output
		out.Total.CostMicros += row.CostMicros
		out.Total.UnknownCalls += row.UnknownCalls
	}
	return out
}

func insightTable(in *f8Insights, stdout io.Writer) int {
	label := "all days"
	if in.Since != "" {
		label = "since " + in.Since
	}
	if say(stdout, "insights %s\n", label) != 0 {
		return f8ExitFail
	}
	if say(stdout, "day         calls   input  cached  output  cost_usd  unknown\n") != 0 {
		return f8ExitFail
	}
	for _, r := range in.Rows {
		if insightLine(stdout, r) != 0 {
			return f8ExitFail
		}
	}
	if insightLine(stdout, in.Total) != 0 {
		return f8ExitFail
	}
	return f8ExitOK
}

func insightLine(w io.Writer, r f8Insight) int {
	return say(w, "%-11s %6d %7d %7d %7d %8.4f %8d\n",
		r.Day, r.Calls, r.Input, r.Cached, r.Output, float64(r.CostMicros)/1e6, r.UnknownCalls)
}
