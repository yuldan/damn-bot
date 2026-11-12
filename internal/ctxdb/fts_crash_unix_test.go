//go:build unix

package ctxdb

import (
	"os"
	"syscall"
)

// crashSelf kills the helper process without running deferred cleanup,
// matching the kill -9 half of TestFTSAutoUpgrade.
func crashSelf() {
	if err := syscall.Kill(os.Getpid(), syscall.SIGKILL); err != nil {
		return
	}
}
