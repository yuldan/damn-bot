package gwplat_test

import "testing"

// TestF7PlatformIdentityTable is the aggregate §1 proof for PLAT 30/30: the
// SEC-10 table names all 30 platforms with their identity/rate/edit caps, and
// the closed adapter contract proves every registered surface. Both proofs
// live in their own named tests; this wrapper is the ledger-required name that
// ties them together without duplicating assertions.
func TestF7PlatformIdentityTable(t *testing.T) {
	t.Run("identity_strength", TestIdentityStrengthTable)
	t.Run("adapter_contract", TestPlatformAdapterContract)
}
