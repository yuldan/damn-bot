package importer

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func TestImportMemory(t *testing.T) {
	t.Parallel()
	report, sink, _ := importFixture(t, "hermes-memory")
	if report.Memory.Total != 5 || report.Memory.Imported != 5 || len(report.Memory.Skipped) != 0 {
		t.Fatalf("memory report = %+v", report.Memory)
	}
	if len(sink.memories) != 5 {
		t.Fatalf("stored %d memories", len(sink.memories))
	}
	stores := map[string]int{}
	for _, entry := range sink.memories {
		stores[entry.Store]++
	}
	if stores["memory"] != 3 || stores["user"] != 2 {
		t.Errorf("stores = %v", stores)
	}
}

func TestImportMCP(t *testing.T) {
	t.Parallel()
	report, sink, secrets := importFixture(t, "hermes-mcp")
	if report.MCP.Total != 5 || report.MCP.Imported != 4 || len(report.MCP.Skipped) != 1 {
		t.Fatalf("mcp report = %+v", report.MCP)
	}
	if skip := report.MCP.Skipped[0]; skip.Key != "broken" || skip.Reason == "" {
		t.Errorf("skip = %+v", skip)
	}
	time := sink.mcp["time"]
	if time.Command != "uvx" || time.TimeoutS != 60 || len(time.Args) != 1 {
		t.Errorf("time = %+v", time)
	}
	if sink.mcp["notion"].URL != "https://mcp.notion.com/mcp" {
		t.Errorf("notion = %+v", sink.mcp["notion"])
	}
	github := sink.mcp["github"]
	if !strings.HasPrefix(github.Env["GITHUB_PERSONAL_ACCESS_TOKEN"], "keychain:") ||
		github.Env["LOG_LEVEL"] != "info" {
		t.Errorf("github env = %v", github.Env)
	}
	if !strings.HasPrefix(github.Headers["Authorization"], "keychain:") {
		t.Errorf("github headers = %v", github.Headers)
	}
	if ref := sink.mcp["ref"].Headers["X-Api-Key"]; ref != "keychain:OPENROUTER_API_KEY" {
		t.Errorf("ref header = %q", ref)
	}
	if secrets.values["OPENROUTER_API_KEY"] == "" {
		t.Errorf("referenced env secret not stored: %v", secrets.values)
	}
}

func TestImportCron(t *testing.T) {
	t.Parallel()
	report, sink, _ := importFixture(t, "hermes-cron")
	if report.Cron.Total != 5 || report.Cron.Imported != 3 || len(report.Cron.Skipped) != 2 {
		t.Fatalf("cron report = %+v", report.Cron)
	}
	if schedule := sink.cron["j-interval"].Schedule; schedule.Kind != "interval" || schedule.Minutes != 30 {
		t.Errorf("interval = %+v", schedule)
	}
	if schedule := sink.cron["j-cron"].Schedule; schedule.Kind != "cron" || schedule.Expr != "0 9 * * *" {
		t.Errorf("cron = %+v", schedule)
	}
	if schedule := sink.cron["j-once"].Schedule; schedule.Kind != "once" || schedule.RunAt == "" {
		t.Errorf("once = %+v", schedule)
	}
	for _, job := range sink.cron {
		if job.Enabled {
			t.Errorf("imported job %q is enabled", job.Name)
		}
	}
	reasons := map[string]string{}
	for _, skip := range report.Cron.Skipped {
		reasons[skip.Key] = skip.Reason
	}
	if reasons["script-only"] == "" || reasons["bad-schedule"] == "" {
		t.Errorf("skip reasons = %v", reasons)
	}
}

func TestImportSecretsNeverLeak(t *testing.T) {
	t.Parallel()
	secrets := []string{
		"sk-or-v1-CONFIG-FIXTURE-SECRET",
		"sk-or-v1-ENV-FIXTURE-SECRET",
		"ghp_MCP-FIXTURE-SECRET",
		"MCP-FIXTURE-BEARER",
		"unused-fixture-secret",
	}
	imp, sink, vault := testImporter(t)
	var reports []Report
	for _, name := range []string{"hermes-config", "hermes-mcp"} {
		report, err := imp.Import(context.Background(), filepath.Join("testdata", name), Options{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		reports = append(reports, report)
	}
	encoded, err := json.Marshal(reports)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, secret := range secrets[:4] {
		if strings.Contains(string(encoded), secret) {
			t.Errorf("report leaks %q", secret)
		}
	}
	sinkJSON, err := json.Marshal(map[string]any{
		"config":   sink.config,
		"skills":   sink.skills,
		"memories": sink.memories,
		"mcp":      sink.mcp,
		"cron":     sink.cron,
	})
	if err != nil {
		t.Fatalf("sink Marshal: %v", err)
	}
	for _, secret := range secrets[:4] {
		if strings.Contains(string(sinkJSON), secret) {
			t.Errorf("sink leaks %q", secret)
		}
	}
	if vault.values["OPENROUTER_API_KEY"] != secrets[1] {
		t.Errorf("referenced secret not routed to keychain")
	}
	if _, ok := vault.values["UNUSED_TOKEN"]; ok {
		t.Errorf("unreferenced secret was imported")
	}
}

func TestImportFiveFixturesCoverage(t *testing.T) {
	t.Parallel()
	total, imported, skipped := 0, 0, 0
	for _, name := range []string{"hermes-config", "hermes-skills", "hermes-memory", "hermes-mcp", "hermes-cron"} {
		report, _, _ := importFixture(t, name)
		if report.Coverage() < 0.99 {
			t.Errorf("%s: coverage = %v", name, report.Coverage())
		}
		if report.Imported()+report.SkippedCount() != report.Total() {
			t.Errorf("%s: unaccounted item", name)
		}
		total += report.Total()
		imported += report.Imported()
		skipped += report.SkippedCount()
	}
	if total == 0 || float64(imported+skipped)/float64(total) < 0.99 {
		t.Errorf("total=%d imported=%d skipped=%d", total, imported, skipped)
	}
}

func TestImportFullIdempotent(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"hermes-memory", "hermes-mcp", "hermes-cron"} {
		imp, _, _ := testImporter(t)
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
		if first.Imported() != second.Imported() {
			t.Errorf("%s imported = %d then %d", name, first.Imported(), second.Imported())
		}
	}
}

func TestImportBrokenCronIsAtomic(t *testing.T) {
	t.Parallel()
	imp, sink, secrets := testImporter(t)
	_, err := imp.Import(context.Background(), "testdata/hermes-broken-cron", Options{})
	if !errors.Is(err, ErrSyntax) {
		t.Fatalf("err = %v, want ErrSyntax", err)
	}
	if sink.calls != 0 || secrets.calls != 0 {
		t.Errorf("partial import: sink=%d secrets=%d", sink.calls, secrets.calls)
	}
}
