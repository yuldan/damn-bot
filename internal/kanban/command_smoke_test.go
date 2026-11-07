package kanban

import (
	"errors"
	"strings"
	"testing"
)

// TestF7KanbanCommandSurface is the named test of this slice: the shared
// Invocation surface behind `damnbot kanban` and `/kanban` must expose the
// closed subcommand set, classify the mutating ones, wire every handler, and
// refuse unknown names.
func TestF7KanbanCommandSurface(t *testing.T) {
	t.Parallel()
	s := smokeStore(t)
	iv := &Invocation{Store: s}
	ctx := t.Context()

	subs := Subcommands()
	if len(subs) != 45 {
		t.Fatalf("subcommands = %d, want the closed 45-command set", len(subs))
	}
	// Every closed subcommand must run through the shared surface: empty
	// arguments are a domain error, never an unknown command or a panic.
	for _, sub := range subs {
		_, err := iv.Run(ctx, sub, nil)
		if err == nil {
			continue
		}
		if errors.Is(err, ErrInvalid) && strings.Contains(err.Error(), "unknown subcommand") {
			t.Errorf("%q is not wired into the invocation surface", sub)
		}
	}
	for _, sub := range []string{"create", "claim", "edit", "archive", "block"} {
		if !Mutating(sub) {
			t.Errorf("%q must be mutating", sub)
		}
	}
	for _, sub := range []string{"list", "show", "boards", "context", "stats"} {
		if Mutating(sub) {
			t.Errorf("%q must be read-only", sub)
		}
	}
	if _, err := iv.Run(ctx, "no-such-subcommand", nil); err == nil {
		t.Fatal("unknown subcommand accepted")
	}
	if _, err := (&Invocation{}).Run(ctx, "list", nil); err == nil {
		t.Fatal("invocation without a store accepted a call")
	}
}
