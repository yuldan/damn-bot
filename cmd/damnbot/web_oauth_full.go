//go:build !minimal

package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
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

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/secret"
	"github.com/damnbot/damnbot/internal/web"
)

// Web auth environment (UI-015). Credentials never live in config.toml: the
// drain secret is a keychain entry name, and the Nous client id is public but
// still an operator setting.
const (
	envNousClientID    = "DAMNBOT_WEB_NOUS_CLIENT_ID"
	envNousPortalURL   = "DAMNBOT_WEB_NOUS_PORTAL_URL"
	envNousRedirect    = "DAMNBOT_WEB_NOUS_REDIRECT_URL"
	envNousUserInfo    = "DAMNBOT_WEB_NOUS_USERINFO_URL"
	envNousVaultEntry  = "DAMNBOT_WEB_NOUS_TOKEN_SECRET"
	envDrainVaultEntry = "DAMNBOT_WEB_DRAIN_SECRET"
)

const (
	defaultNousPortal = "https://portal.nousresearch.com"
	oauthVaultEntry   = "web-oauth-token"
	oauthPendingTTL   = 10 * time.Minute
	oauthMaxPending   = 64
	oauthMaxBody      = 64 << 10
)

// secretVault is the keychain subset the web auth providers use; *secret.Store
// implements it in production and tests inject a memory store.
type secretVault interface {
	Get(ctx context.Context, name string) (string, error)
	Set(ctx context.Context, name, value string) error
	Delete(ctx context.Context, name string) error
}

// pendingPKCE is one in-flight authorization-code exchange.
type pendingPKCE struct {
	verifier string
	expires  time.Time
}

// codeOAuth is the shared authorization-code + PKCE provider of the Nous
// Portal and a self-hosted OIDC issuer. It stores the token set only in the
// keychain and never returns it to a page, log line or response body.
type codeOAuth struct {
	name        string
	clientID    string
	secretName  string
	redirect    string
	scopes      string
	authURL     string
	tokenURL    string
	userInfoURL string
	endSession  string
	vaultEntry  string
	client      *netx.Client
	vault       secretVault
	now         func() time.Time
	mu          sync.Mutex
	pending     map[string]pendingPKCE
}

// secretKeychain is the production vault.
type secretKeychain struct{ store *secret.Store }

func (s secretKeychain) Get(ctx context.Context, name string) (string, error) {
	v, err := s.store.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("keychain get %q: %w", name, err)
	}
	return v, nil
}

func (s secretKeychain) Set(ctx context.Context, name, value string) error {
	if err := s.store.Set(ctx, name, value); err != nil {
		return fmt.Errorf("keychain set %q: %w", name, err)
	}
	return nil
}

func (s secretKeychain) Delete(ctx context.Context, name string) error {
	if err := s.store.Delete(ctx, name); err != nil {
		return fmt.Errorf("keychain delete %q: %w", name, err)
	}
	return nil
}

// newNousOAuth builds the Nous Portal provider from the environment. A missing
// client id or a non-TLS portal fails closed before the listener starts.
func newNousOAuth(v secretVault, listen string) (*codeOAuth, error) {
	env := config.OS().Getenv
	portal := strings.TrimSuffix(strings.TrimSpace(env(envNousPortalURL)), "/")
	if portal == "" {
		portal = defaultNousPortal
	}
	if err := validateOAuthURL(portal); err != nil {
		return nil, fmt.Errorf("web nous portal: %w", err)
	}
	clientID := strings.TrimSpace(env(envNousClientID))
	if clientID == "" {
		return nil, errors.New("web nous: DAMNBOT_WEB_NOUS_CLIENT_ID is required")
	}
	redirect := strings.TrimSpace(env(envNousRedirect))
	if redirect == "" {
		redirect = webCallbackURL(listen)
	}
	userinfo := strings.TrimSpace(env(envNousUserInfo))
	if userinfo == "" {
		userinfo = portal + "/oauth/userinfo"
	}
	vaultEntry := strings.TrimSpace(env(envNousVaultEntry))
	if vaultEntry == "" {
		vaultEntry = oauthVaultEntry
	}
	return newCodeOAuth(v, &codeOAuthConfig{
		name: "nous", clientID: clientID, redirect: redirect, vaultEntry: vaultEntry,
		scopes: "openid profile email", authURL: portal + "/oauth/authorize",
		tokenURL: portal + "/oauth/token", userInfoURL: userinfo,
		endSession: portal + "/oauth/logout",
	})
}

// codeOAuthConfig is the endpoint set of one authorization-code provider.
type codeOAuthConfig struct {
	name, clientID, secretName, redirect, scopes, vaultEntry string
	authURL, tokenURL, userInfoURL, endSession               string
}

// newCodeOAuth validates and builds the provider.
func newCodeOAuth(v secretVault, cfg *codeOAuthConfig) (*codeOAuth, error) {
	if v == nil {
		return nil, errors.New("web oauth: no secret vault")
	}
	for _, raw := range []string{cfg.authURL, cfg.tokenURL, cfg.userInfoURL} {
		if raw == "" {
			return nil, errors.New("web oauth: incomplete endpoint set")
		}
		if err := validateOAuthURL(raw); err != nil {
			return nil, err
		}
	}
	if cfg.clientID == "" || cfg.redirect == "" || cfg.vaultEntry == "" {
		return nil, errors.New("web oauth: incomplete provider configuration")
	}
	return &codeOAuth{
		name: cfg.name, clientID: cfg.clientID, secretName: cfg.secretName,
		redirect: cfg.redirect, scopes: cfg.scopes, authURL: cfg.authURL,
		tokenURL: cfg.tokenURL, userInfoURL: cfg.userInfoURL, endSession: cfg.endSession,
		vaultEntry: cfg.vaultEntry, vault: v, now: time.Now, pending: map[string]pendingPKCE{},
		client: netx.New(netx.Config{}).Client("web-oauth", netx.Background),
	}, nil
}

// AuthorizeURL mints one PKCE exchange and returns the provider redirect.
func (o *codeOAuth) AuthorizeURL(state, nonce string) string {
	verifier, challenge := pkcePair()
	o.mu.Lock()
	o.pruneLocked()
	o.pending[state] = pendingPKCE{verifier: verifier, expires: o.now().Add(oauthPendingTTL)}
	o.mu.Unlock()
	q := url.Values{
		"response_type":         {"code"},
		"client_id":             {o.clientID},
		"redirect_uri":          {o.redirect},
		"scope":                 {o.scopes},
		"state":                 {state},
		"code_challenge":        {challenge},
		"code_challenge_method": {"S256"},
	}
	if nonce != "" {
		q.Set("nonce", nonce)
	}
	return o.authURL + "?" + q.Encode()
}

// pruneLocked drops expired pending exchanges and caps the map.
func (o *codeOAuth) pruneLocked() {
	now := o.now()
	for state, p := range o.pending {
		if now.After(p.expires) {
			delete(o.pending, state)
		}
	}
	for len(o.pending) >= oauthMaxPending {
		for state := range o.pending {
			delete(o.pending, state)
			break
		}
	}
}

// Exchange redeems the code with its PKCE verifier, stores the token set in
// the keychain and returns the principal from userinfo.
func (o *codeOAuth) Exchange(ctx context.Context, code, state string) (string, error) {
	if code == "" {
		return "", errors.New("web oauth: empty code")
	}
	o.mu.Lock()
	p, ok := o.pending[state]
	delete(o.pending, state)
	o.mu.Unlock()
	if !ok || o.now().After(p.expires) {
		return "", errors.New("web oauth: unknown or expired state")
	}
	form := url.Values{
		"grant_type":    {"authorization_code"},
		"code":          {code},
		"redirect_uri":  {o.redirect},
		"client_id":     {o.clientID},
		"code_verifier": {p.verifier},
	}
	if o.secretName != "" {
		secret, err := o.vault.Get(ctx, o.secretName)
		if err != nil || secret == "" {
			return "", errors.New("web oauth: client secret unavailable")
		}
		form.Set("client_secret", secret)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.tokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", errors.New("web oauth: token request")
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	body, err := o.do(req)
	if err != nil {
		return "", err
	}
	var tok struct {
		Access  string `json:"access_token"`
		Refresh string `json:"refresh_token"`
		Expires int    `json:"expires_in"`
	}
	if uerr := json.Unmarshal(body, &tok); uerr != nil || tok.Access == "" {
		return "", errors.New("web oauth: token response")
	}
	principal, err := o.userinfo(ctx, tok.Access)
	if err != nil {
		return "", err
	}
	stored := struct {
		Access  string    `json:"access_token"`
		Refresh string    `json:"refresh_token,omitempty"`
		Expiry  time.Time `json:"expiry,omitzero"`
	}{Access: tok.Access, Refresh: tok.Refresh}
	if tok.Expires > 0 {
		stored.Expiry = o.now().Add(time.Duration(tok.Expires) * time.Second)
	}
	raw, merr := json.Marshal(stored) // #nosec G117 -- the token set is written only to the keychain entry named by vaultEntry
	if merr != nil {
		return "", errors.New("web oauth: encode token")
	}
	if serr := o.vault.Set(ctx, o.vaultEntry, string(raw)); serr != nil {
		return "", errors.New("web oauth: keychain write failed")
	}
	return principal, nil
}

// userinfo returns the principal of an access token. A provider without a
// userinfo endpoint or a response without an identity fails closed.
func (o *codeOAuth) userinfo(ctx context.Context, access string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.userInfoURL, nil)
	if err != nil {
		return "", errors.New("web oauth: userinfo request")
	}
	req.Header.Set("Authorization", "Bearer "+access)
	req.Header.Set("Accept", "application/json")
	body, err := o.do(req)
	if err != nil {
		return "", err
	}
	var info struct {
		Email    string `json:"email"`
		Sub      string `json:"sub"`
		Name     string `json:"name"`
		Username string `json:"preferred_username"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return "", errors.New("web oauth: userinfo response")
	}
	for _, principal := range []string{info.Email, info.Username, info.Sub, info.Name} {
		if principal != "" {
			return principal, nil
		}
	}
	return "", errors.New("web oauth: no principal")
}

// EndSessionURL names the provider logout endpoint; the principal never becomes
// part of the URL.
func (o *codeOAuth) EndSessionURL(string) string {
	if o.endSession == "" {
		return ""
	}
	q := url.Values{"client_id": {o.clientID}}
	return o.endSession + "?" + q.Encode()
}

// do sends one request and returns the bounded body. The Authorization header
// and body are never logged.
func (o *codeOAuth) do(req *http.Request) ([]byte, error) {
	resp, err := o.client.Do(req)
	if err != nil {
		return nil, errors.New("web oauth: provider unreachable")
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	cerr := resp.Body.Close()
	if rerr != nil {
		return nil, errors.New("web oauth: provider response")
	}
	if cerr != nil {
		return nil, fmt.Errorf("web oauth: close response: %w", cerr)
	}
	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, fmt.Errorf("web oauth: provider HTTP %d", resp.StatusCode)
	}
	return body, nil
}

// pkcePair returns one RFC 7636 S256 verifier and challenge.
func pkcePair() (verifier, challenge string) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", ""
	}
	verifier = base64.RawURLEncoding.EncodeToString(raw)
	sum := sha256.Sum256([]byte(verifier))
	return verifier, base64.RawURLEncoding.EncodeToString(sum[:])
}

// webCallbackURL derives the local redirect URI from the listener.
func webCallbackURL(listen string) string {
	scheme := "http"
	if strings.HasPrefix(listen, "unix:") {
		listen = "127.0.0.1:7878"
	}
	if host, port, ok := strings.Cut(listen, ":"); ok && host != "" && port != "" {
		return scheme + "://" + listen + "/auth/callback"
	}
	return scheme + "://127.0.0.1:7878/auth/callback"
}

// validateOAuthURL accepts https or a loopback http endpoint; no other scheme
// or host is allowed.
func validateOAuthURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" {
		return errors.New("oauth endpoint is not an absolute URL")
	}
	if u.Scheme == "https" {
		return nil
	}
	if u.Scheme != "http" {
		return errors.New("oauth endpoint must be https or loopback http")
	}
	host := u.Hostname()
	if host == "localhost" || host == "127.0.0.1" || host == "::1" {
		return nil
	}
	return errors.New("oauth endpoint must be https or loopback http")
}

// drainVerifier checks the shared bearer secret in constant time. It refuses a
// secret below 256 bits or with too few distinct bytes, so the endpoint stays
// disabled instead of accepting a weak value.
type drainVerifier struct{ want string }

// newDrainVerifier loads the keychain entry named by name.
func newDrainVerifier(ctx context.Context, v secretVault, name string) (web.DrainVerifier, error) {
	if v == nil || name == "" {
		return nil, errors.New("web drain: keychain secret name is required")
	}
	secret, err := v.Get(ctx, name)
	if err != nil || secret == "" {
		return nil, errors.New("web drain: secret is unavailable")
	}
	if err := strongDrainSecret(secret); err != nil {
		return nil, err
	}
	return drainVerifier{want: secret}, nil
}

// strongDrainSecret enforces at least 256 bits and eight distinct bytes.
func strongDrainSecret(secret string) error {
	if len(secret) < 32 {
		return errors.New("web drain: secret must be at least 256 bits")
	}
	seen := map[byte]bool{}
	for i := range len(secret) {
		seen[secret[i]] = true
	}
	if len(seen) < 8 {
		return errors.New("web drain: secret has too little entropy")
	}
	return nil
}

// VerifyBearer compares the presented token in constant time.
func (d drainVerifier) VerifyBearer(token string) bool {
	return subtle.ConstantTimeCompare([]byte(token), []byte(d.want)) == 1 && token != ""
}

// newWebDrain wires the drain verifier from the environment.
func newWebDrain(ctx context.Context, v secretVault) (web.DrainVerifier, error) {
	name := strings.TrimSpace(config.OS().Getenv(envDrainVaultEntry))
	return newDrainVerifier(ctx, v, name)
}

// newWebNous wires the Nous provider from the environment.
func newWebNous(v secretVault, listen string) (web.OIDCProvider, error) {
	return newNousOAuth(v, listen)
}

// oidcDiscovery is the RFC 8414 / OIDC discovery subset used by a self-hosted
// issuer.
type oidcDiscovery struct {
	Issuer        string `json:"issuer"`
	Authorization string `json:"authorization_endpoint"`
	Token         string `json:"token_endpoint"`
	UserInfo      string `json:"userinfo_endpoint"`
	EndSession    string `json:"end_session_endpoint"`
}

// newWebOIDC discovers a self-hosted issuer. A missing endpoint set fails
// closed; the client secret is a keychain entry named by config.
func newWebOIDC(ctx context.Context, v secretVault, issuer, clientID, secretName, listen string) (web.OIDCProvider, error) {
	if issuer == "" || clientID == "" {
		return nil, errors.New("web oidc: issuer and client id are required")
	}
	if err := validateOAuthURL(issuer); err != nil {
		return nil, err
	}
	client := netx.New(netx.Config{}).Client("web-oauth", netx.Background)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(issuer, "/")+"/.well-known/openid-configuration", nil)
	if err != nil {
		return nil, errors.New("web oidc: discovery request")
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.New("web oidc: issuer unreachable")
	}
	body, rerr := io.ReadAll(io.LimitReader(resp.Body, oauthMaxBody))
	cerr := resp.Body.Close()
	if rerr != nil || cerr != nil || resp.StatusCode < 200 || resp.StatusCode > 299 {
		return nil, errors.New("web oidc: discovery failed")
	}
	var md oidcDiscovery
	if err := json.Unmarshal(body, &md); err != nil || md.Authorization == "" || md.Token == "" {
		return nil, errors.New("web oidc: incomplete discovery")
	}
	if md.UserInfo == "" {
		return nil, errors.New("web oidc: discovery has no userinfo endpoint")
	}
	return newCodeOAuth(v, &codeOAuthConfig{
		name: "oidc", clientID: clientID, secretName: secretName,
		redirect: webCallbackURL(listen), scopes: "openid profile email",
		vaultEntry: "web-oidc-token", authURL: md.Authorization, tokenURL: md.Token,
		userInfoURL: md.UserInfo, endSession: md.EndSession,
	})
}

// describeWebAuth maps the mode to the env/keychain requirements for a typed
// startup error.
func describeWebAuth(mode string) string {
	switch mode {
	case config.WebAuthDrain:
		return "web drain: set " + envDrainVaultEntry + " to a keychain entry with >= 256 bits"
	case config.WebAuthNous:
		return "web nous: set " + envNousClientID + " (and optionally " + envNousPortalURL + ")"
	case config.WebAuthOIDC:
		return "web oidc: set web.oidc_issuer and web.oidc_client_id"
	default:
		return "web auth"
	}
}

// wireWebOAuth builds the non-basic provider selected by [web].auth. A
// missing keychain entry or environment value fails the listener start.
func wireWebOAuth(ctx context.Context, wc *web.Config, cfg *config.Config) error {
	store, err := secretsStore()
	if err != nil {
		return fmt.Errorf("%s: keychain: %w", describeWebAuth(cfg.Web.Auth), err)
	}
	vault := secretKeychain{store: store}
	switch cfg.Web.Auth {
	case config.WebAuthDrain:
		verifier, derr := newWebDrain(ctx, vault)
		if derr != nil {
			return fmt.Errorf("%s: %w", describeWebAuth(cfg.Web.Auth), derr)
		}
		wc.Drain = verifier
	case config.WebAuthNous:
		provider, perr := newWebNous(vault, cfg.Web.Listen)
		if perr != nil {
			return fmt.Errorf("%s: %w", describeWebAuth(cfg.Web.Auth), perr)
		}
		wc.Nous = provider
	case config.WebAuthOIDC:
		provider, perr := newWebOIDC(ctx, vault, cfg.Web.OIDCIssuer, cfg.Web.OIDCClientID, cfg.Web.OIDCClientSecretSecret, cfg.Web.Listen)
		if perr != nil {
			return fmt.Errorf("%s: %w", describeWebAuth(cfg.Web.Auth), perr)
		}
		wc.OIDC = provider
	}
	return nil
}
