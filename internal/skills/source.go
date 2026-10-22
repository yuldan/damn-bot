package skills

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
)

// Root is one directory whose immediate subdirectories are skills. Source is
// the provenance every skill under it gets: only [SourceUser] and
// [SourceBuiltin] are trusted, so project and external roots use
// [SourceImport] and reach the index through quarantine (ADR-168).
type Root struct {
	Path   string
	Source Source
}

// Catalog is every skill found under a set of roots.
type Catalog struct {
	Skills []Skill
	// Dirs lists every skill and non-skill directory that was scanned, for
	// the payment gate: a directory without SKILL.md is gated too because
	// its files are still reachable (SEC10-13).
	Dirs []string
}

// LoadOptions is the SKILL-004 preprocessing seam (ADR-168). When InlineShell
// is set, a `!`cmd` span is expanded for trusted sources only; untrusted roots
// get a nil runner and therefore fail closed (ErrInlineShellDisabled) instead
// of leaking the literal command. Shell nil means inline shell is unavailable.
type LoadOptions struct {
	InlineShell bool
	Shell       ShellRunner
	Vars        PreprocessVars
}

// preprocessLoaded expands SKILL-004 templates for one loaded skill. The
// inline shell runs only for a trusted source with the config flag on.
func preprocessLoaded(ctx context.Context, sk *Skill, opts LoadOptions) error {
	shell := opts.Shell
	if !opts.InlineShell || !sk.Source.Trusted() {
		shell = nil
	}
	v := opts.Vars
	v.SkillDir, v.SkillName = filepath.Dir(sk.Path), sk.Name
	body, err := Preprocess(ctx, sk.Body, v, shell)
	if err != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	sk.Body = body
	return nil
}

// Load parses and lints every skill under roots in order. A missing root is
// empty (a project need not ship skills). A name must be unique across all
// roots: an import may never shadow a user skill, so a duplicate fails
// closed. An unreadable or invalid skill, or a high lint finding, fails the
// load for every source.
func Load(ctx context.Context, roots []Root, opts LoadOptions) (Catalog, error) {
	var cat Catalog
	known := map[string]bool{}
	for _, r := range roots {
		if err := loadRoot(r, &cat, known); err != nil {
			return Catalog{}, err
		}
	}
	for i := range cat.Skills {
		if err := lintLoaded(&cat.Skills[i], known); err != nil {
			return Catalog{}, err
		}
		if err := preprocessLoaded(ctx, &cat.Skills[i], opts); err != nil {
			return Catalog{}, err
		}
	}
	return cat, nil
}

// loadRoot appends the skills and gated directories of one root.
func loadRoot(root Root, cat *Catalog, known map[string]bool) error {
	entries, err := os.ReadDir(root.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("skills: %w", err)
	}
	for _, e := range entries {
		dir := filepath.Join(root.Path, e.Name())
		info, serr := os.Stat(dir) // follows a symlinked skill directory (SEC10-10)
		if serr != nil {
			return fmt.Errorf("skills: %s: %w", e.Name(), serr)
		}
		if !info.IsDir() {
			continue
		}
		sk, ok, lerr := loadOne(dir, root.Source)
		if lerr != nil {
			return lerr
		}
		if !ok {
			cat.Dirs = append(cat.Dirs, dir)
			continue
		}
		if known[sk.Name] {
			return fmt.Errorf("%w: duplicate skill name %q in %s", ErrInvalid, sk.Name, root.Path)
		}
		known[sk.Name] = true
		cat.Skills = append(cat.Skills, sk)
		cat.Dirs = append(cat.Dirs, dir)
	}
	return nil
}

// loadOne parses one skill directory; ok is false when it is not a skill.
func loadOne(dir string, source Source) (sk Skill, ok bool, err error) {
	r, err := os.OpenRoot(dir) // SEC10-12: SKILL.md cannot symlink outside
	if errors.Is(err, fs.ErrNotExist) {
		return sk, false, nil
	}
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", filepath.Base(dir), err)
	}
	data, rerr := r.ReadFile("SKILL.md")
	if cerr := r.Close(); cerr != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", filepath.Base(dir), cerr)
	}
	if errors.Is(rerr, fs.ErrNotExist) {
		return sk, false, nil
	}
	if rerr != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", filepath.Base(dir), rerr)
	}
	sk, err = Parse(data, filepath.Join(dir, "SKILL.md"), source)
	if err != nil {
		return sk, false, fmt.Errorf("skills: %s: %w", filepath.Base(dir), err)
	}
	return sk, true, nil
}

// lintLoaded lints one loaded skill through os.Root, records file-level
// payment findings on the skill (SEC10-9) and rejects a high finding,
// matching Service.Install for a trusted source.
func lintLoaded(sk *Skill, known map[string]bool) error {
	root, err := os.OpenRoot(filepath.Dir(sk.Path))
	if err != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, err)
	}
	findings, lerr := Lint(sk, LintEnv{Files: root.FS(), Known: func(n string) bool { return known[n] }})
	if cerr := root.Close(); cerr != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, cerr)
	}
	if lerr != nil {
		return fmt.Errorf("skills: %s: %w", sk.Name, lerr)
	}
	sk.AddFindings(findings)
	for _, f := range findings {
		if f.Severity == "high" {
			return fmt.Errorf("%w: %s: %s", ErrInvalid, sk.Name, f.Category)
		}
	}
	return nil
}
