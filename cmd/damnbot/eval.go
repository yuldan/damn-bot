// `damnbot eval recall` (GAP-INF-06 F2): the E4 cross-session memory runner.
// It drives the real headless turn path (retrieval, loop, ctxdb) once per
// scripted session, plants user facts through the memstore, and bounds live
// spend with the F2 ledger. TestEvalDryRunFull is the fake-provider dry run.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/assemble"
	"github.com/damnbot/damnbot/internal/budget"
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/memstore"
	"github.com/damnbot/damnbot/internal/loop"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/redact"
)

// evalOptions configures one run. Prices are USD per 1M tokens.
type evalOptions struct {
	evals, out, model, baseURL, keyFile, ledgerPath string
	capUSD, priceIn, priceCached, priceOut, gateMin float64
	limit, timeoutS                                 int
	fake                                            bool // tests: label a fake-provider run
}

func printTo(w io.Writer, format string, args ...any) {
	if _, err := fmt.Fprintf(w, format, args...); err != nil {
		return
	}
}

func parseEvalFlags(args []string, stderr io.Writer) (evalOptions, error) {
	var o evalOptions
	fs := flag.NewFlagSet("eval recall", flag.ContinueOnError)
	fs.SetOutput(stderr)
	fs.StringVar(&o.model, "model", "openai/gpt-5.4-mini", "provider/model")
	fs.StringVar(&o.baseURL, "base-url", "https://api.teamorouter.com/v1", "provider base URL")
	fs.StringVar(&o.keyFile, "key-file", "~/.config/damnbot/llm_key", "API key file (read at runtime, never printed)")
	fs.StringVar(&o.ledgerPath, "ledger", "bench/h2h/results/f2-spend.json", "spend ledger; the cap lives in the file")
	fs.StringVar(&o.out, "out", "", "artifact directory (default bench/h2h/results/f2-e4-<UTC>)")
	fs.Float64Var(&o.capUSD, "cap-usd", 2.00, "cap when creating a new ledger")
	fs.Float64Var(&o.priceIn, "price-in", 0.15, "USD per 1M input tokens")
	fs.Float64Var(&o.priceOut, "price-out", 0.60, "USD per 1M output tokens")
	if err := fs.Parse(args); err != nil {
		return o, &reportedParseError{err: fmt.Errorf("parse flags: %w", err)}
	}
	if fs.NArg() != 0 {
		return o, fmt.Errorf("unexpected arguments %q", fs.Args())
	}
	o.evals = "bench/evals"
	o.gateMin, o.timeoutS, o.priceCached = 0.90, 180, 0.015
	return o, nil
}

type spendLedger struct {
	path          string
	capUSD, spent float64
	reserved      float64
	calls         int
}

func openSpendLedger(path string, capUSD float64) (*spendLedger, error) {
	l := &spendLedger{path: path, capUSD: capUSD}
	if path == "" {
		return l, nil // tests only: no durable file
	}
	b, err := os.ReadFile(path) // #nosec G304 -- path is an explicit ledger argument
	if errors.Is(err, os.ErrNotExist) {
		if capUSD <= 0 {
			return nil, errors.New("eval ledger: cap must be > 0")
		}
		if serr := l.save(); serr != nil {
			return nil, serr
		}
		return l, nil
	}
	if err != nil {
		return nil, fmt.Errorf("eval ledger %s: %w", path, err)
	}
	var f struct {
		CapUSD   float64   `json:"cap_usd"`
		SpentUSD float64   `json:"spent_usd"`
		Updated  time.Time `json:"updated"`
	}
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("eval ledger %s: %w", path, err)
	}
	if f.CapUSD <= 0 {
		return nil, fmt.Errorf("eval ledger %s: cap_usd must be > 0", path)
	}
	l.capUSD, l.spent = f.CapUSD, f.SpentUSD
	return l, nil
}

// reserve records the next call's worst case, failing closed before the call.
func (l *spendLedger) reserve(worst float64) error {
	if l.spent+l.reserved+max(worst, 0) > l.capUSD {
		return fmt.Errorf("%w: spent %.6f + reserved %.6f + worst %.6f exceeds cap %.2f",
			errEvalBudget, l.spent, l.reserved, worst, l.capUSD)
	}
	l.reserved = worst
	return nil
}

func (l *spendLedger) settle(actual float64) error {
	l.spent += max(actual, 0)
	l.reserved = 0
	l.calls++
	return l.save()
}

func (l *spendLedger) save() error {
	if l.path == "" {
		return nil
	}
	b, err := json.Marshal(struct {
		CapUSD   float64   `json:"cap_usd"`
		SpentUSD float64   `json:"spent_usd"`
		Updated  time.Time `json:"updated"`
	}{l.capUSD, l.spent, time.Now().UTC()})
	if err != nil {
		return fmt.Errorf("eval ledger: %w", err)
	}
	if err := os.WriteFile(l.path+".tmp", append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("eval ledger write: %w", err)
	}
	if err := os.Rename(l.path+".tmp", l.path); err != nil {
		return fmt.Errorf("eval ledger rename: %w", err)
	}
	return nil
}

// evalPrice is the declared model price in USD per 1M tokens.
type evalPrice struct {
	In     float64 `json:"input"`
	Cached float64 `json:"cached_input"`
	Out    float64 `json:"output"`
}

func (p evalPrice) Valid() bool { return p.In > 0 && p.Out > 0 && p.Cached >= 0 }

func (p evalPrice) Cost(in, cached, out int64) float64 {
	return (float64(max(in-cached, 0))*p.In + float64(max(cached, 0))*p.Cached +
		float64(max(out, 0))*p.Out) / 1e6
}

var errEvalBudget = errors.New("eval budget cap reached")

// evalProvider gates every paid call: reserve the worst case before the call,
// settle the actual usage afterwards.
type evalProvider struct {
	inner  loop.Provider
	ledger *spendLedger
	price  evalPrice
	callN  int
}

func (p *evalProvider) Stream(ctx context.Context, req *loop.Request, emit func(loop.Delta)) (loop.Reply, error) {
	worst := p.price.Cost(estimateInputTokens(req), 0, int64(max(req.MaxTokens, 4096)))
	if err := p.ledger.reserve(worst); err != nil {
		return loop.Reply{}, err
	}
	p.callN++
	reply, err := p.inner.Stream(ctx, req, emit)
	if err != nil {
		if serr := p.ledger.settle(worst); serr != nil { // unknown usage: charge the bound
			return loop.Reply{}, serr
		}
		return loop.Reply{}, fmt.Errorf("eval provider: %w", err)
	}
	if serr := p.ledger.settle(usageCost(reply.Usage, p.price)); serr != nil {
		return reply, &loop.StreamError{Class: loop.ClassProvider, Err: serr}
	}
	return reply, nil
}

// estimateInputTokens approximates request tokens with the COST-09 estimator.
func estimateInputTokens(req *loop.Request) int64 {
	var b strings.Builder
	for _, m := range req.Messages {
		b.WriteString(m.Content)
		b.WriteByte('\n')
	}
	for _, t := range req.Tools {
		b.WriteString(t.Name)
		b.WriteString(t.Description)
		b.Write(t.Parameters)
	}
	return int64(budget.Count(b.String()).Raw())
}

func usageCost(u event.Usage, p evalPrice) float64 {
	if u.CostUSD != nil {
		return *u.CostUSD
	}
	return p.Cost(u.Input, u.CachedInput, u.Output)
}

// evalTask mirrors evalset/v1; only steps with a check are scored.
type evalTask struct {
	ID    string     `json:"id"`
	Kind  string     `json:"kind"`
	Steps []evalStep `json:"steps"`
}

type evalStep struct {
	Input string `json:"input"`
	Check string `json:"check"`
}

type evalTaskResult struct {
	ID           string   `json:"id"`
	Kind         string   `json:"kind"`
	Pass         bool     `json:"pass"`
	Skipped      string   `json:"skipped,omitempty"`
	Stale        bool     `json:"stale"`
	HarnessError bool     `json:"harness_error"`
	Answers      []string `json:"answers"` // one per step, in order
	Err          string   `json:"error,omitempty"`
	cause        error
}

type evalSummary struct {
	Model     string           `json:"model"`
	Fake      bool             `json:"fake"`
	Tasks     int              `json:"tasks"`
	Passed    int              `json:"passed"`
	PassRate  float64          `json:"pass_rate"`
	GateMin   float64          `json:"gate_min"`
	GatePass  bool             `json:"gate_pass"`
	Stale     int              `json:"stale"`
	Calls     int              `json:"calls"`
	SpentUSD  float64          `json:"spent_usd"`
	CapUSD    float64          `json:"cap_usd"`
	BudgetEnd bool             `json:"budget_stop"`
	Results   []evalTaskResult `json:"results"`
}

var (
	rememberRE   = regexp.MustCompile(`(?i)^Please remember this: my (.+?) is (.+)\.$`)
	correctionRE = regexp.MustCompile(`(?i)^Correction: my (.+?) is now (.+), not (.+)\.$`)
)

func runEvalRecall(args []string, stdout, stderr io.Writer) int {
	o, err := parseEvalFlags(args, stderr)
	if err != nil {
		if _, reported := errors.AsType[*reportedParseError](err); !reported {
			printTo(stderr, "eval: %s\n", err)
		}
		return 2
	}
	price := evalPrice{In: o.priceIn, Cached: o.priceCached, Out: o.priceOut}
	if !price.Valid() {
		printTo(stderr, "eval: price-in/price-out must be > 0 so the cap bounds spend\n")
		return 2
	}
	ledger, err := openSpendLedger(o.ledgerPath, o.capUSD)
	if err != nil {
		printTo(stderr, "eval: %s\n", err)
		return 2
	}
	inner, err := liveProvider(&o)
	if err != nil {
		printTo(stderr, "eval: %s\n", err)
		return 2
	}
	return runEvalWith(&o, price, ledger, inner, stdout, stderr)
}

// runEvalWith is the runner with the provider injected (tests use a fake).
func runEvalWith(o *evalOptions, price evalPrice, ledger *spendLedger, inner loop.Provider, stdout, stderr io.Writer) int {
	tasks, err := loadEvalTasks(filepath.Join(o.evals, "v1", "E4.jsonl"))
	if err != nil {
		printTo(stderr, "eval: %s\n", err)
		return 2
	}
	if !strings.Contains(o.model, "/") {
		o.model = "openai/" + o.model
	}
	if o.limit > 0 && len(tasks) > o.limit {
		tasks = tasks[:o.limit]
	}
	outDir := o.out
	if outDir == "" {
		outDir = filepath.Join("bench", "h2h", "results", "f2-e4-"+time.Now().UTC().Format("20060102T150405"))
	}
	if err := os.MkdirAll(outDir, 0o750); err != nil {
		printTo(stderr, "eval: %s\n", err)
		return 2
	}
	prov := &evalProvider{inner: inner, ledger: ledger, price: price}
	results := runE4Tasks(context.Background(), tasks, o, prov, outDir)
	sum := summarizeE4(results, o, ledger, prov.callN)
	sum.Results = results
	if err := writeEvalArtifacts(outDir, &sum); err != nil {
		printTo(stderr, "eval: %s\n", err)
		return 1
	}
	mode := "live"
	if o.fake {
		mode = "fake"
	}
	printTo(stdout, "eval recall (%s): %d/%d pass = %.1f%% (gate %.0f%%: %v), calls=%d spent=%.6f / cap %.2f USD\n",
		mode, sum.Passed, sum.Tasks, sum.PassRate*100, sum.GateMin*100, sum.GatePass, sum.Calls, sum.SpentUSD, sum.CapUSD)
	if sum.BudgetEnd {
		return 3
	}
	for _, r := range results {
		if r.HarnessError {
			return 1
		}
	}
	return 0
}

// liveProvider builds the OpenAI-compatible client; the key is never printed.
func liveProvider(o *evalOptions) (loop.Provider, error) {
	keyPath := o.keyFile
	if rest, ok := strings.CutPrefix(keyPath, "~/"); ok {
		home, err := os.UserHomeDir()
		if err != nil {
			return nil, fmt.Errorf("home dir: %w", err)
		}
		keyPath = filepath.Join(home, rest)
	}
	key, err := os.ReadFile(keyPath) // #nosec G304 -- path is an explicit key-file argument
	if err != nil {
		return nil, fmt.Errorf("api key file %s: %w", keyPath, err)
	}
	ref := o.model
	if !strings.Contains(ref, "/") {
		ref = "openai/" + ref
	}
	base, apiKey, family, err := providerEndpoint(ref, &runOptions{baseURL: o.baseURL, apiKey: strings.TrimSpace(string(key))})
	if err != nil {
		return nil, fmt.Errorf("provider endpoint: %w", err)
	}
	provName, modelName, _ := strings.Cut(ref, "/")
	client, err := provider.NewOpenAI(netx.New(netx.Config{}).Client("inference", netx.Inference), &provider.Config{
		Routes: []provider.Route{{
			Provider: provName, Model: modelName, BaseURL: base, APIKey: apiKey,
			ContextWindow: 128000, Family: family,
		}},
		Attempts: 1,
	})
	if err != nil {
		return nil, fmt.Errorf("provider: %w", err)
	}
	return &providerAdapter{client: client, model: ref}, nil
}

func loadEvalTasks(path string) ([]evalTask, error) {
	b, err := os.ReadFile(path) // #nosec G304 -- path is the eval-set directory argument
	if err != nil {
		return nil, fmt.Errorf("eval tasks: %w", err)
	}
	var out []evalTask
	for line := range strings.SplitSeq(string(b), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			var t evalTask
			if err := json.Unmarshal([]byte(line), &t); err != nil {
				return nil, fmt.Errorf("eval tasks %s: %w", path, err)
			}
			out = append(out, t)
		}
	}
	return out, nil
}

// runE4Tasks runs every task sequentially; a budget stop skips the rest.
func runE4Tasks(ctx context.Context, tasks []evalTask, o *evalOptions, prov loop.Provider, outDir string) []evalTaskResult {
	results := make([]evalTaskResult, 0, len(tasks))
	for i := range tasks {
		res := runE4Task(ctx, &tasks[i], o, prov, outDir)
		results = append(results, res)
		if errors.Is(res.cause, errEvalBudget) {
			for _, rest := range tasks[i+1:] {
				results = append(results, evalTaskResult{ID: rest.ID, Kind: rest.Kind, Skipped: "budget"})
			}
			break
		}
	}
	return results
}

// runE4Task plays one task's sessions in a task-private data dir (memories
// survive across sessions, never across tasks) and grades the scripted checks.
func runE4Task(ctx context.Context, t *evalTask, o *evalOptions, prov loop.Provider, outDir string) evalTaskResult {
	res := evalTaskResult{ID: t.ID, Kind: t.Kind, Pass: true}
	dataDir := filepath.Join(outDir, "data", t.ID)
	if err := os.RemoveAll(dataDir); err != nil {
		res.HarnessError = true
		return res
	}
	cfg := config.Default()
	red := redact.New(redact.Options{})
	facts := map[string]int64{}
	for i := range t.Steps {
		if !runE4Step(ctx, &res, &t.Steps[i], o, prov, dataDir, &cfg, red, facts) {
			return res
		}
	}
	res.Pass = res.Pass && !res.HarnessError
	return res
}

// runE4Step plays and grades one session; false stops the task (harness error
// or budget stop).
func runE4Step(ctx context.Context, res *evalTaskResult, st *evalStep, o *evalOptions, prov loop.Provider,
	dataDir string, cfg *config.Config, red *redact.Redactor, facts map[string]int64,
) bool {
	sctx, cancel := context.WithTimeout(ctx, time.Duration(o.timeoutS)*time.Second)
	var buf bytes.Buffer
	_, rerr := runOne(sctx, &runOptions{
		prompt: st.Input, dataDir: dataDir, model: o.model, output: "json", approval: "auto",
		cfgOverride: cfg, prov: prov,
		retrieverFor: func(db *ctxdb.Store) assemble.Retriever { return &evalRetriever{db: db} },
	}, &buf, &buf)
	cancel()
	var turn struct {
		SessionID string `json:"session_id"`
		Answer    string `json:"answer"`
	}
	if uerr := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &turn); uerr != nil {
		res.Err, res.HarnessError = fmt.Sprintf("turn result: %v", uerr), true
		return false
	}
	res.Answers = append(res.Answers, turn.Answer)
	if rerr != nil {
		res.Err, res.HarnessError = fmt.Sprintf("step %q: %v", st.Input, rerr), true
		if errors.Is(rerr, errEvalBudget) {
			res.Skipped, res.cause = "budget", rerr
		}
		return false
	}
	oldValue := plantFacts(ctx, res, st, turn.SessionID, dataDir, red, facts)
	gradeStep(res, st, turn.Answer, oldValue)
	return true
}

// plantFacts records the fact a session states; a correction supersedes the
// previous row and returns the corrected old value.
func plantFacts(ctx context.Context, res *evalTaskResult, st *evalStep, session, dataDir string,
	red *redact.Redactor, facts map[string]int64,
) (oldValue string) {
	plant := func(content string, supersedes *int64, what string) {
		id, err := plantMemory(ctx, dataDir, content, session, supersedes, red)
		if err != nil {
			res.Err, res.HarnessError = fmt.Sprintf("plant %q: %v", what, err), true
			return
		}
		facts[what] = id
	}
	if what, value, ok := parseRemember(st.Input); ok {
		plant("my "+what+" is "+value, nil, what)
	}
	if what, value, old, ok := parseCorrection(st.Input); ok {
		oldValue = old
		var supersedes *int64
		if id, ok := facts[what]; ok {
			supersedes = &id
		}
		plant("my "+what+" is "+value, supersedes, what)
	}
	return oldValue
}

// gradeStep applies the scripted check; a failing answer that repeats a
// corrected value marks the task stale (SCORECARD "fakta basi").
func gradeStep(res *evalTaskResult, st *evalStep, answer, oldValue string) {
	if st.Check == "" {
		return
	}
	re, err := regexp.Compile(st.Check)
	switch {
	case err != nil:
		res.Err, res.HarnessError = fmt.Sprintf("check %q: %v", st.Check, err), true
	case !re.MatchString(answer):
		res.Pass = false
		if oldValue != "" && strings.Contains(strings.ToLower(answer), strings.ToLower(oldValue)) {
			res.Stale = true
		}
	}
}

func parseRemember(input string) (what, value string, ok bool) {
	m := rememberRE.FindStringSubmatch(strings.TrimSpace(input))
	if m == nil {
		return "", "", false
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), true
}

func parseCorrection(input string) (what, value, old string, ok bool) {
	m := correctionRE.FindStringSubmatch(strings.TrimSpace(input))
	if m == nil {
		return "", "", "", false
	}
	return strings.TrimSpace(m[1]), strings.TrimSpace(m[2]), strings.TrimSpace(m[3]), true
}

// plantMemory writes one user-origin approved fact (superseding the previous
// row when given) through the production memstore.
func plantMemory(ctx context.Context, dataDir, content, session string, supersedes *int64, red *redact.Redactor) (int64, error) {
	scanJSON, err := memory.ScanText(red, content).JSON()
	if err != nil {
		return 0, fmt.Errorf("scan: %w", err)
	}
	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: dataDir, Kind: "cli"})
	if err != nil {
		return 0, fmt.Errorf("open task db: %w", err)
	}
	now := time.Now().UnixMilli()
	id, err := memstore.Put(ctx, db, &memstore.Row{
		Category: "fact", Scope: "user", Content: content, Origin: "user",
		SourceSession: session, SupersedesID: supersedes, ScanResult: scanJSON,
		CreatedAt: now, Status: "approved",
	}, &memstore.Decision{Status: "approved", Actor: "user", Reason: "e4 plant", CreatedAt: now}, red)
	if cerr := db.CloseContext(ctx); err == nil && cerr != nil {
		return 0, fmt.Errorf("close task db: %w", cerr)
	}
	if err != nil {
		return 0, fmt.Errorf("memstore put: %w", err)
	}
	return id, nil
}

// evalRetriever feeds the context tier from approved memories (SPEC §4.8/B-03).
type evalRetriever struct {
	db *ctxdb.Store
}

func (r *evalRetriever) Memories(ctx context.Context, query string) ([]assemble.Memory, error) {
	hits, err := memstore.Search(ctx, r.db, &memstore.Query{Text: query, Scopes: []string{"user"}, Limit: memory.DefaultRecall})
	if err != nil {
		return nil, fmt.Errorf("memstore search: %w", err)
	}
	out := make([]assemble.Memory, 0, len(hits))
	for i := range hits {
		out = append(out, assemble.Memory{Text: hits[i].Row.Content, Pinned: hits[i].Row.Pinned})
	}
	return out, nil
}

func (r *evalRetriever) Skills(context.Context, string) ([]string, error) { return nil, nil }

func summarizeE4(results []evalTaskResult, o *evalOptions, ledger *spendLedger, calls int) evalSummary {
	sum := evalSummary{
		Model: o.model, Fake: o.fake, GateMin: o.gateMin,
		Calls: calls, SpentUSD: ledger.spent, CapUSD: ledger.capUSD,
	}
	for _, r := range results {
		if r.Skipped == "budget" {
			sum.BudgetEnd = true
			continue
		}
		sum.Tasks++
		if r.Stale {
			sum.Stale++
		}
		if r.Pass {
			sum.Passed++
		}
	}
	if sum.Tasks > 0 {
		sum.PassRate = float64(sum.Passed) / float64(sum.Tasks)
	}
	sum.GatePass = sum.PassRate >= o.gateMin
	return sum
}

func writeEvalArtifacts(outDir string, sum *evalSummary) error {
	b, err := json.MarshalIndent(sum, "", " ")
	if err != nil {
		return fmt.Errorf("summary.json: %w", err)
	}
	if err := os.WriteFile(filepath.Join(outDir, "summary.json"), append(b, '\n'), 0o600); err != nil {
		return fmt.Errorf("summary.json: %w", err)
	}
	return nil
}
