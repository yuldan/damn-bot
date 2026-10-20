package h2h

import (
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DummyKey adalah kunci yang dilihat agent; proxy menggantinya dengan kunci upstream asli.
const DummyKey = "h2h-proxy-injects-upstream-key"

// Hermes menjalankan `hermes chat -q` satu kali per task (CLI one-shot, non-TTY).
type Hermes struct {
	Dir      string // checkout hermes-agent (read-only; dipakai lewat PYTHONPATH)
	Home     string // HERMES_HOME; di-reset per task agar memori/sesi tidak bocor antar task
	Python   string
	Model    string
	MaxTurns int
}

// AgentRun adalah hasil mentah satu run agent.
type AgentRun struct {
	ExitCode  int     `json:"exit_code"`
	TimedOut  bool    `json:"timed_out"`
	DurationS float64 `json:"duration_s"`
	Answer    string  `json:"answer"`
	Stderr    string  `json:"stderr_tail"`
	// DaemonsStopped = proses dengan HERMES_HOME ini yang masih hidup setelah `chat` selesai
	// (gateway daemon detached yang di-spawn hermes) dan dihentikan harness.
	DaemonsStopped int    `json:"daemons_stopped"`
	StopReason     string `json:"stop_reason,omitempty"`
	StopDetail     string `json:"stop_detail,omitempty"`
}

// config adalah config.yaml minimum: provider custom OpenAI-compatible ke proxy lokal.
func (h *Hermes) config(proxyURL string) string {
	return fmt.Sprintf(`model:
  provider: custom
  default: %q
  base_url: %q
  api_key: %q
  api_mode: chat_completions
agent:
  max_turns: %d
`, h.Model, proxyURL, DummyKey, h.MaxTurns)
}

// Run menjalankan satu task; error hanya untuk kegagalan harness (bukan exit agent).
func (h *Hermes) Run(ctx context.Context, workdir, prompt, proxyURL string, timeout time.Duration) (AgentRun, error) {
	var run AgentRun
	if n := StopHomeProcesses(h.Home); n > 0 {
		return run, fmt.Errorf("h2h: %d proses hermes sisa run sebelumnya masih hidup di %s", n, h.Home)
	}
	if err := os.RemoveAll(h.Home); err != nil {
		return run, fmt.Errorf("h2h: reset HERMES_HOME: %w", err)
	}
	if err := os.MkdirAll(h.Home, 0o700); err != nil {
		return run, fmt.Errorf("h2h: HERMES_HOME: %w", err)
	}
	if err := os.WriteFile(filepath.Join(h.Home, "config.yaml"), []byte(h.config(proxyURL)), 0o600); err != nil {
		return run, fmt.Errorf("h2h: config: %w", err)
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, h.Python, "-m", "hermes_cli.main", "chat",
		"-q", prompt, "-Q", "--yolo", "--source", "tool", "--max-turns", fmt.Sprint(h.MaxTurns))
	cmd.Dir = workdir
	cmd.Env = append(CleanEnv(), "HERMES_HOME="+h.Home, "PYTHONDONTWRITEBYTECODE=1",
		"PYTHONPATH="+h.Dir, "HOME="+h.Home, "NO_COLOR=1", "TERM=dumb",
		// HOME terisolasi: cache Go di bawah HERMES_HOME harus bisa dihapus saat reset.
		"GOTOOLCHAIN=local", "GOFLAGS=-modcacherw")
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	t0 := time.Now()
	err := cmd.Run()
	run.DurationS = time.Since(t0).Seconds()
	run.TimedOut = errors.Is(cctx.Err(), context.DeadlineExceeded)
	run.DaemonsStopped = StopHomeProcesses(h.Home)
	var ee *exec.ExitError
	switch {
	case err == nil:
	case errors.As(err, &ee):
		run.ExitCode = ee.ExitCode()
	case run.TimedOut:
		run.ExitCode = -1
	default:
		return run, fmt.Errorf("h2h: start hermes: %w", err)
	}
	run.Answer = finalAnswer(stdout.String())
	run.Stderr = tailString(stderr.Bytes(), 2000)
	return run, nil
}

// StopHomeProcesses menghentikan (SIGTERM, lalu SIGKILL setelah 5 s) semua proses milik
// user ini yang environment-nya memuat HERMES_HOME=home persis; mengembalikan jumlahnya.
// Gateway daemon hermes lepas dari process group (detached) sehingga tidak ikut terbunuh
// oleh Cancel. Hanya Linux (/proc).
func StopHomeProcesses(home string) int {
	want := []byte("HERMES_HOME=" + home)
	find := func() []int {
		var pids []int
		ents, _ := os.ReadDir("/proc")
		for _, e := range ents {
			pid, err := strconv.Atoi(e.Name())
			if err != nil || pid == os.Getpid() {
				continue
			}
			env, err := os.ReadFile("/proc/" + e.Name() + "/environ") // gagal untuk proses user lain
			if err != nil {
				continue
			}
			for _, kv := range bytes.Split(env, []byte{0}) {
				if bytes.Equal(kv, want) {
					pids = append(pids, pid)
					break
				}
			}
		}
		return pids
	}
	pids := find()
	for _, p := range pids {
		_ = syscall.Kill(p, syscall.SIGTERM)
	}
	for deadline := time.Now().Add(5 * time.Second); time.Now().Before(deadline) && len(find()) > 0; {
		time.Sleep(100 * time.Millisecond)
	}
	for _, p := range find() {
		_ = syscall.Kill(p, syscall.SIGKILL)
	}
	return len(pids)
}

// CleanEnv membuang kredensial provider dari environment agar hermes hanya melihat proxy.
func CleanEnv() []string {
	var out []string
	for _, kv := range os.Environ() {
		k, _, _ := strings.Cut(kv, "=")
		u := strings.ToUpper(k)
		if strings.Contains(u, "API_KEY") || strings.Contains(u, "TOKEN") || strings.Contains(u, "SECRET") ||
			strings.HasPrefix(u, "OPENAI_") || strings.HasPrefix(u, "OPENROUTER_") || strings.HasPrefix(u, "ANTHROPIC_") ||
			strings.HasPrefix(u, "HERMES_") || u == "HOME" || u == "PYTHONPATH" {
			continue
		}
		out = append(out, kv)
	}
	return out
}

// finalAnswer membuang baris `session_id: …` yang dicetak mode -Q.
func finalAnswer(stdout string) string {
	var keep []string
	for _, l := range strings.Split(stdout, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "session_id:") {
			keep = append(keep, l)
		}
	}
	return strings.TrimSpace(strings.Join(keep, "\n"))
}

// ToolCallsFromRecording mengambil tool call unik dari pesan assistant di request yang
// terekam (riwayat percakapan yang dikirim agent ke provider).
func ToolCallsFromRecording(path string) ([]ToolCall, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	type wireCall struct {
		ID       string `json:"id"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	seen := map[string]bool{}
	var out []ToolCall
	sc := bufio.NewScanner(zr)
	sc.Buffer(make([]byte, 0, 1<<20), 256<<20)
	for sc.Scan() {
		var rec struct {
			ReqBody struct {
				Messages []struct {
					Role      string     `json:"role"`
					ToolCalls []wireCall `json:"tool_calls"`
				} `json:"messages"`
			} `json:"req_body"`
		}
		if json.Unmarshal(sc.Bytes(), &rec) != nil {
			continue // req_body non-JSON (GET /models)
		}
		for _, m := range rec.ReqBody.Messages {
			for _, c := range m.ToolCalls {
				key := c.ID + "\x00" + c.Function.Name + "\x00" + c.Function.Arguments
				if !seen[key] {
					seen[key] = true
					out = append(out, ToolCall{Name: c.Function.Name, Arguments: c.Function.Arguments})
				}
			}
		}
	}
	return out, sc.Err()
}
