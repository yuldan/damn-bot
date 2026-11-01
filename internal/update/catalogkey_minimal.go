//go:build minimal

package update

// validateCatalogPublicKey is a no-op in the minimal build: no catalog loader
// is linked, so the key has no consumer and must not pull crypto/ed25519 into
// the 22 MB minimal image (GAP-SEC-20 is a full-build feature).
func validateCatalogPublicKey(string) error { return nil }
