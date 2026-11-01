package tools

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/exec"
)

// RepoMapper renders the deterministic repository map of one root. The
// implementation lives in the codeintel module (wired by the composition
// root), so the core tools package does not import the lazy module.
type RepoMapper interface {
	RepoMap(ctx context.Context, root, focus string, maxTokens int) (string, error)
}

// CodeIntelConfig wires repo_map and code_intel.
type CodeIntelConfig struct {
	Mapper  RepoMapper
	Roots   []string
	Cwd     string
	Trusted bool
}

// NewCodeIntelTools returns the read-only code intelligence tools.
func NewCodeIntelTools(cfg *CodeIntelConfig) []Tool {
	if cfg.Mapper == nil {
		return nil
	}
	return []Tool{&repoMapTool{cfg: *cfg}, &codeIntelTool{cfg: *cfg}}
}

const codeIntelLanguages = "go, c, cpp"

const schemaRepoMap = `{"name":"repo_map","description":"Deterministic repository map. Indexes Go (go/ast) and C/C++ (tree-sitter WASM); JavaScript/TypeScript/Python are unsupported and reported as an error.","parameters":{"type":"object","properties":{"focus":{"type":"string","maxLength":200},"path":{"type":"string"},"max_tokens":{"type":"integer","minimum":200,"maximum":1000}},"additionalProperties":false}}`

const schemaCodeIntel = `{"name":"code_intel","description":"Read-only code intelligence. Indexes Go (go/ast) and C/C++ (tree-sitter WASM); other languages are unsupported. action=languages lists the supported set.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["repo_map","languages"]},"focus":{"type":"string","maxLength":200},"path":{"type":"string"},"max_tokens":{"type":"integer","minimum":200,"maximum":1000}},"required":["action"],"additionalProperties":false}}`

type repoMapTool struct{ cfg CodeIntelConfig }

func (t *repoMapTool) Name() string                  { return "repo_map" }
func (t *repoMapTool) Schema() json.RawMessage       { return json.RawMessage(schemaRepoMap) }
func (t *repoMapTool) Kind() Kind                    { return KindRead }
func (t *repoMapTool) Effect(json.RawMessage) Effect { return EffectRead }
func (t *repoMapTool) Inspect(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
	return PolicyDecision{Requirement: RequireAuto, Class: "read"}, nil
}

func (t *repoMapTool) Run(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	a, err := decodeRepoMap(raw)
	if err != nil {
		return Result{}, err
	}
	return t.run(ctx, env, a)
}

func (t *repoMapTool) run(ctx context.Context, env Env, a repoMapArgs) (Result, error) {
	root, err := t.root(env, a.Path)
	if err != nil {
		return Result{}, err
	}
	out, err := t.cfg.Mapper.RepoMap(ctx, root, a.Focus, a.MaxTokens)
	if err != nil {
		return Result{}, codeIntelErr(err)
	}
	res := Result{OK: true, Content: out, Meta: map[string]any{"languages": codeIntelLanguages}}
	if !t.cfg.Trusted {
		res.UntrustedSource = "workspace"
	}
	return res, nil
}

// root resolves the requested path and rejects anything outside the roots.
func (t *repoMapTool) root(env Env, path string) (string, error) {
	root := path
	if root == "" {
		root = env.Cwd
	}
	if root == "" {
		root = t.cfg.Cwd
	}
	abs, err := filepath.Abs(root)
	if err != nil {
		return "", toolErr(CodeInvalidArgs, "path: "+err.Error(), "")
	}
	if !cwdInRoots(abs, t.cfg.Roots) {
		return "", toolErr(CodeOutsideWorkspace, "path is outside the workspace: "+abs, "choose a path inside the workspace")
	}
	return abs, nil
}

type repoMapArgs struct {
	Focus     string `json:"focus"`
	Path      string `json:"path"`
	MaxTokens int    `json:"max_tokens"`
}

func decodeRepoMap(raw json.RawMessage) (repoMapArgs, error) {
	var a repoMapArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return a, toolErr(CodeInvalidArgs, "arguments: "+err.Error(), "")
	}
	if len(a.Focus) > 200 {
		return a, toolErr(CodeInvalidArgs, "focus exceeds 200 chars", "")
	}
	if a.MaxTokens == 0 {
		a.MaxTokens = 600
	}
	if a.MaxTokens < 200 || a.MaxTokens > 1000 {
		return a, toolErr(CodeInvalidArgs, "max_tokens must be 200..1000", "")
	}
	return a, nil
}

type codeIntelTool struct{ cfg CodeIntelConfig }

func (t *codeIntelTool) Name() string                  { return "code_intel" }
func (t *codeIntelTool) Schema() json.RawMessage       { return json.RawMessage(schemaCodeIntel) }
func (t *codeIntelTool) Kind() Kind                    { return KindRead }
func (t *codeIntelTool) Effect(json.RawMessage) Effect { return EffectRead }
func (t *codeIntelTool) Inspect(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
	return PolicyDecision{Requirement: RequireAuto, Class: "read"}, nil
}

func (t *codeIntelTool) Run(ctx context.Context, env Env, raw json.RawMessage) (Result, error) {
	var a struct {
		repoMapArgs
		Action string `json:"action"`
	}
	if err := json.Unmarshal(raw, &a); err != nil {
		return Result{}, toolErr(CodeInvalidArgs, "arguments: "+err.Error(), "")
	}
	switch a.Action {
	case "languages":
		return Result{OK: true, Content: codeIntelLanguages, Meta: map[string]any{"languages": codeIntelLanguages}}, nil
	case "repo_map":
		rm := &repoMapTool{cfg: t.cfg}
		return rm.run(ctx, env, a.repoMapArgs)
	default:
		return Result{}, toolErr(CodeInvalidArgs, "action must be repo_map or languages", "")
	}
}

func codeIntelErr(err error) error {
	if err == nil {
		return nil
	}
	if errorsIs(err, exec.ErrSpec) {
		return toolErr(CodeInvalidArgs, err.Error(), "")
	}
	return toolErr(CodeExecFailed, "supported languages: "+codeIntelLanguages, "%s", err.Error())
}

func errorsIs(err, target error) bool { return errors.Is(err, target) }
