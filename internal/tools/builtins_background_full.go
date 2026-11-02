//go:build !minimal

package tools

import (
	"context"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// backgroundLinked reports whether shell background is compiled in.
const backgroundLinked = true

// shellArgs is the F5 schema input (background supported).
type shellArgs struct {
	Command    string `json:"command"`
	TimeoutS   int    `json:"timeout_s"`
	Cwd        string `json:"cwd"`
	Session    string `json:"session"`
	Stdin      string `json:"stdin"`
	Background bool   `json:"background"`
}

func shellSchema() string {
	return `{"name":"shell","description":"Run one command in a process group with timeout. Asks for approval unless the exact argv matches shell_allow; background:true returns a process id.","parameters":{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":600},"cwd":{"type":"string"},"session":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"stdin":{"type":"string","maxLength":65536},"background":{"type":"boolean"}},"required":["command"],"additionalProperties":false}}`
}

func backgroundRequested(a *shellArgs) bool { return a.Background }

func validateBackground(a *shellArgs) error {
	if !a.Background {
		return nil
	}
	if a.Session != "" {
		return toolErr(CodeInvalidArgs, "background and session cannot be combined", "omit session")
	}
	if !exec.BackgroundSupported {
		return &ToolError{
			Code:    CodeModuleUnavailable,
			Message: "background shell needs a Job Object, unsupported on Windows in this slice",
			Hint:    "run in the foreground with timeout_s",
			Err:     exec.ErrCapability,
		}
	}
	return nil
}

func (t *shellTool) backgroundRun(ctx context.Context, env Env, a *shellArgs, cwd string, timeout time.Duration) (Result, bool, error) {
	if !a.Background {
		return Result{}, false, nil
	}
	res, err := t.runBackground(ctx, env, a, cwd, timeout)
	return res, true, err
}

// runBackground starts the approved command in the process registry and
// returns its id (EXEC-AND-CODING §A.5, ADR-072). Output is read through
// process_output; the process dies with the registry at shutdown.
func (t *shellTool) runBackground(ctx context.Context, env Env, a *shellArgs, cwd string, timeout time.Duration) (Result, error) {
	bg, ok := t.cfg.Background.(*exec.Background)
	if !ok || bg == nil {
		return Result{}, toolErr(CodeModuleUnavailable, "background processes are not available", "run in the foreground with timeout_s")
	}
	if a.Stdin != "" {
		return Result{}, toolErr(CodeInvalidArgs, "background stdin is sent with process_send", "omit stdin")
	}
	direct, derr := t.approvedDirect(env, a, cwd)
	if derr != nil {
		return Result{}, derr
	}
	spec, _, perr := exec.Prepare(&exec.OneShot{
		Command: a.Command, Direct: direct, Dir: cwd, Root: first(t.cfg.Roots),
		Timeout: timeout, Parent: t.cfg.ParentEnv, Policy: t.cfg.EnvPolicy,
	})
	if perr != nil {
		return Result{}, shellErr(perr)
	}
	session := env.Session.SessionID()
	if session == "" {
		return Result{}, toolErr(CodeModuleUnavailable, "background needs a session", "")
	}
	info, err := bg.Start(ctx, session, spec.Argv, spec.Dir, spec.Env)
	if err != nil {
		return Result{}, shellErr(err)
	}
	return Result{OK: true, Content: "started " + info.ID, Meta: map[string]any{
		"process_id": info.ID, "status": string(info.Status), "pid": info.PID,
	}}, nil
}
