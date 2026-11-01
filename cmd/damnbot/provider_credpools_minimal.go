//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/provider"
)

// resolverCredentials is empty: the minimal build compiles no pool (P13).
type resolverCredentials struct{}

// hasCredentials is always false: the field is absent from this build.
func hasCredentials(*config.CustomProvider) bool { return false }

// attachCredentialPool is a no-op: the minimal build compiles no pool.
func (r *providerResolver) attachCredentialPool(context.Context, *provider.Route, *config.CustomProvider) error {
	return nil
}
