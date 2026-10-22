package phase

import (
	"slices"
	"sort"
	"testing"
)

// TestAcceptanceRequiredSet is the ADR-176 proof of the acceptance rule the
// roadmap states: the job runs while PHASE is raised from Fx to Fx+1, so it
// certifies the phase being left. Concretely:
//
//	(a) PHASE=Fx+1 with an Fx row still pending fails;
//	(b) the same Fx row covered passes;
//	(c) a row below Fx pending fails;
//	(d) a row of the phase being entered may be pending (or absent), but must
//	    label its own phase — and at F9 no row may be pending at all.
//
// The keys are ordinary words: the catalog scan reads real test sources, so a
// synthetic fixture must not look like an EC id.
func TestAcceptanceRequiredSet(t *testing.T) {
	t.Parallel()
	required := map[string]int{"one": 1, "two": 2, "three": 3, "nine": 9}
	covered := func() catalogEntry { return catalogEntry{status: "covered", file: "fixture_test.go"} }
	pending := func(p int) catalogEntry {
		return catalogEntry{status: "pending:" + phaseName(p), file: "fixture_test.go"}
	}
	cat := func(entries map[string]catalogEntry) map[string]catalogEntry { return entries }

	tests := []struct {
		name      string
		cur       int
		catalog   map[string]catalogEntry
		uncatalog []string // ids from uncatalogedViolations, in id order
		coverage  []string // ids from coverageViolations, in id order
	}{
		{
			name:     "a_phase_left_pending_fails",
			cur:      3,
			catalog:  cat(map[string]catalogEntry{"one": covered(), "two": pending(2), "three": pending(3)}),
			coverage: []string{"two"},
		},
		{
			name:    "b_phase_left_covered_passes",
			cur:     3,
			catalog: cat(map[string]catalogEntry{"one": covered(), "two": covered(), "three": pending(3)}),
		},
		{
			name:     "c_below_phase_left_pending_fails",
			cur:      3,
			catalog:  cat(map[string]catalogEntry{"one": pending(1), "two": covered(), "three": pending(3)}),
			coverage: []string{"one"},
		},
		{
			name:    "d_entered_phase_pending_own_label_passes",
			cur:     3,
			catalog: cat(map[string]catalogEntry{"one": covered(), "two": covered(), "three": pending(3)}),
		},
		{
			name:    "d_entered_phase_absent_passes",
			cur:     3,
			catalog: cat(map[string]catalogEntry{"one": covered(), "two": covered()}),
		},
		{
			name:     "d_entered_phase_other_label_fails",
			cur:      3,
			catalog:  cat(map[string]catalogEntry{"one": covered(), "two": covered(), "three": pending(2)}),
			coverage: []string{"three"},
		},
		{
			name:     "d_entered_phase_n_a_passes",
			cur:      3,
			catalog:  cat(map[string]catalogEntry{"one": covered(), "two": covered(), "three": {status: "n/a", file: "fixture_test.go"}}),
			coverage: nil,
		},
		{
			name:     "d_f9_never_pending",
			cur:      9,
			catalog:  cat(map[string]catalogEntry{"one": covered(), "two": covered(), "three": covered(), "nine": pending(9)}),
			coverage: []string{"nine"},
		},
		{
			name:    "f1_certifies_nothing",
			cur:     1,
			catalog: cat(map[string]catalogEntry{"one": pending(1), "two": covered()}),
		},
		{
			name:      "f2_requires_only_f1_row",
			cur:       2,
			catalog:   cat(map[string]catalogEntry{"one": covered(), "two": pending(2)}),
			uncatalog: nil,
		},
		{
			name:      "missing_row_below_phase_left_fails",
			cur:       3,
			catalog:   cat(map[string]catalogEntry{"one": covered(), "three": pending(3)}),
			uncatalog: []string{"two"},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := ids(uncatalogedViolations(required, tc.catalog, tc.cur)); !slices.Equal(got, tc.uncatalog) {
				t.Errorf("uncatalogedViolations = %v, want %v", got, tc.uncatalog)
			}
			if got := ids(coverageViolations(required, tc.catalog, tc.cur)); !slices.Equal(got, tc.coverage) {
				t.Errorf("coverageViolations = %v, want %v", got, tc.coverage)
			}
		})
	}
}

// TestAcceptanceRequiredPhase pins the arithmetic and, with it, the failure the
// old gate would report: at PHASE=F3 the required set stops at F2 (ADR-176).
func TestAcceptanceRequiredPhase(t *testing.T) {
	t.Parallel()
	for cur, want := range map[int]int{1: 0, 2: 1, 3: 2, 9: 8} {
		if got := requiredPhase(cur); got != want {
			t.Errorf("requiredPhase(F%d) = %d, want %d", cur, got, want)
		}
	}
	// The message the gate prints for the phase being entered names it.
	vs := coverageViolations(
		map[string]int{"three": 3},
		map[string]catalogEntry{"three": {status: "pending:2", file: "fixture_test.go"}},
		3,
	)
	want := []string{"phase acceptance F3: catalog status \"pending:2\" (fixture_test.go), want covered, n/a or pending:F3"}
	if len(vs) != 1 || vs[0].id != "three" || vs[0].why != want[0] {
		t.Errorf("coverageViolations = %+v, want one violation with %q", vs, want[0])
	}
}

func ids(vs []violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.id)
	}
	sort.Strings(out)
	return out
}
