package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
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
	want = max(want, price.Cost(estimateInputTokens(req), 0, int64(max(req.MaxTokens, 4096))))
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
	id, err := l.reserve(1)
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if serr := l.settle(id, 1, 1); serr != nil {
		t.Fatalf("settle: %v", serr)
	}
	reopened, err := openSpendLedger(path, 999)
	if err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if reopened.capUSD != 2 || reopened.spent != 1 {
		t.Fatalf("reopened cap/spent = %.2f/%.2f, want 2/1", reopened.capUSD, reopened.spent)
	}
	if _, err := reopened.reserve(1.5); !errors.Is(err, errEvalBudget) {
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
	prov, err := liveProvider(t.Context(), &evalOptions{keyFile: key, model: "openai/gpt-5.4-mini", baseURL: "https://api.teamorouter.com/v1"})
	if err != nil || prov == nil {
		t.Fatalf("liveProvider = %v, %v", prov, err)
	}
	if _, err := liveProvider(t.Context(), &evalOptions{keyFile: filepath.Join(dir, "missing")}); err == nil {
		t.Fatal("missing key file accepted")
	}
}

// zeroUsageProvider reports no usage at all (SEC14-1).
type zeroUsageProvider struct{ calls int }

func (p *zeroUsageProvider) Stream(context.Context, *loop.Request, func(loop.Delta)) (loop.Reply, error) {
	p.calls++
	return loop.Reply{Content: "ok", FinishReason: loop.FinishStop}, nil
}

// TestEvalSettlesWorstWithoutUsage: a provider that reports no usage must be
// charged the reserved worst case, so the cap still stops the next call.
func TestEvalSettlesWorstWithoutUsage(t *testing.T) {
	t.Parallel()
	price := evalPrice{In: 1, Cached: 0, Out: 1}
	ledger, err := openSpendLedger(filepath.Join(t.TempDir(), "l.json"), 0.15)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	inner := &zeroUsageProvider{}
	p := &evalProvider{inner: inner, ledger: ledger, price: price}
	req := &loop.Request{Messages: []loop.Message{{Role: "user", Content: "hello"}}, MaxTokens: 100_000}
	worst := price.Cost(estimateInputTokens(req), 0, 100_000)
	if _, err := p.Stream(context.Background(), req, func(loop.Delta) {}); err != nil {
		t.Fatalf("first stream: %v", err)
	}
	if spent := ledger.spent; spent != worst {
		t.Fatalf("spent = %.9f, want the reserved worst %.9f", spent, worst)
	}
	if _, err := p.Stream(context.Background(), req, func(loop.Delta) {}); !errors.Is(err, errEvalBudget) {
		t.Fatalf("second stream = %v, want errEvalBudget", err)
	}
	if inner.calls != 1 {
		t.Fatalf("provider calls = %d, want 1", inner.calls)
	}
}

// TestSpendLedgerConcurrentCap: two ledger instances (as separate processes)
// share one cap; the file never exceeds it (SEC14-4).
func TestSpendLedgerConcurrentCap(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "l.json")
	a, err := openSpendLedger(path, 1)
	if err != nil {
		t.Fatalf("open a: %v", err)
	}
	b, err := openSpendLedger(path, 1)
	if err != nil {
		t.Fatalf("open b: %v", err)
	}
	var wg sync.WaitGroup
	var granted atomic.Int64
	work := func(l *spendLedger) {
		defer wg.Done()
		for range 20 {
			id, rerr := l.reserve(0.1)
			if rerr != nil {
				continue
			}
			if serr := l.settle(id, 0, 0.1); serr != nil {
				t.Error(serr)
				return
			}
			granted.Add(1)
		}
	}
	wg.Add(2)
	go work(a)
	go work(b)
	wg.Wait()
	f, err := readEvalLedger(path)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got := granted.Load(); got != 10 {
		t.Fatalf("granted = %d, want 10", got)
	}
	if math.Abs(f.SpentUSD-1.0) > 1e-9 {
		t.Fatalf("spent = %.9f, want 1.0", f.SpentUSD)
	}
}

// writeAttemptProvider asks for one write_file per step; the second call
// records the tool result text before answering.
type writeAttemptProvider struct {
	step    int
	targets []string
	denied  string
}

func (p *writeAttemptProvider) Stream(_ context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	p.step++
	if i := p.step - 1; i < len(p.targets) {
		args := fmt.Sprintf(`{"path":%q,"content":"x"}`, p.targets[i])
		emit(loop.Delta{ToolCalls: []loop.ToolCallDelta{{Index: 0, ID: fmt.Sprintf("c%d", p.step), Name: "write_file", Arguments: args}}})
		return loop.Reply{FinishReason: loop.FinishToolCalls, ToolCalls: []loop.ToolCall{{ID: fmt.Sprintf("c%d", p.step), Name: "write_file", Arguments: args}}}, nil
	}
	for _, m := range req.Messages {
		if m.Role == "tool" {
			p.denied += m.Content
		}
	}
	emit(loop.Delta{Text: "done"})
	return loop.Reply{Content: "done", FinishReason: loop.FinishStop}, nil
}

// TestEvalWritesAreDenied: eval runs with approval "ask" (headless deny) and a
// cwd/root at the task data dir, so a model write inside the root is denied by
// approval and one in the repo is denied by the sandbox; the repo is untouched.
func TestEvalWritesAreDenied(t *testing.T) {
	t.Parallel()
	out := t.TempDir()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	repoFile := filepath.Join(wd, "sec14-write-escape.txt")
	if rerr := os.Remove(repoFile); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
		t.Fatal(rerr)
	}
	t.Cleanup(func() {
		if rerr := os.Remove(repoFile); rerr != nil && !errors.Is(rerr, os.ErrNotExist) {
			t.Error(rerr)
		}
	})
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(t.TempDir(), "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	dataDir, err := evalDataDir(out, "T-WRITE")
	if err != nil {
		t.Fatalf("data dir: %v", err)
	}
	inside := filepath.Join(dataDir, "inside.txt")
	prov := &writeAttemptProvider{targets: []string{inside, repoFile}}
	task := &evalTask{ID: "T-WRITE", Kind: "write", Steps: []evalStep{{Input: "try to write"}}}
	res := runE4Task(context.Background(), task, &evalOptions{model: "openai/fake", timeoutS: 60}, &evalProvider{inner: prov, ledger: ledger, price: price}, out)
	if res.HarnessError {
		t.Fatalf("task = %+v", res)
	}
	if _, err := os.Stat(inside); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write inside the task dir was not denied: %v", err)
	}
	if _, err := os.Stat(repoFile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("write into the repo was not denied: %v", err)
	}
	if !strings.Contains(strings.ToLower(prov.denied), "denied") && !strings.Contains(strings.ToLower(prov.denied), "approval") {
		t.Fatalf("tool results = %q, want a denial", prov.denied)
	}
}

// TestEvalRejectsUnsafeTaskIDs: ids become path components; separators, dots
// and traversal are refused before anything under the id can be removed
// (SEC14-3).
func TestEvalRejectsUnsafeTaskIDs(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	for _, id := range []string{"../victim", "a/b", "a.b", "", ".hidden", "a\\b"} {
		if _, err := evalDataDir(filepath.Join(dir, "out"), id); err == nil {
			t.Fatalf("id %q accepted", id)
		}
	}
	// The end-to-end path refuses the file and leaves the victim intact.
	victim := filepath.Join(dir, "victim")
	if err := os.MkdirAll(victim, 0o750); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(victim, "keep")
	if err := os.WriteFile(keep, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	evals := filepath.Join(dir, "evals", "v1")
	if err := os.MkdirAll(evals, 0o750); err != nil {
		t.Fatal(err)
	}
	line := `{"id":"../victim","kind":"recall-n+1","steps":[{"input":"x"}]}` + "\n"
	if err := os.WriteFile(filepath.Join(evals, "E4.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(dir, "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var out, errb bytes.Buffer
	code := runEvalWith(&evalOptions{evals: filepath.Join(dir, "evals"), out: filepath.Join(dir, "out"), model: "openai/fake", timeoutS: 60, gateMin: 0.9}, price, ledger, &fakeProvider{price: price}, &out, &errb)
	if code != 2 {
		t.Fatalf("runEvalWith = %d, want 2 (invalid task id); stderr=%s", code, errb.String())
	}
	if _, err := os.Stat(keep); err != nil {
		t.Fatalf("victim was removed: %v", err)
	}
}

// TestEvalRedactsErrorsInSummary: error text written to summary.json passes
// through the redactor (SEC14-6). The invalid check is a harness-generated
// error (not redacted by the loop), so it exercises the summary writer.
func TestEvalRedactsErrorsInSummary(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	evals := filepath.Join(dir, "evals", "v1")
	if err := os.MkdirAll(evals, 0o750); err != nil {
		t.Fatal(err)
	}
	const secret = "sk-SECRET-abcdefghijklmnopqrstuvwx"
	line := fmt.Sprintf(`{"id":"T-ERR","kind":"recall-n+1","steps":[{"input":"x","check":%q}]}`, "("+secret) + "\n"
	if err := os.WriteFile(filepath.Join(evals, "E4.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(dir, "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var out, errb bytes.Buffer
	o := &evalOptions{evals: filepath.Join(dir, "evals"), out: filepath.Join(dir, "out"), model: "openai/fake", timeoutS: 60, gateMin: 0.9, priceCached: 0.015}
	code := runEvalWith(o, price, ledger, &fakeProvider{price: price}, &out, &errb)
	if code != 1 {
		t.Fatalf("runEvalWith = %d, want 1 (harness error); stderr=%s", code, errb.String())
	}
	b, err := os.ReadFile(filepath.Join(dir, "out", "summary.json")) // #nosec G304 -- artifact under t.TempDir()
	if err != nil {
		t.Fatalf("summary: %v", err)
	}
	if strings.Contains(string(b), secret) {
		t.Fatalf("summary leaks the secret: %s", b)
	}
}

// TestEvalKeyFilePermissions: the key file must not be group/other readable
// (SEC14-6). Windows ACLs are not represented by mode bits. The --base-url
// seam pins openai-chat, the adapter both builds wire: the openai preset
// itself speaks openai-responses and the minimal build refuses it during
// resolution, before this test's permission check could be reached.
func TestEvalKeyFilePermissions(t *testing.T) {
	t.Parallel()
	if runtime.GOOS == "windows" {
		t.Skip("mode bits are not ACLs on Windows")
	}
	dir := t.TempDir()
	open := filepath.Join(dir, "open")
	if err := os.WriteFile(open, []byte("sk-test"), 0o644); err != nil { // #nosec G306 -- deliberately readable to test rejection
		t.Fatal(err)
	}
	if _, err := liveProvider(t.Context(), &evalOptions{keyFile: open, model: "openai/gpt-5.4-mini", baseURL: "http://127.0.0.1:9/v1"}); err == nil {
		t.Fatal("0644 key file accepted")
	}
	closed := filepath.Join(dir, "closed")
	if err := os.WriteFile(closed, []byte("sk-test"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := liveProvider(t.Context(), &evalOptions{keyFile: closed, model: "openai/gpt-5.4-mini", baseURL: "http://127.0.0.1:9/v1"}); err != nil {
		t.Fatalf("0600 key file refused: %v", err)
	}
}

// oneTaskEval writes a one-step eval set under dir and returns the evals path.
func oneTaskEval(t *testing.T, dir, id string) string {
	t.Helper()
	evals := filepath.Join(dir, "evals", "v1")
	if err := os.MkdirAll(evals, 0o750); err != nil {
		t.Fatal(err)
	}
	line := fmt.Sprintf(`{"id":%q,"kind":"recall-n+1","steps":[{"input":"hi"}]}`, id) + "\n"
	if err := os.WriteFile(filepath.Join(evals, "E4.jsonl"), []byte(line), 0o600); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(dir, "evals")
}

// TestEvalWritesOutGitignore: every --out gets a data/ .gitignore, so task
// SQLite dirs stay out of git wherever the artifacts are written (SEC14-6b).
func TestEvalWritesOutGitignore(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	out := filepath.Join(dir, "out")
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(dir, "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	var stdout, stderr bytes.Buffer
	o := &evalOptions{evals: oneTaskEval(t, dir, "T-GI"), out: out, model: "openai/fake", timeoutS: 60, gateMin: 0.9, priceCached: 0.015}
	if code := runEvalWith(o, price, ledger, &fakeProvider{price: price}, &stdout, &stderr); code != 0 {
		t.Fatalf("runEvalWith = %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(out, ".gitignore")) // #nosec G304 -- artifact under t.TempDir()
	if err != nil {
		t.Fatalf("out .gitignore: %v", err)
	}
	if string(b) != "data/\n" {
		t.Fatalf(".gitignore = %q, want data/", b)
	}
}

// TestEvalSummaryReportsPrices: summary.json names the prices used for the
// spend projection (SEC14-6d).
func TestEvalSummaryReportsPrices(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	price := testPrice()
	ledger, err := openSpendLedger(filepath.Join(dir, "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	o := &evalOptions{evals: oneTaskEval(t, dir, "T-PRICE"), out: out, model: "openai/fake", timeoutS: 60, gateMin: 0.9, priceCached: 0.015}
	if code := runEvalWith(o, price, ledger, &fakeProvider{price: price}, &stdout, &stderr); code != 0 {
		t.Fatalf("runEvalWith = %d: %s", code, stderr.String())
	}
	b, err := os.ReadFile(filepath.Join(out, "summary.json")) // #nosec G304 -- artifact under t.TempDir()
	if err != nil {
		t.Fatal(err)
	}
	var doc struct {
		Price struct {
			In     float64 `json:"input"`
			Cached float64 `json:"cached_input"`
			Out    float64 `json:"output"`
		} `json:"price_usd_per_mtok"`
	}
	if uerr := json.Unmarshal(b, &doc); uerr != nil {
		t.Fatal(uerr)
	}
	if doc.Price.In != 0.15 || doc.Price.Cached != 0.015 || doc.Price.Out != 0.60 {
		t.Fatalf("price = %+v, want 0.15/0.015/0.60", doc.Price)
	}
}

// TestEvalWarnsStaleReservations: a reservation left by an earlier run is
// reported at startup, never auto-expired (SEC14-7).
func TestEvalWarnsStaleReservations(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	ledger, err := openSpendLedger(filepath.Join(dir, "l.json"), 2)
	if err != nil {
		t.Fatalf("ledger: %v", err)
	}
	if _, rerr := ledger.reserve(0.5); rerr != nil {
		t.Fatalf("reserve: %v", rerr)
	}
	price := testPrice()
	out := filepath.Join(dir, "out")
	var stdout, stderr bytes.Buffer
	o := &evalOptions{evals: oneTaskEval(t, dir, "T-STALE"), out: out, model: "openai/fake", timeoutS: 60, gateMin: 0.9, priceCached: 0.015}
	if code := runEvalWith(o, price, ledger, &fakeProvider{price: price}, &stdout, &stderr); code != 0 {
		t.Fatalf("runEvalWith = %d: %s", code, stderr.String())
	}
	if msg := stderr.String(); !strings.Contains(msg, "1 in-flight ledger reservation") || !strings.Contains(msg, "0.500000") {
		t.Fatalf("stderr = %q, want the stale-reservation warning", msg)
	}
	f, err := readEvalLedger(filepath.Join(dir, "l.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Reserved) != 1 {
		t.Fatalf("reservations = %d, want the stale one kept", len(f.Reserved))
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
