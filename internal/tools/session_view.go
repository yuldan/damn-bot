package tools

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// SessionView is the explicit tool and capability set of one session
// (GAP-REL-18). It lives in modules/host so the MCP seam receives the same
// value; see host.SessionView for the invariants.
type SessionView = host.SessionView

// Capability is one reviewed session capability.
type Capability = host.Capability

// Session capabilities re-exported for the tools API.
const (
	CapExecuteCode    = host.CapExecuteCode
	CapDelegate       = host.CapDelegate
	CapMCP            = host.CapMCP
	CapUserMemory     = host.CapUserMemory
	CapNetwork        = host.CapNetwork
	CapApprovalBypass = host.CapApprovalBypass
	CapSecretAccess   = host.CapSecretAccess
)

// NewSessionView builds one session's explicit view.
func NewSessionView(id, parentID, origin, cwd string, toolNames []string, caps []Capability) (SessionView, error) {
	v, err := host.NewSessionView(id, parentID, origin, cwd, toolNames, caps)
	if err != nil {
		return SessionView{}, fmt.Errorf("tools: session view: %w", err)
	}
	return v, nil
}

// ValidCapability reports whether c is one of the reviewed capabilities.
func ValidCapability(c Capability) bool { return host.ValidCapability(c) }

// requireSession returns the session view or the closed internal error a
// session-scoped tool returns when the composition root failed to pass one
// (GAP-REL-18: no fallback to state owned by another session).
func requireSession(env Env, tool string) (SessionView, *ToolError) {
	if !env.Session.Valid() {
		return SessionView{}, toolErr(CodeInternal, "the composition root did not pass a session view", "%s: session tools and capabilities are missing", tool)
	}
	return env.Session, nil
}
