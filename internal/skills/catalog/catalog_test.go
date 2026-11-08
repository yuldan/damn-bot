package catalog_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/skills/catalog"
)

// loadCatalog is the one loader every test uses; a broken corpus fails there.
func loadCatalog(t *testing.T) *catalog.Catalog {
	t.Helper()
	c, err := catalog.Load()
	if err != nil {
		t.Fatalf("catalog.Load: %v", err)
	}
	return c
}

// TestF8SkillCatalog211 proves the F8 corpus shape: 58 bundled + 153 optional
// = 211 entries, every row valid, every digest recomputing, 35 categories.
func TestF8SkillCatalog211(t *testing.T) {
	t.Parallel()
	c := loadCatalog(t)
	if got := c.Count(catalog.KindBundled); got != catalog.BundledCount {
		t.Errorf("bundled = %d, want %d", got, catalog.BundledCount)
	}
	if got := c.Count(catalog.KindOptional); got != catalog.OptionalCount {
		t.Errorf("optional = %d, want %d", got, catalog.OptionalCount)
	}
	entries := c.Entries()
	if len(entries) != catalog.TotalCount {
		t.Fatalf("entries = %d, want %d", len(entries), catalog.TotalCount)
	}
	seen := make(map[string]bool, len(entries))
	for _, e := range entries {
		if seen[e.Name] {
			t.Errorf("duplicate entry %q", e.Name)
		}
		seen[e.Name] = true
		if e.Version != catalog.Version {
			t.Errorf("%s: version %q", e.Name, e.Version)
		}
		if want := catalog.Digest(e.Path, e.Name, e.Description); e.SHA256 != want {
			t.Errorf("%s: digest %s, want %s", e.Name, e.SHA256, want)
		}
	}
	cats := c.Categories()
	if len(cats) != 35 {
		t.Fatalf("categories = %d, want 35", len(cats))
	}
	total := 0
	for _, cat := range cats {
		total += cat.Count
		if cat.Count == 0 {
			t.Errorf("category %s/%s is empty", cat.Kind, cat.Name)
		}
	}
	if total != catalog.TotalCount {
		t.Errorf("category sum = %d, want %d", total, catalog.TotalCount)
	}
}

// TestF8SkillCatalogClosed is the negative control: an unknown name or
// category, a path outside its root, a traversal segment, a bad version, a
// mismatched hash, and a duplicate name are all rejected, so the catalog has
// no open extension point outside a code and ledger change.
func TestF8SkillCatalogClosed(t *testing.T) {
	t.Parallel()
	c := loadCatalog(t)
	if _, err := c.Lookup("no-such-skill"); !errors.Is(err, catalog.ErrUnknown) {
		t.Errorf("Lookup(unknown) = %v, want ErrUnknown", err)
	}
	if _, err := c.Category(catalog.KindBundled, "no-such-category"); !errors.Is(err, catalog.ErrUnknown) {
		t.Errorf("Category(unknown) = %v, want ErrUnknown", err)
	}
	good := "skills/apple/findmy/SKILL.md"
	digest := catalog.Digest(good, "findmy", "Find devices.")
	rows := []struct {
		name string
		row  string
	}{
		{"unknown root", "elsewhere/apple/findmy/SKILL.md\tfindmy\tFind devices.\t1.0.0\t" + catalog.Digest("elsewhere/apple/findmy/SKILL.md", "findmy", "Find devices.")},
		{"unknown category", "skills/evil/findmy/SKILL.md\tfindmy\tFind devices.\t1.0.0\t" + catalog.Digest("skills/evil/findmy/SKILL.md", "findmy", "Find devices.")},
		{"unknown name", "skills/apple/findmy/SKILL.md\tBad Name\tFind devices.\t1.0.0\t" + digest},
		{"path traversal", "skills/apple/../findmy/SKILL.md\tfindmy\tFind devices.\t1.0.0\t" + catalog.Digest("skills/apple/../findmy/SKILL.md", "findmy", "Find devices.")},
		{"bad version", good + "\tfindmy\tFind devices.\t9\t" + digest},
		{"bad digest", good + "\tfindmy\tFind devices.\t1.0.0\t" + strings.Repeat("0", 64)},
		{"missing field", good + "\tfindmy\tFind devices."},
	}
	for _, tc := range rows {
		if _, err := catalog.Parse([]byte(tc.row + "\n")); err == nil {
			t.Errorf("%s: Parse accepted a row outside the closed catalog", tc.name)
		}
	}
	dup := good + "\tfindmy\tFind devices.\t1.0.0\t" + digest + "\n"
	if _, err := catalog.Parse([]byte(dup + dup)); err == nil {
		t.Error("Parse accepted a duplicate name")
	}
}

// TestCatalogMatchesBenchTSV ties the embedded copy to the pinned upstream
// frontmatter snapshot, so a drifted corpus is a test failure, not silent rot.
func TestCatalogMatchesBenchTSV(t *testing.T) {
	t.Parallel()
	c := loadCatalog(t)
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "bench", "evals", "sources", "hermes-skills.tsv"))
	if err != nil {
		t.Fatalf("read bench TSV: %v", err)
	}
	var want []string
	for line := range strings.Lines(string(raw)) {
		if line = strings.TrimSpace(line); line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		fields := strings.Split(line, "\t")
		want = append(want, fields[0]+"\t"+fields[1]+"\t"+fields[2])
	}
	entries := c.Entries()
	if len(want) != len(entries) {
		t.Fatalf("bench rows = %d, catalog = %d", len(want), len(entries))
	}
	for i, e := range entries {
		if got := e.Path + "\t" + e.Name + "\t" + e.Description; got != want[i] {
			t.Errorf("row %d drift:\n got %q\nwant %q", i, got, want[i])
		}
	}
}

// checkSKCAT asserts one SKCAT row: the category exists, its count matches the
// ledger, every member belongs to it, and its digest recomputes.
func checkSKCAT(t *testing.T, kind catalog.Kind, name string, want int) {
	t.Helper()
	c := loadCatalog(t)
	list, err := c.Category(kind, name)
	if err != nil {
		t.Fatalf("Category(%s/%s): %v", kind, name, err)
	}
	if len(list) != want {
		t.Fatalf("%s/%s = %d skills, want %d", kind, name, len(list), want)
	}
	for _, e := range list {
		if e.Kind != kind || e.Category != name {
			t.Errorf("%s listed under %s/%s", e.Name, e.Kind, e.Category)
		}
		if e.SHA256 != catalog.Digest(e.Path, e.Name, e.Description) {
			t.Errorf("%s: digest mismatch", e.Name)
		}
	}
}

func TestF8SKCAT001Apple(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "apple", 4)
}

func TestF8SKCAT002AutonomousAIAgents(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "autonomous-ai-agents", 5)
}

func TestF8SKCAT003Creative(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "creative", 10)
}

func TestF8SKCAT004Devops(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "devops", 1)
}

func TestF8SKCAT005Email(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "email", 2)
}

func TestF8SKCAT006Media(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "media", 3)
}

func TestF8SKCAT007NoteTaking(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "note-taking", 1)
}

func TestF8SKCAT008Productivity(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "productivity", 14)
}

func TestF8SKCAT009Research(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "research", 4)
}

func TestF8SKCAT010SocialMedia(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "social-media", 1)
}

func TestF8SKCAT011SoftwareDevelopment(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "software-development", 12)
}

func TestF8SKCAT012Web(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindBundled, "web", 1)
}

func TestF8SKCAT013OptionalAutonomousAIAgents(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "autonomous-ai-agents", 7)
}

func TestF8SKCAT014OptionalBlockchain(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "blockchain", 3)
}

func TestF8SKCAT015OptionalCommunication(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "communication", 1)
}

func TestF8SKCAT016OptionalCreative(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "creative", 31)
}

func TestF8SKCAT017OptionalDataScience(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "data-science", 1)
}

func TestF8SKCAT018OptionalDevops(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "devops", 7)
}

func TestF8SKCAT019OptionalDogfood(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "dogfood", 1)
}

func TestF8SKCAT020OptionalEmail(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "email", 1)
}

func TestF8SKCAT021OptionalFinance(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "finance", 9)
}

func TestF8SKCAT022OptionalGaming(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "gaming", 2)
}

func TestF8SKCAT023OptionalHealth(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "health", 2)
}

func TestF8SKCAT024OptionalMCP(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "mcp", 3)
}

func TestF8SKCAT025OptionalMigration(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "migration", 1)
}

func TestF8SKCAT026OptionalMLOps(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "mlops", 36)
}

func TestF8SKCAT027OptionalPayments(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "payments", 3)
}

func TestF8SKCAT028OptionalProductivity(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "productivity", 10)
}

func TestF8SKCAT029OptionalResearch(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "research", 15)
}

func TestF8SKCAT030OptionalSecurity(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "security", 6)
}

func TestF8SKCAT031OptionalSmartHome(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "smart-home", 1)
}

func TestF8SKCAT032OptionalSocialMedia(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "social-media", 1)
}

func TestF8SKCAT033OptionalSoftwareDevelopment(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "software-development", 6)
}

func TestF8SKCAT034OptionalWebDevelopment(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "web-development", 5)
}

func TestF8SKCAT035OptionalYuanbao(t *testing.T) {
	t.Parallel()
	checkSKCAT(t, catalog.KindOptional, "yuanbao", 1)
}
