package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Memory backend errors the composition root maps from memory.Service.
var (
	ErrMemoryNotFound = errors.New("tools: memory not found")
	ErrMemoryInvalid  = errors.New("tools: memory invalid")
	ErrMemoryFull     = errors.New("tools: memory full")
)

// MemoryAdd is one agent write. CallID identifies the dispatch event used as
// evidence (agent writes must be traceable, P10); the adapter resolves the
// event id and session from it.
type MemoryAdd struct {
	CallID, Category, Scope, Content string
}

// MemoryEntry is one approved memory returned to the model.
type MemoryEntry struct {
	ID              int64
	Category, Scope string
	Content         string
	Pinned          bool
}

// MemoryBackend is the consumer seam over memory.Service (E-09): primitives
// only, so the tools package does not import the memory package.
type MemoryBackend interface {
	Add(ctx context.Context, req MemoryAdd) (id int64, status string, err error)
	Replace(ctx context.Context, req MemoryAdd, id int64) (newID int64, status string, err error)
	Remove(ctx context.Context, id int64, scope string) error
	Read(ctx context.Context, query, scope string, k int) ([]MemoryEntry, error)
}

const schemaMemory = `{"name":"memory","description":"Add, replace, remove or read durable agent/user memories across sessions.","parameters":{"type":"object","properties":{"action":{"type":"string","enum":["add","replace","remove","read"]},"target":{"type":"string","enum":["memory","user"]},"content":{"type":"string","maxLength":500},"category":{"type":"string","enum":["fact","preference","project","procedure"]},"id":{"type":"integer","minimum":1},"query":{"type":"string","maxLength":200}},"required":["action"],"additionalProperties":false}}`

// memoryTool exposes the durable memory store (TOOL-070).
type memoryTool struct{ be MemoryBackend }

// NewMemory returns the memory tool; a nil backend fails closed.
func NewMemory(be MemoryBackend) Tool { return &memoryTool{be: be} }

func (t *memoryTool) Name() string            { return "memory" }
func (t *memoryTool) Schema() json.RawMessage { return json.RawMessage(schemaMemory) }
func (t *memoryTool) Kind() Kind              { return KindEdit }
func (t *memoryTool) Effect(json.RawMessage) Effect {
	return EffectNonIdempotent // adding the same note twice is two notes
}

type memoryArgs struct {
	Action   string `json:"action"`
	Target   string `json:"target"`
	Content  string `json:"content"`
	Category string `json:"category"`
	ID       int64  `json:"id"`
	Query    string `json:"query"`
}

// Run dispatches one memory action. Agent writes go through the memory
// service, so they are quarantined and scanned exactly like other agent writes.
func (t *memoryTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	var a memoryArgs
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	if t.be == nil {
		return Result{}, toolErr(CodeInternal, "memory backend unavailable", "the composition root did not wire memory")
	}
	scope, err := memoryScope(a.Target, env.Cwd)
	if err != nil {
		return Result{}, err
	}
	switch a.Action {
	case "read":
		return t.read(ctx, &a, scope)
	case "remove":
		if a.ID <= 0 {
			return Result{}, toolErr(CodeInvalidArgs, "remove needs id", "memories removed by id returned from read")
		}
		if merr := t.be.Remove(ctx, a.ID, scope); merr != nil {
			return Result{}, memoryErr(merr)
		}
		return Result{OK: true, Content: fmt.Sprintf("removed memory %d", a.ID), Meta: map[string]any{"id": a.ID}}, nil
	case "add", "replace":
		return t.write(ctx, &a, scope, env)
	default:
		return Result{}, toolErr(CodeInvalidArgs, "action must be add, replace, remove or read", "")
	}
}

// memoryScope maps the tool target to the SPEC §4.8 scope.
func memoryScope(target, cwd string) (string, error) {
	switch target {
	case "", "memory":
		if cwd == "" {
			return "", toolErr(CodeInvalidArgs, "memory target needs a workspace", "run from a workspace directory")
		}
		return "project:" + cwd, nil
	case "user":
		return "user", nil
	}
	return "", toolErr(CodeInvalidArgs, "target must be memory or user", "")
}

func (t *memoryTool) read(ctx context.Context, a *memoryArgs, scope string) (Result, error) {
	query := strings.TrimSpace(a.Query)
	if query == "" {
		return Result{}, toolErr(CodeInvalidArgs, "read needs query", "use a keyword from the note you need")
	}
	entries, err := t.be.Read(ctx, query, scope, DefaultMemoryRead)
	if err != nil {
		return Result{}, memoryErr(err)
	}
	if len(entries) == 0 {
		return Result{OK: true, Content: "no memories match", Meta: map[string]any{"count": 0}}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d memories:", len(entries))
	for _, e := range entries {
		pin := ""
		if e.Pinned {
			pin = " pinned"
		}
		fmt.Fprintf(&b, "\n- [%d] (%s%s) %s", e.ID, e.Category, pin, oneLine(e.Content))
	}
	return Result{OK: true, Content: b.String(), Meta: map[string]any{"count": len(entries)}}, nil
}

// DefaultMemoryRead is the k used by the memory tool when reading.
const DefaultMemoryRead = 5

func (t *memoryTool) write(ctx context.Context, a *memoryArgs, scope string, env Env) (Result, error) {
	content := strings.TrimSpace(a.Content)
	if content == "" {
		return Result{}, toolErr(CodeInvalidArgs, "content required", "")
	}
	if len([]rune(content)) > 500 {
		return Result{}, toolErr(CodeTooLarge, "memory content exceeds 500 chars", "split the note")
	}
	category := a.Category
	if category == "" {
		category = "fact"
	}
	req := MemoryAdd{CallID: env.CallID, Category: category, Scope: scope, Content: content}
	var id int64
	var status string
	var err error
	if a.Action == "replace" {
		if a.ID <= 0 {
			return Result{}, toolErr(CodeInvalidArgs, "replace needs id of the note to supersede", "")
		}
		id, status, err = t.be.Replace(ctx, req, a.ID)
	} else {
		id, status, err = t.be.Add(ctx, req)
	}
	if err != nil {
		return Result{}, memoryErr(err)
	}
	content = fmt.Sprintf("stored memory %d (%s)", id, status)
	if status != "approved" {
		content += "; it is quarantined until the user approves it"
	}
	return Result{OK: true, Content: content, Meta: map[string]any{"id": id, "status": status}}, nil
}

// memoryErr maps backend errors onto the closed tool error codes.
func memoryErr(err error) error {
	switch {
	case errors.Is(err, ErrMemoryNotFound):
		return &ToolError{Code: CodeNotFound, Message: "memory entry not found", Err: err}
	case errors.Is(err, ErrMemoryFull):
		return &ToolError{Code: CodeTooLarge, Message: "memory scope is full", Err: err}
	case errors.Is(err, ErrMemoryInvalid):
		return &ToolError{Code: CodeInvalidArgs, Message: "memory request rejected", Err: err}
	}
	return &ToolError{Code: CodeInternal, Message: "memory: " + err.Error(), Err: err}
}

// oneLine collapses a stored value to a single bounded line.
func oneLine(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	s = strings.ReplaceAll(s, "\r", " ")
	r := []rune(s)
	if len(r) > 160 {
		return string(r[:160]) + "…"
	}
	return s
}
