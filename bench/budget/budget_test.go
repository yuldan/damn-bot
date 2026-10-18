package budget

import (
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/bench/budget/o200k"
)

const snapshotDir = "testdata/hermes-868b9f244f"

func loadBoth(t *testing.T) (def, noCtx Snapshot) {
	t.Helper()
	tok, err := o200k.Load()
	if err != nil {
		t.Fatalf("o200k.Load: %v", err)
	}
	def, err = LoadSnapshot(tok, filepath.Join(snapshotDir, "default"))
	if err != nil {
		t.Fatalf("default: %v", err)
	}
	noCtx, err = LoadSnapshot(tok, filepath.Join(snapshotDir, "no_context_files"))
	if err != nil {
		t.Fatalf("no_context_files: %v", err)
	}
	return def, noCtx
}

// TestHermesBaselineReproduced: harness mereproduksi angka BASELINE-hermes.md (overhead per call,
// schema bytes, tier) dalam ±5% (ROADMAP F0 acceptance). Baris non-gate hanya dilaporkan.
func TestHermesBaselineReproduced(t *testing.T) {
	t.Parallel()
	def, noCtx := loadBoth(t)
	for _, c := range Compare(def, noCtx) {
		switch {
		case c.Name == "schema_count" && c.Got != c.Want:
			t.Errorf("%s: got %d, want %d persis", c.Name, c.Got, c.Want)
		case c.Gate && !c.OK():
			t.Errorf("%s: got %d, want %d ± 5%% (Δ %+.2f%%)", c.Name, c.Got, c.Want, 100*c.Delta)
		default:
			t.Logf("%-34s got %6d want %6d Δ %+6.2f%% gate=%v", c.Name, c.Got, c.Want, 100*c.Delta, c.Gate)
		}
	}
}

// TestSnapshotInternallyConsistent: system prompt = tier digabung "\n\n" (agent/system_prompt.py:811).
func TestSnapshotInternallyConsistent(t *testing.T) {
	t.Parallel()
	def, noCtx := loadBoth(t)
	for name, s := range map[string]Snapshot{"default": def, "no_context_files": noCtx} {
		parts := 0
		for _, n := range []int{s.Stable.Bytes, s.Context.Bytes, s.Volatile.Bytes} {
			if n > 0 {
				parts++
			}
		}
		want := s.Stable.Bytes + s.Context.Bytes + s.Volatile.Bytes + 2*(parts-1)
		if s.SystemPrompt.Bytes != want {
			t.Errorf("%s: system_prompt %d B, Σ tier + pemisah = %d B", name, s.SystemPrompt.Bytes, want)
		}
	}
}
