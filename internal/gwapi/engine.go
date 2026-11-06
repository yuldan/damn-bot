package gwapi

import (
	"context"
	"encoding/json"
)

// Engine turns one API request into stream updates. The composition root
// adapts the existing loop/provider turn machinery.
type Engine interface {
	Run(ctx context.Context, turn *Turn) (<-chan Update, error)
}

// Turn is one engine invocation built from the wire request.
type Turn struct {
	SessionID string
	Model     string
	Messages  []Message
	// Tools is the validated subset of request tools this surface exposes.
	Tools     []string
	MaxTokens int
	// Meta records the inbound surface for the loop's user_message payload
	// (origin, taint). It never reaches the model.
	Meta map[string]string
}

// UpdateKind is one stream item the engine emits.
type UpdateKind uint8

// Engine update kinds.
const (
	UpdateText UpdateKind = iota + 1
	UpdateToolCall
	UpdateToolResult
	UpdateUsage
	UpdateStop
)

// Update is one streamed turn item.
type Update struct {
	Kind         UpdateKind
	Text         string
	ToolCallID   string
	ToolName     string
	ToolArgs     string
	Content      string
	Usage        *Usage
	FinishReason string
	Err          string
}

// Toolbox describes the internal tools the server may expose. Tools form the
// composite bundle (TSET-042); ApprovalRequired marks calls refused with a
// structured error instead of being executed silently.
type Toolbox interface {
	Tools() []ToolSpec
}

// ToolSpec is one exposable internal tool.
type ToolSpec struct {
	Name             string
	Description      string
	Parameters       json.RawMessage
	ApprovalRequired bool
}
