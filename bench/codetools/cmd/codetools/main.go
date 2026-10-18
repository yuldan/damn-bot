// Command codetools mengukur code tools (CODE-TOOLS §10, SCORECARD §1.2 "Code tools"):
// `rg` (literal/regex/glob) di C1–C3, tool hermes (`search_files`, `read_file`, `patch`) di C1
// in-process tanpa jaringan, dan token o200k output grep hermes vs rg (CT-11).
//
// `patch` hermes diukur berdurabilitas setara (ADR-082 butir 6, default `-durable`): fsync(file) +
// fsync(dir) di dalam jendela waktu; target patch di `-patch-dir` (wajib bukan tmpfs, mis. ext4 NVMe).
// Baseline F0 (non-durabel, target di tmpfs) = `-durable=false` tanpa `-patch-dir`.
//
//	cd bench && go run ./codetools/cmd/codetools -rg <rg> -c1 ~/Dokumen/hermes-agent \
//	    -c2 /tmp/damnbot-corpora/C2 -c3 /tmp/damnbot-corpora/C3 -durable=false -out codetools/results/f0-baseline.json
//	cd bench && go run ./codetools/cmd/codetools -rg <rg> -c1 ~/Dokumen/hermes-agent -hermes-runs 20 \
//	    -home /tmp/hermes-home-ct10 -patch-dir <dir ext4> -out codetools/results/hermes-868b9f244f-f1.json
//
// `-damnbot` mengukur Damn-Bot lewat TestCodetoolsProbe (internal/codetools): CT-01/02/03 = API
// engine (Limit=-1, tanpa batas halaman, case-sensitive, literal = fixed string) vs
// `rg --no-config --no-require-git` (stdout /dev/null) per kueri, 1 warm-up + `-runs` run
// berselang-seling; rasio = Σp50 dan Σp95 Damn-Bot / rg per (korpus, jenis). CT-10 = tool JSON
// Damn-Bot (Engine baru per run) vs op hermes `-hermes-baseline`; CT-11 = token o200k output
// grep pada limit default dan limit 50 (default hermes).
//
//	cd bench && go run ./codetools/cmd/codetools -damnbot -rg <rg 15.1.0> -c1 ~/Dokumen/hermes-agent \
//	    -c2 /tmp/damnbot-corpora/C2 -c3 /tmp/damnbot-corpora/C3 -patch-dir <dir ext4> \
//	    -out codetools/results/f1-<commit>.json
package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/damnbot/damnbot/bench/budget/o200k"
	"github.com/damnbot/damnbot/bench/speed"
)

type queries struct {
	Literal []string `json:"literal"`
	Regex   []string `json:"regex"`
	Glob    []string `json:"glob"`
}

type timing struct {
	Query  string  `json:"query"`
	P50MS  float64 `json:"p50_ms"`
	P95MS  float64 `json:"p95_ms"`
	Lines  int     `json:"output_lines,omitempty"`
	Tokens int     `json:"output_o200k_tokens,omitempty"`
}

type corpusResult struct {
	Root          string   `json:"root"`
	RgLiteral     []timing `json:"rg_literal"`
	RgRegex       []timing `json:"rg_regex"`
	RgGlob        []timing `json:"rg_glob"`
	MedianP50Lit  float64  `json:"rg_literal_median_p50_ms"`
	MedianP50Re   float64  `json:"rg_regex_median_p50_ms"`
	MedianP50Glob float64  `json:"rg_glob_median_p50_ms"`
}

type hermesOp struct {
	ID         string  `json:"id"`
	Kind       string  `json:"kind"`
	Arg        string  `json:"arg"`
	P50MS      float64 `json:"p50_ms,omitempty"`
	P95MS      float64 `json:"p95_ms,omitempty"`
	Error      string  `json:"error,omitempty"`
	Tokens     int     `json:"output_o200k_tokens,omitempty"`
	OutputSHA  string  `json:"output_sha256,omitempty"`
	RgTokens   int     `json:"rg_output_o200k_tokens,omitempty"`
	OutputSize int     `json:"output_bytes,omitempty"`
	Durable    bool    `json:"durable,omitempty"`
}

type report struct {
	Meta    map[string]string        `json:"meta"`
	Corpora map[string]*corpusResult `json:"corpora"`
	Hermes  []hermesOp               `json:"hermes_c1"`
	Summary map[string]float64       `json:"summary"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	rg := flag.String("rg", "rg", "binary ripgrep")
	c1 := flag.String("c1", "", "C1 = checkout hermes 868b9f244f")
	c2 := flag.String("c2", "", "C2 sintetis")
	c3 := flag.String("c3", "", "C3 sintetis")
	qpath := flag.String("queries", "codetools/queries.json", "kueri")
	runs := flag.Int("runs", 20, "run per kueri rg (CODE-TOOLS §10.1)")
	hruns := flag.Int("hermes-runs", 5, "run per op hermes")
	out := flag.String("out", "codetools/results/f0-baseline.json", "artefak")
	home := flag.String("home", "/tmp/hm_f0_ct", "HERMES_HOME (dibuat ulang)")
	durable := flag.Bool("durable", true, "patch hermes + fsync(file)+fsync(dir) di jendela waktu (ADR-082)")
	patchDir := flag.String("patch-dir", "", "induk direktori target patch (default: workspace di -home); bukan tmpfs bila -durable")
	damnbot := flag.Bool("damnbot", false, "ukur Damn-Bot (probe modul utama) vs rg dan baseline hermes")
	hermesBase := flag.String("hermes-baseline", "codetools/results/hermes-868b9f244f-f1.json", "baseline hermes CT-10 (-damnbot)")
	flag.Parse()
	raw, err := os.ReadFile(*qpath)
	if err != nil {
		return err
	}
	var q queries
	if err := json.Unmarshal(raw, &q); err != nil {
		return err
	}
	tok, err := o200k.Load()
	if err != nil {
		return err
	}
	rgVer, err := exec.Command(*rg, "--version").Output()
	if err != nil {
		return err
	}
	rep := report{
		Meta: map[string]string{
			"rg": *rg, "rg_version": strings.SplitN(string(rgVer), "\n", 2)[0], "runs": fmt.Sprint(*runs),
			"hermes_runs": fmt.Sprint(*hruns), "date": time.Now().Format(time.RFC3339),
			"method": "page cache hangat (1 warm-up), rg --no-config, stdout ke /dev/null; hermes in-process via model_tools.handle_function_call, unshare -rn",
			"cpu":    cpuModel(), "kernel": cmdOut("uname", "-sr"),
		},
		Corpora: map[string]*corpusResult{}, Summary: map[string]float64{},
	}
	if *damnbot {
		pd := ""
		if *c1 != "" {
			if pd, err = durablePatchDir(*patchDir); err != nil {
				return err
			}
			defer func() { _ = os.RemoveAll(pd) }()
		}
		return runDamnbot(*rg, *c1, *c2, *c3, pd, *hermesBase, *out, *runs, rep.Meta)
	}
	if *c1 != "" {
		rep.Meta["hermes_commit"] = cmdOut("git", "-C", *c1, "rev-parse", "HEAD")
	}
	for name, root := range map[string]string{"C1": *c1, "C2": *c2, "C3": *c3} {
		if root == "" {
			continue
		}
		fmt.Fprintln(os.Stderr, "rg", name)
		cr, err := benchRg(*rg, root, q, *runs, tok)
		if err != nil {
			return err
		}
		rep.Corpora[name] = cr
	}
	if *c1 != "" {
		pd, cleanup, err := patchTargetDir(*patchDir, *home, *durable, rep.Meta)
		if err != nil {
			return err
		}
		defer cleanup()
		hops, err := benchHermes(hermesConf{c1: *c1, home: *home, patchSrc: pd, durable: *durable}, q, *hruns, tok, rep.Corpora["C1"])
		if err != nil {
			return err
		}
		rep.Hermes = hops
		summarize(&rep)
	}
	data, err := json.MarshalIndent(rep, "", " ")
	if err != nil {
		return err
	}
	return os.WriteFile(*out, data, 0o644)
}

// timeCmd: 1 warm-up + n run; exit 1 rg (tanpa match) bukan error.
func timeCmd(n int, argv ...string) ([]float64, []byte, error) {
	var samples []float64
	var last []byte
	for i := range n + 1 {
		cmd := exec.Command(argv[0], argv[1:]...)
		var buf bytes.Buffer
		if i == 0 {
			cmd.Stdout = &buf // output warm-up disimpan untuk hitung baris/token
		}
		t0 := time.Now()
		err := cmd.Run()
		dt := time.Since(t0)
		var ee *exec.ExitError
		if err != nil && !(errors.As(err, &ee) && ee.ExitCode() == 1) {
			return nil, nil, fmt.Errorf("%v: %w", argv, err)
		}
		if i == 0 {
			last = buf.Bytes()
		} else {
			samples = append(samples, float64(dt.Microseconds())/1000)
		}
	}
	return samples, last, nil
}

func benchRg(rg, root string, q queries, runs int, tok *o200k.Tokenizer) (*corpusResult, error) {
	cr := &corpusResult{Root: root}
	do := func(dst *[]timing, query string, argv ...string) error {
		s, outb, err := timeCmd(runs, argv...)
		if err != nil {
			return err
		}
		*dst = append(*dst, timing{
			Query: query, P50MS: speed.Percentile(s, 50), P95MS: speed.Percentile(s, 95),
			Lines: bytes.Count(outb, []byte("\n")), Tokens: tok.Count(string(outb)),
		})
		return nil
	}
	for _, lit := range q.Literal {
		if err := do(&cr.RgLiteral, lit, rg, "--no-config", "-n", "-F", "-e", lit, root); err != nil {
			return nil, err
		}
	}
	for _, re := range q.Regex {
		if err := do(&cr.RgRegex, re, rg, "--no-config", "-n", "-e", re, root); err != nil {
			return nil, err
		}
	}
	for _, g := range q.Glob {
		if err := do(&cr.RgGlob, g, rg, "--no-config", "--files", "-g", g, root); err != nil {
			return nil, err
		}
	}
	med := func(ts []timing) float64 {
		p := make([]float64, len(ts))
		for i, t := range ts {
			p[i] = t.P50MS
		}
		return speed.Percentile(p, 50)
	}
	cr.MedianP50Lit, cr.MedianP50Re, cr.MedianP50Glob = med(cr.RgLiteral), med(cr.RgRegex), med(cr.RgGlob)
	return cr, nil
}

// pickFiles: 10 file .py ter-track pertama (urut path) dengan ≥ 400 baris dan satu baris `def `
// yang unik (target patch).
func pickFiles(c1 string) ([]string, error) {
	out, err := exec.Command("git", "-C", c1, "ls-files", "*.py").Output()
	if err != nil {
		return nil, err
	}
	var picked []string
	sc := bufio.NewScanner(bytes.NewReader(out))
	for sc.Scan() && len(picked) < 10 {
		data, err := os.ReadFile(filepath.Join(c1, sc.Text()))
		if err != nil || bytes.Count(data, []byte("\n")) < 400 || uniqueDef(string(data)) == "" {
			continue
		}
		picked = append(picked, sc.Text())
	}
	return picked, nil
}

func uniqueDef(s string) string {
	for line := range strings.Lines(s) {
		t := strings.TrimRight(line, "\n")
		if strings.HasPrefix(strings.TrimSpace(t), "def ") && strings.Count(s, t) == 1 {
			return t
		}
	}
	return ""
}

// hermesConf = setelan run hermes di C1.
type hermesConf struct {
	c1, home string
	patchSrc string // direktori asli target patch; "" = workspace di home (setelan F0)
	durable  bool   // fsync(file)+fsync(dir) pada target patch di dalam jendela waktu (ADR-082)
}

// patchTargetDir membuat direktori sementara target patch di bawah parent dan mencatat
// filesystem-nya di meta. hermes melihatnya lewat bind mount di `<home>/patch`: path tanpa
// leluhur `.git` (diagnostik LSP hermes hanya aktif di worktree git; setelan F0 tetap) dengan fsync
// nyata. parent "" = target di workspace `<home>/ws` (F0). tmpfs ditolak bila durable (fsync no-op).
func patchTargetDir(parent, home string, durable bool, meta map[string]string) (string, func(), error) {
	fsOf := parent
	if parent == "" {
		fsOf = filepath.Dir(home)
	}
	fs := cmdOut("findmnt", "-no", "FSTYPE,SOURCE", "-T", fsOf)
	if durable && strings.HasPrefix(fs, "tmpfs") {
		return "", nil, fmt.Errorf("target patch %s di tmpfs: fsync no-op, baseline durabel tidak sah (ADR-082); pakai -patch-dir", fsOf)
	}
	meta["patch_dir_fs"], meta["patch_durable"] = fs, fmt.Sprint(durable)
	if durable {
		meta["patch_method"] = "reset file + fsync(file)+fsync(dir) di luar jendela; jendela = handle_function_call(patch) + fsync(file) + fsync(dir) path yang sama (ADR-082 butir 6)"
	}
	if parent == "" {
		meta["patch_target"] = filepath.Join(home, "ws")
		return "", func() {}, nil
	}
	meta["patch_target"] = filepath.Join(home, "patch") + " = bind mount -patch-dir di namespace mount hermes (tanpa leluhur .git: LSP hermes nonaktif, setelan F0)"
	dir, err := os.MkdirTemp(parent, "ct10-patch-")
	if err != nil {
		return "", nil, err
	}
	return dir, func() { _ = os.RemoveAll(dir) }, nil
}

// cmdOut = stdout perintah (trim); string kosong bila gagal (hanya untuk meta mesin).
func cmdOut(name string, args ...string) string {
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for line := range strings.Lines(string(data)) {
		if k, v, ok := strings.Cut(line, ":"); ok && strings.TrimSpace(k) == "model name" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func benchHermes(hc hermesConf, q queries, runs int, tok *o200k.Tokenizer, rgC1 *corpusResult) ([]hermesOp, error) {
	c1, home := hc.c1, hc.home
	if !strings.HasPrefix(home, "/tmp/") {
		return nil, errors.New("home harus di /tmp")
	}
	if err := os.RemoveAll(home); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Join(home, "home"), 0o700); err != nil {
		return nil, err
	}
	driver, err := filepath.Abs("speed/hermes_driver.py")
	if err != nil {
		return nil, err
	}
	ws, pdir := filepath.Join(home, "ws"), filepath.Join(home, "ws")
	r := &speed.Runner{HermesDir: c1, Driver: driver, WS: ws, Env: []string{
		"PATH=" + os.Getenv("PATH"), "LANG=C.UTF-8", "HOME=" + filepath.Join(home, "home"),
		"HERMES_HOME=" + home, "PYTHONDONTWRITEBYTECODE=1", "OPENROUTER_API_KEY=inspect-only",
	}}
	if hc.patchSrc != "" {
		pdir = filepath.Join(home, "patch")
		if err := os.Mkdir(pdir, 0o700); err != nil {
			return nil, err
		}
		r.Bind = [2]string{hc.patchSrc, pdir}
	}
	ctx := context.Background()
	if err := r.Prepare(ctx); err != nil {
		return nil, err
	}
	ops := map[string]speed.Op{}
	var ids []string
	var hops []hermesOp
	add := func(id, kind, arg string, op speed.Op) {
		ids = append(ids, id)
		ops[id] = op
		hops = append(hops, hermesOp{ID: id, Kind: kind, Arg: arg})
	}
	mustJSON := func(v any) json.RawMessage { b, _ := json.Marshal(v); return b }
	golden := slices.Concat(q.Literal, q.Regex[:10])
	for i, pat := range golden {
		add(fmt.Sprintf("search-%02d", i), "search_files", pat, speed.Op{Kind: "tool", Tool: "search_files", Capture: true, FreshTask: true,
			Args: mustJSON(map[string]any{"pattern": pat, "path": c1})})
	}
	files, err := pickFiles(c1)
	if err != nil {
		return nil, err
	}
	for i, f := range files {
		add(fmt.Sprintf("read-%02d", i), "read_file", f, speed.Op{Kind: "tool", Tool: "read_file", FreshTask: true,
			Args: mustJSON(map[string]any{"path": filepath.Join(c1, f), "offset": 1, "limit": 400})})
		data, err := os.ReadFile(filepath.Join(c1, f))
		if err != nil {
			return nil, err
		}
		target := uniqueDef(string(data))
		dst := filepath.Join(pdir, fmt.Sprintf("patch_%02d.py", i))
		op := speed.Op{Kind: "tool", Tool: "patch",
			Reset: &speed.Reset{Path: dst, Content: string(data)},
			Args:  mustJSON(map[string]any{"path": dst, "old_string": target, "new_string": target + "  # f0"})}
		if hc.durable {
			op.Fsync = dst
		}
		add(fmt.Sprintf("patch-%02d", i), "patch", f, op)
		hops[len(hops)-1].Durable = hc.durable
	}
	res, err := r.RunInproc(ctx, ids, ops, runs)
	if err != nil {
		return nil, err
	}
	rgTok := map[string]int{}
	for _, t := range slices.Concat(rgC1.RgLiteral, rgC1.RgRegex) {
		rgTok[t.Query] = t.Tokens
	}
	for i := range hops {
		h := &hops[i]
		rr := res[h.ID]
		if rr == nil {
			h.Error = "tanpa hasil"
			continue
		}
		h.P50MS, h.P95MS, h.Error = rr.P50MS, rr.P95MS, rr.Error
		if rr.Output != "" {
			sum := sha256.Sum256([]byte(rr.Output))
			h.Tokens, h.OutputSHA, h.OutputSize = tok.Count(rr.Output), hex.EncodeToString(sum[:]), len(rr.Output)
			h.RgTokens = rgTok[h.Arg]
		}
	}
	return hops, nil
}

func summarize(rep *report) {
	per, per95 := map[string][]float64{}, map[string][]float64{}
	tokH, tokRg := 0, 0
	for _, h := range rep.Hermes {
		if h.Error == "" {
			per[h.Kind] = append(per[h.Kind], h.P50MS)
			per95[h.Kind] = append(per95[h.Kind], h.P95MS)
		}
		tokH += h.Tokens
		tokRg += h.RgTokens
	}
	for k, v := range per {
		rep.Summary["hermes_"+k+"_median_p50_ms"] = speed.Percentile(v, 50)
		rep.Summary["hermes_"+k+"_median_p95_ms"] = speed.Percentile(per95[k], 50)
		rep.Summary["hermes_"+k+"_ok_ops"] = float64(len(v))
	}
	rep.Summary["ct11_hermes_tokens_sum"] = float64(tokH)
	rep.Summary["ct11_rg_tokens_sum"] = float64(tokRg)
	if c1 := rep.Corpora["C1"]; c1 != nil {
		rep.Summary["c1_rg_literal_median_p50_ms"] = c1.MedianP50Lit
	}
}
