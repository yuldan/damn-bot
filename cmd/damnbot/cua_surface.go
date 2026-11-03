//go:build !minimal

package main

// phaseF6 is the owner phase of the F6 module surfaces (ADR-064/ADR-197).
const phaseF6 = 6

// cuaSurface registers `damnbot cua` only when the module surface exists.
func cuaSurface() []surfaceEntry {
	return []surfaceEntry{{
		Name: "cua", Summary: "Computer use: external backend status and actions",
		Phase: phaseF6, Run: runCua, Usage: cuaUsage,
	}}
}
