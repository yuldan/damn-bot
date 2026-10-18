package h2h

import (
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/damnbot/damnbot/bench/evals"
)

// DamnbotNA adalah isian sisi Damn-Bot sampai agent loop ada (F1); tidak dipalsukan.
const DamnbotNA = "N/A: belum ada agent loop (F1)"

// TaskResult adalah hasil satu task × satu agent × satu run.
type TaskResult struct {
	ID              string     `json:"id"`
	Set             string     `json:"set"`
	Kind            string     `json:"kind"`
	Agent           string     `json:"agent"`
	Status          string     `json:"status"` // ran | skipped_budget
	Run             AgentRun   `json:"run"`
	Verdict         *Verdict   `json:"verdict,omitempty"` // informatif di smoke F0
	Usage           Usage      `json:"usage"`
	ToolCalls       []ToolCall `json:"tool_calls"`
	PageHits        []Hit      `json:"page_hits,omitempty"`
	Recording       string     `json:"recording"`
	RecordingSHA256 string     `json:"recording_sha256"`
	HarnessErrors   []string   `json:"harness_errors"`
	Damnbot         string     `json:"damnbot"`
}

// Runner menjalankan task smoke secara berurutan lewat satu Recorder.
type Runner struct {
	Recorder   *Recorder
	ProxyURL   string // mis. http://127.0.0.1:PORT/v1
	Hermes     *Hermes
	OutDir     string // rekaman <id>.hermes.proxy.jsonl.gz
	WorkRoot   string // workdir per task (di /tmp)
	Timeout    time.Duration
	FirstGuess float64 // perkiraan biaya task pertama untuk proyeksi anggaran
	Log        io.Writer
}

// Run menjalankan semua task. Sebelum tiap task, proyeksi = terpakai + max(biaya task
// termahal sejauh ini, FirstGuess) × 1,5; bila > CapUSD task dilewati (skipped_budget).
func (r *Runner) Run(ctx context.Context, tasks []evals.Task) []TaskResult {
	var out []TaskResult
	worst := r.FirstGuess
	for i := range tasks {
		t := &tasks[i]
		res := TaskResult{ID: t.ID, Set: t.Set, Kind: t.Kind, Agent: "hermes", Damnbot: DamnbotNA, HarnessErrors: []string{}}
		if cap := r.Recorder.CapUSD; cap > 0 && r.Recorder.Spent()+worst*1.5 > cap {
			res.Status = "skipped_budget"
			out = append(out, res)
			fmt.Fprintf(r.Log, "%s: dilewati, proyeksi %.4f > cap %.2f\n", t.ID, r.Recorder.Spent()+worst*1.5, cap)
			continue
		}
		r.runOne(ctx, t, &res)
		worst = max(worst, res.Usage.CostUSD)
		fmt.Fprintf(r.Log, "%s: exit=%d timeout=%v pass=%v calls=%d cost=%.4f total=%.4f harness_errors=%d\n",
			t.ID, res.Run.ExitCode, res.Run.TimedOut, res.Verdict != nil && res.Verdict.Pass,
			res.Usage.Calls, res.Usage.CostUSD, r.Recorder.Spent(), len(res.HarnessErrors))
		out = append(out, res)
	}
	return out
}

func (r *Runner) runOne(ctx context.Context, t *evals.Task, res *TaskResult) {
	res.Status = "ran"
	herr := func(format string, a ...any) {
		res.HarnessErrors = append(res.HarnessErrors, fmt.Sprintf(format, a...))
	}

	workdir := filepath.Join(r.WorkRoot, t.ID)
	if err := os.RemoveAll(workdir); err != nil {
		herr("reset workdir: %v", err)
		return
	}
	if err := Materialize(t, workdir); err != nil {
		herr("%v", err)
		return
	}

	// Server halaman/fault per task (state fault tidak bocor antar task).
	pages := NewPageServer(t)
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		herr("listen page server: %v", err)
		return
	}
	srv := &http.Server{Handler: pages, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(ln) }()
	defer srv.Close()
	baseURL := "http://" + ln.Addr().String()

	res.Recording = t.ID + ".hermes.proxy.jsonl.gz"
	recPath := filepath.Join(r.OutDir, res.Recording)
	f, err := os.Create(recPath)
	if err != nil {
		herr("create recording: %v", err)
		return
	}
	zw := gzip.NewWriter(f)
	r.Recorder.Begin("hermes", t.ID, zw)
	run, runErr := r.Hermes.Run(ctx, workdir, Prompt(t, baseURL), r.ProxyURL, r.Timeout)
	usage, perrs := r.Recorder.End()
	res.Run, res.Usage = run, usage
	res.HarnessErrors = append(res.HarnessErrors, perrs...)
	if runErr != nil {
		herr("%v", runErr)
	}
	if err := zw.Close(); err != nil {
		herr("close recording: %v", err)
	}
	if err := f.Close(); err != nil {
		herr("close recording: %v", err)
	}
	if usage.Calls == 0 {
		herr("tidak ada completion terekam (agent tidak pernah memanggil provider lewat proxy)")
	}
	if sum, err := fileSHA256(recPath); err == nil {
		res.RecordingSHA256 = sum
	} else {
		herr("hash recording: %v", err)
	}
	calls, err := ToolCallsFromRecording(recPath)
	if err != nil {
		herr("baca recording: %v", err)
	}
	res.ToolCalls = calls
	if res.ToolCalls == nil {
		res.ToolCalls = []ToolCall{}
	}
	res.PageHits = pages.Hits()
	v := Grade(ctx, t, workdir, run.Answer, calls)
	res.Verdict = &v
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
