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
	UpstreamErrors  []string   `json:"upstream_errors,omitempty"`
	Category        string     `json:"category"` // ok|no_progress|upstream_unavailable|task_timeout|harness_error
	Damnbot         string     `json:"damnbot"`
}

// Runner menjalankan task smoke secara berurutan lewat satu Recorder.
type Runner struct {
	Recorder    *Recorder
	ProxyURL    string // mis. http://127.0.0.1:PORT/v1
	Hermes      *Hermes
	Damnbot     *Damnbot
	OutDir      string // rekaman <id>.hermes.proxy.jsonl.gz
	WorkRoot    string // workdir per task (di /tmp)
	Timeout     time.Duration
	TaskRetries int
	FirstGuess  float64 // perkiraan biaya task pertama untuk proyeksi anggaran
	Log         io.Writer
}

// Run menjalankan semua task. Sebelum tiap task, proyeksi = terpakai + max(biaya task
// termahal sejauh ini, FirstGuess) × 1,5; bila > cap efektif (Recorder.Cap) task dilewati
// (skipped_budget). Proxy tetap menolak setiap request di atas cap (reservasi, ADR-075).
func (r *Runner) agent() string {
	if r.Damnbot != nil {
		return "damnbot"
	}
	return "hermes"
}

func (r *Runner) Run(ctx context.Context, tasks []evals.Task) []TaskResult {
	var out []TaskResult
	worst := r.FirstGuess
	for i := range tasks {
		t := &tasks[i]
		res := TaskResult{ID: t.ID, Set: t.Set, Kind: t.Kind, Agent: r.agent(), Damnbot: DamnbotNA, HarnessErrors: []string{}}
		if cap := r.Recorder.Cap(); cap > 0 && r.Recorder.Spent()+worst*1.5 > cap {
			res.Status = "skipped_budget"
			out = append(out, res)
			fmt.Fprintf(r.Log, "%s: dilewati, proyeksi %.4f > cap %.2f\n", t.ID, r.Recorder.Spent()+worst*1.5, cap)
			continue
		}
		for attempt := 0; ; attempt++ {
			r.runOne(ctx, t, &res)
			if res.Category != "upstream_unavailable" || attempt >= r.TaskRetries {
				break
			}
			fmt.Fprintf(r.Log, "%s: upstream unavailable, retry %d/%d\n", t.ID, attempt+1, r.TaskRetries)
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(attempt+1) * 2 * time.Second):
			}
			res = TaskResult{ID: t.ID, Set: t.Set, Kind: t.Kind, Agent: r.agent(), Damnbot: DamnbotNA, HarnessErrors: []string{}}
		}
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

	res.Recording = t.ID + "." + r.agent() + ".proxy.jsonl.gz"
	recPath := filepath.Join(r.OutDir, res.Recording)
	f, err := os.Create(recPath)
	if err != nil {
		herr("create recording: %v", err)
		return
	}
	zw := gzip.NewWriter(f)
	r.Recorder.Begin(r.agent(), t.ID, zw)
	var run AgentRun
	var runErr error
	if r.Damnbot != nil {
		run, runErr = r.Damnbot.Run(ctx, workdir, Prompt(t, baseURL), r.ProxyURL, r.Timeout)
		res.Damnbot = fmt.Sprintf("exit=%d stop=%s/%s", run.ExitCode, run.StopReason, run.StopDetail)
	} else {
		run, runErr = r.Hermes.Run(ctx, workdir, Prompt(t, baseURL), r.ProxyURL, r.Timeout)
	}
	usage, perrs := r.Recorder.End()
	res.Run, res.Usage = run, usage
	res.UpstreamErrors = append(res.UpstreamErrors, perrs...)
	if runErr != nil && !run.TimedOut {
		herr("%v", runErr)
	}
	if err := zw.Close(); err != nil {
		herr("close recording: %v", err)
	}
	if err := f.Close(); err != nil {
		herr("close recording: %v", err)
	}
	if usage.Calls == 0 {
		res.UpstreamErrors = append(res.UpstreamErrors, "tidak ada completion terekam (provider tidak pernah menjawab)")
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
	res.Category = categorizeRun(run, res.Usage, runErr)
}

func categorizeRun(run AgentRun, usage Usage, runErr error) string {
	switch {
	case run.TimedOut:
		return "task_timeout"
	case runErr != nil:
		return "harness_error"
	case run.StopReason == "error":
		return "upstream_unavailable"
	case run.StopReason == "end_turn" && run.StopDetail == "completed":
		return "ok"
	case run.StopReason == "end_turn" && run.StopDetail == "no_progress":
		return "no_progress"
	case run.StopReason == "end_turn":
		return "model_other"
	default:
		return "upstream_unavailable"
	}
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
