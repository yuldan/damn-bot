package phase

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"
)

// ecTotalF9 = seluruh entri EDGE-CASES (ROADMAP F9 acceptance 3).
const ecTotalF9 = 166

// TestF9EdgeCases166: 166/166 entri EDGE-CASES `covered` atau `n/a`
// beralasan. Kerangka ini memakai parser katalog yang sama dengan
// TestEdgeCaseCatalogSync; selama masih ada entri terbuka ia hanya skip sampai
// penanda rilis (ADR-347).
func TestF9EdgeCases166(t *testing.T) {
	t.Parallel()
	root := moduleRoot(t)
	required := parseRoadmap(t, root)
	catalog := scanCatalogs(t, root)
	if len(required) != ecTotalF9 {
		t.Fatalf("EDGE-CASES = %d entri, want %d", len(required), ecTotalF9)
	}
	var open []string
	for id := range required {
		e, ok := catalog[id]
		if !ok || (e.status != "covered" && e.status != "n/a") {
			open = append(open, id)
		}
	}
	if len(open) > 0 {
		slices.Sort(open)
		skipOrFail(t, fmt.Sprintf("%d/%d EC covered/n/a; belum: %s",
			len(required)-len(open), ecTotalF9, strings.Join(open, " ")))
	}
}

// skipOrFail menunda gate agregat sampai DAMNBOT_RELEASE=1 (ADR-347).
func skipOrFail(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("DAMNBOT_RELEASE") == "1" {
		t.Fatalf("F9 GAGAL: %s", reason)
	}
	t.Skipf("F9 belum aktif (DAMNBOT_RELEASE=1): %s", reason)
}
