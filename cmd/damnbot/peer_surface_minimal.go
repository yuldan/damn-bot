//go:build minimal

package main

// peerSurface is empty in the minimal build: gwpeer is not linked in.
func peerSurface() []surfaceEntry { return nil }
