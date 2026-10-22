package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

func writeImportedSkill(t *testing.T, dir, name, desc string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	md := "---\nname: " + name + "\ndescription: " + desc + "\n---\nbody"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(md), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestSkillRootsSources: <config>/skills is the first (trusted) root; project
// discovery and external dirs are untrusted imports, and dedupe keeps the
// first source of a path.
func TestSkillRootsSources(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	configDir := filepath.Join(base, "config")
	ext := filepath.Join(base, "ext")
	cfg := config.Default()
	cfg.Skills.ProjectDiscovery = false
	cfg.Skills.ExternalDirs = []string{ext, filepath.Join(configDir, "skills")}
	roots, err := skillRoots(&cfg, configDir, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(roots) != 2 {
		t.Fatalf("roots = %+v, want 2 (dedupe keeps config/skills as the trusted root)", roots)
	}
	if roots[0].Source != skills.SourceUser || roots[0].Path != filepath.Join(configDir, "skills") {
		t.Fatalf("first root = %+v, want the trusted user root", roots[0])
	}
	if roots[1].Source != skills.SourceImport || roots[1].Path != ext {
		t.Fatalf("second root = %+v, want the import root", roots[1])
	}
}

// TestQuarantineImportedSkills: a skill from a new (untrusted) location is
// quarantined and stays invisible to the skill tools until the user approves
// it, while <config>/skills is never quarantined or indexed by this path
// (negative control for the old, trusted location).
func TestQuarantineImportedSkills(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	base := t.TempDir()
	configDir := filepath.Join(base, "config")
	ext := filepath.Join(base, "ext")
	cfg := config.Default()
	cfg.Skills.ProjectDiscovery = false
	cfg.Skills.ExternalDirs = []string{ext}

	writeImportedSkill(t, filepath.Join(ext, "proj-skill"), "proj-skill", "from the project")
	writeImportedSkill(t, filepath.Join(configDir, "skills", "user-skill"), "user-skill", "from the user dir")

	db, err := ctxdb.Open(ctx, &ctxdb.Options{Dir: filepath.Join(base, "data"), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Error(cerr)
		}
	})

	ids, err := quarantineImportedSkills(ctx, db, redact.New(redact.Options{}), &cfg, configDir, base)
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("quarantined ids = %v, want exactly the imported skill", ids)
	}
	if rows, serr := skillstore.Search(ctx, db, "", 10); serr != nil || len(rows) != 0 {
		t.Fatalf("an unapproved import is visible to the skill tools: %+v, %v", rows, serr)
	}
	// Control: the user approves the linked memory entry and only then does the
	// imported skill become visible, still marked with its import source.
	if aerr := skillstore.Approve(ctx, db, ids[0], 0); aerr != nil {
		t.Fatal(aerr)
	}
	rows, serr := skillstore.Search(ctx, db, "", 10)
	if serr != nil || len(rows) != 1 || rows[0].Name != "proj-skill" || rows[0].Source != string(skills.SourceImport) {
		t.Fatalf("imported skill after approval = %+v, %v", rows, serr)
	}
	if rows[0].Path != filepath.Join(ext, "proj-skill", "SKILL.md") {
		t.Fatalf("imported skill path = %q", rows[0].Path)
	}
}
