package catalog_test

import (
	"testing"

	"github.com/damnbot/damnbot/internal/skills/catalog"
)

// skcatCounts is the closed per-category corpus of docs/F8-ACCEPTANCE.md §6:
// twelve bundled categories (SKCAT-001..012) and twenty-three optional
// categories (SKCAT-013..035), 58 + 153 = 211 entries.
func skcatCounts() map[string]int {
	return map[string]int{
		"bundled/apple": 4, "bundled/autonomous-ai-agents": 5, "bundled/creative": 10,
		"bundled/devops": 1, "bundled/email": 2, "bundled/media": 3,
		"bundled/note-taking": 1, "bundled/productivity": 14, "bundled/research": 4,
		"bundled/social-media": 1, "bundled/software-development": 12, "bundled/web": 1,

		"optional/autonomous-ai-agents": 7, "optional/blockchain": 3, "optional/communication": 1,
		"optional/creative": 31, "optional/data-science": 1, "optional/devops": 7,
		"optional/dogfood": 1, "optional/email": 1, "optional/finance": 9,
		"optional/gaming": 2, "optional/health": 2, "optional/mcp": 3,
		"optional/migration": 1, "optional/mlops": 36, "optional/payments": 3,
		"optional/productivity": 10, "optional/research": 15, "optional/security": 6,
		"optional/smart-home": 1, "optional/social-media": 1, "optional/software-development": 6,
		"optional/web-development": 5, "optional/yuanbao": 1,
	}
}

// TestF8SkillCatalogCounts adalah agregat §1 "SKILL/SKCAT/PLUG": katalog
// embedded memuat 58 bundled + 153 optional = 211 entri dan setiap baris
// SKCAT-001..035 memiliki kategori dengan jumlah tepat seperti ledger.
func TestF8SkillCatalogCounts(t *testing.T) {
	t.Parallel()
	c := loadCatalog(t)
	if got := c.Count(catalog.KindBundled); got != 58 {
		t.Errorf("bundled = %d, want 58", got)
	}
	if got := c.Count(catalog.KindOptional); got != 153 {
		t.Errorf("optional = %d, want 153", got)
	}
	if got := len(c.Entries()); got != 211 {
		t.Errorf("entries = %d, want 211", got)
	}

	want := skcatCounts()
	cats := c.Categories()
	if len(cats) != len(want) {
		t.Fatalf("categories = %d, want %d", len(cats), len(want))
	}
	for _, cat := range cats {
		key := string(cat.Kind) + "/" + cat.Name
		n, ok := want[key]
		if !ok {
			t.Errorf("unexpected category %s", key)
			continue
		}
		if cat.Count != n {
			t.Errorf("%s has %d skills, want %d", key, cat.Count, n)
		}
		delete(want, key)
	}
	for key := range want {
		t.Errorf("category %s is missing", key)
	}
}
