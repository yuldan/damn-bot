package auth

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/netx"
)

// Metadata is the RFC 8414 discovery subset F5-03 uses.
type Metadata struct {
	Issuer               string `json:"issuer"`
	TokenEndpoint        string `json:"token_endpoint"`
	DeviceEndpoint       string `json:"device_authorization_endpoint"`
	RegistrationEndpoint string `json:"registration_endpoint"`
	RevocationEndpoint   string `json:"revocation_endpoint"`
}

// OAuthConfig configures one provider's OAuth transport. Output receives only
// user instructions; tokens never reach it.
type OAuthConfig struct {
	Provider     string
	Issuer       string
	ClientID     string
	ClientSecret string
	Scopes       []string
	Store        *Store
	Client       *netx.Client
	Now          func() time.Time
	Sleep        func(context.Context, time.Duration) error
	Output       io.Writer
	Offline      bool
}

// OAuth runs device flow (RFC 8628 + PKCE), refresh, revocation and signing.
type OAuth struct {
	mu  sync.Mutex
	cfg OAuthConfig
}

// NewOAuth validates the configuration; it performs no I/O.
func NewOAuth(c *OAuthConfig) (*OAuth, error) {
	if err := ValidateProvider(c.Provider); err != nil {
		return nil, err
	}
	if c.Store == nil || c.Client == nil {
		return nil, fmt.Errorf("%w: oauth needs a token store and netx client", ErrCredential)
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	if c.Sleep == nil {
		c.Sleep = func(ctx context.Context, d time.Duration) error {
			t := time.NewTimer(d)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-t.C:
				return nil
			}
		}
	}
	if c.Output == nil {
		c.Output = io.Discard
	}
	return &OAuth{cfg: *c}, nil
}

// Login discovers the server, registers a client when needed, runs the device
// flow and stores the token.
func (o *OAuth) Login(ctx context.Context) (Token, error) {
	if o.cfg.Offline {
		return Token{}, fmt.Errorf("%w: login needs the network", ErrOffline)
	}
	md, err := Discover(ctx, o.cfg.Client, o.cfg.Issuer)
	if err != nil {
		return Token{}, err
	}
	if md.DeviceEndpoint == "" {
		return Token{}, fmt.Errorf("%w: no device authorization endpoint", ErrUnsupported)
	}
	client, secret := o.cfg.ClientID, o.cfg.ClientSecret
	if client == "" {
		if client, secret, err = registerClient(ctx, o.cfg.Client, &md); err != nil {
			return Token{}, err
		}
	}
	resp, err := o.deviceLogin(ctx, &md, client, secret)
	if err != nil {
		return Token{}, err
	}
	t := Token{
		Provider: o.cfg.Provider, AccessToken: resp.AccessToken, RefreshToken: resp.RefreshToken,
		Scope: resp.Scope, Issuer: md.Issuer, ClientID: client, ClientSecret: secret,
		TokenEndpoint: md.TokenEndpoint, RevocationEndpoint: md.RevocationEndpoint,
	}
	if resp.ExpiresIn > 0 {
		t.Expiry = o.cfg.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if err := o.cfg.Store.Save(ctx, &t); err != nil {
		return Token{}, err
	}
	return t, nil
}

// deviceLogin is RFC 8628 plus code_challenge on the device request and
// code_verifier on every poll.
func (o *OAuth) deviceLogin(ctx context.Context, md *Metadata, client, secret string) (tokenResponse, error) {
	verifier, challenge, err := PKCE()
	if err != nil {
		return tokenResponse{}, err
	}
	form := url.Values{
		"client_id": {client}, "scope": {strings.Join(o.cfg.Scopes, " ")},
		"code_challenge": {challenge}, "code_challenge_method": {"S256"},
	}
	var dev deviceAuth
	if err := doJSON(ctx, o.cfg.Client, http.MethodPost, md.DeviceEndpoint, form, &dev); err != nil {
		return tokenResponse{}, err
	}
	if dev.DeviceCode == "" || dev.ExpiresIn <= 0 {
		return tokenResponse{}, fmt.Errorf("%w: device authorization incomplete", ErrUnsupported)
	}
	uri := dev.VerificationURI
	if dev.VerificationURIComplete != "" {
		uri = dev.VerificationURIComplete
	}
	if _, err := fmt.Fprintf(o.cfg.Output, "open %s and enter code %s\n", uri, dev.UserCode); err != nil {
		return tokenResponse{}, fmt.Errorf("auth: device instructions: %w", err)
	}
	return o.pollDevice(ctx, md, client, secret, &dev, verifier)
}

// pollDevice waits for the device grant (RFC 8628 §3.5), honouring
// authorization_pending and slow_down until the code expires.
func (o *OAuth) pollDevice(ctx context.Context, md *Metadata, client, secret string, dev *deviceAuth, verifier string) (tokenResponse, error) {
	deadline := o.cfg.Now().Add(time.Duration(dev.ExpiresIn) * time.Second)
	interval := time.Duration(max(dev.Interval, 1)) * time.Second
	for {
		form := url.Values{
			"grant_type":  {"urn:ietf:params:oauth:grant-type:device_code"},
			"device_code": {dev.DeviceCode}, "client_id": {client}, "code_verifier": {verifier},
		}
		if secret != "" {
			form.Set("client_secret", secret)
		}
		resp, err := doForm(ctx, o.cfg.Client, md.TokenEndpoint, form)
		if err == nil && resp.AccessToken != "" {
			return resp, nil
		}
		if err == nil && resp.Error != "" {
			err = &OAuthError{Code: resp.Error, Status: http.StatusOK}
		}
		if err == nil {
			err = fmt.Errorf("%w: device token response without access_token", ErrCredential)
		}
		var oe *OAuthError
		if !errors.As(err, &oe) {
			return tokenResponse{}, err
		}
		switch oe.Code {
		case "authorization_pending":
		case "slow_down":
			interval += 5 * time.Second
		default:
			return tokenResponse{}, err
		}
		if o.cfg.Now().After(deadline) {
			return tokenResponse{}, fmt.Errorf("%w: device code expired", ErrInvalidGrant)
		}
		if err := o.cfg.Sleep(ctx, interval); err != nil {
			return tokenResponse{}, fmt.Errorf("auth: device poll: %w", err)
		}
	}
}

// Refresh rotates the access token in place and persists it.
func (o *OAuth) Refresh(ctx context.Context, t *Token) error {
	if o.cfg.Offline {
		return fmt.Errorf("%w: token refresh needs the network", ErrOffline)
	}
	if t.RefreshToken == "" {
		return fmt.Errorf("%w: no refresh token", ErrCredential)
	}
	if t.TokenEndpoint == "" {
		return fmt.Errorf("%w: no token endpoint", ErrUnsupported)
	}
	form := url.Values{"grant_type": {"refresh_token"}, "refresh_token": {t.RefreshToken}, "client_id": {t.ClientID}}
	if t.ClientSecret != "" {
		form.Set("client_secret", t.ClientSecret)
	}
	resp, err := doForm(ctx, o.cfg.Client, t.TokenEndpoint, form)
	if err != nil {
		return fmt.Errorf("auth: refresh %s: %w", t.Provider, err)
	}
	if resp.AccessToken == "" {
		return fmt.Errorf("%w: refresh returned no access token", ErrCredential)
	}
	t.AccessToken = resp.AccessToken
	if resp.RefreshToken != "" {
		t.RefreshToken = resp.RefreshToken
	}
	if resp.ExpiresIn > 0 {
		t.Expiry = o.cfg.Now().Add(time.Duration(resp.ExpiresIn) * time.Second)
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.cfg.Store.Save(ctx, t)
}

// Revoke implements RFC 7009 for the refresh token, then the access token.
func (o *OAuth) Revoke(ctx context.Context, t *Token) error {
	if o.cfg.Offline {
		return fmt.Errorf("%w: revocation needs the network", ErrOffline)
	}
	if t.RevocationEndpoint == "" {
		return fmt.Errorf("%w: no revocation endpoint", ErrUnsupported)
	}
	var first error
	for _, tok := range []struct{ value, hint string }{
		{t.RefreshToken, "refresh_token"}, {t.AccessToken, "access_token"},
	} {
		if tok.value == "" {
			continue
		}
		form := url.Values{"token": {tok.value}, "token_type_hint": {tok.hint}, "client_id": {t.ClientID}}
		if t.ClientSecret != "" {
			form.Set("client_secret", t.ClientSecret)
		}
		if _, err := doJSONRaw(ctx, o.cfg.Client, http.MethodPost, t.RevocationEndpoint, form.Encode(),
			"application/x-www-form-urlencoded"); err != nil && first == nil {
			first = err
		}
	}
	if first != nil {
		return fmt.Errorf("auth: revoke %s: %w", t.Provider, first)
	}
	return nil
}

// Sign implements [Signer]: load, refresh when expired, set the bearer.
func (o *OAuth) Sign(ctx context.Context, req *http.Request, _ []byte) error {
	t, err := o.cfg.Store.Load(ctx, o.cfg.Provider)
	if err != nil {
		return err
	}
	if t.Expired(o.cfg.Now()) {
		if err := o.Refresh(ctx, &t); err != nil {
			return err
		}
	}
	req.Header.Set("Authorization", "Bearer "+t.AccessToken)
	return nil
}

// Discover fetches RFC 8414 metadata and pins the issuer the caller asked for.
func Discover(ctx context.Context, c *netx.Client, issuer string) (Metadata, error) {
	issuer = strings.TrimSuffix(issuer, "/")
	if err := endpointOK(issuer); err != nil {
		return Metadata{}, err
	}
	var md Metadata
	if err := doJSON(ctx, c, http.MethodGet, issuer+"/.well-known/oauth-authorization-server", nil, &md); err != nil {
		return Metadata{}, fmt.Errorf("auth: discovery %s: %w", issuer, err)
	}
	if md.Issuer != "" && strings.TrimSuffix(md.Issuer, "/") != issuer {
		return Metadata{}, fmt.Errorf("%w: metadata issuer mismatch", ErrEndpoint)
	}
	md.Issuer = issuer
	if md.TokenEndpoint == "" {
		return Metadata{}, fmt.Errorf("%w: no token endpoint", ErrUnsupported)
	}
	if err := endpointOK(md.TokenEndpoint); err != nil {
		return Metadata{}, err
	}
	return md, nil
}

// registerClient performs RFC 7591 dynamic client registration.
func registerClient(ctx context.Context, c *netx.Client, md *Metadata) (string, string, error) {
	if md.RegistrationEndpoint == "" {
		return "", "", fmt.Errorf("%w: no client_id and no registration endpoint", ErrUnsupported)
	}
	body, err := json.Marshal(map[string]any{
		"client_name":                "damnbot",
		"grant_types":                []string{"urn:ietf:params:oauth:grant-type:device_code", "refresh_token"},
		"token_endpoint_auth_method": "none",
	})
	if err != nil {
		return "", "", fmt.Errorf("auth: registration body: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, md.RegistrationEndpoint, strings.NewReader(string(body)))
	if err != nil {
		return "", "", fmt.Errorf("auth: registration request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	raw, err := doNetx(c, req)
	if err != nil {
		return "", "", err
	}
	var out struct {
		ClientID     string `json:"client_id"`
		ClientSecret string `json:"client_secret"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", "", fmt.Errorf("auth: decode registration: %w", err)
	}
	if out.ClientID == "" {
		return "", "", fmt.Errorf("%w: registration returned no client_id", ErrCredential)
	}
	return out.ClientID, out.ClientSecret, nil
}

type deviceAuth struct {
	DeviceCode              string `json:"device_code"`
	UserCode                string `json:"user_code"`
	VerificationURI         string `json:"verification_uri"`
	VerificationURIComplete string `json:"verification_uri_complete"`
	ExpiresIn               int64  `json:"expires_in"`
	Interval                int64  `json:"interval"`
}

// PKCE returns an RFC 7636 verifier and its S256 challenge.
func PKCE() (verifier, challenge string, err error) {
	verifier, err = randomToken()
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:]), nil
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("auth: random: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func doJSON(ctx context.Context, c *netx.Client, method, endpoint string, form url.Values, out any) error {
	var body, ctype string
	if form != nil {
		body, ctype = form.Encode(), "application/x-www-form-urlencoded"
	}
	raw, err := doJSONRaw(ctx, c, method, endpoint, body, ctype)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("auth: decode response: %w", err)
	}
	return nil
}
