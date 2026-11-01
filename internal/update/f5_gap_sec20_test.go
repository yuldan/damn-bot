package update

import (
	"encoding/base64"
	"testing"
)

// TestGAPF5_SEC20ConfigKey pins the operator-configured verification key:
// a missing key is allowed (embedded catalog), an invalid key is a startup
// error, and a valid ed25519 key is accepted.
func TestGAPF5_SEC20ConfigKey(t *testing.T) {
	t.Parallel()
	_, pub := testKey(t)
	if err := (Config{Channel: channelStable}).Validate(); err != nil {
		t.Fatalf("empty key = %v", err)
	}
	if err := (Config{Channel: channelStable, CatalogPublicKey: "!!!"}).Validate(); err == nil {
		t.Fatal("invalid catalog key accepted")
	}
	if err := (Config{Channel: channelStable, CatalogPublicKey: base64.StdEncoding.EncodeToString([]byte("short"))}).Validate(); err == nil {
		t.Fatal("short catalog key accepted")
	}
	if err := (Config{Channel: channelStable, CatalogPublicKey: base64.StdEncoding.EncodeToString(pub)}).Validate(); err != nil {
		t.Fatalf("valid catalog key = %v", err)
	}
}
