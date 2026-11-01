//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"sync"
	"time"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp"
	"github.com/damnbot/damnbot/internal/netx"
	"github.com/damnbot/damnbot/internal/provider/auth"
	"github.com/damnbot/damnbot/internal/secret"
)

// mcpTokens is the OAuth 2.1 adapter of one MCP server. It reuses
// provider/auth wholesale (device grant + PKCE S256 + dynamic client
// registration + refresh + RFC 7009 revoke) and only adds the MCP policy:
// the issuer is pinned by server config, the granted scope must stay inside
// the minimal requested set, and no token ever reaches output.
type mcpTokens struct {
	server    string
	provider  string
	issuer    string
	oauth     *mcp.OAuth
	store     *auth.Store
	transport *auth.OAuth
	offline   bool
	now       func() time.Time

	mu sync.Mutex
}

// openMCPTokens resolves the token store (keychain, else a 0600 file under
// <data>/auth) and the OAuth transport of one server. A configured client
// secret is read from the keychain through its `keychain:<id>` reference;
// config never holds the value itself.
func openMCPTokens(ctx context.Context, srv mcp.Server, secretRef string, offline bool, out io.Writer) (*mcpTokens, error) {
	if srv.OAuth == nil {
		return nil, fmt.Errorf("%w: %s has no oauth section", mcp.ErrInvalidConfig, srv.Name)
	}
	if srv.URL == "" {
		return nil, fmt.Errorf("%w: %s oauth needs an http server", mcp.ErrInvalidConfig, srv.Name)
	}
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	paths, err := loader.Paths(activeProfile())
	if err != nil {
		return nil, fmt.Errorf("config paths: %w", err)
	}
	sec, err := secret.New(secret.Options{ConfigDir: paths.ConfigDir, Profile: activeProfile(), Keyring: setupKeyring()})
	if err != nil {
		return nil, fmt.Errorf("secret store: %w", err)
	}
	clientSecret := ""
	if secretRef != "" {
		id := secretRef[len("keychain:"):]
		if clientSecret, err = sec.Get(ctx, id); err != nil {
			return nil, fmt.Errorf("mcp %s: client secret %s: %w", srv.Name, id, err)
		}
	}
	store, err := auth.NewStore(sec, filepath.Join(paths.DataDir, "auth"))
	if err != nil {
		return nil, fmt.Errorf("token store: %w", err)
	}
	net := netx.New(netx.Config{Offline: offline, NoBackground: true})
	transport, err := auth.NewOAuth(&auth.OAuthConfig{
		Provider: mcpProvider(srv.Name), Issuer: srv.OAuth.Issuer, ClientID: srv.OAuth.ClientID,
		ClientSecret: clientSecret, Scopes: srv.OAuth.Scopes, Store: store,
		Client: net.Client("mcp", netx.Inference), Output: out, Offline: offline,
	})
	if err != nil {
		return nil, fmt.Errorf("oauth transport: %w", err)
	}
	return &mcpTokens{
		server: srv.Name, provider: mcpProvider(srv.Name), issuer: srv.OAuth.Issuer, oauth: srv.OAuth,
		store: store, transport: transport, offline: offline, now: time.Now,
	}, nil
}

// AccessToken returns a usable bearer token and refreshes an expired one. The
// provider name and issuer are checked before the token is used, so a token
// copied from another server can not authorize this one.
func (m *mcpTokens) AccessToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.load(ctx)
	if err != nil {
		return "", err
	}
	if t.Expired(m.now()) {
		if t.RefreshToken == "" {
			return "", fmt.Errorf("%w: %s token expired; run damnbot mcp auth login %s", mcp.ErrAuthRequired, m.server, m.server)
		}
		if err := m.transport.Refresh(ctx, &t); err != nil {
			return "", m.refreshError(err)
		}
	}
	if err := m.oauth.CheckScope(t.Scope); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// RefreshToken forces a refresh after a 401, so a server-side revoke or an
// early clock is observed once instead of retried forever.
func (m *mcpTokens) RefreshToken(ctx context.Context) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, err := m.load(ctx)
	if err != nil {
		return "", err
	}
	if t.RefreshToken == "" {
		return "", fmt.Errorf("%w: %s has no refresh token", mcp.ErrAuthRequired, m.server)
	}
	if err := m.transport.Refresh(ctx, &t); err != nil {
		return "", m.refreshError(err)
	}
	if err := m.oauth.CheckScope(t.Scope); err != nil {
		return "", err
	}
	return t.AccessToken, nil
}

// Login runs the device grant (RFC 8628 + PKCE S256, DCR when no client id is
// configured) and verifies the granted scope. An authorization server that
// widens the request has its token revoked instead of accepted.
func (m *mcpTokens) Login(ctx context.Context) (auth.Token, error) {
	t, err := m.transport.Login(ctx)
	if err != nil {
		return auth.Token{}, err
	}
	if serr := m.oauth.CheckScope(t.Scope); serr != nil {
		_ = m.transport.Revoke(ctx, &t)     //nolint:errcheck // best-effort cleanup of a rejected token
		_ = m.store.Delete(ctx, t.Provider) //nolint:errcheck // the credential must not survive the refusal
		return auth.Token{}, serr
	}
	return t, nil
}

// Logout revokes the refresh then access token (RFC 7009) and deletes the
// local copy only after a successful or unsupported revoke.
func (m *mcpTokens) Logout(ctx context.Context) (string, error) {
	t, err := m.load(ctx)
	if err != nil {
		return "", err
	}
	err = m.transport.Revoke(ctx, &t)
	if err != nil && !errors.Is(err, auth.ErrUnsupported) {
		return "failed", err
	}
	state := "revoked"
	if errors.Is(err, auth.ErrUnsupported) {
		state = "unsupported"
	}
	if derr := m.store.Delete(ctx, m.provider); derr != nil {
		return "failed", derr
	}
	return state, nil
}

// mcpTokenStatus is the only status shape printed: it has no token fields.
type mcpTokenStatus struct {
	Server  string `json:"server"`
	State   string `json:"state"`
	Issuer  string `json:"issuer,omitempty"`
	Scope   string `json:"scope,omitempty"`
	Expiry  string `json:"expiry,omitempty"`
	Refresh bool   `json:"refresh,omitempty"`
	Error   string `json:"error,omitempty"`
}

// Status reports the stored credential without printing it. An expired token
// with a refresh token is refreshed once; invalid_grant after a revoke is a
// distinct state, not a generic failure.
func (m *mcpTokens) Status(ctx context.Context) mcpTokenStatus {
	r := mcpTokenStatus{Server: m.server, State: "ok"}
	t, err := m.load(ctx)
	if err != nil {
		r.State, r.Error = "missing", cleanLine(err.Error())
		if !errors.Is(err, mcp.ErrAuthRequired) {
			r.State = "failed"
		}
		return r
	}
	r.Issuer, r.Scope, r.Expiry, r.Refresh = t.Issuer, t.Scope, expiryLabel(&t, m.now()), t.RefreshToken != ""
	switch {
	case !t.Expired(m.now()):
	case t.RefreshToken == "":
		r.State = "expired"
	default:
		if err := m.transport.Refresh(ctx, &t); err != nil {
			switch {
			case errors.Is(err, auth.ErrInvalidGrant):
				r.State = "invalid_grant"
			case errors.Is(err, auth.ErrOffline):
				r.State = "expired"
			default:
				r.State = "failed"
			}
			r.Error = cleanLine(err.Error())
			return r
		}
		r.Expiry = expiryLabel(&t, m.now())
	}
	if err := m.oauth.CheckScope(t.Scope); err != nil {
		r.State, r.Error = "failed", cleanLine(err.Error())
	}
	return r
}

// load reads the token and enforces the server binding.
func (m *mcpTokens) load(ctx context.Context) (auth.Token, error) {
	t, err := m.store.Load(ctx, m.provider)
	if err != nil {
		if errors.Is(err, auth.ErrTokenMissing) {
			return auth.Token{}, fmt.Errorf("%w: %s has no token; run damnbot mcp auth login %s", mcp.ErrAuthRequired, m.server, m.server)
		}
		return auth.Token{}, fmt.Errorf("mcp %s: %w", m.server, err)
	}
	if t.Provider != m.provider {
		return auth.Token{}, fmt.Errorf("%w: %s token provider mismatch", mcp.ErrAuthRequired, m.server)
	}
	if t.Issuer != "" && !mcpIssuerEqual(t.Issuer, m.issuer) {
		return auth.Token{}, fmt.Errorf("%w: %s token issuer does not match server config", mcp.ErrAuthRequired, m.server)
	}
	return t, nil
}

// refreshError keeps the authorization server's classification visible to
// callers (invalid_grant after a revoke) while staying an auth failure for
// the client.
func (m *mcpTokens) refreshError(err error) error {
	return fmt.Errorf("%w: refresh %s: %w", mcp.ErrAuthRequired, m.server, err)
}
