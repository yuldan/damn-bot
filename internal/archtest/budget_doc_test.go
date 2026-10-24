package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// totalCoreRow reads the "| **Total core** | **N** |" table row.
var totalCoreRow = regexp.MustCompile(`\*\*Total core\*\* \| \*\*([0-9.]+)\*\*`)

// totalCoreProse reads the "Total core per paket = N ≤ 47.000" sentence.
var totalCoreProse = regexp.MustCompile(`Total core per paket = ([0-9.]+) ≤ 47\.000`)

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
// that sum is what `TestBudgetTableSum` caps at 47.000 (ADR-054, V-20). The two
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
	// The §2.4 table is reported but not asserted: it also lists packages that
	// loc.budget keeps outside Σ core (e2e 300 + integration 200) and is stale
	// per package, so on main it sums to 45.121 while the row/sentence say
	// 44.338 and the Σ is 43.631. Aligning the table is a docs task of its own;
	// until then the guard compares the two authoritative figures.
	t.Logf("docs/ENGINEERING.md §2.4 table sums to %d (row %d, sentence %d, Σ %d)", tableCoreSum(t, doc), rowN, proseN, sum)
	if rowN != sum || proseN != sum {
		t.Errorf("Total core drift (ADR-054, V-20): docs/ENGINEERING.md row = %d, sentence = %d, Σ loc.budget (budgetCoreSum) = %d, cap = 47000; known drift 207 lines (44.238 vs 44.031 at ef8ec40d; 44.131 vs 43.631 at b979bc83)",
			rowN, proseN, sum)
	}
}

// tableCellNumber parses a LOC cell of the §2.4 table ("2.160" -> 2160).
func tableCellNumber(cell string) (int, bool) {
	v := strings.TrimSpace(cell)
	if v == "" {
		return 0, false
	}
	n, err := strconv.Atoi(replaceDots(v))
	if err != nil {
		return 0, false
	}
	return n, true
}

// tableCoreSum adds the LOC column of the §2.4 package table (every cell that
// is not the Total core row). It is the figure a reader sees, so it must equal
// the gate sum too.
func tableCoreSum(t *testing.T, doc string) int {
	t.Helper()
	sum, rows := 0, 0
	for line := range strings.SplitSeq(doc, "\n") {
		if !strings.HasPrefix(line, "| `") || strings.Contains(line, "**Total core**") {
			continue
		}
		rows++
		for cell := range strings.SplitSeq(line, "|") {
			if n, ok := tableCellNumber(cell); ok {
				sum += n
			}
		}
	}
	if rows == 0 {
		t.Fatal("docs/ENGINEERING.md: no §2.4 package rows found")
	}
	return sum
}
