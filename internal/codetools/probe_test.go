package codetools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

// probeCompare is one CT-01/02/03 query: engine API vs rg, same harness.
type probeCompare struct {
	Corpus  string  `json:"corpus"`
	Kind    string  `json:"kind"`
	Query   string  `json:"query"`
	OursP50 float64 `json:"ours_p50_ms"`
	OursP95 float64 `json:"ours_p95_ms"`
	RgP50   float64 `json:"rg_p50_ms"`
	RgP95   float64 `json:"rg_p95_ms"`
}

// probeOp is one CT-10 tool operation, paired by ID with the hermes op.
type probeOp struct {
	ID     string  `json:"id"`
	Kind   string  `json:"kind"`
	Arg    string  `json:"arg"`
	P50    float64 `json:"p50_ms"`
	P95    float64 `json:"p95_ms"`
	Error  string  `json:"error,omitempty"`
	Output string  `json:"output,omitempty"` // grep tool output (CT-11 tokens)
}

type probeReport struct {
	Meta    map[string]string `json:"meta"`
	Compare []probeCompare    `json:"compare,omitempty"`
	Ops     []probeOp         `json:"ops,omitempty"`
}

// TestCodetoolsProbe is the measuring half of bench/codetools/cmd/codetools
// (the bench module cannot import this package): it writes a probeReport to
// DAMNBOT_PROBE_OUT. DAMNBOT_PROBE selects "compare" (CT-01/02/03 on every
// corpus set in DAMNBOT_CORPUS_*: 1 warm-up + DAMNBOT_BENCH_RUNS runs per
// query, engine and rg interleaved), "ops" (CT-10/CT-11 on C1, the ops of
// the hermes baseline DAMNBOT_HERMES_BASELINE; patch targets in
// DAMNBOT_PATCH_DIR, which must be a real fsync filesystem), "search"
// (only the search_files ops, for CT-11 tokens) or a comma list.
// DAMNBOT_PROBE_GREP_LIMIT overrides the grep limit of search_files ops.
func TestCodetoolsProbe(t *testing.T) {
	out := os.Getenv("DAMNBOT_PROBE_OUT")
	if out == "" {
		t.Skip("DAMNBOT_PROBE_OUT unset (run via bench/codetools/cmd/codetools)")
	}
	runs := benchRuns()
	parts := os.Getenv("DAMNBOT_PROBE")
	if parts == "" {
		parts = "compare,ops"
	}
	load, _ := os.ReadFile("/proc/loadavg")
	rep := probeReport{Meta: map[string]string{
		"go": runtime.Version(), "gomaxprocs": strconv.Itoa(runtime.GOMAXPROCS(0)), "runs": strconv.Itoa(runs),
		"loadavg_start": strings.TrimSpace(string(load)), "date": time.Now().Format(time.RFC3339),
	}}
	if strings.Contains(parts, "compare") {
		rep.Compare = probeCompareAll(t, runs)
	}
	if strings.Contains(parts, "ops") || strings.Contains(parts, "search") {
		rep.Ops = probeOps(t, runs, !strings.Contains(parts, "ops"))
	}
	load, _ = os.ReadFile("/proc/loadavg")
	rep.Meta["loadavg_end"] = strings.TrimSpace(string(load))
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(out, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// benchRuns is DAMNBOT_BENCH_RUNS, default 20 (CODE-TOOLS §10.1).
func benchRuns() int {
	if n, err := strconv.Atoi(os.Getenv("DAMNBOT_BENCH_RUNS")); err == nil && n > 0 {
		return n
	}
	return 20
}

// rgTargets are the CT-01/02/03 bounds on ours/rg (CODE-TOOLS §10.2).
func rgTargets() map[string]float64 { return map[string]float64{"literal": 1.2, "regex": 1.5, "glob": 1.2} }

// TestGrepSpeedVsRg is the CT-01/02/03 gate (job bench-codetools): per
// corpus and query kind, Σp50 and Σp95 of the engine over the queries are
// within rgTargets × rg's, measured interleaved in one harness (§10.1). C1
// and C3 are required, C2 is measured when DAMNBOT_CORPUS_C2 is set.
func TestGrepSpeedVsRg(t *testing.T) {
	corpusDir(t, "C1")
	corpusDir(t, "C3")
	type sums struct{ o50, r50, o95, r95 float64 }
	agg := map[[2]string]*sums{}
	for _, c := range probeCompareAll(t, benchRuns()) {
		k := [2]string{c.Corpus, c.Kind}
		if agg[k] == nil {
			agg[k] = &sums{}
		}
		s := agg[k]
		s.o50, s.r50, s.o95, s.r95 = s.o50+c.OursP50, s.r50+c.RgP50, s.o95+c.OursP95, s.r95+c.RgP95
	}
	for k, s := range agg {
		limit := rgTargets()[k[1]]
		r50, r95 := s.o50/s.r50, s.o95/s.r95
		t.Logf("%s %s: p50 ×%.2f p95 ×%.2f (limit ×%.1f)", k[0], k[1], r50, r95, limit)
		if r50 > limit || r95 > limit {
			t.Errorf("%s %s: Σp50 ×%.2f, Σp95 ×%.2f of rg > ×%.1f", k[0], k[1], r50, r95, limit)
		}
	}
}

func probeCompareAll(t *testing.T, runs int) []probeCompare {
	rg := rgPath(t)
	q := loadBenchQueries(t)
	ctx := context.Background()
	var res []probeCompare
	for _, id := range []string{"C1", "C2", "C3"} {
		if os.Getenv("DAMNBOT_CORPUS_"+id) == "" {
			continue
		}
		root := corpusDir(t, id)
		sb := corpusSandbox(t, root)
		for _, k := range []struct {
			kind string
			qs   []string
		}{{"literal", q.Literal}, {"regex", q.Regex}, {"glob", q.Glob}} {
			for _, query := range k.qs {
				var ours, theirs []time.Duration
				argv := rgArgv(rg, k.kind, query, root)
				for i := range runs + 1 {
					t0 := time.Now()
					e := freshEngine(t, sb, root)
					if _, err := corpusOp(ctx, e, k.kind, query); err != nil {
						t.Fatalf("%s %s %q: %v", id, k.kind, query, err)
					}
					e.Close()
					d := time.Since(t0)
					t0 = time.Now()
					if err := runRg(argv, "", nil); err != nil {
						t.Fatalf("rg %q: %v", query, err)
					}
					if i > 0 {
						ours, theirs = append(ours, d), append(theirs, time.Since(t0))
					}
				}
				c := probeCompare{Corpus: id, Kind: k.kind, Query: query}
				c.OursP50, c.OursP95 = percentiles(ours)
				c.RgP50, c.RgP95 = percentiles(theirs)
				res = append(res, c)
			}
		}
	}
	return res
}

// probeOps replays the hermes CT-10 operations through the JSON tool
// dispatch: search_files → grep (default limit and page), read_file 400
// lines, patch → read_file for the tag outside the window, then edit_file
// (fsync file + dir, like the durable hermes driver). searchOnly keeps the
// search_files ops.
func probeOps(t *testing.T, runs int, searchOnly bool) []probeOp {
	c1 := corpusDir(t, "C1")
	base := os.Getenv("DAMNBOT_HERMES_BASELINE")
	if base == "" {
		base = filepath.Join("..", "..", "bench", "codetools", "results", "hermes-868b9f244f-f1.json")
	}
	var hermes struct {
		Ops []probeOp `json:"hermes_c1"`
	}
	if err := json.Unmarshal(readFixture(t, base), &hermes); err != nil {
		t.Fatal(err)
	}
	sb := corpusSandbox(t, c1)
	patchDir := os.Getenv("DAMNBOT_PATCH_DIR")
	ctx := context.Background()
	ops := make([]probeOp, 0, len(hermes.Ops))
	for _, h := range hermes.Ops {
		if searchOnly && h.Kind != "search_files" {
			continue
		}
		op := probeOp{ID: h.ID, Kind: h.Kind, Arg: h.Arg}
		var ds []time.Duration
		var err error
		switch h.Kind {
		case "search_files", "read_file":
			tool, args := "grep", map[string]any{"pattern": h.Arg}
			if n, err := strconv.Atoi(os.Getenv("DAMNBOT_PROBE_GREP_LIMIT")); err == nil && n > 0 {
				args["limit"] = n // CT-11 at the hermes default limit (50)
			}
			if h.Kind == "read_file" {
				tool, args = "read_file", map[string]any{"path": h.Arg, "offset": 1, "limit": 400}
			}
			raw, _ := json.Marshal(args)
			ds, op.Output, err = timeOp(runs, func() (string, error) {
				e := freshEngine(t, sb, c1)
				defer e.Close()
				r, err := e.Run(ctx, tool, raw, Opts{})
				return r.Content, err
			})
			if h.Kind == "read_file" {
				op.Output = ""
			}
		case "patch":
			ds, err = probePatch(t, c1, patchDir, h)
		}
		if err != nil {
			op.Error = err.Error()
		} else {
			op.P50, op.P95 = percentiles(ds)
		}
		ops = append(ops, op)
	}
	return ops
}

// timeOp runs f once to warm up, then runs times; out is the last output.
func timeOp(runs int, f func() (string, error)) (ds []time.Duration, out string, err error) {
	for i := range runs + 1 {
		t0 := time.Now()
		out, err = f()
		if err != nil {
			return nil, "", err
		}
		if i > 0 {
			ds = append(ds, time.Since(t0))
		}
	}
	return ds, out, nil
}

// probePatch times edit_file on a copy of h.Arg reset before every run.
func probePatch(t *testing.T, c1, dir string, h probeOp) ([]time.Duration, error) {
	if dir == "" {
		return nil, fmt.Errorf("DAMNBOT_PATCH_DIR unset")
	}
	data, err := os.ReadFile(filepath.Join(c1, h.Arg)) //nolint:gosec // G304: corpus file named by the baseline
	if err != nil {
		return nil, err
	}
	target := uniqueDefLine(string(data))
	dst := filepath.Join(dir, fmt.Sprintf("patch_%s.py", strings.TrimPrefix(h.ID, "patch-")))
	e := freshEngine(t, corpusSandbox(t, dir), dir)
	defer e.Close()
	ctx := context.Background()
	edit := func(tag string) error {
		raw, _ := json.Marshal(map[string]any{"path": dst, "tag": tag, "edits": []map[string]string{{"old_string": target, "new_string": target + "  # f0"}}})
		_, err := e.Run(ctx, "edit_file", raw, Opts{})
		return err
	}
	var ds []time.Duration
	for i := range 21 {
		tag, err := resetPatch(ctx, e, dst, data)
		if err != nil {
			return nil, err
		}
		t0 := time.Now()
		if err := edit(tag); err != nil {
			return nil, err
		}
		if i > 0 {
			ds = append(ds, time.Since(t0))
		}
	}
	return ds, os.Remove(dst)
}

// resetPatch restores dst durably and reads it for the edit tag (outside
// the timed window, like the hermes Reset).
func resetPatch(ctx context.Context, e *Engine, dst string, data []byte) (string, error) {
	f, err := os.Create(dst) //nolint:gosec // G304: probe patch target
	if err != nil {
		return "", err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = fsyncPath(filepath.Dir(dst))
	}
	if err != nil {
		return "", err
	}
	r, err := e.Read(ctx, ReadArgs{Path: dst, Limit: 1}, Opts{})
	if err != nil {
		return "", err
	}
	return metaStr(&r, "tag"), nil
}

// uniqueDefLine is the patch target of bench/codetools/cmd/codetools: the
// first `def ` line occurring once in s.
func uniqueDefLine(s string) string {
	for line := range strings.Lines(s) {
		l := strings.TrimRight(line, "\n")
		if strings.HasPrefix(strings.TrimSpace(l), "def ") && strings.Count(s, l) == 1 {
			return l
		}
	}
	return ""
}

// fsyncPath opens and fsyncs p (the directory half of a durable reset).
func fsyncPath(p string) error {
	f, err := os.Open(p) //nolint:gosec // G304: probe patch directory
	if err != nil {
		return err
	}
	err = f.Sync()
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}
