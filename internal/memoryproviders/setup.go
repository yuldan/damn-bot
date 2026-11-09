package memoryproviders

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// SecretStore is the keychain seam of MEM-010. The value never appears in
// config: config only names the entry.
type SecretStore interface {
	Get(ctx context.Context, name string) (string, error)
	Set(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
}

// Token is one OAuth token set. Expiry is zero when the provider omitted
// expires_in.
type Token struct {
	Access  string
	Refresh string
	Expiry  time.Time
}

// Setup implements `damnbot memory setup` for a provider: OAuth exchange,
// keychain storage and revoke. It never returns a credential in a config
// reference.
type Setup struct {
	HTTP             Doer
	Secrets          SecretStore
	TokenURL         string
	ClientID         string
	ClientSecretName string
	Now              func() time.Time
}

type oauthReply struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	ExpiresIn    int    `json:"expires_in"`
}

// Exchange redeems an authorization code (PKCE) for an access token. The
// client secret, when configured, comes from the keychain by name.
func (s *Setup) Exchange(ctx context.Context, code, verifier string) (tok Token, err error) {
	if s == nil || s.HTTP == nil || s.Secrets == nil || s.TokenURL == "" || s.ClientID == "" {
		return Token{}, fmt.Errorf("%w: incomplete OAuth setup", ErrInvalid)
	}
	if strings.TrimSpace(code) == "" {
		return Token{}, fmt.Errorf("%w: empty authorization code", ErrInvalid)
	}
	form, err := s.oauthForm(ctx, code, verifier)
	if err != nil {
		return Token{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.TokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return Token{}, fmt.Errorf("%w: token request", ErrInvalid)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	resp, err := s.HTTP.Do(req)
	if err != nil {
		return Token{}, fmt.Errorf("%w: token endpoint unreachable", ErrUnavailable)
	}
	defer func() { err = errors.Join(err, resp.Body.Close()) }()
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return Token{}, fmt.Errorf("%w: token endpoint HTTP %d", ErrResponse, resp.StatusCode)
	}
	reply, err := decodeToken(io.LimitReader(resp.Body, honchoMaxBody))
	if err != nil {
		return Token{}, err
	}
	return Token{Access: reply.AccessToken, Refresh: reply.RefreshToken, Expiry: s.expiry(reply.ExpiresIn)}, nil
}

func (s *Setup) oauthForm(ctx context.Context, code, verifier string) (url.Values, error) {
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"client_id":     {s.ClientID},
		"code_verifier": {verifier},
	}
	if s.ClientSecretName == "" {
		return form, nil
	}
	secret, err := s.Secrets.Get(ctx, s.ClientSecretName)
	if err != nil || secret == "" {
		return nil, fmt.Errorf("%w: OAuth client secret is unavailable", ErrUnavailable)
	}
	form.Set("client_secret", secret)
	return form, nil
}

func decodeToken(r io.Reader) (oauthReply, error) {
	var reply oauthReply
	if err := json.NewDecoder(r).Decode(&reply); err != nil {
		return oauthReply{}, fmt.Errorf("%w: token response", ErrResponse)
	}
	if reply.AccessToken == "" {
		return oauthReply{}, fmt.Errorf("%w: token response has no access_token", ErrResponse)
	}
	return reply, nil
}

func (s *Setup) expiry(seconds int) time.Time {
	if seconds <= 0 {
		return time.Time{}
	}
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	return now().Add(time.Duration(seconds) * time.Second)
}

// Save stores the access token under name and returns the config reference
// (never the value).
func (s *Setup) Save(ctx context.Context, name string, tok Token) (string, error) {
	if s == nil || s.Secrets == nil {
		return "", fmt.Errorf("%w: no secret store", ErrInvalid)
	}
	if !validSecretName(name) {
		return "", fmt.Errorf("%w: secret name %q", ErrInvalid, name)
	}
	if strings.TrimSpace(tok.Access) == "" {
		return "", fmt.Errorf("%w: empty token", ErrInvalid)
	}
	if err := s.Secrets.Set(ctx, name, tok.Access); err != nil {
		return "", fmt.Errorf("%w: keychain write failed", ErrUnavailable)
	}
	return ConfigReference(name), nil
}

// Resolve reads the token from the keychain.
func (s *Setup) Resolve(ctx context.Context, name string) (string, error) {
	if s == nil || s.Secrets == nil || !validSecretName(name) {
		return "", fmt.Errorf("%w: secret name %q", ErrInvalid, name)
	}
	token, err := s.Secrets.Get(ctx, name)
	if err != nil || token == "" {
		return "", fmt.Errorf("%w: memory credential %q is unavailable", ErrUnavailable, name)
	}
	return token, nil
}

// Revoke deletes the keychain entry. The provider-side revoke is the caller's
// next setup step; an absent entry is already revoked.
func (s *Setup) Revoke(ctx context.Context, name string) error {
	if s == nil || s.Secrets == nil || !validSecretName(name) {
		return fmt.Errorf("%w: secret name %q", ErrInvalid, name)
	}
	if err := s.Secrets.Delete(ctx, name); err != nil {
		return fmt.Errorf("%w: keychain delete failed", ErrUnavailable)
	}
	return nil
}

// ConfigReference is the only thing setup writes to config.
func ConfigReference(name string) string {
	return "token_secret = " + strconv.Quote(name)
}

func validSecretName(name string) bool {
	if name == "" || len(name) > 128 {
		return false
	}
	for _, r := range name {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}
