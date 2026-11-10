//go:build !minimal

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/internal/batch"
	"github.com/damnbot/damnbot/internal/loop"
)

type evalBatchOptions struct {
	parallel               int
	out, model, baseURL    string
	keyFile, route, ledger string
	capUSD                 float64
	maxChars               int
	noResume               bool
}

// runEvalBatch implements `eval batch run|compress` (OPS-020/021).
func runEvalBatch(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		evalUsage(stderr)
		return 2
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "run":
		return runEvalBatchRun(rest, stdout, stderr)
	case "compress":
		return runEvalBatchCompress(rest, stdout, stderr)
	default:
		sessionsPrintf(stderr, "eval batch: unknown subcommand %q\n", cleanArg(sub))
		evalUsage(stderr)
		return 2
	}
}

func parseEvalBatch(args []string, stderr io.Writer) (evalBatchOptions, []string, error) {
	var o evalBatchOptions
	fs := flag.NewFlagSet("eval batch", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.IntVar(&o.parallel, "parallel", batch.DefaultParallel, "bounded concurrency (1..32)")
	fs.StringVar(&o.out, "out", "", "output directory (default batch-<UTC>)")
	fs.StringVar(&o.model, "model", "openai/gpt-5.4-mini", "provider/model")
	fs.StringVar(&o.route, "route", "", "provider route label for the ledger")
	fs.StringVar(&o.baseURL, "base-url", "https://api.teamorouter.com/v1", "provider base URL")
	fs.StringVar(&o.keyFile, "key-file", "~/.config/damnbot/llm_key", "API key file (never printed)")
	fs.StringVar(&o.ledger, "ledger", "", "spend ledger (default <out>/spend.json)")
	fs.Float64Var(&o.capUSD, "cap-usd", 5.00, "spend cap of this batch run (SPEC §18.5)")
	fs.IntVar(&o.maxChars, "max-chars", batch.DefaultMaxChars, "compressed step limit in runes")
	fs.BoolVar(&o.noResume, "no-resume", false, "rerun every item even when a trajectory exists")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return o, nil, err
	}
	return o, pos, nil
}

func runEvalBatchRun(args []string, stdout, stderr io.Writer) int {
	o, pos, err := parseEvalBatch(args, stderr)
	if err != nil {
		return batchFail(stderr, 2, err)
	}
	if len(pos) != 1 {
		sessionsPrintf(stderr, "eval batch run: exactly one prompts.jsonl is required\n")
		return 2
	}
	outDir := o.out
	if outDir == "" {
		outDir = "batch-" + time.Now().UTC().Format("20060102T150405")
	}
	b, runner, items, err := prepareBatch(&o, outDir, pos[0])
	if err != nil {
		return batchFail(stderr, 2, err)
	}
	res, err := b.Run(context.Background(), runner, items)
	if err != nil {
		return batchFail(stderr, 1, err)
	}
	return reportBatch(res, outDir, stdout, stderr)
}

func batchFail(stderr io.Writer, code int, err error) int {
	sessionsPrintf(stderr, "eval batch run: %s\n", cleanLine(err.Error()))
	return code
}

func prepareBatch(o *evalBatchOptions, outDir, prompts string) (*batch.Batch, batch.Runner, []batch.Item, error) {
	inner, err := liveProvider(&evalOptions{model: o.model, baseURL: o.baseURL, keyFile: o.keyFile})
	if err != nil {
		return nil, nil, nil, err
	}
	ledgerPath := o.ledger
	if ledgerPath == "" {
		ledgerPath = filepath.Join(outDir, "spend.json")
	}
	if merr := os.MkdirAll(outDir, 0o700); merr != nil {
		return nil, nil, nil, fmt.Errorf("eval batch: output dir: %w", merr)
	}
	ledger, err := openSpendLedger(ledgerPath, o.capUSD)
	if err != nil {
		return nil, nil, nil, err
	}
	price := evalPrice{In: 0.15, Cached: 0.015, Out: 0.60}
	b, err := batch.New(&batch.Config{
		Parallel: o.parallel, OutDir: outDir, Model: o.model,
		Route: o.route, Resume: !o.noResume, Ledger: &batchLedger{l: ledger, price: price},
	})
	if err != nil {
		return nil, nil, nil, fmt.Errorf("eval batch: %w", err)
	}
	f, err := os.Open(prompts) // #nosec G304 G703 -- explicit prompts path
	if err != nil {
		return nil, nil, nil, fmt.Errorf("eval batch: open prompts: %w", err)
	}
	items, lerr := b.Load(f)
	if cerr := f.Close(); lerr != nil || cerr != nil {
		return nil, nil, nil, errors.Join(lerr, cerr)
	}
	return b, &loopBatchRunner{inner: inner, price: price}, items, nil
}

func reportBatch(res []batch.Result, outDir string, stdout, stderr io.Writer) int {
	var ok, failed int
	for i := range res {
		if res[i].Err != "" {
			failed++
			sessionsPrintf(stderr, "eval batch %s: %s\n", cleanArg(res[i].ID), cleanLine(res[i].Err))
			continue
		}
		ok++
	}
	sessionsPrintf(stdout, "eval batch run: %d items, %d ok, %d failed, out %s\n", len(res), ok, failed, outDir)
	if failed > 0 {
		return 1
	}
	return 0
}

func runEvalBatchCompress(args []string, stdout, stderr io.Writer) int {
	o, pos, err := parseEvalBatch(args, stderr)
	if err != nil {
		sessionsPrintf(stderr, "eval batch compress: %s\n", cleanLine(err.Error()))
		return 2
	}
	if len(pos) != 1 {
		sessionsPrintf(stderr, "eval batch compress: exactly one output directory is required\n")
		return 2
	}
	sum, err := batch.CompressDir(pos[0], o.maxChars)
	if err != nil {
		sessionsPrintf(stderr, "eval batch compress: %s\n", cleanLine(err.Error()))
		return 1
	}
	sessionsPrintf(stdout, "eval batch compress: %d items, %d truncated, %d -> %d chars\n",
		sum.Items, sum.Truncated, sum.OriginalChars, sum.CompressedChars)
	return 0
}

// loopBatchRunner adapts the OpenAI-compatible provider to one item: a single
// request with no tools, recorded as a three-step trajectory.
type loopBatchRunner struct {
	inner loop.Provider
	price evalPrice
}

func (r *loopBatchRunner) Run(ctx context.Context, req *batch.Request) (batch.Reply, error) {
	steps := make([]batch.Step, 0, 3)
	msgs := make([]loop.Message, 0, 2)
	if req.System != "" {
		steps = append(steps, batch.Step{Role: "system", Content: req.System})
		msgs = append(msgs, loop.Message{Role: "system", Content: req.System})
	}
	steps = append(steps, batch.Step{Role: "user", Content: req.Prompt})
	msgs = append(msgs, loop.Message{Role: "user", Content: req.Prompt})
	reply, err := r.inner.Stream(ctx, &loop.Request{Model: req.Model, Messages: msgs, MaxTokens: 4096}, func(loop.Delta) {})
	if err != nil {
		return batch.Reply{}, fmt.Errorf("batch: provider stream: %w", err)
	}
	steps = append(steps, batch.Step{Role: "assistant", Content: reply.Content})
	u := reply.Usage
	cost := r.price.Cost(u.Input, u.CachedInput, u.Output)
	if u.CostUSD != nil {
		cost = *u.CostUSD
	}
	micros := int64(math.Round(cost * 1e6))
	return batch.Reply{Steps: steps, Usage: batch.Usage{
		Input: u.Input, CachedInput: u.CachedInput,
		CacheCreation: u.CacheCreation, Output: u.Output, CostUSDMicros: &micros,
	}}, nil
}

// batchLedger charges the shared eval spend ledger, one row per item.
type batchLedger struct {
	l     *spendLedger
	price evalPrice
}

func (b *batchLedger) Record(_ context.Context, _ *batch.Request, u batch.Usage) error {
	cost := b.price.Cost(u.Input, u.CachedInput, u.Output)
	if u.CostUSDMicros != nil {
		cost = float64(*u.CostUSDMicros) / 1e6
	}
	return b.l.update(func(f *evalLedgerFile) error {
		if f.CapUSD > 0 && f.SpentUSD+cost > f.CapUSD {
			return fmt.Errorf("batch: spend cap %.2f USD reached", f.CapUSD)
		}
		f.SpentUSD += cost
		return nil
	})
}
