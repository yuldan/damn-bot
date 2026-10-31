package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"github.com/damnbot/damnbot/internal/codetools"
	"github.com/damnbot/damnbot/internal/sandbox"
)

// fileTool adapts one internal/codetools engine call to the Tool contract.
// The engine owns the sandbox verdict and the paged output; this adapter
// only exposes subjects (path) so the pipeline can approve outside-root
// paths before Run.
type fileTool struct {
	name   string
	kind   Kind
	effect Effect
	write  bool
	raw    json.RawMessage
	engine *codetools.Engine
	sb     *sandbox.Sandbox
	rec    FileRecorder
}

// FileRecorder journals one commit of a write tool before the target is
// replaced (SPEC §4.3 step 8, §18.1); an error aborts the write untouched.
type FileRecorder func(ctx context.Context, env Env, tool string, c *codetools.Commit) error

// FileTools returns the five eager file tools in codetools schema order
// (read_file, write_file, edit_file, glob, grep). rec, when non-nil,
// journals every write_file/edit_file commit.
func FileTools(engine *codetools.Engine, sb *sandbox.Sandbox, rec FileRecorder) []Tool {
	if engine == nil || sb == nil {
		return nil
	}
	specs := []struct {
		name   string
		kind   Kind
		effect Effect
		write  bool
	}{
		{"read_file", KindRead, EffectRead, false},
		{"write_file", KindEdit, EffectIdempotent, true},
		{"edit_file", KindEdit, EffectIdempotent, true},
		{"glob", KindSearch, EffectRead, false},
		{"grep", KindSearch, EffectRead, false},
	}
	var out []Tool
	for _, spec := range specs {
		for _, raw := range codetools.Schemas() {
			var doc SchemaDoc
			if json.Unmarshal(raw, &doc) != nil || doc.Name != spec.name {
				continue
			}
			out = append(out, &fileTool{
				name: spec.name, kind: spec.kind, effect: spec.effect, write: spec.write,
				raw: raw, engine: engine, sb: sb, rec: rec,
			})
			break
		}
	}
	return out
}

func (t *fileTool) Name() string                  { return t.name }
func (t *fileTool) Schema() json.RawMessage       { return t.raw }
func (t *fileTool) Kind() Kind                    { return t.kind }
func (t *fileTool) Effect(json.RawMessage) Effect { return t.effect }

// Inspect checks the call's path against deny_paths and the sandbox roots.
// outside_root and hardlinked_file are approval verdicts, not errors
// (SPEC §4.3 step 5, ADR-069/ADR-079).
func (t *fileTool) Inspect(ctx context.Context, env Env, args json.RawMessage, pc PolicyContext) (PolicyDecision, error) {
	var a struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return PolicyDecision{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	path := a.Path
	if path == "" {
		path = "."
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(pc.Cwd, path)
	}
	op := sandbox.OpRead
	if t.write {
		op = sandbox.OpWrite
	}
	verdict, resolved, err := t.sb.Check(ctx, path, op)
	if err != nil {
		if errors.Is(err, sandbox.ErrDeniedPath) {
			return PolicyDecision{}, &ToolError{Code: CodeDeniedPath, Message: "path matches deny_paths: " + path, Hint: "choose another path", Err: err}
		}
		if errors.Is(err, sandbox.ErrOutsideWorkspace) {
			verdict = sandbox.OutsideRoot
		} else {
			return PolicyDecision{}, &ToolError{Code: CodeInvalidArgs, Message: err.Error(), Err: err}
		}
	}
	if verdict != sandbox.Inside {
		return PolicyDecision{
			Requirement:  RequireOutside,
			Paths:        []string{resolved},
			Preview:      previewArgs(t.name, args),
			Class:        t.class(),
			AllowSession: true,
		}, nil
	}
	dec := defaultPolicy(t.effect, pc)
	if dec.Requirement == RequireAsk {
		dec.Paths = []string{resolved}
		dec.Preview = previewArgs(t.name, args)
	}
	return dec, nil
}

func (t *fileTool) class() string {
	if t.write {
		return "write"
	}
	return "read"
}

// Run delegates to the engine; env.Approved carries the outside verdict and
// env's turn/call ids key the journal record of a write.
func (t *fileTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	o := codetools.Opts{Approved: env.Approved, PageChars: int(env.PageChars)}
	if t.write && t.rec != nil {
		o.Journal = func(ctx context.Context, c *codetools.Commit) error { return t.rec(ctx, env, t.name, c) }
	}
	r, err := t.engine.Run(ctx, t.name, args, o)
	if err != nil {
		return Result{}, fmt.Errorf("tools: %s: %w", t.name, err)
	}
	return Result{OK: true, Content: r.Content, Meta: r.Meta}, nil
}
