package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunHeadlessFakeProvider is the F1 `damnbot run` smoke: one loop turn
// against the loopback fake provider, JSON result on stdout, exit 0.
func TestRunHeadlessFakeProvider(t *testing.T) {
	t.Parallel()
	srv := providertest.New(t, nil, providertest.Text("hello from the fake provider"))
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "say hello",
		"--model", "openai/gpt-fake",
		"--base-url", srv.URL,
		"--data-dir", t.TempDir(),
		"--output", "json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	var got struct {
		Schema     string `json:"schema"`
		StopReason string `json:"stop_reason"`
		StopDetail string `json:"stop_detail"`
		Answer     string `json:"answer"`
	}
	if err := json.Unmarshal(out.Bytes(), &got); err != nil {
		t.Fatalf("json output: %v\n%s", err, out.String())
	}
	if got.Schema != "damnbot.turn_result/v1" || got.StopReason != "end_turn" || got.StopDetail != "completed" {
		t.Errorf("result = %+v", got)
	}
	if !strings.Contains(got.Answer, "hello from the fake provider") {
		t.Errorf("answer = %q", got.Answer)
	}
	if len(srv.Requests()) != 1 {
		t.Errorf("provider calls = %d, want 1", len(srv.Requests()))
	}
}

// TestRunHeadlessToolCall exercises the full tool pipeline: the fake model
// calls todo, the dispatcher executes it, and the second reply completes.
func TestRunHeadlessToolCall(t *testing.T) {
	t.Parallel()
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"todo","arguments":"{\"items\":[{\"id\":\"1\",\"text\":\"smoke\",\"status\":\"pending\"}]}"}}]}}]}`
	end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	usage := `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10}}`
	srv := providertest.New(t, nil,
		providertest.Reply{Frames: []string{call, end, usage, "[DONE]"}},
		providertest.Text("done after tool"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "use todo", "--model", "openai/gpt-fake",
		"--base-url", srv.URL, "--data-dir", t.TempDir(), "--output", "json",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	if !strings.Contains(out.String(), "done after tool") {
		t.Errorf("stdout = %s", out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	var second struct {
		Messages []struct {
			Role       string `json:"role"`
			Content    string `json:"content"`
			ToolCallID string `json:"tool_call_id"`
			ToolCalls  []struct {
				ID string `json:"id"`
			} `json:"tool_calls"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqs[1], &second); err != nil {
		t.Fatalf("second request: %v", err)
	}
	var assistantCalls, toolMsgs int
	var callID string
	for _, m := range second.Messages {
		switch m.Role {
		case "assistant":
			assistantCalls += len(m.ToolCalls)
			if len(m.ToolCalls) > 0 {
				callID = m.ToolCalls[0].ID
			}
		case "tool":
			toolMsgs++
			if m.ToolCallID == "" {
				t.Errorf("tool message without tool_call_id: %+v", m)
			}
		}
	}
	if assistantCalls != 1 || toolMsgs != 1 {
		t.Errorf("assistant tool_calls=%d tool messages=%d, want 1/1", assistantCalls, toolMsgs)
	}
	if callID == "" {
		t.Error("assistant tool call id missing")
	}
}

// TestRunHeadlessParseErrors covers A4: validation failures print their
// message; failures the flag package already wrote (unknown flag, -h) are
// not printed twice.
func TestRunHeadlessParseErrors(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		args []string
		want string
	}{
		{"missing prompt", []string{"run"}, "run: prompt required"},
		{"invalid output", []string{"run", "-p", "hi", "--output", "bogus"}, `run: invalid --output "bogus"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var out, errb bytes.Buffer
			if code := run(tc.args, &out, &errb); code != 2 {
				t.Fatalf("exit = %d, want 2", code)
			}
			if out.Len() != 0 {
				t.Errorf("stdout = %q, want empty", out.String())
			}
			if !strings.Contains(errb.String(), tc.want) {
				t.Errorf("stderr = %q, want %q", errb.String(), tc.want)
			}
		})
	}
	t.Run("unknown flag keeps the flag package output", func(t *testing.T) {
		t.Parallel()
		var out, errb bytes.Buffer
		if code := run([]string{"run", "-nope"}, &out, &errb); code != 2 {
			t.Fatalf("exit = %d, want 2", code)
		}
		if n := strings.Count(errb.String(), "flag provided but not defined: -nope"); n != 1 {
			t.Errorf("unknown flag printed %d times: %q", n, errb.String())
		}
		if !strings.Contains(errb.String(), "Usage of run:") {
			t.Errorf("stderr lacks usage: %q", errb.String())
		}
	})
}

// TestRunHeadlessShellSession covers C15: the headless root wires
// exec.Sessions, so a session call executes instead of module_unavailable.
func TestRunHeadlessShellSession(t *testing.T) {
	t.Parallel()
	call := `{"choices":[{"index":0,"delta":{"tool_calls":[{"index":0,"id":"c1","function":{"name":"shell","arguments":"{\"command\":\"echo session-ok\",\"session\":\"s1\"}"}}]}}]}`
	end := `{"choices":[{"index":0,"delta":{},"finish_reason":"tool_calls"}]}`
	usage := `{"choices":[],"usage":{"prompt_tokens":50,"completion_tokens":10}}`
	srv := providertest.New(t, nil,
		providertest.Reply{Frames: []string{call, end, usage, "[DONE]"}},
		providertest.Text("done after shell"),
	)
	var out, errb bytes.Buffer
	code := run([]string{
		"run", "-p", "use the shell session", "--model", "openai/gpt-fake",
		"--base-url", srv.URL, "--data-dir", t.TempDir(), "--output", "json",
		"--approval", "auto", "--unsafe-auto-shell",
	}, &out, &errb)
	if code != 0 {
		t.Fatalf("exit = %d, stderr = %s, stdout = %s", code, errb.String(), out.String())
	}
	reqs := srv.Requests()
	if len(reqs) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(reqs))
	}
	var second struct {
		Messages []struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(reqs[1], &second); err != nil {
		t.Fatalf("second request: %v", err)
	}
	var toolContent string
	for _, m := range second.Messages {
		if m.Role == "tool" {
			toolContent = m.Content
		}
	}
	if !strings.Contains(toolContent, "session-ok") {
		t.Errorf("tool result = %q, want the session output", toolContent)
	}
	if strings.Contains(toolContent, "module_unavailable") {
		t.Errorf("tool result = %q, sessions are not wired", toolContent)
	}
}
