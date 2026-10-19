// Package loop is the turn state machine (SPEC §3.4, §4.2). It owns iteration
// budgeting, response classification, tool dispatch order, cancellation, and
// the transcript/stream events of one turn. It performs no I/O itself: every
// effect goes through an interface declared here (E-09), and the only imports
// are the standard library, internal/loop/event, and internal/boundary (E-02).
package loop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/damnbot/damnbot/internal/loop/event"
)

// Message is one chat message sent to the provider. The zero ToolCalls slice
// is omitted by the encoder (EC-TCALL-001: an empty array is a 400).
type Message struct {
	Role       string // system|user|assistant|tool
	Content    string
	ToolCalls  []ToolCall
	ToolCallID string
}

// ToolCall is a native model tool call. Arguments stay raw; schema
// validation belongs to the Toolset.
type ToolCall struct {
	ID        string
	Name      string
	Arguments string // JSON object as streamed, after assembly
}

// ToolSchema is one entry of the request tools array (Assembler output).
type ToolSchema struct {
	Name        string
	Description string
	Parameters  json.RawMessage
}

// Request is one provider call.
type Request struct {
	Model     string
	Messages  []Message
	Tools     []ToolSchema
	MaxTokens int
}

// Prompt is the Assembler's output: the request plus its local token
// accounting, used by the context-window guard.
type Prompt struct {
	Request
	InputTokens  int64 // estimated request tokens (COST-09)
	WindowTokens int64 // model context window; 0 = unknown
}

// Finish reasons (SPEC §4.1). content_filter ends the turn whatever else the
// reply holds (ADR-072/082).
const (
	FinishStop          = "stop"
	FinishToolCalls     = "tool_calls"
	FinishLength        = "length"
	FinishContentFilter = "content_filter"
)

// Delta is one stream fragment. ToolCall fragments are assembled by the loop
// (EC-PROV-001): same index continues a call; a repeated index with a new id
// starts a new call; an id-less, nameless fragment continues the last call.
type Delta struct {
	Text      string
	ToolCalls []ToolCallDelta
}

// ToolCallDelta is one streamed tool-call fragment.
type ToolCallDelta struct {
	Index     int
	HasIndex  bool // the provider sent an index for this fragment
	ID        string
	Name      string
	Arguments string
}

// Reply is a completed provider response. ToolCalls may be empty when the
// Provider already assembled the fragments; the loop then uses the deltas.
type Reply struct {
	Content      string
	ToolCalls    []ToolCall
	FinishReason string
	Usage        event.Usage
	Model        string
	FallbackFrom string // P7: the primary that failed, empty when no fallback
	Attempts     int
}

// Class is the only provider-error classification the loop acts on. The
// provider adapter maps retry classes (SPEC §4.1.1) onto it; everything else
// is a terminal provider error.
type Class uint8

// Provider error classes the loop distinguishes.
const (
	ClassProvider Class = iota // any terminal provider failure
	ClassEmptyResponse
	ClassContextLength
	ClassCancelled
)

// StreamError is a classified provider failure. An adapter wraps the
// provider package's typed error here so loop stays free of that import.
type StreamError struct {
	Class Class
	Err   error
}

func (e *StreamError) Error() string { return "provider: " + e.Err.Error() }

// Unwrap exposes the underlying provider error.
func (e *StreamError) Unwrap() error { return e.Err }

// Effect is the recovered effect class of SPEC §4.3.
type Effect string

// Effect classes.
const (
	EffectRead          Effect = "read"
	EffectIdempotent    Effect = "idempotent"
	EffectNonIdempotent Effect = "non_idempotent"
)

// ApprovalMode says whether the loop must consult the Approver.
type ApprovalMode uint8

// Approval modes returned by Toolset.Prepare.
const (
	ApprovalNone ApprovalMode = iota
	ApprovalAsk
)

// Prepared is a decoded, schema-validated, sandboxed tool call ready for
// approval and dispatch (Toolset owns pipeline steps 1-6 of SPEC §4.3).
type Prepared struct {
	Call      ToolCall
	Effect    Effect
	Subject   string   // allow_session subject (exact pair); empty = the call
	Subjects  []string // exact subjects when a decision grants several paths
	Preview   string   // approval preview (SPEC §18.2)
	Approval  ApprovalMode
	NoSession bool // allow_session never applies (shell, P26, taint)
	Terminal  bool // a denial ends the turn (approval_denied)
	Approved  bool // adapter sets after the loop approver allows outside-root
}

// ToolError is a model-facing tool failure. Code is one of the closed
// SPEC §4.3 codes.
type ToolError struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint,omitempty"`
}

func (e *ToolError) Error() string { return e.Code + ": " + e.Message }

// Loop-owned error codes (all in the closed SPEC §4.3 list).
const (
	CodeInvalidArgs = "invalid_args"
	CodeDenied      = "denied"
	CodeTooLarge    = "too_large"
	CodeInternal    = "internal"
)

// ToolResult is what a tool returns. Err is a model-facing failure; a Go
// error from Dispatch is fatal to the turn (tool_error). Tainted marks
// untrusted output: every later non-read action is forced to ask (ADR-070).
type ToolResult struct {
	Content string
	Err     *ToolError
	Status  string // ExecStatus where the tool defines one (ADR-082)
	Tainted bool
	BlobSHA string
	Meta    json.RawMessage
}

// Attachment is one input attachment reference. The Assembler resolves it.
type Attachment struct {
	Name    string
	MIME    string
	BlobSHA string
	Size    int64
}

// Input is one user prompt (SPEC §4.2).
type Input struct {
	Text        string
	Attachments []Attachment
	PlanMode    bool
}

// AssembleRequest is what the loop asks the Assembler to build.
type AssembleRequest struct {
	SessionID string
	TurnID    string
	Input     Input
	Iteration int
	// History is the exact model-visible sequence the loop produced: user
	// message, assistant tool-call messages, and tool results. The Assembler
	// merges it with its tiers; it never reads Store on the loop's behalf.
	History []Message
}

// Decision is an approval answer (SPEC §4.2/§14.1).
type Decision string

// Approval decisions.
const (
	DecisionAllowOnce    Decision = "allow_once"
	DecisionAllowSession Decision = "allow_session"
	DecisionDeny         Decision = "deny"
	DecisionEdit         Decision = "edit"
)

// ApprovalRequest is one pending approval shown to the user.
type ApprovalRequest struct {
	SessionID string
	TurnID    string
	Tool      string
	Args      json.RawMessage
	Preview   string
	Effect    Effect
	Tainted   bool
	Terminal  bool
}

// ApprovalResult is the Approver's answer. Args replaces the arguments when
// Decision is edit; the loop re-prepares and re-validates them.
type ApprovalResult struct {
	Decision Decision
	Args     json.RawMessage
}

// Provider streams one chat completion (SPEC §4.1).
type Provider interface {
	Stream(ctx context.Context, req *Request, emit func(Delta)) (Reply, error)
}

// Toolset owns the dispatch pipeline except approval: Prepare decodes,
// validates, sandboxes, and classifies the call; Dispatch executes it.
// Dispatch must return promptly once ctx is canceled (REL-06).
type Toolset interface {
	Prepare(ctx context.Context, call ToolCall) (Prepared, error)
	Dispatch(ctx context.Context, call *Prepared) (ToolResult, error)
}

// Assembler builds the tiered prompt and reports its token accounting
// (SPEC §6, ADR-067).
type Assembler interface {
	Assemble(ctx context.Context, req *AssembleRequest) (Prompt, error)
}

// Compactor is the F2 compaction stage; the loop only calls it once when the
// prompt crosses the compaction threshold.
type Compactor interface {
	Compact(ctx context.Context, sessionID string) error
}

// Store appends one transcript event atomically (ADR-074; ctxdb implements
// the storage side, the loop only sees this one-method seam).
type Store interface {
	Append(ctx context.Context, sessionID, turnID string, ev *event.Event) (int64, error)
}

// Approver answers one approval request (SPEC §4.2).
type Approver interface {
	Approve(ctx context.Context, req *ApprovalRequest) (ApprovalResult, error)
}

// Ledger records one llm_call's usage (SPEC §18.4).
type Ledger interface {
	Record(ctx context.Context, sessionID, turnID string, eventID int64, u *event.Usage) error
}

// Redactor removes secrets from text the loop persists or shows (SPEC §14.3).
type Redactor interface {
	Redact(text string) string
}

// Spiller persists oversized tool output and returns a stable reference
// (SPEC P5). The F1 tools package implements the real spill.
type Spiller interface {
	Spill(ctx context.Context, content string) (ref string, err error)
}

// Config bounds one Loop. Zero values take the defaults in New.
type Config struct {
	MaxIterations   int           // default 40 (SPEC §3.4)
	MaxInputTokens  int64         // turn input cap, 0 = unlimited
	MaxOutputTokens int64         // turn output cap, 0 = unlimited
	MaxWall         time.Duration // wall-clock cap, 0 = unlimited (REL-07)
	MaxIdentical    int           // no_progress threshold, default 3 (COST-07)
	MaxInline       int           // per-result inline chars, default 4000 (P5)
	MaxInlineTurn   int           // turn aggregate inline chars, default 24000 (P5)
	StreamBuffer    int           // stream channel capacity, default 16
	ParallelTools   int           // concurrent read-effect dispatches, default 4
	KillGrace       time.Duration // cancellation confirmation window, default 1s (REL-06)
	CompactAt       int           // percent of window that triggers compaction, default 75
	OverflowAt      int           // percent of window that stops the turn, default 90
	Now             func() time.Time
	NewTurnID       func() string
}

// Deps are the loop's effects. Provider and Assembler are required; the rest
// are optional and documented per field.
type Deps struct {
	Provider  Provider
	Assembler Assembler
	Toolset   Toolset  // required once a reply carries a tool call
	Store     Store    // nil = no transcript persistence
	Ledger    Ledger   // nil = no usage accounting
	Approver  Approver // nil = every approval is denied (headless)
	Compactor Compactor
	Redactor  Redactor // nil = no redaction (tests only)
	Spiller   Spiller  // nil = oversized results become too_large
}

// TurnResult is the terminal state of one turn.
type TurnResult struct {
	SessionID string
	TurnID    string
	Stop      event.StopReason
	Detail    event.StopDetail
	Usage     event.Usage
	Answer    string
}

func (r TurnResult) String() string {
	return fmt.Sprintf("%s/%s after %d iteration(s)", r.Stop, r.Detail, r.Usage.Iterations)
}
