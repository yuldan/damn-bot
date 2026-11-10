package main

import (
	"os"
	"testing"
)

// TestSurfaceParity: every server request × five frontends is rendered or has
// a defined fallback (GAP-UX-09); a missing cell is a hard failure.
func TestSurfaceParity(t *testing.T) {
	t.Parallel()
	if err := checkSurfaceParity(); err != nil {
		t.Fatal(err)
	}
	if got := len(surfaceContract()); got != len(serverRequests())*len(frontends()) {
		t.Fatalf("rows = %d, want %d", got, len(serverRequests())*len(frontends()))
	}
}

// TestSurfaceDocSync: docs/SURFACE-PARITY.md is generated from the contract.
func TestSurfaceDocSync(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../../docs/SURFACE-PARITY.md")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw), surfaceDoc(); got != want {
		t.Fatalf("docs/SURFACE-PARITY.md is stale\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}
