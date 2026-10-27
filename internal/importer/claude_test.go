package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeClaude(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func claudeImporter(t *testing.T, sink *memSink) *ClaudeImporter {
	t.Helper()
	imp, err := NewClaude(&ClaudeDeps{
		ReadFile: os.ReadFile, WalkDir: filepath.WalkDir, Sink: sink,
	})
	if err != nil {
		t.Fatalf("NewClaude: %v", err)
	}
	return imp
}

func TestExtractClaudeEntries(t *testing.T) {
	t.Parallel()
	text := `# Project
Intro paragraph.

## Rules
- Use tabs
* Run tests
1. Keep commits small

` + "```" + `
ignored code
` + "```" + `
| a | b |
|---|---|
## Notes
Another paragraph.
- Another paragraph.
`
	got := extractMarkdownEntries(text)
	want := []string{
		"Project: Intro paragraph.",
		"Project > Rules: Use tabs",
		"Project > Rules: Run tests",
		"Project > Rules: Keep commits small",
		"Project > Notes: Another paragraph.",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("entries:\n%q\nwant:\n%q", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
	if got := extractMarkdownEntries("# CLAUDE.md\nbody\n"); got[0] != "body" {
		t.Errorf("excluded heading = %q", got[0])
	}
}

func TestImportClaudeWritesMemoryAndSkills(t *testing.T) {
	t.Parallel()
	base := t.TempDir()
	root := filepath.Join(base, "claude")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeClaude(t, root, "settings.json", `{
  "permissions": {"allow": ["Bash(npm run test:*)", "Bash"], "deny": ["Bash(rm:*)"]},
  "mcpServers": {"files": {"command": "npx"}}
}`)
	writeClaude(t, root, "CLAUDE.md", "# Project\nIntro.\n## Rules\n- Use tabs\n")
	writeClaude(t, root, "skills/deploy/SKILL.md", "---\nname: deploy\ndescription: Deploy.\n---\nbody\n")
	writeClaude(t, root, "commands/foo.md", "do things\n")
	sink := newMemSink()
	report, err := claudeImporter(t, sink).Import(context.Background(), root, ClaudeOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertClaudeReport(t, &report, sink)
}

func assertClaudeReport(t *testing.T, report *Report, sink *memSink) {
	t.Helper()
	if report.Config.Total != 1 || report.Config.Imported != 0 || len(report.Config.Skipped) != 1 {
		t.Errorf("settings = %+v", report.Config)
	}
	if report.MCP.Total != 1 || report.MCP.Imported != 0 {
		t.Errorf("mcp = %+v", report.MCP)
	}
	if report.Memory.Imported != 2 || len(sink.memories) != 2 {
		t.Errorf("memory = %+v / %d", report.Memory, len(sink.memories))
	}
	if report.Skills.Imported != 1 || len(sink.skills) != 1 {
		t.Errorf("skills = %+v", report.Skills)
	}
	if report.Cron.Total != 1 || report.Cron.Imported != 0 {
		t.Errorf("commands = %+v", report.Cron)
	}
	if report.Coverage() != 1 || report.Imported() != 3 {
		t.Errorf("coverage = %v, imported = %d", report.Coverage(), report.Imported())
	}
	if !strings.Contains(report.Config.Skipped[0].Reason, "not migrated: no config writer in F3") {
		t.Errorf("settings reason = %q", report.Config.Skipped[0].Reason)
	}
}

func assertCategorySkipped(t *testing.T, report *Report, category string, otherImported int) {
	t.Helper()
	skipped, other := report.Memory, report.Skills
	if category == "skills" {
		skipped, other = report.Skills, report.Memory
	}
	if skipped.Total != 1 || skipped.Skipped[0].Reason != "not selected (--only)" {
		t.Errorf("%s skip = %+v", category, skipped)
	}
	if other.Imported != otherImported {
		t.Errorf("other category = %+v, want %d imported", other, otherImported)
	}
}

func TestImportClaudeBrokenSettingsIsAtomic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeClaude(t, root, "settings.json", "{not json")
	writeClaude(t, root, "CLAUDE.md", "- would import\n")
	sink := newMemSink()
	if _, err := claudeImporter(t, sink).Import(context.Background(), root, ClaudeOptions{}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("broken settings wrote %d items", sink.calls)
	}
}

func assertMemoryCapSkipped(t *testing.T, report *Report) {
	t.Helper()
	capped := false
	for _, skip := range report.Memory.Skipped {
		if strings.Contains(skip.Reason, "memory cap") {
			capped = true
		}
	}
	if report.Memory.Imported != 1 || !capped {
		t.Errorf("memory = %+v", report.Memory)
	}
}

func TestImportClaudeOversizedMemoryIsSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeClaude(t, root, "CLAUDE.md", "- "+strings.Repeat("x", maxMemoryChars+1)+"\n- short\n")
	sink := newMemSink()
	report, err := claudeImporter(t, sink).Import(context.Background(), root, ClaudeOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Memory.Imported != 1 || len(report.Memory.Skipped) != 1 ||
		!strings.Contains(report.Memory.Skipped[0].Reason, "memory cap") {
		t.Errorf("memory = %+v", report.Memory)
	}
}

func TestNewClaudeRejectsMissingDeps(t *testing.T) {
	t.Parallel()
	if _, err := NewClaude(&ClaudeDeps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("NewClaude = %v, want ErrDeps", err)
	}
}
