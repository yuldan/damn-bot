//go:build !minimal

package netx

import (
	"strings"
	"testing"
)

// TestEgressAuthScoped: the auth egress is limited to the configured issuer's
// OAuth endpoints, never a wildcard host.
func TestEgressAuthScoped(t *testing.T) {
	t.Parallel()
	e, ok := Registry()["auth"]
	if !ok || e.Purpose != Inference {
		t.Fatalf("auth egress = %+v, ok=%t", e, ok)
	}
	if strings.Contains(e.Host, "*") || !strings.Contains(e.Host, "issuer") || !strings.Contains(e.Trigger, "revocation") {
		t.Fatalf("auth egress not narrowly scoped: %+v", e)
	}
}
