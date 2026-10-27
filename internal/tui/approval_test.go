package tui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
)

func approvalApp(t *testing.T, timeout time.Duration) *Model {
	t.Helper()
	m, _, _ := newTestApp(t)
	if timeout > 0 {
		m.host.Config = fakeConfig{cfg: Config{Model: "fake/model", ApprovalTimeout: timeout}}
	}
	return m
}

func approvalReq() ApprovalRequest {
	return ApprovalRequest{
		Tool: "shell", Preview: "+ echo hi > out.txt", Effect: "non_idempotent",
		Args: `{"command":"echo hi > out.txt"}`, Origin: "tui",
	}
}

// TestApprovalPreviewThenAllowOnce: preview is visible before any decision, no
// decision exists until y, and y answers exactly one action (allow_once).
func TestApprovalPreviewThenAllowOnce(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 0)
	reply := make(chan ApprovalDecision, 1)
	if _, cmd := m.Update(ApprovalMsg{Request: approvalReq(), Reply: reply}); cmd == nil {
		t.Fatalf("approval did not arm the timeout")
	}
	view := m.View().Content
	if !strings.Contains(view, "APPROVAL shell") || !strings.Contains(view, "+ echo hi > out.txt") {
		t.Fatalf("preview missing before decision:\n%s", view)
	}
	select {
	case d := <-reply:
		t.Fatalf("decision %q before any key", d)
	default:
	}
	m.Update(navKey('y', 0))
	if got := <-reply; got != ApprovalAllowOnce {
		t.Fatalf("y = %q, want allow_once", got)
	}
	if m.approval != nil {
		t.Fatalf("modal stayed open after a decision")
	}

	// A second action sends a new request and must not inherit the first grant.
	second := make(chan ApprovalDecision, 1)
	m.Update(ApprovalMsg{Request: approvalReq(), Reply: second})
	select {
	case d := <-second:
		t.Fatalf("second action was auto-answered %q", d)
	default:
	}
	m.Update(navKey('n', 0))
	if got := <-second; got != ApprovalDeny {
		t.Fatalf("second n = %q, want deny", got)
	}
}

// TestApprovalDenyKeys: n and esc both deny.
func TestApprovalDenyKeys(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		key  tea.KeyPressMsg
	}{
		{"n", navKey('n', 0)},
		{"esc", navKey(tea.KeyEsc, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := approvalApp(t, 0)
			reply := make(chan ApprovalDecision, 1)
			m.Update(ApprovalMsg{Request: approvalReq(), Reply: reply})
			m.Update(tc.key)
			if got := <-reply; got != ApprovalDeny {
				t.Fatalf("%s = %q, want deny", tc.name, got)
			}
			if !strings.Contains(m.View().Content, "denied") {
				t.Fatalf("denial not reported")
			}
		})
	}
}

// TestApprovalTimeoutDenies uses the injected clock: no key, advance past the
// timeout, and the pending action is denied.
func TestApprovalTimeoutDenies(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 10*time.Second)
	clk, ok := m.host.Clock.(*fakeClock)
	if !ok {
		t.Fatalf("clock = %T", m.host.Clock)
	}
	reply := make(chan ApprovalDecision, 1)
	_, cmd := m.Update(ApprovalMsg{Request: approvalReq(), Reply: reply})
	if cmd == nil {
		t.Fatalf("no timeout command")
	}
	msgs := make(chan tea.Msg, 1)
	go func() { msgs <- cmd() }()
	<-clk.after
	clk.Advance(11 * time.Second)
	m.Update(<-msgs)
	if got := <-reply; got != ApprovalDeny {
		t.Fatalf("timeout = %q, want deny", got)
	}
	if !strings.Contains(m.View().Content, "approval timed out; denied") {
		t.Fatalf("timeout not reported")
	}
}

// TestApprovalEditMovesCommand: e denies the original and hands the command to
// the prompt; nothing is approved until the user resubmits.
func TestApprovalEditMovesCommand(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 0)
	reply := make(chan ApprovalDecision, 1)
	m.Update(ApprovalMsg{Request: approvalReq(), Reply: reply})
	m.Update(navKey('e', 0))
	if got := <-reply; got != ApprovalDeny {
		t.Fatalf("e = %q, want the original denied", got)
	}
	if got := m.input.Value(); got != "echo hi > out.txt" {
		t.Fatalf("input = %q, want the editable command", got)
	}
	if m.approval != nil {
		t.Fatalf("modal stayed open after edit")
	}
	select {
	case d := <-reply:
		t.Fatalf("edit produced a second decision %q", d)
	default:
	}
}

// TestApprovalNoTransportDenies: a request without a reply channel is denied.
func TestApprovalNoTransportDenies(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 0)
	m.Update(ApprovalMsg{Request: approvalReq()})
	if m.approval != nil {
		t.Fatalf("modal armed without transport")
	}
	if !strings.Contains(m.View().Content, "approval denied: no transport") {
		t.Fatalf("missing-transport denial not reported")
	}
}

// TestApprovalConcurrentRequestDenied: two pending approvals never share a
// decision; the second is denied immediately.
func TestApprovalConcurrentRequestDenied(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 0)
	first := make(chan ApprovalDecision, 1)
	second := make(chan ApprovalDecision, 1)
	m.Update(ApprovalMsg{Request: approvalReq(), Reply: first})
	m.Update(ApprovalMsg{Request: approvalReq(), Reply: second})
	if got := <-second; got != ApprovalDeny {
		t.Fatalf("concurrent request = %q, want deny", got)
	}
	select {
	case d := <-first:
		t.Fatalf("first request answered early: %q", d)
	default:
	}
	m.Update(navKey('y', 0))
	if got := <-first; got != ApprovalAllowOnce {
		t.Fatalf("first = %q, want allow_once", got)
	}
}

// TestApprovalSessionUnavailable: s is refused for a class without session
// grants and the modal stays up for y/n.
func TestApprovalSessionUnavailable(t *testing.T) {
	t.Parallel()
	m := approvalApp(t, 0)
	req := approvalReq()
	req.NoSession = true
	reply := make(chan ApprovalDecision, 1)
	m.Update(ApprovalMsg{Request: req, Reply: reply})
	m.Update(navKey('s', 0))
	select {
	case d := <-reply:
		t.Fatalf("s decided %q for a no-session class", d)
	default:
	}
	if last := m.blocks[len(m.blocks)-1]; !strings.Contains(last.Text, "session grant is not available") {
		t.Fatalf("s refusal not reported: %+v", last)
	}
	if strings.Contains(m.footer(), "s session") {
		t.Fatalf("footer advertised s for a no-session class: %q", m.footer())
	}
	m.Update(navKey('y', 0))
	if got := <-reply; got != ApprovalAllowOnce {
		t.Fatalf("y after s = %q", got)
	}
}
