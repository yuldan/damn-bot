//go:build !minimal

package main

import (
	"slices"
	"strings"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp"
)

// cliMCPServers converts [modules.mcp.servers] into the module's validated
// value type. Config and mcp.Server are deliberately separate (E-06): the
// composition root performs the one conversion and never hands the module a
// config struct.
func cliMCPServers(cfg *config.Config) []mcp.Server {
	names := make([]string, 0, len(cfg.Modules.MCP.Servers))
	for name := range cfg.Modules.MCP.Servers {
		names = append(names, name)
	}
	slices.Sort(names)
	out := make([]mcp.Server, 0, len(names))
	for _, name := range names {
		out = append(out, cliMCPServer(name, cfg.Modules.MCP.Servers[name]))
	}
	return out
}

// cliMCPServer converts one section without inventing defaults: trust and
// timeout keep their config-normalized values, and a read-only override is
// the only field that can lower an effect.
func cliMCPServer(name string, s config.ModulesMCPServer) mcp.Server {
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

// mcpProvider is the token-store provider id of one server: stable across
// restarts, valid for auth.ValidateProvider, and unable to collide with a
// provider name (which never starts with "mcp.").
func mcpProvider(server string) string { return "mcp." + server }

// mcpServerByName returns the configured server and whether it exists.
func mcpServerByName(cfg *config.Config, name string) (mcp.Server, bool) {
	section, ok := cfg.Modules.MCP.Servers[name]
	if !ok {
		return mcp.Server{}, false
	}
	return cliMCPServer(name, section), true
}

// mcpIssuerEqual compares issuers ignoring a trailing slash, the same
// normalization discovery applies.
func mcpIssuerEqual(a, b string) bool {
	return strings.TrimSuffix(a, "/") == strings.TrimSuffix(b, "/")
}
