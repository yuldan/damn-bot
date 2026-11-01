//go:build !minimal

package main

import (
	"context"
	"fmt"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider"
	"github.com/damnbot/damnbot/internal/provider/credpool"
)

// resolverCredentials holds the credential-pool seam of the provider
// resolver. The minimal build compiles no pool (P13).
type resolverCredentials struct {
	// secretGet resolves a keychain:<id> pool entry; nil lazily opens the
	// profile's internal/secret store. Tests inject it.
	secretGet func(context.Context, string) (string, error)
	// pools shares one pool per provider across that provider's routes
	// (models, fallback, light), so cooldowns are provider-wide.
	pools map[string]*credpool.Pool
}

// hasCredentials reports whether the custom provider carries a pool.
func hasCredentials(c *config.CustomProvider) bool { return c.Credentials != nil }

// attachCredentialPool resolves the keychain references of one custom
// provider and binds the cached pool to rt. One pool per provider means a
// cooldown covers every model route of that provider, and rotation never
// changes the base_url (EC-PROV-017).
func (r *providerResolver) attachCredentialPool(ctx context.Context, rt *provider.Route, c *config.CustomProvider) error {
	if c.Credentials == nil {
		return nil
	}
	pool, err := r.credentialPool(ctx, c)
	if err != nil {
		return err
	}
	provider.WithCredentialPool(rt, pool)
	return nil
}

// credentialPool resolves and caches one provider's pool.
func (r *providerResolver) credentialPool(ctx context.Context, c *config.CustomProvider) (*credpool.Pool, error) {
	if p := r.pools[c.Name]; p != nil {
		return p, nil
	}
	creds := make([]credpool.Credential, 0, len(c.Credentials))
	for _, pc := range c.Credentials {
		id, ok := strings.CutPrefix(pc.Secret, "keychain:")
		if !ok || id == "" {
			return nil, fmt.Errorf("%w: providers.custom[%q].credentials[%q].secret must be a keychain:<id> reference", ErrProviderResolve, c.Name, pc.ID)
		}
		value, err := r.secretValue(ctx, id)
		if err != nil {
			return nil, fmt.Errorf("%w: providers.custom[%q].credentials[%q]: %w", ErrProviderResolve, c.Name, pc.ID, err)
		}
		creds = append(creds, credpool.NewCredential(pc.ID, value))
	}
	pool, err := credpool.New(creds)
	if err != nil {
		return nil, fmt.Errorf("%w: providers.custom[%q]: %w", ErrProviderResolve, c.Name, err)
	}
	if r.pools == nil {
		r.pools = make(map[string]*credpool.Pool, 1)
	}
	r.pools[c.Name] = pool
	return pool, nil
}

// secretValue resolves one named keychain entry, opening the profile store
// on first use. Values never leave the pool.
func (r *providerResolver) secretValue(ctx context.Context, name string) (string, error) {
	if r.secretGet != nil {
		return r.secretGet(ctx, name)
	}
	st, err := secretsStore()
	if err != nil {
		return "", err
	}
	value, err := st.Get(ctx, name)
	if err != nil {
		return "", fmt.Errorf("keychain %q: %w", name, err)
	}
	return value, nil
}
