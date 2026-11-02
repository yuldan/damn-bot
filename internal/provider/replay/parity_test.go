//go:build !minimal

package replay

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/registry"
	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// EC-TCALL-011: a single chat->Responses converter must make both adapters
// agree on roles, tool calls, and tool results. This test sends one
// conversation through the openai-chat and openai-responses adapters and
// compares the two wire bodies, so neither path can diverge or drop a turn.
func TestChatResponsesParity(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if !g.Item(t, "EC-TCALL-011", "F5", "covered") {
		return
	}
	o, bodies := parityClient(t)
	msgs := parityConversation()
	for _, ref := range []string{"chat/m", "resp/m"} {
		if _, err := o.Do(t.Context(), provider.PurposeTurn, &provider.Request{
			Model: ref, Messages: msgs, MaxTokens: replayMaxTok,
		}, nil); err != nil {
			t.Fatalf("%s: Do: %v", ref, err)
		}
	}
	chatRaw, respRaw := bodies("/chat/completions"), bodies("/responses")
	if chatRaw == nil || respRaw == nil {
		t.Fatalf("missing bodies: chat=%v responses=%v", chatRaw != nil, respRaw != nil)
	}
	chat, resp := decodeReplayBody(t, chatRaw), decodeReplayBody(t, respRaw)
	checkChatParity(t, chat)
	checkResponsesParity(t, resp)
	checkCallParity(t, chat, resp)
}

// parityConversation is the multi-part conversation both adapters render.
func parityConversation() []provider.Message {
	return []provider.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "calling", ToolCalls: []provider.ToolCall{
			{ID: "call_1", Name: "read_file", ArgumentsRaw: `{"path":"a"}`},
		}},
		{Role: "tool", ToolCallID: "call_1", Content: "ok"},
	}
}

// parityClient starts one loopback server that answers each path with its
// own protocol stream and returns the recorded bodies by path.
func parityClient(t *testing.T) (*provider.OpenAI, func(string) []byte) {
	t.Helper()
	var (
		mu     sync.Mutex
		bodies = map[string][]byte{}
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "read", http.StatusBadRequest)
			return
		}
		mu.Lock()
		bodies[r.URL.Path] = body
		mu.Unlock()
		w.Header().Set("Content-Type", "text/event-stream")
		stream := chatStream("m")
		if strings.HasSuffix(r.URL.Path, "/responses") {
			stream = responsesStream("m")
		}
		if _, werr := io.WriteString(w, stream); werr != nil {
			return
		}
	}))
	t.Cleanup(srv.Close)

	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{
			{Provider: "chat", Model: "m", BaseURL: srv.URL, APIKey: "k"},
			{
				Provider: "resp", Model: "m", BaseURL: srv.URL, APIKey: "k",
				Protocols: []registry.Protocol{registry.ProtocolOpenAIResponses},
			},
		},
		UserAgent: "damnbot/replay", Wires: wireMap(),
		Wait: func(context.Context, time.Duration) error { return nil },
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	return o, func(path string) []byte {
		mu.Lock()
		defer mu.Unlock()
		return bodies[path]
	}
}

func checkChatParity(t *testing.T, chat map[string]any) {
	t.Helper()
	msgs, ok := chat["messages"].([]any)
	if !ok || len(msgs) != 4 {
		t.Fatalf("chat messages = %v, want 4", chat["messages"])
	}
	for i, role := range []string{"system", "user", "assistant", "tool"} {
		if got := textMap(t, msgs[i], "message")["role"]; got != role {
			t.Fatalf("chat message %d role = %v, want %q", i, got, role)
		}
	}
}

func checkResponsesParity(t *testing.T, resp map[string]any) {
	t.Helper()
	items, ok := resp["input"].([]any)
	if !ok || len(items) != 5 {
		t.Fatalf("responses input = %v, want 5", resp["input"])
	}
	kinds := []string{"message", "message", "message", "function_call", "function_call_output"}
	for i, kind := range kinds {
		item := textMap(t, items[i], "item")
		got := "message"
		if item["type"] != nil {
			got = textString(t, item["type"], "item type")
		}
		if got != kind {
			t.Fatalf("responses item %d type = %q, want %q", i, got, kind)
		}
	}
}

// checkCallParity compares the tool call and tool result fields both wires
// carry, including the user/assistant text.
func checkCallParity(t *testing.T, chat, resp map[string]any) {
	t.Helper()
	chatMsgs := textSlice(t, chat["messages"], "messages")
	items := textSlice(t, resp["input"], "input")
	call := textMap(t, textSlice(t, textMap(t, chatMsgs[2], "assistant")["tool_calls"], "tool_calls")[0], "chat call")
	fn := textMap(t, items[3], "function_call")
	out := textMap(t, items[4], "function_call_output")
	if call["id"] != "call_1" || fn["call_id"] != "call_1" || out["call_id"] != "call_1" {
		t.Fatalf("call ids differ: chat=%v responses=%v/%v", call["id"], fn["call_id"], out["call_id"])
	}
	if textMap(t, chatMsgs[3], "tool")["tool_call_id"] != "call_1" || out["output"] != "ok" {
		t.Fatalf("tool result differs: chat=%v responses=%v", chatMsgs[3], out)
	}
	chatText := textMap(t, chatMsgs[1], "user")["content"]
	part := textMap(t, textSlice(t, textMap(t, items[1], "user")["content"], "content")[0], "part")
	if chatText != "hi" || part["text"] != "hi" {
		t.Fatalf("user text differs: chat=%v responses=%v", chatText, part["text"])
	}
}

// TestChatResponsesRejectParity is the negative control: an unsupported role
// fails on both adapters before any request is sent, rather than being
// coerced by one of them.
func TestChatResponsesRejectParity(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, `{"error":{"message":"unexpected"}}`, http.StatusTeapot)
	}))
	t.Cleanup(srv.Close)
	n := netx.New(netx.Config{})
	t.Cleanup(n.CloseIdleConnections)
	o, err := provider.NewOpenAI(n.Client("provider", netx.Inference), &provider.Config{
		Routes: []provider.Route{
			{Provider: "chat", Model: "m", BaseURL: srv.URL, APIKey: "k"},
			{
				Provider: "resp", Model: "m", BaseURL: srv.URL, APIKey: "k",
				Protocols: []registry.Protocol{registry.ProtocolOpenAIResponses},
			},
		},
		Wires: wireMap(),
	})
	if err != nil {
		t.Fatalf("NewOpenAI: %v", err)
	}
	for _, ref := range []string{"chat/m", "resp/m"} {
		_, derr := o.Do(t.Context(), provider.PurposeTurn, &provider.Request{
			Model: ref, Messages: []provider.Message{{Role: "model", Content: "x"}},
		}, nil)
		if derr == nil || !strings.Contains(derr.Error(), `unsupported role "model"`) {
			t.Fatalf("%s: error = %v, want unsupported role", ref, derr)
		}
	}
}

func decodeReplayBody(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var body map[string]any
	if err := json.Unmarshal(raw, &body); err != nil {
		t.Fatalf("body %s: %v", raw, err)
	}
	return body
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

func textString(t *testing.T, v any, what string) string {
	t.Helper()
	s, ok := v.(string)
	if !ok {
		t.Fatalf("%s = %v, want string", what, v)
	}
	return s
}
