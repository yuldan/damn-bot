package evals

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/batch"
)

// fixtureProvider answers the committed suite without a network or model.
type fixtureProvider struct {
	cost  float64
	wrong map[string]string // case id -> wrong answer
}

func (f *fixtureProvider) Eval(_ context.Context, c Case, _ int64) (Reply, error) {
	if wrong, ok := f.wrong[c.ID]; ok {
		return Reply{Text: wrong, CostUSD: f.cost}, nil
	}
	return Reply{Text: c.Want, CostUSD: f.cost}, nil
}

// TestF8OPS024Evals runs the committed suite, checks determinism and the
// scorecard adapter, and proves a broken case is a regression.
func TestF8OPS024Evals(t *testing.T) {
	t.Parallel()
	res, err := Run(context.Background(), Fixtures(), &fixtureProvider{}, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	if res.Overall != 1 || len(res.Cases) != 3 {
		t.Fatalf("result = %+v", res)
	}
	rep := res.Scorecard()
	if len(rep.Dimensions) != 3 || rep.Score != 1 {
		t.Fatalf("scorecard = %+v", rep)
	}
	base, err := LoadBaseline("baseline.json")
	if err != nil {
		t.Fatal(err)
	}
	if regs := Compare(base, AsBaseline(res)); len(regs) != 0 {
		t.Fatalf("baseline regressions = %+v", regs)
	}
	broken := &fixtureProvider{wrong: map[string]string{"recall": "0000"}}
	bad, err := Run(context.Background(), Fixtures(), broken, Options{Seed: 1})
	if err != nil {
		t.Fatal(err)
	}
	regs := Compare(base, AsBaseline(bad))
	if len(regs) != 1 || regs[0].ID != "recall" {
		t.Fatalf("regressions = %+v", regs)
	}
}

// TestF8EvalDeterministic proves a repeated run is byte-identical.
func TestF8EvalDeterministic(t *testing.T) {
	t.Parallel()
	first, err := Run(context.Background(), Fixtures(), &fixtureProvider{}, Options{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	second, err := Run(context.Background(), Fixtures(), &fixtureProvider{}, Options{Seed: 7})
	if err != nil {
		t.Fatal(err)
	}
	a, err := first.JSON()
	if err != nil {
		t.Fatal(err)
	}
	b, err := second.JSON()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		t.Fatalf("same seed differs:\n%s\n%s", a, b)
	}
	if !strings.Contains(string(a), `"seed": 7`) {
		t.Fatalf("result lacks the seed:\n%s", a)
	}
}

// TestF8EvalCostCap proves a live run stops at max_cost_usd and never reports
// more cases than it paid for.
func TestF8EvalCostCap(t *testing.T) {
	t.Parallel()
	res, err := Run(context.Background(), Fixtures(), &fixtureProvider{cost: 3}, Options{Seed: 1, MaxCostUSD: 5})
	if !errors.Is(err, ErrCostCap) {
		t.Fatalf("err = %v, want ErrCostCap", err)
	}
	if !res.Stopped || len(res.Cases) != 2 || res.CostUSD != 6 {
		t.Fatalf("result = %+v", res)
	}
}

type patchSolver struct {
	override map[string]map[string]string
	failID   string
}

func (p *patchSolver) Solve(_ context.Context, t SWETask) (Patch, error) {
	if t.ID == p.failID {
		return Patch{}, errors.New("solver failed")
	}
	if files, ok := p.override[t.ID]; ok {
		return Patch{Files: files}, nil
	}
	return Patch{Files: t.Want}, nil
}

func sweTasks() []SWETask {
	return []SWETask{
		{
			ID: "swe-1", Issue: "fix sum", Files: map[string]string{"sum.go": "func Sum(a,b int) int { return a-b }"},
			Want: map[string]string{"sum.go": "func Sum(a,b int) int { return a+b }"},
		},
		{
			ID: "swe-2", Issue: "fix index", Files: map[string]string{"index.go": "return xs[i+1]"},
			Want: map[string]string{"index.go": "return xs[i]"},
		},
		{
			ID: "swe-3", Issue: "guard empty", Files: map[string]string{"guard.go": "return xs[0]"},
			Want: map[string]string{"guard.go": "if len(xs)==0 { return 0 }; return xs[0]"},
		},
	}
}

// TestF8OPS023MiniSWE proves the subset selection, a deterministic pass
// score, and a Hermes-shaped trajectory per task. A wrong or failed patch
// scores zero instead of panicking.
func TestF8OPS023MiniSWE(t *testing.T) {
	t.Parallel()
	tasks, err := Subset(sweTasks(), []string{"swe-1", "swe-3"})
	if err != nil || len(tasks) != 2 || tasks[1].ID != "swe-3" {
		t.Fatalf("subset = %+v, %v", tasks, err)
	}
	if _, err := Subset(sweTasks(), []string{"swe-9"}); err == nil {
		t.Fatal("unknown subset id accepted")
	}
	res, rerr := RunMiniSWE(context.Background(), tasks, &patchSolver{})
	if rerr != nil {
		t.Fatal(rerr)
	}
	if res.Tasks != 2 || res.Passed != 2 || len(res.Trajectories) != 2 {
		t.Fatalf("result = %+v", res)
	}
	var buf strings.Builder
	if werr := batch.WriteTrajectory(&buf, &res.Trajectories[0], batch.FormatOpenAI); werr != nil {
		t.Fatalf("trajectory: %v", werr)
	}
	bad, berr := RunMiniSWE(context.Background(), tasks, &patchSolver{
		override: map[string]map[string]string{"swe-1": {"sum.go": "unchanged"}},
		failID:   "swe-3",
	})
	if berr != nil {
		t.Fatal(berr)
	}
	if bad.Passed != 0 {
		t.Fatalf("bad result = %+v", bad)
	}
}
