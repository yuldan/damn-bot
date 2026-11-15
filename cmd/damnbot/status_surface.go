//go:build !minimal

package main

// statusSurface keeps `damnbot status` out of the minimal build's registry:
// the component aggregator and its store readers are excluded from the
// size-gated binary (P13), like browser/mcp/cua.
func statusSurface() []surfaceEntry {
	return []surfaceEntry{{
		Name:    "status",
		Summary: "Component, session, model, token, and context status",
		Phase:   phaseF3,
		Run:     runStatus,
		Usage:   statusUsage,
	}}
}
