package e2e

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCodexFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCodexHome(t *testing.T, e *Env) string {
	t.Helper()
	root := filepath.Join(e.home, ".codex")
	writeCodexFile(t, filepath.Join(root, "config.toml"), `
[mcp_servers.files]
command = "npx"
[mcp_servers.other]
url = "https://x"
`)
	writeCodexFile(t, filepath.Join(root, "AGENTS.md"), "# A\n- agent rule\n")
	writeCodexFile(t, filepath.Join(root, "memories", "a.md"), "- shared fact\n- a rule\n")
	writeCodexFile(t, filepath.Join(root, "memories", "b.md"), "- shared fact\n- b rule\n")
	writeCodexFile(t, filepath.Join(root, "skills", "x", "SKILL.md"),
		"---\nname: x\ndescription: X skill.\n---\nbody\n")
	return root
}

func TestImportCodex(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeCodexHome(t, e)
	first := importExec(e, "import", "codex")
	first.RequireOK(t)
	for _, want := range []string{
		"mcp: 2 items, 0 imported, 0 changed, 2 skipped",
		"memory: 5 items, 4 imported, 4 changed, 1 skipped",
		"skills: 1 items, 1 imported, 1 changed, 0 skipped",
		"skipped memories/b.md[1]: duplicate redacted content",
		"import: 8 items, 5 imported, 5 changed, 3 skipped, 100.0% accounted",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("import output missing %q:\n%s", want, first.Stdout)
		}
	}
	review := e.ExecEnv([]string{"XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")}, "review")
	review.RequireOK(t)
	for _, want := range []string{"A: agent rule", "shared fact", "a rule", "b rule"} {
		if !strings.Contains(review.Stdout, want) {
			t.Errorf("review missing %q:\n%s", want, review.Stdout)
		}
	}
	again := importExec(e, "import", "codex")
	again.RequireOK(t)
	if !strings.Contains(again.Stdout, "memory: 5 items, 4 imported, 0 changed, 1 skipped") ||
		!strings.Contains(again.Stdout, "skills: 1 items, 1 imported, 0 changed, 0 skipped") {
		t.Errorf("second run not idempotent:\n%s", again.Stdout)
	}
	db := openImportDB(t, e)
	if n := countRows(t, db, `SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import'`); n != 4 {
		t.Errorf("imported memory rows = %d, want 4", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM skills WHERE name = 'x' AND source = 'import' AND memory_id IS NOT NULL`); n != 1 {
		t.Errorf("quarantined skills = %d, want 1", n)
	}
}

func TestImportCodexDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeCodexHome(t, e)
	before := hashTree(t, e.home)
	res := importExec(e, "import", "codex", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "memory: 5 items, 4 imported, 0 changed, 1 skipped") ||
		!strings.Contains(res.Stdout, "dry run: nothing was written") {
		t.Errorf("dry run output:\n%s", res.Stdout)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("dry run changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

func TestImportCodexBrokenConfig(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := filepath.Join(e.home, ".codex")
	writeCodexFile(t, filepath.Join(root, "config.toml"), "[mcp_servers.files\ncommand =")
	writeCodexFile(t, filepath.Join(root, "AGENTS.md"), "- would import\n")
	assertRejectedImport(t, e, []string{"import", "codex"}, "invalid source")
}

func TestImportCodexSymlinkEscape(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeCodexHome(t, e)
	addEscapingSkill(t, root)
	assertRejectedImport(t, e, []string{"import", "codex"}, "import codex:")
}

func TestImportCodexOnly(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeCodexHome(t, e)
	memory := importExec(e, "import", "codex", "--only", "memory")
	memory.RequireOK(t)
	if !strings.Contains(memory.Stdout, "memory: 5 items, 4 imported, 4 changed, 1 skipped") {
		t.Errorf("only memory output:\n%s", memory.Stdout)
	}
	if !strings.Contains(memory.Stdout, "skipped (all): not selected (--only)") {
		t.Errorf("unselected skills not reported:\n%s", memory.Stdout)
	}
	for _, category := range []string{"mcp"} {
		res := importExec(e, "import", "codex", "--only", category)
		if res.Code != 2 || !strings.Contains(res.Stderr, "ADR-218") {
			t.Errorf("--only %s = %d %q, want 2 with ADR-218", category, res.Code, res.Stderr)
		}
	}
}

func TestImportCodexUnsupportedOnlyDoesNotWrite(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeCodexHome(t, e)
	before := hashTree(t, e.home)
	if res := importExec(e, "import", "codex", "--only", "mcp"); res.Code != 2 {
		t.Errorf("--only mcp = %d, want 2", res.Code)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("unsupported --only changed state")
	}
	requireNoDataDir(t, e)
	if _, err := os.Stat(importHermesDataDir(e)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unsupported --only wrote data dir: %v", err)
	}
}
