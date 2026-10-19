package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// schemaTodo is the eager todo schema (SPEC §5.1). Effect is idempotent:
// each call replaces the whole list.
const schemaTodo = `{"name":"todo","description":"Replace the task list. Use for plans with several steps; status pending|in_progress|done.","parameters":{"type":"object","properties":{"items":{"type":"array","maxItems":30,"items":{"type":"object","properties":{"id":{"type":"string","minLength":1,"maxLength":64},"text":{"type":"string","minLength":1,"maxLength":200},"status":{"type":"string","enum":["pending","in_progress","done"]}},"required":["id","text","status"],"additionalProperties":false}}},"required":["items"],"additionalProperties":false}}`

// todoTool keeps the session's plan list (volatile tier reads the same
// state through TodoItems).
type todoTool struct {
	mu    sync.Mutex
	items []TodoItem
}

// TodoItem is one plan row.
type TodoItem struct {
	ID     string `json:"id"`
	Text   string `json:"text"`
	Status string `json:"status"`
}

// NewTodo returns the todo tool.
func NewTodo() Tool { return &todoTool{} }

// Items returns a copy of the current list.
func (t *todoTool) Items() []TodoItem {
	t.mu.Lock()
	defer t.mu.Unlock()
	return append([]TodoItem(nil), t.items...)
}

func (t *todoTool) Name() string                  { return "todo" }
func (t *todoTool) Schema() json.RawMessage       { return json.RawMessage(schemaTodo) }
func (t *todoTool) Kind() Kind                    { return KindThink }
func (t *todoTool) Effect(json.RawMessage) Effect { return EffectIdempotent }

// Run replaces the list and renders it deterministically.
func (t *todoTool) Run(_ context.Context, _ Env, args json.RawMessage) (Result, error) {
	var a struct {
		Items []TodoItem `json:"items"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	seen := make(map[string]bool, len(a.Items))
	counts := map[string]int{"pending": 0, "in_progress": 0, "done": 0}
	for i, it := range a.Items {
		if seen[it.ID] {
			return Result{}, toolErr(CodeInvalidArgs, "ids must be unique", "/items/%d/id: duplicate %q", i, it.ID)
		}
		seen[it.ID] = true
		counts[it.Status]++
	}
	t.mu.Lock()
	t.items = append([]TodoItem(nil), a.Items...)
	t.mu.Unlock()
	var b strings.Builder
	fmt.Fprintf(&b, "%d items: %d pending, %d in progress, %d done", len(a.Items), counts["pending"], counts["in_progress"], counts["done"])
	for _, it := range a.Items {
		mark := " "
		switch it.Status {
		case "in_progress":
			mark = "~"
		case "done":
			mark = "x"
		}
		fmt.Fprintf(&b, "\n- [%s] %s: %s", mark, it.ID, it.Text)
	}
	return Result{OK: true, Content: b.String(), Meta: map[string]any{"counts": counts}}, nil
}

// schemaClarify asks the user one question (FEATURE-PARITY TOOL-021). The
// tool is eager because it has no module dependency; headless runs deny it.
const schemaClarify = `{"name":"clarify","description":"Ask the user one clarifying question, optionally with up to five choices. Use only when tools cannot answer.","parameters":{"type":"object","properties":{"question":{"type":"string","minLength":1,"maxLength":500},"choices":{"type":"array","maxItems":5,"items":{"type":"string","minLength":1,"maxLength":200}},"allow_free_text":{"type":"boolean"}},"required":["question"],"additionalProperties":false}}`

// Question is one clarify request.
type Question struct {
	Text          string
	Choices       []string
	AllowFreeText bool
}

// Asker is the interactive question channel (TUI, ACP, web UI, gateway).
type Asker interface {
	Ask(ctx context.Context, q Question) (string, error)
}

// clarifyTool asks Asker; without one it fails closed.
type clarifyTool struct{ ask Asker }

// NewClarify returns the clarify tool.
func NewClarify(ask Asker) Tool { return &clarifyTool{ask: ask} }

func (t *clarifyTool) Name() string            { return "clarify" }
func (t *clarifyTool) Schema() json.RawMessage { return json.RawMessage(schemaClarify) }
func (t *clarifyTool) Kind() Kind              { return KindOther }
func (t *clarifyTool) Effect(json.RawMessage) Effect {
	// An answer is an interaction with the user, not a read: it must not be
	// retried after an unknown outcome and it must not be treated as harmless
	// while the turn is tainted (REVIEW-F1-SEC7 TL7-9, ADR-070).
	return EffectNonIdempotent
}

// Inspect denies clarify without an interactive channel even in auto mode:
// the question has no answer to auto-approve.
func (t *clarifyTool) Inspect(context.Context, Env, json.RawMessage, PolicyContext) (PolicyDecision, error) {
	if t.ask == nil {
		return PolicyDecision{Requirement: RequireDeny, Class: "clarify"}, nil
	}
	return PolicyDecision{Requirement: RequireAuto, Class: "clarify"}, nil
}

// Run asks and returns the answer verbatim.
func (t *clarifyTool) Run(ctx context.Context, _ Env, args json.RawMessage) (Result, error) {
	if t.ask == nil {
		return Result{}, toolErr(CodeDenied, "use an interactive frontend", "clarify is unavailable in headless runs")
	}
	var a struct {
		Question      string   `json:"question"`
		Choices       []string `json:"choices"`
		AllowFreeText bool     `json:"allow_free_text"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	answer, err := t.ask.Ask(ctx, Question{Text: a.Question, Choices: a.Choices, AllowFreeText: a.AllowFreeText})
	if err != nil {
		if ctx.Err() != nil {
			return Result{}, &ToolError{Code: CodeTimeout, Message: "clarify cancelled", Err: err}
		}
		return Result{}, &ToolError{Code: CodeInternal, Message: "clarify: " + err.Error(), Err: err}
	}
	return Result{OK: true, Content: answer, Meta: map[string]any{"choices": len(a.Choices)}}, nil
}
