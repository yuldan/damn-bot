package kanban

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/tools"
)

// ToolSet is the 15-tool kanban surface (TOOL-055..069, TSET-020). Every
// tool is a thin call into the same Store, so the board state machine, lease
// and attachment guards apply identically to CLI, chat and the dispatcher.
// No tool executes a shell command, and none reads the board outside the
// task/board ids the dispatcher supplied.
type ToolSet struct {
	Store        *Store
	Board        string
	TaskID       string
	Actor        string
	Orchestrator bool
}

// NewTools returns the 15 tools in canonical order. taskID is the dispatcher
// task (HERMES_KANBAN_TASK); empty is a valid setup and every task-scoped
// call then answers module_unavailable.
func NewTools(store *Store, board, taskID, actor string, orchestrator bool) []tools.Tool {
	ts := &ToolSet{Store: store, Board: board, TaskID: taskID, Actor: actor, Orchestrator: orchestrator}
	names := []string{
		"kanban_attach", "kanban_attach_url", "kanban_attachments", "kanban_block", "kanban_comment",
		"kanban_complete", "kanban_create", "kanban_heartbeat", "kanban_link", "kanban_list",
		"kanban_request_changes", "kanban_request_review", "kanban_schedule", "kanban_show", "kanban_unblock",
	}
	out := make([]tools.Tool, 0, len(names))
	for _, name := range names {
		out = append(out, &tool{ts: ts, name: name})
	}
	return out
}

type tool struct {
	ts   *ToolSet
	name string
}

func (t *tool) Name() string { return t.name }

func (t *tool) Kind() tools.Kind {
	if t.read() {
		return tools.KindRead
	}
	return tools.KindEdit
}

func (t *tool) read() bool {
	switch t.name {
	case "kanban_show", "kanban_list", "kanban_attachments":
		return true
	}
	return false
}

func (t *tool) Effect(json.RawMessage) tools.Effect {
	if t.read() {
		return tools.EffectRead
	}
	if t.name == "kanban_heartbeat" || t.name == "kanban_create" {
		return tools.EffectIdempotent
	}
	return tools.EffectNonIdempotent
}

func (t *tool) Inspect(context.Context, tools.Env, json.RawMessage, tools.PolicyContext) (tools.PolicyDecision, error) {
	if t.read() {
		return tools.PolicyDecision{Requirement: tools.RequireAuto, Class: "read"}, nil
	}
	return tools.PolicyDecision{Requirement: tools.RequireAuto, Class: "kanban:write"}, nil
}

type toolArgs struct {
	TaskID     string   `json:"task_id"`
	Title      string   `json:"title"`
	Body       string   `json:"body"`
	Status     string   `json:"status"`
	Assignee   string   `json:"assignee"`
	Priority   *int     `json:"priority"`
	Labels     []string `json:"labels"`
	Links      []string `json:"links"`
	Reason     string   `json:"reason"`
	Text       string   `json:"text"`
	Path       string   `json:"path"`
	URL        string   `json:"url"`
	Name       string   `json:"name"`
	Dep        string   `json:"dep"`
	At         string   `json:"at"`
	Summary    string   `json:"summary"`
	Commit     string   `json:"commit"`
	Files      []string `json:"files"`
	Verify     []string `json:"verify"`
	RunID      string   `json:"run_id"`
	CostMicros int64    `json:"cost_micros"`
	Limit      int      `json:"limit"`
}

const toolParams = `{"type":"object","additionalProperties":false,"properties":{` +
	`"task_id":{"type":"string","maxLength":128},"title":{"type":"string","maxLength":200},` +
	`"body":{"type":"string","maxLength":65536},"status":{"type":"string","enum":["backlog","ready"]},` +
	`"assignee":{"type":"string","maxLength":128},"priority":{"type":"integer","minimum":0,"maximum":4},` +
	`"labels":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":64}},` +
	`"links":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":128}},` +
	`"reason":{"type":"string","maxLength":2000},"text":{"type":"string","maxLength":65536},` +
	`"path":{"type":"string","maxLength":4096},"url":{"type":"string","maxLength":4096},` +
	`"name":{"type":"string","maxLength":4096},"dep":{"type":"string","maxLength":128},` +
	`"at":{"type":"string","maxLength":64},"summary":{"type":"string","maxLength":65536},` +
	`"commit":{"type":"string","maxLength":128},"files":{"type":"array","maxItems":64,"items":{"type":"string","maxLength":4096}},` +
	`"verify":{"type":"array","maxItems":32,"items":{"type":"string","maxLength":512}},` +
	`"run_id":{"type":"string","maxLength":128},"cost_micros":{"type":"integer","minimum":0},` +
	`"limit":{"type":"integer","minimum":0,"maximum":500}}}`

func (t *tool) Schema() json.RawMessage {
	desc := map[string]string{
		"kanban_attach":          "Attach a file inside the workspace to a kanban task; outside paths and files over the size cap are rejected.",
		"kanban_attach_url":      "Attach an https URL to a kanban task; private, loopback and metadata destinations are rejected before fetch.",
		"kanban_attachments":     "List the attachments of a kanban task with size and hash.",
		"kanban_block":           "Mark a kanban task blocked; a reason is required.",
		"kanban_comment":         "Append a comment to a kanban task; history is append-only.",
		"kanban_complete":        "Complete a kanban task with a structured handoff; the run cost is billed once.",
		"kanban_create":          "Create a kanban child task with optional dependencies; idempotent on board and slug.",
		"kanban_heartbeat":       "Renew the worker lease of a claimed kanban task.",
		"kanban_link":            "Link a dependency between kanban tasks; cycles are rejected.",
		"kanban_list":            "List kanban tasks on the board; orchestrator mode only.",
		"kanban_request_changes": "Return a review task with a change request; a reason is required.",
		"kanban_request_review":  "Move an in-progress task to review.",
		"kanban_schedule":        "Schedule a kanban task with an RFC 3339 due time; past times are rejected.",
		"kanban_show":            "Show a kanban task with comments and attachments.",
		"kanban_unblock":         "Unblock a task; orchestrator mode only.",
	}
	doc := struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	}{t.name, desc[t.name], json.RawMessage(toolParams)}
	b := marshal(doc)
	return b
}

func (t *tool) Run(ctx context.Context, env tools.Env, raw json.RawMessage) (tools.Result, error) {
	var a toolArgs
	if err := json.Unmarshal(raw, &a); err != nil {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeInvalidArgs, Message: "arguments: " + err.Error()}
	}
	if a.TaskID == "" {
		a.TaskID = t.ts.TaskID
	}
	if !t.read() && t.name != "kanban_create" && a.TaskID == "" {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeModuleUnavailable, Message: "not in kanban dispatcher mode"}
	}
	if t.name == "kanban_list" && !t.ts.Orchestrator {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeDenied, Message: ErrOrchestratorOnly.Error()}
	}
	if t.name == "kanban_unblock" && !t.ts.Orchestrator {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeDenied, Message: ErrOrchestratorOnly.Error()}
	}
	v, err := t.run(ctx, env, &a)
	if err != nil {
		return tools.Result{}, toolError(err)
	}
	b, merr := json.Marshal(v)
	if merr != nil {
		return tools.Result{}, &tools.ToolError{Code: tools.CodeExecFailed, Message: "encode result", Err: merr}
	}
	return tools.Result{OK: true, Content: string(b)}, nil
}

func (t *tool) run(ctx context.Context, env tools.Env, a *toolArgs) (any, error) {
	if t.read() {
		return t.runRead(ctx, a)
	}
	if t.name == "kanban_attach" || t.name == "kanban_attach_url" || t.name == "kanban_attachments" {
		return t.runAttach(ctx, env, a)
	}
	return t.runWrite(ctx, a)
}

func (t *tool) runRead(ctx context.Context, a *toolArgs) (any, error) {
	s := t.ts.Store
	if t.name == "kanban_list" {
		return s.Tasks(ctx, t.ts.Board, Filter{Status: Status(a.Status), Assignee: a.Assignee, Limit: a.Limit})
	}
	if t.name == "kanban_attachments" {
		return s.Attachments(ctx, a.TaskID)
	}
	task, err := s.Task(ctx, a.TaskID)
	if err != nil {
		return nil, err
	}
	evs, err := s.TaskEvents(ctx, a.TaskID, 0)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task, "events": evs}, nil
}

func (t *tool) runAttach(ctx context.Context, env tools.Env, a *toolArgs) (any, error) {
	s := t.ts.Store
	switch t.name {
	case "kanban_attach":
		workspace := env.Cwd
		if workspace == "" {
			workspace = s.Workspace
		}
		cp := *s
		cp.Workspace = workspace
		return cp.AttachFile(ctx, a.TaskID, a.Path, t.ts.Actor)
	case "kanban_attach_url":
		return s.AttachURL(ctx, a.TaskID, a.URL, t.ts.Actor)
	default:
		return s.Attachments(ctx, a.TaskID)
	}
}

func (t *tool) runWrite(ctx context.Context, a *toolArgs) (any, error) {
	s := t.ts.Store
	switch t.name {
	case "kanban_create":
		return t.create(ctx, a)
	case "kanban_comment":
		return s.Comment(ctx, a.TaskID, a.Text, t.ts.Actor)
	case "kanban_complete":
		return t.complete(ctx, a)
	case "kanban_block":
		return s.Block(ctx, a.TaskID, a.Reason, t.ts.Actor)
	case "kanban_schedule":
		at, err := time.Parse(time.RFC3339, a.At)
		if err != nil {
			return nil, fmt.Errorf("%w: at needs RFC3339", ErrInvalid)
		}
		return s.Schedule(ctx, a.TaskID, at, t.ts.Actor)
	case "kanban_request_review":
		return s.RequestReview(ctx, a.TaskID, t.ts.Actor)
	case "kanban_request_changes":
		return s.RequestChanges(ctx, a.TaskID, a.Reason, t.ts.Actor)
	case "kanban_heartbeat":
		return s.Heartbeat(ctx, a.TaskID, t.ts.Actor, 0)
	case "kanban_link":
		return s.Link(ctx, a.TaskID, a.Dep, t.ts.Actor)
	case "kanban_unblock":
		return s.Unblock(ctx, a.TaskID, t.ts.Actor)
	}
	return nil, fmt.Errorf("%w: tool %s", ErrInvalid, t.name)
}

func (t *tool) create(ctx context.Context, a *toolArgs) (any, error) {
	status := Status(a.Status)
	if status == "" {
		status = StatusReady
	}
	spec := TaskSpec{Title: a.Title, Body: a.Body, Status: status, Assignee: a.Assignee, Priority: a.Priority, Labels: a.Labels, Links: a.Links}
	task, created, err := t.ts.Store.CreateTask(ctx, t.ts.Board, &spec, t.ts.Actor)
	if err != nil {
		return nil, err
	}
	return map[string]any{"task": task, "created": created}, nil
}

func (t *tool) complete(ctx context.Context, a *toolArgs) (any, error) {
	var run *Run
	if a.RunID != "" {
		run = &Run{ID: a.RunID, Worker: t.ts.Actor, Outcome: "ok", CostMicros: a.CostMicros, FinishedAt: time.Now().UTC()}
	}
	return t.ts.Store.Complete(ctx, a.TaskID, &Handoff{Summary: a.Summary, Files: a.Files, Commit: a.Commit, Verify: a.Verify}, run, t.ts.Actor)
}

func toolError(err error) *tools.ToolError {
	code := tools.CodeExecFailed
	switch {
	case errors.Is(err, ErrInvalid), errors.Is(err, ErrCycle):
		code = tools.CodeInvalidArgs
	case errors.Is(err, ErrNotFound), errors.Is(err, ErrBoardNotFound):
		code = tools.CodeNotFound
	case errors.Is(err, ErrOutsideWorkspace):
		code = tools.CodeOutsideWorkspace
	case errors.Is(err, ErrBlockedURL):
		code = tools.CodeBlockedHost
	case errors.Is(err, ErrTooLarge):
		code = tools.CodeTooLarge
	case errors.Is(err, ErrTransition), errors.Is(err, ErrClaimed), errors.Is(err, ErrNotOwner), errors.Is(err, ErrOrchestratorOnly):
		code = tools.CodeDenied
	case errors.Is(err, ErrNotConfigured):
		code = tools.CodeModuleUnavailable
	}
	return &tools.ToolError{Code: code, Message: err.Error(), Err: err}
}
