package builtin

import (
	"errors"
	"testing"

	"github.com/damnbot/damnbot/internal/memoryproviders"
)

// TestF8PLUG014ContextEngine: the context-engine plugin is default off, the
// built-in compressor is the only accepted engine, and an unwired target is
// module_unavailable.
func TestF8PLUG014ContextEngine(t *testing.T) {
	t.Parallel()
	p, ok := Lookup(ContextEngineID)
	if !ok || !p.DefaultOff || p.Target != TargetContextEngine {
		t.Fatalf("Lookup(%q) = (%+v, %t)", ContextEngineID, p, ok)
	}
	if _, err := Resolve(Targets{}, ContextEngineID); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("unwired target err = %v, want ErrUnavailable", err)
	}
	if _, err := Resolve(Targets{ContextEngine: true}, ContextEngineID); err != nil {
		t.Fatalf("wired target: %v", err)
	}
	if _, err := ContextEngine(""); !errors.Is(err, memoryproviders.ErrUnavailable) {
		t.Fatalf("empty engine err = %v, want ErrUnavailable (default off)", err)
	}
	engine, err := ContextEngine(memoryproviders.ContextEngineCompressor)
	if err != nil || engine.Name() != memoryproviders.ContextEngineCompressor {
		t.Fatalf("compressor = (%v, %v)", engine, err)
	}
	if _, err := ContextEngine("foreign-engine"); !errors.Is(err, memoryproviders.ErrUnknownEngine) {
		t.Fatalf("foreign engine err = %v, want ErrUnknownEngine", err)
	}
}
