package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// SessionHit is one transcript match returned by the backend.
type SessionHit struct {
	SessionID string
	EventID   int64
	Kind      string
	Snippet   string
	Score     float64
}

// SessionScope is the caller's explicit search scope (GAP-REL-18): its own
// session and workspace. Both fields are required; the backend fails closed
// rather than searching every session when one is empty.
type SessionScope struct {
	SessionID string
	Cwd       string
}

// SessionSearchBackend is the consumer seam over ctxdb.Search. The scope is
// always the session view received by the tool, never a process-global set.
type SessionSearchBackend interface {
	Search(ctx context.Context, query string, k int, scope SessionScope) ([]SessionHit, error)
}

const schemaSessionSearch = `{"name":"session_search","description":"Search past session transcripts (FTS) and return ranked snippets.","parameters":{"type":"object","properties":{"query":{"type":"string","minLength":1,"maxLength":200},"k":{"type":"integer","minimum":1,"maximum":10}},"required":["query"],"additionalProperties":false}}`

// sessionSearchTool searches earlier conversations (TOOL-073).
type sessionSearchTool struct{ be SessionSearchBackend }

// NewSessionSearch returns the session_search tool.
func NewSessionSearch(be SessionSearchBackend) Tool { return &sessionSearchTool{be: be} }

func (t *sessionSearchTool) Name() string            { return "session_search" }
func (t *sessionSearchTool) Schema() json.RawMessage { return json.RawMessage(schemaSessionSearch) }
func (t *sessionSearchTool) Kind() Kind              { return KindRead }
func (t *sessionSearchTool) Effect(json.RawMessage) Effect {
	return EffectRead
}

// Run searches and renders ranked hits. Transcript text is untrusted, so the
// result taints the turn (SPEC §14.5).
func (t *sessionSearchTool) Run(ctx context.Context, env Env, args json.RawMessage) (Result, error) {
	var a struct {
		Query string `json:"query"`
		K     int    `json:"k"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return Result{}, &ToolError{Code: CodeInvalidArgs, Message: "arguments: " + err.Error(), Err: err}
	}
	query := strings.TrimSpace(a.Query)
	if query == "" || len([]rune(query)) > 200 {
		return Result{}, toolErr(CodeInvalidArgs, "query must be 1..200 chars", "")
	}
	if t.be == nil {
		return Result{}, toolErr(CodeInternal, "session search unavailable", "the composition root did not wire ctxdb search")
	}
	view, terr := requireSession(env, "session_search")
	if terr != nil {
		return Result{}, terr
	}
	cwd := env.Cwd
	if cwd == "" {
		cwd = view.Cwd()
	}
	if cwd == "" { // fail closed: no workspace, no search (SEC17-1)
		return Result{}, toolErr(CodeInvalidArgs, "session search needs a workspace", "")
	}
	k := a.K
	if k <= 0 {
		k = 5
	}
	if k > 10 {
		k = 10
	}
	hits, err := t.be.Search(ctx, query, k, SessionScope{SessionID: view.SessionID(), Cwd: cwd})
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return Result{}, &ToolError{Code: CodeTimeout, Message: "session search cancelled", Err: err}
		}
		return Result{}, &ToolError{Code: CodeInternal, Message: "session search: " + err.Error(), Err: err}
	}
	if len(hits) == 0 {
		return Result{OK: true, Content: "no matching sessions"}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%d matches:", len(hits))
	for _, h := range hits {
		fmt.Fprintf(&b, "\n- %s#%d (%s): %s", h.SessionID, h.EventID, h.Kind, oneLine(h.Snippet))
	}
	return Result{OK: true, Content: b.String(), Meta: map[string]any{"count": len(hits)}, UntrustedSource: "session"}, nil
}
