//go:build !minimal

package main

// addPluginsCompletion exposes the Q18 `plugins` vocabulary only in the full
// build; the minimal binary has no such surface (ADR-345).
func addPluginsCompletion(m map[string][]string) {
	m["plugins"] = []string{"list", "show", "verify"}
}
