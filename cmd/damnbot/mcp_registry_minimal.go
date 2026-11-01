//go:build minimal

package main

import "github.com/damnbot/damnbot/internal/gateway"

// attachMCPRegistry has no servers in the minimal build: [modules.mcp] is not
// compiled in, and /reload-mcp answers "not supported" through a nil reloader.
func attachMCPRegistry(*serveGateway, string) (gateway.MCPReloader, error) { return nil, nil }
