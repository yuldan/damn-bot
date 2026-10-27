package e2e

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClaudeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeClaudeHome(t *testing.T, e *Env) string {
	t.Helper()
	root := filepath.Join(e.home, ".claude")
	writeClaudeFile(t, filepath.Join(root, "settings.json"), `{
  "permissions": {"allow": ["Bash(npm run test:*)", "Bash"], "deny": ["Bash(rm:*)"]},
  "mcpServers": {"files": {"command": "npx"}}
}`)
	writeClaudeFile(t, filepath.Join(root, "CLAUDE.md"), `# Project
Intro paragraph.

## Rules
- Use tabs
- Run tests

`+"```"+`
ignored code
`+"```"+`
| a | b |
|---|---|
## Notes
Another paragraph.
`)
	writeClaudeFile(t, filepath.Join(root, "skills", "deploy", "SKILL.md"),
		"---\nname: deploy\ndescription: Deploy the project.\n---\n# Deploy\nRun it.\n")
	writeClaudeFile(t, filepath.Join(root, "commands", "foo.md"), "do things\n")
	return root
}

func TestImportClaudeCode(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeClaudeHome(t, e)
	first := importExec(e, "import", "claude-code")
	first.RequireOK(t)
	for _, want := range []string{
		"settings: 1 items, 0 imported, 0 changed, 1 skipped",
		"mcp: 1 items, 0 imported, 0 changed, 1 skipped",
		"memory: 4 items, 4 imported, 4 changed, 0 skipped",
		"skills: 1 items, 1 imported, 1 changed, 0 skipped",
		"commands: 1 items, 0 imported, 0 changed, 1 skipped",
		"skipped permissions: not migrated: no config writer in F3",
		"skipped files: MCP store is not available",
		"skipped (all): Claude slash commands have no Damn-Bot equivalent",
		"import: 8 items, 5 imported, 5 changed, 3 skipped, 100.0% accounted",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("import output missing %q:\n%s", want, first.Stdout)
		}
	}
	review := e.ExecEnv([]string{"XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")}, "review")
	review.RequireOK(t)
	for _, want := range []string{"Project: Intro paragraph.", "Project > Rules: Use tabs", "Project > Notes: Another paragraph."} {
		if !strings.Contains(review.Stdout, want) {
			t.Errorf("review missing %q:\n%s", want, review.Stdout)
		}
	}
	// Rerun is idempotent: no duplicate memory or skill rows.
	again := importExec(e, "import", "claude-code")
	again.RequireOK(t)
	if !strings.Contains(again.Stdout, "memory: 4 items, 4 imported, 0 changed, 0 skipped") ||
		!strings.Contains(again.Stdout, "skills: 1 items, 1 imported, 0 changed, 0 skipped") {
		t.Errorf("second run not idempotent:\n%s", again.Stdout)
	}
	db := openImportDB(t, e)
	if n := countRows(t, db, `SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import'`); n != 4 {
		t.Errorf("imported memory rows = %d, want 4", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM skills WHERE name = 'deploy' AND source = 'import' AND memory_id IS NOT NULL`); n != 1 {
		t.Errorf("quarantined skills = %d, want 1", n)
	}
}

func TestImportClaudeCodeDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeClaudeHome(t, e)
	before := hashTree(t, e.home)
	res := importExec(e, "import", "claude-code", "--dry-run")
	res.RequireOK(t)
	if !strings.Contains(res.Stdout, "memory: 4 items, 4 imported, 0 changed, 0 skipped") ||
		!strings.Contains(res.Stdout, "skills: 1 items, 1 imported, 0 changed, 0 skipped") ||
		!strings.Contains(res.Stdout, "dry run: nothing was written") {
		t.Errorf("dry run output:\n%s", res.Stdout)
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("dry run changed state: %s -> %s", before, after)
	}
	requireNoDataDir(t, e)
}

func TestImportClaudeCodeBrokenSettings(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := filepath.Join(e.home, ".claude")
	writeClaudeFile(t, filepath.Join(root, "settings.json"), "{not json")
	writeClaudeFile(t, filepath.Join(root, "CLAUDE.md"), "- would import\n")
	assertRejectedImport(t, e, []string{"import", "claude-code"}, "invalid source")
}

func TestImportClaudeCodeSymlinkEscape(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeClaudeHome(t, e)
	addEscapingSkill(t, root)
	assertRejectedImport(t, e, []string{"import", "claude-code"}, "import claude-code:")
}

func TestImportClaudeCodeOnly(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeClaudeHome(t, e)
	memory := importExec(e, "import", "claude-code", "--only", "memory")
	memory.RequireOK(t)
	if !strings.Contains(memory.Stdout, "memory: 4 items, 4 imported, 4 changed, 0 skipped") {
		t.Errorf("only memory output:\n%s", memory.Stdout)
	}
	if !strings.Contains(memory.Stdout, "skipped (all): not selected (--only)") {
		t.Errorf("unselected skills not reported:\n%s", memory.Stdout)
	}
}

func TestImportClaudeCodeUnsupportedOnly(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeClaudeHome(t, e)
	before := hashTree(t, e.home)
	for _, category := range []string{"settings", "mcp", "commands"} {
		res := importExec(e, "import", "claude-code", "--only", category)
		if res.Code != 2 || !strings.Contains(res.Stderr, "ADR-218") {
			t.Errorf("--only %s = %d %q, want 2 with ADR-218", category, res.Code, res.Stderr)
		}
	}
	if after := hashTree(t, e.home); after != before {
		t.Errorf("unsupported --only changed state")
	}
	requireNoDataDir(t, e)
	if _, err := os.Stat(importHermesDataDir(e)); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("unsupported --only wrote data dir: %v", err)
	}
}
