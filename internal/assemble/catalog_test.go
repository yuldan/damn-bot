package assemble

import (
	"strings"
	"testing"
)

func TestCatalogSortedAndOverflow(t *testing.T) {
	t.Parallel()
	groups := make([]CatalogGroup, 30)
	for i := range groups {
		groups[i] = CatalogGroup{Name: string(rune('a' + i)), Tools: []string{"one", "two"}}
	}
	// Reverse input order; output must be sorted byte-wise.
	for i, j := 0, len(groups)-1; i < j; i, j = i+1, j-1 {
		groups[i], groups[j] = groups[j], groups[i]
	}
	text := Catalog(groups, asciiCounter())
	lines := strings.Split(text, "\n")
	if len(lines) != CatalogMaxGroups+1 {
		t.Fatalf("lines = %d, want %d + overflow", len(lines), CatalogMaxGroups)
	}
	if !strings.Contains(lines[len(lines)-1], "+6 more groups: tool_search") {
		t.Errorf("overflow line = %q", lines[len(lines)-1])
	}
	for i := 1; i < CatalogMaxGroups; i++ {
		if lines[i] < lines[i-1] {
			t.Errorf("groups not sorted: %q before %q", lines[i-1], lines[i])
		}
	}
}

func TestCatalogEmpty(t *testing.T) {
	t.Parallel()
	if got := Catalog(nil, asciiCounter()); got != "" {
		t.Errorf("empty catalog = %q", got)
	}
}

func TestCatalogCap(t *testing.T) {
	t.Parallel()
	groups := []CatalogGroup{
		{Name: "a", Tools: []string{strings.Repeat("tool ", 200)}},
		{Name: "b", Tools: []string{"short"}},
	}
	text := Catalog(groups, asciiCounter())
	if n := asciiCounter().Count(text); n > CapCatalog {
		t.Errorf("catalog = %d tokens, cap %d", n, CapCatalog)
	}
}
