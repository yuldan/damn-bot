package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
)

// isolatePromptHome points every config/data lookup at a temp tree so the
// measurement never reads the developer's profile.
func isolatePromptHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("APPDATA", filepath.Join(home, "config"))
	t.Setenv("LOCALAPPDATA", filepath.Join(home, "data"))
}

func TestPromptSizeRequiresModel(t *testing.T) {
	isolatePromptHome(t)
	code, _, errOut := runArgs(t, "prompt-size")
	if code != 1 || !strings.Contains(errOut, "--model") {
		t.Fatalf("prompt-size without model = %d %q", code, errOut)
	}
	if code, _, _ := runArgs(t, "prompt-size", "extra"); code != 2 {
		t.Fatalf("extra arg = %d", code)
	}
}

// TestPromptSizeCLI measures the real run wiring: the stable prompt and every
// declared tool schema, without touching the operator's data dir. This is the
// named evidence for CLI-072/OPS-017.
func TestPromptSizeCLI(t *testing.T) {
	isolatePromptHome(t)
	code, out, errOut := runArgs(t, "prompt-size", "--model", "openai/gpt-5")
	if code != 0 {
		t.Fatalf("prompt-size = %d %q", code, errOut)
	}
	for _, want := range []string{"system prompt:", "tool schemas:", "read_file", "total:"} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q: %q", want, out)
		}
	}
	code, out, errOut = runArgs(t, "prompt-size", "--model", "openai/gpt-5", "--json")
	if code != 0 {
		t.Fatalf("prompt-size --json = %d %q", code, errOut)
	}
	var rep promptSizeReport
	if err := json.Unmarshal([]byte(out), &rep); err != nil {
		t.Fatalf("json: %v (%q)", err, out)
	}
	if rep.Model != "openai/gpt-5" || rep.SystemBytes <= 0 || rep.StableBytes <= 0 || rep.ToolsBytes <= 0 {
		t.Fatalf("report = %+v", rep)
	}
	if rep.TotalBytes != rep.SystemBytes+rep.ToolsBytes {
		t.Fatalf("total = %d, want %d", rep.TotalBytes, rep.SystemBytes+rep.ToolsBytes)
	}
	if len(rep.Tools) == 0 {
		t.Fatal("no tool schemas measured")
	}
	names := map[string]bool{}
	for _, tool := range rep.Tools {
		if tool.Bytes <= 0 {
			t.Errorf("%s: bytes = %d", tool.Name, tool.Bytes)
		}
		names[tool.Name] = true
	}
	if !names["read_file"] || !names["shell"] {
		t.Fatalf("tool set = %v", names)
	}
}
