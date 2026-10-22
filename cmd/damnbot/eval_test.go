package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
)

func testPrice() evalPrice { return evalPrice{In: 0.15, Cached: 0.015, Out: 0.60} }

// fakeProvider answers deterministically without a network: on a question it
// returns the memory bullets the assembler put in the context tier, proving
// the retrieval plumbing end to end. Usage comes from the COST-09 estimator,
// so the dry run projects real calls and cost.
type fakeProvider struct{ price evalPrice }

func (f *fakeProvider) Stream(_ context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	answer := "ok"
	if q := lastUserText(req); strings.Contains(q, "?") {
		if answer = memoryBullets(req); answer == "" {
			answer = "I don't know."
		}
	}
	emit(loop.Delta{Text: answer})
	in, out := estimateInputTokens(req), int64(budget.Count(answer).Raw())
	cost := f.price.Cost(in, 0, out)
	return loop.Reply{
		Content: answer, FinishReason: loop.FinishStop, Model: "fake/gpt-5.4-mini",
		Usage: event.Usage{Input: in, Output: out, CostUSD: &cost, Iterations: 1},
	}, nil
}

func lastUserText(req *loop.Request) string {
	for _, m := range slices.Backward(req.Messages) {
		if m.Role == "user" {
			return m.Content
		}
	}
	return ""
}

// memoryBullets extracts the entries rendered under "# Memory" in the context
// tier (assemble.renderMemory: "- <text>" lines).
func memoryBullets(req *loop.Request) string {
	for _, m := range req.Messages {
		start := strings.Index(m.Content, "# Memory")
		if m.Role != "system" || start < 0 {
			continue
		}
		var out []string
		for line := range strings.SplitSeq(m.Content[start+len("# Memory"):], "\n") {
			line = strings.TrimSpace(line)
			if rest, ok := strings.CutPrefix(line, "- "); ok {
				out = append(out, strings.TrimSpace(rest))
				continue
			}
			if len(out) == 0 {
				continue // blank line between the heading and the first entry
			}
			if line == "" || strings.HasPrefix(line, "# ") {
				break
			}
		}
		if len(out) > 0 {
			return strings.Join(out, "; ")
		}
	}
	return ""
}

// fakeRunOpts builds a dry-run configuration over the committed eval set.
func fakeRunOpts(t *testing.T, out string, limit int) *evalOptions {
	t.Helper()
	return &evalOptions{
		evals: "../../bench/evals", out: out, model: "openai/gpt-5.4-mini",
		ledgerPath: filepath.Join(t.TempDir(), "f2-spend.json"), capUSD: 2,
		priceIn: 0.15, priceCached: 0.015, priceOut: 0.60,
		gateMin: 0.90, timeoutS: 120, limit: limit, fake: true,
	}
}

func fakeRun(t *testing.T, o *evalOptions) (int, evalSummary, []evalTaskResult) {
	t.Helper()
	price := testPrice()
	ledger, err := openSpendLedger(o.ledgerPath, o.capUSD)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var out, errb bytes.Buffer
	code := runEvalWith(o, price, ledger, &fakeProvider{price: price}, &out, &errb)
	b, err := os.ReadFile(filepath.Join(o.out, "summary.json"))
	if err != nil {
		t.Fatalf("summary: %v (stderr %s)", err, errb.String())
	}
	var sum evalSummary
	if err := json.Unmarshal(b, &sum); err != nil {
		t.Fatalf("summary json: %v", err)
	}
	return code, sum, sum.Results
}

// TestEvalRecallFakeSuite exercises the real turn path (assemble, retrieval,
// loop, ctxdb) over the committed E4 file without a network or a paid call.
func TestEvalRecallFakeSuite(t *testing.T) {
	t.Parallel()
	o := fakeRunOpts(t, t.TempDir(), 4)
	code, sum, results := fakeRun(t, o)
	if code != 0 {
		t.Fatalf("runEvalWith = %d", code)
	}
	if sum.Tasks != 4 || sum.Passed != 4 || sum.PassRate != 1 || !sum.GatePass {
		t.Fatalf("summary = %+v, want 4/4", sum)
	}
	if sum.Calls == 0 || sum.SpentUSD <= 0 || sum.CapUSD != 2 || !sum.Fake {
		t.Fatalf("projection = calls %d spent %.6f cap %.2f fake %v", sum.Calls, sum.SpentUSD, sum.CapUSD, sum.Fake)
	}
	if len(results) != 4 {
		t.Fatalf("results = %d lines", len(results))
	}
	// E4-001 must answer with the planted locker number, not echo the prompt.
	last := results[0].Answers[len(results[0].Answers)-1]
	if !strings.Contains(last, "4417") {
		t.Fatalf("E4-001 answer = %q, want the planted 4417", last)
	}
}

// TestEvalDryRunFull runs all 30 E4 tasks against the fake provider and logs
// the projections used before any paid run (DAMNBOT_EVAL_DRYRUN=1).
func TestEvalDryRunFull(t *testing.T) {
	if os.Getenv("DAMNBOT_EVAL_DRYRUN") == "" {
		t.Skip("set DAMNBOT_EVAL_DRYRUN=1 for the full dry run")
	}
	code, sum, _ := fakeRun(t, fakeRunOpts(t, t.TempDir(), 0))
	if code != 0 || sum.Tasks != 30 || sum.Passed != 30 {
		t.Fatalf("dry run code=%d sum=%+v", code, sum)
	}
	// Same seed, same scores: a second run is score-identical.
	_, sum2, _ := fakeRun(t, fakeRunOpts(t, t.TempDir(), 0))
	r1, err1 := json.Marshal(sum.Results)
	r2, err2 := json.Marshal(sum2.Results)
	if err1 != nil || err2 != nil {
		t.Fatalf("marshal results: %v / %v", err1, err2)
	}
	if sum.Passed != sum2.Passed || sum.Calls != sum2.Calls || sum.SpentUSD != sum2.SpentUSD || !bytes.Equal(r1, r2) {
		t.Fatalf("second dry run differs: %+v vs %+v", sum, sum2)
	}
	t.Logf("dry run: 30/30 pass, calls=%d, projected cost=%.6f USD, cap=%.2f", sum.Calls, sum.SpentUSD, sum.CapUSD)
}

// TestEvalCorrectionSupersedes checks the correction script end to end: the
// old row is retired, so the later question sees only the new value.
func TestEvalCorrectionSupersedes(t *testing.T) {
	t.Parallel()
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(t.TempDir(), "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	prov := &evalProvider{inner: &fakeProvider{price: price}, ledger: ledger, price: price}
	task := &evalTask{ID: "T-CORR", Kind: "correction-n+1", Steps: []evalStep{
		{Input: "Please remember this: my plant is monstera."},
		{Input: "Correction: my plant is now calathea, not monstera."},
		{Input: "What is my plant?", Check: `(?i)calathea`},
	}}
	res := runE4Task(context.Background(), task, &evalOptions{model: "openai/fake", timeoutS: 120}, prov, t.TempDir())
	if res.HarnessError || !res.Pass {
		t.Fatalf("task = %+v", res)
	}
	answer := res.Answers[len(res.Answers)-1]
	if !strings.Contains(answer, "calathea") || strings.Contains(strings.ToLower(answer), "monstera") {
		t.Fatalf("answer after correction = %q, want only the new value", answer)
	}
}

// TestEvalTaskMemoriesAreIsolated proves memories never leak between tasks:
// the second task runs in a fresh data dir and must not recall task one's fact.
func TestEvalTaskMemoriesAreIsolated(t *testing.T) {
	t.Parallel()
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(t.TempDir(), "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	prov := &evalProvider{inner: &fakeProvider{price: price}, ledger: ledger, price: price}
	out := t.TempDir()
	plant := &evalTask{ID: "T-A", Kind: "recall-n+1", Steps: []evalStep{
		{Input: "Please remember this: my locker number is 4417."},
	}}
	if r := runE4Task(context.Background(), plant, &evalOptions{model: "openai/fake", timeoutS: 120}, prov, out); r.HarnessError {
		t.Fatalf("plant task = %+v", r)
	}
	ask := &evalTask{ID: "T-B", Kind: "recall-n+1", Steps: []evalStep{
		{Input: "What is my locker number?", Check: `(?i)4417`},
	}}
	res := runE4Task(context.Background(), ask, &evalOptions{model: "openai/fake", timeoutS: 120}, prov, out)
	if res.HarnessError || res.Pass {
		t.Fatalf("isolated task = %+v, want a failing check (no leaked memory)", res)
	}
	if got := res.Answers[0]; got != "I don't know." {
		t.Fatalf("answer = %q, want no memory", got)
	}
}

type countingProvider struct {
	calls int
	usage event.Usage
}

func (c *countingProvider) Stream(context.Context, *loop.Request, func(loop.Delta)) (loop.Reply, error) {
	c.calls++
	return loop.Reply{Content: "x", FinishReason: loop.FinishStop, Usage: c.usage}, nil
}

// TestEvalProviderChargesAndFailsClosed checks the wrapper prices actual usage
// and never calls upstream once the cap would be crossed.
func TestEvalProviderChargesAndFailsClosed(t *testing.T) {
	t.Parallel()
	price := evalPrice{In: 1, Cached: 0.1, Out: 2} // easy numbers: USD per 1M
	ledger, err := openSpendLedger(filepath.Join(t.TempDir(), "l.json"), 1)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	inner := &countingProvider{usage: event.Usage{Input: 1000, CachedInput: 200, Output: 500}}
	p := &evalProvider{inner: inner, ledger: ledger, price: price}
	req := &loop.Request{Messages: []loop.Message{{Role: "user", Content: "hello"}}, MaxTokens: 10}
	if _, err := p.Stream(context.Background(), req, func(loop.Delta) {}); err != nil {
		t.Fatalf("stream: %v", err)
	}
	want := (800*1.0 + 200*0.1 + 500*2.0) / 1e6
	if spent := ledger.spent; spent != want || p.callN != 1 {
		t.Fatalf("spend = %.9f/%d, want %.9f/1", spent, p.callN, want)
	}
	// Force the cap with a worst case above it: the reservation must refuse
	// before the inner call.
	big := &loop.Request{Messages: req.Messages, MaxTokens: 2_000_000}
	before := inner.calls
	if _, err := p.Stream(context.Background(), big, func(loop.Delta) {}); !errors.Is(err, errEvalBudget) {
		t.Fatalf("stream over cap = %v, want errEvalBudget", err)
	}
	if inner.calls != before {
		t.Fatalf("inner called %d times over cap, want %d", inner.calls, before)
	}
}

// TestSpendLedgerCapAuthoritative: the cap stored in the file wins over the
// flag, and spent_usd is never reset.
func TestSpendLedgerCapAuthoritative(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "f2-spend.json")
	l, err := openSpendLedger(path, 2)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if serr := l.settle(1); serr != nil {
		t.Fatalf("settle: %v", serr)
	}
	reopened, err := openSpendLedger(path, 999)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.capUSD != 2 || reopened.spent != 1 {
		t.Fatalf("reopened cap/spent = %.2f/%.2f, want 2/1", reopened.capUSD, reopened.spent)
	}
	if err := reopened.reserve(1.5); !errors.Is(err, errEvalBudget) {
		t.Fatalf("reserve past cap = %v, want errEvalBudget", err)
	}
}

// TestLiveProviderBuilds reads a key file and builds the client without a
// network call (the request budget is the ledger's job).
func TestLiveProviderBuilds(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	key := filepath.Join(dir, "key")
	if err := os.WriteFile(key, []byte("sk-test-not-real"), 0o600); err != nil {
		t.Fatal(err)
	}
	prov, err := liveProvider(&evalOptions{keyFile: key, model: "openai/gpt-5.4-mini", baseURL: "https://api.teamorouter.com/v1"})
	if err != nil || prov == nil {
		t.Fatalf("liveProvider = %v, %v", prov, err)
	}
	if _, err := liveProvider(&evalOptions{keyFile: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing key file accepted")
	}
}

func TestParseRememberCorrection(t *testing.T) {
	t.Parallel()
	if what, val, ok := parseRemember("Please remember this: my locker number is 4417."); !ok || what != "locker number" || val != "4417" {
		t.Fatalf("remember = %q/%q/%v", what, val, ok)
	}
	what, val, old, ok := parseCorrection("Correction: my plant is now calathea, not monstera.")
	if !ok || what != "plant" || val != "calathea" || old != "monstera" {
		t.Fatalf("correction = %q/%q/%q/%v", what, val, old, ok)
	}
	if _, _, ok := parseRemember("How many minutes are in 3 hours?"); ok {
		t.Fatal("plain question parsed as a remember instruction")
	}
}
