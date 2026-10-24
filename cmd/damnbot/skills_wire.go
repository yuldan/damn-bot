package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/ctxdb/skillstore"
	"github.com/damnbot/damnbot/internal/memory"
	"github.com/damnbot/damnbot/internal/redact"
	"github.com/damnbot/damnbot/internal/skills"
)

// trustedProjectsFile is the user-managed trust store beside config.toml:
// one absolute project path per line (the CLI command is a follow-up).
// Trust is required for project discovery, so a cloned tree can never load
// skills by being the cwd alone (ADR-168).
const trustedProjectsFile = "trusted-projects"

// skillRoots resolves every local skill root: <config>/skills (user, the
// only trusted location), the discovered project skills of the nearest
// trusted project, then skills.external_dirs. Roots are deduplicated by
// canonical path with the earlier source winning.
func skillRoots(cfg *config.Config, configDir, cwd string) ([]skills.Root, error) {
	home, herr := os.UserHomeDir()
	if herr != nil {
		return nil, fmt.Errorf("home: %w", herr)
	}
	var out []skills.Root
	seen := map[string]bool{}
	add := func(path string, src skills.Source) error {
		key, err := canonicalPath(path, home)
		if err != nil {
			return err
		}
		if !seen[key] {
			seen[key] = true
			out = append(out, skills.Root{Path: path, Source: src})
		}
		return nil
	}
	if configDir != "" {
		if err := add(filepath.Join(configDir, "skills"), skills.SourceUser); err != nil {
			return nil, err
		}
	}
	if cfg.Skills.ProjectDiscovery {
		if err := addProjectRoots(cfg, configDir, cwd, home, add); err != nil {
			return nil, err
		}
	}
	for _, dir := range cfg.Skills.ExternalDirs {
		if err := add(expandHome(dir, home), skills.SourceImport); err != nil {
			return nil, err
		}
	}
	return out, nil
}

// addProjectRoots adds the skill roots of the nearest trusted project: only a
// project named in skills.trusted_projects or in <config>/trusted-projects is
// discovered, so a cloned tree cannot load skills by being the cwd alone.
func addProjectRoots(cfg *config.Config, configDir, cwd, home string, add func(string, skills.Source) error) error {
	trusted, err := trustedProjects(cfg.Skills.TrustedProjects, configDir, home)
	if err != nil {
		return err
	}
	root := nearestTrustedProject(cwd, trusted, home)
	if root == "" {
		return nil
	}
	for _, name := range []string{".damnbot", ".hermes"} {
		if aerr := add(filepath.Join(root, name, "skills"), skills.SourceImport); aerr != nil {
			return aerr
		}
	}
	return nil
}

// loadSkillRoots parses every root into one catalog.
func loadSkillRoots(cfg *config.Config, configDir, cwd string) (skills.Catalog, error) {
	roots, err := skillRoots(cfg, configDir, cwd)
	if err != nil {
		return skills.Catalog{}, err
	}
	cat, err := skills.Load(roots)
	if err != nil {
		return skills.Catalog{}, fmt.Errorf("skills: %w", err)
	}
	return cat, nil
}

// paymentSkillDirs computes the shell-gate directories for every local skill
// source (SPEC §4.8, SEC-15/SEC-26, ADR-133 amendment, ADR-168). Every
// directory is gated regardless of quarantine state because its files are
// already on disk; a skill that cannot be read or is invalid fails the run
// instead of silently leaving its files ungated (fail closed).
func paymentSkillDirs(cfg *config.Config, configDir, cwd string) ([]string, error) {
	cat, err := loadSkillRoots(cfg, configDir, cwd)
	if err != nil {
		return nil, err
	}
	return dedupeDirs(append(skills.PaymentDirs(cat.Skills), cat.Dirs...)), nil
}

// dedupeDirs removes duplicate gate directories in first-seen order.
func dedupeDirs(dirs []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(dirs))
	for _, d := range dirs {
		if !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

// trustedProjects merges the config list with the trust store file. A
// relative or unreadable entry fails closed.
func trustedProjects(configured []string, configDir, home string) (map[string]bool, error) {
	out := map[string]bool{}
	for _, p := range configured {
		key, err := canonicalPath(expandHome(p, home), home)
		if err != nil {
			return nil, fmt.Errorf("skills.trusted_projects: %w", err)
		}
		out[key] = true
	}
	file, err := readTrustedFile(configDir)
	if err != nil {
		return nil, err
	}
	for _, p := range file {
		key, kerr := canonicalPath(p, home)
		if kerr != nil {
			return nil, fmt.Errorf("%s: %w", trustedProjectsFile, kerr)
		}
		out[key] = true
	}
	return out, nil
}

// workspaceTrusted reports whether cwd is inside a project the user
// registered in <config>/trusted-projects (the store behind `damnbot skills
// trust`), compared by canonical path. It is the trust decision for a
// headless run: SPEC §14/§10 make `damnbot run` untrusted unless the path is
// listed, so an empty or unreadable store fails closed, and a workspace
// reached through a symlink is compared by its resolved path.
func workspaceTrusted(cfg *config.Config, configDir, cwd, home string) (bool, error) {
	trusted, err := trustedProjects(cfg.Skills.TrustedProjects, configDir, home)
	if err != nil {
		return false, err
	}
	return nearestTrustedProject(cwd, trusted, home) != "", nil
}

// readTrustedFile reads <config>/trusted-projects; a missing file is empty.
func readTrustedFile(configDir string) ([]string, error) {
	if configDir == "" {
		return nil, nil
	}
	root, err := os.OpenRoot(configDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("skills: config dir: %w", err)
	}
	data, rerr := root.ReadFile(trustedProjectsFile)
	if cerr := root.Close(); cerr != nil {
		return nil, fmt.Errorf("skills: config dir: %w", cerr)
	}
	if errors.Is(rerr, fs.ErrNotExist) {
		return nil, nil
	}
	if rerr != nil {
		return nil, fmt.Errorf("skills: %s: %w", trustedProjectsFile, rerr)
	}
	var out []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if line = strings.TrimSpace(line); line != "" && !strings.HasPrefix(line, "#") {
			out = append(out, line)
		}
	}
	return out, nil
}

// writeTrustedFile rewrites the trust store atomically with a fixed order.

// nearestTrustedProject walks up from cwd and returns the first trusted
// project root; nested repositories stop at their own project.
func nearestTrustedProject(cwd string, trusted map[string]bool, home string) string {
	dir := cwd
	for {
		if key, err := canonicalPath(dir, home); err == nil && trusted[key] {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// canonicalPath is the trust key: absolute, symlinks resolved when the path
// exists, cleaned. A relative entry is resolved against the process working
// directory instead of being rejected, which yields a key that cannot match a
// real workspace (inert, fail closed): the store is hand-editable, so a
// misspelling must not grant trust *and* must not break every run. The CLI
// normalizer (`normalizeTrustedProject`) rejects relative input before it is
// ever written.
func canonicalPath(p, home string) (string, error) {
	p = expandHome(p, home)
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", fmt.Errorf("path %q: %w", p, err)
	}
	if resolved, rerr := filepath.EvalSymlinks(abs); rerr == nil {
		return resolved, nil
	}
	return filepath.Clean(abs), nil
}

// expandHome expands a leading "~" using home.
func expandHome(p, home string) string {
	if home == "" {
		return p
	}
	if p == "~" {
		return home
	}
	if strings.HasPrefix(p, "~/") || strings.HasPrefix(p, `~\`) {
		return filepath.Join(home, p[2:])
	}
	return p
}

// quarantineImportedSkills indexes every skill from an untrusted local root
// (project discovery, skills.external_dirs) through the quarantine path: the
// proposed row is linked to a memory entry and stays invisible to
// skills_list, skill_view and retrieval until the user approves that entry
// (ADR-168). <config>/skills is the only trusted location and is not touched
// here, so its behaviour is unchanged. The returned memory ids exist for the
// approval flow and for tests.
func quarantineImportedSkills(ctx context.Context, db *ctxdb.Store, red *redact.Redactor, cfg *config.Config, configDir, cwd string) ([]int64, error) {
	cat, err := loadSkillRoots(cfg, configDir, cwd)
	if err != nil {
		return nil, err
	}
	var ids []int64
	for i := range cat.Skills {
		sk := &cat.Skills[i]
		if sk.Source.Trusted() {
			continue // <config>/skills keeps the existing trusted path
		}
		scan := memory.ScanText(red, sk.Description+"\n"+sk.Body)
		js, jerr := scan.JSON()
		if jerr != nil {
			return nil, fmt.Errorf("scan imported skill %s: %w", sk.Name, jerr)
		}
		row := &skillstore.Row{
			Name: sk.Name, Description: sk.Description, Body: sk.Body, Path: sk.Path,
			SHA256: sk.SHA256, Source: string(sk.Source),
		}
		memoryID, _, qerr := skillstore.Quarantine(ctx, db, row, string(js), scan.High(), 0)
		if qerr != nil {
			return nil, fmt.Errorf("quarantine imported skill %s: %w", sk.Name, qerr)
		}
		ids = append(ids, memoryID)
	}
	return ids, nil
}
