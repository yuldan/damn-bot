package gwplat

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
)

// HMACSHA256Base64 signs parts with secret and returns base64.
func HMACSHA256Base64(secret string, parts ...[]byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	for _, p := range parts {
		m.Write(p)
	}
	return base64.StdEncoding.EncodeToString(m.Sum(nil))
}

// VerifyHMACSHA256 compares a base64 signature in constant time.
func VerifyHMACSHA256(secret string, body []byte, provided string) error {
	if !ConstantTimeEqual(HMACSHA256Base64(secret, body), provided) {
		return ErrBadSignature
	}
	return nil
}

// ErrBadSignature rejects a platform signature.
var ErrBadSignature = errors.New("gwplat: signature verification failed")
