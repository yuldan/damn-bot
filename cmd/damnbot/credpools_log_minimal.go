//go:build minimal

package main

import (
	"context"

	"github.com/damnbot/damnbot/internal/provider"
)

// configureCredentialLog is a no-op: the minimal build has no pool event.
func configureCredentialLog(context.Context, *provider.Config) {}
