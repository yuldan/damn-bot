//go:build unix && !linux && !darwin

package codetools

import "os"

// copyXattrs: other Unix targets are not release targets (E-39); attributes
// are reported as not preserved (P14).
func copyXattrs(_, _ *os.File) bool { return false }
