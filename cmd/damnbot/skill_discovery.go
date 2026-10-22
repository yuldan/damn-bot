package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

// discovery is the result of scanning untrusted skill locations.
type discovery struct {
	list  []skills.Skill // discovered skills (already quarantined)
	extra []string       // skill dirs without SKILL.md, gated anyway
}

// discoverSkills scans external dirs and trusted-project skill dirs. Every
// discovered skill is SourceImport: it is linted, scanned and quarantined, and
// is never treated as a trusted user skill. A duplicate name never overrides a
// config/user skill or an earlier root.
func discoverSkills(ctx context.Context, db *ctxdb.Store, cfg *config.Config, configDir, cwd, home string, red *redact.Redactor) (discovery, error) {
	var out discovery
	known, err := configSkillNames(ctx, configDir, cwd, home)
	if err != nil {
		return out, err
	}
	for _, root := range discoveryRoots(cfg.Skills, cwd, home) {
		list, extra, lerr := loadSkills(ctx, root, skillLoadOptions{
			Source: skills.SourceImport, ProjectDir: cwd, Home: home,
		})
		if lerr != nil {
			return out, fmt.Errorf("skills discovery %s: %w", root, lerr)
		}
		out.extra = append(out.extra, extra...)
		for i := range list {
			sk := list[i]
			if known[sk.Name] {
				continue
			}
			known[sk.Name] = true
			if qerr := quarantineSkill(ctx, db, &sk, red); qerr != nil {
				return out, fmt.Errorf("skills discovery %s: %w", sk.Name, qerr)
			}
			out.list = append(out.list, sk)
		}
	}
	return out, nil
}

// configSkillNames returns the names of skills already installed in the
// config directory (the trusted side of the precedence rule).
func configSkillNames(ctx context.Context, configDir, cwd, home string) (map[string]bool, error) {
	known := map[string]bool{}
	if configDir == "" {
		return known, nil
	}
	list, _, err := loadSkills(ctx, filepath.Join(configDir, "skills"), skillLoadOptions{ProjectDir: cwd, Home: home})
	if err != nil {
		return nil, err
	}
	for i := range list {
		known[list[i].Name] = true
	}
	return known, nil
}

// discoveryRoots expands external dirs and trusted-project skill roots.
func discoveryRoots(cfg config.Skills, cwd, home string) []string {
	var roots []string
	seen := map[string]bool{}
	add := func(p string) {
		p = expandHome(p, home)
		if p == "" || !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	for _, dir := range cfg.ExternalDirs {
		add(dir)
	}
	if cfg.ProjectDiscovery {
		for _, project := range cfg.TrustedProjects {
			project = expandHome(project, home)
			if project == "" {
				continue
			}
			rel, err := filepath.Rel(project, cwd)
			if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				continue
			}
			add(filepath.Join(project, ".damnbot", "skills"))
		}
	}
	return roots
}

// expandHome resolves a leading ~ or ~/ against home (already absolute).
func expandHome(p, home string) string {
	if p == "~" {
		return home
	}
	if rest, ok := strings.CutPrefix(p, "~/"); ok && home != "" {
		return filepath.Join(home, rest)
	}
	return p
}

// quarantineSkill stores one discovered skill as a quarantined import. The
// scan and lint results are recorded in the memory decision; a high scan
// severity rejects it (never retrievable). Re-indexing the same name+sha is
// idempotent.
func quarantineSkill(ctx context.Context, db *ctxdb.Store, sk *skills.Skill, red *redact.Redactor) error {
	var existing int64
	if err := db.Write(ctx, func(tx *ctxdb.Tx) error {
		return tx.Get("SELECT count(*) FROM skills WHERE name = ?1 AND sha256 = ?2", []any{sk.Name, sk.SHA256}, &existing)
	}); err != nil {
		return fmt.Errorf("check existing: %w", err)
	}
	if existing > 0 {
		return nil
	}
	scan := memory.ScanText(red, sk.Description+"\n"+sk.Body)
	result, err := scan.JSON()
	if err != nil {
		return fmt.Errorf("scan result: %w", err)
	}
	row := &skillstore.Row{
		Name: sk.Name, Description: red.String(sk.Description), Body: red.String(sk.Body),
		Path: red.String(sk.Path), SHA256: sk.SHA256, Source: string(sk.Source),
	}
	if _, _, qerr := skillstore.Quarantine(ctx, db, row, result, scan.Severity == memory.SeverityHigh, time.Now().UnixMilli()); qerr != nil {
		return fmt.Errorf("quarantine: %w", qerr)
	}
	return nil
}
