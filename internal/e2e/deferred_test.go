package e2e

import (
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/provider/providertest"
)

// TestRunAdvertisesNoDeferredTools (F2 wiring contract): with the web module
// registered but disabled by default, `run` wires no deferred tool and neither
// tool_search nor tool_invoke, while the eager tools stay in the payload. This
// locks the disabled-module contract (the enabled counterpart is
// TestF6WebModuleE2E); it fails the moment a module becomes enabled by default,
// which is the signal to revisit this assertion.
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
