package assemble

import (
	"context"
	"fmt"
	"strings"
	"testing"
)

// TestContextTierBudget (GAP-PROV-02, SPEC §22.1): normal and degraded
// profiles keep every tier inside its cap. Degraded mode is the <64K window
// profile: at most eight catalog groups within 200 tokens and a context tier
// within 600.
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
		Context: renderContext(t, a, fixtureContext()),
		Tools:   fixtureTools(),
		Turn:    a.Freeze(fixtureVolatile()),
		History: []Message{{Role: "user", Content: "hello"}},
	}
	_, normal, err := a.Build(context.Background(), &base)
	if err != nil {
		t.Fatalf("Build normal: %v", err)
	}
	if normal.Catalog > CapCatalog || normal.Context > CapContext {
		t.Fatalf("normal tiers over cap: %+v", normal)
	}
	if normal.Catalog <= CapCatalogDegraded || normal.Context <= CapContextDegraded {
		t.Fatalf("normal tiers do not exceed the degraded caps: %+v", normal)
	}

	degraded := base
	degraded.Degraded = true
	_, deg, err := a.Build(context.Background(), &degraded)
	if err != nil {
		t.Fatalf("Build degraded: %v", err)
	}
	if deg.Catalog > CapCatalogDegraded {
		t.Errorf("degraded catalog = %d tokens, cap %d", deg.Catalog, CapCatalogDegraded)
	}
	if deg.Context > CapContextDegraded {
		t.Errorf("degraded context = %d tokens, cap %d", deg.Context, CapContextDegraded)
	}
	if deg.Context == 0 || deg.Catalog == 0 {
		t.Errorf("degraded tiers are empty: %+v", deg)
	}
}
