//go:build minimal

package main

import "io"

// helpDocsCommand is absent from the minimal build: the offline guide set is a
// full/serve surface (GAP-UX-08), so `help docs` reports it as unavailable
// rather than linking the generated documentation strings.
func helpDocsCommand(_ []string, _, stderr io.Writer) int {
	sessionsPrintf(stderr, "help docs: unavailable in minimal build\n")
	return 2
}
