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

// p1Fixture is one ADR-066 fixture and its per-tier allocations.
type p1Fixture struct {
	name    string
	stable  StableOptions
	catalog []CatalogGroup
	context func() *ContextInput
	tools   func() []Tool
	// Allocations: f1-core uses ADR-086 NEW-41, all-modules the SPEC P1 row.
	capStable, capTools, capTotal, eager int
}

func p1Fixtures() []p1Fixture {
	return []p1Fixture{
		{
			name: "f1-core", context: fixtureContext, tools: fixtureTools,
			capStable: 1000, capTools: 1800, capTotal: 4000, eager: 8,
		},
		{
			name: "all-modules", stable: StableOptions{Codeintel: true},
			catalog: allModulesCatalog(), context: allModulesContext, tools: allModulesTools,
			capStable: 1200, capTools: 2000, capTotal: 5000, eager: 10,
		},
	}
}

type p1Render struct {
	request, tiers []byte
	est            Estimate
	ctx            ContextResult
	stable         string
	tools          []Tool
}

func buildP1(t *testing.T, fx *p1Fixture) p1Render {
	t.Helper()
	a := newAssembler(t, identity)
	turn := a.Freeze(fixtureVolatile())
	filtered, err := FilterTools(PhaseF1, fx.tools())
	if err != nil {
		t.Fatalf("FilterTools: %v", err)
	}
	ctxResult, err := a.Context(context.Background(), fx.context())
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	view := SessionView{
		Phase:   PhaseF1,
		Stable:  fx.stable,
		Catalog: fx.catalog,
		Context: ctxResult.Text,
		Tools:   fx.tools(),
		Turn:    turn,
		History: []Message{{Role: "user", Content: allModulesQuery}},
	}
	req, est, err := a.Build(context.Background(), &view)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	requestJSON, err := MarshalRequest(req)
	if err != nil {
		t.Fatalf("MarshalRequest: %v", err)
	}
	toolsJSON, err := MarshalTools(filtered)
	if err != nil {
		t.Fatalf("MarshalTools: %v", err)
	}
	stableOpts := fx.stable
	stableOpts.Deferred = len(fx.catalog) > 0
	r := p1Render{est: est, ctx: ctxResult, stable: Stable(stableOpts), tools: filtered}
	tiers := tiersFile{
		Stable:   r.stable,
		Catalog:  a.Catalog(fx.catalog),
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
	tiersJSON, err := json.MarshalIndent(tiers, "", "  ")
	if err != nil {
		t.Fatalf("marshal tiers: %v", err)
	}
	requestJSON = append(requestJSON, '\n')
	tiersJSON = append(tiersJSON, '\n')
	r.request, r.tiers = requestJSON, tiersJSON
	return r
}

// TestFixedOverheadGolden is the ADR-066 cross-module hook: it renders each
// P1 fixture in openai-chat wire form plus the per-tier strings. The bench
// module (TestFixedOverhead/<fixture>) measures both with o200k_base and
// enforces the P1 gates; this test pins the bytes (E-68) and checks the same
// allocations with the injected estimator.
func TestFixedOverheadGolden(t *testing.T) {
	t.Parallel()
	for _, fx := range p1Fixtures() {
		t.Run(fx.name, func(t *testing.T) {
			t.Parallel()
			r := buildP1(t, &fx)
			checkGolden(t, filepath.Join(p1Dir, fx.name+".request.json"), r.request)
			checkGolden(t, filepath.Join(p1Dir, fx.name+".tiers.json"), r.tiers)

			c := asciiCounter()
			caps := []struct {
				name      string
				got, capN int
			}{
				{"stable", c.Count(r.stable), fx.capStable},
				{"catalog", r.est.Catalog, CapCatalog},
				{"context", r.est.Context, CapContext},
				{"facts", c.Count(r.ctx.Slots.Facts), CapFacts},
				{"workspace", c.Count(r.ctx.Slots.Workspace), CapWorkspace},
				{"files", c.Count(r.ctx.Slots.Files), CapFiles},
				{"memory", c.Count(r.ctx.Slots.Memory), CapMemory},
				{"skills", c.Count(r.ctx.Slots.Skills), CapSkills},
				{"volatile", r.est.Volatile, CapVolatile},
				{"tools", r.est.Tools, fx.capTools},
				{"request", r.est.Total, fx.capTotal},
			}
			for _, cp := range caps {
				if cp.got > cp.capN {
					t.Errorf("%s = %d tokens, allocation %d", cp.name, cp.got, cp.capN)
				}
			}
			if len(r.tools) != fx.eager {
				t.Errorf("eager tools = %d, want %d", len(r.tools), fx.eager)
			}
			if r.est.Stable != c.Count(r.stable) {
				t.Errorf("stable tier in request = %d tokens, golden %d", r.est.Stable, c.Count(r.stable))
			}
			t.Logf("%s estimate: stable=%d catalog=%d context=%d volatile=%d tools=%d total=%d",
				fx.name, r.est.Stable, r.est.Catalog, r.est.Context, r.est.Volatile, r.est.Tools, r.est.Total)
		})
	}
}
