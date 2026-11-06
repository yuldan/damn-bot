package gwapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func toolRequest(extra string) string {
	return `{"model":"damnbot","messages":[{"role":"user","content":"read it"}],"tools":[{"type":"function","function":{"name":"read_file"}}]` + extra + `}`
}

func toolStreamUpdates() <-chan Update {
	return scripted(
		Update{Kind: UpdateToolCall, ToolCallID: "call-1", ToolName: "read_file", ToolArgs: `{"path":"a.txt"}`},
		Update{Kind: UpdateToolResult, ToolCallID: "call-1", ToolName: "read_file", Content: "contents"},
		Update{Kind: UpdateText, Text: "done"},
		Update{Kind: UpdateStop, FinishReason: "stop", Usage: &Usage{PromptTokens: 1, CompletionTokens: 1, TotalTokens: 2}},
	)
}

// TestAPIServerToolCalls maps tools onto the safe bundle, refuses
// approval-required tools before a turn, and surfaces calls exactly once.
func TestAPIServerToolCalls(t *testing.T) {
	t.Parallel()
	tb := fakeToolbox{
		{Name: "read_file", Description: "read a file", Parameters: json.RawMessage(`{"type":"object"}`)},
		{Name: "shell", Description: "run a command", Parameters: json.RawMessage(`{"type":"object"}`), ApprovalRequired: true},
	}
	eng := &fakeEngine{run: func(_ context.Context, _ *Turn) (<-chan Update, error) { return toolStreamUpdates(), nil }}
	h := newTestServer(t, eng, tb, testConfig()).Handler()
	checkApprovalRefused(t, h, eng)
	checkUnknownTool(t, h, eng)
	checkSafeTool(t, h, eng)
	checkStreamToolDelta(t, h)
	checkClientToolHistory(t, h, eng)
	checkToolChoiceNone(t, h, eng)
}

func checkApprovalRefused(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	body := `{"model":"damnbot","messages":[{"role":"user","content":"go"}],"tools":[{"type":"function","function":{"name":"shell"}}]}`
	rec := postJSON(t, h, body, authHeaders())
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", rec.Code)
	}
	if er := decodeError(t, rec); er.Error.Type != TypePermission || er.Error.Code != "approval_required" {
		t.Errorf("error = %+v", er.Error)
	}
	if eng.count() != 0 {
		t.Fatal("approval-required tool executed a turn")
	}
}

func checkUnknownTool(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	body := `{"model":"damnbot","messages":[{"role":"user","content":"go"}],"tools":[{"type":"function","function":{"name":"nuke"}}]}`
	rec := postJSON(t, h, body, authHeaders())
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", rec.Code)
	}
	if eng.count() != 0 {
		t.Fatal("unknown tool executed a turn")
	}
}

func checkSafeTool(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	rec := postJSON(t, h, toolRequest(""), authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if eng.count() != 1 || len(eng.last().Tools) != 1 || eng.last().Tools[0] != "read_file" {
		t.Fatalf("turn = %+v", eng.last())
	}
	var resp ChatResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	msg := resp.Choices[0].Message
	if msg == nil || len(msg.ToolCalls) != 1 {
		t.Fatalf("message = %+v", msg)
	}
	tc := msg.ToolCalls[0]
	if tc.ID != "call-1" || tc.Type != "function" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"a.txt"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if msg.Content != "done" {
		t.Errorf("content = %q", msg.Content)
	}
}

func checkStreamToolDelta(t *testing.T, h http.Handler) {
	t.Helper()
	body := postJSON(t, h, toolRequest(`,"stream":true`), authHeaders()).Body.String()
	want := `"tool_calls":[{"index":0,"id":"call-1","type":"function","function":{"name":"read_file","arguments":"{\"path\":\"a.txt\"}"}}]`
	if !strings.Contains(body, want) {
		t.Errorf("tool delta missing: %s", body)
	}
	if strings.Count(body, `"id":"call-1"`) != 1 {
		t.Errorf("tool call streamed more than once: %s", body)
	}
}

func checkClientToolHistory(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	before := eng.count()
	body := `{"model":"damnbot","messages":[` +
		`{"role":"assistant","content":null,"tool_calls":[{"id":"call-9","type":"function","function":{"name":"read_file","arguments":"{}"}}]},` +
		`{"role":"tool","tool_call_id":"call-9","content":"result"}]}`
	rec := postJSON(t, h, body, authHeaders())
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rec.Code, rec.Body.String())
	}
	if eng.count() != before+1 {
		t.Fatalf("engine runs = %d, want %d", eng.count(), before+1)
	}
	if turn := eng.last(); turn.Messages[1].Role != "tool" || turn.Messages[1].ToolCallID != "call-9" {
		t.Errorf("tool history not forwarded: %+v", turn.Messages)
	}
}

func checkToolChoiceNone(t *testing.T, h http.Handler, eng *fakeEngine) {
	t.Helper()
	if rec := postJSON(t, h, toolRequest(`,"tool_choice":"none"`), authHeaders()); rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	if len(eng.last().Tools) != 0 {
		t.Errorf("tools = %v, want none", eng.last().Tools)
	}
}
