package skills

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"testing"
)

// TestBuiltinSkillRefs is the GAP-SEC-26 CI rule (ADR-133, ADR-144): every
// builtin skill under <repo>/skills must parse and lint against the builtin
// names, so a dangling `requires`, `skill://` link or script path fails the
// build before it is shipped. The repo has no builtin skills yet, so the rule
// is green but armed; scripts/ci-meta-test.sh proves it fails on a violation.
func TestBuiltinSkillRefs(t *testing.T) {
	t.Parallel()
	root, err := repoRoot()
	if err != nil {
		t.Fatalf("repo root: %v", err)
	}
	base := filepath.Join(root, "skills")
	type loaded struct {
		dir, rel string
		sk       Skill
	}
	var all []loaded
	err = filepath.WalkDir(base, func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if d.IsDir() || d.Name() != "SKILL.md" {
			return nil
		}
		rel, rerr := filepath.Rel(base, path)
		if rerr != nil {
			return rerr
		}
		raw, rerr := os.ReadFile(path) //nolint:gosec // repo-local builtin tree
		if rerr != nil {
			return rerr
		}
		sk, perr := Parse(raw, rel, SourceBuiltin)
		if perr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", rel, perr)
			return nil
		}
		all = append(all, loaded{dir: filepath.Dir(path), rel: rel, sk: sk})
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		t.Log("no builtin skills")
		return
	}
	if err != nil {
		t.Fatalf("walk builtin skills: %v", err)
	}
	known := make(map[string]bool, len(all))
	for _, l := range all {
		known[l.sk.Name] = true
	}
	for _, l := range all {
		fsys, oerr := os.OpenRoot(l.dir)
		if oerr != nil {
			t.Errorf("SKILL-REFS GAGAL %s: %v", l.rel, oerr)
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
		return "", err
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
