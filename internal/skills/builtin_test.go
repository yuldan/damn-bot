package skills

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"testing"
)

// builtinSkill is one parsed file under the builtin root.
type builtinSkill struct {
	dir, rel string
	sk       Skill
}

// TestBuiltinSkillRefs is the GAP-SEC-26 CI rule (ADR-133, ADR-144): every
// builtin skill under <repo>/skills must parse and lint against the builtin
// names, so a dangling `requires`, `skill://` link or script path fails the
// build before it is shipped. The root is committed (skills/.gitkeep) and a
// missing root fails, so a rename cannot silently disarm the rule; the
// compiled-in corpus under internal/skills/bundled is linted by the same test,
// and scripts/ci-meta-test.sh proves an injected skill fails.
func TestBuiltinSkillRefs(t *testing.T) {
	t.Parallel()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	base := filepath.Join(root, "skills")
	all, err := collectBuiltinSkills(base)
	if errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("SKILL-REFS GAGAL: builtin skills root %s is missing", base)
	}
	if err != nil {
		t.Fatalf("builtin skills: %v", err)
	}
	t.Run("repo", func(t *testing.T) { lintBuiltin(t, all) })
	t.Run("embedded", func(t *testing.T) { lintBundled(t, bundledFS) })
}

// lintBundled lints the compiled-in corpus. The corpus must not be empty: the
// whole point of SKILL-016 is that a builtin skill ships with the binary.
func lintBundled(t *testing.T, fsys fs.FS) {
	t.Helper()
	var all []builtinSkill
	err := fs.WalkDir(fsys, "bundled", func(p string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		raw, rerr := fs.ReadFile(fsys, p)
		if rerr != nil {
			return fmt.Errorf("read %s: %w", p, rerr)
		}
		sk, perr := Parse(raw, p, SourceBuiltin)
		if perr != nil {
			return fmt.Errorf("SKILL-REFS GAGAL %s: %w", p, perr)
		}
		all = append(all, builtinSkill{dir: path.Dir(p), rel: p, sk: sk})
		return nil
	})
	if err != nil {
		t.Fatalf("embedded skills: %v", err)
	}
	if len(all) == 0 {
		t.Fatal("SKILL-REFS GAGAL: embedded bundled corpus is empty")
	}
	known := make(map[string]bool, len(all))
	for i := range all {
		known[all[i].sk.Name] = true
	}
	for i := range all {
		sub, serr := fs.Sub(fsys, all[i].dir)
		if serr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", all[i].rel, serr)
			continue
		}
		findings, lerr := Lint(&all[i].sk, LintEnv{Files: sub, Known: func(n string) bool { return known[n] }})
		if lerr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", all[i].rel, lerr)
			continue
		}
		for _, f := range findings {
			t.Errorf("SKILL-REFS GAGAL %s: %s: %s", all[i].rel, f.Category, f.Detail)
		}
	}
}

// collectBuiltinSkills parses every SKILL.md under base.
func collectBuiltinSkills(base string) ([]builtinSkill, error) {
	var all []builtinSkill
	err := filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		rel, err := filepath.Rel(base, path)
		if err != nil {
			return fmt.Errorf("relative %s: %w", path, err)
		}
		raw, err := os.ReadFile(path) //nolint:gosec // repo-local builtin tree
		if err != nil {
			return fmt.Errorf("read %s: %w", path, err)
		}
		sk, perr := Parse(raw, rel, SourceBuiltin)
		if perr != nil {
			return fmt.Errorf("SKILL-REFS GAGAL %s: %w", rel, perr)
		}
		all = append(all, builtinSkill{dir: filepath.Dir(path), rel: rel, sk: sk})
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("walk builtin skills: %w", err)
	}
	return all, nil
}

// lintBuiltin lints each skill through os.Root with the builtin name set.
func lintBuiltin(t *testing.T, all []builtinSkill) {
	t.Helper()
	known := make(map[string]bool, len(all))
	for i := range all {
		known[all[i].sk.Name] = true
	}
	for i := range all {
		l := &all[i]
		fsys, err := os.OpenRoot(l.dir)
		if err != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", l.rel, err)
			continue
		}
		findings, lerr := Lint(&l.sk, LintEnv{Files: fsys.FS(), Known: func(n string) bool { return known[n] }})
		if cerr := fsys.Close(); cerr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: close: %v", l.rel, cerr)
		}
		if lerr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", l.rel, lerr)
			continue
		}
		for _, f := range findings {
			if f.Severity == "high" {
				t.Errorf("SKILL-REFS GAGAL %s: %s: %s", l.rel, f.Category, f.Detail)
			}
		}
	}
}

// repoRoot walks up from the package directory to the go.mod root.
func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("getwd: %w", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", os.ErrNotExist
		}
		dir = parent
	}
}
