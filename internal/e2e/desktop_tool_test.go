package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF8DesktopToolSurfaceBinary: the real binary exposes the desktop_ui
// catalog only when the toolset is selected, and a terminal-only `run` turn
// is denied before any GUI action (no surface is attached).
func TestF8DesktopToolSurfaceBinary(t *testing.T) {
	e := New(t,
		ToolCallReply("s1", "tool_search", `{"query":"annotate_preview"}`),
		ToolCallReply("i1", "tool_invoke", `{"name":"annotate_preview","arguments":{"selector":"#x","note":"y"}}`),
		providertest.Text("done"),
	)
	if err := os.WriteFile(e.cfg, []byte("[toolsets]\nactive = [\"desktop_ui\"]\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	e.Run("check the desktop tools").RequireOK(t)
	if !e.Contains(t, 0, `"tool_search"`) || !e.Contains(t, 0, `"tool_invoke"`) {
		t.Fatalf("deferred meta-tools missing from the desktop_ui payload: %s", e.RequestText(t, 0))
	}
	if !e.Contains(t, 1, "annotate_preview") {
		t.Fatalf("tool_search result lacks the schema: %s", e.RequestText(t, 1))
	}
	reqs := e.Requests()
	last := string(reqs[len(reqs)-1])
	if !strings.Contains(last, "denied") {
		t.Fatalf("non-GUI annotation was not denied:\n%.1600s", last)
	}
}
