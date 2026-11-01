//go:build !minimal

package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp"
)

// mcpRegistry owns the configured MCP servers of a daemon and lets
// "/reload-mcp" replace them without a restart. Reload is atomic: a config
// that does not parse or validate leaves the previous set in place.
type mcpRegistry struct {
	mu      sync.Mutex
	loader  *config.Loader
	path    string
	servers []mcp.Server
}

func newMCPRegistry(path string) (*mcpRegistry, error) {
	loader, err := config.New(config.OS())
	if err != nil {
		return nil, fmt.Errorf("config deps: %w", err)
	}
	if path == "" {
		paths, perr := loader.Paths(activeProfile())
		if perr != nil {
			return nil, fmt.Errorf("config paths: %w", perr)
		}
		path = paths.ConfigFile()
	}
	r := &mcpRegistry{loader: loader, path: path}
	if _, err := r.load(); err != nil {
		return nil, err
	}
	return r, nil
}

// ReloadMCP re-reads the config file and swaps the server set. It returns the
// number of configured servers; clients built from the previous set must be
// dropped by their owner (the registry is the only current holder).
func (r *mcpRegistry) ReloadMCP(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	n, err := r.load()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *mcpRegistry) load() (int, error) {
	cfg, err := r.loader.Load(r.path)
	if err != nil {
		return 0, err
	}
	servers := cliMCPServers(&cfg)
	for i := range servers {
		servers[i].Normalize()
		if verr := servers[i].Validate(); verr != nil {
			return 0, verr
		}
	}
	r.mu.Lock()
	r.servers = servers
	r.mu.Unlock()
	return len(servers), nil
}

// Servers returns a copy of the current validated server set.
func (r *mcpRegistry) Servers() []mcp.Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcp.Server(nil), r.servers...)
}
