package importer

import (
	"context"
	"testing"
)

// agentUnderTest lets one suite cover the dry-run and --only contract shared by
// the agent importers without duplicating their typed option literals.
type agentUnderTest interface {
	Dry() (Report, error)
	Skip() (Report, error)
}

type claudeUnderTest struct {
	t    *testing.T
	root string
	sink *memSink
}

func (a claudeUnderTest) Dry() (Report, error) {
	return claudeImporter(a.t, a.sink).Import(context.Background(), a.root, ClaudeOptions{DryRun: true})
}

func (a claudeUnderTest) Skip() (Report, error) {
	return claudeImporter(a.t, a.sink).Import(context.Background(), a.root,
		ClaudeOptions{Skip: map[string]string{"memory": "not selected (--only)"}})
}

type codexUnderTest struct {
	t    *testing.T
	root string
	sink *memSink
}

func (a codexUnderTest) Dry() (Report, error) {
	return codexImporter(a.t, a.sink).Import(context.Background(), a.root, CodexOptions{DryRun: true})
}

func (a codexUnderTest) Skip() (Report, error) {
	return codexImporter(a.t, a.sink).Import(context.Background(), a.root,
		CodexOptions{Skip: map[string]string{"skills": "not selected (--only)"}})
}

func assertDryRunMemorySkills(t *testing.T, sink *memSink, report *Report, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("drop: %v", err)
	}
	if sink.calls != 0 || report.Memory.Imported != 2 || report.Skills.Imported != 1 || report.Changed() != 0 {
		t.Errorf("dry run = %+v, calls=%d", report, sink.calls)
	}
}

func TestAgentDryRunAndSkip(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name      string
		setup     func(*testing.T, string)
		agent     func(*testing.T, string, *memSink) agentUnderTest
		skipCat   string
		otherImpr int
	}{
		{
			name: "claude-code",
			setup: func(t *testing.T, root string) {
				writeClaude(t, root, "CLAUDE.md", "# P\n- one\n- two\n")
				writeClaude(t, root, "skills/x/SKILL.md", "---\nname: x\ndescription: X.\n---\nbody\n")
			},
			agent:     func(t *testing.T, root string, sink *memSink) agentUnderTest { return claudeUnderTest{t, root, sink} },
			skipCat:   "memory",
			otherImpr: 1,
		},
		{
			name: "codex",
			setup: func(t *testing.T, root string) {
				writeCodex(t, root, "AGENTS.md", "- one\n- two\n")
				writeCodex(t, root, "skills/x/SKILL.md", "---\nname: x\ndescription: X.\n---\nbody\n")
			},
			agent:     func(t *testing.T, root string, sink *memSink) agentUnderTest { return codexUnderTest{t, root, sink} },
			skipCat:   "skills",
			otherImpr: 2,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			tc.setup(t, root)
			sink := newMemSink()
			a := tc.agent(t, root, sink)
			report, err := a.Dry()
			assertDryRunMemorySkills(t, sink, &report, err)
			report, err = a.Skip()
			if err != nil {
				t.Fatalf("skip: %v", err)
			}
			assertCategorySkipped(t, &report, tc.skipCat, tc.otherImpr)
		})
	}
}
