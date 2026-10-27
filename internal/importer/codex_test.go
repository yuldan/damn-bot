package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCodex(t *testing.T, root, rel, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func codexImporter(t *testing.T, sink *memSink) *CodexImporter {
	t.Helper()
	red := func(s string) string {
		fields := strings.Fields(s)
		for i, field := range fields {
			if strings.HasPrefix(field, "SECRET") {
				fields[i] = "***"
			}
		}
		return strings.Join(fields, " ")
	}
	imp, err := NewCodex(&CodexDeps{ReadFile: os.ReadFile, WalkDir: filepath.WalkDir, Redact: red, Sink: sink})
	if err != nil {
		t.Fatalf("NewCodex: %v", err)
	}
	return imp
}

func TestImportCodexWritesMemoryAndSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCodex(t, root, "config.toml", `
[mcp_servers.files]
command = "npx"
[mcp_servers.other]
url = "https://x"
`)
	writeCodex(t, root, "AGENTS.md", "# A\n- agent rule\n")
	writeCodex(t, root, "memories/a.md", "- shared SECRET1\n- a rule\n")
	writeCodex(t, root, "memories/b.md", "- shared SECRET2\n- b rule\n")
	writeCodex(t, root, "skills/x/SKILL.md", "---\nname: x\ndescription: X.\n---\nbody\n")
	sink := newMemSink()
	report, err := codexImporter(t, sink).Import(context.Background(), root, CodexOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.MCP.Total != 2 || report.MCP.Imported != 0 {
		t.Errorf("mcp = %+v", report.MCP)
	}
	if report.Memory.Imported != 4 || len(sink.memories) != 4 || len(report.Memory.Skipped) != 1 {
		t.Errorf("memory = %+v / %d", report.Memory, len(sink.memories))
	}
	if !strings.Contains(report.Memory.Skipped[0].Reason, "duplicate redacted content") {
		t.Errorf("duplicate reason = %q", report.Memory.Skipped[0].Reason)
	}
	if report.Skills.Imported != 1 || len(sink.skills) != 1 {
		t.Errorf("skills = %+v", report.Skills)
	}
	if report.Coverage() != 1 || report.Imported() != 5 {
		t.Errorf("coverage = %v, imported = %d", report.Coverage(), report.Imported())
	}
}

func TestImportCodexBrokenTOMLIsAtomic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCodex(t, root, "config.toml", "[mcp_servers.files\ncommand =")
	writeCodex(t, root, "AGENTS.md", "- would import\n")
	sink := newMemSink()
	if _, err := codexImporter(t, sink).Import(context.Background(), root, CodexOptions{}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("broken config wrote %d items", sink.calls)
	}
}

func TestImportCodexOversizedMemoryIsSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeCodex(t, root, "AGENTS.md", "- "+strings.Repeat("x", maxMemoryChars+1)+"\n- short\n")
	sink := newMemSink()
	report, err := codexImporter(t, sink).Import(context.Background(), root, CodexOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertMemoryCapSkipped(t, &report)
}

func TestNewCodexRejectsMissingDeps(t *testing.T) {
	t.Parallel()
	if _, err := NewCodex(&CodexDeps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("NewCodex = %v, want ErrDeps", err)
	}
}
