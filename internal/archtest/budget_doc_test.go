package archtest

import (
	"regexp"
	"strconv"
	"testing"
)

// totalCoreRow reads the "| **Total core** | **N** |" table row.
var totalCoreRow = regexp.MustCompile(`\*\*Total core\*\* \| \*\*([0-9.]+)\*\*`)

// totalCoreProse reads the "Total core per paket = N ≤ 49.700" sentence.
var totalCoreProse = regexp.MustCompile(`Total core per paket = ([0-9.]+) ≤ 49\.700`)

// dotted parses the thousand-separated form the docs use (43.746).
func dotted(t *testing.T, s string) int {
	t.Helper()
	n, err := strconv.Atoi(replaceDots(s))
	if err != nil {
		t.Fatalf("number %q: %v", s, err)
	}
	return n
}

func replaceDots(s string) string {
	out := make([]byte, 0, len(s))
	for i := range len(s) {
		if s[i] != '.' {
			out = append(out, s[i])
		}
	}
	return string(out)
}

// TestEngineeringTotalCoreMatchesLocBudget: the "Total core" figure in
// docs/ENGINEERING.md (§2.4) is the sum of the core rows of loc.budget, because
// that sum is what `TestBudgetTableSum` caps at 48.780 (ADR-054, V-20; ADR-211; ADR-225). The two
// numbers are written by hand in different files, so they drift: 44.238 vs
// 44.031 at ef8ec40d and 44.131 vs 43.631 at b979bc83 — a 207-line gap that no
// gate noticed.
func TestEngineeringTotalCoreMatchesLocBudget(t *testing.T) {
	t.Parallel()
	sum := budgetCoreSum(t, readRepoFile(t, "loc.budget"))
	doc := readRepoFile(t, "docs/ENGINEERING.md")

	row := totalCoreRow.FindStringSubmatch(doc)
	prose := totalCoreProse.FindStringSubmatch(doc)
	if row == nil || prose == nil {
		t.Fatalf("docs/ENGINEERING.md: missing the Total core row (%v) or sentence (%v)", row != nil, prose != nil)
	}
	rowN, proseN := dotted(t, row[1]), dotted(t, prose[1])
	if rowN != sum || proseN != sum {
		t.Errorf("Total core drift (ADR-054, V-20): docs/ENGINEERING.md row = %d, sentence = %d, Σ loc.budget (budgetCoreSum) = %d, cap = %d.\nRecompute Σ from the rebased tree with an inline probe, then set the loc.budget rows and both ENGINEERING.md figures to that number - never copy figures from a PR body or from another branch (known drift 207 lines: 44.238 vs 44.031 at ef8ec40d).",
			rowN, proseN, sum, maxCoreLOC)
	}
}
