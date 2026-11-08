package e2e

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"strings"
	"testing"
)

// knownDefectScenario is one package slice of the GAP-REL-14 known-defects
// suite: the exact `go test -run` pattern that selects its named scenarios.
type knownDefectScenario struct {
	name    string
	pkg     string
	pattern string
}

// knownDefectScenarios lists every surface the F7 slice of GAP-REL-14 covers.
// The render package carries the ten historical delivery defects required by
// the ledger; the remaining rows pin the F7 surfaces (api-server 413, kanban
// card cost once, one streaming message per reply, an interrupted stream that
// is retried and typed, and MCP images that stay attachments).
func knownDefectScenarios() []knownDefectScenario {
	return []knownDefectScenario{
		{"render", "./internal/gateway/render", `^TestKnownDefect`},
		{"api_stream_413", "./internal/gwapi", `^TestF7APIStream413$`},
		{"kanban_cost_once", "./internal/kanban", `^TestF7KanbanCostOnce$`},
		{"stream_once_per_message", "./internal/gateway/telegram", `^TestStreamSendsOnceThenEdits$`},
		{"interrupted_stream", "./internal/provider", `^TestGAPF5_REL14$`},
		{"mcp_image_attachment", "./internal/mcp", `^TestF6GAPREL14MCPAttachment$`},
	}
}

// TestKnownDefectsSuite is the GAP-REL-14 aggregate: it runs the named
// known-defect scenarios of every F7 surface as child `go test` runs and
// requires all of them to pass without a skip. A scenario is only aggregation
// here — its assertions live with the package that owns the behaviour.
func TestKnownDefectsSuite(t *testing.T) {
	root, err := moduleRoot()
	if err != nil {
		t.Fatal(err)
	}
	passed, skipped := 0, 0
	for _, sc := range knownDefectScenarios() {
		t.Run(sc.name, func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), "go", "test", "-count=1", "-p=1", "-json", "-run", sc.pattern, sc.pkg) //nolint:gosec // fixed package table, no user input
			cmd.Dir = root
			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr
			runErr := cmd.Run()
			p, s := countKnownDefectActions(t, stdout.Bytes())
			passed += p
			skipped += s
			if runErr != nil {
				t.Fatalf("%s: %v\n%s", sc.pkg, runErr, suiteTail(stderr.String(), stdout.String()))
			}
			if p == 0 {
				t.Fatalf("%s: pattern %q matched no passing scenario\n%s", sc.pkg, sc.pattern, suiteTail(stderr.String(), stdout.String()))
			}
		})
	}
	if skipped != 0 {
		t.Fatalf("known-defects suite skipped %d scenario(s); REL-15 forbids runtime skips", skipped)
	}
	if passed < 10 {
		t.Fatalf("known-defects suite ran %d scenario(s), want at least 10", passed)
	}
}

// countKnownDefectActions counts top-level test results in `go test -json`
// output; subtests are ignored because their parent already reports them.
func countKnownDefectActions(t *testing.T, out []byte) (passed, skipped int) {
	t.Helper()
	dec := json.NewDecoder(bytes.NewReader(out))
	for {
		var ev struct {
			Action string
			Test   string
		}
		if err := dec.Decode(&ev); err != nil {
			return passed, skipped
		}
		if ev.Test == "" || strings.Contains(ev.Test, "/") {
			continue
		}
		switch ev.Action {
		case "pass":
			passed++
		case "skip":
			skipped++
		}
	}
}

// tail keeps the failure readable when a child run prints a long log.
func suiteTail(parts ...string) string {
	joined := strings.TrimSpace(strings.Join(parts, "\n"))
	if len(joined) <= 4000 {
		return joined
	}
	return "…" + joined[len(joined)-4000:]
}
