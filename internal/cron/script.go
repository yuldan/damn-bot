package cron

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/redact"
)

var (
	// ErrTimeout reports a script killed by its timeout.
	ErrTimeout = errors.New("cron: script timeout")
	// ErrApproval reports a script the job's approval mode does not allow.
	ErrApproval = errors.New("cron: script denied")
)

// ExitError carries a process exit code to the monitor (cron_runs.stop_reason).
type ExitError struct {
	Code int
	Err  error
}

func (e *ExitError) Error() string { return e.Err.Error() }
func (e *ExitError) Unwrap() error { return e.Err }

// ExitCode reports the process exit code.
func (e *ExitError) ExitCode() int { return e.Code }

// ScriptExecutor runs Job.Script through internal/exec (process-group kill,
// timeout, SEC-02 env), never a second executor. AUTO-007 allows 3600 s but
// exec caps one call at 600 s.
type ScriptExecutor struct {
	Runner    *exec.Runner
	Redactor  *redact.Redactor
	Parent    []string // nil: os.Environ(), still filtered by exec.EnvPolicy
	Timeout   time.Duration
	MaxOutput int
}

// Run executes one script job; a timeout is ErrTimeout.
func (e *ScriptExecutor) Run(ctx context.Context, j *Job) (string, error) {
	if j.Script == nil || len(j.Script.Argv) == 0 {
		return "", errors.New("cron: job has no script")
	}
	if err := approvable(j); err != nil {
		return "", err
	}
	if e.Runner == nil {
		return "", errors.New("cron: nil exec runner")
	}
	parent := e.Parent
	if parent == nil {
		parent = os.Environ()
	}
	spec, _, err := exec.Prepare(&exec.OneShot{
		Direct: j.Script.Argv, Dir: j.Cwd, Root: j.Cwd,
		Timeout: e.timeout(j), Parent: parent, Policy: &exec.EnvPolicy{},
	})
	if err != nil {
		return "", fmt.Errorf("cron: prepare script: %w", err)
	}
	res, err := e.Runner.Run(ctx, &spec)
	if err != nil {
		return "", fmt.Errorf("cron: run script: %w", err)
	}
	out := e.cap(res.Output)
	switch res.Status {
	case exec.StatusExited:
		if res.ExitCode != nil && *res.ExitCode != 0 {
			return out, &ExitError{Code: *res.ExitCode, Err: fmt.Errorf("cron: script exit %d", *res.ExitCode)}
		}
		return out, nil
	case exec.StatusTimeout:
		return out, fmt.Errorf("%w after %s", ErrTimeout, e.timeout(j))
	case exec.StatusSignaled, exec.StatusIdleTimeout, exec.StatusWaitingForInput,
		exec.StatusCancelledConfirmed, exec.StatusStillRunning, exec.StatusSpawnFailed:
		return out, fmt.Errorf("cron: script %s %s", res.Status, res.Signal)
	}
	return out, fmt.Errorf("cron: script status %q", res.Status)
}

func (e *ScriptExecutor) timeout(j *Job) time.Duration {
	t := e.Timeout
	if j.Script.Timeout > 0 {
		t = j.Script.Timeout
	}
	if t <= 0 {
		t = exec.MaxTimeout
	}
	return min(t, exec.MaxTimeout)
}

func (e *ScriptExecutor) cap(out []byte) string {
	if e.Redactor != nil {
		out = e.Redactor.Redact(out)
	}
	limit := e.MaxOutput
	if limit <= 0 {
		limit = 64 << 10
	}
	if len(out) <= limit {
		return string(out)
	}
	return string(append(out[:limit:limit], fmt.Sprintf("\n[truncated %d bytes]", len(out)-limit)...))
}

// approvable is the classifier gate (SPEC §9.8): a cron run is headless, so
// only approval_mode "auto" for a plainly mutating command may run.
func approvable(j *Job) error {
	d := classify.Bash
	if runtime.GOOS == "windows" {
		d = classify.Cmd
	}
	v := classify.Classify(strings.Join(j.Script.Argv, " "), &classify.Env{Roots: []string{j.Cwd}, Cwd: j.Cwd, Dialect: d})
	if j.ApprovalMode == "auto" && v.Class == classify.Mutating && v.Flags&classify.DeniedPath == 0 {
		return nil
	}
	return fmt.Errorf("%w: approval_mode %q, class %s flags %v", ErrApproval, j.ApprovalMode, v.Class, v.Flags.Names())
}
