package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// writeTool is one tool whose server annotations claim read-only. The claim
// must never lower the gate (GAP-SEC-03).
func writeTool(i int) Tool {
	return Tool{
		Name:        fmt.Sprintf("write_%02d", i),
		Description: "write something",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}},"additionalProperties":false}`),
		Annotations: json.RawMessage(`{"readOnlyHint":true,"destructiveHint":false}`),
	}
}

// TestGAPF5_SEC03 (SPEC §22.1): a server without trust is untrusted; every
// non-read tool asks, even when the server claims readOnlyHint. A per-tool
// read_only allowlist is the only way to skip the prompt, and a headless host
// (deny-default approvals) denies every write tool before dispatch.
func TestGAPF5_SEC03(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	tools := make([]Tool, 0, 11)
	for i := range 10 {
		tools = append(tools, writeTool(i))
	}
	read := Tool{Name: "read_me", Description: "read", InputSchema: json.RawMessage(`{"type":"object"}`)}
	tools = append(tools, read)
	srv.pages = [][]Tool{tools}

	approver := &fakeApprover{answer: host.ApprovalAllowOnce}
	client, _ := newStdioClient(t, srv, Server{ReadOnly: map[string]bool{"read_me": true}},
		host.Capabilities{Approvals: approver})
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("ListTools: %v", err)
	}
	for i := range 10 {
		name := fmt.Sprintf("write_%02d", i)
		if _, err := client.CallTool(context.Background(), name, json.RawMessage(`{"text":"x"}`), name); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if _, _, calls := srv.counts(); calls != 10 {
		t.Fatalf("server calls = %d, want 10", calls)
	}
	approver.mu.Lock()
	prompts, last := approver.calls, approver.last
	approver.mu.Unlock()
	if prompts != 10 || last == nil || last.Effect != host.EffectNonIdempotent || last.Class != "mcp" || last.Preview == "" {
		t.Fatalf("prompts = %d, last = %+v", prompts, last)
	}
	// The read_only allowlist is the only exemption; it never reaches the
	// approver and still dispatches.
	if _, err := client.CallTool(context.Background(), "read_me", json.RawMessage(`{}`), "read"); err != nil {
		t.Fatalf("read_me: %v", err)
	}
	approver.mu.Lock()
	if approver.calls != 10 {
		t.Errorf("read_only tool prompted: %d approvals", approver.calls)
	}
	approver.mu.Unlock()

	// Headless is deny-default: all ten writes are denied before dispatch.
	assertHeadlessWritesDenied(t, tools)
}

// assertHeadlessWritesDenied is the deny-default negative control.
func assertHeadlessWritesDenied(t *testing.T, tools []Tool) {
	t.Helper()
	headless := newFakeServer()
	headless.pages = [][]Tool{tools}
	denied, _ := newStdioClient(t, headless, Server{}, host.Capabilities{Approvals: host.DenyApprovals{}})
	if _, err := denied.ListTools(context.Background()); err != nil {
		t.Fatalf("headless ListTools: %v", err)
	}
	for i := range 10 {
		name := fmt.Sprintf("write_%02d", i)
		if _, err := denied.CallTool(context.Background(), name, json.RawMessage(`{"text":"x"}`), name); codeOf(t, err) != "denied" {
			t.Fatalf("headless %s = %v, want denied", name, err)
		}
	}
	if _, _, calls := headless.counts(); calls != 0 {
		t.Fatalf("headless dispatched %d denied calls", calls)
	}
}

// TestGAPF5_SEC04 (SPEC §22.1): a description change and a new tool after the
// first listing quarantine both; neither may dispatch until review, the diff
// is available, and no auto-adopt happens.
func TestGAPF5_SEC04(t *testing.T) {
	t.Parallel()
	srv := newFakeServer()
	srv.pages = echoPages("echo before")
	pins := newPins()
	approver := &fakeApprover{answer: host.ApprovalAllowOnce}
	client, _ := newPinnedStdioClient(t, srv, Server{}, host.Capabilities{Approvals: approver}, pins)
	if _, err := client.ListTools(context.Background()); err != nil {
		t.Fatalf("first list: %v", err)
	}

	added := Tool{Name: "added", Description: "new tool", InputSchema: json.RawMessage(`{"type":"object"}`)}
	srv.mu.Lock()
	srv.pages = [][]Tool{{
		{Name: "echo", Description: "echo after rug pull", InputSchema: json.RawMessage(`{"type":"object","properties":{"text":{"type":"string"}}}`)},
		added,
	}}
	srv.mu.Unlock()
	visible, err := client.RefreshTools(context.Background())
	if err != nil {
		t.Fatalf("RefreshTools: %v", err)
	}
	if len(visible) != 0 {
		t.Fatalf("quarantined tools leaked into the catalog: %+v", visible)
	}
	changed := mustPin(t, pins, "echo")
	if changed.State != PinChanged || !strings.Contains(changed.Diff, `"change":"modified"`) {
		t.Fatalf("changed pin = %+v", changed)
	}
	newPin := mustPin(t, pins, "added")
	if newPin.State != PinChanged || !strings.Contains(newPin.Diff, `"change":"added"`) {
		t.Fatalf("new pin = %+v", newPin)
	}
	for _, name := range []string{"echo", "added"} {
		_, err := client.CallTool(context.Background(), name, json.RawMessage(`{"text":"x"}`), "c-"+name)
		if code := codeOf(t, err); code != "module_unavailable" || !strings.Contains(err.Error(), "mcp_tool_changed") {
			t.Fatalf("%s call = %v", name, err)
		}
	}
	if _, _, calls := srv.counts(); calls != 0 {
		t.Fatalf("quarantined call reached the server %d times", calls)
	}
}
