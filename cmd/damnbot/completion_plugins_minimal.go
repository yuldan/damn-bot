//go:build minimal

package main

// addPluginsCompletion is a no-op in the minimal build: `plugins` is not a
// surface there (ADR-342).
func addPluginsCompletion(map[string][]string) {}
