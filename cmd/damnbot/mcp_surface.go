//go:build !minimal

package main

// mcpSurface registers the MCP command only when the module surface exists.
func mcpSurface() []surfaceEntry {
	return []surfaceEntry{{Name: "mcp", Summary: "MCP client and server", Phase: phaseF5, Run: runMCP, Usage: mcpUsage}}
}
