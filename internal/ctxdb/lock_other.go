//go:build !unix

package ctxdb

import (
	"fmt"
	"os"
)

// Windows LockFileEx support is F1 work; the spike never grants a lock here,
// so every barrier operation fails closed with db_busy.
func tryLock(*os.Root, string, bool) (*os.File, error) {
	return nil, fmt.Errorf("ctxdb: file locks unsupported: %w", ErrDBBusy)
}

func unlockFile(f *os.File) error { return wrap(f.Close()) }
