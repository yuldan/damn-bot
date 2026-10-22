package phase

import (
	"slices"
	"sort"
	"testing"
)

// TestAcceptanceRequiredSet is the ADR-176 proof of the acceptance rule the
// roadmap states: the job runs while PHASE is raised from Fx to Fx+1, so it
// certifies the phase being left. The quadrants are named after the rule they
// pin:
//
//	(i)   phase < PHASE: pending or absent fails, covered passes;
//	(ii)  phase = PHASE: pending, n/a or absent passes, a foreign pending
//	      label fails;
//	(iii) phase > PHASE: pending passes (only logged);
//	(iv)  PHASE = F9: nothing may stay pending.
//
// The keys are ordinary words: the catalog scan reads real test sources, so a
// synthetic fixture must not look like an EC id.
func TestAcceptanceRequiredSet(t *testing.T) {
	t.Parallel()
	required := map[string]int{"one": 1, "two": 2, "three": 3, "nine": 9}
	for _, tc := range acceptanceCases() {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := ids(uncatalogedViolations(required, tc.catalog, tc.cur))
			if !slices.Equal(got, tc.uncatalog) {
				t.Errorf("uncatalogedViolations = %v, want %v", got, tc.uncatalog)
			}
			got = ids(coverageViolations(required, tc.catalog, tc.cur))
			if !slices.Equal(got, tc.coverage) {
				t.Errorf("coverageViolations = %v, want %v", got, tc.coverage)
			}
		})
	}
}

type acceptanceCase struct {
	name      string
	cur       int
	catalog   map[string]catalogEntry
	uncatalog []string // ids from uncatalogedViolations, in id order
	coverage  []string // ids from coverageViolations, in id order
}

// acceptanceCases returns one case per quadrant of the rule. The list is built
// by a function, not held in a package-level variable: E-10 keeps globals out
// of the tree, tests included.
func acceptanceCases() []acceptanceCase {
	return []acceptanceCase{
		{ // (i) a row below PHASE-1 still pending fails
			name:     "i_below_phase_pending_fails",
			cur:      3,
			catalog:  map[string]catalogEntry{"one": pendingRow(1), "two": coveredRow(), "three": pendingRow(3)},
			coverage: []string{"one"},
		},
		{ // (i) a row below PHASE-1 covered passes
			name:    "i_below_phase_covered_passes",
			cur:     3,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": coveredRow()},
		},
		{ // (i) a missing row below PHASE-1 fails
			name:      "i2_below_phase_absent_fails",
			cur:       3,
			catalog:   map[string]catalogEntry{"two": coveredRow(), "three": pendingRow(3)},
			uncatalog: []string{"one"},
		},
		{ // (ii) the phase being entered may be pending under its own label
			name:    "ii_entered_phase_pending_passes",
			cur:     3,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": coveredRow(), "three": pendingRow(3)},
		},
		{ // (ii) ... or n/a
			name:    "ii_entered_phase_na_passes",
			cur:     3,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": coveredRow(), "three": {status: "n/a", file: "fixture_test.go"}},
		},
		{ // (ii) ... or absent
			name:    "ii_entered_phase_absent_passes",
			cur:     3,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": coveredRow()},
		},
		{ // (ii) ... but not under another phase's label
			name:     "ii_entered_phase_foreign_label_fails",
			cur:      3,
			catalog:  map[string]catalogEntry{"one": coveredRow(), "two": coveredRow(), "three": pendingRow(2)},
			coverage: []string{"three"},
		},
		{ // (iii) a row above PHASE is only logged
			name:    "iii_above_phase_pending_passes",
			cur:     3,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": coveredRow(), "nine": pendingRow(9)},
		},
		{ // (iv) at F9 nothing may stay pending
			name:     "iv_f9_pending_fails",
			cur:      9,
			catalog:  map[string]catalogEntry{"one": coveredRow(), "two": coveredRow(), "three": coveredRow(), "nine": pendingRow(9)},
			coverage: []string{"nine"},
		},
		{ // F1 certifies nothing: F0 does not exist
			name:    "v_f1_certifies_nothing",
			cur:     1,
			catalog: map[string]catalogEntry{"one": pendingRow(1), "two": coveredRow()},
		},
		{ // F2 requires only the F1 row, while both F2 rows stay open
			name:    "v_f2_requires_only_the_f1_row",
			cur:     2,
			catalog: map[string]catalogEntry{"one": coveredRow(), "two": pendingRow(2)},
		},
	}
}

// TestAcceptanceRequiredPhase pins the arithmetic and the message the gate
// prints for the phase being entered.
func TestAcceptanceRequiredPhase(t *testing.T) {
	t.Parallel()
	for cur, want := range map[int]int{1: 0, 2: 1, 3: 2, 9: 8} {
		if got := requiredPhase(cur); got != want {
			t.Errorf("requiredPhase(F%d) = %d, want %d", cur, got, want)
		}
	}
	vs := coverageViolations(
		map[string]int{"three": 3},
		map[string]catalogEntry{"three": {status: "pending:2", file: "fixture_test.go"}},
		3,
	)
	want := `phase acceptance F3: catalog status "pending:2" (fixture_test.go), want covered, n/a or pending:F3`
	if len(vs) != 1 || vs[0].id != "three" || vs[0].why != want {
		t.Errorf("coverageViolations = %+v, want one violation with %q", vs, want)
	}
}

func coveredRow() catalogEntry {
	return catalogEntry{status: "covered", file: "fixture_test.go"}
}

func pendingRow(p int) catalogEntry {
	return catalogEntry{status: "pending:" + phaseName(p), file: "fixture_test.go"}
}

func ids(vs []violation) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.id)
	}
	sort.Strings(out)
	return out
}
