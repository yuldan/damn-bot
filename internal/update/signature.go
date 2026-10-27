package update

import (
	"crypto/ed25519"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
)

const sigHeader = "damnbot-signature/v1 ed25519"

// version is the build-time ed25519 release public key (ADR-216); the name is
// required by the global-variable gate and -X requires a variable.
var version = ""

// EmbeddedPublicKey returns the build-time trust anchor ("" when absent).
func EmbeddedPublicKey() string { return version }

// ParsePublicKey decodes the base64 ed25519 public key.
func ParsePublicKey(text string) (ed25519.PublicKey, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil, errNoPublicKey
	}
	raw, err := base64.StdEncoding.Strict().DecodeString(text)
	if err != nil {
		return nil, fmt.Errorf("update: public key is not standard base64: %w", err)
	}
	if len(raw) != ed25519.PublicKeySize {
		return nil, fmt.Errorf("update: public key must be %d bytes, got %d", ed25519.PublicKeySize, len(raw))
	}
	return ed25519.PublicKey(raw), nil
}

func parseSignature(data []byte) ([]byte, error) {
	lines := strings.Split(strings.TrimSuffix(string(data), "\n"), "\n")
	if len(lines) != 2 || lines[0] != sigHeader {
		return nil, errors.New("update: signature file has an unknown format")
	}
	sig, err := base64.StdEncoding.Strict().DecodeString(lines[1])
	if err != nil {
		return nil, fmt.Errorf("update: signature base64: %w", err)
	}
	if len(sig) != ed25519.SignatureSize {
		return nil, fmt.Errorf("update: signature must be %d bytes, got %d", ed25519.SignatureSize, len(sig))
	}
	return sig, nil
}
