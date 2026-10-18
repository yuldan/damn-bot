//go:build !linux

package codetools

import "testing"

// setXattr: the suite checks xattr preservation on Linux only.
func setXattr(*testing.T, string) {}
