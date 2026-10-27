//go:build windows

package update

import (
	"fmt"
	"os"

	"golang.org/x/sys/windows"
)

func lockFile(f *os.File) (func() error, error) {
	var ov windows.Overlapped
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ov)
	if err != nil {
		if err == windows.ERROR_LOCK_VIOLATION {
			return nil, ErrLocked
		}
		return nil, fmt.Errorf("update: lock: %w", err)
	}
	return func() error {
		if err := windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &ov); err != nil {
			return fmt.Errorf("update: unlock: %w", err)
		}
		return nil
	}, nil
}
