package gwplat

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"time"
)

// ErrJWT rejects a token that fails signature, issuer, audience or time
// validation. The error never echoes the token.
var ErrJWT = errors.New("gwplat: invalid jwt")

// Claims are the verified fields an adapter consumes.
type Claims struct {
	Issuer    string
	Audience  []string
	Expires   int64
	NotBefore int64
	Tenant    string
}

// VerifyJWT checks an RS256 token against a kid-indexed key set. issuer and
// audience are mandatory; a missing exp, an expired token and a future nbf all
// fail closed.
func VerifyJWT(token string, keys map[string]*rsa.PublicKey, issuer, audience string, now time.Time) (Claims, error) {
	parts, err := jwtKey(token, keys)
	if err != nil {
		return Claims{}, err
	}
	payload, err := decodeJWTPayload(parts[1])
	if err != nil {
		return Claims{}, err
	}
	aud, err := audiences(payload.Aud)
	if err != nil || payload.Iss != issuer || !slices.Contains(aud, audience) {
		return Claims{}, ErrJWT
	}
	if payload.Exp == 0 || now.Unix() >= payload.Exp || payload.Nbf > now.Unix() {
		return Claims{}, fmt.Errorf("%w: time window", ErrJWT)
	}
	return Claims{Issuer: payload.Iss, Audience: aud, Expires: payload.Exp, NotBefore: payload.Nbf, Tenant: payload.Tid}, nil
}

type jwtPayload struct {
	Iss string          `json:"iss"`
	Aud json.RawMessage `json:"aud"`
	Exp int64           `json:"exp"`
	Nbf int64           `json:"nbf"`
	Tid string          `json:"tid"`
}

func jwtKey(token string, keys map[string]*rsa.PublicKey) ([3]string, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(keys) == 0 {
		return [3]string{}, ErrJWT
	}
	rawHeader, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return [3]string{}, ErrJWT
	}
	var header struct{ Kid, Alg string }
	if json.Unmarshal(rawHeader, &header) != nil || header.Alg != "RS256" {
		return [3]string{}, ErrJWT
	}
	key := keys[header.Kid]
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || key == nil {
		return [3]string{}, ErrJWT
	}
	digest := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if rsa.VerifyPKCS1v15(key, crypto.SHA256, digest[:], sig) != nil {
		return [3]string{}, ErrJWT
	}
	return [3]string{parts[0], parts[1], parts[2]}, nil
}

func decodeJWTPayload(part string) (jwtPayload, error) {
	raw, err := base64.RawURLEncoding.DecodeString(part)
	if err != nil {
		return jwtPayload{}, ErrJWT
	}
	var payload jwtPayload
	if uerr := json.Unmarshal(raw, &payload); uerr != nil {
		return jwtPayload{}, ErrJWT
	}
	return payload, nil
}

func audiences(raw json.RawMessage) ([]string, error) {
	if len(raw) == 0 {
		return nil, ErrJWT
	}
	var one string
	if json.Unmarshal(raw, &one) == nil {
		return []string{one}, nil
	}
	var many []string
	if err := json.Unmarshal(raw, &many); err != nil {
		return nil, ErrJWT
	}
	return many, nil
}

// Bearer extracts the token from an Authorization: Bearer header.
func Bearer(r *http.Request) string {
	scheme, tok, ok := strings.Cut(r.Header.Get("Authorization"), " ")
	if !ok || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(tok)
}
