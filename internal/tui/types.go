package tui

import "time"

// Kind is one stream update kind. It mirrors loop/event.UpdateKind one-for-one
// so the composition root can translate the loop stream without the TUI
// importing that leaf (E-03 import matrix).
type Kind string

// Stream update kinds (loop/event.UpdateKind, SPEC §4.1).
const (
	KindTextDelta     Kind = "text_delta"
	KindToolCallStart Kind = "tool_call_start"
	KindToolCallArgs  Kind = "tool_call_args"
	KindToolCallEnd   Kind = "tool_call_end"
	KindToolResult    Kind = "tool_result"
	KindUsage         Kind = "usage"
	KindNotice        Kind = "notice"
	KindStop          Kind = "stop"
)

// StopReason is the loop's coarse terminal state (loop.StopReason, ADR-109).
type StopReason string

// StopDetail is the exact SPEC §3.5 stop_reason behind a StopReason
// (loop.StopDetail). The vocabulary is closed by the loop; the TUI only
// carries and displays it.
type StopDetail string

// Usage is one stream update's accounting, translated from loop/event.Usage.
type Usage struct {
	Input       int64
	CachedInput int64
	Output      int64
	CostUSD     *float64
	Iterations  int
}

// Update is one item of the loop stream in TUI terms. The composition root
// translates loop/event.Update into this type; At carries the loop clock
// stamp of the update (never a wall-clock read inside the TUI). ActionID is
// the journal action anchored on the call when the composition root resolves
// one (0 = not undoable).
type Update struct {
	Kind       Kind
	Text       string
	ToolCallID string
	ToolName   string
	Status     string
	Stop       StopReason
	Detail     StopDetail
	Usage      *Usage
	Err        string
	ActionID   int64
	At         time.Time
}

// BlockKind is the transcript role of one rendered block.
type BlockKind string

// Transcript block kinds.
const (
	BlockUser      BlockKind = "user"
	BlockAssistant BlockKind = "assistant"
	BlockTool      BlockKind = "tool"
	BlockNotice    BlockKind = "notice"
)

// ToolStatus is the normalized display state of a tool block: SPEC §13.1's
// ✓/✗/…/⏸ vocabulary.
type ToolStatus string

// Tool statuses.
const (
	ToolRunning ToolStatus = "running" // … dispatched, no result yet
	ToolOK      ToolStatus = "ok"      // ✓
	ToolFailed  ToolStatus = "failed"  // ✗
	ToolPending ToolStatus = "pending" // ⏸ awaiting approval
)

// Block is one transcript entry the chat panel renders. The tool block keeps
// every R1 attribute (SPEC §13.1) as structured data; the plain renderer is
// the only place that turns them into text, so the theme/virtualisation
// tracks replace rendering, not this model.
type Block struct {
	Kind       BlockKind
	Text       string // assistant text, tool result, or notice
	ToolName   string
	ToolCallID string
	Args       string // raw JSON arguments as streamed
	Tag        string // CODE-TOOLS content tag (8 hex chars, no '#')
	Status     ToolStatus
	Duration   time.Duration
	Size       int    // result characters (full size when spilled)
	Spill      string // blob path from the result marker
	SpillSize  int    // full result characters when spilled
	ActionID   int64  // journal action, 0 = not undoable
	ExitCode   *int   // shell exit code when the result carries one
	At         time.Time
}

// Input is the user's submission handed to the host Runner.
type Input struct {
	Text     string
	PlanMode bool
}

// Config is the display-relevant configuration snapshot. It carries no
// secrets: the host redacts before it reaches the TUI.
type Config struct {
	Model           string
	Theme           string        // [ui] theme (SPEC §12); empty resolves to tern-dark
	ApprovalTimeout time.Duration // approval.preview_timeout_s (§18.2); 0 = default
	Models          []string      // configured refs: primary, fallback, light, custom
	Budget          BudgetInfo
}

// BudgetInfo is the configured cost cap snapshot (SPEC §12 [budget]).
type BudgetInfo struct {
	TurnUSD, SessionUSD, DailyUSD float64
}
