package scorecard

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strconv"
	"strings"
	"time"
)

// Status is the outcome of one weekly run. A run is `ok` when the runner
// returned a report, `failed` when it errored, and `skipped` when no runner is
// wired (never a fabricated score).
type Status string

// Run and alarm statuses. The alarm reuses the same closed set: `ok` means it
// was enqueued, `skipped` that no alarm was required or no owner chat exists,
// and `failed` that enqueueing it failed.
const (
	StatusOK      Status = "ok"
	StatusFailed  Status = "failed"
	StatusSkipped Status = "skipped"
)

// Dimension is one SCORECARD dimension score from the eval harness.
type Dimension struct {
	Name  string  `json:"name"`
	Score float64 `json:"score"`
}

// Alarm records what happened to the weekly notification.
type Alarm struct {
	Status Status `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Result is the readable summary of one weekly run. WriteJSON and WriteCSV
// render the same data for the scorecard artifact; flocking is deliberately
// not part of this package.
type Result struct {
	Week        string      `json:"week"`
	WeekStart   time.Time   `json:"week_start"`
	Status      Status      `json:"status"`
	Seed        int64       `json:"seed"`
	Score       float64     `json:"score"`
	Dimensions  []Dimension `json:"dimensions"`
	Regressions []string    `json:"regressions,omitempty"`
	CostUSD     float64     `json:"cost_usd"`
	StartedAt   time.Time   `json:"started_at"`
	FinishedAt  time.Time   `json:"finished_at"`
	Error       string      `json:"error,omitempty"`
	Alarm       Alarm       `json:"alarm"`
}

// WriteJSON writes the result as indented JSON plus a trailing newline.
func (r *Result) WriteJSON(w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	if err := enc.Encode(r); err != nil {
		return fmt.Errorf("scorecard: encode json: %w", err)
	}
	return nil
}

// WriteCSV writes the overall score followed by one row per dimension:
// week,status,seed,score,dimension,value. Dimensions are in the stored order
// (the job sorts them by name, so equal seeds produce identical bytes).
func (r *Result) WriteCSV(w io.Writer) error {
	cw := csv.NewWriter(w)
	if err := cw.Write([]string{"week", "status", "seed", "score", "dimension", "value"}); err != nil {
		return fmt.Errorf("scorecard: csv header: %w", err)
	}
	row := func(name string, score float64) []string {
		return []string{
			r.Week,
			string(r.Status),
			strconv.FormatInt(r.Seed, 10),
			strconv.FormatFloat(r.Score, 'g', -1, 64),
			name,
			strconv.FormatFloat(score, 'g', -1, 64),
		}
	}
	if err := cw.Write(row("", r.Score)); err != nil {
		return fmt.Errorf("scorecard: csv overall: %w", err)
	}
	for _, d := range r.Dimensions {
		if err := cw.Write(row(d.Name, d.Score)); err != nil {
			return fmt.Errorf("scorecard: csv dimension %q: %w", d.Name, err)
		}
	}
	cw.Flush()
	if err := cw.Error(); err != nil {
		return fmt.Errorf("scorecard: csv flush: %w", err)
	}
	return nil
}

// Summary is the one-line alarm body. It carries only the run result (never
// model or network input), so it is safe to enqueue into the gateway outbox.
func Summary(r *Result) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s: %s", JobID, r.Week, r.Status)
	if r.Status == StatusOK {
		fmt.Fprintf(&b, " score=%.3f dimensions=%d cost_usd=%.2f", r.Score, len(r.Dimensions), r.CostUSD)
	}
	if r.Error != "" {
		fmt.Fprintf(&b, " error=%s", r.Error)
	}
	if len(r.Regressions) > 0 {
		fmt.Fprintf(&b, " regressions=%s", strings.Join(r.Regressions, ";"))
	}
	return b.String()
}

// normalize copies dims and sorts them by name so JSON/CSV artifacts are
// deterministic for a fixed seed.
func normalize(dims []Dimension) []Dimension {
	out := make([]Dimension, len(dims))
	copy(out, dims)
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// relativeDrop is the fraction lost from prev to cur; negative means gained.
func relativeDrop(prev, cur float64) float64 {
	if prev <= 0 {
		return 0
	}
	return (prev - cur) / prev
}
