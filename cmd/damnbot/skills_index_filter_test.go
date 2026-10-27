package main

import (
	"context"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/skills"
)

// indexSkill inserts one row with a stored host requirement.
func indexSkill(t *testing.T, db *ctxdb.Store, name, requires string) {
	t.Helper()
	if _, err := skillstore.Index(context.Background(), db, &skillstore.Row{
		Name: name, Description: name + " description", Body: "body", Path: "/s/" + name + "/SKILL.md",
		SHA256: strings.Repeat("a", 64), Source: "user", RequiresHost: requires,
	}, 0); err != nil {
		t.Fatalf("index %s: %v", name, err)
	}
}

// TestSkillStoreSearchFiltersUnmet (GAP-TOOL-10): the retrieval seam skips a
// skill whose stored requirement this host does not meet, and keeps one that
// meets it (negative control).
func TestSkillStoreSearchFiltersUnmet(t *testing.T) {
	db := openData(t, t.TempDir())
	indexSkill(t, db, "plan9only", `{"os":["plan9"]}`)
	indexSkill(t, db, "here", `{"os":["linux"]}`)
	indexSkill(t, db, "plain", "")
	store := skillStore{db: db, host: skills.Host{
		GOOS: "linux", GOARCH: "amd64",
		LookPath: func(string) (string, error) { return "/bin/x", nil },
	}}
	got, err := store.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	names := map[string]bool{}
	for i := range got {
		names[got[i].Name] = true
	}
	if names["plan9only"] {
		t.Errorf("unmet skill reached search results: %v", names)
	}
	if !names["here"] || !names["plain"] {
		t.Errorf("available skills missing from search results: %v", names)
	}
}

// TestSkillStoreSearchCorruptRequirement: a stored requirement that cannot be
// decoded is unmet (fail closed), never silently available.
func TestSkillStoreSearchCorruptRequirement(t *testing.T) {
	db := openData(t, t.TempDir())
	indexSkill(t, db, "corrupt", `{"colour":true}`)
	store := skillStore{db: db, host: skills.Host{GOOS: "linux", GOARCH: "amd64"}}
	got, err := store.Search(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("Search: %v", err)
	}
	for i := range got {
		if got[i].Name == "corrupt" {
			t.Fatalf("corrupt requirement was treated as available")
		}
	}
}
