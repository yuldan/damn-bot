package gateway

// Route identifies one gateway conversation. SPEC §9.7 keeps one gateway
// session per (platform, chat, thread).
type Route struct {
	Platform string
	ChatID   string
	ThreadID string
}

// Scope is the (user, chat) identity of one admitted update. Admit builds it
// from the adapter-authenticated Inbound and the stored identity; it is the
// only input to session routing and memory scoping, and nothing in a message
// can choose or override it.
//
// The two keys are deliberately different, per SPEC §9.7: a session belongs to
// a chat route (a group chat shares one session), while memory belongs to the
// gateway principal. Two users can therefore never share a memory scope, and a
// session labelled with another principal is not readable through this Scope
// (internal/ctxdb/gwstore.Session applies that filter).
type Scope struct {
	Platform string
	ChatID   string
	ThreadID string
	UserID   string
	// Workspace is the sandbox root of a role user identity; "" for an owner,
	// whose root is the serve cwd (ADR-084).
	Workspace string
}

// MemoryScope is the memory.scope key of this principal (SPEC §9.7):
// "gateway:<platform>:<identity>".
func (s *Scope) MemoryScope() string {
	return "gateway:" + s.Platform + ":" + s.UserID
}

// Label is the session_labels `gateway_chat` value of this principal
// ("<platform>:<identity>"); a session carrying another label is invisible to
// it.
func (s *Scope) Label() string {
	return s.Platform + ":" + s.UserID
}

// Route is the session route of this Scope.
func (s *Scope) Route() Route {
	return Route{Platform: s.Platform, ChatID: s.ChatID, ThreadID: s.ThreadID}
}
