// Package codetools menjalankan probe pengukur code tools Damn-Bot (TestCodetoolsProbe di
// internal/codetools modul utama; modul bench tidak meng-import modul utama) dan memasangkan
// hasilnya dengan rg serta baseline hermes (CODE-TOOLS §10: CT-01/02/03, CT-10, CT-11).
package codetools

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

// Compare = satu kueri CT-01/02/03: API engine vs rg, harness sama (interleaved).
type Compare struct {
	Corpus  string  `json:"corpus"`
	Kind    string  `json:"kind"`
	Query   string  `json:"query"`
	OursP50 float64 `json:"ours_p50_ms"`
	OursP95 float64 `json:"ours_p95_ms"`
	RgP50   float64 `json:"rg_p50_ms"`
	RgP95   float64 `json:"rg_p95_ms"`
}

// Op = satu operasi tool CT-10, berpasangan per ID dengan op hermes.
type Op struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Arg    string  `json:"arg"`
	P50    float64 `json:"p50_ms"`
	P95    float64 `json:"p95_ms"`
	Error  string  `json:"error,omitempty"`
	Output string  `json:"output,omitempty"`
}

// Probe = laporan TestCodetoolsProbe.
type Probe struct {
	Meta    map[string]string `json:"meta"`
	Compare []Compare         `json:"compare,omitempty"`
	Ops     []Op              `json:"ops,omitempty"`
}

// HermesOp = op hermes dari artefak baseline (`hermes_c1`).
type HermesOp struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Arg    string  `json:"arg"`
	P50    float64 `json:"p50_ms"`
	P95    float64 `json:"p95_ms"`
	Tokens int     `json:"output_o200k_tokens"`
}

// LoadHermes membaca op hermes dari artefak bench/codetools/results/*.json.
func LoadHermes(path string) ([]HermesOp, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // G304: artefak baseline pilihan pemanggil
	if err != nil {
		return nil, err
	}
	var r struct {
		Hermes []HermesOp `json:"hermes_c1"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return r.Hermes, nil
}

// RunProbe menjalankan `go test -run '^TestCodetoolsProbe$' ./internal/codetools` di root repo
// dengan env tambahan (DAMNBOT_PROBE, DAMNBOT_CORPUS_*, DAMNBOT_RG, DAMNBOT_PATCH_DIR,
// DAMNBOT_BENCH_RUNS, DAMNBOT_HERMES_BASELINE); stderr probe diteruskan.
func RunProbe(repo string, env []string) (*Probe, error) {
	dir, err := os.MkdirTemp("", "ct-probe-")
	if err != nil {
		return nil, err
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "probe.json")
	cmd := exec.Command("go", "test", "-tags", "goolm", "-count=1", "-timeout=0", "-run", "^TestCodetoolsProbe$", "./internal/codetools")
	cmd.Dir = repo
	cmd.Env = append(append(os.Environ(), env...), "DAMNBOT_PROBE_OUT="+out)
	cmd.Stdout, cmd.Stderr = os.Stderr, os.Stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("probe: %w", err)
	}
	raw, err := os.ReadFile(out) //nolint:gosec // G304: file sementara milik fungsi ini
	if err != nil {
		return nil, fmt.Errorf("probe tidak menulis laporan (korpus/rg tidak diset?): %w", err)
	}
	var p Probe
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

// matchLineRe = baris match output grep Damn-Bot ("N:isi").
var matchLineRe = regexp.MustCompile(`(?m)^\d+:`)

// GrepTokens = token o200k dan jumlah match output tool grep Damn-Bot untuk op `search_files`
// baseline (50 kueri golden CT-11) di C1, 1 run, pada limit grep (0 = default tool).
type GrepTokens struct {
	Tokens  int            `json:"tokens"`
	Matches int            `json:"matches"`
	PerOp   map[string]int `json:"per_op_tokens"`
}

// MeasureGrepTokens menjalankan probe "search" dan menghitung token outputnya.
func MeasureGrepTokens(repo, c1 string, limit int, tok *o200k.Tokenizer) (*GrepTokens, error) {
	p, err := RunProbe(repo, []string{"DAMNBOT_PROBE=search", "DAMNBOT_BENCH_RUNS=1", "DAMNBOT_CORPUS_C1=" + c1, fmt.Sprintf("DAMNBOT_PROBE_GREP_LIMIT=%d", limit)})
	if err != nil {
		return nil, err
	}
	g := &GrepTokens{PerOp: map[string]int{}}
	for _, op := range p.Ops {
		if op.Error != "" {
			return nil, fmt.Errorf("%s %q: %s", op.ID, op.Arg, op.Error)
		}
		n := tok.Count(op.Output)
		g.PerOp[op.ID] = n
		g.Tokens += n
		g.Matches += len(matchLineRe.FindAllStringIndex(op.Output, -1))
	}
	return g, nil
}
