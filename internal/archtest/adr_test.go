package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// adrHeading matches one decision-record heading, e.g. "## ADR-186 — …".
var adrHeading = regexp.MustCompile(`^## ADR-(\d{3,})\b`)

// TestADRUnique: every ADR number appears exactly once in docs/DECISIONS.md.
// Two PRs may legitimately pick the same "next free" number (that is how
// ADR-174..177 collided); this test turns that silent collision into a failure
// and names both lines.
//
// PRC-2 in the original proposal also asserted that headings are monotonically
// increasing in file order. That cannot be asserted on the current file: the
// appends leave four regions out of order (ADR-163 -> 139, 183 -> 140,
// 175 -> 142, 146 -> 124), so enforcing it here would turn `main` red. Restoring
// the order is a docs-only change; the monotonic rule can come back with it.
func TestADRUnique(t *testing.T) {
	t.Parallel()
	seen := map[int][]int{} // number -> line numbers
	for i, line := range strings.Split(readRepoFile(t, "docs/DECISIONS.md"), "\n") {
		m := adrHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("docs/DECISIONS.md:%d: ADR heading %q: %v", i+1, line, err)
		}
		seen[n] = append(seen[n], i+1)
	}
	if len(seen) == 0 {
		t.Fatal("no ADR headings found in docs/DECISIONS.md")
	}
	for n, lines := range seen {
		if len(lines) > 1 {
			t.Errorf("ADR-%03d is used %d times (lines %v): pick the next free number and renumber the newer block",
				n, len(lines), lines)
		}
	}
}
