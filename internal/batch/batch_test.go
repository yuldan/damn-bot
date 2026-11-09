package batch

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeRunner records calls, tracks peak concurrency, and returns a scripted
// reply per id. No model or network is involved.
type fakeRunner struct {
	mu      sync.Mutex
	calls   []string
	active  atomic.Int64
	peak    atomic.Int64
	release <-chan struct{}
	failID  string
}

func (f *fakeRunner) Run(_ context.Context, req *Request) (Reply, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req.ID)
	f.mu.Unlock()
	n := f.active.Add(1)
	for {
		p := f.peak.Load()
		if n <= p || f.peak.CompareAndSwap(p, n) {
			break
		}
	}
	if f.release != nil {
		<-f.release
	}
	f.active.Add(-1)
	if req.ID == f.failID {
		return Reply{}, errors.New("scripted failure")
	}
	usage := Usage{Input: int64(len(req.Prompt)), Output: 3}
	return Reply{Steps: []Step{{Role: "assistant", Content: "echo:" + req.Prompt}}, Usage: usage}, nil
}

type fakeLedger struct {
	mu    sync.Mutex
	items []string
	usage []Usage
}

func (l *fakeLedger) Record(_ context.Context, req *Request, u Usage) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.items = append(l.items, req.ID)
	l.usage = append(l.usage, u)
	return nil
}

// TestF8OPS020BatchRun proves bounded parallelism, per-item route/model
// override, the ledger call per executed item, and deterministic artifact
// writes without any LLM call.
func TestF8OPS020BatchRun(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	release := make(chan struct{})
	run := &fakeRunner{release: release}
	led := &fakeLedger{}
	b, err := New(&Config{OutDir: dir, Parallel: 2, Model: "openai/base", Route: "openai", Ledger: led})
	if err != nil {
		t.Fatal(err)
	}
	items, err := b.Load(strings.NewReader(`{"id":"a","prompt":"one"}
{"id":"b","prompt":"two","model":"openai/alt","route":"alt"}
{"id":"c","prompt":"three"}
{"id":"d","prompt":"four"}
`))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan []Result, 1)
	go func() {
		res, rerr := b.Run(context.Background(), run, items)
		if rerr != nil {
			done <- nil
			return
		}
		done <- res
	}()
	deadline := time.After(5 * time.Second)
	for run.active.Load() < 2 {
		select {
		case <-deadline:
			t.Fatal("runner never reached the parallel bound")
		default:
		}
	}
	if run.active.Load() > 2 {
		t.Fatalf("active = %d, want <= 2", run.active.Load())
	}
	close(release)
	res := <-done
	if res == nil {
		t.Fatal("run failed")
	}
	if run.peak.Load() > 2 {
		t.Fatalf("peak = %d, want <= parallel", run.peak.Load())
	}
	assertBatchRun(t, res, led, dir)
}

func assertBatchRun(t *testing.T, res []Result, led *fakeLedger, dir string) {
	t.Helper()
	if len(res) != 4 || res[0].ID != "a" || res[3].ID != "d" {
		t.Fatalf("results = %+v", res)
	}
	if res[1].Model != "openai/alt" || res[1].Route != "alt" {
		t.Fatalf("per-item override = %+v", res[1])
	}
	if len(led.items) != 4 {
		t.Fatalf("ledger items = %v, want one per executed item", led.items)
	}
	for i := range res {
		if res[i].Err != "" || len(res[i].Steps) == 0 {
			t.Fatalf("result %s = %+v", res[i].ID, &res[i])
		}
	}
	raw, err := os.ReadFile(filepath.Join(dir, "results.jsonl")) // #nosec G304 -- test temp output
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; got != 4 {
		t.Fatalf("results.jsonl rows = %d, want 4", got)
	}
	if !strings.Contains(string(raw), `"input_tokens"`) {
		t.Fatalf("results rows lack accounting: %s", raw)
	}
}

// TestF8OPS020BatchResume proves idempotent resume: a second Run executes no
// item twice, appends no duplicate rows, and keeps per-item costs once.
func TestF8OPS020BatchResume(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	led := &fakeLedger{}
	cfg := &Config{OutDir: dir, Parallel: 2, Resume: true, Model: "m", Ledger: led}
	b, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	items, err := b.Load(strings.NewReader(`{"id":"x","prompt":"p1"}
{"id":"y","prompt":"p2"}
`))
	if err != nil {
		t.Fatal(err)
	}
	first := &fakeRunner{}
	if _, rerr := b.Run(context.Background(), first, items); rerr != nil {
		t.Fatal(rerr)
	}
	second := &fakeRunner{}
	b2, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	res, rerr := b2.Run(context.Background(), second, items)
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(second.calls) != 0 {
		t.Fatalf("resume re-ran %v", second.calls)
	}
	for _, r := range res {
		if len(r.Steps) == 0 {
			t.Fatalf("result %s lost its resumed trajectory", r.ID)
		}
	}
	if got := len(led.items); got != 2 {
		t.Fatalf("ledger entries = %d, want 2 (one per item, never on resume)", got)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "results.jsonl")) // #nosec G304 -- test temp output
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Count(strings.TrimSpace(string(raw)), "\n") + 1; got != 2 {
		t.Fatalf("results.jsonl rows = %d, want 2 (idempotent)", got)
	}
}

// TestF8OPS020BatchRunner covers the ledger-row test required by the F8
// ledger: a failed item records its error, does not charge the ledger, and
// does not abort its siblings.
func TestF8OPS020BatchRunner(t *testing.T) {
	t.Parallel()
	led := &fakeLedger{}
	b, err := New(&Config{OutDir: t.TempDir(), Parallel: 2, Ledger: led})
	if err != nil {
		t.Fatal(err)
	}
	items, err := b.Load(strings.NewReader(`{"id":"ok","prompt":"fine"}
{"id":"bad","prompt":"boom"}
`))
	if err != nil {
		t.Fatal(err)
	}
	res, err := b.Run(context.Background(), &fakeRunner{failID: "bad"}, items)
	if err != nil {
		t.Fatal(err)
	}
	if res[0].Err != "" || res[1].Err == "" {
		t.Fatalf("results = %+v", res)
	}
	if len(led.items) != 1 || led.items[0] != "ok" {
		t.Fatalf("ledger = %v, want only the successful item", led.items)
	}
}

func TestBatchLoadRejectsBadItems(t *testing.T) {
	t.Parallel()
	b, err := New(&Config{OutDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	cases := []string{
		`{"id":"a","prompt":""}`,
		`{"id":"../a","prompt":"x"}`,
		`{"id":"a","prompt":"x"}
{"id":"a","prompt":"y"}`,
		`not json`,
	}
	for _, raw := range cases {
		if _, err := b.Load(strings.NewReader(raw)); err == nil {
			t.Errorf("Load(%q) accepted invalid input", raw)
		}
	}
}

// TestF8OPS021Trajectory proves both views are valid, carry every step, and
// replay read-only without a provider or tool.
func TestF8OPS021Trajectory(t *testing.T) {
	t.Parallel()
	tr := &Trajectory{ID: "t1", Model: "openai/fake", Steps: []Step{
		{Role: "system", Content: "be brief"},
		{Role: "user", Content: "hello"},
		{Role: "assistant", Content: "hi"},
	}}
	var openai, sharegpt bytes.Buffer
	if err := WriteTrajectory(&openai, tr, FormatOpenAI); err != nil {
		t.Fatal(err)
	}
	if err := WriteTrajectory(&sharegpt, tr, FormatShareGPT); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(openai.String(), `"messages"`) || !strings.Contains(openai.String(), `"assistant"`) {
		t.Fatalf("openai view = %s", openai.String())
	}
	if !strings.Contains(sharegpt.String(), `"from":"human"`) || !strings.Contains(sharegpt.String(), `"from":"gpt"`) {
		t.Fatalf("sharegpt view = %s", sharegpt.String())
	}
	back, err := ReadTrajectory(&openai)
	if err != nil {
		t.Fatal(err)
	}
	rep, err := NewReplay(back)
	if err != nil {
		t.Fatal(err)
	}
	var roles []string
	for {
		s, ok := rep.Next()
		if !ok {
			break
		}
		roles = append(roles, s.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant" {
		t.Fatalf("replay roles = %v", roles)
	}
	if back.Model != "openai/fake" {
		t.Fatalf("model = %q", back.Model)
	}
	bad := &Trajectory{ID: "t2", Steps: []Step{{Role: "assistant", Content: "x"}, {Role: "user", Content: "y"}}}
	if _, err := NewReplay(bad); err == nil {
		t.Fatal("replay accepted a trajectory not ending with the assistant")
	}
}

// TestF8OPS022Trajectory is the package-level name recorded by the OPS-022
// line: files land in both formats for every completed item.
func TestF8OPS022Trajectory(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b, err := New(&Config{OutDir: dir, Parallel: 1, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	items, err := b.Load(strings.NewReader(`{"id":"i1","prompt":"p"}`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.Run(context.Background(), &fakeRunner{}, items); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"i1.openai.json", "i1.sharegpt.json"} {
		raw, rerr := os.ReadFile(filepath.Join(dir, "trajectories", name)) // #nosec G304 -- test temp output
		if rerr != nil {
			t.Fatalf("%s: %v", name, rerr)
		}
		if len(raw) == 0 {
			t.Fatalf("%s is empty", name)
		}
	}
}

// TestF8OPS021BatchCompress proves compression is deterministic, bounded,
// and does not mutate the saved trajectory.
func TestF8OPS021BatchCompress(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	b, err := New(&Config{OutDir: dir, Parallel: 1, Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	long := strings.Repeat("abcdefghij", 100)
	run := &fakeRunner{}
	items := []Item{{ID: "big", Prompt: long}}
	if _, rerr := b.Run(context.Background(), run, items); rerr != nil {
		t.Fatal(rerr)
	}
	first, cerr := CompressDir(dir, 50)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if first.Items != 1 || first.Truncated != 1 || first.CompressedChars >= first.OriginalChars {
		t.Fatalf("summary = %+v", first)
	}
	again, cerr := CompressDir(dir, 50)
	if cerr != nil {
		t.Fatal(cerr)
	}
	if first != again {
		t.Fatalf("compress not idempotent: %+v vs %+v", first, again)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "trajectories", "big.openai.json")) // #nosec G304 -- test temp output
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), long) {
		t.Fatal("compression mutated the saved trajectory")
	}
	comp, err := os.ReadFile(filepath.Join(dir, "trajectories", "big.openai.compressed.json")) // #nosec G304 -- test temp output
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(comp), "trimmed") {
		t.Fatalf("compressed view lacks the truncation marker:\n%s", comp)
	}
}
