package toolset_test

import (
	"runtime"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/toolset"
)

// TestF7TSET061WebhookToolset (TSET-061): the webhook bundle is composite and
// exposes exactly web, vision and clarify; no shell or file tool can enter it.
func TestF7TSET061WebhookToolset(t *testing.T) {
	t.Parallel()
	var def toolset.Definition
	for _, candidate := range toolset.Builtin() {
		if candidate.Name == "hermes-webhook" {
			def = candidate
		}
	}
	if def.Name == "" {
		t.Fatal("hermes-webhook bundle is missing")
	}
	if len(def.Tools) != 0 || !slices.Equal(def.Includes, []string{"web", "vision", "clarify"}) {
		t.Fatalf("hermes-webhook = %+v, want includes web/vision/clarify", def)
	}
	available := map[string]bool{
		"web_search": true, "web_fetch": true, "vision_analyze": true, "clarify": true,
		"shell": true, "read_file": true, "write_file": true, "edit_file": true,
	}
	sel, err := toolset.Resolve(toolset.Config{Active: []string{"hermes-webhook"}}, runtime.GOOS, available)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"clarify", "vision_analyze", "web_fetch", "web_search"}
	if !slices.Equal(sel.Tools, want) {
		t.Fatalf("resolved tools = %v, want %v", sel.Tools, want)
	}
	for _, forbidden := range []string{"shell", "read_file", "write_file", "edit_file"} {
		if sel.Allowed[forbidden] {
			t.Fatalf("webhook bundle exposes %s", forbidden)
		}
	}
}
