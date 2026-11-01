//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/gateway"

// newMCPRegistry has no servers in the minimal build: [modules.mcp] is not
// compiled in, and /reload-mcp answers "not supported" through a nil reloader.
func newMCPRegistry(string) (gateway.MCPReloader, error) { return nil, nil }
