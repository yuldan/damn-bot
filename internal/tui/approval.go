package tui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
)

// approvalDefaultTimeout is SPEC §18.2 [approval] preview_timeout_s.
const approvalDefaultTimeout = 600 * time.Second

// ApprovalRequest is one real loop approval; Preview is shown before any key.
type ApprovalRequest struct {
	Tool, Args, Preview, Origin, Effect string
	Tainted, Terminal, NoSession        bool
}

// ApprovalDecision is the answer sent back to the loop.
type ApprovalDecision string

// Approval decisions; the loop maps any other value to deny.
const (
	ApprovalAllowOnce    ApprovalDecision = "allow_once"
	ApprovalAllowSession ApprovalDecision = "allow_session"
	ApprovalDeny         ApprovalDecision = "deny"
)

// ApprovalMsg carries one request; a nil Reply means no transport (denied).
type ApprovalMsg struct {
	Request ApprovalRequest
	Reply   chan<- ApprovalDecision
}

type (
	approvalTimeoutMsg struct{ id int64 }
	approvalDoneMsg    struct{ id int64 }
)

type pendingApproval struct {
	id    int64
	req   ApprovalRequest
	reply chan<- ApprovalDecision
	done  chan struct{}
}

// startApproval shows the modal. A second concurrent request is denied: one
// decision can never cover two actions.
func (m *Model) startApproval(msg *ApprovalMsg) tea.Cmd {
	if msg.Reply == nil {
		m.appendNotice("approval denied: no transport")
		return nil
	}
	if m.approval != nil {
		select {
		case msg.Reply <- ApprovalDeny:
		default:
		}
		m.appendNotice("approval denied: another decision is still pending")
		return nil
	}
	m.exitNav()
	m.approvalSeq++
	m.approval = &pendingApproval{id: m.approvalSeq, req: msg.Request, reply: msg.Reply, done: make(chan struct{})}
	return m.approvalTimerCmd(m.approval.id)
}

// approvalTimerCmd uses the injected clock; done releases it after a decision.
func (m *Model) approvalTimerCmd(id int64) tea.Cmd {
	timeout := m.host.Config.Snapshot().ApprovalTimeout
	if timeout <= 0 {
		timeout = approvalDefaultTimeout
	}
	clock, done := m.host.Clock, m.approval.done
	return func() tea.Msg {
		select {
		case <-clock.After(timeout):
			return approvalTimeoutMsg{id: id}
		case <-done:
			return approvalDoneMsg{id: id}
		}
	}
}

// resolveApproval closes the modal once and answers the loop.
func (m *Model) resolveApproval(d ApprovalDecision, note string) {
	p := m.approval
	if p == nil {
		return
	}
	m.approval = nil
	close(p.done)
	select {
	case p.reply <- d:
	default:
	}
	m.appendNotice(note)
}

func (m *Model) timeoutApproval(id int64) {
	if m.approval != nil && m.approval.id == id {
		m.resolveApproval(ApprovalDeny, "approval timed out; denied")
	}
}

// handleApprovalKey: y is once-only; s only when the class allows a session
// grant; e denies and hands the command to the prompt for resubmission.
func (m *Model) handleApprovalKey(msg tea.KeyPressMsg) {
	req := m.approval.req
	switch {
	case key.Matches(msg, m.keys.Once):
		m.resolveApproval(ApprovalAllowOnce, "approval: allowed once")
	case key.Matches(msg, m.keys.Session):
		if req.NoSession {
			m.appendNotice("approval: session grant is not available for this tool; use y once or n")
			return
		}
		m.resolveApproval(ApprovalAllowSession, "approval: allowed for this session")
	case key.Matches(msg, m.keys.Edit):
		cmd := editCommand(req.Args)
		m.resolveApproval(ApprovalDeny, "approval: edit requested; original denied, resubmit the edited command")
		m.draft = nil
		m.input.SetValue(cmd)
	case key.Matches(msg, m.keys.No), key.Matches(msg, m.keys.Cancel):
		m.resolveApproval(ApprovalDeny, "approval: denied")
	}
}

// approvalBody renders the preview; there is deliberately no auto-approve path.
func (m *Model) approvalBody() string {
	req := m.approval.req
	head := fmt.Sprintf("APPROVAL %s · %s", req.Tool, req.Effect)
	if req.Tainted {
		head += " · tainted"
	}
	if req.Terminal {
		head += " · terminal"
	}
	parts := []string{head, req.Preview}
	if req.Args != "" {
		parts = append(parts, "args: "+clipRunes(req.Args, 240))
	}
	return strings.Join(parts, "\n")
}

// editCommand extracts the editable command field, else the raw JSON.
func editCommand(raw string) string {
	var obj map[string]any
	if json.Unmarshal([]byte(raw), &obj) == nil {
		for _, k := range []string{"command", "path", "query", "pattern"} {
			if s, ok := obj[k].(string); ok && s != "" {
				return s
			}
		}
	}
	return raw
}
