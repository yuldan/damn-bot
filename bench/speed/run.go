package speed

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

// Result = sampel satu op (milidetik) + statistik.
type Result struct {
	SamplesMS []float64 `json:"samples_ms,omitempty"`
	P50MS     float64   `json:"p50_ms,omitempty"`
	P95MS     float64   `json:"p95_ms,omitempty"`
	MaxRSSKB  int64     `json:"max_rss_kb,omitempty"` // cli: ru_maxrss proses hermes
	Error     string    `json:"error,omitempty"`
	Output    string    `json:"-"` // output tool bila Op.Capture (tidak ditulis ke artefak)
	// pty: output pertama, settle, RSS idle per run.
	PTY []map[string]float64 `json:"pty,omitempty"`
}

// Percentile = nearest-rank pada sampel terurut (p dalam 0..100).
func Percentile(s []float64, p float64) float64 {
	if len(s) == 0 {
		return 0
	}
	c := slices.Clone(s)
	slices.Sort(c)
	idx := int(float64(len(c))*p/100+0.999999) - 1
	return c[max(0, min(idx, len(c)-1))]
}

func (r *Result) finish() {
	r.P50MS, r.P95MS = Percentile(r.SamplesMS, 50), Percentile(r.SamplesMS, 95)
}

// Runner menjalankan op hermes tanpa jaringan.
type Runner struct {
	HermesDir string   // root hermes-agent (cwd semua proses)
	Driver    string   // path absolut hermes_driver.py
	WS        string   // workspace fixture
	Env       []string // HOME/HERMES_HOME di /tmp
	CLIRuns   int
	cliCache  map[string]*Result
}

// command membuat proses di namespace jaringan baru yang hanya punya loopback (dinaikkan
// sebelum exec): RPC lokal hermes (mis. kernel execute_code) jalan, tetapi tidak ada rute
// keluar sehingga panggilan LLM/HTTP mustahil. Grup proses sendiri: timeout membunuh semua turunan.
func (r *Runner) command(ctx context.Context, args ...string) *exec.Cmd {
	argv := append([]string{"-rn", "sh", "-c", `ip link set lo up && exec "$0" "$@"`, "python3"}, args...)
	cmd := exec.CommandContext(ctx, "unshare", argv...)
	cmd.Dir, cmd.Env = r.HermesDir, r.Env
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

// Prepare menjalankan mode `prepare` driver.
func (r *Runner) Prepare(ctx context.Context) error {
	out, err := r.command(ctx, r.Driver, "prepare", r.WS).CombinedOutput()
	if err != nil {
		return fmt.Errorf("speed: prepare: %w: %s", err, tail(out))
	}
	return nil
}

func (r *Runner) expand(s string) string { return strings.ReplaceAll(s, "$WS", r.WS) }

// RunCLI mengukur `python3 -m hermes_cli.main <argv>` cold start per run (1 warm-up).
// Argv identik di-cache (mis. CLI-022 dan OPS-012).
func (r *Runner) RunCLI(ctx context.Context, op Op) *Result {
	argv := make([]string, len(op.Argv))
	for i, a := range op.Argv {
		argv[i] = r.expand(a)
	}
	key := strings.Join(argv, "\x00")
	if res, ok := r.cliCache[key]; ok {
		return res
	}
	runs := op.Runs
	if runs == 0 {
		runs = r.CLIRuns
	}
	res := &Result{}
	for i := range runs + 1 {
		cctx, cancel := context.WithTimeout(ctx, 120*time.Second)
		cmd := r.command(cctx, append([]string{"-m", "hermes_cli.main"}, argv...)...)
		var out bytes.Buffer
		cmd.Stdout, cmd.Stderr = &out, &out
		t0 := time.Now()
		err := cmd.Run()
		dt := time.Since(t0)
		cancel()
		var ee *exec.ExitError
		if err != nil && !(errors.As(err, &ee) && slices.Contains(op.OkExit, ee.ExitCode())) {
			res.Error = fmt.Sprintf("%v: %s", err, tail(out.Bytes()))
			break
		}
		if ru, ok := cmd.ProcessState.SysUsage().(*syscall.Rusage); ok {
			res.MaxRSSKB = max(res.MaxRSSKB, ru.Maxrss)
		}
		if i > 0 {
			res.SamplesMS = append(res.SamplesMS, float64(dt.Microseconds())/1000)
		}
	}
	res.finish()
	if r.cliCache == nil {
		r.cliCache = map[string]*Result{}
	}
	r.cliCache[key] = res
	return res
}

type driverOp struct {
	ID        string          `json:"id"`
	Kind      string          `json:"kind"`
	Tool      string          `json:"tool,omitempty"`
	Args      json.RawMessage `json:"args,omitempty"`
	Command   string          `json:"command,omitempty"`
	Setup     string          `json:"setup,omitempty"`
	Code      string          `json:"code,omitempty"`
	Reset     *Reset          `json:"reset,omitempty"`
	Runs      int             `json:"runs"`
	Warmup    int             `json:"warmup"`
	Capture   bool            `json:"capture,omitempty"`
	FreshTask bool            `json:"fresh_task,omitempty"`
}

// inprocTimeout = batas satu proses driver per op (init hermes + warm-up + N run).
const inprocTimeout = 5 * time.Minute

// RunInproc mengukur op tool/slash/py; setiap op di proses driver sendiri (warm di dalam proses,
// N run) sehingga op yang menggantung hanya menggagalkan dirinya (timeout → Error).
func (r *Runner) RunInproc(ctx context.Context, ids []string, ops map[string]Op, runs int) (map[string]*Result, error) {
	res := map[string]*Result{}
	for _, id := range ids {
		op := ops[id]
		d := driverOp{ID: id, Kind: op.Kind, Tool: op.Tool, Command: op.Command, Setup: op.Setup, Code: op.Code, Runs: runs, Warmup: 1, Capture: op.Capture, FreshTask: op.FreshTask}
		if op.Args != nil {
			d.Args = json.RawMessage(r.expand(string(op.Args)))
		}
		if op.Reset != nil {
			d.Reset = &Reset{Path: r.expand(op.Reset.Path), Content: op.Reset.Content}
		}
		got, err := r.runDriver(ctx, d)
		if err != nil {
			res[id] = &Result{Error: err.Error()}
			continue
		}
		maps.Copy(res, got)
	}
	return res, nil
}

func (r *Runner) runDriver(ctx context.Context, d driverOp) (map[string]*Result, error) {
	job := struct {
		Ops []driverOp `json:"ops"`
	}{Ops: []driverOp{d}}
	dir, err := os.MkdirTemp("", "f0-speed-job")
	if err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	jobPath, outPath := filepath.Join(dir, "jobs.json"), filepath.Join(dir, "out.json")
	data, err := json.Marshal(job)
	if err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	if err := os.WriteFile(jobPath, data, 0o600); err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, inprocTimeout)
	defer cancel()
	if out, err := r.command(cctx, r.Driver, "inproc", jobPath, outPath).CombinedOutput(); err != nil {
		if cctx.Err() != nil {
			return nil, fmt.Errorf("timeout > %s (op menggantung offline)", inprocTimeout)
		}
		return nil, fmt.Errorf("driver: %w: %s", err, tail(out))
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	var got struct {
		Results []struct {
			ID        string    `json:"id"`
			SamplesMS []float64 `json:"samples_ms"`
			Error     *string   `json:"error"`
			Output    string    `json:"output"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil, fmt.Errorf("speed: inproc out: %w", err)
	}
	res := map[string]*Result{}
	for _, g := range got.Results {
		rr := &Result{SamplesMS: g.SamplesMS, Output: g.Output}
		if g.Error != nil {
			rr.Error = *g.Error
		}
		rr.finish()
		res[g.ID] = rr
	}
	return res, nil
}

// RunPTY mengukur startup interaktif (output pertama, settle, RSS idle 5 s).
func (r *Runner) RunPTY(ctx context.Context, op Op) (*Result, error) {
	dir, err := os.MkdirTemp("", "f0-speed-pty")
	if err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	defer func() { _ = os.RemoveAll(dir) }()
	out := filepath.Join(dir, "pty.json")
	args := append([]string{r.Driver, "pty", out, fmt.Sprint(op.Runs), "--", "python3", "-m", "hermes_cli.main"}, op.Argv...)
	if b, err := r.command(ctx, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("speed: pty: %w: %s", err, tail(b))
	}
	raw, err := os.ReadFile(out)
	if err != nil {
		return nil, fmt.Errorf("speed: %w", err)
	}
	var got struct {
		Runs []map[string]*float64 `json:"runs"`
	}
	if err := json.Unmarshal(raw, &got); err != nil {
		return nil, fmt.Errorf("speed: pty out: %w", err)
	}
	res := &Result{}
	for _, run := range got.Runs {
		m := map[string]float64{}
		for k, v := range run {
			if v == nil {
				return nil, errors.New("speed: pty: tidak ada output dari hermes")
			}
			m[k] = *v
		}
		res.PTY = append(res.PTY, m)
		// Sampel = waktu frame pertama. settle/idle RSS tidak valid di netns (lihat F0-REPORT: chat
		// keluar "Gateway connection failed"), tetap disimpan apa adanya di PTY.
		res.SamplesMS = append(res.SamplesMS, m["first_output_ms"])
	}
	res.finish()
	return res, nil
}

func tail(b []byte) string {
	s := strings.ToValidUTF8(string(b), "?")
	if len(s) > 400 {
		s = s[len(s)-400:]
	}
	return strings.TrimSpace(s)
}
