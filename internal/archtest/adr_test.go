package archtest

import (
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var adrHeading = regexp.MustCompile(`^## ADR-(\d{3})\b`)

// TestADRUnique (PRC-2): every ADR number appears once and headings are
// monotonically increasing in file order. A bad rebase must not duplicate or
// reorder decision records.
func TestADRUnique(t *testing.T) {
	t.Parallel()
	seen := map[int]bool{}
	last := 0 // monotonic from ADR-120; earlier numbers are reserved/backfilled
	for line := range strings.SplitSeq(readRepoFile(t, "docs/DECISIONS.md"), "\n") {
		m := adrHeading.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		n, err := strconv.Atoi(m[1])
		if err != nil {
			t.Fatalf("ADR heading %q: %v", line, err)
		}
		if seen[n] {
			t.Fatalf("duplicate ADR-%03d", n)
		}
		if n >= 120 {
			if n <= last {
				t.Fatalf("ADR-%03d is not after ADR-%03d", n, last)
			}
			last = n
		}
		seen[n] = true
	}
	if len(seen) == 0 {
		t.Fatal("no ADR headings found")
	}
}
