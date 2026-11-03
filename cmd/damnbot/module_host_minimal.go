//go:build minimal

package main

import (
	"github.com/damnbot/damnbot/internal/ctxdb"
	"github.com/damnbot/damnbot/internal/modules/host"
)

// newRunModuleHost is empty in the minimal build: no module is compiled in,
// so there is no deferred catalog to hand a host to.
func newRunModuleHost(*ctxdb.Store, string) host.Host { return nil }
