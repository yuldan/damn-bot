package h2h

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math"
	"net/url"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"
)

// f6e3ArtifactSchema is the only accepted schema of bench/evals/results/F6-e3.json.
const f6e3ArtifactSchema = "damnbot.f6.e3-smoke/v1"

// f6e3SmokeIDs is the deterministic SelectF6E3 selection; the artifact must
// record exactly this set, so a hand-written or stale file cannot pass.
var f6e3SmokeIDs = []string{"E3-012", "E3-016", "E3-001", "E3-026", "E3-010", "E3-020", "E3-018", "E3-009", "E3-013", "E3-017"}

// f6e3Placeholders are strings that mark an unfilled template instead of a
// measured run. They are rejected anywhere in the artifact (case-insensitive).
var f6e3Placeholders = []string{"todo", "tbd", "placeholder", "lorem ipsum", "<insert", "example.com", "replace-me"}

var f6e3Hex40 = regexp.MustCompile(`^[0-9a-f]{40}$`)
var f6e3Hex64 = regexp.MustCompile(`^[0-9a-f]{64}$`)

type f6e3Artifact struct {
	Schema string `json:"schema"`
	Meta   struct {
		Date          string  `json:"date"`
		Smoke         string  `json:"smoke"`
		Selection     string  `json:"selection"`
		DamnbotCommit string  `json:"damnbot_commit"`
		HermesCommit  string  `json:"hermes_commit"`
		Model         string  `json:"model"`
		Upstream      string  `json:"upstream"`
		Price         Price   `json:"price_usd"`
		PriceSource   string  `json:"price_source"`
		CapUSD        float64 `json:"cap_usd"`
		TimeoutS      float64 `json:"timeout_s_per_task"`
		MaxTurns      int     `json:"max_turns"`
		Command       string  `json:"command"`
		Summary       string  `json:"summary_source"`
		SummarySHA    string  `json:"summary_sha256"`
		Host          struct {
			GOOS     string `json:"goos"`
			GOARCH   string `json:"goarch"`
			CPUs     int    `json:"cpus"`
			Kernel   string `json:"kernel"`
			Workload string `json:"workload"`
		} `json:"host"`
		HermesVerification struct {
			Method       string   `json:"method"`
			MatchedFiles []string `json:"matched_files"`
		} `json:"hermes_verification"`
	} `json:"meta"`
	Totals struct {
		Tasks            int     `json:"tasks"`
		Ran              int     `json:"ran"`
		Passed           int     `json:"passed"`
		HarnessErrors    int     `json:"harness_errors"`
		Calls            int     `json:"calls"`
		PromptTokens     int64   `json:"prompt_tokens"`
		CachedTokens     int64   `json:"cached_tokens"`
		CompletionTokens int64   `json:"completion_tokens"`
		CostUSD          float64 `json:"cost_usd"`
		LedgerSpentUSD   float64 `json:"ledger_spent_usd"`
	} `json:"totals"`
	Tasks []struct {
		ID               string   `json:"id"`
		Set              string   `json:"set"`
		Agent            string   `json:"agent"`
		Status           string   `json:"status"`
		Passed           bool     `json:"passed"`
		Category         string   `json:"category"`
		ExitCode         int      `json:"exit_code"`
		StopReason       string   `json:"stop_reason"`
		StopDetail       string   `json:"stop_detail"`
		DurationS        float64  `json:"duration_s"`
		Calls            int      `json:"calls"`
		PromptTokens     int64    `json:"prompt_tokens"`
		CachedTokens     int64    `json:"cached_tokens"`
		CompletionTokens int64    `json:"completion_tokens"`
		CostUSD          float64  `json:"cost_usd"`
		VerdictDetail    []string `json:"verdict_detail"`
		Recording        string   `json:"recording"`
		RecordingSHA256  string   `json:"recording_sha256"`
	} `json:"tasks"`
}

// TestF6E3Artifact validates the saved Smoke E3 artifact. It rejects an empty
// or placeholder file and refuses to bless a task marked passed without the
// evidence fields a real harness run produces (recording hash, calls, tokens,
// exit/stop contract). It deliberately does not require every task to pass:
// an honest failing run is a valid artifact, but then the ledger row stays
// BELUM.
func TestF6E3Artifact(t *testing.T) {
	t.Parallel()
	b, err := os.ReadFile("../evals/results/F6-e3.json")
	if err != nil {
		t.Fatalf("F6 E3 artifact: %v", err)
	}
	if len(bytes.TrimSpace(b)) == 0 {
		t.Fatal("F6 E3 artifact is empty")
	}
	lower := strings.ToLower(string(b))
	for _, p := range f6e3Placeholders {
		if strings.Contains(lower, p) {
			t.Fatalf("F6 E3 artifact contains placeholder %q", p)
		}
	}
	var a f6e3Artifact
	if err := json.Unmarshal(b, &a); err != nil {
		t.Fatalf("F6 E3 artifact JSON: %v", err)
	}
	if a.Schema != f6e3ArtifactSchema {
		t.Fatalf("schema = %q, want %q", a.Schema, f6e3ArtifactSchema)
	}

	// meta: provenance a reader can re-check without the original machine.
	if a.Meta.Smoke != "f6e3" {
		t.Fatalf("meta.smoke = %q, want f6e3", a.Meta.Smoke)
	}
	if !strings.Contains(a.Meta.Selection, "E3") {
		t.Fatalf("meta.selection does not name E3: %q", a.Meta.Selection)
	}
	if !f6e3Hex40.MatchString(a.Meta.DamnbotCommit) {
		t.Fatalf("meta.damnbot_commit = %q, want 40 hex", a.Meta.DamnbotCommit)
	}
	if !f6e3Hex40.MatchString(a.Meta.HermesCommit) {
		t.Fatalf("meta.hermes_commit = %q, want 40 hex", a.Meta.HermesCommit)
	}
	if len(a.Meta.HermesVerification.MatchedFiles) == 0 || a.Meta.HermesVerification.Method == "" {
		t.Fatal("meta.hermes_verification must record how the commit was verified")
	}
	if a.Meta.Model == "" || a.Meta.Upstream == "" {
		t.Fatal("meta.model and meta.upstream are required")
	}
	u, err := url.Parse(a.Meta.Upstream)
	if err != nil || u.Scheme != "https" || u.Host == "" {
		t.Fatalf("meta.upstream = %q, want https URL", a.Meta.Upstream)
	}
	if !a.Meta.Price.Valid() {
		t.Fatalf("meta.price_usd = %+v, want input/output > 0 and cached >= 0", a.Meta.Price)
	}
	if a.Meta.PriceSource == "" {
		t.Fatal("meta.price_source is required")
	}
	if a.Meta.CapUSD <= 0 {
		t.Fatalf("meta.cap_usd = %v, want > 0", a.Meta.CapUSD)
	}
	if a.Meta.TimeoutS <= 0 || a.Meta.MaxTurns <= 0 {
		t.Fatalf("meta.timeout_s_per_task/max_turns = %v/%d, want > 0", a.Meta.TimeoutS, a.Meta.MaxTurns)
	}
	if a.Meta.Command == "" || a.Meta.Summary == "" {
		t.Fatal("meta.command and meta.summary_source are required")
	}
	if !f6e3Hex64.MatchString(a.Meta.SummarySHA) {
		t.Fatalf("meta.summary_sha256 = %q, want 64 hex", a.Meta.SummarySHA)
	}
	if _, err := time.Parse(time.RFC3339, a.Meta.Date); err != nil {
		t.Fatalf("meta.date = %q: %v", a.Meta.Date, err)
	}
	if a.Meta.Host.GOOS == "" || a.Meta.Host.GOARCH == "" || a.Meta.Host.CPUs <= 0 {
		t.Fatalf("meta.host incomplete: %+v", a.Meta.Host)
	}

	// totals: 10 tasks, aggregates must equal the per-task rows.
	if a.Totals.Tasks != 10 || len(a.Tasks) != 10 {
		t.Fatalf("tasks = %d/%d, want 10", a.Totals.Tasks, len(a.Tasks))
	}
	if a.Totals.Ran != 10 {
		t.Fatalf("totals.ran = %d, want 10", a.Totals.Ran)
	}
	if a.Totals.HarnessErrors != 0 {
		t.Fatalf("totals.harness_errors = %d, want 0", a.Totals.HarnessErrors)
	}
	if a.Totals.Calls == 0 || a.Totals.CostUSD <= 0 {
		t.Fatalf("totals calls/cost = %d/%.6f, want a real run", a.Totals.Calls, a.Totals.CostUSD)
	}
	seen := map[string]bool{}
	var calls int
	var passed int
	var prompt, cached, completion int64
	var cost float64
	for i, task := range a.Tasks {
		if task.ID != f6e3SmokeIDs[i] {
			t.Fatalf("task %d = %s, want %s", i, task.ID, f6e3SmokeIDs[i])
		}
		if seen[task.ID] || task.Set != "E3" {
			t.Fatalf("task %d: duplicate %s or set %q", i, task.ID, task.Set)
		}
		seen[task.ID] = true
		if task.Agent == "" || (task.Agent != "hermes" && task.Agent != "damnbot") {
			t.Fatalf("%s: agent = %q", task.ID, task.Agent)
		}
		if task.Status != "ran" {
			t.Fatalf("%s: status = %q, want ran", task.ID, task.Status)
		}
		if !f6e3Hex64.MatchString(task.RecordingSHA256) || task.Recording == "" {
			t.Fatalf("%s: missing recording hash", task.ID)
		}
		if task.DurationS <= 0 {
			t.Fatalf("%s: duration_s = %v", task.ID, task.DurationS)
		}
		if task.Passed {
			passed++
			// A pass without a completed, billed run is not evidence.
			if task.ExitCode != 0 || task.Category != "ok" || task.StopReason != "end_turn" || task.StopDetail != "completed" {
				t.Fatalf("%s: passed but exit=%d category=%s stop=%s/%s", task.ID, task.ExitCode, task.Category, task.StopReason, task.StopDetail)
			}
			if task.Calls <= 0 || task.PromptTokens <= 0 || task.CostUSD <= 0 {
				t.Fatalf("%s: passed but calls/tokens/cost = %d/%d/%.6f", task.ID, task.Calls, task.PromptTokens, task.CostUSD)
			}
		} else if len(task.VerdictDetail) == 0 && task.Category == "ok" {
			t.Fatalf("%s: failed without a verdict detail", task.ID)
		}
		calls += task.Calls
		prompt += task.PromptTokens
		cached += task.CachedTokens
		completion += task.CompletionTokens
		cost += task.CostUSD
	}
	if a.Totals.Passed != passed {
		t.Fatalf("totals.passed = %d, rows = %d", a.Totals.Passed, passed)
	}
	if a.Totals.Calls != calls || a.Totals.PromptTokens != prompt || a.Totals.CachedTokens != cached || a.Totals.CompletionTokens != completion {
		t.Fatalf("totals usage %d/%d/%d/%d != rows %d/%d/%d/%d",
			a.Totals.Calls, a.Totals.PromptTokens, a.Totals.CachedTokens, a.Totals.CompletionTokens,
			calls, prompt, cached, completion)
	}
	if math.Abs(a.Totals.CostUSD-cost) > 1e-9 {
		t.Fatalf("totals.cost_usd = %.9f, rows = %.9f", a.Totals.CostUSD, cost)
	}
	if a.Totals.LedgerSpentUSD < a.Totals.CostUSD {
		t.Fatalf("totals.ledger_spent_usd = %.9f < run cost %.9f", a.Totals.LedgerSpentUSD, a.Totals.CostUSD)
	}
	if _, err := hex.DecodeString(a.Meta.SummarySHA); err != nil {
		t.Fatalf("meta.summary_sha256: %v", err)
	}
}
