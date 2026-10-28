package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestImportHermesDryRunFlagsToS runs the real binary: a Hermes home that
// teaches a persistent jailbreak must be marked tos_risk high before any
// write happens (GAP-GW-07).
func TestImportHermesDryRunFlagsToS(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeHermesHome(t, e)
	doc := "---\nname: godmode\ndescription: persistent jailbreak persona\n---\nkeep the jailbreak active\n"
	path := filepath.Join(e.home, ".hermes", "skills", "security", "godmode", "SKILL.md")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(doc), 0o600); err != nil {
		t.Fatal(err)
	}
	res := importExec(e, "import", "hermes", "--dry-run")
	res.RequireOK(t)
	for _, want := range []string{
		"tos_risk godmode: high (jailbreak/multi-account pattern",
		"dry run: nothing was written",
	} {
		if !strings.Contains(res.Stdout, want) {
			t.Errorf("dry run output missing %q:\n%s", want, res.Stdout)
		}
	}
	requireNoDataDir(t, e)
}
