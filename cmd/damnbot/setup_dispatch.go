//go:build !minimal

package main

import (
	"io"
)

func setupDispatch(args []string, stdout, stderr io.Writer) (bool, int) {
	if len(args) == 0 {
		return false, 0
	}
	switch args[0] {
	case "completion":
		return true, setupCompletion(args[1:], stdout, stderr)
	case "secrets":
		return true, setupSecrets(args[1:], stdout, stderr)
	case "auth":
		return true, setupAuth(args[1:], stdout, stderr)
	}
	return false, 0
}
