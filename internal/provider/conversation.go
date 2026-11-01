//go:build !minimal

package provider

import "fmt"

// Turn is one conversation message after the shared role and field validation
// of EC-TCALL-011. The openai-chat encoder and the openai-responses converter
// both render this list, so a role, a tool call, or a tool result cannot be
// mapped one way by one adapter and another way (or dropped) by the other.
type Turn struct {
	Role       string // system | user | assistant | tool
	Content    string
	ToolCalls  []ToolCall // assistant only
	ToolCallID string     // tool only
	Message    int        // index into the original Request.Messages
}

// Conversation validates msgs and returns their canonical turns. An empty
// role is user (the provider packages' historical default); every other role
// must be one of system, user, assistant, or tool. A field the role cannot
// carry is an error, never a silent coercion or drop.
func Conversation(msgs []Message) ([]Turn, error) {
	turns := make([]Turn, len(msgs))
	for i := range msgs {
		t, err := turnOf(&msgs[i], i)
		if err != nil {
			return nil, err
		}
		turns[i] = t
	}
	return turns, nil
}

// turnOf validates one message and copies its wire-relevant fields.
func turnOf(m *Message, i int) (Turn, error) {
	role, err := turnRole(m.Role)
	if err != nil {
		return Turn{}, fmt.Errorf("message %d: %w", i, err)
	}
	t := Turn{Role: role, Content: m.Content, ToolCalls: m.ToolCalls, ToolCallID: m.ToolCallID, Message: i}
	switch role {
	case "assistant":
		if t.ToolCallID != "" {
			return Turn{}, fmt.Errorf("message %d: assistant cannot carry tool_call_id %q", i, t.ToolCallID)
		}
		for j := range t.ToolCalls {
			if t.ToolCalls[j].ID == "" || t.ToolCalls[j].Name == "" {
				return Turn{}, fmt.Errorf("message %d: tool call %d needs id and name", i, j)
			}
		}
	case "tool":
		if t.ToolCallID == "" {
			return Turn{}, fmt.Errorf("message %d: tool result needs tool_call_id", i)
		}
		if len(t.ToolCalls) > 0 {
			return Turn{}, fmt.Errorf("message %d: tool result cannot carry tool_calls", i)
		}
	case "system", "user":
		if len(t.ToolCalls) > 0 {
			return Turn{}, fmt.Errorf("message %d: %s message cannot carry tool_calls", i, role)
		}
		if t.ToolCallID != "" {
			return Turn{}, fmt.Errorf("message %d: %s message cannot carry tool_call_id", i, role)
		}
	}
	return t, nil
}

// turnRole normalizes the role vocabulary shared by both adapters.
func turnRole(role string) (string, error) {
	switch role {
	case "", "user":
		return "user", nil
	case "system", "assistant", "tool":
		return role, nil
	default:
		return "", fmt.Errorf("unsupported role %q", role)
	}
}
