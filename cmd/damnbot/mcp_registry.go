//go:build !minimal

package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/gateway"
	"github.com/damnbot/damnbot/internal/mcp"
)

// mcpRegistry reloads configured MCP servers atomically; a rejected config
// leaves the previous set in place.
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

// ReloadMCP re-reads the config file and swaps the validated server set.
func (r *mcpRegistry) ReloadMCP(ctx context.Context) (int, error) {
	if err := ctx.Err(); err != nil {
		return 0, fmt.Errorf("mcp reload: %w", err)
	}
	n, err := r.load()
	if err != nil {
		return 0, err
	}
	return n, nil
}

func (r *mcpRegistry) load() (int, error) {
	cfg, err := r.loader.Load(r.path)
	if errors.Is(err, os.ErrNotExist) {
		cfg, err = r.loader.Parse(nil, r.path)
	}
	if err != nil {
		return 0, fmt.Errorf("mcp: load config %s: %w", r.path, err)
	}
	servers := cliMCPServers(&cfg)
	for i := range servers {
		servers[i].Normalize()
		if verr := servers[i].Validate(); verr != nil {
			return 0, fmt.Errorf("mcp: validate %s: %w", servers[i].Name, verr)
		}
	}
	r.mu.Lock()
	r.servers = servers
	r.mu.Unlock()
	return len(servers), nil
}

// attachMCPRegistry builds the serve registry, closing the webhook on failure.
func attachMCPRegistry(g *serveGateway, path string) (gateway.MCPReloader, error) {
	reg, err := newMCPRegistry(path)
	if err != nil {
		g.closeWebhook()
		return nil, fmt.Errorf("mcp registry: %w", err)
	}
	return reg, nil
}

// Servers returns a copy of the current validated server set.
func (r *mcpRegistry) Servers() []mcp.Server {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]mcp.Server(nil), r.servers...)
}
