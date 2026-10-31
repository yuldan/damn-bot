package host

import (
	"fmt"
	"slices"
)

// Capability is one reviewed session capability (GAP-REL-18). The set is
// closed: an unknown value is invalid and never granted.
type Capability string

// Session capabilities. A capability is a permission fact the composition
// root decided; a tool never infers one from global state.
const (
	// CapExecuteCode allows running submitted code (execute_code).
	CapExecuteCode Capability = "execute_code"
	// CapDelegate allows spawning subagents (delegate_task).
	CapDelegate Capability = "delegate"
	// CapMCP allows calling MCP tools.
	CapMCP Capability = "mcp"
	// CapUserMemory allows reading and writing the user memory scope.
	CapUserMemory Capability = "user_memory"
	// CapNetwork allows network egress through tools.
	CapNetwork Capability = "network"
	// CapApprovalBypass records that this session may skip interactive
	// approval. It is never inherited by a delegate child.
	CapApprovalBypass Capability = "approval_bypass"
	// CapSecretAccess records that this session may reference secrets. It is
	// never inherited by a delegate child.
	CapSecretAccess Capability = "secret_access"
)

// ValidCapability reports whether c is one of the reviewed capabilities.
func ValidCapability(c Capability) bool {
	switch c {
	case CapExecuteCode, CapDelegate, CapMCP, CapUserMemory, CapNetwork,
		CapApprovalBypass, CapSecretAccess:
		return true
	}
	return false
}

// SessionView is the explicit tool and capability set of one session
// (GAP-REL-18). It is passed by value to every tool that crosses a process
// or agent boundary (execute_code, delegate_task, MCP) so no tool can fall
// back to a process-global set. The zero value is invalid: a caller that
// omits the view fails closed with an internal error instead of seeing the
// empty set of somebody else.
//
// The value is one pointer to immutable data: copying it is cheap (the tool
// Env grows by one word), and no caller can mutate a constructed view.
type SessionView struct{ data *sessionData }

// sessionData is the immutable content of one view.
type sessionData struct {
	id, parentID, origin, cwd string
	depth                     int
	tools                     []string // sorted, non-nil
	caps                      map[Capability]bool
}

// NewSessionView builds one session's explicit view. tools is copied and
// deduplicated; nil and []string{} are both the valid empty set (the caller
// chose to expose no tools). Duplicate or empty names and unknown
// capabilities are rejected rather than silently dropped.
func NewSessionView(id, parentID, origin, cwd string, toolNames []string, caps []Capability) (SessionView, error) {
	if id == "" {
		return SessionView{}, fmt.Errorf("tools: session view needs an id")
	}
	seen := make(map[string]bool, len(toolNames))
	names := make([]string, 0, len(toolNames))
	for _, name := range toolNames {
		if name == "" {
			return SessionView{}, fmt.Errorf("tools: session %s: empty tool name", id)
		}
		if seen[name] {
			return SessionView{}, fmt.Errorf("tools: session %s: duplicate tool %q", id, name)
		}
		seen[name] = true
		names = append(names, name)
	}
	cp := make(map[Capability]bool, len(caps))
	for _, c := range caps {
		if !ValidCapability(c) {
			return SessionView{}, fmt.Errorf("tools: session %s: unknown capability %q", id, c)
		}
		if cp[c] {
			return SessionView{}, fmt.Errorf("tools: session %s: duplicate capability %q", id, c)
		}
		cp[c] = true
	}
	slices.Sort(names)
	return SessionView{data: &sessionData{id: id, parentID: parentID, origin: origin, cwd: cwd, tools: names, caps: cp}}, nil
}

// Valid reports whether v was built by NewSessionView. The zero value — a
// nil argument in an old caller — is invalid and fails closed.
func (v SessionView) Valid() bool { return v.data != nil && v.data.tools != nil }

// SessionID returns the owning session id.
func (v SessionView) SessionID() string {
	if v.data == nil {
		return ""
	}
	return v.data.id
}

// ParentID returns the parent session id ("" for a root).
func (v SessionView) ParentID() string {
	if v.data == nil {
		return ""
	}
	return v.data.parentID
}

// Origin returns the session origin ("run", "delegate", …).
func (v SessionView) Origin() string {
	if v.data == nil {
		return ""
	}
	return v.data.origin
}

// Cwd returns the workspace root the session was opened in.
func (v SessionView) Cwd() string {
	if v.data == nil {
		return ""
	}
	return v.data.cwd
}

// Depth returns the delegation depth (0 for a root session).
func (v SessionView) Depth() int {
	if v.data == nil {
		return 0
	}
	return v.data.depth
}

// Tools returns a copy of the allowed tool names in sorted order.
func (v SessionView) Tools() []string {
	if v.data == nil {
		return nil
	}
	return slices.Clone(v.data.tools)
}

// Allows reports whether name is in this session's explicit set. An invalid
// view allows nothing.
func (v SessionView) Allows(name string) bool {
	if !v.Valid() {
		return false
	}
	_, ok := slices.BinarySearch(v.data.tools, name)
	return ok
}

// Capabilities returns the granted capabilities in sorted order.
func (v SessionView) Capabilities() []Capability {
	if v.data == nil || len(v.data.caps) == 0 {
		return nil
	}
	out := make([]Capability, 0, len(v.data.caps))
	for c, ok := range v.data.caps {
		if ok {
			out = append(out, c)
		}
	}
	slices.Sort(out)
	return out
}

// Can reports whether c was granted to this session. An invalid view grants
// nothing.
func (v SessionView) Can(c Capability) bool {
	if !v.Valid() {
		return false
	}
	return v.data.caps[c]
}

// Child derives the view of a delegate child. The child can never exceed its
// parent: toolNames and caps are intersected with the parent set, the depth
// increments by one, and the approval-bypass and secret-access capabilities
// are always dropped (SPEC §9.3: a subagent inherits neither approvals nor
// secrets automatically). The result is a new value; v is unchanged.
func (v SessionView) Child(id string, toolNames []string, caps []Capability) (SessionView, error) {
	if !v.Valid() {
		return SessionView{}, fmt.Errorf("tools: child of an invalid session view")
	}
	return v.ChildIn(id, v.data.cwd, toolNames, caps)
}

// ChildIn is Child with an explicit workspace (a delegate worktree). The
// intersection, depth, and dropped capabilities are identical.
func (v SessionView) ChildIn(id, cwd string, toolNames []string, caps []Capability) (SessionView, error) {
	if !v.Valid() {
		return SessionView{}, fmt.Errorf("tools: child of an invalid session view")
	}
	child, err := NewSessionView(id, v.data.id, "delegate", cwd, toolNames, caps)
	if err != nil {
		return SessionView{}, err
	}
	kept := child.data.tools[:0]
	for _, name := range child.data.tools {
		if v.Allows(name) {
			kept = append(kept, name)
		}
	}
	child.data.tools = kept
	for c := range child.data.caps {
		if c == CapApprovalBypass || c == CapSecretAccess || !v.Can(c) {
			delete(child.data.caps, c)
		}
	}
	child.data.depth = v.data.depth + 1
	return child, nil
}
