package connectors

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/damnbot/damnbot/internal/modules/host"
)

// Scope is one capability a connector exposes. The set is closed: config and
// tests can only name a scope that appears in a [Builtin] descriptor, and an
// empty scope is never treated as "everything".
type Scope string

// Connector scopes of SPEC §9.9.
const (
	ScopeMail        Scope = "mail"
	ScopeCalendar    Scope = "calendar"
	ScopeFiles       Scope = "files"
	ScopeMeetings    Scope = "meetings"
	ScopeDocs        Scope = "docs"
	ScopeMessages    Scope = "messages"
	ScopeAdmin       Scope = "admin"
	ScopeSearch      Scope = "search"
	ScopeSupplyChain Scope = "supply_chain"
)

// Descriptor is one connector's static identity. Tools is the exact list of
// deferred tool names the connector may serve; a tool outside the list is a
// construction error, not a fallback.
type Descriptor struct {
	Name        string
	Description string
	Scopes      []Scope
	Tools       []string
}

// Registry errors.
var (
	ErrUnknownConnector   = errors.New("connectors: unknown connector")
	ErrDuplicateConnector = errors.New("connectors: duplicate connector")
	ErrInvalidConnector   = errors.New("connectors: invalid connector descriptor")
	ErrUnknownTool        = errors.New("connectors: tool does not belong to the connector")
)

// Builtin returns the closed connector table.
func Builtin() []Descriptor {
	return []Descriptor{
		{
			Name:        "graph",
			Description: "Microsoft Graph mail, calendar and files",
			Scopes:      []Scope{ScopeMail, ScopeCalendar, ScopeFiles},
			Tools:       []string{"ms_graph_mail", "ms_graph_calendar", "ms_graph_files"},
		},
		{
			Name:        "teams",
			Description: "Microsoft Teams meeting pipeline over Graph",
			Scopes:      []Scope{ScopeMeetings},
			Tools:       []string{"teams_pipeline"},
		},
		{
			Name:        "feishu",
			Description: "Feishu/Lark documents and drive comments",
			Scopes:      []Scope{ScopeDocs},
			Tools: []string{
				"feishu_doc_read", "feishu_drive_list_comments",
				"feishu_drive_list_comment_replies", "feishu_drive_reply_comment",
				"feishu_drive_add_comment",
			},
		},
		{
			Name:        "meet",
			Description: "Google Meet bot node (join, captions, speech)",
			Scopes:      []Scope{ScopeMeetings},
			Tools:       []string{"google_meet"},
		},
		{
			Name:        "xsearch",
			Description: "X/Twitter post and thread search through xAI",
			Scopes:      []Scope{ScopeSearch},
			Tools:       []string{"x_search"},
		},
		{
			Name:        "discord",
			Description: "Discord messages, member lookup and threads",
			Scopes:      []Scope{ScopeMessages, ScopeAdmin},
			Tools:       []string{"discord", "discord_admin"},
		},
		{
			Name:        "osv",
			Description: "OSV.dev vulnerability lookup",
			Scopes:      []Scope{ScopeSupplyChain},
			Tools:       []string{"osv_check"},
		},
	}
}

// Handler is one connector implementation. Invoke receives an already
// approved call and returns either a result or a typed error; the module
// never checks approval itself.
type Handler interface {
	// Tools returns the tool names this handler serves; every name must be
	// declared by the connector descriptor.
	Tools() []string
	// Invoke runs one tool. A read result carries UntrustedSource when its
	// content came from outside the process.
	Invoke(ctx context.Context, tool string, args json.RawMessage, env host.Env) (host.Result, error)
}

// Registry is the closed built-in table plus attached handlers.
type Registry struct {
	mu       sync.Mutex
	descs    []Descriptor
	byName   map[string]int
	attached map[string]Handler
}

// NewRegistry returns the closed built-in table.
func NewRegistry() *Registry {
	descs := Builtin()
	byName := make(map[string]int, len(descs))
	for i := range descs {
		byName[descs[i].Name] = i
	}
	return &Registry{descs: descs, byName: byName, attached: map[string]Handler{}}
}

// Lookup resolves one built-in descriptor by exact name.
func (r *Registry) Lookup(name string) (Descriptor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byName[name]
	if !ok {
		return Descriptor{}, false
	}
	return r.descs[i], true
}

// Names returns the closed connector names.
func (r *Registry) Names() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.descs))
	for i := range r.descs {
		out[i] = r.descs[i].Name
	}
	return out
}

// Attach binds one implementation to a built-in name.
func (r *Registry) Attach(name string, h Handler) error {
	if h == nil {
		return fmt.Errorf("%w: nil handler for %q", ErrInvalidConnector, name)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	i, ok := r.byName[name]
	if !ok {
		return fmt.Errorf("%w: %q", ErrUnknownConnector, name)
	}
	if r.attached[name] != nil {
		return fmt.Errorf("%w: %q", ErrDuplicateConnector, name)
	}
	desc := r.descs[i]
	for _, tool := range h.Tools() {
		if !slices.Contains(desc.Tools, tool) {
			return fmt.Errorf("%w: %s serves undeclared %q", ErrUnknownTool, name, tool)
		}
	}
	r.attached[name] = h
	return nil
}

// HandlerFor resolves the implementation of one tool name. A tool of an
// unattached connector is unavailable, never silently routed elsewhere.
// HandlerFor resolves the implementation of one tool name.
func (r *Registry) HandlerFor(tool string) (Handler, Descriptor, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for i := range r.descs {
		if !slices.Contains(r.descs[i].Tools, tool) {
			continue
		}
		h := r.attached[r.descs[i].Name]
		return h, r.descs[i], h != nil
	}
	return nil, Descriptor{}, false
}

func validName(name string) bool { return validIdent(name, 32) }

func validToolName(name string) bool { return validIdent(name, 64) }

func validIdent(name string, maxLen int) bool {
	if name == "" || len(name) > maxLen {
		return false
	}
	for i := range len(name) {
		c := name[i]
		if (c < 'a' || c > 'z') && (c < '0' || c > '9') && c != '_' && c != '-' {
			return false
		}
	}
	return true
}
