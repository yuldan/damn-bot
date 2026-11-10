//go:build !minimal

package main

func monitoringSurface() surfaceEntry {
	return surfaceEntry{Name: "monitoring", Summary: "Gateway health and redacted diagnostics", Phase: phaseF8, Run: runMonitoring, Usage: monitoringUsage}
}
