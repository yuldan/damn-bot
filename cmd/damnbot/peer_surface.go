//go:build !minimal

package main

// peerSurface registers `damnbot peer` (CLI-027) in the full build. The
// minimal build omits the surface so the peer transport is never linked in.
func peerSurface() []surfaceEntry {
	return []surfaceEntry{{
		Name: "peer", Summary: "Peer gateways, rooms, relay, and profile routing",
		Phase: phaseF7, Run: runPeer, Usage: peerUsage,
	}}
}
