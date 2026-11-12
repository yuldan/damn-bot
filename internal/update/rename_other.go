//go:build !windows

package update

import "os"

// renameWithRetry renames once off Windows: no sharing-violation class exists.
func renameWithRetry(oldpath, newpath string) error { return os.Rename(oldpath, newpath) }
