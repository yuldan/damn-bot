package archtest

import "testing"

// TestZZProbeCoreSum sementara: mencetak Σ budgetCoreSum(loc.budget) untuk verifikasi manual.
func TestZZProbeCoreSum(t *testing.T) {
	t.Logf("budgetCoreSum=%d", budgetCoreSum(t, readRepoFile(t, "loc.budget")))
}
