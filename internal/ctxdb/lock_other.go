//go:build !unix

package ctxdb

import "os"

// Windows LockFileEx support is F1 work; the spike never grants a lock here,
// so every barrier operation fails closed with db_busy.
func tryLock(string, bool) (*os.File, bool) { return nil, false }

func unlockFile(f *os.File) { _ = f.Close() }
