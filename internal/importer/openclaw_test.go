package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func openClawImporter(t *testing.T, sink *memSink) *OpenClawImporter {
	t.Helper()
	red := agentRedact
	imp, err := NewOpenClaw(&OpenClawDeps{ReadFile: os.ReadFile, WalkDir: filepath.WalkDir, Redact: red, Sink: sink})
	if err != nil {
		t.Fatalf("NewOpenClaw: %v", err)
	}
	return imp
}

func openClawFixture(t *testing.T, root string) {
	t.Helper()
	writeAgentFile(t, root, "openclaw.json", `{
  "cron": {"nightly": {"schedule": "0 3 * * *"}},
  "memory": {"backend": "qmd"},
  "skills": {"entries": {}},
  "hooks": {},
  "mcp": {"servers": {}},
  "sessions": {"ttl": 7}
}`)
	writeAgentFile(t, root, "workspace/MEMORY.md", "# OSC\n- main fact\n")
	writeAgentFile(t, root, "workspace/USER.md", "- user pref SECRET1\n")
	writeAgentFile(t, root, "workspace/memory/a.md", "- same daily\n")
	writeAgentFile(t, root, "workspace/memory/b.md", "- same daily\n- other daily\n")
	writeAgentFile(t, root, "workspace/AGENTS.md", "workspace instructions\n")
	writeAgentFile(t, root, "agents/worker/prompt.md", "not read\n")
	writeAgentFile(t, root, "workspace/skills/ws-skill/SKILL.md", "---\nname: ws-skill\ndescription: Ws.\n---\nbody\n")
	writeAgentFile(t, root, "skills/shared-skill/SKILL.md", "---\nname: shared-skill\ndescription: Shared.\n---\nbody\n")
	writeAgentFile(t, root, "cron/jobs.json", "[]\n")
}

func assertOtherSkips(t *testing.T, report SourceReport) {
	t.Helper()
	for key, reason := range map[string]string{
		"mcp":      "mcp.servers has no Damn-Bot store yet (F3); not imported",
		"sessions": "session import is not available here; not imported",
		"agents/":  "sub-agent definitions have no Damn-Bot target",
	} {
		found := false
		for _, skip := range report.Skipped {
			if skip.Key == key && skip.Reason == reason && skip.Source == SourceConfig {
				found = true
			}
		}
		if !found {
			t.Errorf("missing skip %q: %+v", key, report.Skipped)
		}
	}
}

func TestImportOpenClawWritesMemoryAndSkills(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	openClawFixture(t, root)
	sink := newMemSink()
	report, err := openClawImporter(t, sink).Import(context.Background(), root, OpenClawOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Memory.Imported != 4 || len(sink.memories) != 4 || len(report.Memory.Skipped) != 1 {
		t.Errorf("memory = %+v / %d", report.Memory, len(sink.memories))
	}
	if report.Skills.Imported != 2 || len(sink.skills) != 2 {
		t.Errorf("skills = %+v", report.Skills)
	}
	if report.Cron.Total != 2 || report.Cron.Imported != 0 {
		t.Errorf("cron = %+v", report.Cron)
	}
	if report.Config.Total != 7 || report.Config.Imported != 0 {
		t.Errorf("other = %+v", report.Config)
	}
	assertOtherSkips(t, report.Config)
	if report.Coverage() != 1 || report.Imported() != 6 {
		t.Errorf("coverage = %v, imported = %d", report.Coverage(), report.Imported())
	}
}

func TestImportOpenClawBrokenConfigIsAtomic(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAgentFile(t, root, "openclaw.json", "{not json")
	writeAgentFile(t, root, "workspace/MEMORY.md", "- would import\n")
	sink := newMemSink()
	if _, err := openClawImporter(t, sink).Import(context.Background(), root, OpenClawOptions{}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("broken config wrote %d items", sink.calls)
	}
}

func TestImportOpenClawBadCronShapeIsError(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAgentFile(t, root, "openclaw.json", `{"cron": 3}`)
	writeAgentFile(t, root, "workspace/MEMORY.md", "- would import\n")
	sink := newMemSink()
	if _, err := openClawImporter(t, sink).Import(context.Background(), root, OpenClawOptions{}); !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("bad cron shape wrote %d items", sink.calls)
	}
}

func TestImportOpenClawOversizedMemoryIsSkipped(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeAgentFile(t, root, "workspace/MEMORY.md", "- "+strings.Repeat("x", maxMemoryChars+1)+"\n- short\n")
	sink := newMemSink()
	report, err := openClawImporter(t, sink).Import(context.Background(), root, OpenClawOptions{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	assertMemoryCapSkipped(t, &report)
}

func TestNewOpenClawRejectsMissingDeps(t *testing.T) {
	t.Parallel()
	if _, err := NewOpenClaw(&OpenClawDeps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("NewOpenClaw = %v, want ErrDeps", err)
	}
}
