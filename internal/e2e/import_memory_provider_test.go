package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportHermesWarnsExternalMemoryProvider(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeHermesHome(t, e)
	cfg := "memory:\n  provider: byterover\n"
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}
	first := importExec(e, "import", "hermes")
	first.RequireOK(t)
	if got := strings.Count(first.Stdout, "warning: memory provider \"byterover\" is not ported"); got != 1 {
		t.Errorf("warning count = %d:\n%s", got, first.Stdout)
	}
	for _, want := range []string{
		"memory: 3 items, 3 imported, 3 changed, 0 skipped",
		"skipped memory.provider: no config writer in F3",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("missing %q:\n%s", want, first.Stdout)
		}
	}
	second := importExec(e, "import", "hermes")
	second.RequireOK(t)
	if got := strings.Count(second.Stdout, "warning: memory provider"); got != 1 {
		t.Errorf("second warning count = %d", got)
	}
	if !strings.Contains(second.Stdout, "memory: 3 items, 3 imported, 0 changed, 0 skipped") {
		t.Errorf("second run not idempotent:\n%s", second.Stdout)
	}
}
