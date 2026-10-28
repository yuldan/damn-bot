package importer

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func importYAML(t *testing.T, config string) (Report, *memSink) {
	t.Helper()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte(config), 0o600); err != nil {
		t.Fatal(err)
	}
	imp, sink, _ := testImporter(t)
	report, err := imp.Import(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	return report, sink
}

func TestImportYAML11(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name  string
		yaml  string
		key   string
		value any
		raw   string
	}{
		{"yes", "model:\n  default: yes\n", "", nil, "yes"},
		{"no", "model:\n  default: no\n", "", nil, "no"},
		{"on", "model:\n  default: ON\n", "", nil, "ON"},
		{"off", "model:\n  default: off\n", "", nil, "off"},
		{"octal", "terminal:\n  timeout: 010\n", "exec.default_timeout_s", 8, ""},
		{"octal-o-string", "model:\n  default: 0o10\n", "provider.model", "0o10", ""},
		{"exponent-string", "model:\n  default: 1e3\n", "provider.model", "1e3", ""},
		{"exponent-dot-string", "model:\n  default: 1.0e3\n", "provider.model", "1.0e3", ""},
		{"sexagesimal", "agent:\n  max_turns: 1:30\n", "loop.max_iterations", 40, ""},
		{"timestamp", "model:\n  default: 2026-01-02\n", "", nil, "2026-01-02"},
		{"sexagesimal-float", "model:\n  default: 1:30.5\n", "", nil, "1:30.5"},
		{"integer", "model:\n  default: 42\n", "", nil, "42"},
		{"quoted", "model:\n  default: \"yes\"\n", "provider.model", "yes", ""},
		{"single-quoted", "model:\n  default: 'on'\n", "provider.model", "on", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			report, sink := importYAML(t, tc.yaml)
			if tc.raw == "" {
				if got := sink.config[tc.key].Value; !reflect.DeepEqual(got, tc.value) {
					t.Fatalf("%s = %#v, want %#v (report %+v)", tc.key, got, tc.value, report)
				}
				return
			}
			if _, ok := sink.config["provider.model"]; ok {
				t.Fatalf("ambiguous %q was imported", tc.raw)
			}
			skip := report.Config.Skipped[0]
			if skip.Key != "model.default" || !strings.Contains(skip.Reason, tc.raw) {
				t.Fatalf("skip = %+v, want original %q", skip, tc.raw)
			}
		})
	}
}

func TestImportYAML11QuotedList(t *testing.T) {
	t.Parallel()
	_, sink := importYAML(t, "skills:\n  external_dirs: [\"yes\", \"010\"]\n")
	want := []string{"yes", "010"}
	if got := sink.config["skills.external_dirs"].Value; !reflect.DeepEqual(got, want) {
		t.Fatalf("external_dirs = %#v, want %#v", got, want)
	}
}

func TestImportYAML11DuplicateKey(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "config.yaml"), []byte("model:\n  default: a\n  default: b\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	imp, _, _ := testImporter(t)
	if _, err := imp.Import(context.Background(), root, Options{}); err == nil {
		t.Fatal("duplicate key accepted")
	}
}

func TestImportYAML11LeavesYAML12Alone(t *testing.T) {
	t.Parallel()
	report, sink := importYAML(t, "model:\n  default: openrouter/anthropic/claude-sonnet-4.5\n")
	if got := sink.config["provider.model"].Value; got != "openrouter/anthropic/claude-sonnet-4.5" {
		t.Fatalf("model = %#v", got)
	}
	if report.Config.Imported != 1 || len(report.Config.Skipped) != 0 {
		t.Fatalf("report = %+v", report.Config)
	}
}

func TestImportYAML11SkillFrontmatterStrings(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	dir := filepath.Join(root, "skills", "yes")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	body := "---\nname: yes\ndescription: on\n---\nbody\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	imp, sink, _ := testImporter(t)
	if _, err := imp.Import(context.Background(), root, Options{}); err != nil {
		t.Fatal(err)
	}
	got, ok := sink.skills["yes"]
	if !ok || got.Description != "on" {
		t.Fatalf("skill = %+v, want name yes description on", sink.skills)
	}
}

func TestImportYAML11Fixture(t *testing.T) {
	t.Parallel()
	report, sink, _ := importFixture(t, "hermes-yaml11")
	if got := sink.config["loop.max_iterations"].Value; got != 40 {
		t.Errorf("loop.max_iterations = %#v", got)
	}
	if got := sink.config["exec.default_timeout_s"].Value; got != 8 {
		t.Errorf("exec.default_timeout_s = %#v", got)
	}
	if got := sink.config["skills.external_dirs"].Value; !reflect.DeepEqual(got, []string{"yes", "010"}) {
		t.Errorf("skills.external_dirs = %#v", got)
	}
	if len(report.Clamps) != 1 || report.Clamps[0].From != 90 || report.Clamps[0].To != 40 {
		t.Errorf("clamps = %+v", report.Clamps)
	}
	if report.Config.Total != 5 || report.Config.Imported != 3 || len(report.Config.Skipped) != 2 {
		t.Errorf("config report = %+v", report.Config)
	}
}
