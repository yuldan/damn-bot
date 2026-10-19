package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/damnbot/damnbot/internal/exec"
	"github.com/damnbot/damnbot/internal/exec/classify"
	"github.com/damnbot/damnbot/internal/redact"
)

// schemaShell is the F1 shell schema (SPEC §5.1; background arrives with
// F5, so it is not a property here and is rejected by additionalProperties).
const schemaShell = `{"name":"shell","description":"Run one command in a process group with timeout. Asks for approval unless the exact argv matches shell_allow; no background in F1.","parameters":{"type":"object","properties":{"command":{"type":"string","minLength":1,"maxLength":65536},"timeout_s":{"type":"integer","minimum":1,"maximum":600},"cwd":{"type":"string"},"session":{"type":"string","pattern":"^[a-z0-9_-]{1,32}$"},"stdin":{"type":"string","maxLength":65536}},"required":["command"],"additionalProperties":false}}`

// ShellRunner runs one process tree (implemented by *exec.Runner).
type ShellRunner interface {
	Run(ctx context.Context, spec *exec.Spec) (exec.Result, error)
}

// ShellConfig wires the shell tool. Resolve is supplied by the composition
// root (exec-backed binary identity); without it shell_allow never matches.
type ShellConfig struct {
	Runner           ShellRunner
	Sessions         *exec.Sessions
	Spill            Spill
	Redactor         *redact.Redactor
	Allow            []classify.AllowEntry
	SessionStart     map[string]classify.BinaryID
	Resolve          func(argv0 string) (classify.BinaryID, bool)
	Roots            []string
	Home             string
	Cwd              string
	DenyPaths        []string
	ParentEnv        []string
	EnvPolicy        *exec.EnvPolicy
	WorkspaceTrusted bool
	UnsafeAutoShell  bool
	Dialect          classify.Dialect
}

// shellTool implements one `shell` call (SPEC §5.1, EXEC-AND-CODING §A).
type shellTool struct {
	cfg ShellConfig
	red *redact.Redactor
}

// NewShell returns the shell tool.
func NewShell(cfg ShellConfig) Tool {
	red := cfg.Redactor
	if red == nil {
		red = redact.New(redact.Options{})
	}
	return &shellTool{cfg: cfg, red: red}
}

func (t *shellTool) Name() string            { return "shell" }
func (t *shellTool) Schema() json.RawMessage { return json.RawMessage(schemaShell) }
func (t *shellTool) Kind() Kind              { return KindExecute }

// Effect resolves the classifier's risk label per call; classification
// never grants auto approval (ADR-079).
func (t *shellTool) Effect(args json.RawMessage) Effect {
	var a shellArgs
	if json.Unmarshal(args, &a) != nil {
		return EffectNonIdempotent
	}
	v := t.classify(a)
	if v.Class == classify.Mutating && v.Flags == 0 {
		return EffectIdempotent
	}
	return EffectNonIdempotent
}

type shellArgs struct {
	Command  string `json:"command"`
	TimeoutS int    `json:"timeout_s"`
	Cwd      string `json:"cwd"`
	Session  string `json:"session"`
	Stdin    string `json:"stdin"`
}

// Inspect applies classify.Decide: the only auto path is an exact
// shell_allow match with every condition; otherwise the mode decides
// (SPEC §14.1, ADR-079, ADR-088).
func (t *shellTool) Inspect(_ context.Context, _ Env, args json.RawMessage, pc PolicyContext) (PolicyDecision, error) {
	a, err := decodeShell(args)
	if err != nil {
		return PolicyDecision{}, err
	}
	if a.Session != "" && a.Stdin != "" {
		return PolicyDecision{}, toolErr(CodeInvalidArgs, "persistent sessions never accept stdin", "/stdin: not allowed with /session")
	}
	if a.Session != "" && a.Cwd != "" {
		return PolicyDecision{}, toolErr(CodeInvalidArgs, "cwd is fixed when a persistent session starts", "/cwd: not allowed with /session")
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd = t.cfg.Cwd
	}
	v := t.classifyIn(a.Command, cwd)
	matched := t.allowMatched(a.Command, cwd, a.Session != "")
	cctx := &classify.Context{
		Mode: t.classifyMode(pc.Mode), AllowMatched: matched,
		WorkspaceTrusted: pc.WorkspaceTrusted, TurnTainted: pc.Tainted,
		Session: a.Session != "", Stdin: a.Stdin != "", CwdInRoot: cwdInRoots(cwd, t.cfg.Roots),
	}
	dec := classify.Decide(&v, cctx)
	class := "shell:" + v.Class.String()
	if names := v.Flags.Names(); len(names) > 0 {
		class += " flags:" + strings.Join(names, ",")
	}
	preview := shellPreview(a.Command, v)
	switch dec {
	case classify.AutoApprove:
		return PolicyDecision{Requirement: RequireAuto, Class: class}, nil
	case classify.DeniedPathError:
		return PolicyDecision{}, &ToolError{Code: CodeDeniedPath, Message: "command resolves to a deny_paths file", Hint: "choose another command", Err: nil}
	case classify.OutsideWorkspace:
		return PolicyDecision{}, &ToolError{Code: CodeOutsideWorkspace, Message: "command reads outside the workspace", Hint: "headless runs never approve outside paths"}
	case classify.Denied:
		return PolicyDecision{Requirement: RequireDeny, Class: class, Preview: preview}, nil
	case classify.Unverified:
		return PolicyDecision{Requirement: RequireDeny, Class: class, Preview: preview}, nil
	default: // Ask, AskPreview, AskPerCommand
		return PolicyDecision{Requirement: RequireAsk, Class: class, Preview: preview, AllowSession: false}, nil
	}
}

// Run executes the approved command and renders the §A.3 summary.
func (t *shellTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	a, err := decodeShell(args)
	if err != nil {
		return Result{}, err
	}
	cwd := a.Cwd
	if cwd == "" {
		cwd = t.cfg.Cwd
	}
	timeout := time.Duration(a.TimeoutS) * time.Second
	if timeout == 0 {
		timeout = exec.DefaultTimeout
	}
	v := t.classifyIn(a.Command, cwd)
	var res exec.Result
	if a.Session != "" {
		if t.cfg.Sessions == nil {
			return Result{}, toolErr(CodeModuleUnavailable, "persistent sessions are not configured", "")
		}
		res, err = t.cfg.Sessions.Exec(ctx, a.Session, a.Command, nil, timeout)
	} else {
		if t.cfg.Runner == nil {
			return Result{}, toolErr(CodeModuleUnavailable, "shell runner is not configured", "")
		}
		var direct []string
		if t.allowMatched(a.Command, cwd, false) {
			if argv, ok := classify.DirectArgv(a.Command, t.cfg.Dialect); ok {
				direct = argv
			}
		}
		spec, notice, perr := exec.Prepare(&exec.OneShot{
			Command: a.Command, Direct: direct, Dir: cwd, Root: first(t.cfg.Roots),
			Stdin: []byte(a.Stdin), PTY: exec.NeedsTTY(v.Argv),
			Timeout: timeout, Parent: t.cfg.ParentEnv, Policy: t.cfg.EnvPolicy,
		})
		if perr != nil {
			return Result{}, shellErr(perr)
		}
		res, err = t.cfg.Runner.Run(ctx, &spec)
		if notice != "" {
			if res.Status == exec.StatusExited && res.ExitCode != nil && *res.ExitCode == 0 {
				res.Output = append(res.Output, []byte("\n[notice: "+notice+"]")...)
			}
		}
	}
	if err != nil && res.Status == exec.StatusSpawnFailed {
		return Result{}, shellErr(err)
	}
	return t.render(ctx, env, &res, err, v)
}

// render summarizes output, spills oversized text and fills Meta.
func (t *shellTool) render(ctx context.Context, env Env, res *exec.Result, runErr error, v classify.Verdict) (Result, error) {
	summarizer := exec.NewSummarizer(t.red)
	var logRef, rawRef BlobRef
	spill := func(log, raw []byte) (string, error) {
		if t.cfg.Spill == nil {
			return "(spill unavailable)", nil
		}
		var err error
		if logRef, err = t.cfg.Spill.Put(ctx, log); err != nil {
			return "", err
		}
		if rawRef, err = t.cfg.Spill.Put(ctx, raw); err != nil {
			return "", err
		}
		return logRef.Path, nil
	}
	inlineLeft := env.InlineLeft
	if inlineLeft <= 0 {
		inlineLeft = TurnInlineChars
	}
	sum, err := summarizer.Summarize(res, inlineLeft, spill)
	if err != nil {
		return Result{}, &ToolError{Code: CodeInternal, Message: "shell summary: " + err.Error(), Err: err}
	}
	content := sum.Header
	if sum.Text != "" {
		content += "\n" + sum.Text
	}
	meta := map[string]any{
		"status": string(res.Status), "runner": res.Runner,
		"duration_ms": res.Duration.Milliseconds(), "bytes_raw": res.BytesRaw,
		"truncated": res.Truncated,
	}
	if res.ExitCode != nil {
		meta["exit_code"] = *res.ExitCode
	}
	if res.Signal != "" {
		meta["signal"] = res.Signal
	}
	if res.Prompt != "" {
		meta["prompt"] = res.Prompt
		meta["input_kind"] = res.InputKind
	}
	if sum.Spilled {
		meta["log_blob_sha"] = logRef.SHA
		meta["raw_blob_sha"] = rawRef.SHA
	}
	if runErr != nil {
		meta["cleanup_error"] = true
	}
	out := Result{OK: true, Content: content, Meta: meta}
	out.UntrustedSource = classify.OutputSource(&v, t.cfg.WorkspaceTrusted)
	return out, nil
}

func (t *shellTool) classify(a shellArgs) classify.Verdict {
	return t.classifyIn(a.Command, t.cfg.Cwd)
}

func (t *shellTool) classifyIn(command, cwd string) classify.Verdict {
	ce := &classify.Env{
		Roots: t.cfg.Roots, Cwd: cwd, Home: t.cfg.Home,
		DenyPaths: t.cfg.DenyPaths, Dialect: t.cfg.Dialect,
	}
	if t.cfg.EnvPolicy != nil {
		for _, s := range t.cfg.EnvPolicy.Secrets {
			ce.SecretVars = append(ce.SecretVars, s.Name)
		}
	}
	return classify.Classify(command, ce)
}

func (t *shellTool) allowMatched(command, cwd string, session bool) bool {
	if session || len(t.cfg.Allow) == 0 || t.cfg.Resolve == nil {
		return false
	}
	return classify.MatchAllow(&classify.AllowRequest{
		Command: command, Dialect: t.cfg.Dialect, Cwd: cwd, Roots: t.cfg.Roots,
		Entries: t.cfg.Allow, Resolve: t.cfg.Resolve, SessionStart: t.cfg.SessionStart,
	})
}

func (t *shellTool) classifyMode(m Mode) classify.Mode {
	switch m {
	case ModeAutoEdit:
		return classify.ModeAutoEdit
	case ModeAuto:
		return classify.ModeAuto
	case ModeHeadless, ModeHeadlessAuto:
		if m == ModeHeadlessAuto && t.cfg.UnsafeAutoShell {
			return classify.ModeUnsafeAutoShell
		}
		return classify.ModeHeadless
	case ModeAsk:
		return classify.ModeAsk
	}
	return classify.ModeAsk
}

func decodeShell(args json.RawMessage) (shellArgs, error) {
	var a shellArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return a, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	if a.Command == "" {
		return a, toolErr(CodeInvalidArgs, `shell requires {"command": string}`, "/command: required")
	}
	if utf8.RuneCountInString(a.Command) > 65536 {
		return a, toolErr(CodeInvalidArgs, "commands are limited to 65536 characters", "/command: too long")
	}
	if utf8.RuneCountInString(a.Stdin) > 65536 {
		return a, toolErr(CodeInvalidArgs, "stdin is limited to 65536 characters", "/stdin: too long")
	}
	return a, nil
}

func shellErr(err error) error {
	switch {
	case errors.Is(err, exec.ErrNotFound):
		return &ToolError{Code: CodeExecFailed, Message: "command not found", Hint: "check the command name and PATH", Err: err}
	case errors.Is(err, exec.ErrSpec):
		return &ToolError{Code: CodeInvalidArgs, Message: err.Error(), Err: err}
	default:
		return &ToolError{Code: CodeExecFailed, Message: "spawn failed: " + err.Error(), Err: err}
	}
}

func shellPreview(command string, v classify.Verdict) string {
	var b strings.Builder
	b.WriteString("command: ")
	b.WriteString(command)
	b.WriteString("\nclass: ")
	b.WriteString(v.Class.String())
	if names := v.Flags.Names(); len(names) > 0 {
		b.WriteString("\nflags: ")
		b.WriteString(strings.Join(names, ","))
	}
	for i, r := range v.Reasons {
		if i == 3 {
			break
		}
		b.WriteString("\nreason: ")
		b.WriteString(r)
	}
	return b.String()
}

func cwdInRoots(cwd string, roots []string) bool {
	if cwd == "" {
		return true
	}
	abs, err := filepath.Abs(cwd)
	if err != nil {
		return false
	}
	for _, r := range roots {
		rel, err := filepath.Rel(r, abs)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return true
		}
	}
	return false
}

func first(s []string) string {
	if len(s) > 0 {
		return s[0]
	}
	return ""
}
