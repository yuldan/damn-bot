//go:build minimal

package main

// addPluginsCompletion is a no-op in the minimal build: `plugins` is not a
// surface there (ADR-340).
func addPluginsCompletion(map[string][]string) {}
