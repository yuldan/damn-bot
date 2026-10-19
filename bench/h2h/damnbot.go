package h2h

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"syscall"
	"time"
)

// Damnbot drives the F1 headless runner (cmd/damnbot) against the recording
// proxy. The proxy URL is passed as the OpenAI base URL; the agent uses a
// dummy key, so the real key stays in the proxy only.
type Damnbot struct {
	Binary string
	Model  string
}

type damnbotTurn struct {
	Schema     string `json:"schema"`
	SessionID  string `json:"session_id"`
	StopReason string `json:"stop_reason"`
	StopDetail string `json:"stop_detail"`
	Answer     string `json:"answer"`
	Error      string `json:"error"`
	Usage      struct {
		Input       int64    `json:"input_tokens"`
		CachedInput int64    `json:"cached_input_tokens"`
		Output      int64    `json:"output_tokens"`
		CostUSD     *float64 `json:"cost_usd"`
		Iterations  int      `json:"iterations"`
	} `json:"usage"`
}

// Run executes one task: `damnbot run --output json --approval auto
// --data-dir <tmp> -p <prompt>` with the model routed through proxyURL.
func (d *Damnbot) Run(ctx context.Context, workdir, prompt, proxyURL string, timeout time.Duration) (AgentRun, error) {
	var run AgentRun
	dataDir, err := os.MkdirTemp("", "damnbot-h2h-")
	if err != nil {
		return run, fmt.Errorf("h2h: data dir: %w", err)
	}
	defer os.RemoveAll(dataDir)
	model := d.Model
	if !strings.Contains(model, "/") {
		model = "openai/" + model
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, d.Binary, "run",
		"--output", "json", "--approval", "auto", "--data-dir", dataDir,
		"--model", model, "--base-url", proxyURL, "--api-key", DummyKey, "-p", prompt)
	cmd.Dir = workdir
	cmd.Env = append(CleanEnv(), "HOME="+dataDir, "NO_COLOR=1", "TERM=dumb")
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error { return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL) }
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	t0 := time.Now()
	runErr := cmd.Run()
	run.DurationS = time.Since(t0).Seconds()
	run.TimedOut = cctx.Err() == context.DeadlineExceeded
	run.Stderr = tail(stderr.String(), 4000)
	if cmd.ProcessState != nil {
		run.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil && !run.TimedOut && run.ExitCode == 0 {
		return run, fmt.Errorf("h2h: damnbot: %w", runErr)
	}
	var turn damnbotTurn
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &turn); err != nil {
		return run, fmt.Errorf("h2h: parse turn_result (%d bytes): %w; stderr=%s", stdout.Len(), err, run.Stderr)
	}
	if turn.Schema != "damnbot.turn_result/v1" {
		return run, fmt.Errorf("h2h: schema %q", turn.Schema)
	}
	run.Answer = turn.Answer
	run.StopReason = turn.StopReason
	run.StopDetail = turn.StopDetail
	return run, nil
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[len(s)-n:]
}
