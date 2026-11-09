// Package resolve maps one plugin-catalog entry to the live damnbot target
// that would serve it (SPEC §9.10 / Q18). It is a pure lookup: it reads the
// embedded catalog and the caller-supplied registry sets, opens nothing,
// downloads nothing and never executes an entry.
//
// An entry whose target is not registered is not an error the caller should
// treat as a crash: it reports [ErrModuleUnavailable]. The CLI renders that
// status, while an id outside the closed catalog is [catalog.ErrUnknown].
package resolve

import (
	"errors"
	"fmt"

	"github.com/damnbot/damnbot/internal/plugins/catalog"
)

// ErrModuleUnavailable reports a catalog entry whose live target (MCP server,
// skill id or module id) is not registered in this process. It is a status,
// not a failure: nothing was installed, started or executed.
var ErrModuleUnavailable = errors.New("module_unavailable")

// Sources are the live registries resolution reads. MCP is keyed by server
// name (the tool list is informational; a server with zero tools is still
// registered); Skills and Modules are id sets.
type Sources struct {
	MCP     map[string][]string
	Skills  map[string]struct{}
	Modules map[string]struct{}
}

// Target is the resolved live target: the catalog entry's kind and id are the
// registry key in Sources.
type Target struct {
	Kind catalog.Kind
	ID   string
}

// Resolve looks up id in the closed catalog and checks the matching registry.
// The zero Target plus an error is returned for an unknown id or an
// unregistered target; a resolved target never implies activation.
func Resolve(c *catalog.Catalog, s Sources, id string) (Target, error) {
	e, ok := c.Lookup(id)
	if !ok {
		return Target{}, fmt.Errorf("%w: %q", catalog.ErrUnknown, id)
	}
	switch e.Kind {
	case catalog.KindMCP:
		if _, ok := s.MCP[e.ID]; !ok {
			return Target{}, unavailable(&e)
		}
	case catalog.KindSkill:
		if _, ok := s.Skills[e.ID]; !ok {
			return Target{}, unavailable(&e)
		}
	case catalog.KindModule:
		if _, ok := s.Modules[e.ID]; !ok {
			return Target{}, unavailable(&e)
		}
	default:
		return Target{}, fmt.Errorf("%w: %s: kind %q", catalog.ErrInvalid, e.ID, e.Kind)
	}
	return Target{Kind: e.Kind, ID: e.ID}, nil
}

func unavailable(e *catalog.Entry) error {
	return fmt.Errorf("%w: %s %q is not registered", ErrModuleUnavailable, e.Kind, e.ID)
}
