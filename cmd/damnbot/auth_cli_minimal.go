//go:build minimal

package main

import (
	"io"
)

// setupAuth is absent from the minimal build: OAuth transport, token store
// and the CLI are excluded to keep the binary under P13. Refuse clearly
// instead of advertising a command that cannot run.
func setupAuth(_ []string, _ io.Writer, stderr io.Writer) int {
	say(stderr, "setup auth: auth tersedia hanya di build penuh (minimal tidak menautkan OAuth)\n")
	return 2
}
