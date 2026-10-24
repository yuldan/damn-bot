package config

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/toolset"
)

// TestConfigToolsets: the [toolsets] section decodes and validates.
func TestConfigToolsets(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, strings.Join([]string{
		`[provider]`, `model = "openai/gpt-4.1"`,
		"[toolsets]",
		`active = ["coding"]`,
		`disabled = ["terminal"]`,
		"[toolsets.platform.windows]",
		`active = ["safe"]`,
		"[toolsets.custom.review]",
		`description = "review only"`,
		`tools = ["read_file", "grep"]`,
		`includes = ["web"]`,
	}, "\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	ts := cfg.Toolsets
	if len(ts.Active) != 1 || ts.Active[0] != "coding" || len(ts.Disabled) != 1 || ts.Disabled[0] != "terminal" {
		t.Fatalf("active/disabled = %v/%v", ts.Active, ts.Disabled)
	}
	if got := ts.Platform["windows"].Active; len(got) != 1 || got[0] != "safe" {
		t.Fatalf("windows active = %v", got)
	}
	if d := ts.Custom["review"]; !strings.Contains(d.Description, "review") || len(d.Tools) != 2 || len(d.Includes) != 1 {
		t.Fatalf("custom review = %+v", d)
	}
}

// TestConfigToolsetsFailClosed: a tool name that no toolset declares is a
// config error naming the key, so a typo never silently hides a tool.
func TestConfigToolsetsFailClosed(t *testing.T) {
	t.Parallel()
	_, err := parse(t, strings.Join([]string{
		`[provider]`, `model = "openai/gpt-4.1"`,
		"[toolsets.custom.mine]",
		`tools = ["read_file", "typo_tool"]`,
	}, "\n"))
	if err == nil || !strings.Contains(err.Error(), "toolsets") || !strings.Contains(err.Error(), "typo_tool") {
		t.Fatalf("err = %v, want a toolsets/typo_tool error", err)
	}
}

// TestConfigToolsetsUnknownName: active/disabled names must resolve.
func TestConfigToolsetsUnknownName(t *testing.T) {
	t.Parallel()
	_, err := parse(t, strings.Join([]string{
		`[provider]`, `model = "openai/gpt-4.1"`,
		"[toolsets]",
		`active = ["nope"]`,
	}, "\n"))
	if err == nil || !strings.Contains(err.Error(), "nope") {
		t.Fatalf("err = %v, want unknown toolset nope", err)
	}
}

// TestCloneToolsetsIndependent: Clone deep-copies every toolsets slice/map.
func TestCloneToolsetsIndependent(t *testing.T) {
	t.Parallel()
	c := Default()
	c.Toolsets = toolset.Config{
		Active:   []string{"coding"},
		Custom:   map[string]toolset.Definition{"review": {Tools: []string{"read_file"}}},
		Platform: map[string]toolset.Override{"linux": {Active: []string{"safe"}}},
	}
	clone := c.Clone()
	clone.Toolsets.Active[0] = "safe"
	clone.Toolsets.Custom["review"].Tools[0] = "shell"
	clone.Toolsets.Platform["linux"] = toolset.Override{Active: []string{"safe"}}
	if c.Toolsets.Active[0] == "safe" || c.Toolsets.Custom["review"].Tools[0] == "shell" {
		t.Fatal("Clone shares toolsets slices with the original")
	}
}
