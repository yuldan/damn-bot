//go:build !minimal

package tools

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/code"
)

// schemaExecuteCode is the execute_code schema (SPEC §9.2, ADR-089).
const schemaExecuteCode = `{"name":"execute_code","description":"Run a short Python, JavaScript or Go program in a temporary directory. Network access is blocked. Approval follows exec.code_auto_languages.","parameters":{"type":"object","properties":{"language":{"type":"string","enum":["python","javascript","typescript","go"]},"code":{"type":"string","minLength":1,"maxLength":65536},"stdin":{"type":"string","maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":30},"network":{"type":"string","enum":["deny"]}},"required":["language","code"],"additionalProperties":false}}`

// CodeRunner runs one execute_code request (implemented by *code.Runner).
type CodeRunner interface {
	Run(ctx context.Context, req *code.Request) (code.Result, error)
}

// ExecuteCodeConfig wires the tool; AutoLanguages is exec.code_auto_languages.
type ExecuteCodeConfig struct {
	Runner        CodeRunner
	AutoLanguages []string
}

// executeCodeTool implements execute_code (ADR-089).
type executeCodeTool struct{ cfg ExecuteCodeConfig }

// NewExecuteCode returns the tool.
func NewExecuteCode(cfg *ExecuteCodeConfig) Tool { return &executeCodeTool{cfg: *cfg} }

func (t *executeCodeTool) Name() string            { return "execute_code" }
func (t *executeCodeTool) Schema() json.RawMessage { return json.RawMessage(schemaExecuteCode) }
func (t *executeCodeTool) Kind() Kind              { return KindExecute }

// Effect is always non_idempotent: code may have external effects.
func (t *executeCodeTool) Effect(json.RawMessage) Effect { return EffectNonIdempotent }

type executeCodeArgs struct {
	Language string `json:"language"`
	Code     string `json:"code"`
	Stdin    string `json:"stdin"`
	TimeoutS int    `json:"timeout_s"`
	Network  string `json:"network"`
}

func decodeExecuteCode(args json.RawMessage) (executeCodeArgs, error) {
	var a executeCodeArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, toolErr(CodeInvalidArgs, "arguments: "+err.Error(), "")
	}
	if a.Language == "" || a.Code == "" {
		return a, toolErr(CodeInvalidArgs, "execute_code requires language and code", "/language: required")
	}
	switch code.Language(a.Language) {
	case code.Python, code.JavaScript, code.Go:
	default:
		if code.ShellLanguage(code.Language(a.Language)) {
			return a, toolErr(CodeInvalidArgs, "shell languages use the shell tool", "use shell with shell_allow")
		}
		return a, toolErr(CodeInvalidArgs, "unsupported language: "+a.Language, "")
	}
	if a.Network != "" && a.Network != "deny" {
		return a, toolErr(CodeInvalidArgs, "network must be deny", "")
	}
	return a, nil
}

// Inspect applies ADR-089 exactly; shell languages never reach it.
func (t *executeCodeTool) Inspect(_ context.Context, _ Env, args json.RawMessage, pc PolicyContext) (PolicyDecision, error) {
	a, err := decodeExecuteCode(args)
	if err != nil {
		return PolicyDecision{}, err
	}
	mode, headless := approvalMode(pc.Mode)
	in := code.PolicyInput{
		Mode: mode, AutoLanguages: t.cfg.AutoLanguages,
		WorkspaceTrusted: pc.WorkspaceTrusted, Tainted: pc.Tainted,
	}
	var d code.Decision
	if headless {
		d, err = code.DecideHeadless(code.Language(a.Language), in)
	} else {
		d, err = code.Decide(code.Language(a.Language), in)
	}
	if err != nil {
		return PolicyDecision{}, toolErr(CodeInvalidArgs, err.Error(), "")
	}
	dec := PolicyDecision{Class: "execute_code", Preview: code.Preview(&code.Request{Language: code.Language(a.Language), Code: a.Code})}
	switch d.Requirement {
	case code.RequireAuto:
		dec.Requirement = RequireAuto
	case code.RequireAsk:
		dec.Requirement = RequireAsk
	case code.RequireDeny:
		dec.Requirement = RequireDeny
	default:
		dec.Requirement = RequireDeny
	}
	return dec, nil
}

// approvalMode maps tools.Mode to the ADR-089 policy mode.
func approvalMode(m Mode) (code.Mode, bool) {
	switch m {
	case ModeAsk:
		return code.ModeAsk, false
	case ModeAutoEdit:
		return code.ModeAutoEdit, false
	case ModeAuto:
		return code.ModeAuto, false
	case ModeHeadless:
		return code.ModeAsk, true
	case ModeHeadlessAuto:
		return code.ModeAuto, true
	default:
		return code.ModeAsk, true
	}
}

// Run executes the approved call and renders the combined output.
func (t *executeCodeTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	a, err := decodeExecuteCode(args)
	if err != nil {
		return Result{}, err
	}
	if t.cfg.Runner == nil {
		return Result{}, toolErr(CodeModuleUnavailable, "execute_code runner is not configured", "")
	}
	req := &code.Request{Language: code.Language(a.Language), Code: a.Code, Network: "deny"}
	if a.Stdin != "" {
		req.Stdin = []byte(a.Stdin)
	}
	req.Timeout = 30 * time.Second
	if a.TimeoutS > 0 {
		req.Timeout = time.Duration(a.TimeoutS) * time.Second
	}
	res, err := t.cfg.Runner.Run(ctx, req)
	if err != nil {
		if errors.Is(err, code.ErrNetworkDeny) || errors.Is(err, code.ErrBackend) {
			return Result{}, toolErr(CodeExecFailed, err.Error(), "network deny requires an isolating backend")
		}
		return Result{}, toolErr(CodeExecFailed, err.Error(), "")
	}
	out := strings.TrimSpace(string(res.Stdout))
	if out == "" {
		out = "(no output)"
	}
	if res.Execution.Sandboxed {
		out = "[sandbox: " + res.Execution.Backend + " - host execution replaced]\n" + out
	}
	meta := map[string]any{"language": a.Language, "status": string(res.Status), "backend": res.Backend}
	if res.ExitCode != nil {
		meta["exit_code"] = *res.ExitCode
	}
	meta["isolation"] = map[string]any{"network": res.Isolation.Network, "memory": res.Isolation.Memory}
	meta["execution"] = res.Execution
	return Result{OK: res.Status == exec.StatusExited && res.ExitCode != nil && *res.ExitCode == 0, Content: out, Meta: meta}, nil
}
