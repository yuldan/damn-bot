package config

import (
	"errors"
	"testing"
)

// TestKanbanConfigDefaults: every [kanban] automation switch is off unless
// the operator turns it on (ADR-326 P9-12).
func TestKanbanConfigDefaults(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Kanban.DispatchInGateway || cfg.Kanban.AutoDecompose || cfg.Kanban.NotifyInGateway {
		t.Fatalf("kanban defaults = %+v, want all false", cfg.Kanban)
	}
}

// TestKanbanConfigParses: the three keys decode from [kanban].
func TestKanbanConfigParses(t *testing.T) {
	t.Parallel()
	cfg, err := parse(t, "[kanban]\ndispatch_in_gateway = true\nauto_decompose = true\nnotify_in_gateway = true\n")
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Kanban.DispatchInGateway || !cfg.Kanban.AutoDecompose || !cfg.Kanban.NotifyInGateway {
		t.Fatalf("kanban = %+v, want all true", cfg.Kanban)
	}
}

// TestKanbanConfigRejectsUnknownKey: the section is validated strictly like
// every other key (a typo must not silently disable an automation switch).
func TestKanbanConfigRejectsUnknownKey(t *testing.T) {
	t.Parallel()
	_, err := parse(t, "[kanban]\ndispatch_in_gateways = true\n")
	if !errors.Is(err, ErrUnknownKey) {
		t.Fatalf("err = %v, want ErrUnknownKey", err)
	}
}
