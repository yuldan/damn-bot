package builtin

import (
	"fmt"

	"github.com/damnbot/damnbot/internal/memoryproviders"
)

// ContextEngine resolves the configured context-engine plugin. An empty name
// is the default-off state; an engine outside the closed slot is rejected by
// the memoryproviders registry (PLUG-014/MEM-023).
func ContextEngine(name string) (memoryproviders.ContextEngine, error) {
	engine, err := memoryproviders.LookupContextEngine(name)
	if err != nil {
		return nil, fmt.Errorf("context engine: %w", err)
	}
	return engine, nil
}
