//go:build !unix && !windows

// Transport isolation is defined for the supported OS families only (SPEC
// §13.3 names Linux/macOS/Windows). On other platforms the transport uses the
// inherited descriptors unchanged.
package main

import "os"

func isolateACPStdio(string) (*os.File, *os.File, *os.File, error) {
	return os.Stdin, os.Stdout, nil, nil
}
