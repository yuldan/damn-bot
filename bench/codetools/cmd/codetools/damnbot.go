package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/damnbot/damnbot/bench/budget/o200k"
	"github.com/damnbot/damnbot/bench/codetools"
	"github.com/damnbot/damnbot/bench/speed"
)

// Target CODE-TOOLS §10.2: rasio p50/p95 vs rg per jenis kueri (CT-01/02/03), CT-10, CT-11.
var rgTarget = map[string]float64{"literal": 1.2, "regex": 1.5, "glob": 1.2}

const (
	ct10ReadSearchSpeedup = 10
	ct10PatchSpeedup      = 2
	ct10PatchP95MS        = 6
	ct11TokenRatio        = 0.6
)

// kindSummary = agregat satu (korpus, jenis): rasio Σp50 dan Σp95 Damn-Bot / rg atas semua
// kueri (berbobot waktu), median dan maksimum rasio p50 per kueri.
type kindSummary struct {
	Corpus      string  `json:"corpus"`
	Kind        string  `json:"kind"`
	Queries     int     `json:"queries"`
	OursSumP50  float64 `json:"ours_sum_p50_ms"`
	RgSumP50    float64 `json:"rg_sum_p50_ms"`
	OursSumP95  float64 `json:"ours_sum_p95_ms"`
	RgSumP95    float64 `json:"rg_sum_p95_ms"`
	RatioP50    float64 `json:"ratio_p50"`
	RatioP95    float64 `json:"ratio_p95"`
	MedianRatio float64 `json:"median_query_ratio_p50"`
	MaxRatio    float64 `json:"max_query_ratio_p50"`
	Target      float64 `json:"target"`
	Pass        bool    `json:"pass"`
}

// ct10Kind = satu kategori CT-10: median p50/p95 per op Damn-Bot vs hermes.
type ct10Kind struct {
	Kind          string  `json:"kind"`
	Ops           int     `json:"ops"`
	HermesP50     float64 `json:"hermes_median_p50_ms"`
	OursP50       float64 `json:"ours_median_p50_ms"`
	OursP95       float64 `json:"ours_median_p95_ms"`
	OursMaxP95    float64 `json:"ours_max_p95_ms"`
	Speedup       float64 `json:"speedup_p50"`
	MinOpSpeedup  float64 `json:"min_op_speedup_p50"`
	TargetSpeedup float64 `json:"target_speedup"`
	Pass          bool    `json:"pass"`
}

type damnbotReport struct {
	Meta    map[string]string   `json:"meta"`
	Probe   map[string]string   `json:"probe_meta"`
	Kinds   []kindSummary       `json:"rg_compare"`
	CT10    []ct10Kind          `json:"ct10"`
	CT11    map[string]float64  `json:"ct11"`
	Compare []codetools.Compare `json:"queries"`
	Ops     []codetools.Op      `json:"ops"`
}

// durablePatchDir membuat direktori target patch CT-10 di bawah parent; tmpfs ditolak (fsync
// no-op, baseline hermes durabel, ADR-082).
func durablePatchDir(parent string) (string, error) {
	if parent == "" {
		return "", errors.New("-damnbot dengan -c1 butuh -patch-dir (filesystem dengan fsync nyata)")
	}
	if fs := cmdOut("findmnt", "-no", "FSTYPE", "-T", parent); fs == "tmpfs" {
		return "", fmt.Errorf("-patch-dir %s di tmpfs: fsync no-op (ADR-082)", parent)
	}
	return os.MkdirTemp(parent, "ct10-damnbot-")
}

// runDamnbot mengukur Damn-Bot lewat probe modul utama: CT-01/02/03 di korpus yang diberikan
// (rg di harness yang sama), CT-10/CT-11 di C1 terhadap baseline hermes.
func runDamnbot(rg, c1, c2, c3, patchDir, hermesPath, out string, runs int, meta map[string]string) error {
	hermes, err := codetools.LoadHermes(hermesPath)
	if err != nil {
		return err
	}
	absHermes, err := filepath.Abs(hermesPath)
	if err != nil {
		return err
	}
	env := []string{"DAMNBOT_RG=" + rg, fmt.Sprintf("DAMNBOT_BENCH_RUNS=%d", runs), "DAMNBOT_HERMES_BASELINE=" + absHermes, "DAMNBOT_PATCH_DIR=" + patchDir}
	for id, root := range map[string]string{"C1": c1, "C2": c2, "C3": c3} {
		if root != "" {
			env = append(env, "DAMNBOT_CORPUS_"+id+"="+root)
		}
	}
	parts := "compare"
	if c1 != "" {
		parts += ",ops"
	}
	p, err := codetools.RunProbe("..", append(env, "DAMNBOT_PROBE="+parts))
	if err != nil {
		return err
	}
	rep := damnbotReport{Meta: meta, Probe: p.Meta, Compare: p.Compare, Ops: p.Ops, CT11: map[string]float64{}}
	rep.Meta["hermes_baseline"] = hermesPath
	rep.Kinds = summarizeCompare(p.Compare)
	if c1 != "" {
		if rep.CT10, err = summarizeCT10(p.Ops, hermes); err != nil {
			return err
		}
		tok, err := o200k.Load()
		if err != nil {
			return err
		}
		theirs := 0
		for _, h := range hermes {
			theirs += h.Tokens
		}
		rep.CT11 = map[string]float64{"hermes_tokens": float64(theirs), "target": ct11TokenRatio}
		for name, limit := range map[string]int{"default": 0, "limit50": 50} { // limit 50 = default search_files hermes
			g, err := codetools.MeasureGrepTokens("..", c1, limit, tok)
			if err != nil {
				return err
			}
			rep.CT11[name+"_tokens"], rep.CT11[name+"_matches"] = float64(g.Tokens), float64(g.Matches)
			rep.CT11[name+"_ratio"], rep.CT11[name+"_tokens_per_match"] = float64(g.Tokens)/float64(theirs), float64(g.Tokens)/float64(g.Matches)
		}
	}
	for i := range rep.Ops {
		rep.Ops[i].Output = "" // token sudah dihitung; artefak tetap kecil
	}
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	printSummary(&rep)
	return os.WriteFile(out, data, 0o644) //nolint:gosec // G306: artefak hasil bench di-commit
}

func summarizeCompare(cs []codetools.Compare) []kindSummary {
	var res []kindSummary
	idx := map[[2]string]int{}
	ratios := map[[2]string][]float64{}
	for _, c := range cs {
		k := [2]string{c.Corpus, c.Kind}
		i, ok := idx[k]
		if !ok {
			i = len(res)
			idx[k] = i
			res = append(res, kindSummary{Corpus: c.Corpus, Kind: c.Kind, Target: rgTarget[c.Kind]})
		}
		s := &res[i]
		s.Queries++
		s.OursSumP50 += c.OursP50
		s.RgSumP50 += c.RgP50
		s.OursSumP95 += c.OursP95
		s.RgSumP95 += c.RgP95
		ratios[k] = append(ratios[k], c.OursP50/c.RgP50)
	}
	for i := range res {
		s := &res[i]
		r := ratios[[2]string{s.Corpus, s.Kind}]
		s.RatioP50, s.RatioP95 = s.OursSumP50/s.RgSumP50, s.OursSumP95/s.RgSumP95
		s.MedianRatio, s.MaxRatio = speed.Percentile(r, 50), slices.Max(r)
		s.Pass = s.RatioP50 <= s.Target && s.RatioP95 <= s.Target
	}
	return res
}

func summarizeCT10(ops []codetools.Op, hermes []codetools.HermesOp) ([]ct10Kind, error) {
	byID := map[string]codetools.HermesOp{}
	for _, h := range hermes {
		byID[h.ID] = h
	}
	type acc struct{ h, o, o95, sp []float64 }
	per := map[string]*acc{}
	for _, op := range ops {
		if op.Error != "" {
			return nil, fmt.Errorf("CT-10 %s %q: %s", op.ID, op.Arg, op.Error)
		}
		h, ok := byID[op.ID]
		if !ok {
			return nil, fmt.Errorf("CT-10 %s tanpa pasangan hermes", op.ID)
		}
		a := per[op.Kind]
		if a == nil {
			a = &acc{}
			per[op.Kind] = a
		}
		a.h, a.o, a.o95, a.sp = append(a.h, h.P50), append(a.o, op.P50), append(a.o95, op.P95), append(a.sp, h.P50/op.P50)
	}
	var res []ct10Kind
	for _, kind := range []string{"search_files", "read_file", "patch"} {
		a := per[kind]
		if a == nil {
			continue
		}
		k := ct10Kind{
			Kind: kind, Ops: len(a.o), HermesP50: speed.Percentile(a.h, 50), OursP50: speed.Percentile(a.o, 50),
			OursP95: speed.Percentile(a.o95, 50), OursMaxP95: slices.Max(a.o95), MinOpSpeedup: slices.Min(a.sp),
			TargetSpeedup: ct10ReadSearchSpeedup,
		}
		k.Speedup = k.HermesP50 / k.OursP50
		if kind == "patch" {
			k.TargetSpeedup = ct10PatchSpeedup
		}
		k.Pass = k.Speedup >= k.TargetSpeedup && k.MinOpSpeedup >= 1 && (kind != "patch" || k.OursMaxP95 <= ct10PatchP95MS)
		res = append(res, k)
	}
	return res, nil
}

func printSummary(rep *damnbotReport) {
	var b strings.Builder
	fmt.Fprintf(&b, "loadavg %s → %s\n", rep.Probe["loadavg_start"], rep.Probe["loadavg_end"])
	for _, s := range rep.Kinds {
		fmt.Fprintf(&b, "%s %-7s Σp50 ours %9.1f rg %9.1f ×%.2f | Σp95 ours %9.1f rg %9.1f ×%.2f | median ×%.2f max ×%.2f target ×%.1f pass=%v\n",
			s.Corpus, s.Kind, s.OursSumP50, s.RgSumP50, s.RatioP50, s.OursSumP95, s.RgSumP95, s.RatioP95, s.MedianRatio, s.MaxRatio, s.Target, s.Pass)
	}
	for _, k := range rep.CT10 {
		fmt.Fprintf(&b, "CT-10 %-12s hermes p50 %7.2f ours p50 %6.3f (p95 med %6.3f max %6.3f) speedup ×%.1f (min op ×%.1f) target ×%d pass=%v\n",
			k.Kind, k.HermesP50, k.OursP50, k.OursP95, k.OursMaxP95, k.Speedup, k.MinOpSpeedup, int(k.TargetSpeedup), k.Pass)
	}
	for _, n := range []string{"default", "limit50"} {
		if r, ok := rep.CT11[n+"_ratio"]; ok {
			fmt.Fprintf(&b, "CT-11 %-8s tokens %.0f (%.0f match, %.1f tok/match) hermes %.0f ratio %.3f target %.1f\n",
				n, rep.CT11[n+"_tokens"], rep.CT11[n+"_matches"], rep.CT11[n+"_tokens_per_match"], rep.CT11["hermes_tokens"], r, ct11TokenRatio)
		}
	}
	fmt.Fprint(os.Stderr, b.String())
}
