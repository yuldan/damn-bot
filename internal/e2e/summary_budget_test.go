package e2e

import (
	"os"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// writeConfig replaces the scenario's config.toml: the [budget] caps live there.
func writeConfig(t *testing.T, e *Env, body string) {
	t.Helper()
	if err := os.WriteFile(e.cfg, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// summaryScript drives a turn into compaction: five trusted reads, then the
// provider rejects the request as too long, so stage 2 asks for a summary.
// That summary request is a paid call on the run's own provider.
func summaryScript() []providertest.Reply {
	return append(readNotes(),
		providertest.Status(400, "context_length_exceeded", ""),
		providertest.Text(summaryJSON),
		providertest.Text("done"),
	)
}

// TestSummaryCallObeysTheBudgetGuard (W6 security boundary): the compaction
// summary rides the run's own provider/client (compaction_wire.go:136), so the
// [budget] cap applies to it like to any other call. With a cap configured on
// an unpriced fake route the guard fails closed before anything is sent — the summary,
// which would be the next paid call, never reaches the provider — and raising
// the cap makes the same summary request go out: the negative control pins the
// refusal to the cap and not to some other failure.
func TestSummaryCallObeysTheBudgetGuard(t *testing.T) {
	t.Parallel()
	const marker = "You summarise an earlier part" // internal/compaction systemPrompt

	denied := New(t, summaryScript()...)
	writeConfig(t, denied, "[budget]\ndaily_usd = 0.01\n")
	res := denied.Run("read the notes and summarise where we are")
	if n := len(denied.Requests()); n != 0 {
		t.Fatalf("provider received %d requests under a 0.01 USD cap (want 0)", n)
	}
	if out := res.Stdout + res.Stderr; !strings.Contains(strings.ToLower(out), "budget") {
		t.Fatalf("no budget stop reported: stdout=%q stderr=%q", res.Stdout, res.Stderr)
	}

	// Control: no cap at all (daily_usd = 0 means "no cap"): the same scenario
	// then reaches the provider and asks for the summary, so the refusal above
	// is pinned to the cap and not to an unpriced route or another failure.
	allowed := New(t, summaryScript()...)
	writeConfig(t, allowed, "[budget]\ndaily_usd = 0\n")
	allowed.Run("read the notes and summarise where we are").RequireOK(t)
	summary := false
	for i := range allowed.Requests() {
		if allowed.Contains(t, i, marker) {
			summary = true
			break
		}
	}
	if !summary {
		t.Fatalf("the summary call never reached the provider with the cap raised (%d requests)", len(allowed.Requests()))
	}
}
