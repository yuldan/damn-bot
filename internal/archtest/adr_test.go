package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// adrHeading matches one decision-record heading, e.g. "## ADR-186 — …".
var adrHeading = regexp.MustCompile(`^## ADR-(\d{3,})\b`)

// adrOrderFloor is the first number the ordering rule applies to: smaller
// numbers are reserved/backfilled (the same floor the original PRC-2 proposal
// used), so their historical order is not asserted.
const adrOrderFloor = 120

// adrHead is one heading with its number and line.
type adrHead struct {
	n, line int
}

// adrHeads reads every "## ADR-NNN" heading of docs/DECISIONS.md in file order.
func adrHeads(t *testing.T) []adrHead {
	t.Helper()
	var out []adrHead
	for i, line := range strings.Split(readRepoFile(t, "docs/DECISIONS.md"), "\n") {
		m := adrHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("docs/DECISIONS.md:%d: ADR heading %q: %v", i+1, line, err)
		}
		out = append(out, adrHead{n: n, line: i + 1})
	}
	if len(out) == 0 {
		t.Fatal("no ADR headings found in docs/DECISIONS.md")
	}
	return out
}

// TestADRUnique: every ADR number appears exactly once. Two PRs may
// legitimately pick the same "next free" number (that is how ADR-174..177
// collided); this test turns that silent collision into a failure and names
// both lines.
func TestADRUnique(t *testing.T) {
	t.Parallel()
	seen := map[int][]int{}
	for _, h := range adrHeads(t) {
		seen[h.n] = append(seen[h.n], h.line)
	}
	for n, lines := range seen {
		if len(lines) > 1 {
			t.Errorf("ADR-%03d is used %d times (lines %v): pick the next free number and renumber the newer block",
				n, len(lines), lines)
		}
	}
}

// TestADRMonotonic: a heading numbered >= adrOrderFloor must not follow a
// higher one, so an appended decision record cannot land between existing
// records. The four legacy inversions are allowlisted inside the test; a new
// one fails with both numbers and lines.
func TestADRMonotonic(t *testing.T) {
	t.Parallel()
	// The legacy inversions that predate this guard, {previous, current} in
	// file order. Renumbering them would break the code comments, PR bodies and
	// history that cite those numbers, so the debt is listed here instead of
	// being hidden: only a NEW inversion fails.
	allow := [][2]int{
		{163, 139},
		{183, 140},
		{175, 142},
		{146, 124},
	}
	allowed := func(prev, cur int) bool {
		for _, p := range allow {
			if p[0] == prev && p[1] == cur {
				return true
			}
		}
		return false
	}
	heads := adrHeads(t)
	for i := 1; i < len(heads); i++ {
		prev, cur := heads[i-1], heads[i]
		if cur.n < adrOrderFloor || cur.n > prev.n || allowed(prev.n, cur.n) {
			continue
		}
		t.Errorf("docs/DECISIONS.md:%d: ADR-%03d appears after ADR-%03d (line %d); append new ADRs with the next free number above the current maximum and do not renumber existing blocks",
			cur.line, cur.n, prev.n, prev.line)
	}
}
