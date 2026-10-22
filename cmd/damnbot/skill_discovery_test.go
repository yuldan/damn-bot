package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

// discoveryStore opens a temp ctxdb for quarantine assertions.
func discoveryStore(t *testing.T) *ctxdb.Store {
	t.Helper()
	db, err := ctxdb.Open(context.Background(), &ctxdb.Options{Dir: t.TempDir(), Kind: "run"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if cerr := db.Close(); cerr != nil {
			t.Errorf("close store: %v", cerr)
		}
	})
	return db
}

// writeDiscoverySkill writes one SKILL.md below root/<name>.
func writeDiscoverySkill(t *testing.T, root, name, body string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	doc := "---\nname: " + name + "\ndescription: " + name + " from disk\n---\n" + body
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
}

// TestDiscoverExternalDirs: discovered skills are imports, quarantined and
// idempotent; duplicates never override config skills.
func TestDiscoverExternalDirs(t *testing.T) {
	external := t.TempDir()
	writeDiscoverySkill(t, external, "ext-skill", "body")
	configDir := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(configDir, "skills"), "ext-skill", "user body")
	db := discoveryStore(t)
	cfg := config.Default()
	cfg.Skills.ExternalDirs = []string{external}
	disc, err := discoverSkills(context.Background(), db, &cfg, configDir, t.TempDir(), t.TempDir(), redact.New(redact.Options{}))
	if err != nil {
		t.Fatal(err)
	}
	if len(disc.list) != 0 {
		t.Fatalf("config duplicate was rediscovered: %+v", disc.list)
	}
	if rmErr := os.RemoveAll(filepath.Join(configDir, "skills", "ext-skill")); rmErr != nil {
		t.Fatal(rmErr)
	}
	disc, err = discoverSkills(context.Background(), db, &cfg, configDir, t.TempDir(), t.TempDir(), redact.New(redact.Options{}))
	if err != nil || len(disc.list) != 1 || disc.list[0].Source != skills.SourceImport {
		t.Fatalf("discovery = %+v, %v", disc, err)
	}
	var source, status string
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		if err := tx.Get("SELECT source FROM skills WHERE name = 'ext-skill'", nil, &source); err != nil {
			return fmt.Errorf("source: %w", err)
		}
		if err := tx.Get(`SELECT status FROM v_memory_status WHERE id = (SELECT memory_id FROM skills WHERE name = 'ext-skill')`, nil, &status); err != nil {
			return fmt.Errorf("status: %w", err)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if source != "import" || status != "scanned" {
		t.Fatalf("stored source/status = %q/%q, want import/scanned", source, status)
	}
	// A second discovery is idempotent.
	if _, err := discoverSkills(context.Background(), db, &cfg, configDir, t.TempDir(), t.TempDir(), redact.New(redact.Options{})); err != nil {
		t.Fatal(err)
	}
	var rows int64
	if err := db.Write(context.Background(), func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT count(*) FROM skills WHERE name = 'ext-skill'", nil, &rows)
	}); err != nil || rows != 1 {
		t.Fatalf("skills rows = %d, %v; want 1", rows, err)
	}
}

// TestDiscoverUntrustedInlineShell: discovered skills never run inline shell,
// even when the config enables it for trusted skills.
func TestDiscoverUntrustedInlineShell(t *testing.T) {
	external := t.TempDir()
	writeDiscoverySkill(t, external, "shell-skill", "run `!echo pwn`")
	db := discoveryStore(t)
	cfg := config.Default()
	cfg.Skills.ExternalDirs = []string{external}
	cfg.Skills.InlineShell = true
	if _, err := discoverSkills(context.Background(), db, &cfg, t.TempDir(), t.TempDir(), t.TempDir(), redact.New(redact.Options{})); err == nil {
		t.Fatal("untrusted inline shell was accepted")
	}
}

// TestDiscoverTrustedProject: project skills are scanned only when the project
// is listed and the cwd is inside it.
func TestDiscoverTrustedProject(t *testing.T) {
	project := t.TempDir()
	writeDiscoverySkill(t, filepath.Join(project, ".damnbot", "skills"), "proj-skill", "body")
	cwd := filepath.Join(project, "sub")
	if err := os.MkdirAll(cwd, 0o700); err != nil {
		t.Fatal(err)
	}
	db := discoveryStore(t)
	cfg := config.Default()
	if disc, err := discoverSkills(context.Background(), db, &cfg, t.TempDir(), cwd, t.TempDir(), redact.New(redact.Options{})); err != nil || len(disc.list) != 0 {
		t.Fatalf("untrusted project discovered = %+v, %v", disc, err)
	}
	cfg.Skills.TrustedProjects = []string{project}
	disc, err := discoverSkills(context.Background(), db, &cfg, t.TempDir(), cwd, t.TempDir(), redact.New(redact.Options{}))
	if err != nil || len(disc.list) != 1 || disc.list[0].Name != "proj-skill" {
		t.Fatalf("trusted project = %+v, %v", disc, err)
	}
}
