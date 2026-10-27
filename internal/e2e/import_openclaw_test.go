package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOpenClawFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeOpenClawHome(t *testing.T, e *Env) string {
	t.Helper()
	root := filepath.Join(e.home, ".openclaw")
	writeOpenClawFile(t, filepath.Join(root, "openclaw.json"), `{
  "cron": {"nightly": {"schedule": "0 3 * * *"}},
  "memory": {"backend": "qmd"},
  "skills": {"entries": {}},
  "hooks": {},
  "mcp": {"servers": {}},
  "sessions": {"ttl": 7}
}`)
	writeOpenClawFile(t, filepath.Join(root, "workspace", "MEMORY.md"), "# OSC\n- main fact\n")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "USER.md"), "- user pref\n")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "memory", "a.md"), "- same daily\n")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "memory", "b.md"), "- same daily\n- other daily\n")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "AGENTS.md"), "workspace instructions\n")
	writeOpenClawFile(t, filepath.Join(root, "agents", "worker", "prompt.md"), "not read\n")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "skills", "ws-skill", "SKILL.md"),
		"---\nname: ws-skill\ndescription: Ws skill.\n---\nbody\n")
	writeOpenClawFile(t, filepath.Join(root, "skills", "shared-skill", "SKILL.md"),
		"---\nname: shared-skill\ndescription: Shared skill.\n---\nbody\n")
	writeOpenClawFile(t, filepath.Join(root, "cron", "jobs.json"), "[]\n")
	return root
}

func TestImportOpenClaw(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeOpenClawHome(t, e)
	first := importExec(e, "import", "openclaw")
	first.RequireOK(t)
	for _, want := range []string{
		"memory: 5 items, 4 imported, 4 changed, 1 skipped",
		"skills: 2 items, 2 imported, 2 changed, 0 skipped",
		"cron: 2 items, 0 imported, 0 changed, 2 skipped",
		"other: 7 items, 0 imported, 0 changed, 7 skipped",
		"skipped workspace/memory/b.md[1]: duplicate redacted content",
		"skipped mcp: mcp.servers has no Damn-Bot store yet (F3); not imported",
		"skipped sessions: session import is not available here; not imported",
		"skipped agents/: sub-agent definitions have no Damn-Bot target",
		"import: 16 items, 6 imported, 6 changed, 10 skipped, 100.0% accounted",
	} {
		if !strings.Contains(first.Stdout, want) {
			t.Errorf("import output missing %q:\n%s", want, first.Stdout)
		}
	}
	review := e.ExecEnv([]string{"XDG_DATA_HOME=" + filepath.Join(e.home, "xdg-data")}, "review")
	review.RequireOK(t)
	for _, want := range []string{"OSC: main fact", "user pref", "same daily", "other daily"} {
		if !strings.Contains(review.Stdout, want) {
			t.Errorf("review missing %q:\n%s", want, review.Stdout)
		}
	}
	again := importExec(e, "import", "openclaw")
	again.RequireOK(t)
	if !strings.Contains(again.Stdout, "memory: 5 items, 4 imported, 0 changed, 1 skipped") ||
		!strings.Contains(again.Stdout, "skills: 2 items, 2 imported, 0 changed, 0 skipped") {
		t.Errorf("second run not idempotent:\n%s", again.Stdout)
	}
	db := openImportDB(t, e)
	if n := countRows(t, db, `SELECT count(*) FROM v_memory_status WHERE kind = 'memory' AND origin = 'import'`); n != 4 {
		t.Errorf("imported memory rows = %d, want 4", n)
	}
	if n := countRows(t, db, `SELECT count(*) FROM skills WHERE source = 'import' AND memory_id IS NOT NULL`); n != 2 {
		t.Errorf("quarantined skills = %d, want 2", n)
	}
}

func TestImportOpenClawDryRun(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeOpenClawHome(t, e)
	assertAgentDryRun(t, e, []string{"import", "openclaw", "--dry-run"},
		"memory: 5 items, 4 imported, 0 changed, 1 skipped")
}

func TestImportOpenClawBrokenConfig(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := filepath.Join(e.home, ".openclaw")
	writeOpenClawFile(t, filepath.Join(root, "openclaw.json"), "{not json")
	writeOpenClawFile(t, filepath.Join(root, "workspace", "MEMORY.md"), "- would import\n")
	assertRejectedImport(t, e, []string{"import", "openclaw"}, "invalid source")
}

func TestImportOpenClawSymlinkEscape(t *testing.T) {
	t.Parallel()
	e := New(t)
	root := writeOpenClawHome(t, e)
	addEscapingSkill(t, root)
	assertRejectedImport(t, e, []string{"import", "openclaw"}, "import openclaw:")
}

func TestImportOpenClawDetection(t *testing.T) {
	t.Parallel()
	t.Run("none", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		res := importExec(e, "import", "openclaw")
		if res.Code == 0 || !strings.Contains(res.Stderr, "no OpenClaw home found") {
			t.Errorf("none = %d %q", res.Code, res.Stderr)
		}
	})
	t.Run("multiple", func(t *testing.T) {
		t.Parallel()
		e := New(t)
		root := writeOpenClawHome(t, e)
		writeOpenClawFile(t, filepath.Join(filepath.Dir(root), ".clawdbot", "openclaw.json"), "{}")
		before := hashTree(t, e.home)
		res := importExec(e, "import", "openclaw")
		if res.Code == 0 || !strings.Contains(res.Stderr, "multiple OpenClaw homes") ||
			!strings.Contains(res.Stderr, ".clawdbot") {
			t.Errorf("multiple = %d %q", res.Code, res.Stderr)
		}
		if after := hashTree(t, e.home); after != before {
			t.Errorf("ambiguous detection changed state")
		}
		requireNoDataDir(t, e)
	})
}

func TestImportOpenClawOnly(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeOpenClawHome(t, e)
	memory := importExec(e, "import", "openclaw", "--only", "memory")
	memory.RequireOK(t)
	if !strings.Contains(memory.Stdout, "memory: 5 items, 4 imported, 4 changed, 1 skipped") {
		t.Errorf("only memory output:\n%s", memory.Stdout)
	}
	if !strings.Contains(memory.Stdout, "skipped (all): not selected (--only)") {
		t.Errorf("unselected skills not reported:\n%s", memory.Stdout)
	}
	if res := importExec(e, "import", "openclaw", "--only", "cron"); res.Code != 2 ||
		!strings.Contains(res.Stderr, "ADR-218") {
		t.Errorf("--only cron = %d %q, want 2 with ADR-218", res.Code, res.Stderr)
	}
}

func TestImportOpenClawUnsupportedOnlyDoesNotWrite(t *testing.T) {
	t.Parallel()
	e := New(t)
	writeOpenClawHome(t, e)
	assertUnsupportedOnlyNoWrite(t, e, []string{"import", "openclaw", "--only", "cron"})
}
