package budget

import (
	"encoding/json"
	"os"
	"path/filepath"
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

// TestFixedOverhead/f1-core is the cross-module half of the ADR-066 hook: it
// measures the exact bytes internal/assemble renders with o200k_base and
// enforces the F1 P1 gate (≤ 4.000 total) plus the ADR-086 NEW-41 per-tier
// allocations and SPEC §6 context sub-caps.
func TestFixedOverhead(t *testing.T) {
	t.Parallel()
	t.Run("f1-core", func(t *testing.T) {
		t.Parallel()
		tok, err := o200k.Load()
		if err != nil {
			t.Fatalf("o200k.Load: %v", err)
		}
		request, err := os.ReadFile(filepath.Join(p1Dir, "f1-core.request.json"))
		if err != nil {
			t.Fatalf("read request (run internal/assemble TestFixedOverheadGolden -update): %v", err)
		}
		var tiers p1Tiers
		raw, err := os.ReadFile(filepath.Join(p1Dir, "f1-core.tiers.json"))
		if err != nil {
			t.Fatalf("read tiers: %v", err)
		}
		if err := json.Unmarshal(raw, &tiers); err != nil {
			t.Fatalf("decode tiers: %v", err)
		}
		total := tok.Count(string(request))
		t.Logf("f1-core o200k: total=%d stable=%d catalog=%d context=%d volatile=%d tools=%d",
			total, tok.Count(tiers.Stable), tok.Count(tiers.Catalog), tok.Count(tiers.Context),
			tok.Count(tiers.Volatile), tok.Count(tiers.Tools))
		if total > 4000 {
			t.Errorf("f1-core request = %d o200k tokens, gate 4000", total)
		}
		caps := []struct {
			name string
			text string
			cap  int
		}{
			{"stable", tiers.Stable, 1000},
			{"catalog", tiers.Catalog, 600},
			{"context", tiers.Context, 1100},
			{"volatile", tiers.Volatile, 100},
			{"tools", tiers.Tools, 1800},
		}
		for _, c := range caps {
			if got := tok.Count(c.text); got > c.cap {
				t.Errorf("%s = %d o200k tokens, allocation %d", c.name, got, c.cap)
			}
		}
		sub := []struct {
			name string
			cap  int
		}{
			{"facts", 60},
			{"workspace", 150},
			{"files", 400},
			{"memory", 300},
			{"skills", 150},
		}
		for _, s := range sub {
			if got := tok.Count(tiers.ContextSlots[s.name]); got > s.cap {
				t.Errorf("context %s = %d o200k tokens, cap %d", s.name, got, s.cap)
			}
		}
	})
}
