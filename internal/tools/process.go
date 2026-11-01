//go:build !minimal

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
)

// ProcessRegistry is the consumer view of exec.Background (E-09, ≤5
// methods). The session argument is never model-controlled.
type ProcessRegistry interface {
	List(session string) []exec.ProcessInfo
	Poll(session, id string, since int64, tailLines int, grep string) (exec.PollResult, error)
	Write(session, id, text string, enter bool) error
	Kill(ctx context.Context, session, id, signal string) (exec.ProcessInfo, error)
	Wait(ctx context.Context, session, id string, timeout time.Duration) (exec.ProcessInfo, bool, error)
}

// ProcessConfig wires the five process_* tools.
type ProcessConfig struct {
	Registry ProcessRegistry
}

// processTool is one process_* tool; name selects schema and behavior.
type processTool struct {
	name string
	reg  ProcessRegistry
}

// NewProcessTools returns process_list/output/wait/send/kill in a stable
// order.
func NewProcessTools(cfg *ProcessConfig) []Tool {
	if cfg.Registry == nil {
		return nil
	}
	var out []Tool
	for _, name := range []string{"process_list", "process_output", "process_wait", "process_send", "process_kill"} {
		out = append(out, &processTool{name: name, reg: cfg.Registry})
	}
	return out
}

// processSchema returns the closed schema of one process tool.
func processSchema(name string) string {
	switch name {
	case "process_list":
		return `{"name":"process_list","description":"List background processes of this session (max 40).","parameters":{"type":"object","properties":{},"additionalProperties":false}}`
	case "process_output":
		return `{"name":"process_output","description":"Read buffered output of a background process.","parameters":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":32},"since":{"type":"integer","minimum":0},"tail_lines":{"type":"integer","minimum":1,"maximum":500},"grep":{"type":"string","maxLength":256}},"required":["id"],"additionalProperties":false}}`
	case "process_wait":
		return `{"name":"process_wait","description":"Wait for a background process; returns still_running on timeout.","parameters":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":32},"timeout_s":{"type":"integer","minimum":1,"maximum":600}},"required":["id"],"additionalProperties":false}}`
	case "process_send":
		return `{"name":"process_send","description":"Send text to a background process stdin.","parameters":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":32},"text":{"type":"string","maxLength":4096},"enter":{"type":"boolean"}},"required":["id","text"],"additionalProperties":false}}`
	default:
		return `{"name":"process_kill","description":"Signal a background process (TERM, INT or KILL).","parameters":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":32},"signal":{"type":"string","enum":["TERM","INT","KILL"]}},"required":["id"],"additionalProperties":false}}`
	}
}

func (t *processTool) Name() string            { return t.name }
func (t *processTool) Schema() json.RawMessage { return json.RawMessage(processSchema(t.name)) }
func (t *processTool) Kind() Kind              { return KindExecute }

// Effect: list/output/wait are reads; send/kill are non_idempotent.
func (t *processTool) Effect(json.RawMessage) Effect {
	switch t.name {
	case "process_list", "process_output", "process_wait":
		return EffectRead
	default:
		return EffectNonIdempotent
	}
}

type processArgs struct {
	ID        string `json:"id"`
	Since     int64  `json:"since"`
	TailLines int    `json:"tail_lines"`
	Grep      string `json:"grep"`
	TimeoutS  int    `json:"timeout_s"`
	Text      string `json:"text"`
	Enter     *bool  `json:"enter"`
	Signal    string `json:"signal"`
}

func decodeProcess(name string, raw json.RawMessage) (processArgs, error) {
	var a processArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, toolErr(CodeInvalidArgs, "arguments: "+err.Error(), "")
	}
	if name != "process_list" && (a.ID == "" || len(a.ID) > 32) {
		return a, toolErr(CodeInvalidArgs, "id is required", "/id: required")
	}
	if a.TailLines < 0 || a.TailLines > 500 || a.Since < 0 {
		return a, toolErr(CodeInvalidArgs, "tail_lines/since out of range", "")
	}
	if name == "process_wait" && (a.TimeoutS < 0 || a.TimeoutS > 600) {
		return a, toolErr(CodeInvalidArgs, "timeout_s must be 1..600", "")
	}
	if name == "process_send" && len(a.Text) > 4096 {
		return a, toolErr(CodeInvalidArgs, "text exceeds 4096", "")
	}
	if name == "process_kill" {
		switch a.Signal {
		case "", "TERM", "INT", "KILL":
		default:
			return a, toolErr(CodeInvalidArgs, "signal must be TERM, INT or KILL", "")
		}
	}
	return a, nil
}

// Inspect asks for send/kill; headless never auto-approves and never allows
// session grants. list/output/wait are reads.
func (t *processTool) Inspect(_ context.Context, _ Env, raw json.RawMessage, pc PolicyContext) (PolicyDecision, error) {
	a, err := decodeProcess(t.name, raw)
	if err != nil {
		return PolicyDecision{}, err
	}
	switch t.name {
	case "process_send", "process_kill":
		preview := t.name + " " + a.ID
		if t.name == "process_send" {
			preview += fmt.Sprintf(" (%d bytes, enter=%v)", len(a.Text), a.Enter == nil || *a.Enter)
		} else {
			preview += " signal=" + orDefault(a.Signal, "TERM")
		}
		if pc.Mode.Headless() {
			return PolicyDecision{Requirement: RequireDeny, Class: t.name, Preview: preview}, nil
		}
		return PolicyDecision{Requirement: RequireAsk, Class: t.name, Preview: preview, AllowSession: false}, nil
	default:
		return PolicyDecision{Requirement: RequireAuto, Class: "read"}, nil
	}
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// Run dispatches to the registry; the session id comes from Env.Session.
//
//nolint:gocyclo // one branch per tool is the readable dispatch
func (t *processTool) Run(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	a, err := decodeProcess(t.name, raw)
	if err != nil {
		return Result{}, err
	}
	session := env.Session.SessionID()
	if session == "" {
		return Result{}, toolErr(CodeModuleUnavailable, "process tools need a session", "")
	}
	switch t.name {
	case "process_list":
		infos := t.reg.List(session)
		b, err := json.Marshal(infos)
		if err != nil {
			return Result{}, toolErr(CodeInternal, "process_list encode: "+err.Error(), "")
		}
		return Result{OK: true, Content: string(b), Meta: map[string]any{"count": len(infos)}}, nil
	case "process_output":
		pr, err := t.reg.Poll(session, a.ID, a.Since, a.TailLines, a.Grep)
		if err != nil {
			return Result{}, processErr(err)
		}
		content := string(pr.Output)
		if content == "" {
			content = "(no output)"
		}
		return Result{OK: true, Content: content, Meta: map[string]any{"id": a.ID, "status": pr.Info.Status, "offset": pr.Offset, "lines": pr.Lines}}, nil
	case "process_wait":
		timeout := 60
		if a.TimeoutS > 0 {
			timeout = a.TimeoutS
		}
		info, still, err := t.reg.Wait(ctx, session, a.ID, time.Duration(timeout)*time.Second)
		if err != nil {
			return Result{}, processErr(err)
		}
		return processResult(&info, still), nil
	case "process_send":
		enter := a.Enter == nil || *a.Enter
		if err := t.reg.Write(session, a.ID, a.Text, enter); err != nil {
			return Result{}, processErr(err)
		}
		info, _, werr := t.reg.Wait(ctx, session, a.ID, 0)
		if werr != nil {
			return Result{}, processErr(werr)
		}
		return processResult(&info, false), nil
	case "process_kill":
		info, err := t.reg.Kill(ctx, session, a.ID, a.Signal)
		if err != nil {
			return Result{}, processErr(err)
		}
		return processResult(&info, info.Status == exec.ProcessRunning), nil
	}
	return Result{}, toolErr(CodeInternal, "unknown process tool", "")
}

// processResult renders one final/still-running status; a still_running
// wait is a normal result, never a hang.
func processResult(info *exec.ProcessInfo, still bool) Result {
	status := string(info.Status)
	if still {
		status = "still_running"
	}
	meta := map[string]any{"id": info.ID, "status": status, "pid": info.PID, "runtime_ms": info.RuntimeMS}
	if info.ExitCode != nil {
		meta["exit_code"] = *info.ExitCode
	}
	if info.Signal != "" {
		meta["signal"] = info.Signal
	}
	content := status
	if info.ExitCode != nil {
		content = fmt.Sprintf("%s exit=%d", status, *info.ExitCode)
	}
	return Result{OK: status != string(exec.ProcessRunning), Content: content, Meta: meta}
}

// processErr maps registry errors to the closed tool code set; a foreign
// session is indistinguishable from a missing process.
func processErr(err error) error {
	switch {
	case errors.Is(err, exec.ErrProcessNotFound):
		return toolErr(CodeNotFound, "process not found", "check process_list")
	case errors.Is(err, exec.ErrProcessLimit):
		return toolErr(CodeTooLarge, "process limit reached", "process_kill a finished process")
	case errors.Is(err, exec.ErrProcessClosed):
		return toolErr(CodeModuleUnavailable, "process registry closed", "")
	case errors.Is(err, exec.ErrSpec):
		return toolErr(CodeInvalidArgs, err.Error(), "")
	default:
		return toolErr(CodeExecFailed, err.Error(), "")
	}
}

var _ ProcessRegistry = (*exec.Background)(nil)
