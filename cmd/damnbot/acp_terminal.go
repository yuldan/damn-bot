package main

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/acp"
	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/tools"
)

// ACP client terminal limits (GAP-UX-01 butir b). Output is polled, bounded
// and redacted here; the client never receives a secret-bearing env value.
const (
	acpTerminalCap      = 1 << 20
	acpTerminalPoll     = 50 * time.Millisecond
	acpTerminalKillWait = 5 * time.Second
	acpTerminalName     = "acp-client-terminal"
	acpTerminalMarker   = "\n[truncated: client terminal output exceeded 1 MiB]"
)

var acpTerminalSecretName = regexp.MustCompile(`(?i)(key|token|secret|password|passwd|credential|authorization|auth)`)

// acpTerminalRunner implements tools.ShellRunner on the ACP client terminal.
// It reports tools.ErrTerminalUnsupported when the client lacks the
// capability, so the shell tool falls back locally and records one event.
type acpTerminalRunner struct {
	term *acp.Terminal
	red  *redact.Redactor
}

func (r *acpTerminalRunner) Run(ctx context.Context, spec *exec.Spec) (exec.Result, error) {
	if !r.term.Supports() || len(spec.Argv) == 0 {
		return exec.Result{}, tools.ErrTerminalUnsupported
	}
	timeout := spec.Timeout
	if timeout <= 0 {
		timeout = exec.DefaultTimeout
	}
	id, err := r.term.Create(ctx, spec.Argv[0], spec.Argv[1:], spec.Dir, r.env(spec.Env), acpTerminalCap)
	if err != nil {
		return exec.Result{Runner: acpTerminalName, Status: exec.StatusSpawnFailed}, fmt.Errorf("acp terminal create: %w", err)
	}
	start := time.Now()
	defer r.release(ctx, id)
	deadline := start.Add(timeout)
	for {
		res, finished, serr := r.step(ctx, id, start, deadline)
		if serr != nil || finished {
			return res, serr
		}
		select {
		case <-ctx.Done():
			r.kill(ctx, id)
			return r.result(id, start, nil, 0, false, r.waitAfterKill(ctx, id), exec.StatusCancelledConfirmed), nil
		case <-time.After(acpTerminalPoll):
		}
	}
}

// step polls the client once: output is cumulative, so each call replaces the
// buffer. finished reports an exit, a cap kill or the deadline.
func (r *acpTerminalRunner) step(ctx context.Context, id string, start time.Time, deadline time.Time) (exec.Result, bool, error) {
	out, truncated, exit, err := r.term.Output(ctx, id)
	if err != nil {
		r.kill(ctx, id)
		return exec.Result{Runner: acpTerminalName, Status: exec.StatusSpawnFailed, Duration: time.Since(start)}, true,
			fmt.Errorf("acp terminal output: %w", err)
	}
	body := out
	if len(body) > acpTerminalCap || truncated {
		body = append(append([]byte(nil), body[:min(len(body), acpTerminalCap)]...), acpTerminalMarker...)
		if exit == nil {
			r.kill(ctx, id)
			exit = r.waitAfterKill(ctx, id)
		}
		return r.result(id, start, body, len(out), true, exit, ""), true, nil
	}
	if exit != nil {
		if canonical, werr := r.term.WaitForExit(ctx, id); werr == nil {
			exit = &canonical
		}
		return r.result(id, start, body, len(out), false, exit, ""), true, nil
	}
	if time.Now().After(deadline) {
		r.kill(ctx, id)
		return r.result(id, start, body, len(out), false, r.waitAfterKill(ctx, id), exec.StatusTimeout), true, nil
	}
	return exec.Result{}, false, nil
}

// result renders one exec.Result with the output already redacted.
func (r *acpTerminalRunner) result(id string, start time.Time, out []byte, raw int, truncated bool, exit *acp.TerminalExit, forced exec.Status) exec.Result {
	res := exec.Result{
		Status: exec.StatusExited, Runner: acpTerminalName, ExecID: id,
		Duration: time.Since(start), Output: []byte(r.redact(string(out))), Truncated: truncated, BytesRaw: int64(raw),
	}
	if forced != "" {
		res.Status = forced
	}
	if exit != nil {
		res.ExitCode, res.Signal = exit.ExitCode, exit.Signal
		if forced == "" && exit.Signal != "" {
			res.Status = exec.StatusSignaled
		}
	}
	return res
}

func (r *acpTerminalRunner) redact(s string) string {
	if r.red == nil {
		return s
	}
	return r.red.String(s)
}

// detached keeps cleanup working after the turn context is cancelled.
func detached(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), acpTerminalKillWait)
}

// release frees the client terminal even when the turn is over.
func (r *acpTerminalRunner) release(ctx context.Context, id string) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := r.term.Release(ctx, id); err != nil {
		return
	}
}

func (r *acpTerminalRunner) kill(ctx context.Context, id string) {
	ctx, cancel := detached(ctx)
	defer cancel()
	if err := r.term.Kill(ctx, id); err != nil {
		return
	}
}

// waitAfterKill asks the client for the exit state after a kill; a failed
// wait leaves Signal empty and is visible in the timeout status.
func (r *acpTerminalRunner) waitAfterKill(ctx context.Context, id string) *acp.TerminalExit {
	ctx, cancel := detached(ctx)
	defer cancel()
	exit, err := r.term.WaitForExit(ctx, id)
	if err != nil {
		return nil
	}
	return &exit
}

// env sends only variables that are safe to hand to the client: secret-named
// entries and values the redactor would change are dropped, never logged.
func (r *acpTerminalRunner) env(env []string) []acp.TerminalEnv {
	out := make([]acp.TerminalEnv, 0, len(env))
	for _, kv := range env {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || name == "" || acpTerminalSecretName.MatchString(name) {
			continue
		}
		if r.red != nil && r.red.String(value) != value {
			continue
		}
		out = append(out, acp.TerminalEnv{Name: name, Value: value})
	}
	return out
}
