//go:build !minimal

package main

import (
	"sort"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp"
)

// namedMCPServers selects all servers or the one named.
func namedMCPServers(cfg *config.Config, names []string) []mcp.Server {
	if len(names) == 1 {
		if srv, ok := mcpServerByName(cfg, names[0]); ok {
			return []mcp.Server{srv}
		}
		return nil
	}
	return cliMCPServers(cfg)
}

// cliMCPServers converts every configured server in name order.
func cliMCPServers(cfg *config.Config) []mcp.Server {
	names := make([]string, 0, len(cfg.Modules.MCP.Servers))
	for name := range cfg.Modules.MCP.Servers {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]mcp.Server, 0, len(names))
	for _, name := range names {
		out = append(out, cliMCPServer(name, cfg.Modules.MCP.Servers[name]))
	}
	return out
}

func cliMCPServer(name string, s config.ModulesMCPServer) mcp.Server { //nolint:gocritic // one validated config section converted once
	srv := mcp.Server{
		Name: name, Command: s.Command, Args: s.Args, Env: s.Env, URL: s.URL,
		Headers: s.Headers, Trust: mcp.Trust(s.Trust), TimeoutS: s.TimeoutS,
	}
	if s.OAuth != nil {
		srv.OAuth = &mcp.OAuth{Issuer: s.OAuth.Issuer, ClientID: s.OAuth.ClientID, Scopes: s.OAuth.Scopes}
	}
	for tool, override := range s.Tools {
		if override.ReadOnly {
			if srv.ReadOnly == nil {
				srv.ReadOnly = make(map[string]bool, len(s.Tools))
			}
			srv.ReadOnly[tool] = true
		}
	}
	return srv
}

func mcpServerByName(cfg *config.Config, name string) (mcp.Server, bool) {
	section, ok := cfg.Modules.MCP.Servers[name]
	if !ok {
		return mcp.Server{}, false
	}
	return cliMCPServer(name, section), true
}

func mcpProvider(server string) string { return "mcp." + server }

func mcpIssuerEqual(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}
