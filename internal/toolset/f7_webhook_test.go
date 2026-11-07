package toolset

import (
	"runtime"
	"slices"
	"testing"
)

// TestF7TSET061WebhookToolset (TSET-061): the webhook bundle is a composite
// (no registry of its own) of the web, vision and clarify bundles, it never
// exposes a shell/file tool, and it coexists with hermes-api-server.
func TestF7TSET061WebhookToolset(t *testing.T) {
	t.Parallel()
	testWebhookToolset(t)
}

// TestF7TSET061WebhookBundle is the ledger name of the same proof.
func TestF7TSET061WebhookBundle(t *testing.T) {
	t.Parallel()
	testWebhookToolset(t)
}

func testWebhookToolset(t *testing.T) {
	def, ok := lookupDef("hermes-webhook")
	if !ok {
		t.Fatal("hermes-webhook is not a built-in toolset")
	}
	if len(def.Tools) != 0 || !slices.Equal(def.Includes, []string{"web", "vision", "clarify"}) {
		t.Fatalf("webhook bundle = tools %v includes %v", def.Tools, def.Includes)
	}
	if _, ok := lookupDef("hermes-api-server"); !ok {
		t.Fatal("hermes-api-server disappeared: the two bundles must coexist")
	}
	declared := DeclaredTools()
	for _, part := range def.Includes {
		partDef, ok := lookupDef(part)
		if !ok {
			t.Fatalf("include %q missing", part)
		}
		for _, name := range partDef.Tools {
			if !declared[name] {
				t.Errorf("tool %q is not declared by a built-in", name)
			}
		}
	}

	checkWebhookSelection(t)
	checkWebhookCoexist(t)
}

func checkWebhookSelection(t *testing.T) {
	t.Helper()
	sel, err := Resolve(Config{Active: []string{"hermes-webhook"}}, runtime.GOOS, DeclaredTools())
	if err != nil {
		t.Fatal(err)
	}
	if len(sel.Unavailable) != 0 || len(sel.Tools) == 0 {
		t.Fatalf("selection = %+v", sel)
	}
	for _, unsafe := range []string{"shell", "write_file", "edit_file", "read_file", "glob", "grep"} {
		if sel.Allowed[unsafe] {
			t.Errorf("webhook bundle exposes %q", unsafe)
		}
	}
}

func checkWebhookCoexist(t *testing.T) {
	t.Helper()
	both, err := Resolve(Config{Active: []string{"hermes-webhook", "hermes-api-server"}}, runtime.GOOS, DeclaredTools())
	if err != nil {
		t.Fatalf("both bundles: %v", err)
	}
	if !both.Allowed["web_search"] || !both.Allowed["vision_analyze"] {
		t.Fatalf("union lost a webhook tool: %v", both.Tools)
	}
}
