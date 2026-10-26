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
// stamp of the update (never a wall-clock read inside the TUI).
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

// Block is one transcript entry the chat panel renders.
type Block struct {
	Kind       BlockKind
	Text       string
	ToolName   string
	ToolCallID string
	Status     string
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
	Model string
}
