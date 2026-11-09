//go:build !minimal

package main

// pluginsSurface registers the Q18 catalog surface only when the full build
// carries the embedded catalog (ADR-340).
func pluginsSurface() []surfaceEntry {
	return []surfaceEntry{{Name: "plugins", Summary: "Plugin catalog Q18: list, show, verify", Phase: phaseF8, Run: runPlugins, Usage: pluginsUsage}}
}
