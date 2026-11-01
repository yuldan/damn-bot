//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/doctor"
)

// attachCredentialPools is a no-op: the minimal build has no pool status.
func attachCredentialPools(*doctor.Options, *config.Config) {}
