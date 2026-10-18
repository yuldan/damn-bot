package speed

import (
	"os"
	"strings"
	"testing"
)

func load(t *testing.T) ([]Row, Catalog) {
	t.Helper()
	p, err := os.ReadFile("../../docs/FEATURE-PARITY.md")
	if err != nil {
		t.Fatalf("read parity: %v", err)
	}
	c, err := os.ReadFile("catalog.json")
	if err != nil {
		t.Fatalf("read catalog: %v", err)
	}
	cat, err := ParseCatalog(c)
	if err != nil {
		t.Fatal(err)
	}
	return ParseParity(string(p)), cat
}

// TestParseParityCounts: 854 baris, 17 dropped-with-approval (FEATURE-PARITY "Konvensi kolom").
func TestParseParityCounts(t *testing.T) {
	t.Parallel()
	rows, _ := load(t)
	dropped := 0
	for _, r := range rows {
		if strings.HasPrefix(r.Status, "dropped-with-approval") {
			dropped++
		}
	}
	if len(rows) != 854 || dropped != 17 {
		t.Fatalf("rows=%d dropped=%d, want 854/17", len(rows), dropped)
	}
}

// TestEveryRowClassified: tidak ada baris tanpa op atau alasan (SCORECARD §3.3 / acceptance F0).
func TestEveryRowClassified(t *testing.T) {
	t.Parallel()
	rows, cat := load(t)
	ids := map[string]bool{}
	for _, e := range Classify(rows, cat) {
		ids[e.ID] = true
		if e.Class == ClassUnclassified {
			t.Errorf("%s (%s) tanpa op/alasan", e.ID, e.Feature)
		}
	}
	for id := range cat.Ops {
		if !strings.HasPrefix(id, "_") && !ids[id] {
			t.Errorf("op %s tidak ada di FEATURE-PARITY", id)
		}
	}
}

func TestSplitCellsEscapedPipe(t *testing.T) {
	t.Parallel()
	got := splitCells(`| A-001 | x \| y | planned |`)
	if len(got) != 3 || got[1] != "x | y" || got[2] != "planned" {
		t.Fatalf("splitCells = %q", got)
	}
}

func TestPercentile(t *testing.T) {
	t.Parallel()
	s := []float64{5, 1, 4, 2, 3}
	if Percentile(s, 50) != 3 || Percentile(s, 95) != 5 || Percentile(nil, 50) != 0 {
		t.Fatalf("percentile salah: %v %v", Percentile(s, 50), Percentile(s, 95))
	}
}
