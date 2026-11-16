//go:build minimal

package main

// statusSurface is empty in the minimal build: the status aggregator is
// excluded from the size-gated binary (P13).
func statusSurface() []surfaceEntry { return nil }
