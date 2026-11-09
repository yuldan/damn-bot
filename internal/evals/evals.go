// Package evals is the deterministic eval harness of SPEC §18.5 and
// GAP-INF-06 (OPS-024): a fixed seed, a caller-supplied provider (a fake or
// recorded fixture in tests), byte-identical results for the same seed, a
// committed baseline, and a per-run cost cap. It also carries the mini SWE
// subset runner (OPS-023) and adapts a result to the scorecard job.
package evals

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"

	"github.com/damnbot/damnbot/internal/scorecard"
)

// GAP-INF-06 regression thresholds: fail on a drop > 2 pp absolute or > 3%
// relative, whichever is tighter.
const (
	DefaultAbsDropPP = 0.02
	DefaultRelDrop   = 0.03
)

// ErrCostCap marks a run stopped by MaxCostUSD.
var ErrCostCap = errors.New("evals: cost cap reached")

// Case is one deterministic probe. Want is compared with the trimmed reply.
type Case struct {
	ID     string  `json:"id"`
	Prompt string  `json:"prompt"`
	Want   string  `json:"want"`
	Weight float64 `json:"weight"`
}

// Suite is an ordered case list; order is the determinism contract.
type Suite struct {
	ID    string `json:"id"`
	Cases []Case `json:"cases"`
}

// Reply is one provider answer and its reported cost.
type Reply struct {
	Text    string  `json:"text"`
	CostUSD float64 `json:"cost_usd"`
}

// Provider answers one case; seed is passed so a recorded fixture can pick a
// deterministic variant.
type Provider interface {
	Eval(ctx context.Context, c Case, seed int64) (Reply, error)
}

// CaseResult is one scored case.
type CaseResult struct {
	ID      string  `json:"id"`
	Pass    bool    `json:"pass"`
	Score   float64 `json:"score"`
	CostUSD float64 `json:"cost_usd"`
	Error   string  `json:"error,omitempty"`
}

// Result is a complete, deterministic run.
type Result struct {
	Suite   string       `json:"suite"`
	Seed    int64        `json:"seed"`
	Cases   []CaseResult `json:"cases"`
	Overall float64      `json:"overall"`
	CostUSD float64      `json:"cost_usd"`
	Stopped bool         `json:"stopped,omitempty"`
}

// Options bounds one run. MaxCostUSD <= 0 means the documented US$5,00 cap.
type Options struct {
	Seed       int64
	MaxCostUSD float64
}

const defaultMaxCostUSD = 5.00

// Run executes cases in declared order. A provider error fails its case and
// the run continues; a cost overrun stops the run at the cap (no case is
// invented after it).
func Run(ctx context.Context, s Suite, p Provider, o Options) (Result, error) {
	if p == nil {
		return Result{}, errors.New("evals: provider required")
	}
	if o.MaxCostUSD <= 0 {
		o.MaxCostUSD = defaultMaxCostUSD
	}
	res := Result{Suite: s.ID, Seed: o.Seed, Cases: make([]CaseResult, 0, len(s.Cases))}
	var weighted, weightSum float64
	for _, c := range s.Cases {
		reply, err := p.Eval(ctx, c, o.Seed)
		res.CostUSD += reply.CostUSD
		cr := CaseResult{ID: c.ID, CostUSD: reply.CostUSD}
		w := c.Weight
		if w <= 0 {
			w = 1
		}
		weightSum += w
		switch {
		case err != nil:
			cr.Error = err.Error()
		case strings.TrimSpace(reply.Text) == strings.TrimSpace(c.Want):
			cr.Pass, cr.Score = true, 1
		}
		weighted += cr.Score * w
		res.Cases = append(res.Cases, cr)
		if res.CostUSD > o.MaxCostUSD {
			res.Stopped = true
			break
		}
	}
	if weightSum > 0 {
		res.Overall = weighted / weightSum
	}
	if res.Stopped {
		return res, fmt.Errorf("%w: %.4f > %.2f", ErrCostCap, res.CostUSD, o.MaxCostUSD)
	}
	return res, nil
}

// JSON renders the result deterministically (struct order, no maps).
func (r Result) JSON() ([]byte, error) {
	raw, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("evals: encode result: %w", err)
	}
	return append(raw, '\n'), nil
}

// Baseline is the committed reference score of one suite and seed.
type Baseline struct {
	Suite   string             `json:"suite"`
	Seed    int64              `json:"seed"`
	Overall float64            `json:"overall"`
	Cases   map[string]float64 `json:"cases"`
}

// Regression is one case whose score dropped past the GAP-INF-06 threshold.
type Regression struct {
	ID   string  `json:"id"`
	Base float64 `json:"base"`
	Cur  float64 `json:"cur"`
	Abs  float64 `json:"abs_drop"`
	Rel  float64 `json:"rel_drop"`
}

// Compare reports regressions using the fixed absolute/relative thresholds.
func Compare(base, cur Baseline) []Regression {
	var out []Regression
	for id, b := range base.Cases {
		c, ok := cur.Cases[id]
		if !ok {
			continue
		}
		abs, rel := b-c, 0.0
		if b > 0 {
			rel = abs / b
		}
		if abs > DefaultAbsDropPP || rel > DefaultRelDrop {
			out = append(out, Regression{ID: id, Base: b, Cur: c, Abs: abs, Rel: rel})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// AsBaseline converts a run for a later Compare.
func AsBaseline(r Result) Baseline {
	b := Baseline{Suite: r.Suite, Seed: r.Seed, Overall: r.Overall, Cases: map[string]float64{}}
	for _, c := range r.Cases {
		b.Cases[c.ID] = c.Score
	}
	return b
}

// LoadBaseline reads the committed baseline file.
func LoadBaseline(path string) (Baseline, error) {
	raw, err := os.ReadFile(path) // #nosec G304 -- explicit baseline path
	if err != nil {
		return Baseline{}, fmt.Errorf("evals: read baseline: %w", err)
	}
	var b Baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return Baseline{}, fmt.Errorf("evals: decode baseline: %w", err)
	}
	return b, nil
}

// Scorecard adapts the run to the weekly job's report; the dimension names
// are the case ids, so two equal seeds produce identical dimensions.
func (r Result) Scorecard() scorecard.Report {
	dims := make([]scorecard.Dimension, 0, len(r.Cases))
	for _, c := range r.Cases {
		dims = append(dims, scorecard.Dimension{Name: c.ID, Score: round(c.Score)})
	}
	return scorecard.Report{Score: round(r.Overall), Dimensions: dims, CostUSD: r.CostUSD}
}

func round(v float64) float64 { return math.Round(v*1e6) / 1e6 }

// Fixtures is the committed deterministic suite. Its cases mirror the F8
// probes (recall E4, compaction, skill routing) without user transcripts.
func Fixtures() Suite {
	return Suite{ID: "f8-behavior", Cases: []Case{
		{ID: "recall", Prompt: "What is the locker number?", Want: "4417"},
		{ID: "compact", Prompt: "Summarize: alpha beta gamma", Want: "alpha beta gamma"},
		{ID: "route", Prompt: "Pick a skill for JSON repair", Want: "json-repair"},
	}}
}
