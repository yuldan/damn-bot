package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// skipSkillSink declines every skill the way a conflict-aware Sink does.
type skipSkillSink struct{ *memSink }

func (s skipSkillSink) PutSkill(context.Context, *Skill) (bool, error) {
	return false, &SkipError{Reason: "conflict: name already installed"}
}

func TestImportUnsupportedSourcesAreReported(t *testing.T) {
	t.Parallel()
	sink, vault := newMemSink(), &memSecrets{values: map[string]string{}}
	imp, err := New(Deps{ReadFile: os.ReadFile, WalkDir: filepath.WalkDir, Sink: sink, Secrets: vault})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := imp.Import(context.Background(), "testdata/hermes-config", Options{
		Unsupported: map[Source]string{SourceConfig: "no writer (test)", SourceSecrets: "no writer (test)"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sink.calls != 0 || vault.calls != 0 {
		t.Errorf("unsupported source wrote: sink=%d secrets=%d", sink.calls, vault.calls)
	}
	if report.Config.Imported != 0 || len(report.Config.Skipped) != report.Config.Total || report.Config.Total == 0 {
		t.Errorf("config report = %+v", report.Config)
	}
	for _, skip := range report.Config.Skipped {
		if skip.Reason != "no writer (test)" {
			t.Errorf("reason = %q", skip.Reason)
		}
	}
	if report.Secrets.Imported != 0 || len(report.Secrets.Skipped) != report.Secrets.Total {
		t.Errorf("secrets report = %+v", report.Secrets)
	}
	if report.Coverage() != 1 {
		t.Errorf("coverage = %v", report.Coverage())
	}
}

func TestImportUnselectedSourceIsReported(t *testing.T) {
	t.Parallel()
	imp, sink, _ := testImporter(t)
	report, err := imp.Import(context.Background(), "testdata/hermes-memory", Options{
		Unsupported: map[Source]string{SourceMemory: "not selected (--only)"},
	})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Memory.Total != 5 || report.Memory.Imported != 0 || len(report.Memory.Skipped) != 5 {
		t.Errorf("memory report = %+v", report.Memory)
	}
	for _, skip := range report.Memory.Skipped {
		if skip.Reason != "not selected (--only)" {
			t.Errorf("reason = %q", skip.Reason)
		}
	}
	if sink.calls != 0 {
		t.Errorf("unselected source wrote %d items", sink.calls)
	}
}

func TestImportSkipErrorIsReported(t *testing.T) {
	t.Parallel()
	sink := skipSkillSink{newMemSink()}
	imp, err := New(Deps{
		ReadFile: os.ReadFile, WalkDir: filepath.WalkDir,
		Sink: sink, Secrets: &memSecrets{values: map[string]string{}},
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	report, err := imp.Import(context.Background(), "testdata/hermes-skills", Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Skills.Total != 3 || report.Skills.Imported != 0 || len(report.Skills.Skipped) != 3 {
		t.Fatalf("skills report = %+v", report.Skills)
	}
	conflicts := 0
	for _, skip := range report.Skills.Skipped {
		if skip.Reason == "conflict: name already installed" {
			conflicts++
		}
	}
	if conflicts != 2 {
		t.Errorf("conflicts = %d, skips = %+v", conflicts, report.Skills.Skipped)
	}
	if report.Imported()+report.SkippedCount() != report.Total() || report.Coverage() != 1 {
		t.Errorf("accounting = %+v", report)
	}
}

func TestImportSkillCarriesDigest(t *testing.T) {
	t.Parallel()
	report, sink, _ := importFixture(t, "hermes-skills")
	if report.Skills.Total != 3 || report.Skills.Imported != 2 {
		t.Fatalf("skills report = %+v", report.Skills)
	}
	for name, skill := range sink.skills {
		if len(skill.SHA256) != 64 || skill.Origin == "" {
			t.Errorf("%s = %+v", name, skill)
		}
	}
}

func TestImportRejectsOversizedMemory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "MEMORY.md"), strings.Repeat("x", maxMemoryChars+1))
	imp, sink, _ := testImporter(t)
	_, err := imp.Import(context.Background(), root, Options{})
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("oversized entry wrote %d items", sink.calls)
	}
}

func TestImportRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "USER.md"), string([]byte{0xff, 0xfe, 'x'}))
	imp, sink, _ := testImporter(t)
	_, err := imp.Import(context.Background(), root, Options{})
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 {
		t.Errorf("invalid encoding wrote %d items", sink.calls)
	}
}

func TestImportInvalidSkillNameIsReported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	writeTestFile(t, filepath.Join(root, "skills", "evil", "SKILL.md"),
		"---\nname: ../escape\ndescription: bad\n---\nbody\n")
	imp, sink, _ := testImporter(t)
	report, err := imp.Import(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Skills.Total != 1 || report.Skills.Imported != 0 || len(report.Skills.Skipped) != 1 {
		t.Fatalf("skills report = %+v", report.Skills)
	}
	if sink.calls != 0 {
		t.Errorf("invalid name wrote %d items", sink.calls)
	}
}

func TestImportSymlinkedSkillDirectoryIsReported(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	target := t.TempDir()
	writeTestFile(t, filepath.Join(target, "SKILL.md"), "---\nname: linked\ndescription: linked\n---\nbody\n")
	if err := os.MkdirAll(filepath.Join(root, "skills"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "skills", "linked")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	imp, sink, _ := testImporter(t)
	report, err := imp.Import(context.Background(), root, Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(report.Skills.Skipped) != 1 || !strings.Contains(report.Skills.Skipped[0].Reason, "symlink is not followed") {
		t.Fatalf("skips = %+v", report.Skills.Skipped)
	}
	if sink.calls != 0 {
		t.Errorf("followed a symlink: %d calls", sink.calls)
	}
}

func writeTestFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
