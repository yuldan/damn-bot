package delegate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/tools"
)

// schemaDelegateTask is the model-facing document of delegate_task
// (TOOL-026, SPEC §9.3).
const schemaDelegateTask = `{"name":"delegate_task","description":"Run up to 10 subagents in parallel with their own prompt, role, iteration limit, budget and optional git worktree.","parameters":{"type":"object","properties":{"tasks":{"type":"array","minItems":1,"maxItems":10,"items":{"type":"object","properties":{"goal":{"type":"string","minLength":1,"maxLength":4000},"context":{"type":"string","maxLength":8000},"role":{"type":"string","maxLength":64},"files":{"type":"array","maxItems":64,"items":{"type":"string","minLength":1,"maxLength":500}},"isolation":{"type":"string","enum":["none","worktree"]},"max_iterations":{"type":"integer","minimum":1,"maximum":40},"budget_usd":{"type":"number","minimum":0}},"required":["goal"],"additionalProperties":false}},"max_parallel":{"type":"integer","minimum":1,"maximum":16}},"required":["tasks"],"additionalProperties":false}}`

// Tool is the delegate_task tool (TOOL-026). It is eager in the delegation
// toolset and always asks for approval, because spawning a subagent is a
// non-read action that spends the parent's budget.
type Tool struct{ svc *Service }

// NewTool returns delegate_task over svc. A nil Service fails closed: the
// composition root must wire the agent, ledger and budget seams first.
func NewTool(svc *Service) (tools.Tool, error) {
	if svc == nil {
		return nil, errors.New("delegate: service required")
	}
	return &Tool{svc: svc}, nil
}

// Name implements tools.Tool.
func (*Tool) Name() string { return "delegate_task" }

// Schema implements tools.Tool.
func (*Tool) Schema() json.RawMessage { return json.RawMessage(schemaDelegateTask) }

// Kind implements tools.Tool.
func (*Tool) Kind() tools.Kind { return tools.KindExecute }

// Effect implements tools.Tool: a subagent can write and spend, so the call
// is never retried after an unknown outcome.
func (*Tool) Effect(json.RawMessage) tools.Effect { return tools.EffectNonIdempotent }

type toolArgs struct {
	Tasks []struct {
		Goal          string   `json:"goal"`
		Context       string   `json:"context"`
		Role          string   `json:"role"`
		Files         []string `json:"files"`
		Isolation     string   `json:"isolation"`
		MaxIterations int      `json:"max_iterations"`
		BudgetUSD     float64  `json:"budget_usd"`
	} `json:"tasks"`
	MaxParallel int `json:"max_parallel"`
}

func parseArgs(args json.RawMessage) ([]Task, int, error) {
	var a toolArgs
	dec := json.NewDecoder(strings.NewReader(string(args)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&a); err != nil {
		return nil, 0, fmt.Errorf("arguments: %w", err)
	}
	out := make([]Task, len(a.Tasks))
	for i, t := range a.Tasks {
		out[i] = Task{
			Goal: t.Goal, Context: t.Context, Role: t.Role, Files: t.Files,
			Isolation: t.Isolation, MaxIterations: t.MaxIterations, BudgetUSD: t.BudgetUSD,
		}
	}
	return out, a.MaxParallel, nil
}

// Inspect implements tools.Inspector: every delegation asks, even in auto
// mode, with a preview of the prompts and the budget allocation. Headless
// runs have no approver and therefore deny (SPEC §18.2).
func (t *Tool) Inspect(_ context.Context, _ tools.Env, raw json.RawMessage, _ tools.PolicyContext) (tools.PolicyDecision, error) {
	tasks, parallel, err := parseArgs(raw)
	if err != nil {
		return tools.PolicyDecision{}, err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "delegate %d subagent(s), at most %d in parallel\n", len(tasks), parallel)
	var files []string
	seen := make(map[string]bool)
	for i, task := range tasks {
		role := task.Role
		if role == "" {
			role = "default"
		}
		budget := "share of the parent remainder"
		if task.BudgetUSD > 0 {
			budget = fmt.Sprintf("%.2f USD", task.BudgetUSD)
		}
		fmt.Fprintf(&b, "%d. [%s] %s (budget %s", i+1, role, clipLine(task.Goal, 200), budget)
		if task.Isolation == "worktree" {
			b.WriteString(", worktree")
		}
		if len(task.Files) > 0 {
			fmt.Fprintf(&b, ", %d leased file(s)", len(task.Files))
		}
		b.WriteString(")\n")
		for _, f := range task.Files {
			if !seen[f] && len(files) < 64 {
				seen[f] = true
				files = append(files, f)
			}
		}
	}
	return tools.PolicyDecision{
		Requirement: tools.RequireAsk, Class: "delegate", Preview: b.String(),
		Paths: files, AllowSession: false,
	}, nil
}

// Run implements tools.Tool.
func (t *Tool) Run(ctx context.Context, env tools.Env, raw json.RawMessage) (tools.Result, error) {
	tasks, parallel, err := parseArgs(raw)
	if err != nil {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeInvalidArgs, Message: err.Error(), Err: err}
	}
	batch, err := t.svc.Run(ctx, env, tasks, parallel)
	if err != nil {
		return tools.Result{}, toolError(err)
	}
	content, merr := json.Marshal(batch)
	if merr != nil {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeInternal, Message: "delegate: result encoding failed", Err: merr}
	}
	return tools.Result{OK: true, Content: string(content), Meta: map[string]any{
		"tasks": len(batch.Reports), "allocated_usd": batch.Allocated, "spent_usd": batch.Spent,
	}}, nil
}

// toolError maps a Service error onto the closed tool-code set.
func toolError(err error) *tools.ToolError {
	switch {
	case errors.Is(err, ErrSession):
		return &tools.ToolError{
			Code: tools.CodeInternal, Message: err.Error(),
			Hint: "the composition root must pass an explicit session view with the delegate capability", Err: err,
		}
	case errors.Is(err, ErrDepth):
		return &tools.ToolError{
			Code: tools.CodeDenied, Message: err.Error(),
			Hint: "finish the task in this session instead of nesting deeper", Err: err,
		}
	case errors.Is(err, ErrRateLimited):
		return &tools.ToolError{
			Code: tools.CodeDenied, Message: err.Error(),
			Hint: "delegation is expensive; wait for the retry window", Err: err,
		}
	case errors.Is(err, ErrInvalidConfig):
		return &tools.ToolError{Code: tools.CodeInvalidArgs, Message: err.Error(), Err: err}
	}
	return &tools.ToolError{Code: tools.CodeInternal, Message: "delegate: " + err.Error(), Err: err}
}

func clipLine(s string, n int) string { return clip(strings.ReplaceAll(s, "\n", " "), n) }
