package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

// p1Dir is internal/assemble/testdata/p1 relative to bench/budget (ADR-066).
const p1Dir = "../../internal/assemble/testdata/p1"

type p1Tiers struct {
	Stable       string            `json:"stable"`
	Catalog      string            `json:"catalog"`
	Context      string            `json:"context"`
	Volatile     string            `json:"volatile"`
	Tools        string            `json:"tools"`
	ContextSlots map[string]string `json:"context_slots"`
}

// p1Gate is one fixture's P1 gate: the request total and per-tier
// allocations. f1-core uses the F1 gate (ADR-066) with the ADR-086 NEW-41
// allocations; all-modules uses the full SPEC P1 row (ADR-121).
type p1Gate struct {
	name                                         string
	total, stable, catalog, tools, catalogGroups int
}

// TestFixedOverhead is the cross-module half of the ADR-066 hook: it
// measures the exact bytes internal/assemble renders with o200k_base and
// enforces the P1 gate, the per-tier allocations, the SPEC §6 context
// sub-caps, and for the catalog the SPEC §5.2 line rules (≤ 24 groups, each
// line ≤ 15 tokens).
func TestFixedOverhead(t *testing.T) {
	t.Parallel()
	gates := []p1Gate{
		{name: "f1-core", total: 4000, stable: 1000, catalog: 600, tools: 1800, catalogGroups: 0},
		{name: "mcp-65", total: 5000, stable: 1000, catalog: 600, tools: 1800, catalogGroups: 24},
		{name: "all-modules", total: 5000, stable: 1200, catalog: 600, tools: 2000, catalogGroups: 24},
	}
	for _, g := range gates {
		t.Run(g.name, func(t *testing.T) {
			t.Parallel()
			checkFixedOverhead(t, g)
		})
	}
}

func checkFixedOverhead(t *testing.T, g p1Gate) {
	t.Helper()
	tok, err := o200k.Load()
	if err != nil {
		t.Fatalf("o200k.Load: %v", err)
	}
	request, err := os.ReadFile(filepath.Join(p1Dir, g.name+".request.json"))
	if err != nil {
		t.Fatalf("read request (run internal/assemble TestFixedOverheadGolden -update): %v", err)
	}
	var tiers p1Tiers
	raw, err := os.ReadFile(filepath.Join(p1Dir, g.name+".tiers.json"))
	if err != nil {
		t.Fatalf("read tiers: %v", err)
	}
	if err := json.Unmarshal(raw, &tiers); err != nil {
		t.Fatalf("decode tiers: %v", err)
	}
	total := tok.Count(string(request))
	slots := tiers.ContextSlots
	t.Logf("%s o200k: total=%d stable=%d catalog=%d context=%d (facts=%d workspace=%d files=%d memory=%d skills=%d) volatile=%d tools=%d",
		g.name, total, tok.Count(tiers.Stable), tok.Count(tiers.Catalog), tok.Count(tiers.Context),
		tok.Count(slots["facts"]), tok.Count(slots["workspace"]), tok.Count(slots["files"]),
		tok.Count(slots["memory"]), tok.Count(slots["skills"]),
		tok.Count(tiers.Volatile), tok.Count(tiers.Tools))
	if total > g.total {
		t.Errorf("%s request = %d o200k tokens, gate %d", g.name, total, g.total)
	}
	caps := []struct {
		name string
		text string
		cap  int
	}{
		{"stable", tiers.Stable, g.stable},
		{"catalog", tiers.Catalog, g.catalog},
		{"context", tiers.Context, 1100},
		{"volatile", tiers.Volatile, 100},
		{"tools", tiers.Tools, g.tools},
		{"facts", tiers.ContextSlots["facts"], 60},
		{"workspace", tiers.ContextSlots["workspace"], 150},
		{"files", tiers.ContextSlots["files"], 400},
		{"memory", tiers.ContextSlots["memory"], 300},
		{"skills", tiers.ContextSlots["skills"], 150},
	}
	for _, c := range caps {
		if got := tok.Count(c.text); got > c.cap {
			t.Errorf("%s = %d o200k tokens, allocation %d", c.name, got, c.cap)
		}
	}
	checkCatalogLines(t, tok, tiers.Catalog, g.catalogGroups)
}

// checkCatalogLines enforces SPEC §5.2 on the rendered catalog: the expected
// number of group lines (no more than 24), each ≤ 15 o200k tokens.
func checkCatalogLines(t *testing.T, tok *o200k.Tokenizer, catalog string, groups int) {
	t.Helper()
	n := 0
	for line := range strings.SplitSeq(catalog, "\n") {
		if !strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "- +") {
			continue
		}
		n++
		if got := tok.Count(line); got > 15 {
			t.Errorf("catalog line %q = %d o200k tokens, cap 15", line, got)
		}
	}
	if n != groups || n > 24 {
		t.Errorf("catalog group lines = %d, want %d (≤ 24)", n, groups)
	}
}
