//go:build !windows

package journal

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"syscall"
)

// oNoFollow refuses a symlink in the final component of a Root open.
const oNoFollow = syscall.O_NOFOLLOW

// syncDir makes a rename/remove durable (POSIX). os.Root keeps the path
// relative to the sandbox-resolved directory (gosec G304).
func syncDir(dir string) error {
	root, err := os.OpenRoot(dir)
	if err != nil {
		return fmt.Errorf("journal: open dir: %w", err)
	}
	f, err := root.Open(".")
	if err != nil {
		return errors.Join(fmt.Errorf("journal: open dir: %w", err), root.Close())
	}
	return errors.Join(f.Sync(), f.Close(), root.Close())
}

func hardLinks(fi fs.FileInfo) uint64 {
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return 1
	}
	return uint64(st.Nlink)
}
