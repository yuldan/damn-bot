package assemble

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"
)

func TestShellSchemaVariant(t *testing.T) {
	t.Parallel()
	tools := fixtureTools()
	f1, err := FilterTools(PhaseF1, tools)
	if err != nil {
		t.Fatalf("FilterTools(F1): %v", err)
	}
	f5, err := FilterTools(PhaseF5, tools)
	if err != nil {
		t.Fatalf("FilterTools(F5): %v", err)
	}
	shellF1 := findTool(t, f1, "shell")
	shellF5 := findTool(t, f5, "shell")
	if strings.Contains(strings.ToLower(shellF1.Description), "background") {
		t.Errorf("F1 shell description mentions background: %q", shellF1.Description)
	}
	if strings.Contains(string(shellF1.Parameters), "background") {
		t.Errorf("F1 shell parameters still contain background: %s", shellF1.Parameters)
	}
	if !strings.Contains(strings.ToLower(shellF5.Description), "background") {
		t.Errorf("F5 shell description lost background: %q", shellF5.Description)
	}
	if !strings.Contains(string(shellF5.Parameters), `"background"`) {
		t.Errorf("F5 shell parameters lost background: %s", shellF5.Parameters)
	}
	again, err := FilterTools(PhaseF1, f1)
	if err != nil {
		t.Fatalf("FilterTools(F1) idempotent: %v", err)
	}
	if got := findTool(t, again, "shell"); string(got.Parameters) != string(shellF1.Parameters) {
		t.Errorf("F1 filter is not idempotent:\n%s\n%s", got.Parameters, shellF1.Parameters)
	}
	var schema map[string]any
	if err := json.Unmarshal(shellF1.Parameters, &schema); err != nil {
		t.Fatalf("F1 shell schema JSON: %v", err)
	}
}

func TestFilterToolsSortsAndValidates(t *testing.T) {
	t.Parallel()
	tools, err := FilterTools(PhaseF1, []Tool{
		{Name: "z", Parameters: json.RawMessage(`{}`)},
		{Name: "a", Parameters: json.RawMessage(`{}`)},
	})
	if err != nil {
		t.Fatalf("FilterTools: %v", err)
	}
	if tools[0].Name != "a" || tools[1].Name != "z" {
		t.Errorf("tools not sorted: %+v", tools)
	}
	if _, err := FilterTools(PhaseF1, []Tool{{Name: "bad", Parameters: json.RawMessage(`{`)}}); err == nil {
		t.Error("invalid schema accepted")
	}
	if _, err := FilterTools(Phase(99), nil); err == nil {
		t.Error("unknown phase accepted")
	}
	if _, err := FilterTools(PhaseF1, []Tool{{Parameters: json.RawMessage(`{}`)}}); err == nil {
		t.Error("empty tool name accepted")
	}
}

func TestMarshalToolsDeterministic(t *testing.T) {
	t.Parallel()
	tools, err := FilterTools(PhaseF1, fixtureTools())
	if err != nil {
		t.Fatalf("FilterTools: %v", err)
	}
	a, err := MarshalTools(tools)
	if err != nil {
		t.Fatalf("MarshalTools: %v", err)
	}
	b, err := MarshalTools(tools)
	if err != nil {
		t.Fatalf("MarshalTools: %v", err)
	}
	if !bytes.Equal(a, b) {
		t.Error("MarshalTools is not deterministic")
	}
	if strings.Contains(string(a), `\u003c`) {
		t.Error("MarshalTools HTML-escaped content")
	}
}

func findTool(t *testing.T, tools []Tool, name string) Tool {
	t.Helper()
	for _, tool := range tools {
		if tool.Name == name {
			return tool
		}
	}
	t.Fatalf("tool %q not found", name)
	return Tool{}
}

// TestPromptNoUnavailableTools: the stable tier never names a tool that is not
// in the provided array or the session catalog (ADR-067 butir 1).
func TestPromptNoUnavailableTools(t *testing.T) {
	t.Parallel()
	core := fixtureTools()
	coreNames := make([]string, len(core))
	for i, tool := range core {
		coreNames[i] = tool.Name
	}
	known := []string{
		"read_file", "write_file", "edit_file", "glob", "grep", "shell", "web_fetch", "todo",
		"tool_search", "tool_invoke", "repo_map", "recall", "execute_code", "delegate_task",
		"browser_navigate", "send_message", "ask_user",
	}
	tests := []struct {
		name    string
		opts    StableOptions
		tools   []string
		catalog []string
	}{
		{"f1", StableOptions{}, coreNames, nil},
		{"deferred", StableOptions{Deferred: true}, append(append([]string{}, coreNames...), "tool_search", "tool_invoke"), nil},
		{"codeintel", StableOptions{Codeintel: true}, append(append([]string{}, coreNames...), "repo_map"), nil},
	}
	for _, tc := range tests {
		allowed := map[string]bool{}
		for _, name := range append(tc.tools, tc.catalog...) {
			allowed[name] = true
		}
		for _, token := range words(Stable(tc.opts)) {
			if slices.Contains(known, token) && !allowed[token] {
				t.Errorf("%s: stable tier names unavailable tool %q", tc.name, token)
			}
		}
	}
}

func words(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool {
		isWord := r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_'
		return !isWord
	})
}
