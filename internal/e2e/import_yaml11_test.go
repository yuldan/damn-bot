package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeYAML11Home(t *testing.T, e *Env) string {
	t.Helper()
	root := filepath.Join(e.home, ".hermes")
	files := map[string]string{
		"config.yaml":         "model:\n  default: yes\nagent:\n  max_turns: 1:30\nterminal:\n  timeout: 010\n",
		"MEMORY.md":           "- yaml11 memory\n",
		"skills/y11/SKILL.md": "---\nname: yes\ndescription: on\n---\nbody\n",
	}
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

func TestImportHermesYAML11(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeYAML11Home(t, e)
	first := importExec(e, "import", "hermes")
	first.RequireOK(t)
	for _, want := range []string{
		"config: 3 items, 0 imported, 0 changed, 3 skipped",
		"memory: 1 items, 1 imported, 1 changed, 0 skipped",
		"skills: 1 items, 1 imported, 1 changed, 0 skipped",
		"skipped model.default: no config writer in F3",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("missing %q:\n%s", want, first.Stdout)
		}
	}
	second := importExec(e, "import", "hermes")
	second.RequireOK(t)
	for _, want := range []string{
		"memory: 1 items, 1 imported, 0 changed, 0 skipped",
		"skills: 1 items, 1 imported, 0 changed, 0 skipped",
	} {
		if !strings.Contains(second.Stdout, want) {
			t.Errorf("second run missing %q:\n%s", want, second.Stdout)
		}
	}
}
