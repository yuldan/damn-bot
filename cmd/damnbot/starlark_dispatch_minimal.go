//go:build minimal

package main

import "io"

func tryStarlark([]string, io.Writer, io.Writer) (int, bool) { return 0, false }
