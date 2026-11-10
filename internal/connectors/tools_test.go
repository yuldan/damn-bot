package connectors

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/damnbot/damnbot/internal/schema"
)

func TestConnectorsToolCatalog(t *testing.T) {
	t.Parallel()
	tools := Tools()
	if len(tools) != 16 {
		t.Fatalf("tools = %d, want 16", len(tools))
	}
	names := make([]string, 0, len(tools))
	for _, tool := range tools {
		names = append(names, tool.Name)
		switch tool.Effect {
		case "read", "idempotent", "non_idempotent", "dynamic":
		default:
			t.Errorf("%s effect %q", tool.Name, tool.Effect)
		}
		if tool.Description == "" {
			t.Errorf("%s has no description", tool.Name)
		}
		if _, err := schema.Compile(tool.Schema, schema.ModeInternal); err != nil {
			t.Errorf("%s schema: %v", tool.Name, err)
		}
		var doc struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool.Schema, &doc); err != nil || doc.Type != "object" {
			t.Errorf("%s schema root = %q, %v", tool.Name, doc.Type, err)
		}
	}
	for _, d := range Builtin() {
		for _, name := range d.Tools {
			if !slices.Contains(names, name) {
				t.Errorf("descriptor %s declares %q without a catalog tool", d.Name, name)
			}
		}
	}
	if len(slices.Compact(slices.Sorted(slices.Values(names)))) != len(names) {
		t.Fatalf("duplicate tool name in %v", names)
	}
}

func TestConnectorsNewFromConfig(t *testing.T) {
	t.Parallel()
	m, err := NewFromConfig(json.RawMessage(`{"enabled":false}`), Deps{})
	if err != nil || m == nil {
		t.Fatalf("disabled section: %v", err)
	}
	if _, err := NewFromConfig(json.RawMessage(`{"enabled":true,"timeout_s":9999}`), Deps{}); err == nil {
		t.Fatal("invalid section was accepted")
	}
}
