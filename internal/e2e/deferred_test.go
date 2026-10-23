package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunAdvertisesNoDeferredTools (F2 wiring contract): with no module
// registered — `run` wires newRunModules(modules.Default(), nil) and the
// default registry is empty — the real binary declares no deferred tool and
// neither tool_search nor tool_invoke, while the eager tools stay in the
// payload. This is NOT the EC-TCALL-010 proof (that needs a wired module:
// the deferred schema must stay out of the payload although the tools
// exist); it locks the contract the `n/a` reason for EC-TCALL-010 rests on,
// and it fails the moment a module is wired, which is the signal to add the
// run-level e2e.
func TestRunAdvertisesNoDeferredTools(t *testing.T) {
	e := New(t, providertest.Text("done"))
	e.Run("say done").RequireOK(t)
	for _, deferred := range []string{`"tool_search"`, `"tool_invoke"`} {
		if body := e.RequestText(t, 0); strings.Contains(body, deferred) {
			t.Fatalf("deferred tool %s advertised without a module: %s", deferred, body)
		}
	}
	// Negative control: the eager tools are in the same payload, so the
	// assertion above is not vacuous.
	for _, eager := range []string{`"read_file"`, `"write_file"`, `"shell"`} {
		if !e.Contains(t, 0, eager) {
			t.Fatalf("eager tool %s missing from the payload", eager)
		}
	}
}
