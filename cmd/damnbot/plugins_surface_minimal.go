//go:build minimal

package main

// pluginsSurface is empty in the minimal build: the Q18 catalog is not
// compiled into P13 (ADR-345), so the command does not exist.
func pluginsSurface() []surfaceEntry { return nil }
