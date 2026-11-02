package responses_test

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider"
)

// TestConversationItems: the single chat-to-Responses converter maps system,
// user, assistant (text + tool call), and tool result to the Responses item
// sequence without losing a field.
func TestConversationItems(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	req := &provider.Request{
		Model: "openai/m",
		Messages: []provider.Message{
			{Role: "system", Content: "sys"},
			{Role: "user", Content: "hi"},
			{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
				{ID: "call_1", Name: "read_file", ArgumentsRaw: `{"path":"a"}`},
			}},
			{Role: "tool", ToolCallID: "call_1", Content: "ok"},
		},
	}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _ := f.requests()
	items, ok := decodeBody(t, reqs[0])["input"].([]any)
	if !ok || len(items) != 5 {
		t.Fatalf("input = %v, want 5 items", decodeBody(t, reqs[0])["input"])
	}
	want := []struct{ kind, role, text string }{
		{"message", "system", "sys"},
		{"message", "user", "hi"},
		{"message", "assistant", "calling"},
		{"function_call", "", ""},
		{"function_call_output", "", ""},
	}
	for i, w := range want {
		item := textMap(t, items[i], "item")
		if w.kind == "message" {
			if item["type"] != nil || item["role"] != w.role {
				t.Fatalf("item %d = %v, want message role %q", i, item, w.role)
			}
			part := textMap(t, textSlice(t, item["content"], "content")[0], "part")
			if part["text"] != w.text {
				t.Fatalf("item %d text = %v, want %q", i, part["text"], w.text)
			}
			continue
		}
		if item["type"] != w.kind {
			t.Fatalf("item %d = %v, want type %q", i, item, w.kind)
		}
	}
	call := textMap(t, items[3], "function_call")
	out := textMap(t, items[4], "function_call_output")
	if call["call_id"] != "call_1" || call["name"] != "read_file" || call["arguments"] != `{"path":"a"}` {
		t.Fatalf("function_call = %v", call)
	}
	if out["call_id"] != "call_1" || out["output"] != "ok" {
		t.Fatalf("function_call_output = %v", out)
	}
}

// TestConversationRejects: an unsupported role or field fails before any
// request is sent, so the adapter cannot silently drop or coerce it.
func TestConversationRejects(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		msg  provider.Message
		want string
	}{
		{"unknown role", provider.Message{Role: "model", Content: "x"}, `unsupported role "model"`},
		{"assistant tool_call_id", provider.Message{Role: "assistant", Content: "x", ToolCallID: "c"}, "assistant cannot carry tool_call_id"},
		{"tool without call id", provider.Message{Role: "tool", Content: "x"}, "tool result needs tool_call_id"},
		{"system with calls", provider.Message{Role: "system", Content: "x", ToolCalls: []provider.ToolCall{{ID: "c", Name: "n"}}}, "system message cannot carry tool_calls"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()
			f := newFake(t, reply{raw: fixture(t, "text.sse")})
			cl := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
			_, err := cl.Do(t.Context(), provider.PurposeTurn, &provider.Request{
				Model: "openai/m", Messages: []provider.Message{c.msg},
			}, nil)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("error = %v, want %q", err, c.want)
			}
			if reqs, _ := f.requests(); len(reqs) != 0 {
				t.Fatalf("invalid conversation reached the wire: %s", reqs[0])
			}
		})
	}
}

// TestEmptyAssistantNotDropped: an assistant turn without text or calls still
// becomes an item (empty output_text), so no field disappears silently.
func TestEmptyAssistantNotDropped(t *testing.T) {
	t.Parallel()
	f := newFake(t, reply{raw: fixture(t, "text.sse")})
	c := newClient(t, []provider.Route{route("m", f.URL())}, nil, 0)
	req := &provider.Request{Model: "openai/m", Messages: []provider.Message{{Role: "assistant"}}}
	if _, err := c.Do(t.Context(), provider.PurposeTurn, req, nil); err != nil {
		t.Fatalf("Do: %v", err)
	}
	reqs, _ := f.requests()
	items, ok := decodeBody(t, reqs[0])["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %v, want one item", decodeBody(t, reqs[0])["input"])
	}
	part := textMap(t, textSlice(t, textMap(t, items[0], "item")["content"], "content")[0], "part")
	if part["type"] != "output_text" || part["text"] != "" {
		t.Fatalf("empty assistant = %v", part)
	}
}

func textMap(t *testing.T, v any, what string) map[string]any {
	t.Helper()
	m, ok := v.(map[string]any)
	if !ok {
		t.Fatalf("%s = %v, want object", what, v)
	}
	return m
}

func textSlice(t *testing.T, v any, what string) []any {
	t.Helper()
	s, ok := v.([]any)
	if !ok {
		t.Fatalf("%s = %v, want array", what, v)
	}
	return s
}
