//go:build minimal

package main

// f8Surfaces is empty in the minimal build: the F8 modules are not linked.
func f8Surfaces() []surfaceEntry { return nil }
