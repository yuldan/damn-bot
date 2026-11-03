//go:build minimal

package main

// cuaSurface is empty in the minimal build: computer_use is a lazy module and
// [modules.computer_use] is not compiled in.
func cuaSurface() []surfaceEntry { return nil }
