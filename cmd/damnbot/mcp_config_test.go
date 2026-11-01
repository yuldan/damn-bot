package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/damnbot/damnbot/internal/config"
	"github.com/damnbot/damnbot/internal/mcp"
)

func TestCLIMCPServerConversion(t *testing.T) {
	t.Parallel()
	cfg := &config.Config{}
	cfg.Modules.MCP.Servers = map[string]config.ModulesMCPServer{
		"remote": {
			URL: "https://mcp.example.test/v1", Trust: "full", TimeoutS: 30,
			OAuth: &config.ModulesMCPOAuth{Issuer: "https://as.example.test", ClientID: "dcr-1", Scopes: []string{"mcp.read"}},
			Tools: map[string]config.ModulesMCPTool{"search": {ReadOnly: true}, "write": {ReadOnly: false}},
		},
		"local": {Command: []string{"srv"}, Trust: "untrusted", TimeoutS: 120},
	}
	servers := cliMCPServers(cfg)
	if len(servers) != 2 || servers[0].Name != "local" || servers[1].Name != "remote" {
		t.Fatalf("servers = %+v", servers)
	}
	remote := servers[1]
	if remote.OAuth == nil || remote.OAuth.Issuer != "https://as.example.test" || len(remote.OAuth.Scopes) != 1 {
		t.Fatalf("oauth = %+v", remote.OAuth)
	}
	if !remote.ReadOnly["search"] || remote.ReadOnly["write"] {
		t.Fatalf("read_only = %+v", remote.ReadOnly)
	}
	remote.Normalize()
	if err := remote.Validate(); err != nil {
		t.Fatalf("converted server invalid: %v", err)
	}
	if _, ok := mcpServerByName(cfg, "local"); !ok {
		t.Fatal("mcpServerByName lost local")
	}
	if _, ok := mcpServerByName(cfg, "absent"); ok {
		t.Fatal("mcpServerByName invented a server")
	}
	if got := mcpProvider("remote"); got != "mcp.remote" {
		t.Fatalf("provider = %q", got)
	}
}

func TestOpenMCPTokensRejectsNonOAuth(t *testing.T) {
	t.Parallel()
	_, err := openMCPTokens(context.Background(), mcp.Server{Name: "remote", URL: "https://h/mcp"}, "", false, io.Discard)
	if !errors.Is(err, mcp.ErrInvalidConfig) {
		t.Fatalf("openMCPTokens = %v, want ErrInvalidConfig", err)
	}
	_, err = openMCPTokens(context.Background(), mcp.Server{Name: "local", Command: []string{"srv"},
		OAuth: &mcp.OAuth{Issuer: "https://as.example.test", Scopes: []string{"mcp.read"}}}, "", false, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "http") {
		t.Fatalf("stdio oauth = %v, want http-only error", err)
	}
}

func TestMCPIssuerEqual(t *testing.T) {
	t.Parallel()
	if !mcpIssuerEqual("https://as.example.test/", "https://as.example.test") {
		t.Fatal("trailing slash not normalized")
	}
	if mcpIssuerEqual("https://as.example.test", "https://evil.example.test") {
		t.Fatal("different issuers compared equal")
	}
}
