package tools

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/damnbot/damnbot/internal/testutil/phase"
)

// Status `n/a` beralasan (ADR-080) menggantikan `pending:F2`: pada F2 tidak ada satu pun
// modul/tool deferred yang dapat di-wire pada `run` — `newRunModules(modules.Default(), nil)`
// (cmd/damnbot/run.go:364) memakai registry default yang kosong (terukur: descriptors=0, tools=[]),
// tidak ada `modules.Register` di jalur biner, dan tidak ada flag/config yang menyalakannya — sehingga
// e2e lewat biner asli akan vakum (ia hanya membuktikan "tidak ada schema deferred karena tidak ada
// modul", bukan properti EC-TCALL-010). Invariannya dipatok in-process di file ini:
// `TestDeferredSchemaNotInPayload` (baris 23) — payload yang dideklarasikan ke provider tidak memuat
// schema deferred. e2e lewat biner asli WAJIB ditambahkan begitu modul pertama benar-benar di-wire
// (MCP, F5); lihat ADR-077/ADR-080 amandemen 2026-10-22 dan CHANGELOG.
// TestEdgeCasesF2 records EC-TCALL-010 as still pending at F2: the deferred
// path exists at the tools layer, but cmd/damnbot does not wire tool_search,
// tool_invoke or tools.Deferred yet (the composition root is part of the #174
// module wiring), so the EC cannot be proven end-to-end (review ACC-5).
func TestEdgeCasesF2(t *testing.T) {
	t.Parallel()
	g := phase.Current(t)
	if g.Item(t, "EC-TCALL-010", "F2", "n/a") {
		t.Run("EC-TCALL-010", ecTcall010)
	}
}

// TestDeferredSchemaNotInPayload is the tools-layer guard for EC-TCALL-010: a
// deferred (MCP) schema never joins the declared payload, tool_search defers
// it, tool_invoke runs it, and the registry the payload is rendered from is
// unchanged by either. The run-level proof waits for the #174 wiring.
func TestDeferredSchemaNotInPayload(t *testing.T) {
	t.Parallel()
	ecTcall010(t)
}

func ecTcall010(t *testing.T) {
	f := newDeferredFixture(t, []DeferredTool{deferredDef("mcp_issue", "mcp", "read")}, ModeAsk, &approverRec{answer: ApprovalAllowOnce})
	before := f.d.reg.Schemas()

	declared := toolNames(t, before)
	if _, leaked := declared["mcp_issue"]; leaked {
		t.Fatalf("deferred schema leaked into the declared payload: %v", declared)
	}
	if !declared[ToolSearchName] || !declared[ToolInvokeName] {
		t.Fatalf("meta-tools not declared: %v", declared)
	}

	hits := decodeSearch(t, toolSearchOutcome(t, f, `{"query":"mcp_issue"}`))
	if len(hits) != 1 || hits[0].Name != "mcp_issue" {
		t.Fatalf("tool_search hits = %+v", hits)
	}
	out := f.d.Dispatch(context.Background(), Call{
		Name: ToolInvokeName,
		Args: json.RawMessage(`{"name":"mcp_issue","arguments":{"q":"x"}}`),
	})
	if out.Err != nil {
		t.Fatalf("tool_invoke: %v", out.Err)
	}
	if f.mod.calls() != 1 || f.mod.lastTool != "mcp_issue" {
		t.Fatalf("module calls = %d, tool = %q", f.mod.calls(), f.mod.lastTool)
	}

	after := f.d.reg.Schemas()
	if len(after) != len(before) {
		t.Fatalf("payload grew from %d to %d tools", len(before), len(after))
	}
	for i := range before {
		if string(before[i]) != string(after[i]) {
			t.Fatalf("payload tool %d changed after tool_search/tool_invoke", i)
		}
	}
}

// toolNames decodes the declared schemas into a set of names.
func toolNames(t *testing.T, schemas []json.RawMessage) map[string]bool {
	t.Helper()
	out := make(map[string]bool, len(schemas))
	for _, raw := range schemas {
		var doc struct {
			Name string `json:"name"`
		}
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("schema %s: %v", raw, err)
		}
		out[doc.Name] = true
	}
	return out
}
