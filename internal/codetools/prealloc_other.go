//go:build !linux

package codetools

import "os"

// preallocate: no portable reserve-without-resize call; an in-place
// rewrite that runs out of space is undone from its backup.
func preallocate(*os.File, int64) error { return nil }
