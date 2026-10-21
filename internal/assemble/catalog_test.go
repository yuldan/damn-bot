package assemble

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

func TestCatalogSortedAndOverflow(t *testing.T) {
	t.Parallel()
	groups := make([]CatalogGroup, 30)
	for i := range groups {
		groups[i] = CatalogGroup{Name: string(rune('a' + i)), Summary: "one, two"}
	}
	// Reverse input order; output must be sorted byte-wise.
	for i, j := 0, len(groups)-1; i < j; i, j = i+1, j-1 {
		groups[i], groups[j] = groups[j], groups[i]
	}
	text := newAssembler(t, identity).Catalog(groups)
	lines := strings.Split(text, "\n")
	if len(lines) != 1+CatalogMaxGroups+1 {
		t.Fatalf("lines = %d, want header + %d + overflow", len(lines), CatalogMaxGroups)
	}
	if lines[0] != catalogHeader {
		t.Errorf("header = %q", lines[0])
	}
	if got := lines[len(lines)-1]; got != "- +6 more groups: tool_search" {
		t.Errorf("overflow line = %q", got)
	}
	for i := 2; i <= CatalogMaxGroups; i++ {
		if lines[i] < lines[i-1] {
			t.Errorf("groups not sorted: %q before %q", lines[i-1], lines[i])
		}
	}
}

func TestCatalogEmpty(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	if got := a.Catalog(nil); got != "" {
		t.Errorf("empty catalog = %q", got)
	}
	if got := a.Catalog([]CatalogGroup{{Name: " \n", Summary: "x"}}); got != "" {
		t.Errorf("nameless group rendered %q", got)
	}
}

func TestCatalogCap(t *testing.T) {
	t.Parallel()
	groups := []CatalogGroup{
		{Name: "a", Summary: strings.Repeat("tool ", 200)},
		{Name: "b", Summary: "short"},
	}
	text := newAssembler(t, identity).Catalog(groups)
	if n := asciiCounter().Count(text); n > CapCatalog {
		t.Errorf("catalog = %d tokens, cap %d", n, CapCatalog)
	}
}

// TestCatalogLineLimit: a group line above CatalogLineMax tokens is never cut
// (a cut can forge a tool name); the group moves to the overflow line and the
// next group takes its slot (REVIEW-F2-SEC4 ASM4-4, ADR-119 butir 7).
func TestCatalogLineLimit(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	// asciiCounter: 60 bytes = 15 tokens, 61 bytes = 16.
	atLimit := "- b: " + strings.Repeat("x", 60-len("- b: "))
	for _, tc := range []struct {
		name   string
		groups []CatalogGroup
		want   string
	}{
		{"one oversized", []CatalogGroup{
			{Name: "a", Summary: strings.Repeat("y", 61-len("- a: "))},
			{Name: "b", Summary: atLimit},
			{Name: "c", Summary: "short"},
		}, catalogHeader + "\n" + atLimit + "\n- c: short\n- +1 more groups: tool_search"},
		{"merged line oversized", []CatalogGroup{
			{Name: "a", Summary: "first half of the group tools"},
			{Name: "a", Summary: "second half of the group tools"},
			{Name: "c", Summary: "short"},
		}, catalogHeader + "\n- c: short\n- +1 more groups: tool_search"},
		{"all oversized", []CatalogGroup{
			{Name: "a", Summary: strings.Repeat("tool ", 200)},
		}, catalogHeader + "\n- +1 more groups: tool_search"},
	} {
		got := a.Catalog(tc.groups)
		if got != tc.want {
			t.Errorf("%s:\n%s\nwant\n%s", tc.name, got, tc.want)
		}
		for line := range strings.SplitSeq(got, "\n") {
			if n := asciiCounter().Count(line); n > CatalogLineMax {
				t.Errorf("%s: line %q = %d tokens, cap %d", tc.name, line, n, CatalogLineMax)
			}
		}
	}
}

// TestCatalogSanitized: names and summaries are scrubbed and lose line
// breaks, C0/C1 controls and format runes (bidi overrides and isolates,
// zero-width marks) before rendering (REVIEW-F2-SEC4 ASM4-5).
func TestCatalogSanitized(t *testing.T) {
	t.Parallel()
	scrub := func(s string) string { return strings.ReplaceAll(s, "sk-secret", "[redacted]") }
	a := newAssembler(t, scrub)
	got := a.Catalog([]CatalogGroup{
		{Name: "b\u202e", Summary: "left\u202eright\u2066x\u2069"},
		{Name: "c", Summary: "ctl\x1b[31m red\u200b\x00\u0085end"},
		{Name: "d", Summary: "line\r\none\u2028two token sk-secret"},
	})
	want := catalogHeader + "\n" +
		"- b: leftrightx\n" +
		"- c: ctl[31m red end\n" +
		"- d: line one two token [redacted]"
	if got != want {
		t.Errorf("catalog:\n%q\nwant\n%q", got, want)
	}
}

// TestCatalogDeterministic: the catalog is a pure function of the group set
// (P15, SPEC §5.2): input order, duplicate registrations, whitespace and an
// already-prefixed registry line never change the bytes.
func TestCatalogDeterministic(t *testing.T) {
	t.Parallel()
	a := []CatalogGroup{
		{Name: "web", Summary: "search the web and extract pages"},
		{Name: "browser", Summary: "- browser: navigate, click,\ntype, read"},
		{Name: "code", Summary: "symbols, definitions"},
		{Name: "code", Summary: "rename"},
		{Name: "code", Summary: "rename"},
		{Name: "undo", Summary: ""},
	}
	b := []CatalogGroup{a[5], a[3], a[0], a[4], a[2], a[1]}
	want := catalogHeader + "\n" +
		"- browser: navigate, click, type, read\n" +
		"- code: rename; symbols, definitions\n" +
		"- undo\n" +
		"- web: search the web and extract pages"
	for i, groups := range [][]CatalogGroup{a, b} {
		if got := newAssembler(t, identity).Catalog(groups); got != want {
			t.Errorf("order %d:\n%s\nwant\n%s", i, got, want)
		}
	}
}

// TestCatalogOverflow: 40 module groups at the 15-token line limit still fit
// the 600-token tier with the single overflow line (SPEC P1, §5.2).
func TestCatalogOverflow(t *testing.T) {
	t.Parallel()
	groups := make([]CatalogGroup, 40)
	for i := range groups {
		groups[i] = CatalogGroup{
			Name:    fmt.Sprintf("group%02d", i),
			Summary: "list, read, write, search, run and stop items",
		}
	}
	c := asciiCounter()
	text := newAssembler(t, identity).Catalog(groups)
	if n := c.Count(text); n > CapCatalog {
		t.Fatalf("catalog = %d tokens, cap %d", n, CapCatalog)
	}
	lines := strings.Split(text, "\n")
	shown := len(lines) - 2
	if shown > CatalogMaxGroups || shown < 1 {
		t.Fatalf("shown groups = %d", shown)
	}
	if want := fmt.Sprintf("- +%d more groups: tool_search", 40-shown); lines[len(lines)-1] != want {
		t.Errorf("overflow = %q, want %q", lines[len(lines)-1], want)
	}
	if lines[1] != "- group00: "+groups[0].Summary {
		t.Errorf("first group = %q", lines[1])
	}
}

// TestBuildRule3FollowsCatalog: stable rule 3 (tool_search/tool_invoke) is
// rendered exactly when the deferred catalog is, whatever the caller set.
func TestBuildRule3FollowsCatalog(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	for _, tc := range []struct {
		name    string
		catalog []CatalogGroup
		opt     bool
		want    bool
	}{
		{"none", nil, true, false},
		{"groups", []CatalogGroup{{Name: "web", Summary: "search pages"}}, false, true},
	} {
		req, est, err := a.Build(context.Background(), &SessionView{
			Phase: PhaseF1, Stable: StableOptions{Deferred: tc.opt}, Catalog: tc.catalog,
			History: []Message{{Role: "user", Content: "hi"}},
		})
		if err != nil {
			t.Fatalf("%s: Build: %v", tc.name, err)
		}
		system := req.Messages[0].Content
		if got := strings.Contains(system, "tool_invoke with that exact name"); got != tc.want {
			t.Errorf("%s: rule 3 rendered = %v, want %v", tc.name, got, tc.want)
		}
		if got := strings.Contains(system, catalogHeader); got != tc.want || (est.Catalog > 0) != tc.want {
			t.Errorf("%s: catalog rendered = %v (est %d), want %v", tc.name, got, est.Catalog, tc.want)
		}
	}
}
