// Package scorecard is the P20 release gate (SPEC §2 P20, SCORECARD §4):
// every SCORECARD §1.1–§1.8 dimension must match or beat the pinned hermes
// baseline, and every core ([I]) dimension must win with its margin. A missing
// measurement fails; the artefact is rebuilt from committed phase artifacts.
package scorecard

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Phase is the acceptance phase that owns the RC artefact.
const Phase = "F9"

// HermesCommit is the pinned baseline snapshot (SCORECARD intro).
const HermesCommit = "868b9f244f5aa3ad238e494048046fe4f29c28ba"

// Rule is a comparison direction. LowerIsBetter dimensions compare
// Damnbot <= Hermes (optionally times MaxRatio); higher-is-better the inverse.
type Rule string

// Rule values: *_ratio applies the published margin; absolute_* uses Target.
const (
	RuleLower     Rule = "lower"
	RuleHigher    Rule = "higher"
	RuleEqual     Rule = "equal"
	RuleLowerMax  Rule = "lower_max_ratio"
	RuleHigherMin Rule = "higher_min_ratio"
	RuleAbsMax    Rule = "absolute_max"
	RuleAbsMin    Rule = "absolute_min"
)

// Dimension is one row of the gate. Exactly one of Damnbot/Hermes or
// NotMeasured is set; a half-filled row is invalid.
type Dimension struct {
	Name        string   `json:"name"`
	Section     string   `json:"section"`
	Core        bool     `json:"core"`
	Rule        Rule     `json:"rule"`
	MaxRatio    float64  `json:"max_ratio,omitempty"`
	MinRatio    float64  `json:"min_ratio,omitempty"`
	Target      float64  `json:"target,omitempty"`
	Unit        string   `json:"unit,omitempty"`
	Hermes      *float64 `json:"hermes,omitempty"`
	Damnbot     *float64 `json:"damnbot,omitempty"`
	Source      []string `json:"source,omitempty"`
	Detail      string   `json:"detail,omitempty"`
	NotMeasured string   `json:"not_measured,omitempty"`
}

// Meta records provenance of the published artefact.
type Meta struct {
	Phase         string            `json:"phase"`
	Date          string            `json:"date"`
	HermesCommit  string            `json:"hermes_commit"`
	DamnbotCommit string            `json:"damnbot_commit"`
	EvalSet       string            `json:"evalset"`
	Harness       string            `json:"harness"`
	Sources       map[string]string `json:"sources_sha256"`
}

// Artifact is the published RC scorecard.
type Artifact struct {
	Meta       Meta                 `json:"meta"`
	Dimensions map[string]Dimension `json:"dimensions"`
}

// GateResult separates hard violations from missing measurements; both must be empty.
type GateResult struct {
	Violations  []string
	NotMeasured []string
}

// OK reports whether the artefact closes the P20 gate.
func (g GateResult) OK() bool { return len(g.Violations) == 0 && len(g.NotMeasured) == 0 }

const corePrefix = "**[I]** "

// ParseDimensions reads the canonical dimension list from SCORECARD §1.1–§1.8.
// §1.9 is a GAP table whose first column is an ID, not a dimension, so it is
// excluded; the aggregate row counts it through GAP-SWEEP.
func ParseDimensions(doc string) []Dimension {
	var out []Dimension
	section := ""
	for line := range strings.SplitSeq(doc, "\n") {
		if strings.HasPrefix(line, "### 1.") {
			section = strings.TrimSpace(strings.TrimPrefix(line, "### "))
			continue
		}
		if section == "" || strings.HasPrefix(section, "1.9") || !strings.HasPrefix(line, "|") {
			continue
		}
		cells := SplitCells(strings.TrimRight(line, "\r"))
		if len(cells) == 0 {
			continue
		}
		name := strings.TrimSpace(cells[0])
		if name == "Dimensi" || strings.HasPrefix(name, "---") || strings.Trim(name, "-: ") == "" {
			continue
		}
		d := Dimension{Name: name, Section: section}
		if strings.HasPrefix(name, corePrefix) {
			d.Core = true
			d.Name = strings.TrimPrefix(name, corePrefix)
		}
		out = append(out, d)
	}
	return out
}

// SplitCells splits a Markdown table row on unescaped `|` and drops the empty
// leading/trailing cells.
func SplitCells(line string) []string {
	var cells []string
	var cur strings.Builder
	s := strings.TrimSuffix(strings.TrimPrefix(strings.TrimSpace(line), "|"), "|")
	// Classic loop: `\|` consumes the next byte, which a range cannot express.
	for i := 0; i < len(s); i++ {
		switch {
		case s[i] == '\\' && i+1 < len(s) && s[i+1] == '|':
			cur.WriteByte('|')
			i++
		case s[i] == '|':
			cells = append(cells, strings.TrimSpace(cur.String()))
			cur.Reset()
		default:
			cur.WriteByte(s[i])
		}
	}
	cells = append(cells, strings.TrimSpace(cur.String()))
	return cells
}

// Gate checks a parsed artefact against the canonical list.
func (a *Artifact) Gate(canonical []Dimension) GateResult {
	var res GateResult
	want := map[string]bool{}
	for i := range canonical {
		c := &canonical[i]
		want[c.Name] = true
		d, ok := a.Dimensions[c.Name]
		if !ok {
			res.Violations = append(res.Violations, "hilang: "+c.Name)
			continue
		}
		if d.Name != c.Name || d.Section != c.Section || d.Core != c.Core {
			res.Violations = append(res.Violations, fmt.Sprintf("baris %q tidak cocok dengan SCORECARD (section/core berubah)", c.Name))
			continue
		}
		if d.NotMeasured != "" {
			if len(strings.TrimSpace(d.NotMeasured)) < 8 {
				res.Violations = append(res.Violations, c.Name+": alasan not_measured terlalu pendek")
				continue
			}
			res.NotMeasured = append(res.NotMeasured, c.Name+": "+d.NotMeasured)
			continue
		}
		if d.Hermes == nil || d.Damnbot == nil {
			res.Violations = append(res.Violations, c.Name+": nilai hermes/damnbot tidak lengkap")
			continue
		}
		if msg := checkRule(&d); msg != "" {
			res.Violations = append(res.Violations, msg)
		}
	}
	var extra []string
	for name := range a.Dimensions {
		if !want[name] {
			extra = append(extra, name)
		}
	}
	sort.Strings(extra)
	for _, name := range extra {
		res.Violations = append(res.Violations, "dimensi di luar SCORECARD: "+name)
	}
	return res
}

//nolint:gocognit,gocyclo // dispatch tabel aturan gate; setiap cabang adalah aturan SCORECARD.
func checkRule(d *Dimension) string {
	h, b := *d.Hermes, *d.Damnbot
	fail := func(want string) string {
		return fmt.Sprintf("%s: damnbot %g vs hermes %g (%s)", d.Name, b, h, want)
	}
	switch d.Rule {
	case RuleLower:
		if b > h {
			return fail("harus ≤ hermes")
		}
	case RuleHigher:
		if b < h {
			return fail("harus ≥ hermes")
		}
	case RuleEqual:
		if h != 0 {
			if diff := (b - h) / h; diff > 0.02 || diff < -0.02 {
				return fail("harus sama (±2%)")
			}
		} else if b != 0 {
			return fail("harus sama")
		}
	case RuleLowerMax:
		if d.MaxRatio <= 0 || d.MaxRatio >= 1 {
			return d.Name + ": max_ratio tidak valid"
		}
		if b > h*d.MaxRatio {
			return fail(fmt.Sprintf("harus ≤ %g× hermes", d.MaxRatio))
		}
	case RuleHigherMin:
		if d.MinRatio <= 0 {
			return d.Name + ": min_ratio tidak valid"
		}
		if b < h*d.MinRatio {
			return fail(fmt.Sprintf("harus ≥ %g× hermes", d.MinRatio))
		}
	case RuleAbsMax:
		if d.Target < 0 || b > d.Target {
			return fail(fmt.Sprintf("target absolut %g", d.Target))
		}
	case RuleAbsMin:
		if b < d.Target {
			return fail(fmt.Sprintf("target absolut %g", d.Target))
		}
	default:
		return d.Name + ": rule tidak dikenal"
	}
	if d.Core {
		// A core row without a margin is a table change that silently
		// weakens P20; require an explicit ratio or absolute target.
		if d.Rule == RuleLower || d.Rule == RuleHigher || d.Rule == RuleEqual {
			return d.Name + ": dimensi inti tanpa margin eksplisit"
		}
		if d.Rule == RuleHigherMin && d.MinRatio < 1 {
			return d.Name + ": margin dimensi inti di bawah hermes"
		}
	}
	return ""
}

// Write emits deterministic JSON (one trailing newline).
func (a *Artifact) Write(path string) error {
	data, err := json.MarshalIndent(a, "", " ")
	if err != nil {
		return fmt.Errorf("scorecard: marshal: %w", err)
	}
	data = append(data, '\n')
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("scorecard: mkdir: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // artefak repo
		return fmt.Errorf("scorecard: write: %w", err)
	}
	return nil
}

// Load reads an artefact.
func Load(path string) (*Artifact, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path artefak repo yang di-commit
	if err != nil {
		return nil, fmt.Errorf("scorecard: read %s: %w", path, err)
	}
	var a Artifact
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, fmt.Errorf("scorecard: parse %s: %w", path, err)
	}
	return &a, nil
}

// num reads a JSON number by path (maps and slices unsupported).
func num(v any, path ...string) (float64, bool) {
	cur := v
	for _, key := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return 0, false
		}
		cur, ok = m[key]
		if !ok {
			return 0, false
		}
	}
	switch n := cur.(type) {
	case float64:
		return n, true
	case json.Number:
		x, err := n.Float64()
		return x, err == nil
	case string:
		x, err := strconv.ParseFloat(strings.TrimSpace(n), 64)
		return x, err == nil
	}
	return 0, false
}

// readJSON decodes a JSON file with numbers as float64.
func readJSON(path string) (any, error) {
	data, err := os.ReadFile(path) //nolint:gosec // path artefak repo yang di-commit
	if err != nil {
		return nil, fmt.Errorf("scorecard: read %s: %w", path, err)
	}
	var v any
	if err := json.Unmarshal(data, &v); err != nil {
		return nil, fmt.Errorf("scorecard: parse %s: %w", path, err)
	}
	return v, nil
}
