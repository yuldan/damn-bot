//go:build !minimal

package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"fmt"
	"strings"
)

// validateCatalogPublicKey accepts a base64 ed25519 key, or empty for the
// embedded catalog only (GAP-SEC-20). It lives in the full build so the
// minimal binary does not link crypto/ed25519 for a catalog it cannot use.
func validateCatalogPublicKey(text string) error {
	if strings.TrimSpace(text) == "" {
		return nil
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(strings.TrimSpace(text))
	if err != nil || len(raw) != ed25519.PublicKeySize {
		return fmt.Errorf("update.catalog_public_key must be base64 ed25519 (%d bytes)", ed25519.PublicKeySize)
	}
	return nil
}
