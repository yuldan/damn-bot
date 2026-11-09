package e2e

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestF8OPS020EvalBatch runs the real binary twice: the first pass executes
// two prompts against the fake provider, the second resumes with no provider
// request and no duplicate row.
func TestF8OPS020EvalBatch(t *testing.T) {
	t.Parallel()
	e := New(t, providertest.Text("one"), providertest.Text("two"))
	prompts := filepath.Join(e.Workdir, "prompts.jsonl")
	if err := os.WriteFile(prompts, []byte(`{"id":"p1","prompt":"first"}
{"id":"p2","prompt":"second"}
`), 0o600); err != nil {
		t.Fatal(err)
	}
	key := filepath.Join(e.Workdir, "key")
	if err := os.WriteFile(key, []byte("e2e\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(e.Workdir, "batch-out")
	args := []string{
		"eval", "batch", "run", prompts, "--out", out, "--model", modelRef,
		"--base-url", e.Server.URL, "--key-file", key, "--parallel", "2",
	}
	res := e.Exec(args...)
	res.RequireOK(t)
	if got := len(e.Server.Requests()); got != 2 {
		t.Fatalf("provider requests = %d, want 2", got)
	}
	rows := readBatchRows(t, out)
	if len(rows) != 2 {
		t.Fatalf("results rows = %d, want 2:\n%s", len(rows), rows)
	}
	for _, id := range []string{"p1", "p2"} {
		path := filepath.Join(out, "trajectories", id+".openai.json")
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("trajectory %s: %v", id, err)
		}
	}
	res = e.Exec(args...)
	res.RequireOK(t)
	if got := len(e.Server.Requests()); got != 2 {
		t.Fatalf("resume issued %d extra provider request(s)", got-2)
	}
	if got := readBatchRows(t, out); len(got) != 2 {
		t.Fatalf("resume duplicated results rows: %d", len(got))
	}
	res = e.Exec("eval", "batch", "compress", out, "--max-chars", "20")
	res.RequireOK(t)
}

func readBatchRows(t *testing.T, out string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(out, "results.jsonl")) // #nosec G304 -- test temp output
	if err != nil {
		t.Fatal(err)
	}
	var rows []string
	for line := range strings.SplitSeq(strings.TrimSpace(string(raw)), "\n") {
		if line != "" {
			rows = append(rows, line)
		}
	}
	return rows
}
