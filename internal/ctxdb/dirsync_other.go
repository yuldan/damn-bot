//go:build !windows

package ctxdb

import (
	"fmt"
	"os"
)

// syncDirFile fsyncs an open directory so a rename/create inside it is
// durable (POSIX: the directory entry lives in the directory's data).
func syncDirFile(f *os.File) error {
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync dir: %w", err)
	}
	return nil
}
