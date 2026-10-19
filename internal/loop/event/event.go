// Package event defines the types the turn loop appends to the event log
// and streams to a frontend. It is a leaf (ENGINEERING E-02): declarations
// only, no imports outside the standard library, no I/O, no behaviour.
//
// The persisted vocabulary mirrors events.kind in SPEC §11.2 exactly; a kind
// outside that CHECK constraint cannot be stored. Stream updates are a
// separate vocabulary (text deltas, tool-call fragments, usage, stop) and are
// never persisted as events.
package event

import "encoding/json"

// Kind is one events.kind value. The set is closed (SPEC §11.2).
type Kind string

// Persisted event kinds (SPEC §11.2, migrations/0001_core.sql + 0007_gaps.sql).
const (
	UserMessage      Kind = "user_message"
	AssistantMessage Kind = "assistant_message"
	ToolDispatch     Kind = "tool_dispatch"
	ToolRejected     Kind = "tool_rejected"
	ToolResult       Kind = "tool_result"
	LLMCall          Kind = "llm_call"
	StreamAborted    Kind = "stream_aborted"
	ProviderFallback Kind = "provider_fallback"
	AuthRotated      Kind = "credential_rotated"
	Compaction       Kind = "compaction"
	SummaryRef       Kind = "summary_ref"
	Approval         Kind = "approval"
	Preview          Kind = "preview"
	HookError        Kind = "hook_error"
	Error            Kind = "error"
	BudgetWarning    Kind = "budget_warning"
	BudgetExhausted  Kind = "budget_exhausted"
	TurnStart        Kind = "turn_start"
	TurnEnd          Kind = "turn_end"
	TodoUpdate       Kind = "todo_update"
	Plan             Kind = "plan"
	PlanStep         Kind = "plan_step"
	PlanDeviation    Kind = "plan_deviation"
	VerifyRun        Kind = "verify_run"
	Undo             Kind = "undo"
	SystemNotice     Kind = "system_notice"
	ConfigReload     Kind = "config_reload"
	ModuleStart      Kind = "module_start"
	ModuleError      Kind = "module_error"
	DelegateStart    Kind = "delegate_start"
	DelegateEnd      Kind = "delegate_end"
	CrashRecovered   Kind = "crash_recovered"
	Imported         Kind = "imported"
	ContextExclude   Kind = "context_exclude"
	DLPBlocked       Kind = "dlp_blocked"
	ACPFallback      Kind = "acp_fs_fallback"
)

// Event is one row of the transcript as the loop sees it. Store assigns seq,
// hash chain, and the event id; the loop supplies turn identity separately.
type Event struct {
	Kind       Kind
	Role       string // user|assistant|tool|system (events.role)
	Content    string // exactly what the model sees, after redaction
	ToolCallID string
	ToolName   string
	Payload    json.RawMessage // JSON object; nil is stored as {}
	BlobSHA    string
}

// StopReason is the loop's coarse terminal state (ADR-109). Detail carries
// the exact SPEC §3.5 stop_reason so the frontends do not infer it.
type StopReason string

// Terminal reasons. cancelled_confirmed is the stricter cancellation outcome:
// every in-flight tool reported cancelled_confirmed inside the kill grace.
const (
	StopEndTurn            StopReason = "end_turn"
	StopMaxIterations      StopReason = "max_iterations"
	StopMaxTokens          StopReason = "max_tokens"
	StopCancelled          StopReason = "cancelled"
	StopCancelledConfirmed StopReason = "cancelled_confirmed"
	StopContentFiltered    StopReason = "content_filtered"
	StopError              StopReason = "error"
	StopApprovalDenied     StopReason = "approval_denied"
	StopToolError          StopReason = "tool_error"
)

// StopDetail is the SPEC §3.5 stop_reason behind a coarse StopReason
// (ADR-109): end_turn covers completed, empty_response, no_progress, and
// invalid_answer; max_tokens covers output_truncated, context_overflow, and
// the budget_* rows; error covers provider_error and internal failures.
type StopDetail string

// Stop details.
const (
	DetailCompleted       StopDetail = "completed"
	DetailEmptyResponse   StopDetail = "empty_response"
	DetailNoProgress      StopDetail = "no_progress"
	DetailInvalidAnswer   StopDetail = "invalid_answer"
	DetailOutputTruncated StopDetail = "output_truncated"
	DetailContextOverflow StopDetail = "context_overflow"
	DetailBudgetInput     StopDetail = "budget_input_tokens"
	DetailBudgetOutput    StopDetail = "budget_output_tokens"
	DetailCancelled       StopDetail = "cancelled"
	DetailContentFiltered StopDetail = "content_filtered"
	DetailProviderError   StopDetail = "provider_error"
	DetailInternal        StopDetail = "internal"
	DetailApprovalDenied  StopDetail = "approval_denied"
	DetailToolError       StopDetail = "tool_error"
)

// UpdateKind is one stream update for a frontend (never an events.kind).
type UpdateKind string

// Stream update kinds.
const (
	UpdateTextDelta     UpdateKind = "text_delta"
	UpdateToolCallStart UpdateKind = "tool_call_start"
	UpdateToolCallArgs  UpdateKind = "tool_call_args"
	UpdateToolCallEnd   UpdateKind = "tool_call_end"
	UpdateToolResult    UpdateKind = "tool_result"
	UpdateUsage         UpdateKind = "usage"
	UpdateNotice        UpdateKind = "notice"
	UpdateStop          UpdateKind = "stop"
)

// Update is one item on the stream returned by Loop.Prompt.
type Update struct {
	Kind       UpdateKind
	Text       string // text delta, args fragment, or notice text
	ToolCallID string
	ToolName   string
	Status     string // ExecStatus of a tool result when the tool reports one
	Stop       StopReason
	Detail     StopDetail
	Usage      *Usage // usage update and final stop update
	Err        string
}

// Usage is one llm_call's accounting plus the turn totals.
type Usage struct {
	Input       int64
	CachedInput int64
	Output      int64
	CostUSD     *float64 // nil when the provider did not send a cost
	Iterations  int
}
