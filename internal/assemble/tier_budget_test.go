package assemble

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestContextTierBudget (GAP-PROV-02, SPEC §22.1): normal and degraded
// profiles keep every tier and sub-cap inside its budget. Degraded mode is the
// <64K window profile: at most eight catalog groups within 200 tokens, and
// facts 60 / workspace 100 / files 200 / memory 120 (pinned inside it) /
// skills 80 / separators 40 inside a 600-token tier.
func TestContextTierBudget(t *testing.T) {
	t.Parallel()
	a := newAssembler(t, identity)
	groups := make([]CatalogGroup, 0, 30)
	for i := range 30 {
		groups = append(groups, CatalogGroup{Name: fmt.Sprintf("g%02d", i), Summary: strings.Repeat("word ", 9)})
	}
	base := SessionView{
		Phase:   PhaseF1,
		Catalog: groups,
		Tools:   fixtureTools(),
		Turn:    a.Freeze(fixtureVolatile()),
		History: []Message{{Role: "user", Content: "hello"}},
	}
	t.Run("normal", func(t *testing.T) { testNormalBudget(t, a, &base) })
	t.Run("degraded", func(t *testing.T) { testDegradedBudget(t, a, &base) })
}

func testNormalBudget(t *testing.T, a *Assembler, base *SessionView) {
	t.Helper()
	v := *base
	v.Context = renderContext(t, a, fixtureContext())
	req, est, err := a.Build(context.Background(), &v)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if est.Catalog > CapCatalog || est.Context > CapContext {
		t.Fatalf("normal tiers over cap: %+v", est)
	}
	if est.Catalog <= CapCatalogDegraded || est.Context <= CapContextDegraded {
		t.Fatalf("normal tiers do not exceed the degraded caps: %+v", est)
	}
	if n := catalogGroups(req.Messages[0].Content); n < 8 || n > CatalogMaxGroups {
		t.Fatalf("normal catalog groups = %d", n)
	}
}

// degradedContextInput fills every degraded slot with many small entries:
// single huge entries are rejected whole by appendEntries, so they cannot show
// whether pinned and ranked share one memory budget.
func degradedContextInput() *ContextInput {
	var memories []Memory
	for i := range 4 {
		memories = append(memories, Memory{Text: fmt.Sprintf("pinned: repository rule %d keeps the review focused and small.", i), Pinned: true})
	}
	for i := range 40 {
		memories = append(memories, Memory{Text: fmt.Sprintf("approved: ranked memory entry %d with a short fact.", i)})
	}
	return &ContextInput{
		Facts:     fillBody("# Environment", "os=linux/amd64 shell=bash cwd=/work/damnbot", CapFactsDegraded),
		Workspace: fillBody("# Workspace", "branch=main dirty=false go=1.26.9 verify=go test ./...", CapWorkspaceDegraded),
		Files:     []ContextFile{{Path: "AGENTS.md"}},
		Reader:    fixtureReader{"AGENTS.md": strings.Repeat("Use read_file, grep and glob before editing. Run go test. ", 800)},
		Retriever: &fixtureRetriever{
			memories: memories,
			skills: []string{
				"code-review-" + strings.Repeat("x", 120),
				"diagnose-" + strings.Repeat("y", 120),
				"tdd-" + strings.Repeat("z", 120),
			},
		},
		Degraded: true,
	}
}

func testDegradedBudget(t *testing.T, a *Assembler, base *SessionView) {
	t.Helper()
	in := degradedContextInput()
	res, err := a.Context(context.Background(), in)
	if err != nil {
		t.Fatalf("Context: %v", err)
	}
	v := *base
	v.Context = res.Text
	v.Degraded = true
	req, est, err := a.Build(context.Background(), &v)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if est.Catalog > CapCatalogDegraded {
		t.Errorf("degraded catalog = %d tokens, cap %d", est.Catalog, CapCatalogDegraded)
	}
	if est.Context > CapContextDegraded {
		t.Errorf("degraded context = %d tokens, cap %d", est.Context, CapContextDegraded)
	}
	if n := catalogGroups(req.Messages[0].Content); n > CatalogMaxGroupsDegraded {
		t.Errorf("degraded catalog groups = %d, max %d", n, CatalogMaxGroupsDegraded)
	}
	assertDegradedSlots(t, a, &res, &est)
	assertDegradedMemory(t, a, &res)
	if est.Context == 0 || est.Catalog == 0 {
		t.Errorf("degraded tiers are empty: %+v", est)
	}
}

// assertDegradedSlots checks the literal GAP-PROV-02 values, independent of
// the implementation constants.
func assertDegradedSlots(t *testing.T, a *Assembler, res *ContextResult, est *Estimate) {
	t.Helper()
	sum := 0
	for _, c := range []struct {
		name string
		text string
		cap  int
	}{
		{"facts", res.Slots.Facts, 60},
		{"workspace", res.Slots.Workspace, 100},
		{"files", res.Slots.Files, 200},
		{"memory (pinned included)", res.Slots.Memory, 120},
		{"skills", res.Slots.Skills, 80},
	} {
		n := a.tokens.Count(c.text)
		sum += n
		if n > c.cap {
			t.Errorf("degraded %s = %d tokens, cap %d", c.name, n, c.cap)
		}
	}
	if sum > 560 {
		t.Errorf("degraded slots = %d tokens, want <= 560 (600 - 40 separators)", sum)
	}
	if sep := est.Context - sum; sep > 40 {
		t.Errorf("degraded separators = %d tokens, cap 40", sep)
	}
}

// assertDegradedMemory proves both pinned and ranked entries share the one
// 120-token budget: the fixture has many small entries, so a vacuous render
// (all entries rejected whole) cannot pass.
func assertDegradedMemory(t *testing.T, a *Assembler, res *ContextResult) {
	t.Helper()
	mem := res.Slots.Memory
	if got := a.tokens.Count(mem); got <= 80 || got > 120 {
		t.Errorf("degraded memory = %d tokens, want 81..120 (one shared budget, nearly full)", got)
	}
	for _, want := range []string{"pinned: repository rule 0", "ranked memory entry 0"} {
		if !strings.Contains(mem, want) {
			t.Errorf("degraded memory missing %q: %q", want, mem)
		}
	}
}

// catalogGroups counts rendered catalog group lines in the catalog block
// (overflow excluded).
func catalogGroups(system string) int {
	i := strings.Index(system, catalogHeader)
	if i < 0 {
		return 0
	}
	block := system[i:]
	if j := strings.Index(block, "\n\n#"); j >= 0 {
		block = block[:j]
	}
	n := 0
	for line := range strings.SplitSeq(block, "\n") {
		if strings.HasPrefix(line, "- ") && !strings.HasPrefix(line, "- +") {
			n++
		}
	}
	return n
}
