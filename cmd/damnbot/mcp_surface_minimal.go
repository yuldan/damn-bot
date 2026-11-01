//go:build minimal

package main

// mcpSurface is empty in the minimal build: [modules.mcp] is not compiled in.
func mcpSurface() []surfaceEntry { return nil }
