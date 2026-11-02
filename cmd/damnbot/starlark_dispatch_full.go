//go:build !minimal

package main

import "io"

func tryStarlark(args []string, stdout, stderr io.Writer) (int, bool) {
	if len(args) == 0 || args[0] != "__starlark" {
		return 0, false
	}
	return runStarlark(args[1:], stdout, stderr), true
}
