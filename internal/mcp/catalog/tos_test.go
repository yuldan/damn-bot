package catalog

import (
	"strings"
	"testing"
)

// TestMCPCatalogToS: every MCP hub entry must carry tos_risk/tos_note; an
// unknown level or empty note is rejected before verification (GAP-GW-07).
func TestMCPCatalogToS(t *testing.T) {
	t.Parallel()
	m := testManifest(KindMCP, 1)
	m.Entries[0].ToSRisk, m.Entries[0].ToSNote = "", ""
	data, err := m.SigningBytes()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParseManifest(data); err == nil || !strings.Contains(err.Error(), "tos_risk") {
		t.Fatalf("missing tos = %v", err)
	}
	m.Entries[0].ToSRisk, m.Entries[0].ToSNote = "high", "needs review"
	var derr error
	data, derr = m.SigningBytes()
	if derr != nil {
		t.Fatal(derr)
	}
	if _, err := ParseManifest(data); err != nil {
		t.Fatalf("high tos: %v", err)
	}
	m.Entries[0].ToSRisk = "critical"
	data, derr = m.SigningBytes()
	if derr != nil {
		t.Fatal(derr)
	}
	if _, err := ParseManifest(data); err == nil {
		t.Fatal("unknown tos level accepted")
	}
}
