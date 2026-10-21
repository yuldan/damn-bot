package assemble

import (
	"context"
	"encoding/json"
	"path/filepath"
	"testing"
)

const p1Dir = "testdata/p1"

type tiersFile struct {
	Stable       string            `json:"stable"`
	Catalog      string            `json:"catalog"`
	Context      string            `json:"context"`
	Volatile     string            `json:"volatile"`
	Tools        string            `json:"tools"`
	ContextSlots map[string]string `json:"context_slots"`
}

func buildF1Core(t *testing.T) (requestJSON, tiersJSON []byte, est Estimate, ctxResult ContextResult, tools []Tool) {
	t.Helper()
	a := newAssembler(t, identity)
	turn := a.Freeze(fixtureVolatile())
	filtered, err := FilterTools(PhaseF1, fixtureTools())
	if err != nil {
		t.Fatalf("FilterTools: %v", err)
	}
	view := SessionView{
		Phase:   PhaseF1,
		Context: renderContext(t, a, fixtureContext()),
		Tools:   fixtureTools(),
		Turn:    turn,
		History: []Message{{Role: "user", Content: "Fix the failing test in internal/foo."}},
	}
	req, est, err := a.Build(context.Background(), &view)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	requestJSON, err = MarshalRequest(req)
	if err != nil {
		t.Fatalf("MarshalRequest: %v", err)
	}
	requestJSON = append(requestJSON, '\n')
	toolsJSON, err := MarshalTools(filtered)
	if err != nil {
		t.Fatalf("MarshalTools: %v", err)
	}
	ctxResult, err = a.Context(context.Background(), fixtureContext())
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	tiers := tiersFile{
		Stable:   Stable(StableOptions{}),
		Catalog:  "",
		Context:  ctxResult.Text,
		Volatile: turn.String(),
		Tools:    string(toolsJSON),
		ContextSlots: map[string]string{
			"facts":     ctxResult.Slots.Facts,
			"workspace": ctxResult.Slots.Workspace,
			"files":     ctxResult.Slots.Files,
			"memory":    ctxResult.Slots.Memory,
			"skills":    ctxResult.Slots.Skills,
		},
	}
	tiersJSON, err = json.MarshalIndent(tiers, "", "  ")
	if err != nil {
		t.Fatalf("marshal tiers: %v", err)
	}
	tiersJSON = append(tiersJSON, '\n')
	return requestJSON, tiersJSON, est, ctxResult, filtered
}

// TestFixedOverheadGolden is the ADR-066 cross-module hook: it renders the
// f1-core fixture in openai-chat wire form plus the per-tier strings. The bench
// module (TestFixedOverhead/f1-core) measures both with o200k_base and
// enforces the P1 gate; this test pins the bytes (E-68) and the per-tier
// allocations of ADR-086 NEW-41.
func TestFixedOverheadGolden(t *testing.T) {
	t.Parallel()
	requestJSON, tiersJSON, est, ctxResult, tools := buildF1Core(t)
	checkGolden(t, filepath.Join(p1Dir, "f1-core.request.json"), requestJSON)
	checkGolden(t, filepath.Join(p1Dir, "f1-core.tiers.json"), tiersJSON)

	c := asciiCounter()
	if got := c.Count(Stable(StableOptions{})); got > 1000 {
		t.Errorf("stable tier = %d tokens, allocation 1000", got)
	}
	if est.Context > CapContext {
		t.Errorf("context tier = %d tokens, cap %d", est.Context, CapContext)
	}
	if got := c.Count(ctxResult.Slots.Facts); got > CapFacts {
		t.Errorf("facts = %d tokens, cap %d", got, CapFacts)
	}
	if got := c.Count(ctxResult.Slots.Workspace); got > CapWorkspace {
		t.Errorf("workspace = %d tokens, cap %d", got, CapWorkspace)
	}
	if got := c.Count(ctxResult.Slots.Files); got > CapFiles {
		t.Errorf("files = %d tokens, cap %d", got, CapFiles)
	}
	if got := c.Count(ctxResult.Slots.Memory); got > CapMemory {
		t.Errorf("memory = %d tokens, cap %d", got, CapMemory)
	}
	if got := c.Count(ctxResult.Slots.Skills); got > CapSkills {
		t.Errorf("skills = %d tokens, cap %d", got, CapSkills)
	}
	if est.Volatile > CapVolatile {
		t.Errorf("volatile = %d tokens, cap %d", est.Volatile, CapVolatile)
	}
	if est.Tools > 1800 {
		t.Errorf("tools = %d tokens, allocation 1800", est.Tools)
	}
	if est.Total > 4000 {
		t.Errorf("request = %d tokens, P1 f1-core gate 4000", est.Total)
	}
	if len(tools) != 8 {
		t.Errorf("eager tools = %d, want 8", len(tools))
	}
	t.Logf("f1-core estimate: stable=%d catalog=%d context=%d volatile=%d tools=%d total=%d",
		est.Stable, est.Catalog, est.Context, est.Volatile, est.Tools, est.Total)
}
