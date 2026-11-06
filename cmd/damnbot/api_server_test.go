//go:build goolm && !minimal

package main

import (
	"testing"

	"github.com/damnbot/damnbot/internal/gwapi"
	"github.com/damnbot/damnbot/internal/loop/event"
	"github.com/damnbot/damnbot/internal/redact"
)

// TestAPIServerUpdateMapping pins the loop→wire projection.
func TestAPIServerUpdateMapping(t *testing.T) {
	t.Parallel()
	red := redact.New(redact.Options{})
	cases := []struct {
		in   event.Update
		kind gwapi.UpdateKind
		text string
	}{
		{event.Update{Kind: event.UpdateTextDelta, Text: "hi"}, gwapi.UpdateText, "hi"},
		{event.Update{Kind: event.UpdateToolCallEnd, ToolCallID: "c1", ToolName: "read_file", Text: "{}"}, gwapi.UpdateToolCall, ""},
		{event.Update{Kind: event.UpdateToolResult, ToolCallID: "c1", ToolName: "read_file", Text: "body"}, gwapi.UpdateToolResult, "body"},
	}
	for _, tc := range cases {
		out, ok := apiUpdate(&tc.in, red)
		if !ok || out.Kind != tc.kind || (tc.text != "" && out.Content != tc.text && out.Text != tc.text) {
			t.Errorf("apiUpdate(%+v) = %+v ok=%t", tc.in, out, ok)
		}
	}
	finish, errText := apiStop(&event.Update{Stop: event.StopMaxTokens}, red)
	if finish != "length" || errText != "" {
		t.Errorf("max tokens = %q %q", finish, errText)
	}
	finish, _ = apiStop(&event.Update{Stop: event.StopEndTurn}, red)
	if finish != "stop" {
		t.Errorf("end turn = %q", finish)
	}
}

// TestAPIServerToolboxIsReadOnly proves every mutating tool is refused before
// a turn and that the bundle is the safe composite (no clarify).
func TestAPIServerToolboxIsReadOnly(t *testing.T) {
	t.Parallel()
	tb := apiToolbox()
	byName := map[string]gwapi.ToolSpec{}
	for _, spec := range tb.Tools() {
		byName[spec.Name] = spec
	}
	if _, ok := byName["clarify"]; ok {
		t.Error("clarify must be excluded from the api-server bundle")
	}
	if spec, ok := byName["read_file"]; !ok || spec.ApprovalRequired {
		t.Errorf("read_file = %+v", spec)
	}
	for _, name := range []string{"shell", "write_file", "edit_file", "image_generate", "cronjob_manage"} {
		if spec, ok := byName[name]; !ok || !spec.ApprovalRequired {
			t.Errorf("%s = %+v, want approval-required", name, spec)
		}
	}
}

// TestAPIServerLastUserText keeps the prompt projection deterministic.
func TestAPIServerLastUserText(t *testing.T) {
	t.Parallel()
	msgs := []gwapi.Message{{Role: "user", Content: "first"}, {Role: "assistant", Content: "no"}, {Role: "user", Content: "last"}}
	if got := apiLastUserText(msgs); got != "last" {
		t.Errorf("apiLastUserText = %q", got)
	}
	if got := apiLastUserText(nil); got != "" {
		t.Errorf("empty = %q", got)
	}
}
