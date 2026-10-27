package importer

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

type memSink struct {
	config   map[string]ConfigEntry
	skills   map[string]Skill
	memories map[string]MemoryEntry
	mcp      map[string]MCPServer
	cron     map[string]CronJob
	calls    int
}

func newMemSink() *memSink {
	return &memSink{
		config:   map[string]ConfigEntry{},
		skills:   map[string]Skill{},
		memories: map[string]MemoryEntry{},
		mcp:      map[string]MCPServer{},
		cron:     map[string]CronJob{},
	}
}

func (m *memSink) PutConfig(_ context.Context, e ConfigEntry) (bool, error) {
	m.calls++
	key := e.Section + "." + e.Key
	if old, ok := m.config[key]; ok && reflect.DeepEqual(old, e) {
		return false, nil
	}
	m.config[key] = e
	return true, nil
}

func (m *memSink) PutSkill(_ context.Context, s *Skill) (bool, error) {
	m.calls++
	if old, ok := m.skills[s.Name]; ok && reflect.DeepEqual(old, *s) {
		return false, nil
	}
	m.skills[s.Name] = *s
	return true, nil
}

func (m *memSink) PutMemory(_ context.Context, e MemoryEntry) (bool, error) {
	m.calls++
	key := e.Store + "\x00" + e.Text
	if _, ok := m.memories[key]; ok {
		return false, nil
	}
	m.memories[key] = e
	return true, nil
}

func (m *memSink) PutMCPServer(_ context.Context, s MCPServer) (bool, error) { //nolint:gocritic // Sink interface signature
	m.calls++
	if old, ok := m.mcp[s.Name]; ok && reflect.DeepEqual(old, s) {
		return false, nil
	}
	m.mcp[s.Name] = s
	return true, nil
}

func (m *memSink) PutCron(_ context.Context, j CronJob) (bool, error) { //nolint:gocritic // Sink interface signature
	m.calls++
	if old, ok := m.cron[j.ID]; ok && reflect.DeepEqual(old, j) {
		return false, nil
	}
	m.cron[j.ID] = j
	return true, nil
}

type memSecrets struct {
	values map[string]string
	calls  int
}

func (m *memSecrets) Put(_ context.Context, name, value string) (bool, error) {
	m.calls++
	if old, ok := m.values[name]; ok && old == value {
		return false, nil
	}
	m.values[name] = value
	return true, nil
}

func testImporter(t *testing.T) (*Importer, *memSink, *memSecrets) {
	t.Helper()
	sink, secrets := newMemSink(), &memSecrets{values: map[string]string{}}
	imp, err := New(Deps{
		ReadFile: os.ReadFile,
		WalkDir:  filepath.WalkDir,
		Sink:     sink,
		Secrets:  secrets,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	return imp, sink, secrets
}

func importFixture(t *testing.T, name string) (Report, *memSink, *memSecrets) {
	t.Helper()
	imp, sink, secrets := testImporter(t)
	report, err := imp.Import(context.Background(), filepath.Join("testdata", name), Options{})
	if err != nil {
		t.Fatalf("Import(%s): %v", name, err)
	}
	return report, sink, secrets
}

func TestNewRejectsMissingDeps(t *testing.T) {
	t.Parallel()
	if _, err := New(Deps{}); !errors.Is(err, ErrDeps) {
		t.Fatalf("New(Deps{}) = %v, want ErrDeps", err)
	}
}

func TestImportConfig(t *testing.T) {
	t.Parallel()
	report, sink, secrets := importFixture(t, "hermes-config")
	if report.Config.Total != 14 || report.Config.Imported != 5 || len(report.Config.Skipped) != 9 {
		t.Fatalf("config report = %+v", report.Config)
	}
	want := map[string]any{
		"provider.model":         "openrouter/anthropic/claude-sonnet-4.5",
		"loop.max_iterations":    40,
		"exec.default_timeout_s": 600,
		"memory.total_max_chars": 2200,
	}
	for key, value := range want {
		if got := sink.config[key].Value; !reflect.DeepEqual(got, value) {
			t.Errorf("%s = %#v, want %#v", key, got, value)
		}
	}
	if got := sink.config["skills.external_dirs"].Value; !reflect.DeepEqual(got, []string{"~/.agents/skills", "/srv/team-skills"}) {
		t.Errorf("skills.external_dirs = %#v", got)
	}
	if len(report.Clamps) != 2 || report.Clamps[0].Key != "exec.default_timeout_s" ||
		report.Clamps[0].From != 900 || report.Clamps[0].To != 600 {
		t.Errorf("clamps = %+v", report.Clamps)
	}
	if report.Secrets.Imported != 1 || secrets.values["hermes.model.api_key"] == "" {
		t.Errorf("secrets = %+v/%v", report.Secrets, secrets.values)
	}
	if report.Coverage() != 1 {
		t.Errorf("coverage = %v", report.Coverage())
	}
}

func TestImportAccounted(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hermes-config"} {
		report, _, _ := importFixture(t, name)
		if report.Coverage() < 0.99 {
			t.Errorf("%s: coverage = %v", name, report.Coverage())
		}
		if report.Imported()+report.SkippedCount() != report.Total() {
			t.Errorf("%s: imported %d + skipped %d != total %d", name, report.Imported(), report.SkippedCount(), report.Total())
		}
		for _, source := range []SourceReport{report.Config, report.Skills, report.Memory, report.MCP, report.Cron, report.Secrets} {
			for _, skip := range source.Skipped {
				if skip.Source == "" || skip.Key == "" || skip.Reason == "" {
					t.Errorf("%s: incomplete skip %+v", name, skip)
				}
			}
		}
	}
}

func TestImportIdempotent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hermes-config"} {
		imp, sink, _ := testImporter(t)
		root := filepath.Join("testdata", name)
		first, err := imp.Import(context.Background(), root, Options{})
		if err != nil {
			t.Fatalf("%s first: %v", name, err)
		}
		second, err := imp.Import(context.Background(), root, Options{})
		if err != nil {
			t.Fatalf("%s second: %v", name, err)
		}
		if first.Changed() == 0 || second.Changed() != 0 {
			t.Errorf("%s changed = %d then %d", name, first.Changed(), second.Changed())
		}
		if first.Imported() != second.Imported() || first.SkippedCount() != second.SkippedCount() {
			t.Errorf("%s reports differ: %+v vs %+v", name, first, second)
		}
		if sink.calls == 0 {
			t.Errorf("%s: no sink calls", name)
		}
	}
}

func TestImportDryRun(t *testing.T) {
	t.Parallel()
	imp, sink, secrets := testImporter(t)
	report, err := imp.Import(context.Background(), "testdata/hermes-config", Options{DryRun: true})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if sink.calls != 0 || secrets.calls != 0 {
		t.Errorf("dry run wrote: sink=%d secrets=%d", sink.calls, secrets.calls)
	}
	if report.Imported() == 0 || report.Changed() != 0 {
		t.Errorf("dry run report = %+v", report)
	}
}

func TestImportBrokenIsAtomic(t *testing.T) {
	t.Parallel()
	imp, sink, secrets := testImporter(t)
	_, err := imp.Import(context.Background(), "testdata/hermes-broken", Options{})
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 || secrets.calls != 0 {
		t.Errorf("partial import: sink=%d secrets=%d", sink.calls, secrets.calls)
	}
}

func TestImportMissingSources(t *testing.T) {
	t.Parallel()
	imp, _, _ := testImporter(t)
	report, err := imp.Import(context.Background(), filepath.Join("testdata", "hermes-empty"), Options{})
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if report.Total() != 0 || report.Coverage() != 1 {
		t.Errorf("report = %+v", report)
	}
}
