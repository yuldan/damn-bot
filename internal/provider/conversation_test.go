package provider_test

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
)

// TestConversationTurns: the shared converter preserves every role and
// field both wire adapters render.
func TestConversationTurns(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "", Content: "empty role"},
		{Role: "assistant", Content: "thinking", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "read_file", ArgumentsRaw: `{"path":"a"}`},
			{ID: "call_2", Name: "grep", ArgumentsRaw: `{"q":"x"}`},
		}},
		{Role: "tool", Content: "result", ToolCallID: "call_1"},
	}
	turns, err := provider.Conversation(msgs)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(turns) != len(msgs) {
		t.Fatalf("%d turns, want %d", len(turns), len(msgs))
	}
	wantRoles := []string{"system", "user", "user", "assistant", "tool"}
	for i, want := range wantRoles {
		if turns[i].Role != want {
			t.Fatalf("turn %d role = %q, want %q", i, turns[i].Role, want)
		}
		if turns[i].Message != i {
			t.Fatalf("turn %d message index = %d", i, turns[i].Message)
		}
	}
	if turns[1].Content != "hi" || turns[2].Content != "empty role" {
		t.Fatalf("content = %q/%q", turns[1].Content, turns[2].Content)
	}
	if len(turns[3].ToolCalls) != 2 || turns[3].ToolCalls[1].ID != "call_2" || turns[3].Content != "thinking" {
		t.Fatalf("assistant multi-part = %+v", turns[3])
	}
	if turns[4].ToolCallID != "call_1" || turns[4].Content != "result" {
		t.Fatalf("tool result = %+v", turns[4])
	}
}

// TestConversationRejects: an unsupported role or a field the role cannot
// carry is an explicit error, never silently coerced or dropped.
func TestConversationRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  provider.Message
		want string
	}{
		{"unknown role", provider.Message{Role: "model"}, `unsupported role "model"`},
		{"assistant tool_call_id", provider.Message{Role: "assistant", ToolCallID: "c"}, "assistant cannot carry tool_call_id"},
		{"assistant call without id", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{Name: "n"}}}, "needs id and name"},
		{"assistant call without name", provider.Message{Role: "assistant", ToolCalls: []provider.ToolCall{{ID: "c"}}}, "needs id and name"},
		{"tool without call id", provider.Message{Role: "tool", Content: "x"}, "tool result needs tool_call_id"},
		{"tool with calls", provider.Message{Role: "tool", ToolCallID: "c", ToolCalls: []provider.ToolCall{{ID: "c", Name: "n"}}}, "tool result cannot carry tool_calls"},
		{"system with calls", provider.Message{Role: "system", ToolCalls: []provider.ToolCall{{ID: "c", Name: "n"}}}, "system message cannot carry tool_calls"},
		{"user with call id", provider.Message{Role: "user", ToolCallID: "c"}, "user message cannot carry tool_call_id"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			_, err := provider.Conversation([]provider.Message{c.msg})
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
		})
	}
}

// TestConversationNegativeControl: a valid multi-part conversation yields
// exactly one turn per message with no field lost.
func TestConversationNegativeControl(t *testing.T) {
	t.Parallel()
	msgs := []provider.Message{
		{Role: "user", Content: ""},
		{Role: "assistant", Content: "", ToolCalls: []provider.ToolCall{{ID: "c", Name: "n", ArgumentsRaw: "{}"}}},
		{Role: "tool", Content: "", ToolCallID: "c"},
	}
	turns, err := provider.Conversation(msgs)
	if err != nil {
		t.Fatalf("Conversation: %v", err)
	}
	if len(turns) != 3 || turns[0].Content != "" || len(turns[1].ToolCalls) != 1 || turns[2].ToolCallID != "c" {
		t.Fatalf("turns = %+v", turns)
	}
}
